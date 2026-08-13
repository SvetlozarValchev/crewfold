package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestCheckWatchCursorCannotBeRawAdvancedBeforeOrAfterReopen(t *testing.T) {
	dataDirectory := t.TempDir()
	storage, err := Open(context.Background(), dataDirectory, Options{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	workspace, project := initializeWorkTestProject(t, storage)

	assertSealed := func(label string, candidate *Store) {
		t.Helper()
		var before, cutoff int64
		if err := candidate.db.QueryRowContext(context.Background(), `SELECT last_event_sequence FROM check_watch_state WHERE project_id=? AND workspace_id=?`, project.ID, workspace.ID).Scan(&before); err != nil {
			t.Fatalf("%s read check-watch cursor: %v", label, err)
		}
		if err := candidate.db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&cutoff); err != nil {
			t.Fatalf("%s read event cutoff: %v", label, err)
		}
		if _, err := candidate.db.ExecContext(context.Background(), `UPDATE check_watch_state SET last_event_sequence=?,revision=revision+1,updated_at='2099-01-01T00:00:00Z' WHERE project_id=?`, cutoff, project.ID); err == nil {
			t.Fatalf("%s raw SQL advanced the check-watch authority cursor", label)
		}
		if _, err := candidate.db.ExecContext(context.Background(), `DELETE FROM check_watch_state WHERE project_id=?`, project.ID); err == nil {
			t.Fatalf("%s raw SQL deleted durable check-watch state", label)
		}
		var after int64
		if err := candidate.db.QueryRowContext(context.Background(), `SELECT last_event_sequence FROM check_watch_state WHERE project_id=?`, project.ID).Scan(&after); err != nil {
			t.Fatalf("%s reread check-watch cursor: %v", label, err)
		}
		if after != before {
			t.Fatalf("%s cursor changed from %d to %d after rejected raw writes", label, before, after)
		}
	}

	assertSealed("initial connection", storage)
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(context.Background(), dataDirectory, Options{})
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertSealed("reopened connection", reopened)
}

func TestCheckWatchPagesKnownFactsAndScopesAcrossReopenAndWrap(t *testing.T) {
	dataDirectory := t.TempDir()
	storage, err := Open(context.Background(), dataDirectory, Options{})
	if err != nil {
		t.Fatal(err)
	}
	workspace, firstProject := initializeWorkTestProject(t, storage)
	for index := 0; index < 102; index++ {
		name := fmt.Sprintf("watch-scope-%03d", index)
		if _, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
			WorkspaceIdentifier: workspace.ID, Name: name, IdempotencyKey: "register-" + name, CorrelationID: "register-" + name,
			Observation: sourceTestObservation(filepath.Join(t.TempDir(), name), "main"),
		}); err != nil {
			t.Fatalf("RegisterProject(%s) error = %v", name, err)
		}
	}
	first, err := storage.ListCheckWatchScopes(context.Background(), ListCheckWatchScopesQuery{Limit: 100})
	if err != nil || len(first.Items) != 100 || first.NextCursor == "" {
		t.Fatalf("ListCheckWatchScopes(first) = %#v, %v", first, err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), dataDirectory, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	second, err := reopened.ListCheckWatchScopes(context.Background(), ListCheckWatchScopesQuery{After: first.NextCursor, Limit: 100})
	if err != nil || len(second.Items) != 3 || second.NextCursor != "" {
		t.Fatalf("ListCheckWatchScopes(second after reopen) = %#v, %v", second, err)
	}
	empty, err := reopened.ListCheckWatchScopes(context.Background(), ListCheckWatchScopesQuery{After: second.Items[len(second.Items)-1].ProjectID, Limit: 100})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("ListCheckWatchScopes(end) = %#v, %v", empty, err)
	}
	wrapped, err := reopened.ListCheckWatchScopes(context.Background(), ListCheckWatchScopesQuery{Limit: 100})
	if err != nil || !reflect.DeepEqual(wrapped, first) {
		t.Fatalf("ListCheckWatchScopes(wrap) = %#v, %v; want %#v", wrapped, err, first)
	}

	prepared, err := reopened.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: firstProject.ID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("PrepareCheckWatch(>100 known facts after reopen) error = %v", err)
	}
	if !prepared.CaughtUp || prepared.ThroughEventSequence-prepared.FromEventSequence <= 100 || prepared.ThroughEventSequence != prepared.CutoffEventSequence || len(prepared.Candidates) != 0 {
		t.Fatalf("known-fact preparation = %#v, want caught-up >100-fact empty pass", prepared)
	}
	result, err := reopened.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared, Observations: []CheckWatchObservation{}, IdempotencyKey: "background-known-fact-noop", CorrelationID: "background-known-fact-noop", PersistNoop: false,
	})
	if err != nil || result.EventSequence != 0 {
		t.Fatalf("ApplyCheckWatch(background known-fact noop) = %#v, %v", result, err)
	}
	assertCheckRowCount(t, reopened, 0, `SELECT COUNT(*) FROM check_watch_receipts`)
	assertCheckRowCount(t, reopened, 0, `SELECT COUNT(*) FROM events WHERE type='check.watch_completed'`)
}

func TestCheckWatchPagesMoreThanOneHundredCandidatesAcrossReopenAndWrap(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	for index := 0; index < 101; index++ {
		task := createWorkTestTask(t, fixture.storage, fixture.workspace.ID, fixture.project.ID,
			fmt.Sprintf("watch candidate %03d", index), fmt.Sprintf("watch-candidate-task-%03d", index))
		requirement, err := fixture.storage.CreateTaskCheckRequirement(context.Background(), CreateTaskCheckRequirementCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Task.ID,
			CriterionKey: fmt.Sprintf("watch-candidate-%03d", index), Statement: "this exact candidate must remain page-visible",
			CheckDefinitionID: fixture.definition.ID, DefinitionContentRevision: fixture.definition.ContentRevision,
			ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: fmt.Sprintf("watch-candidate-requirement-%03d", index), CorrelationID: fmt.Sprintf("watch-candidate-requirement-%03d", index),
		})
		if err != nil {
			t.Fatalf("CreateTaskCheckRequirement(%d) error = %v", index, err)
		}
		requested, err := fixture.storage.RequestCheckRun(context.Background(), RequestCheckRunCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Task.ID, RequirementID: requirement.Value.ID,
			CheckDefinitionIdentifier: fixture.definition.ID, CheckoutIdentifier: fixture.checkout.ID,
			ExpectedRequirementRevision: requirement.Value.Revision, ExpectedDefinitionContentRevision: fixture.definition.ContentRevision, ExpectedCheckoutRevision: fixture.checkout.Revision,
			IdempotencyKey: fmt.Sprintf("watch-candidate-run-%03d", index), CorrelationID: fmt.Sprintf("watch-candidate-run-%03d", index),
		})
		if err != nil {
			t.Fatalf("RequestCheckRun(%d) error = %v", index, err)
		}
		work, found, err := fixture.storage.ClaimCheckJob(context.Background(), 30*time.Second)
		if err != nil || !found || work.Run.ID != requested.Value.ID {
			t.Fatalf("ClaimCheckJob(%d) = %#v, %t, %v", index, work, found, err)
		}
		fixture.advance()
		started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
		if err != nil || started.LaunchReceipt == nil {
			t.Fatalf("MarkCheckStarting(%d) = %#v, %v", index, started, err)
		}
		fixture.advance()
		if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, fmt.Sprintf("watch-candidate-binding-%03d", index)); err != nil {
			t.Fatal(err)
		}
		fixture.advance()
		if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, fmt.Sprintf("watch-candidate-running-%03d", index)); err != nil {
			t.Fatal(err)
		}
		fixture.advance()
		exitCode := 0
		terminal := started.LaunchReceipt.Observation
		terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
		if _, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
			CheckRunID: work.Run.ID, Outcome: domain.CheckOutcomePassed, ExitCode: &exitCode,
			TerminalObservation: terminal, CorrelationID: fmt.Sprintf("watch-candidate-finish-%03d", index),
		}); err != nil {
			t.Fatalf("FinishCheckRun(%d) error = %v", index, err)
		}
	}
	first := prepareCheckWatchFixture(t, fixture, 100, "")
	if !first.CaughtUp || len(first.Candidates) != 100 || first.ThroughResultID == "" {
		t.Fatalf("first candidate page = caught_up %t, candidates %d, through %q", first.CaughtUp, len(first.Candidates), first.ThroughResultID)
	}
	fixture.advance()
	firstResult := applyPreparedCheckWatchNoop(t, fixture, first, false, "candidate-page-one")
	if firstResult.EventSequence != 0 || firstResult.Value.NextCursor == "" {
		t.Fatalf("first background candidate page = %#v", firstResult)
	}

	dataDirectory := filepath.Dir(fixture.storage.path)
	if err := fixture.storage.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), dataDirectory, Options{Clock: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	fixture.storage = reopened
	t.Cleanup(func() { _ = reopened.Close() })
	second := prepareCheckWatchFixture(t, fixture, 100, firstResult.Value.NextCursor)
	if !second.CaughtUp || len(second.Candidates) != 1 || second.ThroughResultID != "" {
		t.Fatalf("second candidate page after reopen = caught_up %t, candidates %d, through %q", second.CaughtUp, len(second.Candidates), second.ThroughResultID)
	}
	fixture.advance()
	secondResult := applyPreparedCheckWatchNoop(t, fixture, second, false, "candidate-page-two")
	wrapped := prepareCheckWatchFixture(t, fixture, 100, secondResult.Value.NextCursor)
	if len(wrapped.Candidates) != 100 || wrapped.FromResultID != "" {
		t.Fatalf("wrapped candidate page = from %q candidates %d, want reset+100", wrapped.FromResultID, len(wrapped.Candidates))
	}
	assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_watch_receipts`)
	assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE type='check.watch_completed'`)
}

func TestCheckWatchPreparedSnapshotRejectsTamperingAndChangedSource(t *testing.T) {
	fixture, finished := finishCheckWatchFixture(t, domain.CheckOutcomePassed)
	prepared := prepareCheckWatchFixture(t, fixture, 100, "")
	if len(prepared.Candidates) != 1 {
		t.Fatalf("PrepareCheckWatch().Candidates = %#v, want one", prepared.Candidates)
	}
	observation := preparedObservation(fixture, prepared.Candidates[0])

	t.Run("preparation hash", func(t *testing.T) {
		command := ApplyCheckWatchCommand{Preparation: prepared, Observations: []CheckWatchObservation{{
			CheckResultID: finished.Result.ID, FreshnessRevision: 1, Observation: observation,
		}}, IdempotencyKey: "watch-tampered-preparation", CorrelationID: "watch-tampered-preparation", PersistNoop: true}
		command.Preparation.Candidates[0].CheckoutPath += "-substituted"
		if _, err := fixture.storage.ApplyCheckWatch(context.Background(), command); ErrorCode(err) != CodeCheckRunConflict {
			t.Fatalf("ApplyCheckWatch(tampered preparation) error = %v, code %q", err, ErrorCode(err))
		}
		assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_watch_receipts`)
	})

	// A Store source fact changed after the read-only preparation. Apply must not
	// trust the old filesystem path/revision even if its supplied observation is
	// otherwise perfectly canonical.
	fixture.advance()
	if _, err := fixture.storage.ApplyCheckoutObservations(context.Background(), fixture.workspace.ID, fixture.project.ID,
		"change-checkout-after-watch-prepare", map[string]domain.CheckoutObservation{
			fixture.checkout.ID: checkoutObservationFromCheck(observation, fixture.checkout.Path, differentCheckCommit(observation.HeadCommit)),
		}); err != nil {
		t.Fatalf("ApplyCheckoutObservations() error = %v", err)
	}
	if _, err := fixture.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared, Observations: []CheckWatchObservation{{CheckResultID: finished.Result.ID, FreshnessRevision: 1, Observation: observation}},
		IdempotencyKey: "watch-changed-source", CorrelationID: "watch-changed-source", PersistNoop: true,
	}); ErrorCode(err) != CodeCheckRunConflict {
		t.Fatalf("ApplyCheckWatch(changed source) error = %v, code %q", err, ErrorCode(err))
	}
	assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_watch_receipts`)
}

func TestCheckWatchUnknownEventStopsBeforeCursorAndEffect(t *testing.T) {
	fixture, finished := finishCheckWatchFixture(t, domain.CheckOutcomePassed)
	var before int64
	if err := fixture.storage.db.QueryRow(`SELECT last_event_sequence FROM check_watch_state WHERE project_id=?`, fixture.project.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	if _, err := fixture.storage.db.Exec(`INSERT INTO events(event_id,type,schema_version,occurred_at,recorded_at,actor_id,actor_type,workspace_id,entity_type,entity_id,entity_revision,correlation_id,causation_id,data_json)
		VALUES(?,?,?,?,?,'local-owner','human',?,'project',?,1,'unknown-check-watch-event',NULL,'{}')`,
		"evt_"+strings.Repeat("f", 32), "future.check.fact", 1, fixture.now.Format(time.RFC3339Nano), fixture.now.Format(time.RFC3339Nano), fixture.workspace.ID, fixture.project.ID); err != nil {
		t.Fatalf("insert unknown immutable event: %v", err)
	}
	if _, err := fixture.storage.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, Limit: 100,
	}); ErrorCode(err) != CodeUnsupportedCheckEvent {
		t.Fatalf("PrepareCheckWatch(unknown event) error = %v, code %q", err, ErrorCode(err))
	}
	var after int64
	if err := fixture.storage.db.QueryRow(`SELECT last_event_sequence FROM check_watch_state WHERE project_id=?`, fixture.project.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unknown event advanced watch cursor from %d to %d", before, after)
	}
	assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_result_freshness WHERE check_result_id=?`, finished.Result.ID)
	assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_watch_receipts`)
}

func TestCheckWatchFailedResultBecomesCurrentInconclusiveWhenStale(t *testing.T) {
	fixture, finished := finishCheckWatchFixture(t, domain.CheckOutcomeFailed)
	prepared := prepareCheckWatchFixture(t, fixture, 100, "")
	observation := preparedObservation(fixture, prepared.Candidates[0])
	observation.HeadCommit = differentCheckCommit(observation.HeadCommit)
	fixture.advance()
	observation.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	result, err := fixture.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared, Observations: []CheckWatchObservation{{CheckResultID: finished.Result.ID, FreshnessRevision: 1, Observation: observation}},
		IdempotencyKey: "watch-failed-result-stale", CorrelationID: "watch-failed-result-stale", PersistNoop: true,
	})
	if err != nil || result.Value.FreshnessAppended != 1 {
		t.Fatalf("ApplyCheckWatch(stale failed result) = %#v, %v", result, err)
	}
	detail, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, finished.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CurrentFreshness == nil || detail.CurrentFreshness.Status != domain.CheckFreshnessStale || detail.CurrentFreshness.Revision != 2 {
		t.Fatalf("current freshness = %#v, want stale revision 2", detail.CurrentFreshness)
	}
	for _, evidence := range detail.Evidence.MechanicalCheck {
		if evidence.FreshnessRevision == 2 && evidence.Effect != domain.CheckEvidenceInconclusive {
			t.Fatalf("current stale evidence effect = %q, want inconclusive", evidence.Effect)
		}
	}
}

func TestCheckWatchExcludesResultsForRetiredRequirements(t *testing.T) {
	fixture, finished := finishCheckWatchFixture(t, domain.CheckOutcomePassed)
	fixture.advance()
	retired, err := fixture.storage.RetireTaskCheckRequirement(context.Background(), RetireTaskCheckRequirementCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RequirementID: fixture.requirement.ID,
		ExpectedRevision: fixture.requirement.Revision, Reason: "the owner retired this exact criterion",
		IdempotencyKey: "retire-watch-candidate-requirement", CorrelationID: "retire-watch-candidate-requirement",
	})
	if err != nil || retired.Value.Status != domain.CheckRequirementRetired {
		t.Fatalf("RetireTaskCheckRequirement() = %#v, %v", retired, err)
	}
	prepared := prepareCheckWatchFixture(t, fixture, 100, "")
	for _, candidate := range prepared.Candidates {
		if candidate.CheckResultID == finished.Result.ID {
			t.Fatalf("retired requirement result remained a current watch candidate: %#v", candidate)
		}
	}
	if len(prepared.Candidates) != 0 {
		t.Fatalf("watch candidates after sole requirement retired = %#v, want empty", prepared.Candidates)
	}
	result := applyPreparedCheckWatchNoop(t, fixture, prepared, false, "watch-retired-requirement")
	if result.Value.FreshnessAppended != 0 || result.Value.NotificationsCreated != 0 || result.Value.RouteFailuresCreated != 0 || result.Value.RepairsMarkedStale != 0 {
		t.Fatalf("retired requirement watch effects = %#v, want none", result.Value)
	}
}

func TestCheckWatchStaleTransitionRoutesOnlyTheExactStaleDuty(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	staleRecipient := createCheckNotificationAgent(t, fixture, "exact-stale-route-agent", fixture.agent.Role)
	nonpassRecipient := createCheckNotificationAgent(t, fixture, "initial-nonpass-route-agent", fixture.agent.Role)
	nonpassRoute, err := fixture.storage.CreateCheckRoute(context.Background(), CreateCheckRouteCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		CheckDefinitionID: fixture.definition.ID, DefinitionContentRevision: fixture.definition.ContentRevision,
		Trigger: domain.CheckRouteNonpass, Duty: domain.CheckDutyEvidenceReview,
		AgentIdentifier: nonpassRecipient.ID, ExpectedAgentRevision: nonpassRecipient.Revision,
		IdempotencyKey: "initial-check-nonpass-route", CorrelationID: "initial-check-nonpass-route",
	})
	if err != nil {
		t.Fatalf("CreateCheckRoute(nonpass) error = %v", err)
	}
	route, err := fixture.storage.CreateCheckRoute(context.Background(), CreateCheckRouteCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		CheckDefinitionID: fixture.definition.ID, DefinitionContentRevision: fixture.definition.ContentRevision,
		Trigger: domain.CheckRouteStale, Duty: domain.CheckDutyCoordination,
		AgentIdentifier: staleRecipient.ID, ExpectedAgentRevision: staleRecipient.Revision,
		IdempotencyKey: "exact-check-stale-route", CorrelationID: "exact-check-stale-route",
	})
	if err != nil {
		t.Fatalf("CreateCheckRoute(stale) error = %v", err)
	}
	finished := finishExistingCheckFixture(t, fixture, domain.CheckOutcomeFailed)
	if len(finished.Notifications) != 2 || len(finished.RouteFailures) != 0 {
		t.Fatalf("initial failed-result routes = notices %#v failures %#v; want task owner plus exact nonpass route", finished.Notifications, finished.RouteFailures)
	}
	for _, notice := range finished.Notifications {
		if notice.FreshnessRevision != 1 || notice.RouteID == route.Value.ID {
			t.Fatalf("initial failure incorrectly used stale route: %#v", notice)
		}
		if notice.RecipientAgentID == nonpassRecipient.ID && notice.RouteID != nonpassRoute.Value.ID {
			t.Fatalf("initial nonpass route authority = %#v", notice)
		}
	}
	prepared := prepareCheckWatchFixture(t, fixture, 100, "")
	if len(prepared.Candidates) != 1 || prepared.Candidates[0].CheckResultID != finished.Result.ID {
		t.Fatalf("prepared stale candidate = %#v, want result %s", prepared.Candidates, finished.Result.ID)
	}
	fixture.advance()
	observation := preparedObservation(fixture, prepared.Candidates[0])
	observation.HeadCommit = strings.Repeat("b", len(observation.HeadCommit))
	observation.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	result, err := fixture.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared,
		Observations: []CheckWatchObservation{{
			CheckResultID: finished.Result.ID, FreshnessRevision: finished.CurrentFreshness.Revision, Observation: observation,
		}},
		IdempotencyKey: "watch-route-stale-result", CorrelationID: "watch-route-stale-result",
	})
	if err != nil {
		t.Fatalf("ApplyCheckWatch(stale route) error = %v", err)
	}
	if result.Value.FreshnessAppended != 1 || result.Value.NotificationsCreated != 1 || result.Value.RouteFailuresCreated != 0 {
		t.Fatalf("stale watch receipt = %#v; want one freshness and one exact notification", result.Value)
	}
	detail, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, finished.Run.ID)
	if err != nil {
		t.Fatalf("CheckRunDetail(stale route) error = %v", err)
	}
	if len(detail.Notifications) != 3 || len(detail.RouteFailures) != 0 {
		t.Fatalf("stale route bundle = notices %#v failures %#v", detail.Notifications, detail.RouteFailures)
	}
	var notice domain.CheckNotificationReceipt
	for _, candidate := range detail.Notifications {
		if candidate.FreshnessRevision == 2 {
			if notice.ID != "" {
				t.Fatalf("stale transition repeated owner/nonpass routing: %#v", detail.Notifications)
			}
			notice = candidate
		}
	}
	if notice.RouteID != route.Value.ID || notice.Duty != domain.CheckDutyCoordination || notice.RecipientAgentID != staleRecipient.ID || notice.FreshnessRevision != 2 || notice.AssignmentID != "" {
		t.Fatalf("stale notification authority = %#v", notice)
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, staleRecipient.ID, 10); err != nil || len(inbox) != 1 || inbox[0].Message.ID != notice.MessageID || inbox[0].Message.SenderType != "subsystem" || inbox[0].Message.SenderID != "crewfold-check-worker" {
		t.Fatalf("stale route Inbox() = %#v, %v", inbox, err)
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, nonpassRecipient.ID, 10); err != nil || len(inbox) != 1 {
		t.Fatalf("stale transition repeated initial nonpass route Inbox() = %#v, %v", inbox, err)
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, fixture.agent.ID, 10); err != nil || len(inbox) != 1 {
		t.Fatalf("stale transition repeated mandatory task-owner Inbox() = %#v, %v", inbox, err)
	}
}

func TestCheckWatchPublicExplicitCursorReplayReturnsFrozenReceipt(t *testing.T) {
	fixture, finished := finishCheckWatchFixture(t, domain.CheckOutcomePassed)
	firstPreparation := prepareCheckWatchFixture(t, fixture, 1, "")
	firstObservation := preparedObservation(fixture, firstPreparation.Candidates[0])
	first, err := fixture.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: firstPreparation, Observations: []CheckWatchObservation{{CheckResultID: finished.Result.ID, FreshnessRevision: 1, Observation: firstObservation}},
		IdempotencyKey: "watch-establish-explicit-cursor", CorrelationID: "watch-establish-explicit-cursor", PersistNoop: true,
	})
	if err != nil || first.Value.NextCursor == "" {
		t.Fatalf("ApplyCheckWatch(first cursor) = %#v, %v", first, err)
	}
	secondPreparation := prepareCheckWatchFixture(t, fixture, 100, first.Value.NextCursor)
	secondObservations := make([]CheckWatchObservation, 0, len(secondPreparation.Candidates))
	for _, candidate := range secondPreparation.Candidates {
		secondObservations = append(secondObservations, CheckWatchObservation{CheckResultID: candidate.CheckResultID, FreshnessRevision: candidate.FreshnessRevision, Observation: preparedObservation(fixture, candidate)})
	}
	secondCommand := ApplyCheckWatchCommand{Preparation: secondPreparation, Observations: secondObservations,
		IdempotencyKey: "watch-explicit-cursor-replay", CorrelationID: "watch-explicit-cursor-replay", PersistNoop: true}
	second, err := fixture.storage.ApplyCheckWatch(context.Background(), secondCommand)
	if err != nil {
		t.Fatalf("ApplyCheckWatch(explicit cursor) error = %v", err)
	}

	request := PrepareCheckWatchCommand{WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, After: first.Value.NextCursor, Limit: 100}
	directReplay, found, err := fixture.storage.ReplayCheckWatch(context.Background(), request, secondCommand.IdempotencyKey)
	if err != nil || !found || !reflect.DeepEqual(directReplay, second) {
		t.Fatalf("ReplayCheckWatch(exact public replay) = %#v, found %t, %v; want %#v", directReplay, found, err, second)
	}
	changed := request
	changed.Limit = 99
	if _, _, err := fixture.storage.ReplayCheckWatch(context.Background(), changed, secondCommand.IdempotencyKey); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("ReplayCheckWatch(changed request, same key) error = %v, code %q", err, ErrorCode(err))
	}

	// The stale-cursor fallback also remains fail-closed when a caller bypasses
	// the public preflight or supplies a new idempotency key.
	replayPreparation, err := fixture.storage.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, After: first.Value.NextCursor, Limit: 100,
	})
	if err != nil {
		t.Fatalf("PrepareCheckWatch(exact public replay) error = %v", err)
	}
	replayCommand := secondCommand
	replayCommand.Preparation = replayPreparation
	replayed, err := fixture.storage.ApplyCheckWatch(context.Background(), replayCommand)
	if err != nil || !reflect.DeepEqual(replayed, second) {
		t.Fatalf("ApplyCheckWatch(exact public replay) = %#v, %v; want %#v", replayed, err, second)
	}
	replayCommand.IdempotencyKey = "watch-explicit-stale-cursor-new-key"
	if _, err := fixture.storage.ApplyCheckWatch(context.Background(), replayCommand); ErrorCode(err) != CodeCheckRunConflict {
		t.Fatalf("ApplyCheckWatch(stale cursor, new key) error = %v, code %q", err, ErrorCode(err))
	}
}

func finishCheckWatchFixture(t *testing.T, outcome string) (*grantedCheckAuthorityFixture, domain.CheckRunDetail) {
	t.Helper()
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "watch-runtime-binding"); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "watch-running"); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	exitCode := 0
	if outcome == domain.CheckOutcomeFailed {
		exitCode = 1
	}
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID: work.Run.ID, Outcome: outcome, ExitCode: &exitCode, TerminalObservation: terminal, CorrelationID: "watch-finish-" + outcome,
	})
	if err != nil || finished.Result == nil || finished.CurrentFreshness == nil {
		t.Fatalf("FinishCheckRun(%s) = %#v, %v", outcome, finished, err)
	}
	return fixture, finished
}

func prepareCheckWatchFixture(t *testing.T, fixture *grantedCheckAuthorityFixture, limit int, after string) PreparedCheckWatch {
	t.Helper()
	prepared, err := fixture.storage.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, After: after, Limit: limit,
	})
	if err != nil {
		t.Fatalf("PrepareCheckWatch() error = %v", err)
	}
	return prepared
}

func preparedObservation(fixture *grantedCheckAuthorityFixture, candidate CheckWatchCandidate) domain.CheckGitObservation {
	return domain.CheckGitObservation{Available: true, RepositoryID: candidate.RepositoryID, ObjectFormat: candidate.ObjectFormat,
		CheckoutID: candidate.CheckoutID, Branch: fixture.checkout.Branch, HeadCommit: fixture.checkout.HeadCommit,
		DirtyPaths: []string{}, ObservedAt: fixture.now.Format(time.RFC3339Nano)}
}

func applyPreparedCheckWatchNoop(t *testing.T, fixture *grantedCheckAuthorityFixture, prepared PreparedCheckWatch, persist bool, key string) MutationResult[domain.CheckWatchReceipt] {
	t.Helper()
	observations := make([]CheckWatchObservation, 0, len(prepared.Candidates))
	for _, candidate := range prepared.Candidates {
		observations = append(observations, CheckWatchObservation{
			CheckResultID: candidate.CheckResultID, FreshnessRevision: candidate.FreshnessRevision,
			Observation: preparedObservation(fixture, candidate),
		})
	}
	result, err := fixture.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared, Observations: observations, IdempotencyKey: key, CorrelationID: key, PersistNoop: persist,
	})
	if err != nil {
		t.Fatalf("ApplyCheckWatch(%s) error = %v", key, err)
	}
	return result
}

func checkoutObservationFromCheck(observation domain.CheckGitObservation, path, head string) domain.CheckoutObservation {
	return domain.CheckoutObservation{Path: path, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
		Repository: domain.RepositoryObservation{Fingerprint: "sha256:" + strings.Repeat("a", 64), ObjectFormat: observation.ObjectFormat, RootCommits: []string{strings.Repeat("a", 40)}},
		Branch:     observation.Branch, HeadCommit: head, DirtyPaths: []string{}, GitDir: path + "/.git", GitCommonDir: path + "/.git"}
}
