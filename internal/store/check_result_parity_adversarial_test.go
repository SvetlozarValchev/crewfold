package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func TestFinishCheckRunRejectsNonCanonicalResultDiagnosticsAtomically(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		code       string
		diagnostic string
	}{
		{name: "invalid UTF-8 code", code: string([]byte{0xff}), diagnostic: "start failed"},
		{name: "NUL code", code: "start\x00failed", diagnostic: "start failed"},
		{name: "oversized code", code: strings.Repeat("x", 129), diagnostic: "start failed"},
		{name: "invalid UTF-8 diagnostic", code: "runtime_start_failed", diagnostic: string([]byte{0xff})},
		{name: "NUL diagnostic", code: "runtime_start_failed", diagnostic: "start\x00failed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newGrantedCheckAuthorityFixture(t)
			work := fixture.requestAndClaim(t)
			fixture.advance()
			started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
			if err != nil || started.LaunchReceipt == nil {
				t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
			}
			fixture.advance()
			terminal := started.LaunchReceipt.Observation
			terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
			if _, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
				CheckRunID:          work.Run.ID,
				Outcome:             domain.CheckOutcomeStartFailed,
				DiagnosticCode:      testCase.code,
				Diagnostic:          testCase.diagnostic,
				TerminalObservation: terminal,
				CorrelationID:       "finish-malformed-diagnostic",
			}); ErrorCode(err) != CodeInvalidRun {
				t.Fatalf("FinishCheckRun(noncanonical diagnostic) error = %v, code = %q; want %q", err, ErrorCode(err), CodeInvalidRun)
			}
			assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_runs WHERE id=? AND status='starting'`, work.Run.ID)
			assertCheckRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM check_results WHERE check_run_id=?`, work.Run.ID)
		})
	}
}

func TestCheckLaunchReceiptReadRejectsMalformedObservation(t *testing.T) {
	validBranch := "main"
	validHead := strings.Repeat("a", 40)
	base := dbgen.CheckLaunchReceipt{
		ID:                        "checklaunch_" + strings.Repeat("a", 32),
		CheckRunID:                "checkrun_" + strings.Repeat("b", 32),
		CheckJobID:                "checkjob_" + strings.Repeat("c", 32),
		OperationID:               "checkrun_" + strings.Repeat("b", 32),
		EffectiveSpecSha256:       strings.Repeat("d", 64),
		EffectiveWorkingDirectory: "/tmp/check",
		Launchable:                1,
		DefinitionSha256:          strings.Repeat("e", 64),
		SourceType:                domain.CheckRunSourceOwner,
		SourceActorID:             localOwnerActorID,
		ObservationAvailable:      1,
		RepositoryID:              "repo_immutable",
		ObjectFormat:              "sha1",
		CheckoutID:                "checkout_immutable",
		Branch:                    &validBranch,
		HeadCommit:                &validHead,
		DirtyPathsJson:            "[]",
		ObservedAt:                "2026-08-13T12:00:00Z",
		CreatedAt:                 "2026-08-13T12:00:01Z",
		CreatedBy:                 "crewfold-check-worker",
	}
	for _, testCase := range []struct {
		name   string
		branch string
	}{
		{name: "invalid UTF-8", branch: string([]byte{0xff})},
		{name: "NUL", branch: "ma\x00in"},
		{name: "oversized", branch: strings.Repeat("x", 1025)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			row := base
			row.Branch = &testCase.branch
			if receipt, err := checkLaunchReceiptFromRow(row); err == nil {
				t.Fatalf("checkLaunchReceiptFromRow(malformed branch) = %#v, nil; want fail-closed read rejection", receipt)
			}
		})
	}
}
