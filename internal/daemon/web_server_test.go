package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
	protocolschema "crewfold/protocol"

	"github.com/gorilla/websocket"
)

func TestM21WorkbenchBootstrapIsSingleUseAndStatusIsAuthenticated(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatalf("WebBootstrap() error = %v", err)
	}
	parsed, err := url.Parse(bootstrap.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	token := parsed.Fragment
	if !strings.HasPrefix(token, "bootstrap=") {
		t.Fatalf("fragment = %q", token)
	}
	token = strings.TrimPrefix(token, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	unauthenticated, err := client.Get(origin + "/api/v1/session/" + strings.Repeat("0", 64) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.StatusCode)
	}
	unauthenticatedRaw, err := io.ReadAll(unauthenticated.Body)
	_ = unauthenticated.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := protocolschema.ValidateJSON("web/v1/error.response.schema.json", unauthenticatedRaw); err != nil {
		t.Fatalf("unauthenticated error schema = %v; response = %s", err, unauthenticatedRaw)
	}

	sessionRaw := exchangeWorkbenchBootstrap(t, client, origin, token, origin)
	if err := protocolschema.ValidateJSON("web/v1/session.response.schema.json", sessionRaw); err != nil {
		t.Fatalf("session response schema error = %v; response = %s", err, sessionRaw)
	}
	var session struct {
		APIBase string `json:"api_base"`
	}
	if err := json.Unmarshal(sessionRaw, &session); err != nil || !strings.HasPrefix(session.APIBase, "/api/v1/session/") {
		t.Fatalf("session response = %s error = %v", sessionRaw, err)
	}
	originURL, _ := url.Parse(origin + "/")
	if cookies := jar.Cookies(originURL); len(cookies) != 0 {
		t.Fatalf("owner cookie leaked to loopback root path: %#v", cookies)
	}

	statusResponse, err := client.Get(origin + session.APIBase + "/status")
	if err != nil {
		t.Fatal(err)
	}
	statusRaw, err := io.ReadAll(statusResponse.Body)
	_ = statusResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d: %s", statusResponse.StatusCode, statusRaw)
	}
	if err := protocolschema.ValidateJSON("web/v1/status.response.schema.json", statusRaw); err != nil {
		t.Fatalf("status response schema error = %v; response = %s", err, statusRaw)
	}

	replayRequest, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	replayRequest.Header.Set("Origin", origin)
	replayRequest.Header.Set("Content-Type", "application/json")
	replayResponse, err := (&http.Client{}).Do(replayRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap replay status = %d, want 401", replayResponse.StatusCode)
	}
}

func TestM21WorkbenchRejectsOriginHostAndUnknownSessionFields(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)

	for name, mutate := range map[string]func(*http.Request){
		"wrong origin": func(request *http.Request) { request.Header.Set("Origin", "http://attacker.invalid") },
		"wrong host":   func(request *http.Request) { request.Host = "localhost:1" },
	} {
		t.Run(name, func(t *testing.T) {
			bootstrap, err := client.WebBootstrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(bootstrap.URL)
			token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
			parsed.Fragment = ""
			origin := strings.TrimSuffix(parsed.String(), "/")
			request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
			mutate(request)
			response, err := (&http.Client{}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusMisdirectedRequest {
				t.Fatalf("response status = %d", response.StatusCode)
			}
		})
	}

	bootstrap, err := client.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`","extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, want 400", response.StatusCode)
	}
}

func TestM21WorkbenchRPCRequiresExactCSRFAndExposesOnlyOwnerMethods(t *testing.T) {
	t.Parallel()
	if !workbenchMethodAllowed(localapi.MethodLaunchProfileList) {
		t.Fatal("owner workbench must expose launch_profile.list for canonical onboarding refresh and plan editing")
	}

	running := startTestServer(t, testConfig(t))
	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(bootstrap.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	raw := exchangeWorkbenchBootstrap(t, client, origin, token, origin)
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}

	call := func(method, csrf string) (*http.Response, []byte) {
		t.Helper()
		body := `{"id":"web-test","method":"` + method + `","params":{"limit":1}}`
		request, err := http.NewRequest(http.MethodPost, origin+session.APIBase+"/rpc", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Crewfold-CSRF", csrf)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response, result
	}

	response, result := call(localapi.MethodWorkspaceList, session.CSRF)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("workspace RPC status = %d: %s", response.StatusCode, result)
	}
	if err := protocolschema.ValidateJSON("local/v1/response.schema.json", result); err != nil {
		t.Fatalf("workspace RPC response schema = %v: %s", err, result)
	}
	var envelope localapi.Response
	if err := json.Unmarshal(result, &envelope); err != nil || envelope.Error != nil || len(envelope.Result) == 0 {
		t.Fatalf("workspace RPC envelope = %#v, error = %v", envelope, err)
	}

	response, result = call(localapi.MethodWorkspaceList, strings.Repeat("0", 64))
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong CSRF status = %d: %s", response.StatusCode, result)
	}
	response, result = call(localapi.MethodStop, session.CSRF)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("system.stop RPC status = %d: %s", response.StatusCode, result)
	}
}

func TestM21OwnerIntentCommitsReceiptedWorkAndAutomaticallyReviewsWorkerReport(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	config.OwnerInterpreter = m21OwnerLoopInterpreter{}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	workspace, err := api.WorkspaceInit(context.Background(), "personal", "web-intent-workspace")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "world-engine")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := api.ProjectAdd(context.Background(), workspace.Workspace.ID, "world-engine", root, domain.WriteModeShared, "web-intent-project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := api.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: workspace.Workspace.ID, Name: "builder", Role: "implementation", Provider: "fake", Runtime: "fake", MaxConcurrency: 2, IdempotencyKey: "web-intent-agent"})
	if err != nil {
		t.Fatal(err)
	}
	reviewScenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-workbench-manager-review", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "Implementation is underway", WaitForResume: true}}}
	if _, err := api.LaunchProfileCreate(context.Background(), localapi.LaunchProfileCreateParams{Workspace: workspace.Workspace.ID, Project: project.Project.ID, Agent: agent.Agent.ID, ExpectedAgentRevision: agent.Agent.Revision, Purpose: "implementation", Runtime: agent.Agent.Runtime, Provider: agent.Agent.Provider, Checkout: project.Checkout.ID, Scenario: reviewScenario, AssignmentLeaseSeconds: 3600, CapabilityTTLSeconds: 3600, IdempotencyKey: "web-intent-profile"}); err != nil {
		t.Fatal(err)
	}
	policy, err := api.SupervisorPolicyShow(context.Background(), workspace.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.SupervisorPolicyConfigure(context.Background(), localapi.SupervisorPolicyConfigureParams{Workspace: workspace.Workspace.ID, Enabled: true, Limits: policy.Policy.Limits, AutoSchedule: true, AutoRetryLimit: 0, ExpectedRevision: policy.Policy.Revision, IdempotencyKey: "web-intent-supervisor"}); err != nil {
		t.Fatal(err)
	}

	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"workspace": workspace.Workspace.ID, "project": project.Project.ID, "instruction": "Build the first playable world loop",
		"mode": "act", "idempotency_key": "web-owner-act-one",
	})
	call := func() []byte {
		request, err := http.NewRequest(http.MethodPost, origin+session.APIBase+"/intent", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Crewfold-CSRF", session.CSRF)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("owner intent status = %d: %s", response.StatusCode, raw)
		}
		return raw
	}
	first := call()
	second := call()
	if err := protocolschema.ValidateJSON("web/v1/owner-intent.response.schema.json", first); err != nil {
		t.Fatalf("owner intent response schema = %v: %s", err, first)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("owner act replay changed:\nfirst=%s\nsecond=%s", first, second)
	}
	var result struct {
		Detail domain.OwnerTurnDetail `json:"detail"`
	}
	if err := json.Unmarshal(first, &result); err != nil {
		t.Fatal(err)
	}
	if result.Detail.Turn.Status != "completed" || len(result.Detail.Operations) != 3 || len(result.Detail.Receipts) != 3 {
		t.Fatalf("owner intent detail = %#v", result.Detail)
	}
	for _, operation := range result.Detail.Operations {
		if operation.Status != "applied" || operation.EventSequence == 0 {
			t.Fatalf("owner operation = %#v", operation)
		}
	}
	tasks, err := api.TaskList(context.Background(), localapi.TaskListParams{Workspace: workspace.Workspace.ID, Project: project.Project.ID, PageParams: localapi.PageParams{Limit: 10}})
	if err != nil || len(tasks.Tasks) != 1 {
		t.Fatalf("owner-created tasks = %#v, %v", tasks, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var runs localapi.RunListResult
	for time.Now().Before(deadline) {
		runs, err = api.RunList(context.Background(), localapi.RunListParams{Workspace: workspace.Workspace.ID, Project: project.Project.ID, PageParams: localapi.PageParams{Limit: 10}})
		if err == nil && len(runs.Runs) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(runs.Runs) != 1 {
		t.Fatalf("owner-created runs = %#v, %v", runs, err)
	}
	active := waitForRunStatus(t, api, runs.Runs[0].ID, domain.RunActive)
	reporter, err := store.Open(context.Background(), running.config.DataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	progress, err := reporter.SubmitRunReport(context.Background(), store.CreateRunReportCommand{
		RunID: active.Detail.Run.ID, Kind: domain.ObservationProgress,
		Message:        "The implementation boundary is mapped and one owner-facing decision may be needed.",
		Payload:        map[string]any{"completed": []string{"inspected canonical project context"}, "next": []string{"continue implementation"}},
		IdempotencyKey: "web-manager-review-progress",
	})
	if err != nil || progress.ID == "" {
		t.Fatalf("SubmitRunReport(progress) = %#v, %v", progress, err)
	}

	// The fixture worker's progress report must reach the durable manager
	// loop without another owner HTTP turn. The browser reads the resulting
	// manager-originated turn and exact review cursor from the conversation.
	deadline = time.Now().Add(5 * time.Second)
	var conversationRaw []byte
	var conversation struct {
		Turns  []domain.OwnerTurnDetail      `json:"turns"`
		Review *domain.OwnerManagerReviewJob `json:"review"`
	}
	for time.Now().Before(deadline) {
		response, requestErr := client.Get(origin + session.APIBase + "/conversation?workspace=" + url.QueryEscape(workspace.Workspace.ID) + "&project=" + url.QueryEscape(project.Project.ID))
		if requestErr == nil {
			conversationRaw, requestErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if requestErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(conversationRaw, &conversation) == nil && len(conversation.Turns) >= 2 && conversation.Review != nil && conversation.Review.Status == "idle" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := protocolschema.ValidateJSON("web/v1/owner-conversation.response.schema.json", conversationRaw); err != nil {
		t.Fatalf("owner conversation response schema = %v: %s", err, conversationRaw)
	}
	if len(conversation.Turns) != 2 || conversation.Review == nil {
		t.Fatalf("automatic manager conversation = %#v", conversation)
	}
	managerTurn := conversation.Turns[1].Turn
	if managerTurn.Kind != "review" || managerTurn.InitiatedBy != "manager" || managerTurn.TriggerEventSequence < 1 || managerTurn.Interpretation.Disposition != "answer" || managerTurn.Status != "completed" {
		t.Fatalf("automatic manager turn = %#v", managerTurn)
	}
	if conversation.Review.RequestedEventSequence != conversation.Review.ReviewedEventSequence || conversation.Review.LastTurnID != managerTurn.ID || conversation.Review.RequestedEventSequence != managerTurn.TriggerEventSequence {
		t.Fatalf("automatic manager cursor = %#v, turn = %#v", conversation.Review, managerTurn)
	}
}

func TestM21WorkbenchOnboardingPreflightsBeforeMutationAndReplaysExactly(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
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
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	call := func(provider string) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"repository_path": filepath.Join(t.TempDir(), "world-engine"), "workspace": "personal", "project": "world-engine",
			"agent": "builder", "provider": provider, "runtime": "direct", "write_mode": "shared",
		})
		request, err := http.NewRequest(http.MethodPost, origin+session.APIBase+"/onboarding", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Crewfold-CSRF", session.CSRF)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, raw
	}
	status, raw := call("missing-provider")
	if status != http.StatusConflict {
		t.Fatalf("invalid-provider onboarding = %d: %s", status, raw)
	}
	workspaces, err := api.WorkspaceList(context.Background(), localapi.WorkspaceListParams{PageParams: localapi.PageParams{Limit: 10}})
	if err != nil || len(workspaces.Workspaces) != 0 {
		t.Fatalf("preflight failure workspaces = %#v, %v", workspaces, err)
	}

	// Use one stable path for the exact replay request.
	path := filepath.Join(t.TempDir(), "world-engine")
	body, _ := json.Marshal(map[string]any{"repository_path": path, "workspace": "personal", "project": "world-engine", "agent": "builder", "provider": "fixture-mcp", "runtime": "direct", "write_mode": "shared"})
	validCall := func() []byte {
		request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/onboarding", bytes.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Crewfold-CSRF", session.CSRF)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("valid onboarding = %d: %s", response.StatusCode, raw)
		}
		return raw
	}
	first, replay := validCall(), validCall()
	if err := protocolschema.ValidateJSON("web/v1/onboarding.response.schema.json", first); err != nil {
		t.Fatalf("onboarding response schema = %v: %s", err, first)
	}
	if !bytes.Equal(first, replay) {
		t.Fatalf("onboarding replay changed:\nfirst=%s\nreplay=%s", first, replay)
	}
	workspaces, err = api.WorkspaceList(context.Background(), localapi.WorkspaceListParams{PageParams: localapi.PageParams{Limit: 10}})
	if err != nil || len(workspaces.Workspaces) != 1 {
		t.Fatalf("onboarded workspaces = %#v, %v", workspaces, err)
	}
}

func TestM21WorkbenchGitObservationIsFreshBoundedAndSchemaValid(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	paths := make([]string, 140)
	// Stable unique, sorted values model an intentionally noisy checkout.
	for index := range paths {
		paths[index] = filepath.ToSlash(filepath.Join("src", "generated", fmt.Sprintf("file-%03d.go", index)))
	}
	config.GitInspector = fixedWorkbenchGitInspector{observation: domain.CheckoutObservation{
		Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
		Branch: "feature/workbench", HeadCommit: strings.Repeat("3", 40), Dirty: true, DirtyPaths: paths,
		Repository: domain.RepositoryObservation{Fingerprint: "git_" + strings.Repeat("4", 64), ObjectFormat: "sha1", RootCommits: []string{strings.Repeat("5", 40)}},
	}}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	workspace, err := api.WorkspaceInit(context.Background(), "personal", "web-git-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := api.ProjectAdd(context.Background(), workspace.Workspace.ID, "world-engine", filepath.Join(t.TempDir(), "world-engine"), domain.WriteModeShared, "web-git-project")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := api.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin + session.APIBase + "/git?workspace=" + url.QueryEscape(workspace.Workspace.ID) + "&project=" + url.QueryEscape(project.Project.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Git observation status = %d: %s", response.StatusCode, raw)
	}
	if err := protocolschema.ValidateJSON("web/v1/git-observation.response.schema.json", raw); err != nil {
		t.Fatalf("Git observation schema = %v: %s", err, raw)
	}
	var result struct {
		Observations []workbenchGitObservation `json:"observations"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Observations) != 1 {
		t.Fatalf("Git observation = %#v, %v", result, err)
	}
	if value := result.Observations[0]; len(value.DirtyPaths) != maximumWorkbenchDirtyPaths || value.OmittedPaths != 12 || !value.Truncated || value.Branch != "feature/workbench" {
		t.Fatalf("bounded Git observation = %#v", value)
	}
	if strings.Contains(string(raw), project.Checkout.Path) || strings.Contains(string(raw), ".git") {
		t.Fatalf("Git observation leaked checkout or metadata path: %s", raw)
	}
}

func TestM21WorkbenchTerminalGrantIsRunBoundSingleUseAndInteractive(t *testing.T) {
	t.Parallel()

	runtimeDriver := &m21TerminalRuntime{FakeRuntime: execution.NewFakeRuntime()}
	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": runtimeDriver}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, api, t.TempDir())
	task := createAssignedRunWorkerTask(t, api, project.Project.ID, agent.Agent.ID, "web terminal")
	created, err := api.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "web-terminal", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting", WaitForResume: true}}},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "web-terminal-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	active := waitForRunStatus(t, api, created.Detail.Run.ID, domain.RunActive)

	bootstrap, err := api.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	grantBody, _ := json.Marshal(map[string]string{"workspace": "personal", "run": active.Detail.Run.ID})
	grantRequest, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/terminal-grant", bytes.NewReader(grantBody))
	grantRequest.Header.Set("Origin", origin)
	grantRequest.Header.Set("Content-Type", "application/json")
	grantRequest.Header.Set("X-Crewfold-CSRF", session.CSRF)
	grantResponse, err := client.Do(grantRequest)
	if err != nil {
		t.Fatal(err)
	}
	grantRaw, _ := io.ReadAll(grantResponse.Body)
	_ = grantResponse.Body.Close()
	if grantResponse.StatusCode != http.StatusOK {
		t.Fatalf("terminal grant = %d: %s", grantResponse.StatusCode, grantRaw)
	}
	if err := protocolschema.ValidateJSON("web/v1/terminal-grant.response.schema.json", grantRaw); err != nil {
		t.Fatalf("terminal grant schema = %v: %s", err, grantRaw)
	}
	var grant struct {
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(grantRaw, &grant); err != nil {
		t.Fatal(err)
	}
	websocketURL := "ws" + strings.TrimPrefix(origin, "http") + session.APIBase + "/terminal"
	headers := http.Header{"Origin": []string{origin}}
	cookieURL, _ := url.Parse(origin + session.APIBase + "/terminal")
	for _, cookie := range jar.Cookies(cookieURL) {
		headers.Add("Cookie", cookie.String())
	}
	dialer := websocket.Dialer{Subprotocols: []string{grant.Protocol}}
	connection, response, err := dialer.Dial(websocketURL, headers)
	if err != nil {
		if response != nil {
			t.Fatalf("terminal WebSocket = %d, %v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.Subprotocol() != grant.Protocol {
		t.Fatalf("terminal subprotocol = %q", connection.Subprotocol())
	}
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, ready, err := connection.ReadMessage()
	if err != nil || !bytes.Contains(ready, []byte("terminal-ready")) {
		t.Fatalf("terminal initial output = %q, %v", ready, err)
	}
	if err := connection.WriteJSON(workbenchTerminalClientMessage{Type: "input", Data: "hello\n"}); err != nil {
		t.Fatal(err)
	}
	var output []byte
	for !bytes.Contains(output, []byte("terminal-echo:hello")) {
		_, chunk, err := connection.ReadMessage()
		if err != nil {
			t.Fatalf("read terminal echo = %q, %v", output, err)
		}
		output = append(output, chunk...)
	}

	replayDialer := websocket.Dialer{Subprotocols: []string{grant.Protocol}}
	if replay, replayResponse, replayErr := replayDialer.Dial(websocketURL, headers); replayErr == nil {
		replay.Close()
		t.Fatal("consumed terminal grant was replayed")
	} else if replayResponse == nil || replayResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal grant replay response = %#v, %v", replayResponse, replayErr)
	}
}

type m21WorkbenchInspector struct{}

// m21OwnerLoopInterpreter keeps the daemon integration proof provider-free
// while exercising the same untrusted interpretation boundary as Codex. The
// first owner turn freezes one task; the autonomous review consumes the worker
// report and returns a manager-originated update without any new effect.
type m21OwnerLoopInterpreter struct{}

func (m21OwnerLoopInterpreter) Interpret(_ context.Context, request execution.OwnerInterpretationRequest) (domain.OwnerInterpretation, error) {
	var snapshot struct {
		LaunchProfiles []struct {
			ID string `json:"id"`
		} `json:"launch_profiles"`
	}
	if err := json.Unmarshal(request.CanonicalContext, &snapshot); err != nil || len(snapshot.LaunchProfiles) == 0 {
		return domain.OwnerInterpretation{}, errors.New("owner loop fixture received an invalid canonical snapshot")
	}
	if request.Kind == "review" {
		return domain.OwnerInterpretation{
			Disposition: "answer", Summary: "Reviewed the new worker report.",
			Answer:          "The worker mapped the implementation boundary and is continuing without an owner decision.",
			ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{},
		}, nil
	}
	return domain.OwnerInterpretation{
		Disposition: "ready", Summary: "Prepared one exact integration task.", ObjectiveTitle: request.Instruction,
		ObjectiveBudget: domain.Budget{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{},
		Tasks: []domain.OwnerPlanTask{{Key: "implementation", Title: request.Instruction, Description: request.Instruction, Priority: 500, Budget: domain.Budget{}, LaunchProfileID: snapshot.LaunchProfiles[0].ID, DependsOn: []string{}}},
	}, nil
}

func (m21WorkbenchInspector) Inspect(_ context.Context, path string) (domain.CheckoutObservation, error) {
	return domain.CheckoutObservation{
		Path: path, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
		Branch: "main", HeadCommit: strings.Repeat("2", 40), DirtyPaths: []string{}, GitDir: filepath.Join(path, ".git"), GitCommonDir: filepath.Join(path, ".git"),
		Repository: domain.RepositoryObservation{Fingerprint: "git_" + strings.Repeat("1", 64), ObjectFormat: "sha1", RootCommits: []string{strings.Repeat("0", 40)}},
	}, nil
}

type fixedWorkbenchGitInspector struct{ observation domain.CheckoutObservation }

func (value fixedWorkbenchGitInspector) Inspect(_ context.Context, path string) (domain.CheckoutObservation, error) {
	result := value.observation
	result.Path = path
	result.GitDir = filepath.Join(path, ".git")
	result.GitCommonDir = result.GitDir
	return result, nil
}

type m21TerminalRuntime struct{ *execution.FakeRuntime }

func (runtime *m21TerminalRuntime) Attach(context.Context, string, string) (execution.AttachSpec, error) {
	return execution.AttachSpec{Executable: "/bin/sh", Arguments: []string{"-c", `printf 'terminal-ready\n'; IFS= read -r line; printf 'terminal-echo:%s\n' "$line"; sleep 1`}}, nil
}

type m21ReadinessRuntime struct {
	*execution.FakeRuntime
	readyErr error
}

func (runtime *m21ReadinessRuntime) Name() string { return "herdr" }
func (runtime *m21ReadinessRuntime) CheckReady(context.Context) error {
	return runtime.readyErr
}

func TestM21HerdrOnboardingRequiresLiveHostBeforeCanonicalMutation(t *testing.T) {
	t.Parallel()

	runtimeDriver := &m21ReadinessRuntime{FakeRuntime: execution.NewFakeRuntime(), readyErr: errors.New("server_not_running")}
	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"herdr": runtimeDriver}
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
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"repository_path": filepath.Join(t.TempDir(), "signal-garden"), "workspace": "personal", "project": "signal-garden",
		"agent": "builder", "provider": "fixture-mcp", "runtime": "herdr", "write_mode": "shared",
	})
	call := func() (int, []byte) {
		request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/onboarding", bytes.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Crewfold-CSRF", session.CSRF)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, raw
	}
	status, raw := call()
	if status != http.StatusConflict || !bytes.Contains(raw, []byte("Herdr interactive runtime is not ready")) {
		t.Fatalf("unavailable Herdr onboarding = %d: %s", status, raw)
	}
	workspaces, err := api.WorkspaceList(context.Background(), localapi.WorkspaceListParams{PageParams: localapi.PageParams{Limit: 10}})
	if err != nil || len(workspaces.Workspaces) != 0 {
		t.Fatalf("failed preflight changed canonical workspaces = %#v, %v", workspaces, err)
	}
	runtimeDriver.readyErr = nil
	status, raw = call()
	if status != http.StatusOK {
		t.Fatalf("ready Herdr onboarding = %d: %s", status, raw)
	}
}

func TestM21WorkbenchRetriesOneExactStartFailedRunAfterPreflight(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	workspace, err := api.WorkspaceInit(context.Background(), "personal", "web-retry-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := api.ProjectAdd(context.Background(), workspace.Workspace.ID, "signal-garden", filepath.Join(t.TempDir(), "signal-garden"), domain.WriteModeShared, "web-retry-project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := api.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: workspace.Workspace.ID, Name: "builder", Role: "implementation", Provider: "fake", Runtime: "fake", MaxConcurrency: 2, IdempotencyKey: "web-retry-agent"})
	if err != nil {
		t.Fatal(err)
	}
	task := createAssignedRunWorkerTask(t, api, project.Project.ID, agent.Agent.ID, "web retry")
	failed, err := api.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: workspace.Workspace.ID, Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "web-retry-failure", StartFailure: "fixture refused to start"},
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "web-retry-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, api, failed.Detail.Run.ID, domain.RunStartFailed)

	bootstrap, err := api.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, client, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"workspace": workspace.Workspace.ID, "run": failed.Detail.Run.ID, "idempotency_key": "web-retry-second"})
	request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/retry-run", bytes.NewReader(body))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Crewfold-CSRF", session.CSRF)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d: %s", response.StatusCode, raw)
	}
	if err := protocolschema.ValidateJSON("local/v1/run-mutation.result.schema.json", raw); err != nil {
		t.Fatalf("retry response schema = %v: %s", err, raw)
	}
	var result localapi.RunMutationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Detail.Run.ID == failed.Detail.Run.ID || result.Detail.Run.TaskID != task.Detail.Task.ID {
		t.Fatalf("retry result = %#v", result.Detail.Run)
	}
	waitForRunStatus(t, api, result.Detail.Run.ID, domain.RunCompleted)
	runs, err := api.RunList(context.Background(), localapi.RunListParams{Workspace: workspace.Workspace.ID, Task: task.Detail.Task.ID, PageParams: localapi.PageParams{Limit: 10}})
	if err != nil || len(runs.Runs) != 2 {
		t.Fatalf("retry run list = %#v, %v", runs, err)
	}
}

func TestM21WorkbenchShellIsEmbeddedAndSecurityHeadersAreExact(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	parsed.Fragment = ""
	response, err := http.Get(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Crewfold Workbench")) {
		t.Fatalf("root response = %d %q", response.StatusCode, body)
	}
	for name, expected := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
	} {
		if value := response.Header.Get(name); value != expected {
			t.Errorf("%s = %q, want %q", name, value, expected)
		}
	}
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "connect-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", policy)
	}
	if cors := response.Header.Get("Access-Control-Allow-Origin"); cors != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want absent", cors)
	}
}

func TestM21DisabledWorkbenchFailsClosedAtLocalAPI(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.DisableWeb = true
	startTestServer(t, config)
	_, err := localapi.NewClient(config.SocketPath).WebBootstrap(context.Background())
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != "web_unavailable" || apiError.Retryable {
		t.Fatalf("WebBootstrap() error = %#v", err)
	}
}

func TestM21WorkbenchRefusesNonExactLoopbackAddress(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"0.0.0.0:0", "localhost:0", "[::1]:0", "127.0.0.2:0"} {
		if server, err := newWorkbenchServer(address, &server{}); err == nil {
			server.close()
			t.Errorf("newWorkbenchServer(%q) error = nil, want refusal", address)
		}
	}
}

func TestM21WorkbenchSessionCapacityEvictsTheOldestGrant(t *testing.T) {
	t.Parallel()

	server := &workbenchServer{sessions: make(map[[32]byte]workbenchSession)}
	now := time.Now().UTC()
	var oldest [32]byte
	oldest[0] = 1
	server.sessions[oldest] = workbenchSession{expiresAt: now.Add(time.Minute)}
	for index := byte(2); len(server.sessions) < maxWorkbenchSessions; index++ {
		var digest [32]byte
		digest[0] = index
		server.sessions[digest] = workbenchSession{expiresAt: now.Add(time.Duration(index) * time.Minute)}
	}
	server.evictOldestSessionLocked()
	if _, exists := server.sessions[oldest]; exists {
		t.Fatal("oldest owner session was not evicted")
	}
	if len(server.sessions) != maxWorkbenchSessions-1 {
		t.Fatalf("sessions = %d, want %d", len(server.sessions), maxWorkbenchSessions-1)
	}
}

func exchangeWorkbenchBootstrap(t *testing.T, client *http.Client, origin, token, requestOrigin string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{"bootstrap": token})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", requestOrigin)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session response status = %d: %s", response.StatusCode, raw)
	}
	return raw
}
