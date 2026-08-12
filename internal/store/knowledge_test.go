package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestKnowledgeSuccessorFailureRollsBackGovernanceAndIdempotency(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, project := initializeWorkTestProject(t, storage)
			task := createWorkTestTask(t, storage, workspace.ID, project.ID, "rollback source", "knowledge-rollback-source")
			predecessor := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "rollback-predecessor", "Current contract", "The predecessor remains current after an interrupted successor acceptance", "")
			predecessor, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
				WorkspaceIdentifier: workspace.ID, RevisionID: predecessor.Revision.ID,
				ExpectedStateRevision: predecessor.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: "accept-rollback-predecessor", CorrelationID: "request-accept-rollback-predecessor",
			})
			if err != nil {
				t.Fatalf("AcceptKnowledge(predecessor) error = %v", err)
			}
			successor := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "rollback-successor", "Replacement contract", "The successor becomes current only after its complete governance transaction commits", predecessor.Revision.ID)

			var eventsBefore, idempotencyBefore int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore); err != nil {
				t.Fatalf("count events before: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyBefore); err != nil {
				t.Fatalf("count idempotency before: %v", err)
			}
			injected := errors.New("injected knowledge governance interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := AcceptKnowledgeCommand{
				WorkspaceIdentifier: workspace.ID, RevisionID: successor.Revision.ID,
				ExpectedStateRevision: successor.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: "accept-rollback-successor-" + stage, CorrelationID: "request-accept-rollback-successor-" + stage,
			}
			if _, err := storage.AcceptKnowledge(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("AcceptKnowledge(successor) error = %v, want injected failure", err)
			}
			storage.mutationHook = nil

			predecessorAfter, err := storage.KnowledgeRevision(context.Background(), workspace.ID, predecessor.Revision.ID)
			if err != nil || predecessorAfter.CurrencyStatus != domain.KnowledgeCurrencyCurrent || predecessorAfter.StateRevision != predecessor.Revision.StateRevision {
				t.Fatalf("predecessor after rollback = %#v, %v", predecessorAfter, err)
			}
			successorAfter, err := storage.KnowledgeRevision(context.Background(), workspace.ID, successor.Revision.ID)
			if err != nil || successorAfter.ReviewStatus != domain.KnowledgeReviewProposed || successorAfter.CurrencyStatus != domain.KnowledgeCurrencyPending || successorAfter.StateRevision != successor.Revision.StateRevision {
				t.Fatalf("successor after rollback = %#v, %v", successorAfter, err)
			}
			predecessorChecks, err := storage.ListKnowledgeAuthorityChecks(context.Background(), workspace.ID, predecessor.Revision.ID)
			if err != nil || len(predecessorChecks) != 1 {
				t.Fatalf("predecessor authority after rollback = %#v, %v", predecessorChecks, err)
			}
			successorChecks, err := storage.ListKnowledgeAuthorityChecks(context.Background(), workspace.ID, successor.Revision.ID)
			if err != nil || len(successorChecks) != 0 {
				t.Fatalf("successor authority after rollback = %#v, %v", successorChecks, err)
			}
			var eventsAfter, idempotencyAfter int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter); err != nil {
				t.Fatalf("count events after: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyAfter); err != nil {
				t.Fatalf("count idempotency after: %v", err)
			}
			if eventsAfter != eventsBefore || idempotencyAfter != idempotencyBefore {
				t.Fatalf("rollback counts events=%d/%d idempotency=%d/%d", eventsAfter, eventsBefore, idempotencyAfter, idempotencyBefore)
			}
			committed, err := storage.AcceptKnowledge(context.Background(), command)
			if err != nil || committed.Revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
				t.Fatalf("AcceptKnowledge(successor retry) = %#v, %v", committed, err)
			}
		})
	}
}

func TestKnowledgeDatabaseRejectsMalformedIdentifiers(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "identifier source", "knowledge-identifier-source")
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "identifier-proposal", "Strict identifiers", "Every persisted identifier has a lowercase hexadecimal suffix", "")
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-identifier-proposal", CorrelationID: "request-accept-identifier-proposal",
	})
	if err != nil || accepted.AuthorityCheck == nil {
		t.Fatalf("AcceptKnowledge() = %#v, %v", accepted, err)
	}

	malformedItemID := "know_a" + strings.Repeat("!", 31)
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO knowledge_items(
id, workspace_id, project_id, task_scope_id, type, created_at, created_by, created_by_type)
SELECT ?, workspace_id, project_id, task_scope_id, type, created_at, created_by, created_by_type
FROM knowledge_items WHERE id = ?`, malformedItemID, accepted.Revision.ItemID); err == nil {
		t.Fatal("knowledge item identifier with a non-hexadecimal suffix unexpectedly persisted")
	}

	malformedRevisionID := "krev_a" + strings.Repeat("!", 31)
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO knowledge_revisions(
id, item_id, revision_number, state_revision, title, body, content_hash,
review_status, currency_status, confidence, verification_status, freshness_policy,
fresh_until, supersedes_revision_id, proposed_at, proposed_by, proposed_by_type)
SELECT ?, item_id, revision_number + 1000, 1, title, body, content_hash,
'proposed', 'pending', confidence, verification_status, freshness_policy,
fresh_until, NULL, proposed_at, proposed_by, proposed_by_type
FROM knowledge_revisions WHERE id = ?`, malformedRevisionID, accepted.Revision.ID); err == nil {
		t.Fatal("knowledge revision identifier with a non-hexadecimal suffix unexpectedly persisted")
	}

	malformedAuthorityID := "kauth_a" + strings.Repeat("!", 31)
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO knowledge_authority_checks(
id, workspace_id, revision_id, action, actor_id, actor_type, outcome, reason,
note, idempotency_key, request_hash, event_sequence, created_at)
SELECT ?, workspace_id, revision_id, 'reject', actor_id, actor_type, 'denied',
'state_policy', NULL, 'malformed-authority-id', request_hash, event_sequence, created_at
FROM knowledge_authority_checks WHERE id = ?`, malformedAuthorityID, accepted.AuthorityCheck.ID); err == nil {
		t.Fatal("knowledge authority identifier with a non-hexadecimal suffix unexpectedly persisted")
	}
}

func TestCanonicalKnowledgeLifecycleKeepsImmutableHistory(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source task", "knowledge-source-task")

	first := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "first-proposal", "Use stable ordering", "Sort identifiers before emitting output", "")
	if first.Revision.ItemID == "" || first.Revision.ReviewStatus != domain.KnowledgeReviewProposed || first.Revision.CurrencyStatus != domain.KnowledgeCurrencyPending || first.Revision.ProjectID != project.ID {
		t.Fatalf("proposed revision = %#v", first.Revision)
	}
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: first.Revision.ID,
		ExpectedStateRevision: first.Revision.StateRevision, DecisionNote: "owner approved",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "accept-first", CorrelationID: "request-accept-first",
	})
	if err != nil || accepted.Revision.ReviewStatus != domain.KnowledgeReviewAccepted || accepted.Revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent || accepted.Revision.StateRevision != 2 {
		t.Fatalf("AcceptKnowledge() = %#v, %v", accepted, err)
	}
	replayed, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: first.Revision.ID,
		ExpectedStateRevision: first.Revision.StateRevision, DecisionNote: "owner approved",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "accept-first", CorrelationID: "request-replayed-accept",
	})
	if err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("AcceptKnowledge(replay) = %#v, %v; want %#v", replayed, err, accepted)
	}

	successor := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "second-proposal", "Use stable ordering", "Sort canonical identifiers by byte value", accepted.Revision.ID)
	if successor.Revision.ItemID != accepted.Revision.ItemID || successor.Revision.RevisionNumber != 2 || successor.Revision.SupersedesRevisionID != accepted.Revision.ID {
		t.Fatalf("successor = %#v, predecessor = %#v", successor.Revision, accepted.Revision)
	}
	acceptedSuccessor, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: successor.Revision.ID,
		ExpectedStateRevision: successor.Revision.StateRevision, DecisionNote: "replace earlier wording",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "accept-second", CorrelationID: "request-accept-second",
	})
	if err != nil || acceptedSuccessor.Revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
		t.Fatalf("AcceptKnowledge(successor) = %#v, %v", acceptedSuccessor, err)
	}
	predecessor, err := storage.KnowledgeRevision(context.Background(), workspace.ID, accepted.Revision.ID)
	if err != nil || predecessor.CurrencyStatus != domain.KnowledgeCurrencySuperseded || predecessor.Body != accepted.Revision.Body || predecessor.StateRevision != 3 {
		t.Fatalf("superseded predecessor = %#v, %v", predecessor, err)
	}
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	replacementID, err := storage.KnowledgeCurrentSuccessorIDInTransaction(context.Background(), tx, workspace.ID, predecessor.ID)
	_ = tx.Rollback()
	if err != nil || replacementID != acceptedSuccessor.Revision.ID {
		t.Fatalf("KnowledgeCurrentSuccessorIDInTransaction() = %q, %v; want %q", replacementID, err, acceptedSuccessor.Revision.ID)
	}
	listed, err := storage.ListKnowledge(context.Background(), ListKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListKnowledge() = %#v, %v", listed, err)
	}
	detail, err := storage.KnowledgeDetail(context.Background(), workspace.ID, acceptedSuccessor.Revision.ID)
	if err != nil || len(detail.AuthorityChecks) != 1 || detail.AuthorityChecks[0].Outcome != domain.KnowledgeAuthorityAllowed {
		t.Fatalf("KnowledgeDetail() = %#v, %v", detail, err)
	}
	if _, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: acceptedSuccessor.Revision.ID,
		ExpectedStateRevision: acceptedSuccessor.Revision.StateRevision, Reason: "source contract changed",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-second", CorrelationID: "request-stale-second",
	}); err != nil {
		t.Fatalf("MarkKnowledgeStale() error = %v", err)
	}
	stale, _ := storage.KnowledgeRevision(context.Background(), workspace.ID, acceptedSuccessor.Revision.ID)
	if stale.CurrencyStatus != domain.KnowledgeCurrencyStale || stale.StaleReason != "source contract changed" {
		t.Fatalf("stale revision = %#v", stale)
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE knowledge_revisions SET body = ? WHERE id = ?", "rewritten", stale.ID); err == nil {
		t.Fatal("immutable knowledge body update unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE knowledge_revisions SET decision_note = ? WHERE id = ?", "rewritten decision", stale.ID); err == nil {
		t.Fatal("same-state knowledge governance metadata rewrite unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE knowledge_revisions SET review_status = 'rejected', currency_status = 'pending', state_revision = state_revision + 1 WHERE id = ?", stale.ID); err == nil {
		t.Fatal("illegal knowledge governance transition unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), `INSERT INTO knowledge_sources(
revision_id, ordinal, source_type, source_id, source_revision, role) VALUES (?, 1, 'task', ?, 1, 'supporting')`,
		stale.ID, "task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("inserting provenance into a sealed knowledge revision unexpectedly succeeded")
	}
	sealed, err := storage.KnowledgeRevision(context.Background(), workspace.ID, stale.ID)
	if err != nil || !reflect.DeepEqual(sealed.Sources, stale.Sources) {
		t.Fatalf("sealed knowledge sources = %#v, %v; want %#v", sealed.Sources, err, stale.Sources)
	}
	rejectedProposal := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "rejected-proposal", "Rejected option", "Do not promote this option", "")
	rejected, err := storage.RejectKnowledge(context.Background(), RejectKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: rejectedProposal.Revision.ID,
		ExpectedStateRevision: rejectedProposal.Revision.StateRevision, DecisionNote: "not canonical",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "reject-option", CorrelationID: "request-reject-option",
	})
	if err != nil || rejected.Revision.ReviewStatus != domain.KnowledgeReviewRejected || rejected.Revision.CurrencyStatus != domain.KnowledgeCurrencyPending || rejected.Revision.StateRevision != 2 {
		t.Fatalf("RejectKnowledge() = %#v, %v", rejected, err)
	}
}

func TestKnowledgeAuthorityDenialCommitsAuditAndPreservesProposal(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "knowledge authority")
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "knowledge-authority",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "running"}},
	}, "knowledge-authority-run")
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, assigned.Task.ID, "authority-proposal", "Owner-only decision", "Only the local owner can accept this revision", "")
	command := AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, DecisionNote: "agent attempted acceptance",
		Actor:          domain.KnowledgeActor{ID: run.Run.ID, Type: domain.KnowledgeActorAgentRun},
		IdempotencyKey: "denied-agent-accept", CorrelationID: "request-denied-agent-accept",
	}
	result, err := storage.AcceptKnowledge(context.Background(), command)
	if ErrorCode(err) != CodeKnowledgeDenied || result.AuthorityCheck == nil || result.AuthorityCheck.Outcome != domain.KnowledgeAuthorityDenied || result.EventSequence < 1 {
		t.Fatalf("AcceptKnowledge(unauthorized) = %#v, %v", result, err)
	}
	after, err := storage.KnowledgeRevision(context.Background(), workspace.ID, proposed.Revision.ID)
	if err != nil || after.ReviewStatus != domain.KnowledgeReviewProposed || after.CurrencyStatus != domain.KnowledgeCurrencyPending || after.StateRevision != proposed.Revision.StateRevision {
		t.Fatalf("proposal after denied acceptance = %#v, %v", after, err)
	}
	checks, err := storage.ListKnowledgeAuthorityChecks(context.Background(), workspace.ID, proposed.Revision.ID)
	if err != nil || len(checks) != 1 || checks[0].Actor.ID != run.Run.ID || checks[0].Reason != domain.KnowledgeAuthorityReasonNotOwner {
		t.Fatalf("denial checks = %#v, %v", checks, err)
	}
	replayed, replayErr := storage.AcceptKnowledge(context.Background(), command)
	if ErrorCode(replayErr) != CodeKnowledgeDenied || replayed.AuthorityCheck == nil || replayed.AuthorityCheck.ID != result.AuthorityCheck.ID {
		t.Fatalf("AcceptKnowledge(denied replay) = %#v, %v", replayed, replayErr)
	}
	checks, _ = storage.ListKnowledgeAuthorityChecks(context.Background(), workspace.ID, proposed.Revision.ID)
	if len(checks) != 1 {
		t.Fatalf("denied replay created %d checks, want one", len(checks))
	}
}

func TestKnowledgeValidationAndExpectedStateRevision(t *testing.T) {
	t.Parallel()
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return clock }})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "validation source", "knowledge-validation-source")

	invalid := ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding,
		Title: "expired", Body: "this proposal is already expired",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshExpiresAt, FreshUntil: clock.Add(-time.Minute).Format(time.RFC3339Nano),
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: task.Task.ID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "expired-proposal", CorrelationID: "request-expired-proposal",
	}
	if _, err := storage.ProposeKnowledge(context.Background(), invalid); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("ProposeKnowledge(expired) error = %v, code = %q", err, ErrorCode(err))
	}
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "revision-proposal", "State revision", "Require exact governance state", "")
	_, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision + 1, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "stale-state-accept", CorrelationID: "request-stale-state-accept",
	})
	if ErrorCode(err) != CodeRevisionConflict {
		t.Fatalf("AcceptKnowledge(stale state) error = %v, code = %q", err, ErrorCode(err))
	}
	after, _ := storage.KnowledgeRevision(context.Background(), workspace.ID, proposed.Revision.ID)
	if after.ReviewStatus != domain.KnowledgeReviewProposed || after.StateRevision != 1 {
		t.Fatalf("proposal changed after stale state decision: %#v", after)
	}
}

func TestKnowledgeAcceptsOnlyConcludedAcceptedMeetingProvenance(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	proposedMeeting, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "run-knowledge-source-meeting", CorrelationID: "request-run-knowledge-source-meeting",
	})
	if err != nil || proposedMeeting.Detail.Proposal == nil {
		t.Fatalf("RunMeeting() = %#v, %v", proposedMeeting, err)
	}
	input := ProposeKnowledgeCommand{
		WorkspaceIdentifier: setup.workspace.ID, Type: domain.KnowledgeTypeFinding,
		Title: "Meeting resolution", Body: "The accepted resolution sequences the overlapping tasks.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources: []domain.KnowledgeSourceInput{
			{Type: domain.KnowledgeSourceMeetingProposal, ID: proposedMeeting.Detail.Proposal.ID, Role: domain.KnowledgeSourcePrimary},
			{Type: domain.KnowledgeSourceMeeting, ID: proposedMeeting.Detail.Meeting.ID, Role: domain.KnowledgeSourceSupporting},
		},
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "meeting-provenance-before-accept", CorrelationID: "request-meeting-provenance-before-accept",
	}
	if _, err := storage.ProposeKnowledge(context.Background(), input); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("ProposeKnowledge(unaccepted meeting) error = %v, code = %q", err, ErrorCode(err))
	}
	acceptedMeeting, err := storage.AcceptMeeting(context.Background(), AcceptMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: proposedMeeting.Detail.Meeting.ID,
		ExpectedRevision: proposedMeeting.Detail.Meeting.Revision, DecisionNote: "approved as knowledge provenance",
		IdempotencyKey: "accept-knowledge-source-meeting", CorrelationID: "request-accept-knowledge-source-meeting",
	})
	if err != nil || acceptedMeeting.Detail.Proposal == nil {
		t.Fatalf("AcceptMeeting() = %#v, %v", acceptedMeeting, err)
	}
	input.IdempotencyKey = "meeting-provenance-after-accept"
	input.CorrelationID = "request-meeting-provenance-after-accept"
	result, err := storage.ProposeKnowledge(context.Background(), input)
	if err != nil || result.Revision.ProjectID != setup.project.ID || len(result.Revision.Sources) != 2 {
		t.Fatalf("ProposeKnowledge(meeting provenance) = %#v, %v", result, err)
	}
	if result.Revision.Sources[0].ID != acceptedMeeting.Detail.Proposal.ID || result.Revision.Sources[0].Revision != acceptedMeeting.Detail.Proposal.Revision || result.Revision.Sources[1].Revision != acceptedMeeting.Detail.Meeting.Revision {
		t.Fatalf("frozen meeting sources = %#v", result.Revision.Sources)
	}
}

func proposeTaskKnowledge(t *testing.T, storage *Store, workspaceID, taskID, key, title, body, supersedes string) KnowledgeMutationResult {
	t.Helper()
	result, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, Type: domain.KnowledgeTypeDecision,
		Title: title, Body: body, Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus:   domain.KnowledgeVerificationSupported,
		FreshnessPolicy:      domain.KnowledgeFreshUntilSuperseded,
		Sources:              []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: taskID, Role: domain.KnowledgeSourcePrimary}},
		SupersedesRevisionID: supersedes, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: key, CorrelationID: "request-" + key,
	})
	if err != nil {
		t.Fatalf("ProposeKnowledge(%s) error = %v", key, err)
	}
	return result
}
