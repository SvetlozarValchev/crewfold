package daemon

import (
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/gitstate"
)

const (
	maximumWorkbenchGitCheckouts = 4
	maximumWorkbenchDirtyPaths   = 128
)

type workbenchGitObservation struct {
	CheckoutID     string   `json:"checkout_id"`
	Availability   string   `json:"availability"`
	Branch         string   `json:"branch,omitempty"`
	HeadCommit     string   `json:"head_commit,omitempty"`
	Dirty          bool     `json:"dirty"`
	DirtyPaths     []string `json:"dirty_paths"`
	OmittedPaths   int      `json:"omitted_paths"`
	Truncated      bool     `json:"truncated"`
	ObservedAt     string   `json:"observed_at"`
	DiagnosticCode string   `json:"diagnostic_code,omitempty"`
	Diagnostic     string   `json:"diagnostic,omitempty"`
}

// handleWorkbenchGitObservation performs a bounded, read-only observation. It
// deliberately returns neither checkout paths nor Git metadata directories and
// never persists source text or diff content in the browser transport.
func (w *workbenchServer) handleWorkbenchGitObservation(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "workbench Git observation requires GET")
		return
	}
	workspace := strings.TrimSpace(request.URL.Query().Get("workspace"))
	project := strings.TrimSpace(request.URL.Query().Get("project"))
	if workspace == "" || project == "" || len(workspace) > 128 || len(project) > 128 {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "workbench Git observation requires exact workspace and project")
		return
	}
	inspection, err := w.daemon.store.InspectProject(request.Context(), workspace, project)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	if len(inspection.Checkouts) > maximumWorkbenchGitCheckouts {
		w.writeError(response, http.StatusConflict, "scope_too_large", "project exceeds the bounded workbench checkout observation limit")
		return
	}
	observed := make([]workbenchGitObservation, 0, len(inspection.Checkouts))
	for _, checkout := range inspection.Checkouts {
		value := workbenchGitObservation{CheckoutID: checkout.ID, Availability: domain.CheckoutUnavailable, DirtyPaths: []string{}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		observation, inspectErr := w.daemon.gitInspector.Inspect(request.Context(), checkout.Path)
		if inspectErr != nil {
			value.DiagnosticCode = gitstate.ErrorCode(inspectErr)
			value.Diagnostic = boundedWorkbenchObservationText(inspectErr.Error(), 1024)
			observed = append(observed, value)
			continue
		}
		value.Availability = observation.Availability
		value.Branch = boundedWorkbenchObservationText(observation.Branch, 1024)
		value.HeadCommit = observation.HeadCommit
		value.Dirty = observation.Dirty
		paths := observation.DirtyPaths
		if len(paths) > maximumWorkbenchDirtyPaths {
			value.OmittedPaths = len(paths) - maximumWorkbenchDirtyPaths
			value.Truncated = true
			paths = paths[:maximumWorkbenchDirtyPaths]
		}
		for _, path := range paths {
			value.DirtyPaths = append(value.DirtyPaths, boundedWorkbenchObservationText(path, 1024))
		}
		observed = append(observed, value)
	}
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:git-observation:v1",
		"type":   "git_observation", "project_id": inspection.Project.ID,
		"observations": observed,
	})
}

func boundedWorkbenchObservationText(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' || char >= '\u202a' && char <= '\u202e' || char >= '\u2066' && char <= '\u2069' {
			return '�'
		}
		return char
	}, value)
	for len(value) > maximum {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}
