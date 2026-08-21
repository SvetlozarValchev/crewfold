package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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
	if err != nil || len(listed.List.Revisions) != 1 || listed.List.Revisions[0].ID != accepted.Revision.ID ||
		len(listed.List.Presentations) != 1 || listed.List.Presentations[0].RevisionID != accepted.Revision.ID ||
		listed.List.Presentations[0].Producer.Label != "local owner" {
		t.Fatalf("KnowledgeList() = %#v, %v", listed, err)
	}
	after, err := client.KnowledgeShow(context.Background(), "personal", accepted.Revision.ID)
	if err != nil || len(after.Detail.AuthorityChecks) != 1 || after.Detail.AuthorityChecks[0].Outcome != domain.KnowledgeAuthorityAllowed ||
		after.Detail.Presentation.RevisionID != accepted.Revision.ID || after.Detail.Presentation.Producer.Label != "local owner" {
		t.Fatalf("KnowledgeShow(accepted) = %#v, %v", after, err)
	}
	indexBefore, err := client.KnowledgeIndexStatus(context.Background(), "personal")
	if err != nil || indexBefore.Index.Status != domain.KnowledgeIndexOK || indexBefore.Index.SourceCount != 1 {
		t.Fatalf("KnowledgeIndexStatus(after proposal refresh) = %#v, %v", indexBefore, err)
	}
	searched, err := client.KnowledgeSearch(context.Background(), localapi.KnowledgeSearchParams{
		Workspace: "personal", Project: project.Project.ID, Query: "contact ordering",
	})
	if err != nil || len(searched.Search.Matches) != 1 || searched.Search.Matches[0].Revision.ID != accepted.Revision.ID || searched.Search.Matches[0].Explanation.Authority.ReviewStatus != domain.KnowledgeReviewAccepted {
		t.Fatalf("KnowledgeSearch() = %#v, %v", searched, err)
	}
	omittedLimit := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodKnowledgeSearch, map[string]any{
		"workspace": "personal", "project": project.Project.ID, "query": "contact ordering",
	})
	if omittedLimit.Error != nil {
		t.Fatalf("knowledge.search without limit error = %#v", omittedLimit.Error)
	}
	var omittedResult localapi.KnowledgeSearchResult
	if err := json.Unmarshal(omittedLimit.Result, &omittedResult); err != nil || len(omittedResult.Search.Matches) != 1 {
		t.Fatalf("knowledge.search without limit result = %#v, error=%v", omittedResult, err)
	}
	explicitZero := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodKnowledgeSearch, map[string]any{
		"workspace": "personal", "project": project.Project.ID, "query": "contact ordering", "limit": 0,
	})
	if explicitZero.Error == nil || explicitZero.Error.Code != "invalid_request" || explicitZero.Error.Retryable {
		t.Fatalf("knowledge.search with explicit zero limit error = %#v", explicitZero.Error)
	}
	rebuilt, err := client.KnowledgeIndexRebuild(context.Background(), localapi.KnowledgeIndexRebuildParams{
		Workspace: "personal", IdempotencyKey: "api-knowledge-index-rebuild",
	})
	if err != nil || rebuilt.Index.Status != domain.KnowledgeIndexOK || rebuilt.Index.SourceCount != 1 {
		t.Fatalf("KnowledgeIndexRebuild() = %#v, %v", rebuilt, err)
	}
	if rebuilt.Index.Generation < indexBefore.Index.Generation || rebuilt.Index.SourceDigest != indexBefore.Index.SourceDigest {
		t.Fatalf("explicit rebuild changed canonical source identity: before=%#v after=%#v", indexBefore.Index, rebuilt.Index)
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

func TestKnowledgeRetrievalHandlersRejectUnknownFields(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "strict-retrieval-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	for _, test := range []struct {
		method string
		params map[string]any
	}{
		{localapi.MethodKnowledgeSearch, map[string]any{"workspace": "personal", "project": "demo", "query": "term", "unexpected": true}},
		{localapi.MethodKnowledgeIndexStatus, map[string]any{"workspace": "personal", "unexpected": true}},
		{localapi.MethodKnowledgeIndexRebuild, map[string]any{"workspace": "personal", "idempotency_key": "strict", "unexpected": true}},
	} {
		response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
		if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
			t.Errorf("%s response error = %#v, want non-retryable invalid_request", test.method, response.Error)
		}
	}
}

func TestRetrievalDegradedStoreErrorsAreRetryable(t *testing.T) {
	response := storeErrorResponse(localapi.Request{ID: "degraded", Protocol: localapi.MaxProtocol}, &store.Error{
		Code: store.CodeRetrievalDegraded, Message: "knowledge retrieval index is unavailable",
	})
	if response.Error == nil || response.Error.Code != store.CodeRetrievalDegraded || !response.Error.Retryable {
		t.Fatalf("storeErrorResponse(retrieval degraded) = %#v", response.Error)
	}
}

func rawLocalAPIRequest(t *testing.T, socketPath, method string, params any) localapi.Response {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial local API: %v", err)
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	write := func(request localapi.Request) localapi.Response {
		t.Helper()
		if err := json.NewEncoder(connection).Encode(request); err != nil {
			t.Fatalf("encode %s: %v", request.Method, err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read %s: %v", request.Method, err)
		}
		var response localapi.Response
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode %s response: %v", request.Method, err)
		}
		return response
	}
	helloParams, _ := json.Marshal(localapi.HelloParams{MinProtocol: localapi.MinProtocol, MaxProtocol: localapi.MaxProtocol})
	hello := write(localapi.Request{ID: "strict-hello", Method: localapi.MethodHello, Params: helloParams})
	if hello.Error != nil {
		t.Fatalf("hello error = %#v", hello.Error)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return write(localapi.Request{ID: "strict-retrieval", Protocol: localapi.MaxProtocol, Method: method, Params: encoded})
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
	events, err := owner.EventsList(context.Background(), localapi.EventsListParams{Workspace: "personal", After: 0, PageParams: localapi.PageParams{Limit: 200}})
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
