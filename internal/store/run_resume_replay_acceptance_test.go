package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestM19ResumeRunReplayAfterLeaseExpiryHasNoReconciliationSideEffect(t *testing.T) {
	observed := time.Date(2035, 4, 5, 6, 7, 8, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "resume replay after lease expiry")
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "resume-replay-after-expiry",
		Steps: []domain.FakeStep{
			{Kind: domain.ObservationBlocked, Message: "Which exact behavior?"},
			{Kind: domain.ObservationProgress, Message: "resumed"},
		},
	}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-resume-replay-after-expiry")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "resume-replay-starting"); err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime", "provider", "resume-replay-started"); err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}
	blocked, err := storage.ApplyRunObservation(context.Background(), created.Run.ID,
		domain.RunObservation{Kind: domain.ObservationBlocked, Message: "Which exact behavior?"}, true, nil, "resume-replay-blocked")
	if err != nil || blocked.Run.Status != domain.RunBlocked {
		t.Fatalf("ApplyRunObservation(blocked) = %#v, %v", blocked, err)
	}

	resumeCommand := ResumeRunCommand{
		WorkspaceIdentifier: workspace.ID,
		RunID:               created.Run.ID,
		ExpectedRevision:    blocked.Run.Revision,
		IdempotencyKey:      "resume-replay-after-expiry",
		CorrelationID:       "request-resume-replay-after-expiry",
	}
	resumed, err := storage.ResumeRun(context.Background(), resumeCommand)
	if err != nil || resumed.Detail.Run.Status != domain.RunActive || resumed.EventSequence < 1 {
		t.Fatalf("ResumeRun() = %#v, %v", resumed, err)
	}

	// Leave the assignment eligible for time reconciliation: a stopped run no
	// longer protects it, while stopping deliberately retains the assignment.
	stopRequested, err := storage.RequestRunStop(context.Background(), StopRunCommand{
		WorkspaceIdentifier: workspace.ID,
		RunID:               created.Run.ID,
		ExpectedRevision:    resumed.Detail.Run.Revision,
		GracePeriodMillis:   100,
		IdempotencyKey:      "stop-before-resume-replay",
		CorrelationID:       "request-stop-before-resume-replay",
	})
	if err != nil {
		t.Fatalf("RequestRunStop() error = %v", err)
	}
	stopped, err := storage.MarkRunStopped(context.Background(), stopRequested.Detail.Run.ID, false, "stopped for replay proof", "resume-replay-stopped")
	if err != nil || stopped.Run.Status != domain.RunStopped || stopped.Task.AssignmentID == "" {
		t.Fatalf("MarkRunStopped() = %#v, %v", stopped, err)
	}

	observed = observed.Add(10 * time.Minute)
	beforeEvents := m19StoreEventCount(t, storage)
	var beforeAssignmentStatus string
	if err := storage.db.QueryRow("SELECT status FROM task_assignments WHERE id = ?", stopped.Task.AssignmentID).Scan(&beforeAssignmentStatus); err != nil {
		t.Fatalf("read assignment before replay: %v", err)
	}
	if beforeAssignmentStatus != "active" {
		t.Fatalf("assignment status before elapsed replay = %q, want active", beforeAssignmentStatus)
	}

	replayed, err := storage.ResumeRun(context.Background(), resumeCommand)
	if err != nil || !reflect.DeepEqual(replayed, resumed) {
		t.Fatalf("ResumeRun(elapsed exact replay) = %#v, %v; want exact %#v", replayed, err, resumed)
	}
	if afterEvents := m19StoreEventCount(t, storage); afterEvents != beforeEvents {
		t.Fatalf("elapsed exact replay appended events: before=%d after=%d", beforeEvents, afterEvents)
	}
	var afterAssignmentStatus string
	if err := storage.db.QueryRow("SELECT status FROM task_assignments WHERE id = ?", stopped.Task.AssignmentID).Scan(&afterAssignmentStatus); err != nil {
		t.Fatalf("read assignment after replay: %v", err)
	}
	if afterAssignmentStatus != beforeAssignmentStatus {
		t.Fatalf("elapsed exact replay reconciled assignment: before=%q after=%q", beforeAssignmentStatus, afterAssignmentStatus)
	}

	changed := resumeCommand
	changed.ExpectedRevision++
	if _, err := storage.ResumeRun(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("ResumeRun(changed elapsed replay) error = %v, code = %q", err, ErrorCode(err))
	}
	if afterConflictEvents := m19StoreEventCount(t, storage); afterConflictEvents != beforeEvents {
		t.Fatalf("changed elapsed replay appended events: before=%d after=%d", beforeEvents, afterConflictEvents)
	}
	var resumeEvents int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id = ? AND type = 'run.resumed'", created.Run.ID).Scan(&resumeEvents); err != nil {
		t.Fatalf("count resume events: %v", err)
	}
	if resumeEvents != 1 {
		t.Fatalf("run.resumed event count = %d, want exactly 1", resumeEvents)
	}
}

func m19StoreEventCount(t *testing.T, storage *Store) int {
	t.Helper()
	var count int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count store events: %v", err)
	}
	return count
}
