package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestM21OwnerCrewConfigurationReplacesExactExecutiveAuthorityAndReplays(t *testing.T) {
	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	bootstrap, err := api.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, httpClient, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"repository_path": filepath.Join(t.TempDir(), "crew-project"), "workspace": "personal", "project": "crew-project",
		"agent": "builder", "provider": "fake", "runtime": "fake", "write_mode": "shared",
	})
	request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/onboarding", bytes.NewReader(body))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Crewfold-CSRF", session.CSRF)
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("onboarding = %d: %s", response.StatusCode, raw)
	}
	var onboarded struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Objective struct {
			ID string `json:"id"`
		} `json:"executive_objective"`
		Binding struct {
			Revision int64 `json:"revision"`
		} `json:"executive_binding"`
	}
	if err := json.Unmarshal(raw, &onboarded); err != nil {
		t.Fatal(err)
	}
	_, err = api.OwnerCrewConfigure(context.Background(), localapi.OwnerCrewConfigureParams{
		Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID, Action: "disable",
		ExpectedBindingRevision: onboarded.Binding.Revision, Agent: onboarded.Agent.ID, IdempotencyKey: "owner-crew-disable-final-worker",
	})
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != store.CodeInvalidAgent {
		t.Fatalf("disable final implementation worker error = %v, want %s", err, store.CodeInvalidAgent)
	}
	addParams := localapi.OwnerCrewConfigureParams{
		Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID, Action: "add",
		ExpectedBindingRevision: onboarded.Binding.Revision, Name: "reviewer", Provider: "fake", Runtime: "fake",
		MaxConcurrency: 2, IdempotencyKey: "owner-crew-add-reviewer",
	}
	added, err := api.OwnerCrewConfigure(context.Background(), addParams)
	if err != nil {
		t.Fatal(err)
	}
	if added.Action != "add" || added.Agent.Name != "reviewer" || added.Binding.Revision != 2 || len(added.WorkerProfiles) != 2 {
		t.Fatalf("added crew = %#v", added)
	}
	projectRuns, err := api.RunList(context.Background(), localapi.RunListParams{Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID})
	workerRuns := 0
	for _, run := range projectRuns.Runs {
		if run.AgentID == added.Agent.ID {
			workerRuns++
		}
	}
	if err != nil || workerRuns != 0 {
		t.Fatalf("adding a worker started %d runs in %#v: %v", workerRuns, projectRuns.Runs, err)
	}
	replay, err := api.OwnerCrewConfigure(context.Background(), addParams)
	if err != nil || !reflect.DeepEqual(replay, added) {
		t.Fatalf("add replay = %#v, %v; want %#v", replay, err, added)
	}
	ownedTask, err := api.TaskCreate(context.Background(), localapi.TaskCreateParams{
		Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID, Objective: onboarded.Objective.ID,
		Title: "Retained reviewer work", Priority: 1, Budget: domain.Budget{TokenLimit: 100, CostCents: 1, TimeSeconds: 60},
		IdempotencyKey: "owner-crew-retained-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.TaskAssign(context.Background(), localapi.TaskAssignParams{
		Workspace: onboarded.Workspace.ID, Task: ownedTask.Detail.Task.ID, Agent: added.Agent.ID,
		LeaseSeconds: 300, ExpectedRevision: ownedTask.Detail.Task.Revision, IdempotencyKey: "owner-crew-retained-assignment",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = api.OwnerCrewConfigure(context.Background(), localapi.OwnerCrewConfigureParams{
		Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID, Action: "disable",
		ExpectedBindingRevision: added.Binding.Revision, Agent: added.Agent.ID, IdempotencyKey: "owner-crew-disable-busy-reviewer",
	})
	if !errors.As(err, &apiError) || apiError.Code != store.CodeInvalidAgent {
		t.Fatalf("disable worker with retained work error = %v, want %s", err, store.CodeInvalidAgent)
	}
	unchanged, err := api.OwnerCrewConfigure(context.Background(), addParams)
	if err != nil || unchanged.Binding.Revision != added.Binding.Revision || !unchanged.Agent.Enabled {
		t.Fatalf("failed disable changed crew authority = %#v, %v", unchanged, err)
	}
	disableParams := localapi.OwnerCrewConfigureParams{
		Workspace: onboarded.Workspace.ID, Project: onboarded.Project.ID, Action: "disable",
		ExpectedBindingRevision: added.Binding.Revision, Agent: onboarded.Agent.ID,
		IdempotencyKey: "owner-crew-disable-builder",
	}
	disabled, err := api.OwnerCrewConfigure(context.Background(), disableParams)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Action != "disable" || disabled.Agent.Enabled || disabled.Binding.Revision != 3 || len(disabled.WorkerProfiles) != 1 || disabled.WorkerProfiles[0].AgentID != added.Agent.ID {
		t.Fatalf("disabled crew = %#v", disabled)
	}
	disableReplay, err := api.OwnerCrewConfigure(context.Background(), disableParams)
	if err != nil || disableReplay.Binding.ID != disabled.Binding.ID || disableReplay.Binding.Revision != disabled.Binding.Revision || disableReplay.Agent.Enabled {
		t.Fatalf("disable replay = %#v, %v; want binding %#v", disableReplay, err, disabled.Binding)
	}
	grant, err := api.ManagerGrantShow(context.Background(), onboarded.Workspace.ID, disabled.Binding.ManagerGrantID)
	if err != nil || len(grant.Grant.LaunchProfiles) != 1 || grant.Grant.LaunchProfiles[0].AgentID != added.Agent.ID {
		t.Fatalf("replacement grant = %#v, %v", grant, err)
	}
}
