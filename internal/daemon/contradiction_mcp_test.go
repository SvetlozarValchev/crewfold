package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/mcp"
)

func TestRunScopedMCPReportsContradictionButCannotConfirmIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	running := startTestServer(t, config)
	owner := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, owner, fixtureRoot)
	task := createAssignedRunWorkerTask(t, owner, project.Project.ID, agent.Agent.ID, "run contradiction report")
	left := createAcceptedMCPContradictionKnowledge(t, owner, task.Detail.Task.ID, "left", "Use ascending contacts", "Contacts must be ascending")
	right := createAcceptedMCPContradictionKnowledge(t, owner, task.Detail.Task.ID, "right", "Use descending contacts", "Contacts must be descending")

	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "run-contradiction-report",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting", WaitForResume: true}},
	}
	started, err := owner.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "run-contradiction-report",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	active := waitForRunStatus(t, owner, started.Detail.Run.ID, domain.RunActive)
	manager, err := newRunCapabilityManager(config.DataDir, config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := mcp.Dial(context.Background(), config.SocketPath, manager.token(active.Detail.Run.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := client.Call(context.Background(), "tools/list", map[string]any{}, &listed); err != nil {
		t.Fatal(err)
	}
	seenReport, seenConfirm := false, false
	for _, tool := range listed.Tools {
		seenReport = seenReport || tool.Name == toolContradictionReport
		seenConfirm = seenConfirm || tool.Name == toolContradictionConfirm
	}
	if !seenReport || seenConfirm {
		t.Fatalf("run contradiction tools report=%t confirm=%t", seenReport, seenConfirm)
	}
	arguments := map[string]any{
		"left_revision": right.Revision.ID, "right_revision": left.Revision.ID,
		"reason":          "The two accepted contact-ordering decisions disagree",
		"idempotency_key": "mcp-report-contradiction",
	}
	result, err := client.CallTool(context.Background(), toolContradictionReport, arguments)
	if err != nil || result.IsError {
		t.Fatalf("CallTool(report contradiction) = %#v, %v", result, err)
	}
	var detail domain.KnowledgeContradictionDetail
	if err := json.Unmarshal(result.StructuredContent, &detail); err != nil ||
		detail.Contradiction.Status != domain.KnowledgeContradictionProposed ||
		detail.Contradiction.ReportedBy != active.Detail.Run.ID ||
		detail.Contradiction.ReportedByType != domain.KnowledgeActorAgentRun ||
		detail.Contradiction.ProjectID != project.Project.ID {
		t.Fatalf("reported contradiction = %#v, decode error = %v", detail, err)
	}
	replayed, err := client.CallTool(context.Background(), toolContradictionReport, arguments)
	if err != nil || replayed.IsError {
		t.Fatalf("CallTool(report replay) = %#v, %v", replayed, err)
	}
	var replayedDetail domain.KnowledgeContradictionDetail
	if err := json.Unmarshal(replayed.StructuredContent, &replayedDetail); err != nil || !reflect.DeepEqual(replayedDetail, detail) {
		t.Fatalf("reported contradiction replay = %#v, %v; want %#v", replayedDetail, err, detail)
	}

	denied, err := client.CallTool(context.Background(), toolContradictionConfirm, map[string]any{
		"contradiction": detail.Contradiction.ID, "expected_state_revision": 1,
		"idempotency_key": "forbidden-confirm",
	})
	var deniedError mcp.ToolError
	decodeDeniedErr := json.Unmarshal(denied.StructuredContent, &deniedError)
	if err != nil || !denied.IsError || decodeDeniedErr != nil || deniedError.Code != "denied_by_policy" {
		t.Fatalf("CallTool(forbidden confirm) = %#v, %v", denied, err)
	}
	shown, err := owner.ContradictionShow(context.Background(), "personal", detail.Contradiction.ID)
	if err != nil || shown.Detail.Contradiction.Status != domain.KnowledgeContradictionProposed || len(shown.Detail.AuthorityChecks) != 0 {
		t.Fatalf("contradiction after MCP confirm denial = %#v, %v", shown, err)
	}
	events, err := owner.EventsList(context.Background(), localapi.EventsListParams{Workspace: "personal", After: 0, PageParams: localapi.PageParams{Limit: 500}})
	if err != nil {
		t.Fatalf("EventsList() error = %v", err)
	}
	detected, toolDenied := 0, 0
	for _, event := range events.Events {
		if event.Type == "contradiction.detected" && event.Entity.ID == detail.Contradiction.ID {
			detected++
		}
		if event.Type == "run.tool_denied" && event.Entity.ID == active.Detail.Run.ID {
			toolDenied++
		}
	}
	if detected != 1 || toolDenied != 1 {
		t.Fatalf("contradiction events detected=%d tool_denied=%d, want one each", detected, toolDenied)
	}

	latest, err := owner.RunShow(context.Background(), "personal", active.Detail.Run.ID)
	if err != nil {
		t.Fatalf("RunShow(before stop) error = %v", err)
	}
	if _, err := owner.RunStop(context.Background(), localapi.RunStopParams{
		Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: latest.Detail.Run.Revision,
		GracePeriodMillis: 100, IdempotencyKey: "stop-run-contradiction-report",
	}); err != nil {
		t.Fatalf("RunStop() error = %v", err)
	}
	waitForRunStatus(t, owner, active.Detail.Run.ID, domain.RunStopped)
	if _, err := owner.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func createAcceptedMCPContradictionKnowledge(t *testing.T, client *localapi.Client, taskID, key, title, body string) localapi.KnowledgeMutationResult {
	t.Helper()
	proposed, err := client.KnowledgePropose(context.Background(), localapi.KnowledgeProposeParams{
		Workspace: "personal", Type: domain.KnowledgeTypeDecision, Title: title, Body: body,
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: taskID, Role: domain.KnowledgeSourcePrimary}},
		IdempotencyKey:  "propose-mcp-contradiction-" + key,
	})
	if err != nil {
		t.Fatalf("KnowledgePropose(%s) error = %v", key, err)
	}
	accepted, err := client.KnowledgeAccept(context.Background(), localapi.KnowledgeDecisionParams{
		Workspace: "personal", KnowledgeRevision: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, DecisionNote: "accept MCP contradiction fixture",
		IdempotencyKey: "accept-mcp-contradiction-" + key,
	})
	if err != nil {
		t.Fatalf("KnowledgeAccept(%s) error = %v", key, err)
	}
	return accepted
}
