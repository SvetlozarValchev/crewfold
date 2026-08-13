package daemon

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestContradictionLocalAPICanonicalLifecycleAndDerivedDispute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "contradiction source")
	accepted := func(key, title, body string) domain.KnowledgeRevision {
		t.Helper()
		proposed, err := client.KnowledgePropose(context.Background(), localapi.KnowledgeProposeParams{
			Workspace: "personal", Type: domain.KnowledgeTypeFinding, Title: title, Body: body,
			Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
			FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
			Sources: []domain.KnowledgeSourceInput{{
				Type: domain.KnowledgeSourceTask, ID: task.Detail.Task.ID, Role: domain.KnowledgeSourcePrimary,
			}},
			IdempotencyKey: "propose-" + key,
		})
		if err != nil {
			t.Fatalf("KnowledgePropose(%s) error=%v", key, err)
		}
		result, err := client.KnowledgeAccept(context.Background(), localapi.KnowledgeDecisionParams{
			Workspace: "personal", KnowledgeRevision: proposed.Revision.ID,
			ExpectedStateRevision: proposed.Revision.StateRevision, IdempotencyKey: "accept-" + key,
		})
		if err != nil {
			t.Fatalf("KnowledgeAccept(%s) error=%v", key, err)
		}
		return result.Revision
	}
	left := accepted("left", "Left exact claim", "The integration limit is 10.")
	right := accepted("right", "Right exact claim", "The integration limit is 20.")

	reported, err := client.ContradictionReport(context.Background(), localapi.ContradictionReportParams{
		Workspace: "personal", LeftRevision: right.ID, RightRevision: left.ID,
		Reason: "The two accepted limits conflict.", IdempotencyKey: "report-exact-conflict",
	})
	if err != nil {
		t.Fatalf("ContradictionReport() error=%v", err)
	}
	wantLeft, wantRight := left.ID, right.ID
	if wantRight < wantLeft {
		wantLeft, wantRight = wantRight, wantLeft
	}
	contradiction := reported.Detail.Contradiction
	if reported.Schema != localapi.ContradictionMutationSchema || contradiction.Status != domain.KnowledgeContradictionProposed ||
		contradiction.LeftRevisionID != wantLeft || contradiction.RightRevisionID != wantRight ||
		reported.Detail.AuthorityCheckCount != 0 || reported.Detail.AuthorityChecks == nil {
		t.Fatalf("ContradictionReport()=%#v", reported)
	}
	before, err := client.KnowledgeDispute(context.Background(), "personal", left.ID)
	if err != nil || before.Dispute.Disputed || before.Dispute.OpenContradictionCount != 0 || before.Dispute.OpenContradictionIDs == nil {
		t.Fatalf("KnowledgeDispute(before confirm)=%#v, %v", before, err)
	}

	confirmed, err := client.ContradictionConfirm(context.Background(), localapi.ContradictionDecisionParams{
		Workspace: "personal", Contradiction: contradiction.ID,
		ExpectedStateRevision: contradiction.StateRevision, Note: "Owner confirmed exact conflict",
		IdempotencyKey: "confirm-exact-conflict",
	})
	if err != nil || confirmed.Detail.Contradiction.Status != domain.KnowledgeContradictionOpen ||
		confirmed.Detail.Contradiction.ConfirmEventSequence < 1 || confirmed.AuthorityCheck == nil ||
		confirmed.Detail.AuthorityCheckCount != 1 || len(confirmed.Detail.AuthorityChecks) != 1 {
		t.Fatalf("ContradictionConfirm()=%#v, %v", confirmed, err)
	}
	after, err := client.KnowledgeDispute(context.Background(), "personal", left.ID)
	if err != nil || !after.Dispute.Disputed || after.Dispute.OpenContradictionCount != 1 ||
		len(after.Dispute.OpenContradictionIDs) != 1 || after.Dispute.OpenContradictionIDs[0] != contradiction.ID {
		t.Fatalf("KnowledgeDispute(after confirm)=%#v, %v", after, err)
	}
	listed, err := client.ContradictionList(context.Background(), localapi.ContradictionListParams{
		Workspace: "personal", Project: project.Project.ID,
	})
	if err != nil || len(listed.List.Details) != 1 || listed.List.Details[0].Contradiction.ID != contradiction.ID {
		t.Fatalf("ContradictionList(active)=%#v, %v", listed, err)
	}
	shown, err := client.ContradictionShow(context.Background(), "personal", contradiction.ID)
	if err != nil || shown.Detail.Contradiction.Status != domain.KnowledgeContradictionOpen || shown.Detail.LeftRevision.ID != wantLeft {
		t.Fatalf("ContradictionShow()=%#v, %v", shown, err)
	}

	dismissed, err := client.ContradictionDismiss(context.Background(), localapi.ContradictionDecisionParams{
		Workspace: "personal", Contradiction: contradiction.ID,
		ExpectedStateRevision: confirmed.Detail.Contradiction.StateRevision, Note: "False positive after owner review",
		IdempotencyKey: "dismiss-exact-conflict",
	})
	if err != nil || dismissed.Detail.Contradiction.Status != domain.KnowledgeContradictionDismissed ||
		dismissed.Detail.Contradiction.DismissEventSequence < 1 || dismissed.Detail.AuthorityCheckCount != 2 {
		t.Fatalf("ContradictionDismiss()=%#v, %v", dismissed, err)
	}
	cleared, err := client.KnowledgeDispute(context.Background(), "personal", right.ID)
	if err != nil || cleared.Dispute.Disputed || cleared.Dispute.OpenContradictionCount != 0 {
		t.Fatalf("KnowledgeDispute(after dismiss)=%#v, %v", cleared, err)
	}
	active, err := client.ContradictionList(context.Background(), localapi.ContradictionListParams{
		Workspace: "personal", Project: project.Project.ID,
	})
	if err != nil || len(active.List.Details) != 0 {
		t.Fatalf("ContradictionList(default active after dismissal)=%#v, %v", active, err)
	}
	dismissedList, err := client.ContradictionList(context.Background(), localapi.ContradictionListParams{
		Workspace: "personal", Project: project.Project.ID, Status: domain.KnowledgeContradictionDismissed,
	})
	if err != nil || len(dismissedList.List.Details) != 1 {
		t.Fatalf("ContradictionList(dismissed)=%#v, %v", dismissedList, err)
	}
}

func TestContradictionHandlersRejectAuthorityInjectionUnknownAndUnboundedParameters(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	tests := []struct {
		name   string
		method string
		params map[string]any
	}{
		{
			name: "report authority injection", method: localapi.MethodContradictionReport,
			params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "reason": "conflict", "idempotency_key": "report", "actor": map[string]any{"id": "local-owner", "type": "human"}},
		},
		{
			name: "report project injection", method: localapi.MethodContradictionReport,
			params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "reason": "conflict", "idempotency_key": "report", "project": "chosen-project"},
		},
		{name: "report missing reason", method: localapi.MethodContradictionReport, params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "idempotency_key": "report"}},
		{name: "report oversized reason", method: localapi.MethodContradictionReport, params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "reason": strings.Repeat("x", 2049), "idempotency_key": "report"}},
		{name: "report multibyte over bytes", method: localapi.MethodContradictionReport, params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "reason": strings.Repeat("界", 683), "idempotency_key": "report"}},
		{name: "report NUL reason", method: localapi.MethodContradictionReport, params: map[string]any{"workspace": "personal", "left_revision": "krev_left", "right_revision": "krev_right", "reason": "conflict\x00detail", "idempotency_key": "report"}},
		{name: "show authority injection", method: localapi.MethodContradictionShow, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "actor": "local-owner"}},
		{name: "list authority injection", method: localapi.MethodContradictionList, params: map[string]any{"workspace": "personal", "actor": "local-owner"}},
		{name: "list missing project", method: localapi.MethodContradictionList, params: map[string]any{"workspace": "personal"}},
		{name: "list zero limit", method: localapi.MethodContradictionList, params: map[string]any{"workspace": "personal", "project": "engine", "limit": 0}},
		{name: "list over limit", method: localapi.MethodContradictionList, params: map[string]any{"workspace": "personal", "project": "engine", "limit": 201}},
		{name: "list unsupported status", method: localapi.MethodContradictionList, params: map[string]any{"workspace": "personal", "project": "engine", "status": "merged"}},
		{name: "confirm authority injection", method: localapi.MethodContradictionConfirm, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "expected_state_revision": 1, "idempotency_key": "confirm", "actor": "local-owner"}},
		{name: "confirm missing revision", method: localapi.MethodContradictionConfirm, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "idempotency_key": "confirm"}},
		{name: "dismiss authority injection", method: localapi.MethodContradictionDismiss, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "expected_state_revision": 1, "idempotency_key": "dismiss", "actor": "local-owner"}},
		{name: "dismiss oversized note", method: localapi.MethodContradictionDismiss, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "expected_state_revision": 1, "idempotency_key": "dismiss", "note": strings.Repeat("x", 2049)}},
		{name: "dismiss multibyte over bytes", method: localapi.MethodContradictionDismiss, params: map[string]any{"workspace": "personal", "contradiction": "kcon_one", "expected_state_revision": 1, "idempotency_key": "dismiss", "note": strings.Repeat("界", 683)}},
		{name: "dispute authority injection", method: localapi.MethodKnowledgeDispute, params: map[string]any{"workspace": "personal", "knowledge_revision": "krev_one", "actor": "local-owner"}},
		{name: "dispute missing revision", method: localapi.MethodKnowledgeDispute, params: map[string]any{"workspace": "personal"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
			if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
				t.Fatalf("%s error=%#v, want non-retryable invalid_request", test.method, response.Error)
			}
		})
	}
}
