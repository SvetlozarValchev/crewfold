package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestSupervisorJournalUnknownEventFailsClosedWithoutEffectsOrCursorAdvance(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	configureCursorTestPolicy(t, storage, fixture.workspace.ID, "unknown")

	baseline := runCursorTestSupervisor(t, storage, fixture.workspace.ID, "unknown-baseline", false)
	if len(baseline.Actions) != 0 || len(baseline.ScheduledRunIDs) != 0 {
		t.Fatalf("baseline supervisor result = %#v; want no effects", baseline)
	}
	before := supervisorCursorForTest(t, storage, fixture.workspace.ID)

	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin unknown-event fixture = %v", err)
	}
	unknownSequence, err := appendEvent(context.Background(), tx, fixture.workspace.ID, "task", fixture.planning.Task.ID,
		fixture.planning.Task.Revision, "task.safety_contract_changed", "unknown-supervisor-event", storage.nowText(), map[string]any{"version": 2})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("append unknown supervisor event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit unknown supervisor event = %v", err)
	}

	_, err = storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "unknown-supervisor-scan", CorrelationID: "request-unknown-supervisor-scan", PersistNoop: true,
	})
	if ErrorCode(err) != CodeUnsupportedSupervisorEvent {
		t.Fatalf("RunSupervisor(unknown event) = %v, code %q; want %q", err, ErrorCode(err), CodeUnsupportedSupervisorEvent)
	}
	after := supervisorCursorForTest(t, storage, fixture.workspace.ID)
	if after != before || after >= unknownSequence {
		t.Fatalf("supervisor cursor after unknown event = %d, before %d, unknown %d; want unchanged before unknown", after, before, unknownSequence)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions`)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_scheduling_receipts`)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_jobs WHERE origin='supervisor'`)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key='unknown-supervisor-scan'`)
}

func TestSupervisorExplainFailsClosedAtUnclassifiedJournalEvent(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	configureCursorTestPolicy(t, storage, fixture.workspace.ID, "explain-unknown")
	runCursorTestSupervisor(t, storage, fixture.workspace.ID, "explain-unknown-baseline", false)

	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin explanation unknown-event fixture = %v", err)
	}
	if _, err := appendEvent(context.Background(), tx, fixture.workspace.ID, "task", fixture.planning.Task.ID,
		fixture.planning.Task.Revision, "task.unclassified_explanation_changed", "explain-unknown-event", storage.nowText(), map[string]any{"version": 1}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("append explanation unknown event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit explanation unknown event = %v", err)
	}

	_, err = storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{WorkspaceIdentifier: fixture.workspace.ID, Limit: 100})
	if ErrorCode(err) != CodeUnsupportedSupervisorEvent {
		t.Fatalf("ExplainSupervisor(unknown event) = %v, code %q; want %q", err, ErrorCode(err), CodeUnsupportedSupervisorEvent)
	}
}

func TestSupervisorJournalBoundedCatchupIsEffectFreeAndRestartSafe(t *testing.T) {
	dataDir := t.TempDir()
	storage := openTestStore(t, dataDir, Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	configureCursorTestPolicy(t, storage, workspace.ID, "bounded")

	// Catch the policy's own facts up first, then append more than one bounded
	// page of understood, inert checkout observations.
	runCursorTestSupervisor(t, storage, workspace.ID, "bounded-baseline", false)
	before := supervisorCursorForTest(t, storage, workspace.ID)
	var checkoutID string
	if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? ORDER BY id LIMIT 1`, project.ID).Scan(&checkoutID); err != nil {
		t.Fatalf("read bounded checkout = %v", err)
	}
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin bounded journal fixture = %v", err)
	}
	for index := 0; index < maximumSupervisorJournalEvents+7; index++ {
		if _, err := appendEvent(context.Background(), tx, workspace.ID, "checkout", checkoutID, int64(index+2),
			checkoutObserved, fmt.Sprintf("bounded-journal-%d", index), storage.nowText(), map[string]any{"fixture": true}); err != nil {
			_ = tx.Rollback()
			t.Fatalf("append bounded event %d = %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit bounded journal fixture = %v", err)
	}
	var cutoff int64
	if err := storage.db.QueryRow(`SELECT MAX(sequence) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&cutoff); err != nil {
		t.Fatalf("read bounded cutoff = %v", err)
	}

	first := runCursorTestSupervisor(t, storage, workspace.ID, "bounded-page-one", true)
	if len(first.Actions) != 0 || len(first.ScheduledRunIDs) != 0 || first.EventSequence != before+maximumSupervisorJournalEvents {
		t.Fatalf("first bounded page = %#v; want effect-free cursor %d", first, before+maximumSupervisorJournalEvents)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions`)
	if err := storage.Close(); err != nil {
		t.Fatalf("close between supervisor pages = %v", err)
	}
	storage = openTestStore(t, dataDir, Options{})

	second := runCursorTestSupervisor(t, storage, workspace.ID, "bounded-page-two", true)
	if len(second.Actions) != 0 || len(second.ScheduledRunIDs) != 0 || second.EventSequence != cutoff {
		t.Fatalf("second bounded page after restart = %#v; want effect-free cutoff %d", second, cutoff)
	}
	if cursor := supervisorCursorForTest(t, storage, workspace.ID); cursor != cutoff {
		t.Fatalf("supervisor cursor after bounded restart = %d, want %d", cursor, cutoff)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions`)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_jobs WHERE origin='supervisor'`)
}

func TestSupervisorPublicNoopReplayCannotAcquireLaterEffects(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	configureCursorTestPolicy(t, storage, fixture.workspace.ID, "no-op-replay")

	command := RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "public-noop-replay", CorrelationID: "request-public-noop-replay-1", PersistNoop: true,
	}
	first, err := storage.RunSupervisor(context.Background(), command)
	if err != nil {
		t.Fatalf("RunSupervisor(first public no-op) = %v", err)
	}
	if len(first.Actions) != 0 || len(first.ScheduledRunIDs) != 0 {
		t.Fatalf("first public no-op = %#v", first)
	}

	// Accepted work becomes genuinely schedulable later. The exact public retry
	// must still return its original no-op receipt and leave those intents for a
	// fresh command/daemon pass.
	acceptAdversarialSchedulingPair(t, storage, fixture, "later-ready-pair", "")
	assertManagementRowCount(t, storage, 2, `SELECT COUNT(*) FROM scheduling_intents WHERE status='pending'`)
	command.CorrelationID = "request-public-noop-replay-2"
	replayed, err := storage.RunSupervisor(context.Background(), command)
	if err != nil {
		t.Fatalf("RunSupervisor(public no-op replay) = %v", err)
	}
	if replayed.EventSequence != first.EventSequence || len(replayed.Actions) != 0 || len(replayed.ScheduledRunIDs) != 0 {
		t.Fatalf("public no-op replay = %#v, first %#v", replayed, first)
	}
	if cursor := supervisorCursorForTest(t, storage, fixture.workspace.ID); cursor != first.EventSequence {
		t.Fatalf("exact replay advanced supervisor cursor to %d, want original %d", cursor, first.EventSequence)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_jobs WHERE origin='supervisor'`)
	var cutoff int64
	if err := storage.db.QueryRow(`SELECT MAX(sequence) FROM events WHERE workspace_id=?`, fixture.workspace.ID).Scan(&cutoff); err != nil {
		t.Fatalf("read fresh supervisor cutoff = %v", err)
	}
	fresh := runCursorTestSupervisor(t, storage, fixture.workspace.ID, "public-noop-fresh", true)
	if len(fresh.ScheduledRunIDs) == 0 {
		t.Fatalf("fresh supervisor command after no-op replay did not schedule ready work: %#v", fresh)
	}
	for _, action := range fresh.Actions {
		if action.AsOfEventSequence != cutoff {
			t.Fatalf("fresh action as-of = %d, want captured cutoff %d: %#v", action.AsOfEventSequence, cutoff, action)
		}
	}
}

func configureCursorTestPolicy(t *testing.T, storage *Store, workspaceID, key string) {
	t.Helper()
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspaceID, Enabled: true, AutoSchedule: true,
		Limits:         domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 4, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4},
		AutoRetryLimit: 0, RetryCooldownSeconds: 0, ExpectedRevision: 1,
		IdempotencyKey: key + "-policy", CorrelationID: "request-" + key + "-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(%s) = %v", key, err)
	}
}

func runCursorTestSupervisor(t *testing.T, storage *Store, workspaceID, key string, persistNoop bool) SupervisorRunResult {
	t.Helper()
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspaceID, Limit: 100, IdempotencyKey: key,
		CorrelationID: "request-" + key, PersistNoop: persistNoop,
	})
	if err != nil {
		t.Fatalf("RunSupervisor(%s) = %v", key, err)
	}
	return result
}

func supervisorCursorForTest(t *testing.T, storage *Store, workspaceID string) int64 {
	t.Helper()
	var cursor int64
	if err := storage.db.QueryRow(`SELECT last_event_sequence FROM supervisor_state WHERE workspace_id=?`, workspaceID).Scan(&cursor); err != nil {
		t.Fatalf("read supervisor cursor = %v", err)
	}
	return cursor
}

func TestKnownSupervisorJournalEventUnion(t *testing.T) {
	for _, eventType := range []string{
		"workspace.created", "task.completed", "manager.proposal_accepted", "supervisor.scan_completed",
		"check.definition_created", "check.definition_retired", "check.requirement_created", "check.requirement_retired",
		"check.grant_created", "check.grant_revoked", "check.policy_configured", "check.route_created", "check.route_retired",
		"check.run_requested", "check.run_starting", "check.run_runtime_observed", "check.run_started", "check.run_finished", "check.result_recorded",
		"check.freshness_observed", "check.freshness_stale", "check.notification_queued", "check.notification_unroutable",
		"check.repair_proposed", "check.repair_accepted", "check.repair_rejected", "check.repair_stale", "check.watch_completed",
	} {
		if !knownSupervisorJournalEvent(eventType) {
			t.Fatalf("known event %q was rejected", eventType)
		}
	}
	for _, eventType := range []string{"", "task.safety_contract_changed", "supervisor.policy_widened"} {
		if knownSupervisorJournalEvent(eventType) {
			t.Fatalf("unknown event %q was accepted", eventType)
		}
	}
}

func TestAdvanceSupervisorJournalCursorNormalizesFrozenClock(t *testing.T) {
	observed := time.Date(2038, 1, 2, 3, 4, 5, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
	workspace, project := initializeWorkTestProject(t, storage)
	configureCursorTestPolicy(t, storage, workspace.ID, "frozen-clock")
	first := runCursorTestSupervisor(t, storage, workspace.ID, "frozen-clock-one", false)
	if first.EventSequence <= 0 {
		t.Fatalf("first frozen-clock cursor = %d", first.EventSequence)
	}
	var checkoutID string
	if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? ORDER BY id LIMIT 1`, project.ID).Scan(&checkoutID); err != nil {
		t.Fatalf("read frozen-clock checkout = %v", err)
	}
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin frozen-clock event = %v", err)
	}
	if _, err := appendEvent(context.Background(), tx, workspace.ID, "checkout", checkoutID, 2, checkoutObserved,
		"frozen-clock-event", storage.nowText(), map[string]any{"fixture": true}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("append frozen-clock event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit frozen-clock event = %v", err)
	}
	second := runCursorTestSupervisor(t, storage, workspace.ID, "frozen-clock-two", false)
	if second.EventSequence <= first.EventSequence {
		t.Fatalf("second frozen-clock cursor = %d, first %d", second.EventSequence, first.EventSequence)
	}
	var updatedAt string
	if err := storage.db.QueryRow(`SELECT updated_at FROM supervisor_state WHERE workspace_id=?`, workspace.ID).Scan(&updatedAt); err != nil {
		t.Fatalf("read frozen-clock cursor timestamp = %v", err)
	}
	if updatedAt == observed.Format(time.RFC3339Nano) {
		t.Fatalf("frozen-clock cursor timestamp did not advance monotonically: %s", updatedAt)
	}
}
