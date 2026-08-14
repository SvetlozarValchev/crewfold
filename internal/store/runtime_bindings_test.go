package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

const (
	foreignRuntimeNodeID          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	foreignRuntimeNodeFingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestRuntimeHandlesExistOnlyInDedicatedNodeBoundTables(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})

	for _, table := range []string{"runs", "check_runs"} {
		columns := runtimeBindingTestColumns(t, storage, table)
		for _, forbidden := range []string{"runtime_handle", "provider_handle", "node_id", "node_fingerprint", "operation_id"} {
			for _, column := range columns {
				if column == forbidden {
					t.Fatalf("canonical table %s retains node-local column %s: %v", table, forbidden, columns)
				}
			}
		}
	}
	if got, want := runtimeBindingTestColumns(t, storage, "run_runtime_bindings"), []string{
		"run_id", "node_id", "node_fingerprint", "operation_id", "runtime_handle", "provider_handle", "revision", "created_at", "updated_at",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("run_runtime_bindings columns = %v, want %v", got, want)
	}
	if got, want := runtimeBindingTestColumns(t, storage, "check_runtime_bindings"), []string{
		"check_run_id", "node_id", "node_fingerprint", "operation_id", "runtime_handle", "revision", "created_at", "updated_at",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("check_runtime_bindings columns = %v, want %v", got, want)
	}
}

func TestRunRuntimeBindingRejectsForeignNodeAndSurvivesLostUntilOwnerResolution(t *testing.T) {
	dataDirectory := t.TempDir()
	storage := openTestStore(t, dataDirectory, Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "node-bound lost run")
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "node-bound-lost",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "wait", WaitForResume: true}},
	}, "node-bound-lost")
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "node-bound-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	bound, err := storage.RecordRunRuntimeBinding(context.Background(), starting.ID, "runtime:"+starting.ID, "node-bound-runtime")
	if err != nil {
		t.Fatalf("RecordRunRuntimeBinding() error = %v", err)
	}
	if !storage.RunBindingIsCurrent(bound) || bound.RuntimeNodeID != storage.runtimeNodeID ||
		bound.RuntimeNodeFingerprint != storage.runtimeNodeFingerprint || bound.RuntimeOperationID != bound.ID {
		t.Fatalf("current run binding = %#v", bound)
	}
	assertRuntimeBindingRowCount(t, storage, "run_runtime_bindings", "run_id", bound.ID, 1)

	foreign, err := Open(context.Background(), dataDirectory, Options{
		RuntimeNodeID: foreignRuntimeNodeID, RuntimeNodeFingerprint: foreignRuntimeNodeFingerprint,
	})
	if err != nil {
		t.Fatalf("Open(foreign node) error = %v", err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	foreignRun, err := foreign.RunByID(context.Background(), bound.ID)
	if err != nil {
		t.Fatalf("RunByID(foreign node) error = %v", err)
	}
	if foreign.RunBindingIsCurrent(foreignRun) {
		t.Fatalf("foreign store accepted current binding: %#v", foreignRun)
	}
	if _, err := foreign.MarkRunStarted(context.Background(), bound.ID, bound.RuntimeHandle, "provider:"+bound.ID, "foreign-provider-bind"); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("MarkRunStarted(foreign node) error = %v, code = %q", err, ErrorCode(err))
	}

	active, err := storage.MarkRunStarted(context.Background(), bound.ID, bound.RuntimeHandle, "provider:"+bound.ID, "current-provider-bind")
	if err != nil || !storage.RunBindingIsCurrent(active.Run) {
		t.Fatalf("MarkRunStarted(current node) = %#v, %v", active, err)
	}
	stopKey := "foreign-node-must-not-stop"
	beforeStop := runtimeBindingControlSnapshotForTest(t, foreign, active.Run.ID, stopKey)
	if _, err := foreign.RequestRunStop(context.Background(), StopRunCommand{
		WorkspaceIdentifier: workspace.ID,
		RunID:               active.Run.ID,
		ExpectedRevision:    active.Run.Revision,
		GracePeriodMillis:   100,
		IdempotencyKey:      stopKey,
		CorrelationID:       "foreign-node-stop",
	}); ErrorCode(err) != CodeRuntimeBindingUnavailable {
		t.Fatalf("RequestRunStop(foreign node) error = %v, code = %q; want %q", err, ErrorCode(err), CodeRuntimeBindingUnavailable)
	}
	if afterStop := runtimeBindingControlSnapshotForTest(t, foreign, active.Run.ID, stopKey); !reflect.DeepEqual(afterStop, beforeStop) {
		t.Fatalf("foreign run stop changed state before binding refusal: before=%#v after=%#v", beforeStop, afterStop)
	}
	foreignIntegrity, err := foreign.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || !foreignIntegrity.Complete || foreignIntegrity.Status != "failed" ||
		!semanticViolationNamed(foreignIntegrity, "run", "current_node_runtime_binding") {
		t.Fatalf("VerifyCanonical(foreign active run binding) = %#v, %v", foreignIntegrity, err)
	}
	forgedAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := storage.writeDB.ExecContext(context.Background(), `UPDATE runs SET status='failed',failure_code='forged',failure_message='forged',finished_at=?,revision=revision+1,updated_at=? WHERE id=?`, forgedAt, forgedAt, bound.ID); err == nil {
		t.Fatal("raw terminal transition retained a live runtime binding")
	}

	lost, err := storage.LoseRun(context.Background(), bound.ID, "runtime ownership became uncertain", "node-bound-lost-transition")
	if err != nil || lost.Run.Status != domain.RunLost || lost.Run.RuntimeHandle == "" {
		t.Fatalf("LoseRun() = %#v, %v", lost, err)
	}
	assertRuntimeBindingRowCount(t, storage, "run_runtime_bindings", "run_id", bound.ID, 1)
	report, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || !hasQuiescenceBlocker(report.QuiescenceBlockers, "run_runtime_binding", bound.ID) {
		t.Fatalf("VerifyCanonical(lost binding) = %#v, %v", report, err)
	}
	resolved, err := storage.ResolveLostRun(context.Background(), ResolveLostRunCommand{
		WorkspaceIdentifier: workspace.ID, RunID: bound.ID, ExpectedRevision: lost.Run.Revision,
		RuntimeRetiredConfirmed: true, Note: "owner verified the old operation is retired",
		IdempotencyKey: "resolve-node-bound-lost", CorrelationID: "resolve-node-bound-lost",
	})
	if err != nil || resolved.Detail.Run.Status != domain.RunFailed || resolved.Detail.Run.RuntimeHandle != "" || resolved.Detail.Run.RuntimeNodeID != "" {
		t.Fatalf("ResolveLostRun() = %#v, %v", resolved, err)
	}
	assertRuntimeBindingRowCount(t, storage, "run_runtime_bindings", "run_id", bound.ID, 0)
}

func TestRunResumeRejectsMissingBindingBeforeAssignmentReconciliation(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, _, assigned := initializeRunTest(t, storage, "missing binding resume")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "missing-binding-resume",
		Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "wait for owner"}},
	}, "missing-binding-resume")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "missing-binding-resume-starting"); err != nil {
		t.Fatal(err)
	}
	active, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime:"+created.Run.ID, "provider:"+created.Run.ID, "missing-binding-resume-active")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := storage.ApplyRunObservation(context.Background(), active.Run.ID, domain.RunObservation{
		Kind: domain.ObservationBlocked, Message: "wait for owner",
	}, true, nil, "missing-binding-resume-blocked")
	if err != nil || blocked.Run.Status != domain.RunBlocked {
		t.Fatalf("ApplyRunObservation(blocked) = %#v, %v", blocked, err)
	}

	unrelated := createWorkTestTask(t, storage, workspace.ID, project.ID, "unrelated expired assignment", "unrelated-expired-assignment-task")
	unrelatedAssigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID,
		TaskID:              unrelated.Task.ID,
		AgentIdentifier:     agent.ID,
		LeaseSeconds:        300,
		ExpectedRevision:    unrelated.Task.Revision,
		IdempotencyKey:      "unrelated-expired-assignment-assign",
		CorrelationID:       "unrelated-expired-assignment-assign",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.writeDB.ExecContext(context.Background(), `UPDATE task_assignments SET lease_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), unrelatedAssigned.Detail.Task.AssignmentID); err != nil {
		t.Fatalf("expire unrelated assignment: %v", err)
	}
	if _, err := storage.writeDB.ExecContext(context.Background(), `DELETE FROM run_runtime_bindings WHERE run_id=?`, blocked.Run.ID); err != nil {
		t.Fatalf("delete target runtime binding: %v", err)
	}

	const key = "missing-binding-resume-refusal"
	before := runtimeBindingControlSnapshotForTest(t, storage, blocked.Run.ID, key)
	beforeUnrelated := assignmentStateForControlTest(t, storage, unrelated.Task.ID)
	if _, err := storage.ResumeRun(context.Background(), ResumeRunCommand{
		WorkspaceIdentifier: workspace.ID,
		RunID:               blocked.Run.ID,
		ExpectedRevision:    blocked.Run.Revision,
		IdempotencyKey:      key,
		CorrelationID:       "missing-binding-resume-refusal",
	}); ErrorCode(err) != CodeRuntimeBindingUnavailable {
		t.Fatalf("ResumeRun(missing binding) error = %v, code = %q; want %q", err, ErrorCode(err), CodeRuntimeBindingUnavailable)
	}
	if after := runtimeBindingControlSnapshotForTest(t, storage, blocked.Run.ID, key); !reflect.DeepEqual(after, before) {
		t.Fatalf("missing-binding resume changed control state: before=%#v after=%#v", before, after)
	}
	if afterUnrelated := assignmentStateForControlTest(t, storage, unrelated.Task.ID); afterUnrelated != beforeUnrelated {
		t.Fatalf("missing-binding resume reconciled unrelated assignment: before=%q after=%q", beforeUnrelated, afterUnrelated)
	}
}

type runtimeBindingControlSnapshot struct {
	Run              domain.Run
	Task             domain.Task
	JobStatus        string
	JobLease         string
	JobAttempts      int
	JobUpdatedAt     string
	EventCount       int
	WorkspaceEvents  int
	TimelineCount    int
	IdempotencyCount int
}

func runtimeBindingControlSnapshotForTest(t *testing.T, storage *Store, runID, idempotencyKey string) runtimeBindingControlSnapshot {
	t.Helper()
	run, err := storage.RunByID(context.Background(), runID)
	if err != nil {
		t.Fatalf("RunByID(control snapshot) error = %v", err)
	}
	task, err := queryTask(context.Background(), storage.db, run.WorkspaceID, run.TaskID)
	if err != nil {
		t.Fatalf("queryTask(control snapshot) error = %v", err)
	}
	snapshot := runtimeBindingControlSnapshot{Run: run, Task: task}
	err = storage.db.QueryRowContext(context.Background(), `
SELECT job.status,COALESCE(job.lease_expires_at,''),job.attempts,job.updated_at,
 (SELECT COUNT(*) FROM events event WHERE event.entity_type='run' AND event.entity_id=?),
 (SELECT COUNT(*) FROM events event WHERE event.workspace_id=?),
 (SELECT COUNT(*) FROM run_timeline timeline WHERE timeline.run_id=?),
 (SELECT COUNT(*) FROM idempotency_keys receipt WHERE receipt.key=?)
FROM run_jobs job WHERE job.run_id=?`, runID, run.WorkspaceID, runID, idempotencyKey, runID).Scan(
		&snapshot.JobStatus, &snapshot.JobLease, &snapshot.JobAttempts, &snapshot.JobUpdatedAt,
		&snapshot.EventCount, &snapshot.WorkspaceEvents, &snapshot.TimelineCount, &snapshot.IdempotencyCount,
	)
	if err != nil {
		t.Fatalf("read run control snapshot: %v", err)
	}
	return snapshot
}

func assignmentStateForControlTest(t *testing.T, storage *Store, taskID string) string {
	t.Helper()
	var taskStatus, assignmentStatus, leaseExpiresAt string
	var taskRevision, assignmentRevision int
	if err := storage.db.QueryRowContext(context.Background(), `
SELECT task.status,task.revision,assignment.status,assignment.revision,assignment.lease_expires_at
FROM tasks task JOIN task_assignments assignment ON assignment.task_id=task.id
WHERE task.id=?`, taskID).Scan(&taskStatus, &taskRevision, &assignmentStatus, &assignmentRevision, &leaseExpiresAt); err != nil {
		t.Fatalf("read assignment control state: %v", err)
	}
	return fmt.Sprintf("%s/%d/%s/%d/%s", taskStatus, taskRevision, assignmentStatus, assignmentRevision, leaseExpiresAt)
}

func TestRunTerminalTransitionAtomicallyClearsRuntimeBinding(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "terminal binding clear")
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "terminal-binding-clear",
		Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}},
	}, "terminal-binding-clear")
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "terminal-binding-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	active, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime:"+starting.ID, "provider:"+starting.ID, "terminal-binding-active")
	if err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}
	archive := prepareTestRunLogArchive(t, storage, active.Run.ID)
	terminal, err := storage.ApplyRunObservation(context.Background(), active.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review", LogArchive: archive,
	}, true, nil, "terminal-binding-complete")
	if err != nil || terminal.Run.Status != domain.RunCompleted || terminal.Run.RuntimeHandle != "" || terminal.Run.RuntimeNodeID != "" {
		t.Fatalf("ApplyRunObservation(terminal) = %#v, %v", terminal, err)
	}
	assertRuntimeBindingRowCount(t, storage, "run_runtime_bindings", "run_id", active.Run.ID, 0)
}

func TestRecoverRunJobLeasesPreservesExactLiveBindingAndReclaimsImmediately(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "recover bound run lease")
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "recover-bound-run-lease",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "continue"}},
	}, "recover-bound-run-lease")
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "recover-run-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	active, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime:"+starting.ID, "provider:"+starting.ID, "recover-run-active")
	if err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}
	first, found, err := storage.ClaimRunControlJob(context.Background(), time.Hour)
	if err != nil || !found || first.Run.ID != active.Run.ID || !storage.RunBindingIsCurrent(first.Run) {
		t.Fatalf("ClaimRunControlJob(first) = %#v, %t, %v", first, found, err)
	}
	if err := storage.RecoverRunJobLeases(context.Background()); err != nil {
		t.Fatalf("RecoverRunJobLeases() error = %v", err)
	}
	var status string
	var lease *string
	var attempts int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT status,lease_expires_at,attempts FROM run_jobs WHERE run_id=?`, active.Run.ID).Scan(&status, &lease, &attempts); err != nil {
		t.Fatalf("read recovered run job: %v", err)
	}
	if status != "pending" || lease != nil || attempts != 1 {
		t.Fatalf("recovered run job = status %q lease %v attempts %d; want pending nil 1", status, lease, attempts)
	}
	second, found, err := storage.ClaimRunControlJob(context.Background(), time.Hour)
	if err != nil || !found || second.Run.ID != active.Run.ID || second.Run.RuntimeHandle != first.Run.RuntimeHandle || !storage.RunBindingIsCurrent(second.Run) {
		t.Fatalf("ClaimRunControlJob(recovered) = %#v, %t, %v; first %#v", second, found, err, first)
	}
}

func TestCheckRuntimeBindingRejectsForeignNodeAndClearsAtTerminal(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	bound, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "node-bound-check-runtime")
	if err != nil || !fixture.storage.CheckBindingIsCurrent(bound) || bound.RuntimeOperationID != bound.ID {
		t.Fatalf("RecordCheckRuntimeBinding() = %#v, %v", bound, err)
	}
	assertRuntimeBindingRowCount(t, fixture.storage, "check_runtime_bindings", "check_run_id", bound.ID, 1)

	foreign, err := Open(context.Background(), filepath.Dir(fixture.storage.path), Options{
		Clock: func() time.Time { return fixture.now }, RuntimeNodeID: foreignRuntimeNodeID,
		RuntimeNodeFingerprint: foreignRuntimeNodeFingerprint,
	})
	if err != nil {
		t.Fatalf("Open(foreign check node) error = %v", err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	foreignDetail, err := foreign.CheckRunDetail(context.Background(), fixture.workspace.ID, bound.ID)
	if err != nil {
		t.Fatalf("CheckRunDetail(foreign node) error = %v", err)
	}
	if foreign.CheckBindingIsCurrent(foreignDetail.Run) {
		t.Fatalf("foreign store accepted check binding: %#v", foreignDetail.Run)
	}
	if _, err := foreign.MarkCheckRunning(context.Background(), bound.ID, "foreign-check-running"); ErrorCode(err) != CodeCheckRunConflict {
		t.Fatalf("MarkCheckRunning(foreign node) error = %v, code = %q", err, ErrorCode(err))
	}

	fixture.advance()
	running, err := fixture.storage.MarkCheckRunning(context.Background(), bound.ID, "current-check-running")
	if err != nil || !fixture.storage.CheckBindingIsCurrent(running.Run) {
		t.Fatalf("MarkCheckRunning(current node) = %#v, %v", running, err)
	}
	foreignIntegrity, err := foreign.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || !foreignIntegrity.Complete || foreignIntegrity.Status != "failed" ||
		!semanticViolationNamed(foreignIntegrity, "checks", "current_node_runtime_binding") {
		t.Fatalf("VerifyCanonical(foreign running check binding) = %#v, %v", foreignIntegrity, err)
	}
	report, err := fixture.storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || !hasQuiescenceBlocker(report.QuiescenceBlockers, "check_runtime_binding", bound.ID) {
		t.Fatalf("VerifyCanonical(check binding) = %#v, %v", report, err)
	}
	fixture.advance()
	observation := started.LaunchReceipt.Observation
	observation.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	exitCode := 0
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID: bound.ID, Outcome: domain.CheckOutcomePassed, ExitCode: &exitCode,
		TerminalObservation: observation, CorrelationID: "terminal-check-binding-clear",
	})
	if err != nil || finished.Run.Status != domain.CheckRunFinished || finished.Run.RuntimeHandle != "" || finished.Run.RuntimeNodeID != "" {
		t.Fatalf("FinishCheckRun() = %#v, %v", finished, err)
	}
	assertRuntimeBindingRowCount(t, fixture.storage, "check_runtime_bindings", "check_run_id", bound.ID, 0)
}

func runtimeBindingTestColumns(t *testing.T, storage *Store, table string) []string {
	t.Helper()
	rows, err := storage.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan pragma_table_info(%s): %v", table, err)
		}
		result = append(result, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma_table_info(%s): %v", table, err)
	}
	return result
}

func assertRuntimeBindingRowCount(t *testing.T, storage *Store, table, idColumn, id string, want int) {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM " + quoteSQLiteIdentifier(table) + " WHERE " + quoteSQLiteIdentifier(idColumn) + "=?"
	if err := storage.db.QueryRowContext(context.Background(), query, id).Scan(&count); err != nil || count != want {
		t.Fatalf("%s binding count for %s = %d, %v; want %d", table, id, count, err, want)
	}
}

func hasQuiescenceBlocker(blockers []QuiescenceBlocker, kind, id string) bool {
	for _, blocker := range blockers {
		if blocker.Kind == kind && blocker.EntityID == id {
			return true
		}
	}
	return false
}
