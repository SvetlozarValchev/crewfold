package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

func TestKnowledgeLocalAPIAndExplicitContextDelivery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	source := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "knowledge source")

	proposed, err := client.KnowledgePropose(context.Background(), localapi.KnowledgeProposeParams{
		Workspace: "personal", Type: domain.KnowledgeTypeFinding,
		Title: "Accepted contact ordering contract", Body: "Sort contacts by stable identifier before emission.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: source.Detail.Task.ID, Role: domain.KnowledgeSourcePrimary}},
		IdempotencyKey:  "api-knowledge-proposal",
	})
	if err != nil || proposed.Revision.ProjectID != project.Project.ID || proposed.Revision.ReviewStatus != domain.KnowledgeReviewProposed {
		t.Fatalf("KnowledgePropose() = %#v, %v", proposed, err)
	}
	before, err := client.KnowledgeShow(context.Background(), "personal", proposed.Revision.ID)
	if err != nil || len(before.Detail.AuthorityChecks) != 0 {
		t.Fatalf("KnowledgeShow(proposed) = %#v, %v", before, err)
	}
	accepted, err := client.KnowledgeAccept(context.Background(), localapi.KnowledgeDecisionParams{
		Workspace: "personal", KnowledgeRevision: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, DecisionNote: "owner accepted",
		IdempotencyKey: "api-knowledge-accept",
	})
	if err != nil || accepted.Revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent || accepted.AuthorityCheck == nil || accepted.AuthorityCheck.Actor.ID != "local-owner" {
		t.Fatalf("KnowledgeAccept() = %#v, %v", accepted, err)
	}
	if _, err := client.KnowledgeList(context.Background(), localapi.KnowledgeListParams{Workspace: "personal"}); localAPIErrorCode(err) != store.CodeInvalidKnowledge {
		t.Fatalf("KnowledgeList(without project) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	listed, err := client.KnowledgeList(context.Background(), localapi.KnowledgeListParams{Workspace: "personal", Project: project.Project.ID})
	if err != nil || len(listed.List.Revisions) != 1 || listed.List.Revisions[0].ID != accepted.Revision.ID {
		t.Fatalf("KnowledgeList() = %#v, %v", listed, err)
	}
	after, err := client.KnowledgeShow(context.Background(), "personal", accepted.Revision.ID)
	if err != nil || len(after.Detail.AuthorityChecks) != 1 || after.Detail.AuthorityChecks[0].Outcome != domain.KnowledgeAuthorityAllowed {
		t.Fatalf("KnowledgeShow(accepted) = %#v, %v", after, err)
	}

	replacement := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "knowledge replacement")
	packet, err := client.ContextBuild(context.Background(), localapi.ContextBuildParams{
		Workspace: "personal", Task: replacement.Detail.Task.ID, Agent: agent.Agent.ID,
		KnowledgeRevisionIDs: []string{accepted.Revision.ID}, ExpectedTaskRevision: replacement.Detail.Task.Revision,
		IdempotencyKey: "api-knowledge-context",
	})
	if err != nil || len(packet.Packet.AcceptedKnowledge) != 1 || packet.Packet.AcceptedKnowledge[0].ID != accepted.Revision.ID || len(packet.Packet.RequestedKnowledgeRevisionIDs) != 1 {
		t.Fatalf("ContextBuild(knowledge) = %#v, %v", packet, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunScopedMCPCanProposeButCannotAcceptKnowledge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	running := startTestServer(t, config)
	owner := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, owner, fixtureRoot)
	task := createAssignedRunWorkerTask(t, owner, project.Project.ID, agent.Agent.ID, "run knowledge proposal")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "run-knowledge-proposal", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting", WaitForResume: true}}}
	started, err := owner.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "run-knowledge-proposal",
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
	seenProposal, seenAcceptance := false, false
	for _, tool := range listed.Tools {
		seenProposal = seenProposal || tool.Name == toolKnowledge
		seenAcceptance = seenAcceptance || tool.Name == toolKnowledgeAccept
	}
	if !seenProposal || seenAcceptance {
		t.Fatalf("run-scoped tools proposal=%t acceptance=%t", seenProposal, seenAcceptance)
	}
	result, err := client.CallTool(context.Background(), toolKnowledge, map[string]any{
		"type": domain.KnowledgeTypeFinding, "title": "Run finding", "body": "The run observed a structured fact.",
		"confidence": domain.KnowledgeConfidenceMedium, "verification_status": domain.KnowledgeVerificationSupported,
		"freshness_policy": domain.KnowledgeFreshUntilSuperseded, "idempotency_key": "mcp-propose-finding",
	})
	if err != nil || result.IsError {
		t.Fatalf("CallTool(propose knowledge) = %#v, %v", result, err)
	}
	var revision domain.KnowledgeRevision
	if err := json.Unmarshal(result.StructuredContent, &revision); err != nil || revision.ProposedBy != active.Detail.Run.ID || revision.ReviewStatus != domain.KnowledgeReviewProposed {
		t.Fatalf("proposed revision = %#v, decode error = %v", revision, err)
	}
	denied, err := client.CallTool(context.Background(), toolKnowledgeAccept, map[string]any{"knowledge_revision": revision.ID})
	if err != nil || !denied.IsError {
		t.Fatalf("CallTool(unavailable acceptance) = %#v, %v", denied, err)
	}
	afterDenial, err := owner.KnowledgeShow(context.Background(), "personal", revision.ID)
	if err != nil || afterDenial.Detail.Revision.ReviewStatus != domain.KnowledgeReviewProposed || len(afterDenial.Detail.AuthorityChecks) != 0 {
		t.Fatalf("knowledge after MCP acceptance denial = %#v, %v", afterDenial, err)
	}
	events, err := owner.EventsList(context.Background(), 0, 200)
	if err != nil {
		t.Fatalf("EventsList() error = %v", err)
	}
	seenPolicyDenial := false
	for _, event := range events.Events {
		seenPolicyDenial = seenPolicyDenial || event.Type == "run.tool_denied" && event.Entity.ID == active.Detail.Run.ID
	}
	if !seenPolicyDenial {
		t.Fatal("MCP acceptance attempt did not produce run.tool_denied")
	}
	latest, err := owner.RunShow(context.Background(), "personal", active.Detail.Run.ID)
	if err != nil {
		t.Fatalf("RunShow(before stop) error = %v", err)
	}
	if _, err := owner.RunStop(context.Background(), localapi.RunStopParams{Workspace: "personal", Run: active.Detail.Run.ID, ExpectedRevision: latest.Detail.Run.Revision, GracePeriodMillis: 100, IdempotencyKey: "stop-run-knowledge-proposal"}); err != nil {
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
