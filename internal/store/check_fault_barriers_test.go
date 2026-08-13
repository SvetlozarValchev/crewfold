package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

var errInjectedCheckBarrier = errors.New("injected check mutation barrier")

func TestCheckRequestNamedFaultBarriersRollbackAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterCheckRequestProjection,
		MutationAfterCheckRequestEvent,
		MutationAfterCheckRequestJob,
		MutationAfterCheckRequestIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			fixture := newGrantedCheckAuthorityFixture(t)
			command := RequestGrantedCheckRunCommand{
				SourceRunID:           fixture.sourceRun.ID,
				CheckWatchGrantID:     fixture.grant.ID,
				ExpectedGrantRevision: fixture.grant.Revision,
				RequirementID:         fixture.requirement.ID,
				IdempotencyKey:        "fault-check-request-" + stage,
				CorrelationID:         "request-fault-check-request-" + stage,
			}

			fixture.storage.mutationHook = failCheckStage(stage)
			if _, err := fixture.storage.RunGrantedCheck(context.Background(), command); !errors.Is(err, errInjectedCheckBarrier) {
				t.Fatalf("RunGrantedCheck(%s) error = %v; want injected fault", stage, err)
			}
			fixture.storage.mutationHook = nil

			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_runs WHERE source_run_id=?`, fixture.sourceRun.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_jobs WHERE check_run_id IN (SELECT id FROM check_runs WHERE source_run_id=?)`, fixture.sourceRun.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE type='check.run_requested' AND actor_id=?`, fixture.sourceRun.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, runCheckIdempotencyKey(fixture.sourceRun.ID, command.IdempotencyKey))

			created, err := fixture.storage.RunGrantedCheck(context.Background(), command)
			if err != nil || created.Value.Status != domain.CheckRunRequested {
				t.Fatalf("RunGrantedCheck(%s retry) = %#v, %v", stage, created, err)
			}
			replayed, err := fixture.storage.RunGrantedCheck(context.Background(), command)
			if err != nil || !reflect.DeepEqual(replayed, created) {
				t.Fatalf("RunGrantedCheck(%s replay) = %#v, %v; want %#v", stage, replayed, err, created)
			}
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_runs WHERE id=?`, created.Value.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_jobs WHERE check_run_id=?`, created.Value.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE type='check.run_requested' AND entity_id=?`, created.Value.ID)
		})
	}
}

func TestCheckLaunchNamedFaultBarriersRollbackAndReplay(t *testing.T) {
	for _, stage := range []string{MutationAfterCheckLaunchReceipt, MutationAfterCheckLaunchEvent} {
		t.Run(stage, func(t *testing.T) {
			fixture := newGrantedCheckAuthorityFixture(t)
			work := fixture.requestAndClaim(t)
			fixture.advance()
			command := fixture.startCommand(work)

			fixture.storage.mutationHook = failCheckStage(stage)
			if _, err := fixture.storage.MarkCheckStarting(context.Background(), command); !errors.Is(err, errInjectedCheckBarrier) {
				t.Fatalf("MarkCheckStarting(%s) error = %v; want injected fault", stage, err)
			}
			fixture.storage.mutationHook = nil

			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_runs WHERE id=? AND status='requested' AND runtime_handle IS NULL AND revision=1`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_jobs WHERE check_run_id=? AND status='leased'`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_launch_receipts WHERE check_run_id=?`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE type='check.run_starting' AND entity_id=?`, work.Run.ID)

			started, err := fixture.storage.MarkCheckStarting(context.Background(), command)
			if err != nil || started.Run.Status != domain.CheckRunStarting || started.LaunchReceipt == nil {
				t.Fatalf("MarkCheckStarting(%s retry) = %#v, %v", stage, started, err)
			}
			replayed, err := fixture.storage.MarkCheckStarting(context.Background(), command)
			if err != nil || !reflect.DeepEqual(replayed, started) {
				t.Fatalf("MarkCheckStarting(%s replay) = %#v, %v; want %#v", stage, replayed, err, started)
			}
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_launch_receipts WHERE check_run_id=?`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE type='check.run_starting' AND entity_id=?`, work.Run.ID)
		})
	}
}

func TestCheckRuntimeBindingNamedFaultBarrierRollsBackAndReplays(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	runtimeHandle := "direct:" + work.Run.ID

	fixture.storage.mutationHook = failCheckStage(MutationAfterCheckRuntimeBinding)
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, runtimeHandle, "fault-check-runtime-binding"); !errors.Is(err, errInjectedCheckBarrier) {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v; want injected fault", err)
	}
	fixture.storage.mutationHook = nil

	assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_runs WHERE id=? AND status='starting' AND runtime_handle IS NULL AND revision=2`, work.Run.ID)
	assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_jobs WHERE check_run_id=? AND status='leased'`, work.Run.ID)
	assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE type='check.run_runtime_observed' AND entity_id=?`, work.Run.ID)

	bound, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, runtimeHandle, "fault-check-runtime-binding")
	if err != nil || bound.RuntimeHandle != runtimeHandle || bound.Status != domain.CheckRunStarting {
		t.Fatalf("RecordCheckRuntimeBinding(retry) = %#v, %v", bound, err)
	}
	replayed, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, runtimeHandle, "fault-check-runtime-binding")
	if err != nil || !reflect.DeepEqual(replayed, bound) {
		t.Fatalf("RecordCheckRuntimeBinding(replay) = %#v, %v; want %#v", replayed, err, bound)
	}
	assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE type='check.run_runtime_observed' AND entity_id=?`, work.Run.ID)
}

func TestCheckTerminalNamedFaultBarriersRollbackAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterCheckResult,
		MutationAfterCheckArtifact,
		MutationAfterCheckFreshness,
		MutationAfterCheckEvidence,
		MutationAfterCheckNotification,
		MutationAfterCheckMessage,
		MutationAfterCheckResultEvent,
	} {
		t.Run(stage, func(t *testing.T) {
			fixture := newGrantedCheckAuthorityFixture(t)
			work := fixture.requestAndClaim(t)
			fixture.advance()
			started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
			if err != nil || started.LaunchReceipt == nil {
				t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
			}
			fixture.advance()
			if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "fault-terminal-runtime-binding"); err != nil {
				t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
			}
			fixture.advance()
			if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "fault-terminal-running"); err != nil {
				t.Fatalf("MarkCheckRunning() error = %v", err)
			}
			prepared, err := fixture.storage.PrepareCheckArtifact(context.Background(), domain.CheckArtifactStdout, []byte("bounded terminal output\n"), 0)
			if err != nil {
				t.Fatalf("PrepareCheckArtifact() error = %v", err)
			}
			fixture.advance()
			exitCode := 7
			terminal := started.LaunchReceipt.Observation
			terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
			command := FinishCheckRunCommand{
				CheckRunID: work.Run.ID, Outcome: domain.CheckOutcomeFailed, ExitCode: &exitCode,
				TerminalObservation: terminal, Artifacts: []PreparedCheckArtifact{prepared},
				CorrelationID: "fault-terminal-" + stage,
			}

			fixture.storage.mutationHook = failCheckStage(stage)
			if _, err := fixture.storage.FinishCheckRun(context.Background(), command); !errors.Is(err, errInjectedCheckBarrier) {
				t.Fatalf("FinishCheckRun(%s) error = %v; want injected fault", stage, err)
			}
			fixture.storage.mutationHook = nil

			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_runs WHERE id=? AND status='running' AND revision=4`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_jobs WHERE check_run_id=? AND status='leased'`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_results WHERE check_run_id=?`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_artifacts WHERE check_result_id IN (SELECT id FROM check_results WHERE check_run_id=?)`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_result_freshness WHERE check_result_id IN (SELECT id FROM check_results WHERE check_run_id=?)`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_requirement_evidence WHERE check_result_id IN (SELECT id FROM check_results WHERE check_run_id=?)`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_notification_receipts WHERE check_result_id IN (SELECT id FROM check_results WHERE check_run_id=?)`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM messages WHERE sender_type='subsystem' AND sender_id='crewfold-check-worker'`)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE type IN ('check.run_finished','check.result_recorded') AND (entity_id=? OR json_extract(data_json,'$.check_run_id')=?)`, work.Run.ID, work.Run.ID)

			finished, err := fixture.storage.FinishCheckRun(context.Background(), command)
			if err != nil || finished.Result == nil || finished.Result.Outcome != domain.CheckOutcomeFailed || finished.CurrentFreshness == nil || finished.RequirementState != domain.CheckRequirementFailed || len(finished.Artifacts) != 1 || len(finished.Evidence.MechanicalCheck) != 1 {
				t.Fatalf("FinishCheckRun(%s retry) = %#v, %v", stage, finished, err)
			}
			replayed, err := fixture.storage.FinishCheckRun(context.Background(), command)
			if err != nil || !reflect.DeepEqual(replayed, finished) {
				t.Fatalf("FinishCheckRun(%s replay) = %#v, %v; want %#v", stage, replayed, err, finished)
			}
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_results WHERE check_run_id=?`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_artifacts WHERE check_result_id=?`, finished.Result.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_result_freshness WHERE check_result_id=?`, finished.Result.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_requirement_evidence WHERE check_result_id=?`, finished.Result.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_notification_receipts WHERE check_result_id=? AND freshness_revision=1 AND duty='task_owner'`, finished.Result.ID)
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM messages WHERE id IN (SELECT message_id FROM check_notification_receipts WHERE check_result_id=?) AND sender_type='subsystem' AND sender_id='crewfold-check-worker'`, finished.Result.ID)
			assertCheckRowCount(t, fixture.storage, 2, `SELECT COUNT(*) FROM events WHERE type IN ('check.run_finished','check.result_recorded') AND (entity_id IN (?,?) OR json_extract(data_json,'$.check_run_id')=?)`, work.Run.ID, finished.Result.ID, work.Run.ID)
		})
	}
}

func failCheckStage(wanted string) func(string) error {
	return func(observed string) error {
		if observed == wanted {
			return errInjectedCheckBarrier
		}
		return nil
	}
}

func assertCheckRowCount(t *testing.T, storage *Store, want int, query string, arguments ...any) {
	t.Helper()
	var got int
	if err := storage.db.QueryRowContext(context.Background(), query, arguments...).Scan(&got); err != nil {
		t.Fatalf("count check rows: %v", err)
	}
	if got != want {
		t.Fatalf("check row count = %d, want %d; query = %s", got, want, query)
	}
}
