package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestDurableAgentTaskPromptMakesReviewEvidenceAnExactClosureRequirement(t *testing.T) {
	t.Parallel()
	briefing := domain.RunBriefing{
		Run:  domain.Run{ID: "run_review", Placement: domain.RunPlacement{CheckoutPath: "/tmp/review-checkout"}},
		Task: domain.Task{Title: "Review the candidate", Description: "Inspect the exact changed paths.", TaskClass: "review"},
	}
	prompt := durableAgentTaskPrompt(briefing)
	for _, required := range []string{"Task class: review", "crewfold_publish_artifact", "include the returned artifact ID", "intentionally blocks delivery closure"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("review task prompt does not explain %q:\n%s", required, prompt)
		}
	}
	briefing.Task.TaskClass = "implementation"
	if implementation := durableAgentTaskPrompt(briefing); strings.Contains(implementation, "crewfold_publish_artifact") {
		t.Fatalf("ordinary implementation task was given the review-only evidence requirement:\n%s", implementation)
	}
}

func TestM22AcceptedTaskRunsInTheDurableAgentsExistingCodexThread(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := &durableAgentRunAppServerFixture{t: t, threadID: "thread-one-durable-agent", turnID: "turn-one-accepted-task"}
	config := testConfig(t)
	config.CodexToolNetworkAccess = true
	config.CodexAppServerTransportFactory = fixture.transport
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()

	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "durable agent run workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeShared, "durable agent run project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "implementer", Role: "implementation owner",
		Provider: "codex", Runtime: "herdr", MaxConcurrency: 1, IdempotencyKey: "durable-agent-run-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: "Implement exact assigned work in the workstream checkout and report evidence.",
		DelegationPolicy: domain.DomainAgentHandsOn, PreferredEntry: true, IdempotencyKey: "durable-agent-run-attach",
	}); err != nil {
		t.Fatal(err)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "durable-agent-run-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.View.Session.Epoch != 1 || opened.View.Session.State != domain.DomainAgentSessionReady {
		t.Fatalf("opened durable agent = %#v", opened.View.Session)
	}

	assigned := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "same durable thread")
	started, err := client.RunStart(ctx, localapi.RunStartParams{
		Workspace: workspace.Workspace.Name, Task: assigned.Detail.Task.ID, Checkout: project.Checkout.ID,
		Runtime: "herdr", Provider: "codex", ExpectedTaskRevision: assigned.Detail.Task.Revision,
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema, Name: "same-thread-task",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "task turn is controlled by Codex"}},
		},
		IdempotencyKey: "durable-agent-run-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunActive)
	fixture.assertSameThreadTaskTurn(t, project.Checkout.Path)

	view, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.View.Turns) != 1 || view.View.Turns[0].ID != fixture.turnID {
		t.Fatalf("durable agent did not expose the accepted task in its one conversation: %#v", view.View.Turns)
	}

	completed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunCompleted)
	if completed.Detail.Run.RuntimeHandle != "" || completed.Detail.Run.ProviderHandle != "" {
		t.Fatalf("completed same-thread attempt retained live bindings: %#v", completed.Detail.Run)
	}
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestM22RetryStartsANewTurnInTheSameDurableAgentThread(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := &durableAgentRunAppServerFixture{
		t: t, threadID: "thread-one-durable-agent-retry", turnID: "turn-failed-attempt", failFirst: true,
	}
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"herdr": &m21ReadinessRuntime{FakeRuntime: execution.NewFakeRuntime()}}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"codex": durableCodexProbeProvider{}}
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()

	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "durable agent retry workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeShared, "durable agent retry project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "implementer", Role: "implementation owner",
		Provider: "codex", Runtime: "herdr", MaxConcurrency: 1, IdempotencyKey: "durable-agent-retry-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: "Implement exact assigned work and retain one provider identity across retries.",
		DelegationPolicy: domain.DomainAgentHandsOn, PreferredEntry: true, IdempotencyKey: "durable-agent-retry-attach",
	}); err != nil {
		t.Fatal(err)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "durable-agent-retry-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "same durable thread retry")
	started, err := client.RunStart(ctx, localapi.RunStartParams{
		Workspace: workspace.Workspace.Name, Task: assigned.Detail.Task.ID, Checkout: project.Checkout.ID,
		Runtime: "herdr", Provider: "codex", ExpectedTaskRevision: assigned.Detail.Task.Revision,
		Scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "same-thread-retry", Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "same durable thread"},
		}},
		IdempotencyKey: "durable-agent-retry-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunStartFailed)

	retried := retryRunThroughWorkbench(t, client, workspace.Workspace.ID, failed.Detail.Run.ID, "durable-agent-retry-owner")
	waitForRunStatus(t, client, retried.Detail.Run.ID, domain.RunCompleted)
	fixture.assertRetryUsedSameThread(t, project.Checkout.Path)
	view, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if view.View.Session.ThreadID != opened.View.Session.ThreadID || len(view.View.Turns) != 1 {
		t.Fatalf("retry changed durable identity or hid an attempt: opened=%#v view=%#v", opened.View.Session, view.View)
	}
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestM22OwnerInputAfterRestartSteersTheActiveDurableAgentTaskTurn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := &durableAgentRunAppServerFixture{
		t: t, threadID: "thread-one-durable-agent-restart", turnID: "turn-active-across-restart", leaveActive: true,
	}
	config := testConfig(t)
	config.CodexToolNetworkAccess = true
	config.CodexAppServerTransportFactory = fixture.transport
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()

	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "durable agent restart workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeShared, "durable agent restart project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "implementer", Role: "implementation owner",
		Provider: "codex", Runtime: "herdr", MaxConcurrency: 1, IdempotencyKey: "durable-agent-restart-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: "Keep one provider identity through service restarts.",
		DelegationPolicy: domain.DomainAgentHandsOn, PreferredEntry: true, IdempotencyKey: "durable-agent-restart-attach",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "durable-agent-restart-open",
	}); err != nil {
		t.Fatal(err)
	}
	assigned := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "active across restart")
	started, err := client.RunStart(ctx, localapi.RunStartParams{
		Workspace: workspace.Workspace.Name, Task: assigned.Detail.Task.ID, Checkout: project.Checkout.ID,
		Runtime: "herdr", Provider: "codex", ExpectedTaskRevision: assigned.Detail.Task.Revision,
		Scenario: domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "same-thread-restart", Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "same durable thread survives daemon restart"},
		}},
		IdempotencyKey: "durable-agent-restart-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunActive)
	shown, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	var projected bool
	for _, turn := range shown.View.Turns {
		if turn.ID == fixture.turnID && turn.Status == "inProgress" {
			projected = true
		}
	}
	if !projected || shown.View.ThreadStatus != "active" {
		t.Fatalf("active accepted task was not projected into its durable session: %#v", shown.View)
	}
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}

	running = startTestServer(t, config)
	client = localapi.NewClient(config.SocketPath)
	sent, err := client.DomainAgentSessionSend(ctx, localapi.DomainAgentSessionSendParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Text: "Keep the current task moving and report the next exact checkpoint.", IdempotencyKey: "owner-steer-after-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent.AcceptedTurn == nil || sent.AcceptedTurn.ID != fixture.turnID {
		t.Fatalf("owner input started a parallel turn after restart: %#v", sent.AcceptedTurn)
	}
	fixture.assertSteeredActiveTurn(t, "Keep the current task moving")
	message, err := client.MessageSend(ctx, localapi.MessageSendParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.ID, RecipientAgent: agent.Agent.ID,
		Kind: domain.MessageInform, Subject: "Same durable task context", Body: "Incorporate the accepted interface note without starting another agent turn.",
		ArtifactIDs: []string{}, IdempotencyKey: "mailbox-steer-after-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		inbox, inboxErr := client.InboxList(ctx, workspace.Workspace.Name, agent.Agent.ID, 20)
		if inboxErr != nil {
			t.Fatal(inboxErr)
		}
		if len(inbox.Items) == 1 && inbox.Items[0].Message.ID == message.Mutation.Message.ID && inbox.Items[0].Delivery.WakeStatus == domain.WakeSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mailbox wake did not settle through the active durable turn: %#v", inbox.Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fixture.assertSteeredActiveTurn(t, "Crewfold delivered a durable message while this task turn is active")
	fixture.assertSteerCount(t, 2)
	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}
}

type durableCodexProbeProvider struct{ execution.FakeProvider }

func (durableCodexProbeProvider) Name() string { return "codex" }

func retryRunThroughWorkbench(t *testing.T, api *localapi.Client, workspace, runID, idempotencyKey string) localapi.RunMutationResult {
	t.Helper()
	bootstrap, err := api.WebBootstrap(context.Background())
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
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, httpClient, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	current, err := api.RunShow(context.Background(), workspace, runID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"workspace": workspace, "run": runID, "expected_run_revision": current.Detail.Run.Revision,
		"expected_task_revision": current.Detail.Task.Revision, "idempotency_key": idempotencyKey,
	})
	request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/retry-run", bytes.NewReader(body))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Crewfold-CSRF", session.CSRF)
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d: %s", response.StatusCode, raw)
	}
	var result localapi.RunMutationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type durableAgentRunAppServerFixture struct {
	t           *testing.T
	mu          sync.Mutex
	threadID    string
	turnID      string
	thread      map[string]any
	turn        map[string]any
	turns       []map[string]any
	start       map[string]any
	starts      []map[string]any
	steers      []map[string]any
	failFirst   bool
	leaveActive bool
}

func (fixture *durableAgentRunAppServerFixture) transport() (execution.CodexAppServerTransport, error) {
	client, server := net.Pipe()
	go fixture.serve(server)
	return client, nil
}

func (fixture *durableAgentRunAppServerFixture) serve(connection net.Conn) {
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	encoder := json.NewEncoder(connection)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fixture.t.Errorf("decode app-server request: %v", err)
			return
		}
		method, _ := request["method"].(string)
		if method == "initialized" {
			continue
		}
		id, ok := request["id"]
		if !ok {
			fixture.t.Errorf("app-server request %q has no id", method)
			return
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"codexHome": "/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture"}
		case "thread/start":
			params, _ := request["params"].(map[string]any)
			cwd, _ := params["cwd"].(string)
			fixture.mu.Lock()
			fixture.thread = map[string]any{"id": fixture.threadID, "cwd": cwd, "ephemeral": false, "modelProvider": "openai", "status": map[string]any{"type": "idle"}, "turns": []any{}}
			thread := fixture.thread
			fixture.mu.Unlock()
			result = map[string]any{"thread": thread}
		case "thread/name/set":
			result = map[string]any{}
		case "turn/start":
			params, _ := request["params"].(map[string]any)
			fixture.mu.Lock()
			fixture.start = params
			fixture.starts = append(fixture.starts, params)
			failThisTurn := fixture.failFirst && len(fixture.starts) == 1
			if failThisTurn {
				fixture.mu.Unlock()
				if err := encoder.Encode(map[string]any{
					"id": id,
					"error": map[string]any{
						"code": -32000, "message": "Codex account is unavailable until the owner switches accounts",
					},
				}); err != nil {
					return
				}
				continue
			}
			turnID := fixture.turnID
			if len(fixture.starts) > 1 {
				turnID = "turn-retry-attempt"
			}
			fixture.turn = map[string]any{"id": turnID, "status": "inProgress", "items": []any{map[string]any{
				"id": "task-input", "type": "userMessage", "text": "accepted task", "clientId": params["clientUserMessageId"],
			}}}
			fixture.turns = append(fixture.turns, fixture.turn)
			fixture.thread["status"] = map[string]any{"type": "active"}
			threadTurns := make([]any, len(fixture.turns))
			for index := range fixture.turns {
				threadTurns[index] = fixture.turns[index]
			}
			fixture.thread["turns"] = threadTurns
			turn := fixture.turn
			fixture.mu.Unlock()
			if err := encoder.Encode(map[string]any{"id": id, "result": map[string]any{"turn": turn}}); err != nil {
				return
			}
			if fixture.leaveActive {
				continue
			}
			if err := encoder.Encode(map[string]any{
				"id": 9001, "method": "item/tool/call", "params": map[string]any{
					"threadId": fixture.threadID, "turnId": turnID, "callId": "same-thread-completion",
					"tool": toolCompletion, "arguments": map[string]any{
						"summary": "same durable agent completed the accepted task", "handoff": "the accepted task turn and owner conversation share one provider thread",
						"evidence_ids": []string{}, "changed_paths": []string{"README.md"}, "checks": []string{"test fixture passed"},
						"remaining_risks": []string{}, "unknowns": []string{}, "idempotency_key": "same-thread-completion",
					},
				},
			}); err != nil {
				return
			}
			// The production app-server multiplexes read and steer requests while a
			// client-owned tool call is pending. Mirror that behavior instead of
			// assuming the tool response is the next wire message: the owner UI is
			// deliberately allowed to inspect this exact in-flight task turn.
			for {
				if !scanner.Scan() {
					fixture.t.Errorf("read same-thread completion tool response: %v", scanner.Err())
					return
				}
				var envelope map[string]any
				if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
					fixture.t.Errorf("decode interleaved app-server message: %v", err)
					return
				}
				if interleavedMethod, _ := envelope["method"].(string); interleavedMethod != "" {
					var interleavedResult any
					switch interleavedMethod {
					case "thread/read", "thread/resume":
						fixture.mu.Lock()
						interleavedResult = map[string]any{"thread": fixture.thread}
						fixture.mu.Unlock()
					case "thread/turns/list":
						fixture.mu.Lock()
						turns := make([]any, len(fixture.turns))
						for index := range fixture.turns {
							turns[index] = fixture.turns[index]
						}
						fixture.mu.Unlock()
						interleavedResult = map[string]any{"data": turns}
					case "turn/steer":
						params, _ := envelope["params"].(map[string]any)
						fixture.mu.Lock()
						fixture.steers = append(fixture.steers, params)
						fixture.mu.Unlock()
						interleavedResult = map[string]any{"turnId": turnID}
					default:
						fixture.t.Errorf("unexpected interleaved app-server request %q", interleavedMethod)
						return
					}
					if err := encoder.Encode(map[string]any{"id": envelope["id"], "result": interleavedResult}); err != nil {
						return
					}
					continue
				}
				var toolResponse struct {
					ID     int64 `json:"id"`
					Result struct {
						Success bool `json:"success"`
					} `json:"result"`
					Error any `json:"error"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &toolResponse); err != nil || toolResponse.ID != 9001 || toolResponse.Error != nil || !toolResponse.Result.Success {
					fixture.t.Errorf("same-thread completion tool response = %#v, error = %v", toolResponse, err)
					return
				}
				break
			}
			fixture.mu.Lock()
			fixture.turn["status"] = "completed"
			fixture.thread["status"] = map[string]any{"type": "idle"}
			fixture.mu.Unlock()
			continue
		case "turn/steer":
			params, _ := request["params"].(map[string]any)
			fixture.mu.Lock()
			fixture.steers = append(fixture.steers, params)
			fixture.mu.Unlock()
			result = map[string]any{"turnId": fixture.turnID}
		case "thread/read", "thread/resume":
			fixture.mu.Lock()
			thread := fixture.thread
			fixture.mu.Unlock()
			result = map[string]any{"thread": thread}
		case "thread/turns/list":
			fixture.mu.Lock()
			turnsSnapshot := append([]map[string]any(nil), fixture.turns...)
			fixture.mu.Unlock()
			turns := []any{}
			for _, turn := range turnsSnapshot {
				turns = append(turns, turn)
			}
			result = map[string]any{"data": turns}
		case "turn/interrupt":
			fixture.mu.Lock()
			fixture.turn["status"] = "interrupted"
			fixture.thread["status"] = map[string]any{"type": "idle"}
			fixture.mu.Unlock()
			result = map[string]any{}
		default:
			fixture.t.Errorf("unexpected app-server request %q", method)
			return
		}
		if err := encoder.Encode(map[string]any{"id": id, "result": result}); err != nil {
			return
		}
	}
}

func (fixture *durableAgentRunAppServerFixture) assertSameThreadTaskTurn(t *testing.T, checkoutPath string) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.start == nil {
		t.Fatal("accepted task did not start a Codex turn")
	}
	policy, _ := fixture.start["sandboxPolicy"].(map[string]any)
	if fixture.start["threadId"] != fixture.threadID || fixture.start["clientUserMessageId"] == nil ||
		fixture.start["cwd"] != checkoutPath || fixture.start["approvalPolicy"] != "never" ||
		policy["type"] != "workspaceWrite" || policy["networkAccess"] != true {
		t.Fatalf("accepted task turn did not reuse the durable thread with exact task authority: %#v", fixture.start)
	}
}

func (fixture *durableAgentRunAppServerFixture) assertRetryUsedSameThread(t *testing.T, checkoutPath string) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 2 {
		t.Fatalf("task turn starts = %d, want failed attempt plus retry", len(fixture.starts))
	}
	for index, start := range fixture.starts {
		policy, _ := start["sandboxPolicy"].(map[string]any)
		if start["threadId"] != fixture.threadID || start["cwd"] != checkoutPath ||
			start["approvalPolicy"] != "never" || policy["type"] != "workspaceWrite" {
			t.Fatalf("task turn %d changed durable thread or authority: %#v", index, start)
		}
	}
	if fixture.starts[0]["clientUserMessageId"] == fixture.starts[1]["clientUserMessageId"] {
		t.Fatalf("retry reused the prior immutable run identity: %#v", fixture.starts)
	}
}

func (fixture *durableAgentRunAppServerFixture) assertSteeredActiveTurn(t *testing.T, text string) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, steer := range fixture.steers {
		input, _ := steer["input"].([]any)
		if len(input) == 0 {
			// Values assembled inside the same Go process retain their concrete map slice type.
			if typed, ok := steer["input"].([]map[string]string); ok {
				for _, item := range typed {
					if strings.Contains(item["text"], text) && steer["threadId"] == fixture.threadID && steer["expectedTurnId"] == fixture.turnID {
						return
					}
				}
			}
		}
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			itemText, _ := item["text"].(string)
			if strings.Contains(itemText, text) && steer["threadId"] == fixture.threadID && steer["expectedTurnId"] == fixture.turnID {
				return
			}
		}
	}
	t.Fatalf("input %q did not steer the active durable task turn: %#v", text, fixture.steers)
}

func (fixture *durableAgentRunAppServerFixture) assertSteerCount(t *testing.T, want int) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.steers) != want {
		t.Fatalf("turn steer calls = %d, want %d: %#v", len(fixture.steers), want, fixture.steers)
	}
}
