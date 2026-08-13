package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store/dbgen"
)

func TestContextDeltaAcceptedDecisionRefreshPendingAckAndNextCursor(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "accepted delta")

	initial, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "initial-refresh", CorrelationID: "request-initial-refresh",
	})
	if err != nil || initial.Status != domain.ContextRefreshUpToDate || initial.Delta != nil || initial.Chain.Revision < 2 {
		t.Fatalf("RefreshContext(initial) = %#v, %v", initial, err)
	}
	if initial.ScannedFromEventSequence != initial.Chain.BaseEventSequence || initial.ScannedThroughEventSequence < initial.ScannedFromEventSequence {
		t.Fatalf("initial refresh interval = (%d,%d], base=%d", initial.ScannedFromEventSequence, initial.ScannedThroughEventSequence, initial.Chain.BaseEventSequence)
	}
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "delta-decision", "Delta decision", "Use the exact accepted live contract.", "")
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-delta-decision", CorrelationID: "request-accept-delta-decision",
	})
	if err != nil {
		t.Fatalf("AcceptKnowledge() error = %v", err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "decision-refresh", CorrelationID: "request-decision-refresh",
	})
	if err != nil || created.Status != domain.ContextRefreshCreated || created.Delta == nil || created.EventSequence <= 0 {
		t.Fatalf("RefreshContext(decision) = %#v, %v", created, err)
	}
	delta := *created.Delta
	if delta.Sequence != 1 || delta.ParentDeltaID != "" || delta.FromEventSequence != initial.ScannedThroughEventSequence ||
		delta.ThroughEventSequence < accepted.EventSequence || delta.WorkspaceID != workspace.ID || delta.TaskID != run.TaskID ||
		len(delta.Changes) != 1 || delta.Changes[0].Kind != domain.ContextDeltaKnowledgeAccepted ||
		delta.Changes[0].Knowledge == nil || delta.Changes[0].Knowledge.ID != accepted.Revision.ID ||
		delta.ByteSize <= 0 || delta.ByteSize > maximumContextDeltaBytes || delta.ContentHash == "" {
		t.Fatalf("created context delta = %#v", delta)
	}
	pending, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "pending-refresh", CorrelationID: "request-pending-refresh",
	})
	if err != nil || pending.Status != domain.ContextRefreshPending || pending.Delta == nil || !reflect.DeepEqual(*pending.Delta, delta) {
		t.Fatalf("RefreshContext(pending) = %#v, %v; want delta %#v", pending, err, delta)
	}
	fetched, err := storage.FetchRunContextDelta(context.Background(), run.ID)
	if err != nil || fetched.Status != domain.ContextDeltaPending || fetched.Delta == nil || fetched.Delta.ID != delta.ID {
		t.Fatalf("FetchRunContextDelta() = %#v, %v", fetched, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: delta.ID, ExpectedSequence: delta.Sequence + 1, IdempotencyKey: "ack-wrong-sequence",
	}); ErrorCode(err) != CodeInvalidContextDelta {
		t.Fatalf("AcknowledgeRunContextDelta(wrong sequence) error=%v code=%s", err, ErrorCode(err))
	}
	ack, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: delta.ID, ExpectedSequence: delta.Sequence, IdempotencyKey: "ack-decision-delta",
	})
	if err != nil || ack.DeltaID != delta.ID || ack.Sequence != 1 || ack.AcknowledgedBy != run.ID || ack.EventSequence <= 0 {
		t.Fatalf("AcknowledgeRunContextDelta() = %#v, %v", ack, err)
	}
	noOp, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "post-ack-noop", CorrelationID: "request-post-ack-noop"})
	if err != nil || noOp.Status != domain.ContextRefreshUpToDate || noOp.Delta != nil {
		t.Fatalf("RefreshContext(post-ack no-op)=%#v,%v", noOp, err)
	}
	later := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "later-delta-decision", "Later decision", "Created after a cursor gap.", "")
	if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: later.Revision.ID, ExpectedStateRevision: later.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-later-delta", CorrelationID: "request-accept-later-delta"}); err != nil {
		t.Fatal(err)
	}
	second, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "later-delta-refresh", CorrelationID: "request-later-delta-refresh"})
	if err != nil || second.Delta == nil || second.Delta.Sequence != 2 || second.Delta.FromEventSequence != noOp.ScannedThroughEventSequence {
		t.Fatalf("RefreshContext(after no-op gap)=%#v,%v", second, err)
	}
	if _, err := storage.FetchRunContextDelta(context.Background(), run.ID); err != nil {
		t.Fatalf("FetchRunContextDelta(after no-op gap)=%v", err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: second.Delta.ID, ExpectedSequence: second.Delta.Sequence, IdempotencyKey: "ack-later-delta",
	}); err != nil {
		t.Fatal(err)
	}
	ackReplay, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: delta.ID, ExpectedSequence: delta.Sequence, IdempotencyKey: "ack-decision-delta",
	})
	if err != nil || !ackReplay.Replayed || ackReplay.ID != ack.ID || ackReplay.EventSequence != ack.EventSequence {
		t.Fatalf("AcknowledgeRunContextDelta(replay) = %#v, %v", ackReplay, err)
	}
	after, err := storage.FetchRunContextDelta(context.Background(), run.ID)
	if err != nil || after.Status != domain.ContextDeltaNonePending || after.Delta != nil || after.Chain.LastAcknowledgedDeltaID != second.Delta.ID {
		t.Fatalf("FetchRunContextDelta(after ack) = %#v, %v", after, err)
	}
	list, err := storage.ListContextDeltas(context.Background(), ListContextDeltasQuery{WorkspaceIdentifier: workspace.ID, RunID: run.ID, Limit: 1})
	if err != nil || len(list.Deltas) != 1 || list.Deltas[0].ID != delta.ID || !list.HasMore || list.NextSequence != 1 {
		t.Fatalf("ListContextDeltas() = %#v, %v", list, err)
	}
	explanation, err := storage.ExplainContextDelta(context.Background(), workspace.ID, delta.ID)
	if err != nil || explanation.DeltaID != delta.ID || !reflect.DeepEqual(explanation.ChangeKinds, []string{domain.ContextDeltaKnowledgeAccepted}) {
		t.Fatalf("ExplainContextDelta() = %#v, %v", explanation, err)
	}
}

func TestContextDeltaEventBoundsIgnoreInertAndRejectUnknownExactScope(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "event bounds")
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	for index := 0; index < maximumContextDeltaEvents+1; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "message", fmt.Sprintf("msg_%032x", index+1), 2,
			messageDeliveredEvent, fmt.Sprintf("inert-%d", index), now, map[string]any{"run_id": run.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	upToDate, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "ignore-inert", CorrelationID: "request-ignore-inert",
	})
	if err != nil || upToDate.Status != domain.ContextRefreshUpToDate || upToDate.RebaseReason != "" {
		t.Fatalf("RefreshContext(1001 inert) = %#v, %v", upToDate, err)
	}
	tx, err = storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumContextDeltaEvents+1; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "checkout", run.CheckoutID, int64(100+index),
			checkoutObserved, fmt.Sprintf("inert-checkout-%d", index), now, map[string]any{"branch": fmt.Sprintf("agent/%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	observed, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "ignore-checkout-observed", CorrelationID: "request-ignore-checkout-observed",
	})
	if err != nil || observed.Status != domain.ContextRefreshUpToDate || observed.RebaseReason != "" {
		t.Fatalf("RefreshContext(1001 checkout observations) = %#v, %v", observed, err)
	}
	tx, err = storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", run.TaskID, 99,
		"task.safety_contract_changed", "unknown-critical", now, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rebase, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "unknown-critical", CorrelationID: "request-unknown-critical",
	})
	if err != nil || rebase.Status != domain.ContextRefreshRebaseRequired || rebase.RebaseReason != domain.ContextRebaseUnsupportedEventType || rebase.EventSequence <= 0 {
		t.Fatalf("RefreshContext(unknown critical) = %#v, %v", rebase, err)
	}
	knownExactTaskStore := openTestStore(t, t.TempDir(), Options{})
	knownExactTaskWorkspace, _, _, knownExactTaskRun := startContextDeltaTestRun(t, knownExactTaskStore, "known exact task events")
	tx, err = knownExactTaskStore.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now = knownExactTaskStore.nowText()
	for index := 0; index < maximumContextDeltaEvents+1; index++ {
		if _, err := appendEvent(context.Background(), tx, knownExactTaskWorkspace.ID, "task", knownExactTaskRun.TaskID, int64(100+index),
			"task.updated", fmt.Sprintf("known-exact-%d", index), now, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	knownExactTaskResult, err := knownExactTaskStore.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: knownExactTaskWorkspace.ID,
		RunID: knownExactTaskRun.ID, IdempotencyKey: "known-exact-task-events", CorrelationID: "request-known-exact-task-events"})
	if err != nil || knownExactTaskResult.Status != domain.ContextRefreshUpToDate || knownExactTaskResult.RebaseReason != "" {
		t.Fatalf("RefreshContext(1001 known exact-task events) = %#v, %v", knownExactTaskResult, err)
	}
}

func TestContextDeltaUnknownEventForRepresentedAcknowledgedMessageFailsClosed(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, run := startContextDeltaTestRun(t, storage, "represented message unknown")
	sent, err := storage.SendMessage(context.Background(), SendMessageCommand{WorkspaceIdentifier: workspace.ID,
		RecipientAgent: agent.ID, ProjectIdentifier: project.ID, TaskID: run.TaskID,
		Kind: domain.MessageInform, Subject: "Authority update", Body: "This message enters live context.",
		IdempotencyKey: "send-represented-message", CorrelationID: "request-send-represented-message"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-represented-message", CorrelationID: "request-refresh-represented-message"})
	if err != nil || created.Delta == nil {
		t.Fatalf("RefreshContext(message)=%#v,%v", created, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{RunID: run.ID,
		DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-represented-message-delta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AcknowledgeRunMessage(context.Background(), run.ID, sent.Value.Message.ID, "ack-represented-message"); err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "message", sent.Value.Message.ID, 2,
		"message.redacted", "unknown-represented-message", storage.nowText(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rebase, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-unknown-represented-message", CorrelationID: "request-refresh-unknown-represented-message"})
	if err != nil || rebase.Status != domain.ContextRefreshRebaseRequired || rebase.RebaseReason != domain.ContextRebaseUnsupportedEventType {
		t.Fatalf("RefreshContext(unknown represented message)=%#v,%v", rebase, err)
	}
}

func TestContextDeltaSameWindowCrossTaskContradictionEventsAreBounded(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, _, run := startContextDeltaTestRun(t, storage, "cross-task contradiction overflow")
	other := createWorkTestTask(t, storage, workspace.ID, project.ID, "other contradiction task", "overflow-other-task")
	broad := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "overflow-broad", "Overflow broad", "")
	wrongTask := acceptContextDecision(t, storage, workspace.ID, other.Task.ID, "overflow-wrong-task", "Overflow wrong task", other.Task.ID)
	contradiction := openContradiction(t, storage, workspace.ID, broad.Revision.ID, wrongTask.Revision.ID, "overflow-cross-task")
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	for index := 0; index < maximumContextDeltaEvents; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "knowledge_contradiction", contradiction.ID,
			int64(100+index), contradictionConfirmedEvent, fmt.Sprintf("cross-task-overflow-%d", index), now,
			map[string]any{"project_id": project.ID, "left_revision_id": broad.Revision.ID, "right_revision_id": wrongTask.Revision.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-cross-task-overflow", CorrelationID: "request-refresh-cross-task-overflow"})
	if err != nil || result.Status != domain.ContextRefreshRebaseRequired || result.RebaseReason != domain.ContextRebaseEventWindowExceeded {
		t.Fatalf("RefreshContext(cross-task contradiction overflow)=%#v,%v", result, err)
	}
}

func TestContextDeltaEventWindowIgnoresExpiredNeverRepresentedDecisions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "expired event window")
	proposed, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		TaskScopeID: run.TaskID, Type: domain.KnowledgeTypeDecision, Title: "Never represented expiry", Body: "Expires before refresh.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshExpiresAt, FreshUntil: now.Add(time.Nanosecond).Format(time.RFC3339Nano),
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: run.TaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "propose-never-represented-expiry", CorrelationID: "request-propose-never-represented-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-never-represented-expiry", CorrelationID: "request-accept-never-represented-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Nanosecond)
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumContextDeltaEvents; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "knowledge_revision", accepted.Revision.ID,
			int64(100+index), knowledgeAcceptedEvent, fmt.Sprintf("expired-accepted-%d", index), storage.nowText(), map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-expired-event-window", CorrelationID: "request-refresh-expired-event-window"})
	if err != nil || result.Status != domain.ContextRefreshUpToDate || result.Delta != nil || result.RebaseReason != "" {
		t.Fatalf("RefreshContext(expired event window)=%#v,%v", result, err)
	}
}

func TestContextDeltaFreshnessPrefilterPreservesOneNanosecond(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "one nanosecond fresh")
	proposed, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		TaskScopeID: run.TaskID, Type: domain.KnowledgeTypeDecision, Title: "One nanosecond fresh", Body: "Still eligible at evaluated_at.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshExpiresAt, FreshUntil: now.Add(time.Nanosecond).Format(time.RFC3339Nano),
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: run.TaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "propose-one-nanosecond-fresh", CorrelationID: "request-propose-one-nanosecond-fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-one-nanosecond-fresh", CorrelationID: "request-accept-one-nanosecond-fresh"}); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-one-nanosecond-fresh", CorrelationID: "request-refresh-one-nanosecond-fresh"})
	if err != nil || result.Delta == nil || len(result.Delta.Changes) != 1 || result.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeAccepted {
		t.Fatalf("RefreshContext(one nanosecond fresh)=%#v,%v", result, err)
	}
}

func TestContextDeltaEventWindowIgnoresTerminalUnseenContradictions(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "terminal contradiction window")
	left := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "terminal-window-left", "Terminal window left", run.TaskID)
	right := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "terminal-window-right", "Terminal window right", run.TaskID)
	contradiction := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "terminal-window")
	if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: contradiction.ID, ExpectedStateRevision: contradiction.StateRevision,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-terminal-window", CorrelationID: "request-dismiss-terminal-window",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: left.Revision.ID, ExpectedStateRevision: left.Revision.StateRevision, Reason: "remove unseen left",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-terminal-window-left", CorrelationID: "request-stale-terminal-window-left"}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: right.Revision.ID, ExpectedStateRevision: right.Revision.StateRevision, Reason: "remove unseen right",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-terminal-window-right", CorrelationID: "request-stale-terminal-window-right"}); err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumContextDeltaEvents; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "knowledge_contradiction", contradiction.ID,
			int64(100+index), contradictionImportedEvent, fmt.Sprintf("terminal-imported-%d", index), storage.nowText(), map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-terminal-contradiction-window", CorrelationID: "request-refresh-terminal-contradiction-window"})
	if err != nil || result.Status != domain.ContextRefreshUpToDate || result.Delta != nil || result.RebaseReason != "" {
		t.Fatalf("RefreshContext(terminal contradiction window)=%#v,%v", result, err)
	}
}

func TestContextDeltaUnknownEventOnBothApplicableTerminalContradictionFailsClosed(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, checkout, assigned := initializeRunTest(t, storage, "unknown terminal contradiction")
	left := acceptContextDecision(t, storage, workspace.ID, assigned.Task.ID, "unknown-terminal-left", "Unknown terminal left", assigned.Task.ID)
	right := acceptContextDecision(t, storage, workspace.ID, assigned.Task.ID, "unknown-terminal-right", "Unknown terminal right", assigned.Task.ID)
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "unknown-terminal", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	createdRun, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID,
		CheckoutIdentifier: checkout.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "run-unknown-terminal", CorrelationID: "request-run-unknown-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := storage.MarkRunStarting(context.Background(), createdRun.Detail.Run.ID, "worker-unknown-terminal")
	if err != nil {
		t.Fatal(err)
	}
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "proposed but not open", Actor: OwnerKnowledgeActor(), IdempotencyKey: "report-unknown-terminal", CorrelationID: "request-report-unknown-terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "knowledge_contradiction", reported.Detail.Contradiction.ID, 99,
		"contradiction.security_reclassified", "unknown-terminal-security", storage.nowText(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-unknown-terminal", CorrelationID: "request-refresh-unknown-terminal"})
	if err != nil || result.Status != domain.ContextRefreshRebaseRequired || result.RebaseReason != domain.ContextRebaseUnsupportedEventType {
		t.Fatalf("RefreshContext(unknown terminal contradiction)=%#v,%v", result, err)
	}
}

func TestContextDeltaBaseDriftPrecedesRelevantEventOverflow(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "drift precedence")
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	for index := 0; index < maximumContextDeltaEvents+1; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", run.TaskID, int64(100+index),
			"task.updated", fmt.Sprintf("relevant-%d", index), now, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := storage.db.QueryRow("SELECT revision FROM tasks WHERE id = ?", run.TaskID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	title := "contract drift wins"
	if _, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: run.TaskID,
		Title: &title, ExpectedRevision: revision, IdempotencyKey: "drift-before-overflow", CorrelationID: "request-drift-before-overflow"}); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-drift-before-overflow", CorrelationID: "request-refresh-drift-before-overflow"})
	if err != nil || result.Status != domain.ContextRefreshRebaseRequired || result.RebaseReason != domain.ContextRebaseBaseContractChanged {
		t.Fatalf("RefreshContext()=%#v,%v", result, err)
	}
}

func TestContextDeltaRefreshRollsBackAtMutationBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, run := startContextDeltaTestRun(t, storage, "rollback "+stage)
			proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "rollback-decision", "Rollback decision", "Atomic delta.", "")
			if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
				RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: "accept-rollback", CorrelationID: "request-accept-rollback"}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected delta interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
				IdempotencyKey: "atomic-refresh", CorrelationID: "request-atomic-refresh"}
			if _, err := storage.RefreshContext(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("RefreshContext() error=%v", err)
			}
			var deltas, builtEvents int
			var status string
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM context_deltas").Scan(&deltas); err != nil {
				t.Fatal(err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE type = ?", contextDeltaBuiltEvent).Scan(&builtEvents); err != nil {
				t.Fatal(err)
			}
			if err := storage.db.QueryRow("SELECT status FROM run_context_delta_state WHERE run_id = ?", run.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if deltas != 0 || builtEvents != 0 || status != "ready" {
				t.Fatalf("rollback left deltas=%d events=%d status=%s", deltas, builtEvents, status)
			}
			storage.mutationHook = nil
			result, err := storage.RefreshContext(context.Background(), command)
			if err != nil || result.Status != domain.ContextRefreshCreated {
				t.Fatalf("RefreshContext(retry)=%#v,%v", result, err)
			}
		})
	}
}

func TestContextDeltaAckAndRebaseRollBackAtMutationBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run("ack-"+stage, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, run := startContextDeltaTestRun(t, storage, "ack rollback "+stage)
			proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "ack-rollback-decision", "Ack rollback", "Atomic acknowledgement.", "")
			if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
				RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: "accept-ack-rollback", CorrelationID: "request-accept-ack-rollback"}); err != nil {
				t.Fatal(err)
			}
			created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
				IdempotencyKey: "refresh-ack-rollback", CorrelationID: "request-refresh-ack-rollback"})
			if err != nil || created.Delta == nil {
				t.Fatalf("RefreshContext()=%#v,%v", created, err)
			}
			injected := errors.New("injected ack interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := AcknowledgeContextDeltaCommand{RunID: run.ID, DeltaID: created.Delta.ID,
				ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "atomic-ack"}
			if _, err := storage.AcknowledgeRunContextDelta(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("AcknowledgeRunContextDelta() error=%v", err)
			}
			var acknowledgements, events int
			var status string
			_ = storage.db.QueryRow("SELECT COUNT(*) FROM context_delta_acknowledgements").Scan(&acknowledgements)
			_ = storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE type = ?", contextDeltaAcknowledgedEvent).Scan(&events)
			_ = storage.db.QueryRow("SELECT status FROM run_context_delta_state WHERE run_id = ?", run.ID).Scan(&status)
			if acknowledgements != 0 || events != 0 || status != "pending_ack" {
				t.Fatalf("ack rollback left acknowledgements=%d events=%d status=%s", acknowledgements, events, status)
			}
			storage.mutationHook = nil
			if _, err := storage.AcknowledgeRunContextDelta(context.Background(), command); err != nil {
				t.Fatal(err)
			}
		})

		t.Run("rebase-"+stage, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, run := startContextDeltaTestRun(t, storage, "rebase rollback "+stage)
			var revision int64
			if err := storage.db.QueryRow("SELECT revision FROM tasks WHERE id = ?", run.TaskID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			title := "drift for atomic rebase"
			if _, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: run.TaskID,
				Title: &title, ExpectedRevision: revision, IdempotencyKey: "drift-rebase-rollback", CorrelationID: "request-drift-rebase-rollback"}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected rebase interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
				IdempotencyKey: "atomic-rebase", CorrelationID: "request-atomic-rebase"}
			if _, err := storage.RefreshContext(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("RefreshContext(rebase) error=%v", err)
			}
			var events int
			var status string
			_ = storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE type = ?", contextDeltaRebaseRequiredEvent).Scan(&events)
			_ = storage.db.QueryRow("SELECT status FROM run_context_delta_state WHERE run_id = ?", run.ID).Scan(&status)
			if events != 0 || status != "ready" {
				t.Fatalf("rebase rollback left events=%d status=%s", events, status)
			}
			storage.mutationHook = nil
			result, err := storage.RefreshContext(context.Background(), command)
			if err != nil || result.Status != domain.ContextRefreshRebaseRequired {
				t.Fatalf("RefreshContext(rebase retry)=%#v,%v", result, err)
			}
		})
	}
}

func TestVersionFourContextPacketCanonicalSnapshotsBlockForgedRunBinding(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, checkout, assigned := initializeRunTest(t, storage, "forged v4 bind")
	built, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{WorkspaceIdentifier: workspace.ID,
		TaskID: assigned.Task.ID, AgentIdentifier: agent.ID, CheckoutIdentifier: checkout.ID,
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "build-canonical", CorrelationID: "request-build-canonical"})
	if err != nil {
		t.Fatal(err)
	}
	forged := built.Value
	forged.ID = "ctx_dddddddddddddddddddddddddddddddd"
	forged.Role.Name = "forged authority"
	if err := storage.db.QueryRow("SELECT COALESCE(MAX(sequence),0) FROM events").Scan(&forged.AsOfEventSequence); err != nil {
		t.Fatal(err)
	}
	packetJSON, err := finalizeContextPacket(&forged)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := dbgen.New(tx).InsertContextPacket(context.Background(), dbgen.InsertContextPacketParams{ID: forged.ID,
		WorkspaceID: forged.WorkspaceID, ProjectID: forged.ProjectID, TaskID: forged.TaskID, AgentID: forged.AgentID,
		CheckoutID: forged.CheckoutID, PacketJson: string(packetJSON), ContentHash: forged.ContentHash,
		ByteSize: int64(forged.ByteSize), CreatedAt: forged.CreatedAt, CreatedBy: forged.CreatedBy}); err == nil {
		t.Fatal("forged canonical packet insert unexpectedly succeeded")
	}
	_ = tx.Rollback()

	missing := built.Value
	missing.ID = "ctx_cccccccccccccccccccccccccccccccc"
	missingJSON, err := finalizeContextPacket(&missing)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(missingJSON, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw["role"].(map[string]any), "name")
	for range 4 {
		missingJSON, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		raw["byte_size"] = len(missingJSON)
		budget := raw["budget"].(map[string]any)
		total := budget["total"].(map[string]any)
		total["used_bytes"], total["remaining_bytes"] = len(missingJSON), maximumContextBytes-len(missingJSON)
	}
	missingJSON, _ = json.Marshal(raw)
	var semanticHash string
	if err := storage.db.QueryRow(`SELECT 'sha256:' || lower(hex(sha256(CAST(json_set(?,
'$.id','', '$.content_hash','', '$.created_at','', '$.created_by','', '$.byte_size',0,
'$.budget.total.used_bytes',0, '$.budget.total.remaining_bytes',32768) AS BLOB))))`, string(missingJSON)).Scan(&semanticHash); err != nil {
		t.Fatal(err)
	}
	raw["content_hash"] = semanticHash
	missingJSON, _ = json.Marshal(raw)
	storedBytes, ok := raw["byte_size"].(int)
	if !ok || storedBytes != len(missingJSON) {
		t.Fatalf("missing-field packet byte accounting did not remain stable: %v/%d", raw["byte_size"], len(missingJSON))
	}
	if _, err := storage.db.Exec(`INSERT INTO context_packets(id,workspace_id,project_id,task_id,agent_id,checkout_id,packet_json,content_hash,byte_size,created_at,created_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, missing.ID, missing.WorkspaceID, missing.ProjectID, missing.TaskID, missing.AgentID, missing.CheckoutID,
		string(missingJSON), semanticHash, len(missingJSON), missing.CreatedAt, missing.CreatedBy); err == nil {
		t.Fatal("self-hashed packet missing required nested role name unexpectedly succeeded")
	}
}

func TestContextDeltaCommittedPartialAcknowledgementFailsClosed(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "partial ack")
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "partial-ack-decision", "Partial ack", "Must fail closed.", "")
	if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-partial-ack", CorrelationID: "request-accept-partial-ack"}); err != nil {
		t.Fatal(err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "build-partial-ack", CorrelationID: "request-build-partial-ack"})
	if err != nil || created.Delta == nil {
		t.Fatalf("RefreshContext()=%#v,%v", created, err)
	}
	delta := *created.Delta
	ackID, now := "cdack_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", storage.nowText()
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	eventSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "context_delta_acknowledgement", ackID, 1,
		contextDeltaAcknowledgedEvent, "raw-partial-ack", now, run.ID, "agent_run", map[string]any{
			"run_id": run.ID, "context_packet_id": delta.ContextPacketID, "delta_id": delta.ID, "acknowledgement_id": ackID,
			"state_revision": created.StateRevision + 1, "sequence": delta.Sequence, "through_event_sequence": delta.ThroughEventSequence})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbgen.New(tx).InsertContextDeltaAcknowledgement(context.Background(), dbgen.InsertContextDeltaAcknowledgementParams{
		ID: ackID, RunID: run.ID, ContextPacketID: delta.ContextPacketID, DeltaID: delta.ID, Sequence: delta.Sequence,
		AcknowledgedAt: now, AcknowledgedBy: run.ID, IdempotencyKey: "raw-partial-ack", RequestHash: strings.Repeat("a", 64), EventSequence: eventSequence,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.FetchRunContextDelta(context.Background(), run.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("FetchRunContextDelta(partial ack) error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: delta.ID, ExpectedSequence: delta.Sequence, IdempotencyKey: "retry-partial-ack",
	}); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("AcknowledgeRunContextDelta(partial ack) error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "build-partial-ack", CorrelationID: "request-build-partial-ack"}); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("RefreshContext(replay after partial ack) error=%v code=%s", err, ErrorCode(err))
	}
}

func TestContextDeltaDisputeTombstonesReofferOnlyPostBaseCandidates(t *testing.T) {
	t.Parallel()
	t.Run("post-base accepted candidates survive fold and reoffer on closure", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, run := startContextDeltaTestRun(t, storage, "dispute tombstone")
		left := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "tombstone-left", "Left candidate", run.TaskID)
		right := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "tombstone-right", "Right candidate", run.TaskID)
		open := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "tombstone-open")

		created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-tombstones", CorrelationID: "request-refresh-tombstones"})
		if err != nil || created.Delta == nil {
			t.Fatalf("RefreshContext(tombstones)=%#v,%v", created, err)
		}
		withdrawn := make(map[string]bool)
		opened := false
		for _, change := range created.Delta.Changes {
			if change.Kind == domain.ContextDeltaKnowledgeWithdrawn && change.Withdrawal != nil && change.Withdrawal.Reason == "disputed" {
				withdrawn[change.EntityID] = true
			}
			opened = opened || change.Kind == domain.ContextDeltaContradictionOpened
		}
		if !opened || !withdrawn[left.Revision.ID] || !withdrawn[right.Revision.ID] {
			t.Fatalf("suppression delta changes=%#v", created.Delta.Changes)
		}
		if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
			RunID: run.ID, DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-tombstones",
		}); err != nil {
			t.Fatal(err)
		}
		closedResult, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, ContradictionID: open.ID, ExpectedStateRevision: open.StateRevision,
			Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-tombstones", CorrelationID: "request-dismiss-tombstones",
		})
		if err != nil {
			t.Fatal(err)
		}
		third := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "inert-closure-third", "Inert closure third", run.TaskID)
		proposedInert, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: third.Revision.ID,
			ReportNote: "proposed only", Actor: OwnerKnowledgeActor(), IdempotencyKey: "report-inert-closure", CorrelationID: "request-report-inert-closure",
		})
		if err != nil {
			t.Fatal(err)
		}
		inertClosed, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, ContradictionID: proposedInert.Detail.Contradiction.ID,
			ExpectedStateRevision: proposedInert.Detail.Contradiction.StateRevision, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: "dismiss-inert-closure", CorrelationID: "request-dismiss-inert-closure",
		})
		if err != nil || inertClosed.EventSequence <= closedResult.EventSequence {
			t.Fatalf("later inert closure=%#v,%v; actual close=%#v", inertClosed, err, closedResult)
		}
		reoffer, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-tombstone-reoffer", CorrelationID: "request-refresh-tombstone-reoffer"})
		if err != nil || reoffer.Delta == nil {
			t.Fatalf("RefreshContext(reoffer)=%#v,%v", reoffer, err)
		}
		accepted := make(map[string]bool)
		leftCause := int64(0)
		closed := false
		for _, change := range reoffer.Delta.Changes {
			if change.Kind == domain.ContextDeltaKnowledgeAccepted {
				accepted[change.EntityID] = true
				if change.EntityID == left.Revision.ID {
					leftCause = change.Cause.EventSequence
				}
			}
			closed = closed || change.Kind == domain.ContextDeltaContradictionClosed
		}
		if !closed || !accepted[left.Revision.ID] || !accepted[right.Revision.ID] || leftCause != closedResult.EventSequence {
			t.Fatalf("closure reoffer changes=%#v", reoffer.Delta.Changes)
		}
	})

	t.Run("pre-base excluded candidates do not become reoffers", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, agent, checkout, assigned := initializeRunTest(t, storage, "pre-base dispute")
		left := acceptContextDecision(t, storage, workspace.ID, assigned.Task.ID, "prebase-left", "Prebase left", assigned.Task.ID)
		right := acceptContextDecision(t, storage, workspace.ID, assigned.Task.ID, "prebase-right", "Prebase right", assigned.Task.ID)
		built, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{WorkspaceIdentifier: workspace.ID,
			TaskID: assigned.Task.ID, AgentIdentifier: agent.ID, CheckoutIdentifier: checkout.ID,
			ExpectedTaskRevision: assigned.Task.Revision,
			IdempotencyKey:       "build-prebase-dispute", CorrelationID: "request-build-prebase-dispute"})
		if err != nil || len(built.Value.AcceptedKnowledge) != 0 {
			t.Fatalf("BuildContextPacket(prebase dispute)=%#v,%v", built.Value.AcceptedKnowledge, err)
		}
		scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "prebase-dispute", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
		created, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID,
			ContextPacketID: built.Value.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
			IdempotencyKey: "run-prebase-dispute", CorrelationID: "request-run-prebase-dispute"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-prebase-dispute")
		if err != nil {
			t.Fatal(err)
		}
		open := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "prebase-open")
		opened, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-prebase-open", CorrelationID: "request-refresh-prebase-open"})
		if err != nil || opened.Delta == nil || len(opened.Delta.Changes) != 1 || opened.Delta.Changes[0].Kind != domain.ContextDeltaContradictionOpened {
			t.Fatalf("RefreshContext(prebase open)=%#v,%v", opened, err)
		}
		if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
			RunID: run.ID, DeltaID: opened.Delta.ID, ExpectedSequence: opened.Delta.Sequence, IdempotencyKey: "ack-prebase-open",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, ContradictionID: open.ID, ExpectedStateRevision: open.StateRevision,
			Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-prebase", CorrelationID: "request-dismiss-prebase",
		}); err != nil {
			t.Fatal(err)
		}
		result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-prebase-close", CorrelationID: "request-refresh-prebase-close"})
		if err != nil || result.Delta == nil {
			t.Fatalf("RefreshContext(prebase close)=%#v,%v", result, err)
		}
		for _, change := range result.Delta.Changes {
			if change.Kind == domain.ContextDeltaKnowledgeAccepted {
				t.Fatalf("pre-base excluded candidate was incorrectly reoffered: %#v", result.Delta.Changes)
			}
		}
	})

	t.Run("broad candidate tombstone crosses wrong-task closure without contradiction snapshot", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, project, _, run := startContextDeltaTestRun(t, storage, "broad tombstone")
		other := createWorkTestTask(t, storage, workspace.ID, project.ID, "other contradiction scope", "other-tombstone-task")
		broad := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "broad-tombstone", "Broad candidate", "")
		wrongTask := acceptContextDecision(t, storage, workspace.ID, other.Task.ID, "wrong-task-tombstone", "Wrong task candidate", other.Task.ID)
		open := openContradiction(t, storage, workspace.ID, broad.Revision.ID, wrongTask.Revision.ID, "broad-wrong-task")

		created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-broad-tombstone", CorrelationID: "request-refresh-broad-tombstone"})
		if err != nil || created.Delta == nil {
			t.Fatalf("RefreshContext(broad tombstone)=%#v,%v", created, err)
		}
		if len(created.Delta.Changes) != 1 || created.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeWithdrawn ||
			created.Delta.Changes[0].EntityID != broad.Revision.ID || created.Delta.Changes[0].Withdrawal == nil ||
			created.Delta.Changes[0].Withdrawal.Reason != "disputed" {
			t.Fatalf("broad suppression changes=%#v", created.Delta.Changes)
		}
		if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
			RunID: run.ID, DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-broad-tombstone",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, ContradictionID: open.ID, ExpectedStateRevision: open.StateRevision,
			Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-broad-tombstone", CorrelationID: "request-dismiss-broad-tombstone",
		}); err != nil {
			t.Fatal(err)
		}
		reoffer, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "reoffer-broad-tombstone", CorrelationID: "request-reoffer-broad-tombstone"})
		if err != nil || reoffer.Delta == nil || len(reoffer.Delta.Changes) != 1 ||
			reoffer.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeAccepted || reoffer.Delta.Changes[0].EntityID != broad.Revision.ID {
			t.Fatalf("RefreshContext(broad reoffer)=%#v,%v", reoffer, err)
		}
	})

	t.Run("terminal stale transition outranks a suppression tombstone", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, run := startContextDeltaTestRun(t, storage, "terminal tombstone")
		left := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "terminal-left", "Terminal left", run.TaskID)
		right := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "terminal-right", "Terminal right", run.TaskID)
		_ = openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "terminal-open")
		created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-terminal-tombstones", CorrelationID: "request-refresh-terminal-tombstones"})
		if err != nil || created.Delta == nil {
			t.Fatalf("RefreshContext(terminal tombstones)=%#v,%v", created, err)
		}
		if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
			RunID: run.ID, DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-terminal-tombstones",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{WorkspaceIdentifier: workspace.ID,
			RevisionID: left.Revision.ID, ExpectedStateRevision: left.Revision.StateRevision, Reason: "terminal authority wins",
			Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-terminal-tombstone", CorrelationID: "request-stale-terminal-tombstone"}); err != nil {
			t.Fatal(err)
		}
		result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID,
			RunID: run.ID, IdempotencyKey: "refresh-terminal-transition", CorrelationID: "request-refresh-terminal-transition"})
		if err != nil || result.Delta == nil {
			t.Fatalf("RefreshContext(terminal transition)=%#v,%v", result, err)
		}
		leftStale, rightReoffered, closed := false, false, false
		for _, change := range result.Delta.Changes {
			leftStale = leftStale || (change.Kind == domain.ContextDeltaKnowledgeWithdrawn && change.EntityID == left.Revision.ID && change.Withdrawal != nil && change.Withdrawal.Reason == "stale")
			rightReoffered = rightReoffered || (change.Kind == domain.ContextDeltaKnowledgeAccepted && change.EntityID == right.Revision.ID)
			closed = closed || change.Kind == domain.ContextDeltaContradictionClosed
			if change.Kind == domain.ContextDeltaKnowledgeAccepted && change.EntityID == left.Revision.ID {
				t.Fatalf("stale tombstone was reoffered: %#v", result.Delta.Changes)
			}
		}
		if !leftStale || !rightReoffered || !closed {
			t.Fatalf("terminal transition changes=%#v", result.Delta.Changes)
		}
	})
}

func TestContextDeltaDisputeContributorBeyondPublicSampleReoffers(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, _, run := startContextDeltaTestRun(t, storage, "dispute contributor sample")
	broad := acceptContextDecision(t, storage, workspace.ID, run.TaskID, "sample-broad", "Sample broad", "")
	delivered, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "deliver-sample-broad", CorrelationID: "request-deliver-sample-broad"})
	if err != nil || delivered.Delta == nil {
		t.Fatalf("RefreshContext(deliver broad)=%#v,%v", delivered, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{RunID: run.ID,
		DeltaID: delivered.Delta.ID, ExpectedSequence: delivered.Delta.Sequence, IdempotencyKey: "ack-sample-broad"}); err != nil {
		t.Fatal(err)
	}
	other := createWorkTestTask(t, storage, workspace.ID, project.ID, "sample other task", "sample-other-task")
	contradictions := make([]domain.KnowledgeContradiction, 0, maximumWithdrawalContradictions+1)
	for index := 0; index < maximumWithdrawalContradictions+1; index++ {
		candidate := acceptContextDecision(t, storage, workspace.ID, other.Task.ID, fmt.Sprintf("sample-other-%d", index), fmt.Sprintf("Other %d", index), other.Task.ID)
		contradictions = append(contradictions, openContradiction(t, storage, workspace.ID, broad.Revision.ID, candidate.Revision.ID, fmt.Sprintf("sample-conflict-%d", index)))
	}
	withdrawn, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "withdraw-sample-broad", CorrelationID: "request-withdraw-sample-broad"})
	if err != nil || withdrawn.Delta == nil || len(withdrawn.Delta.Changes) != 1 || withdrawn.Delta.Changes[0].Withdrawal == nil ||
		withdrawn.Delta.Changes[0].Withdrawal.OpenContradictionCount != maximumWithdrawalContradictions+1 ||
		len(withdrawn.Delta.Changes[0].Withdrawal.OpenContradictionIDs) != maximumWithdrawalContradictions {
		t.Fatalf("RefreshContext(sample withdrawal)=%#v,%v", withdrawn, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{RunID: run.ID,
		DeltaID: withdrawn.Delta.ID, ExpectedSequence: withdrawn.Delta.Sequence, IdempotencyKey: "ack-sample-withdrawal"}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(contradictions, func(i, j int) bool { return contradictions[i].ID < contradictions[j].ID })
	for index := 0; index < maximumWithdrawalContradictions; index++ {
		if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, ContradictionID: contradictions[index].ID,
			ExpectedStateRevision: contradictions[index].StateRevision, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: fmt.Sprintf("dismiss-sampled-%d", index), CorrelationID: fmt.Sprintf("request-dismiss-sampled-%d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	partial, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-sampled-closures", CorrelationID: "request-refresh-sampled-closures"})
	if err != nil || partial.Status != domain.ContextRefreshUpToDate || partial.Delta != nil {
		t.Fatalf("RefreshContext(partial closures)=%#v,%v", partial, err)
	}
	omitted := contradictions[maximumWithdrawalContradictions]
	closed, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: omitted.ID, ExpectedStateRevision: omitted.StateRevision,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-omitted-contributor", CorrelationID: "request-dismiss-omitted-contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	reoffer, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-omitted-contributor", CorrelationID: "request-refresh-omitted-contributor"})
	if err != nil || reoffer.Delta == nil || len(reoffer.Delta.Changes) != 1 ||
		reoffer.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeAccepted || reoffer.Delta.Changes[0].EntityID != broad.Revision.ID ||
		reoffer.Delta.Changes[0].Cause.EventSequence != closed.EventSequence {
		t.Fatalf("RefreshContext(omitted contributor)=%#v,%v", reoffer, err)
	}
}

func TestContextDeltaExactByteBoundariesRemainClassifiable(t *testing.T) {
	t.Parallel()
	makeDelta := func(bodyBytes, cumulative int) (domain.ContextDelta, error) {
		now := "2026-08-13T12:00:00Z"
		knowledge := domain.KnowledgeRevision{ID: "krev_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkspaceID: "ws_a", ProjectID: "prj_a",
			TaskScopeID: "task_a", Type: domain.KnowledgeTypeDecision, StateRevision: 1, ReviewStatus: domain.KnowledgeReviewAccepted,
			CurrencyStatus: domain.KnowledgeCurrencyCurrent, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded, Body: strings.Repeat("x", bodyBytes)}
		change := domain.ContextDeltaChange{Kind: domain.ContextDeltaKnowledgeAccepted, EntityType: "knowledge_revision", EntityID: knowledge.ID,
			Revision: 1, Cause: domain.ContextDeltaCause{EventSequence: 1, Reason: "accepted applicable decision"}, Knowledge: &knowledge}
		delta := domain.ContextDelta{Schema: domain.ContextDeltaSchema, ID: "cdelta_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RunID: "run_a",
			ContextPacketID: "ctx_a", WorkspaceID: "ws_a", ProjectID: "prj_a", TaskID: "task_a", AgentID: "agent_a",
			BasePacketSchema: domain.ContextPacketSchema, Sequence: 1, FromEventSequence: 0, ThroughEventSequence: 1,
			EvaluatedAt: now, Changes: []domain.ContextDeltaChange{change}, Included: contextDeltaSelections([]domain.ContextDeltaChange{change}), Excluded: []domain.ContextExclusion{}, CreatedAt: now, CreatedBy: localOwnerActorID}
		_, err := finalizeContextDelta(&delta, cumulative)
		return delta, err
	}
	bodyBytes := 14000
	for range 4 {
		delta, err := makeDelta(bodyBytes, 0)
		if err != nil {
			t.Fatal(err)
		}
		bodyBytes += maximumContextDeltaBytes - delta.ByteSize
	}
	atLimit, err := makeDelta(bodyBytes, 0)
	if err != nil || atLimit.ByteSize != maximumContextDeltaBytes {
		t.Fatalf("exact delta size=%d body=%d err=%v", atLimit.ByteSize, bodyBytes, err)
	}
	overLimit, err := makeDelta(bodyBytes+1, 0)
	if err != nil || overLimit.ByteSize != maximumContextDeltaBytes+1 {
		t.Fatalf("over delta size=%d err=%v", overLimit.ByteSize, err)
	}
	chainExact, err := makeDelta(0, maximumContextDeltaTotal)
	if err != nil || chainExact.Budget.Chain.UsedBytes <= maximumContextDeltaTotal {
		t.Fatalf("over-chain delta=%#v err=%v", chainExact.Budget.Chain, err)
	}
}

func TestContextDeltaOversizedProjectionCommitsDurableRebase(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "oversized projection")
	for index := 0; index < 2; index++ {
		key := fmt.Sprintf("large-delta-%d", index)
		proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, key, "Large live decision", strings.Repeat("x", 7000), "")
		if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
			RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: "accept-" + key, CorrelationID: "request-accept-" + key}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-oversized-projection", CorrelationID: "request-refresh-oversized-projection"})
	if err != nil || result.Status != domain.ContextRefreshRebaseRequired || result.RebaseReason != domain.ContextRebaseDeltaLimitExceeded ||
		result.Delta != nil || result.EventSequence <= 0 || result.Chain.DeltaCount != 0 {
		t.Fatalf("RefreshContext(oversized projection)=%#v,%v", result, err)
	}
}

func TestContextDeltaCumulativeBudgetCommitsDurableRebase(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "cumulative projection")
	for index := 0; index < 12; index++ {
		key := fmt.Sprintf("cumulative-delta-%d", index)
		proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, key, "Cumulative live decision", strings.Repeat(string(rune('a'+index)), 7000), "")
		if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
			RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: "accept-" + key, CorrelationID: "request-accept-" + key}); err != nil {
			t.Fatal(err)
		}
		result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
			IdempotencyKey: "refresh-" + key, CorrelationID: "request-refresh-" + key})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == domain.ContextRefreshRebaseRequired {
			if result.RebaseReason != domain.ContextRebaseCumulativeLimitExceeded || result.Delta != nil || result.EventSequence <= 0 ||
				result.Chain.CumulativeByteSize > maximumContextDeltaTotal {
				t.Fatalf("cumulative rebase=%#v", result)
			}
			return
		}
		if result.Delta == nil || result.Delta.ByteSize > maximumContextDeltaBytes {
			t.Fatalf("cumulative created=%#v", result)
		}
		if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{RunID: run.ID,
			DeltaID: result.Delta.ID, ExpectedSequence: result.Delta.Sequence, IdempotencyKey: "ack-" + key}); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("cumulative delta budget did not reach a durable rebase")
}

func TestContextDeltaFreshnessExpiryUsesEqualEventCursor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "equal cursor expiry")
	proposed, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		TaskScopeID: run.TaskID, Type: domain.KnowledgeTypeDecision, Title: "Expiring decision", Body: "Expires without another journal fact.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshExpiresAt, FreshUntil: now.Add(time.Hour).Format(time.RFC3339Nano),
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: run.TaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "propose-expiring-delta", CorrelationID: "request-propose-expiring-delta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspace.ID,
		RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-expiring-delta", CorrelationID: "request-accept-expiring-delta"}); err != nil {
		t.Fatal(err)
	}
	accepted, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-expiring-accepted", CorrelationID: "request-refresh-expiring-accepted"})
	if err != nil || accepted.Delta == nil {
		t.Fatalf("RefreshContext(accepted)=%#v,%v", accepted, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{RunID: run.ID,
		DeltaID: accepted.Delta.ID, ExpectedSequence: accepted.Delta.Sequence, IdempotencyKey: "ack-expiring-accepted"}); err != nil {
		t.Fatal(err)
	}
	noOp, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "scan-expiring-ack", CorrelationID: "request-scan-expiring-ack"})
	if err != nil || noOp.Status != domain.ContextRefreshUpToDate {
		t.Fatalf("RefreshContext(scan ack)=%#v,%v", noOp, err)
	}
	now = now.Add(2 * time.Hour)
	expired, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-expired", CorrelationID: "request-refresh-expired"})
	if err != nil || expired.Delta == nil || expired.Delta.FromEventSequence != noOp.ScannedThroughEventSequence ||
		expired.Delta.ThroughEventSequence != noOp.ScannedThroughEventSequence || len(expired.Delta.Changes) != 1 ||
		expired.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeWithdrawn || expired.Delta.Changes[0].Withdrawal == nil ||
		expired.Delta.Changes[0].Withdrawal.Reason != "freshness_expired" || expired.Delta.Changes[0].Cause.EventSequence != 0 {
		t.Fatalf("RefreshContext(equal-cursor expiry)=%#v,%v", expired, err)
	}
}

func TestContextDeltaRunBindingAllowsPostPacketParticipantInvite(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	createdThread, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Roster grows after packet build",
		Participants: []domain.ParticipantBindingInput{
			{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID},
			{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		}, IdempotencyKey: "create-live-roster", CorrelationID: "request-create-live-roster",
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{WorkspaceIdentifier: fixture.workspace.ID,
		TaskID: fixture.clientTask.Task.ID, AgentIdentifier: fixture.clientAgent.ID,
		ExpectedTaskRevision: fixture.clientTask.Task.Revision, IdempotencyKey: "build-live-roster", CorrelationID: "request-build-live-roster"})
	if err != nil || len(packet.Value.ParticipantThreads) != 1 || packet.Value.ParticipantThreads[0].ParticipantRevision != 1 {
		t.Fatalf("BuildContextPacket(roster)=%#v,%v", packet.Value.ParticipantThreads, err)
	}
	invited, err := storage.InviteThreadParticipant(context.Background(), InviteThreadParticipantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ThreadID: createdThread.Collaboration.Thread.ID,
		Participant:                 domain.ParticipantBindingInput{AgentIdentifier: fixture.opsAgent.ID, TaskIdentifier: fixture.opsTask.Task.ID},
		ExpectedParticipantRevision: createdThread.Collaboration.ParticipantRevision,
		IdempotencyKey:              "invite-live-roster",
		CorrelationID:               "request-invite-live-roster",
	})
	if err != nil {
		t.Fatal(err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "post-packet-roster", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	createdRun, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: fixture.workspace.ID,
		TaskID: fixture.clientTask.Task.ID, ContextPacketID: packet.Value.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: fixture.clientTask.Task.Revision, IdempotencyKey: "run-live-roster", CorrelationID: "request-run-live-roster"})
	if err != nil {
		t.Fatalf("CreateRun(post-packet invite)=%v", err)
	}
	run, err := storage.MarkRunStarting(context.Background(), createdRun.Detail.Run.ID, "worker-live-roster")
	if err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: fixture.workspace.ID,
		RunID: run.ID, IdempotencyKey: "refresh-live-roster", CorrelationID: "request-refresh-live-roster"})
	if err != nil || result.Delta == nil || len(result.Delta.Changes) != 1 ||
		result.Delta.Changes[0].Kind != domain.ContextDeltaParticipantRosterUpdated ||
		result.Delta.Changes[0].ParticipantThread == nil ||
		result.Delta.Changes[0].ParticipantThread.ParticipantRevision != invited.Collaboration.ParticipantRevision {
		t.Fatalf("RefreshContext(post-packet invite)=%#v,%v", result, err)
	}
}

func TestContextDeltaUnknownRepresentedThreadEventSurvivesBindingRemovalFilter(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	fixture := createCollaborationFixture(t, storage)
	createdThread, err := storage.CreateParticipantThread(context.Background(), CreateParticipantThreadCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Subject: "Represented roster removal",
		Participants: []domain.ParticipantBindingInput{
			{AgentIdentifier: fixture.clientAgent.ID, TaskIdentifier: fixture.clientTask.Task.ID},
			{AgentIdentifier: fixture.engineAgent.ID, TaskIdentifier: fixture.engineTask.Task.ID},
		}, IdempotencyKey: "create-represented-removal-roster", CorrelationID: "request-create-represented-removal-roster",
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.clientTask.Task.ID, AgentIdentifier: fixture.clientAgent.ID,
		ExpectedTaskRevision: fixture.clientTask.Task.Revision, IdempotencyKey: "build-represented-removal-roster", CorrelationID: "request-build-represented-removal-roster",
	})
	if err != nil || len(packet.Value.ParticipantThreads) != 1 {
		t.Fatalf("BuildContextPacket(roster)=%#v,%v", packet.Value.ParticipantThreads, err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "represented-removal-roster",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	createdRun, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.clientTask.Task.ID, ContextPacketID: packet.Value.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: fixture.clientTask.Task.Revision,
		IdempotencyKey: "run-represented-removal-roster", CorrelationID: "request-run-represented-removal-roster",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := storage.MarkRunStarting(context.Background(), createdRun.Detail.Run.ID, "worker-represented-removal-roster")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE message_threads SET status = 'closed' WHERE id = ?", createdThread.Collaboration.Thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, fixture.workspace.ID, "thread", createdThread.Collaboration.Thread.ID, 2,
		"thread.participant_removed", "unknown-represented-thread-removal", storage.nowText(), map[string]any{"agent_id": fixture.clientAgent.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-represented-removal-roster", CorrelationID: "request-refresh-represented-removal-roster",
	})
	if err != nil || result.Status != domain.ContextRefreshRebaseRequired || result.RebaseReason != domain.ContextRebaseUnsupportedEventType {
		t.Fatalf("RefreshContext(unknown represented thread event)=%#v,%v", result, err)
	}
}

func TestContextDeltaReverseDependentsBeyondBaseCapAndUpstreamOverflow(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "reverse 33")
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	for index := 0; index < maximumContextDependents; index++ {
		id := fmt.Sprintf("task_%032x", 10000+index)
		if _, err := tx.Exec(`INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,
budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
SELECT ?,workspace_id,project_id,objective_id,?,description,'ready',NULL,priority,budget_tokens,budget_cost_cents,budget_time_seconds,1,?,?,created_by,updated_by FROM tasks WHERE id=?`,
			id, fmt.Sprintf("reverse-%02d", index), now, now, assigned.Task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES(?,?,?,?)", id, assigned.Task.ID, now, localOwnerActorID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "reverse-33", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	createdRun, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "run-reverse-33", CorrelationID: "request-run-reverse-33"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := storage.MarkRunStarting(context.Background(), createdRun.Detail.Run.ID, "worker-reverse-33")
	if err != nil {
		t.Fatal(err)
	}
	tx, err = storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now = storage.nowText()
	lastID := fmt.Sprintf("task_%032x", 10000+maximumContextDependents)
	if _, err := tx.Exec(`INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,
budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
SELECT ?,workspace_id,project_id,objective_id,?,description,'ready',NULL,priority,budget_tokens,budget_cost_cents,budget_time_seconds,1,?,?,created_by,updated_by FROM tasks WHERE id=?`,
		lastID, "reverse-33", now, now, run.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES(?,?,?,?)", lastID, run.TaskID, now, localOwnerActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", lastID, 1, taskCreated, "reverse-task-33", now,
		map[string]any{"title": "reverse-33", "status": domain.TaskReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", lastID, 1, taskDependencyAdded, "reverse-edge-33", now, map[string]any{"depends_on_task_id": run.TaskID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-reverse-33", CorrelationID: "request-refresh-reverse-33"})
	if err != nil || created.Delta == nil {
		t.Fatalf("RefreshContext(reverse 33)=%#v,%v", created, err)
	}
	foundLast := false
	for _, change := range created.Delta.Changes {
		if change.Kind == domain.ContextDeltaDependentAdded && change.EntityID == lastID {
			foundLast = true
		}
	}
	if !foundLast {
		t.Fatalf("33rd reverse dependent %s missing from %d changes", lastID, len(created.Delta.Changes))
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-reverse-33",
	}); err != nil {
		t.Fatal(err)
	}
	tx, err = storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?", lastID, run.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", lastID, 3, "task.updated", "reverse-edge-removed", storage.nowText(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	removed, err := storage.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-reverse-removed", CorrelationID: "request-refresh-reverse-removed"})
	if err != nil || removed.Status != domain.ContextRefreshRebaseRequired || removed.RebaseReason != domain.ContextRebaseDependencySetChanged {
		t.Fatalf("RefreshContext(reverse removed)=%#v,%v", removed, err)
	}

	upstreamStore := openTestStore(t, t.TempDir(), Options{})
	upstreamWorkspace, _, _, upstreamRun := startContextDeltaTestRun(t, upstreamStore, "upstream 33")
	tx, err = upstreamStore.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now = upstreamStore.nowText()
	for index := 0; index < maximumContextDependents+1; index++ {
		id := fmt.Sprintf("task_%032x", 20000+index)
		if _, err := tx.Exec(`INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,
budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
SELECT ?,workspace_id,project_id,objective_id,?,description,'ready',NULL,priority,budget_tokens,budget_cost_cents,budget_time_seconds,1,?,?,created_by,updated_by FROM tasks WHERE id=?`,
			id, fmt.Sprintf("upstream-%02d", index), now, now, upstreamRun.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES(?,?,?,?)", upstreamRun.TaskID, id, now, localOwnerActorID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rebase, err := upstreamStore.RefreshContext(context.Background(), RefreshContextCommand{WorkspaceIdentifier: upstreamWorkspace.ID, RunID: upstreamRun.ID,
		IdempotencyKey: "refresh-upstream-33", CorrelationID: "request-refresh-upstream-33"})
	if err != nil || rebase.Status != domain.ContextRefreshRebaseRequired || rebase.RebaseReason != domain.ContextRebaseDependencySetChanged {
		t.Fatalf("RefreshContext(upstream 33)=%#v,%v", rebase, err)
	}
}

func TestContextDeltaOmittedReverseDependentEventsDoNotOverflowWindow(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "omitted reverse dependent events")
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	for index := 0; index < maximumContextDependents+1; index++ {
		id := fmt.Sprintf("task_%032x", 30000+index)
		if _, err := tx.Exec(`INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,
budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
SELECT ?,workspace_id,project_id,objective_id,?,description,'ready',NULL,priority,budget_tokens,budget_cost_cents,budget_time_seconds,1,?,?,created_by,updated_by FROM tasks WHERE id=?`,
			id, fmt.Sprintf("omitted-reverse-%02d", index), now, now, assigned.Task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES(?,?,?,?)",
			id, assigned.Task.ID, now, localOwnerActorID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "omitted-reverse-dependent-events",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "run-omitted-reverse-events", CorrelationID: "request-run-omitted-reverse-events",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-omitted-reverse-events")
	if err != nil {
		t.Fatal(err)
	}
	omittedID := fmt.Sprintf("task_%032x", 30000+maximumContextDependents)
	tx, err = storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now = storage.nowText()
	for index := 0; index < maximumContextDeltaEvents+1; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "task", omittedID, int64(100+index),
			"task.updated", fmt.Sprintf("omitted-reverse-event-%d", index), now, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-omitted-reverse-events", CorrelationID: "request-refresh-omitted-reverse-events",
	})
	if err != nil || result.Status != domain.ContextRefreshUpToDate || result.RebaseReason != "" || result.Delta != nil {
		t.Fatalf("RefreshContext(1001 omitted dependent events)=%#v,%v", result, err)
	}
}

func TestContextDeltaStaleWithdrawalAndBaseContractRebase(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "withdraw delta")
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, run.TaskID, "withdraw-decision", "Withdraw decision", "This decision will become stale.", "")
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-withdraw-decision", CorrelationID: "request-accept-withdraw-decision",
	})
	if err != nil {
		t.Fatalf("AcceptKnowledge() error = %v", err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "deliver-withdraw-decision", CorrelationID: "request-deliver-withdraw-decision",
	})
	if err != nil || created.Delta == nil {
		t.Fatalf("RefreshContext(deliver) = %#v, %v", created, err)
	}
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: created.Delta.ID, ExpectedSequence: created.Delta.Sequence, IdempotencyKey: "ack-withdraw-decision",
	}); err != nil {
		t.Fatalf("AcknowledgeRunContextDelta() error = %v", err)
	}
	stale, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: accepted.Revision.ID,
		ExpectedStateRevision: accepted.Revision.StateRevision, Reason: "contract retired", Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "stale-withdraw-decision", CorrelationID: "request-stale-withdraw-decision",
	})
	if err != nil {
		t.Fatalf("MarkKnowledgeStale() error = %v", err)
	}
	withdrawn, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, IdempotencyKey: "withdraw-refresh", CorrelationID: "request-withdraw-refresh",
	})
	if err != nil || withdrawn.Delta == nil || withdrawn.Delta.Sequence != 2 || withdrawn.Delta.ParentDeltaID != created.Delta.ID ||
		len(withdrawn.Delta.Changes) != 1 || withdrawn.Delta.Changes[0].Kind != domain.ContextDeltaKnowledgeWithdrawn ||
		withdrawn.Delta.Changes[0].Withdrawal == nil || withdrawn.Delta.Changes[0].Withdrawal.Reason != "stale" ||
		withdrawn.Delta.Changes[0].Revision != stale.Revision.StateRevision {
		t.Fatalf("RefreshContext(withdraw) = %#v, %v", withdrawn, err)
	}

	// A fresh run proves immutable task contract drift wins over ordinary
	// projection and becomes a durable audited rebase state.
	secondStorage := openTestStore(t, t.TempDir(), Options{})
	secondWorkspace, secondProject, _, secondRun := startContextDeltaTestRun(t, secondStorage, "rebase delta")
	checkout, err := queryCheckoutByID(context.Background(), secondStorage.db, secondRun.CheckoutID)
	if err != nil {
		t.Fatalf("query checkout: %v", err)
	}
	var fingerprint, objectFormat string
	var rootsJSON string
	if err := secondStorage.db.QueryRow("SELECT fingerprint, object_format, root_commits_json FROM repositories WHERE id = ?", checkout.RepositoryID).Scan(&fingerprint, &objectFormat, &rootsJSON); err != nil {
		t.Fatalf("query repository: %v", err)
	}
	var roots []string
	if err := json.Unmarshal([]byte(rootsJSON), &roots); err != nil {
		t.Fatalf("decode roots: %v", err)
	}
	if _, err := secondStorage.ApplyCheckoutObservations(context.Background(), secondWorkspace.ID, secondProject.ID, "observe-agent-work", map[string]domain.CheckoutObservation{
		checkout.ID: {Path: checkout.Path, Availability: domain.CheckoutAvailable, CheckoutKind: checkout.CheckoutKind,
			Branch: "agent/work", HeadCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Dirty: true, DirtyPaths: []string{"work.go"},
			Repository: domain.RepositoryObservation{Fingerprint: fingerprint, ObjectFormat: objectFormat, RootCommits: roots}},
	}); err != nil {
		t.Fatalf("ApplyCheckoutObservations() error = %v", err)
	}
	observed, err := secondStorage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: secondWorkspace.ID, RunID: secondRun.ID, IdempotencyKey: "observed-refresh", CorrelationID: "request-observed-refresh",
	})
	if err != nil || observed.Status != domain.ContextRefreshUpToDate || observed.RebaseReason != "" {
		t.Fatalf("RefreshContext(observed checkout) = %#v, %v", observed, err)
	}
	var revision int64
	if err := secondStorage.db.QueryRow("SELECT revision FROM tasks WHERE id = ?", secondRun.TaskID).Scan(&revision); err != nil {
		t.Fatalf("read task revision: %v", err)
	}
	newTitle := "changed live contract"
	if _, err := secondStorage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: secondWorkspace.ID, TaskID: secondRun.TaskID, Title: &newTitle,
		ExpectedRevision: revision, IdempotencyKey: "drift-live-task", CorrelationID: "request-drift-live-task",
	}); err != nil {
		t.Fatalf("UpdateTask() error = %v", err)
	}
	rebase, err := secondStorage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: secondWorkspace.ID, RunID: secondRun.ID, IdempotencyKey: "rebase-refresh", CorrelationID: "request-rebase-refresh",
	})
	if err != nil || rebase.Status != domain.ContextRefreshRebaseRequired || rebase.RebaseReason != domain.ContextRebaseBaseContractChanged || rebase.Chain.RebaseEventSequence <= 0 {
		t.Fatalf("RefreshContext(rebase) = %#v, %v", rebase, err)
	}
	if rebase.ScannedFromEventSequence != observed.ScannedThroughEventSequence || rebase.EventSequence != rebase.Chain.RebaseEventSequence {
		t.Fatalf("rebase interval/event = %#v; previous cursor=%d", rebase, observed.ScannedThroughEventSequence)
	}
	rebaseAgain, err := secondStorage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: secondWorkspace.ID, RunID: secondRun.ID, IdempotencyKey: "rebase-refresh-again", CorrelationID: "request-rebase-refresh-again",
	})
	if err != nil || rebaseAgain.EventSequence != rebase.EventSequence || rebaseAgain.Status != domain.ContextRefreshRebaseRequired ||
		rebaseAgain.ScannedFromEventSequence != rebase.ScannedFromEventSequence || rebaseAgain.ScannedThroughEventSequence != rebase.ScannedThroughEventSequence {
		t.Fatalf("RefreshContext(existing rebase) = %#v, %v", rebaseAgain, err)
	}
	replayedRebase, err := secondStorage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: secondWorkspace.ID, RunID: secondRun.ID, IdempotencyKey: "rebase-refresh", CorrelationID: "request-rebase-refresh",
	})
	if err != nil || !replayedRebase.Replayed || replayedRebase.ScannedFromEventSequence != rebase.ScannedFromEventSequence ||
		replayedRebase.ScannedThroughEventSequence != rebase.ScannedThroughEventSequence || replayedRebase.EventSequence != rebase.EventSequence {
		t.Fatalf("RefreshContext(rebase replay)=%#v,%v", replayedRebase, err)
	}
}

func TestContextDeltaLegacyRefreshAndStorageIntegrity(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	_, _, _, run := startContextDeltaTestRun(t, storage, "legacy delta")
	var packetJSON string
	if err := storage.db.QueryRow("SELECT packet_json FROM context_packets WHERE id = ?", run.ContextPacketID).Scan(&packetJSON); err != nil {
		t.Fatalf("read packet JSON: %v", err)
	}
	var packet domain.ContextPacket
	if err := json.Unmarshal([]byte(packetJSON), &packet); err != nil {
		t.Fatalf("decode packet JSON: %v", err)
	}
	legacy := packet
	legacy.ID = "ctx_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	legacy.Schema = domain.ContextPacketSchemaV3
	legacy.Dependents, legacy.ParticipantThreads = nil, nil
	legacy.LiveContext, legacy.Budget.Collaboration = domain.ContextLivePolicy{}, domain.ContextBudgetUsage{}
	legacy.AsOfEventSequence, legacy.ByteSize = 0, 0
	legacy.ContentHash = packet.ContentHash
	for range 8 {
		encoded, _ := json.Marshal(legacy)
		if len(encoded) == legacy.ByteSize {
			packetJSON = string(encoded)
			break
		}
		legacy.ByteSize = len(encoded)
	}
	if _, err := storage.db.Exec(`INSERT INTO context_packets(id,workspace_id,project_id,task_id,agent_id,checkout_id,packet_json,content_hash,byte_size,created_at,created_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, legacy.ID, legacy.WorkspaceID, legacy.ProjectID, legacy.TaskID, legacy.AgentID, legacy.CheckoutID,
		packetJSON, legacy.ContentHash, legacy.ByteSize, legacy.CreatedAt, legacy.CreatedBy); err != nil {
		t.Fatalf("insert legacy packet: %v", err)
	}
	if _, err := storage.db.Exec("UPDATE run_context_bindings SET context_packet_id = ? WHERE run_id = ?", legacy.ID, run.ID); err == nil {
		t.Fatal("immutable context binding update unexpectedly succeeded")
	}
	// Row/JSON integrity rejects a forged version-four packet before any run can
	// bind it.
	forged := packet
	forged.ID = "ctx_ffffffffffffffffffffffffffffffff"
	forged.Task.Title = "forged semantic content"
	forged.ByteSize = 0
	var encoded []byte
	for range 8 {
		encoded, _ = json.Marshal(forged)
		if len(encoded) == forged.ByteSize {
			break
		}
		forged.ByteSize = len(encoded)
		forged.Budget.Total.UsedBytes = len(encoded)
		forged.Budget.Total.RemainingBytes = maximumContextBytes - len(encoded)
	}
	if _, err := storage.db.Exec(`INSERT INTO context_packets(id,workspace_id,project_id,task_id,agent_id,checkout_id,packet_json,content_hash,byte_size,created_at,created_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, forged.ID, forged.WorkspaceID, forged.ProjectID, forged.TaskID, forged.AgentID, forged.CheckoutID,
		string(encoded), forged.ContentHash, forged.ByteSize, forged.CreatedAt, forged.CreatedBy); err == nil {
		t.Fatal("semantically forged context packet insert unexpectedly succeeded")
	}
}

func startContextDeltaTestRun(t *testing.T, storage *Store, title string) (domain.Workspace, domain.Project, domain.AgentDefinition, domain.Run) {
	t.Helper()
	workspace, project, agent, _, assigned := initializeRunTest(t, storage, title)
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "context-delta-" + title,
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, CapabilityTTL: 3600 * 1000000000,
		IdempotencyKey: "run-" + title, CorrelationID: "request-run-" + title,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	starting, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting-"+title)
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	return workspace, project, agent, starting
}

func acceptContextDecision(t *testing.T, storage *Store, workspaceID, sourceTaskID, key, title, taskScopeID string) KnowledgeMutationResult {
	t.Helper()
	proposed, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{WorkspaceIdentifier: workspaceID,
		TaskScopeID: taskScopeID, Type: domain.KnowledgeTypeDecision, Title: title, Body: title + " body",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sourceTaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:           OwnerKnowledgeActor(), IdempotencyKey: "propose-" + key, CorrelationID: "request-propose-" + key})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{WorkspaceIdentifier: workspaceID,
		RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-" + key, CorrelationID: "request-accept-" + key})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
