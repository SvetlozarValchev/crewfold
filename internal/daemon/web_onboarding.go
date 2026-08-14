package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/gitstate"
	"crewfold/internal/store"
)

const maximumWorkbenchOnboardingBytes = 16 * 1024

var workbenchNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type workbenchOnboardingRequest struct {
	RepositoryPath string `json:"repository_path"`
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	Agent          string `json:"agent"`
	Provider       string `json:"provider"`
	Runtime        string `json:"runtime"`
	WriteMode      string `json:"write_mode"`
}

type workbenchProviderDiagnosis struct {
	Provider     string   `json:"provider"`
	Status       string   `json:"status"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities"`
}

func (w *workbenchServer) handleWorkbenchOnboarding(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "workbench onboarding") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumWorkbenchOnboardingBytes))
	if err != nil {
		w.writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "onboarding request exceeds the bounded body limit")
		return
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "onboarding request is not exact JSON")
		return
	}
	var params workbenchOnboardingRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) ||
		!workbenchNamePattern.MatchString(params.Workspace) || !workbenchNamePattern.MatchString(params.Project) || !workbenchNamePattern.MatchString(params.Agent) ||
		!validWorkbenchText(params.Provider, 128) || !validWorkbenchText(params.Runtime, 128) {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "onboarding request does not match the current exact path and naming contract")
		return
	}
	resolvedPath, ok := resolveWorkbenchRepositoryPath(params.RepositoryPath)
	if !ok {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "onboarding repository path is not an exact absolute path or owner-home path")
		return
	}
	params.RepositoryPath = resolvedPath
	if params.WriteMode == "" {
		params.WriteMode = domain.WriteModeShared
	}
	if params.WriteMode != domain.WriteModeExclusive && params.WriteMode != domain.WriteModeClaimed && params.WriteMode != domain.WriteModeShared && params.WriteMode != domain.WriteModeReadOnly {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "onboarding write mode is not part of the current contract")
		return
	}
	provider, exists := w.daemon.providers[params.Provider]
	if !exists {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected provider is not registered"})
		return
	}
	runtimeDriver, exists := w.daemon.runtimes[params.Runtime]
	if !exists {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected runtime is not registered"})
		return
	}
	if err := preflightWorkbenchRuntime(request.Context(), params.Runtime, runtimeDriver); err != nil {
		w.writeStoreError(response, err)
		return
	}
	probeContext, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	diagnosis, err := diagnoseWorkbenchProvider(probeContext, provider)
	cancel()
	if err != nil {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: err.Error()})
		return
	}
	observation, err := w.daemon.gitInspector.Inspect(request.Context(), params.RepositoryPath)
	if err != nil {
		w.writeError(response, http.StatusBadRequest, gitstate.ErrorCode(err), err.Error())
		return
	}
	requestJSON, err := json.Marshal(params)
	if err != nil {
		w.writeError(response, http.StatusInternalServerError, "onboarding_failed", "could not seal the onboarding operation")
		return
	}
	digest := sha256.Sum256(requestJSON)
	operation := "web-onboard-" + hex.EncodeToString(digest[:12])
	workspace, err := w.daemon.store.InitWorkspace(request.Context(), store.InitWorkspaceCommand{Name: params.Workspace, IdempotencyKey: operation + "-workspace", CorrelationID: operation})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	project, err := w.daemon.store.RegisterProject(request.Context(), store.RegisterProjectCommand{WorkspaceIdentifier: workspace.Workspace.ID, Name: params.Project, WriteMode: params.WriteMode, Observation: observation, IdempotencyKey: operation + "-project", CorrelationID: operation})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	agent, err := w.daemon.store.CreateAgent(request.Context(), store.CreateAgentCommand{WorkspaceIdentifier: workspace.Workspace.ID, Name: params.Agent, Role: "implementation", Provider: params.Provider, Runtime: params.Runtime, MaxConcurrency: 2, IdempotencyKey: operation + "-agent", CorrelationID: operation})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:onboarding:v1", "type": "workbench_onboarding", "status": "completed",
		"workspace": workspace.Workspace, "project": project.Project, "checkout": project.Checkout, "agent": agent.Value,
		"provider_diagnosis": diagnosis, "repository": map[string]any{"branch": observation.Branch, "dirty": observation.Dirty, "dirty_path_count": len(observation.DirtyPaths)},
	})
}

func preflightWorkbenchRuntime(ctx context.Context, name string, runtimeDriver execution.RuntimeDriver) error {
	if name != "herdr" {
		return nil
	}
	probe, ok := runtimeDriver.(execution.RuntimeReadinessProbe)
	if !ok {
		return &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected Herdr runtime cannot prove its live host"}
	}
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := probe.CheckReady(probeContext); err != nil {
		return &store.Error{Code: store.CodeAdapterUnavailable, Message: "Herdr interactive runtime is not ready: " + err.Error() + "; run `crewfold service install` to start its companion service, then retry"}
	}
	return nil
}

func diagnoseWorkbenchProvider(ctx context.Context, provider execution.ProviderAdapter) (workbenchProviderDiagnosis, error) {
	switch value := provider.(type) {
	case execution.CodexProvider:
		report := value.Probe(ctx)
		if err := report.Error(); err != nil {
			return workbenchProviderDiagnosis{}, err
		}
		return workbenchProviderDiagnosis{Provider: report.Provider, Status: report.Status, Version: report.Version, Capabilities: append([]string(nil), report.Capabilities...)}, nil
	case execution.ClaudeProvider:
		report := value.Probe(ctx)
		if err := report.Error(); err != nil {
			return workbenchProviderDiagnosis{}, err
		}
		return workbenchProviderDiagnosis{Provider: report.Provider, Status: report.Status, Version: report.Version, Capabilities: append([]string(nil), report.Capabilities...)}, nil
	default:
		return workbenchProviderDiagnosis{Provider: provider.Name(), Status: "ok", Capabilities: []string{"headless_execution", "scoped_mcp"}}, nil
	}
}

func exactWorkbenchPath(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func resolveWorkbenchRepositoryPath(value string) (string, bool) {
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value, exactWorkbenchPath(value)
}

func validWorkbenchText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
