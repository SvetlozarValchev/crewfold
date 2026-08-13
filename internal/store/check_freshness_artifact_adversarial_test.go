package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestInitialCheckFreshnessDirtyDifferentHeadIsUnknown(t *testing.T) {
	launch := domain.CheckGitObservation{
		Available:    true,
		RepositoryID: "repository",
		ObjectFormat: "sha1",
		CheckoutID:   "checkout",
		HeadCommit:   strings.Repeat("a", 40),
		Dirty:        true,
		DirtyPaths:   []string{"dirty.txt"},
		ObservedAt:   "2026-08-13T12:00:00Z",
	}
	terminal := domain.CheckGitObservation{
		Available:    true,
		RepositoryID: launch.RepositoryID,
		ObjectFormat: launch.ObjectFormat,
		CheckoutID:   launch.CheckoutID,
		HeadCommit:   strings.Repeat("b", 40),
		DirtyPaths:   []string{},
		ObservedAt:   "2026-08-13T12:00:01Z",
	}

	status, _, eligible := initialCheckFreshness(launch, terminal)
	if status != domain.CheckFreshnessUnknown || eligible {
		t.Fatalf("initialCheckFreshness(dirty, different HEAD) = %q, eligible %t; want unknown and ineligible", status, eligible)
	}
}

func TestCheckFreshnessCannotPromoteInitiallyIneligibleResult(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	start := fixture.startCommand(work)
	start.Observation.Dirty = true
	start.Observation.DirtyPaths = []string{"dirty.txt"}

	started, err := fixture.storage.MarkCheckStarting(context.Background(), start)
	if err != nil {
		t.Fatalf("MarkCheckStarting() error = %v", err)
	}
	if started.LaunchReceipt == nil || !started.LaunchReceipt.Launchable {
		t.Fatalf("MarkCheckStarting() = %#v, want launchable receipt", started)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "record-freshness-runtime"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "mark-freshness-running"); err != nil {
		t.Fatalf("MarkCheckRunning() error = %v", err)
	}
	fixture.advance()
	exitCode := 0
	terminal := start.Observation
	terminal.Dirty = false
	terminal.DirtyPaths = []string{}
	terminal.HeadCommit = differentCheckCommit(terminal.HeadCommit)
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID:          work.Run.ID,
		Outcome:             domain.CheckOutcomePassed,
		ExitCode:            &exitCode,
		TerminalObservation: terminal,
		CorrelationID:       "finish-ineligible-freshness-check",
	})
	if err != nil {
		t.Fatalf("FinishCheckRun() error = %v", err)
	}
	if finished.Result == nil || finished.CurrentFreshness == nil || finished.CurrentFreshness.Status != domain.CheckFreshnessUnknown || finished.CurrentFreshness.InitiallyEligible {
		t.Fatalf("FinishCheckRun() freshness = %#v, want initially ineligible unknown", finished.CurrentFreshness)
	}

	fixture.advance()
	err = fixture.storage.withCheckMutationSeal(func() error {
		_, err := fixture.storage.db.ExecContext(context.Background(), `
			INSERT INTO check_result_freshness(
				id,check_result_id,revision,status,reason,initially_eligible,ever_stale,
				observation_available,repository_id,object_format,checkout_id,branch,
				head_commit,dirty,dirty_paths_json,observed_at,diagnostic_code,diagnostic,
				created_at,created_by
			)
			SELECT ?,check_result_id,revision+1,'fresh','later clean observation',initially_eligible,ever_stale,
				observation_available,repository_id,object_format,checkout_id,branch,
				head_commit,dirty,dirty_paths_json,?,diagnostic_code,diagnostic,
				?,'crewfold-check-worker'
			FROM check_result_freshness
			WHERE check_result_id=? AND revision=1`,
			"checkfresh_"+strings.Repeat("f", 32), fixture.now.Format(time.RFC3339Nano), fixture.now.Format(time.RFC3339Nano), finished.Result.ID)
		return err
	})
	if err == nil {
		t.Fatal("sealed SQL promoted an initially ineligible result to fresh; want append-only freshness trigger rejection")
	}
	if !strings.Contains(err.Error(), "invalid append-only check freshness") {
		t.Fatalf("sealed SQL promotion error = %v, want append-only freshness trigger rejection", err)
	}
}

func TestStaleCheckFreshnessCannotRemainCurrentSupportingEvidence(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "record-stale-evidence-runtime"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "mark-stale-evidence-running"); err != nil {
		t.Fatalf("MarkCheckRunning() error = %v", err)
	}
	fixture.advance()
	exitCode := 0
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID:          work.Run.ID,
		Outcome:             domain.CheckOutcomePassed,
		ExitCode:            &exitCode,
		TerminalObservation: terminal,
		CorrelationID:       "finish-stale-evidence-check",
	})
	if err != nil || finished.Result == nil || finished.CurrentFreshness == nil || finished.CurrentFreshness.Status != domain.CheckFreshnessFresh {
		t.Fatalf("FinishCheckRun() = %#v, %v; want initially fresh pass", finished, err)
	}

	fixture.advance()
	staleHead := differentCheckCommit(terminal.HeadCommit)
	err = fixture.storage.withCheckMutationSeal(func() error {
		_, err := fixture.storage.db.ExecContext(context.Background(), `
			INSERT INTO check_result_freshness(
				id,check_result_id,revision,status,reason,initially_eligible,ever_stale,
				observation_available,repository_id,object_format,checkout_id,branch,
				head_commit,dirty,dirty_paths_json,observed_at,diagnostic_code,diagnostic,
				created_at,created_by
			)
			SELECT ?,check_result_id,revision+1,'stale','HEAD changed after the check',initially_eligible,1,
				1,repository_id,object_format,checkout_id,branch,
				?,0,'[]',?,NULL,NULL,?,'crewfold-check-worker'
			FROM check_result_freshness
			WHERE check_result_id=? AND revision=1`,
			"checkfresh_"+strings.Repeat("e", 32), staleHead, fixture.now.Format(time.RFC3339Nano), fixture.now.Format(time.RFC3339Nano), finished.Result.ID)
		if err != nil {
			return err
		}
		_, err = fixture.storage.db.ExecContext(context.Background(), `
			INSERT INTO check_requirement_evidence(
				id,requirement_id,requirement_revision,check_result_id,freshness_revision,
				class,effect,created_at,created_by
			) VALUES(?,?,?,?,2,'mechanical_check','inconclusive',?,'crewfold-check-worker')`,
			"checkevidence_"+strings.Repeat("d", 32), fixture.requirement.ID, fixture.requirement.Revision,
			finished.Result.ID, fixture.now.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		t.Fatalf("append stale freshness observation: %v", err)
	}

	detail, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, work.Run.ID)
	if err != nil {
		t.Fatalf("CheckRunDetail(stale) error = %v", err)
	}
	if detail.CurrentFreshness == nil || detail.CurrentFreshness.Revision != 2 || detail.CurrentFreshness.Status != domain.CheckFreshnessStale || detail.RequirementState != domain.CheckRequirementStale {
		t.Fatalf("CheckRunDetail(stale) freshness/state = %#v/%q, want revision 2 stale/stale", detail.CurrentFreshness, detail.RequirementState)
	}
	historicalSupport := false
	currentInconclusive := false
	for _, evidence := range detail.Evidence.MechanicalCheck {
		if evidence.Class != domain.EvidenceMechanicalCheck {
			t.Fatalf("mechanical evidence class = %q, want exact mechanical_check", evidence.Class)
		}
		if evidence.Effect == domain.CheckEvidenceSupports {
			historicalSupport = true
			if evidence.FreshnessRevision == detail.CurrentFreshness.Revision {
				t.Fatalf("stale current freshness revision %d remains effective supporting evidence: %#v", detail.CurrentFreshness.Revision, evidence)
			}
		}
		if evidence.FreshnessRevision == detail.CurrentFreshness.Revision && evidence.Effect == domain.CheckEvidenceInconclusive {
			currentInconclusive = true
		}
	}
	if !historicalSupport {
		t.Fatal("stale projection erased the explicitly revision-bound historical support")
	}
	if !currentInconclusive {
		t.Fatal("stale projection omitted current revision-bound inconclusive mechanical evidence")
	}
}

func TestPrepareCheckArtifactRejectsGlobalOversize(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	content := bytes.Repeat([]byte("x"), (1<<20)+1)
	if _, err := storage.PrepareCheckArtifact(context.Background(), domain.CheckArtifactStdout, content, 0); err == nil {
		t.Fatal("PrepareCheckArtifact(1 MiB + 1) succeeded, want bounded rejection")
	}
}

func TestPrepareCheckArtifactRejectsSymlinkedPrivateDirectoryComponents(t *testing.T) {
	for _, testCase := range []struct {
		name string
		seed func(*testing.T, *Store, string, string)
	}{
		{
			name: "artifact root",
			seed: func(t *testing.T, storage *Store, outside, _ string) {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(filepath.Dir(storage.path), "check-artifacts")); err != nil {
					t.Fatalf("symlink artifact root: %v", err)
				}
			},
		},
		{
			name: "hash shard",
			seed: func(t *testing.T, storage *Store, outside, shard string) {
				t.Helper()
				root := filepath.Join(filepath.Dir(storage.path), "check-artifacts")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatalf("mkdir artifact root: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(root, shard)); err != nil {
					t.Fatalf("symlink artifact shard: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			outside := t.TempDir()
			content := []byte("private bounded output")
			digest := sha256.Sum256(content)
			hash := hex.EncodeToString(digest[:])
			testCase.seed(t, storage, outside, hash[:2])
			if _, err := storage.PrepareCheckArtifact(context.Background(), domain.CheckArtifactStdout, content, 0); err == nil {
				t.Fatal("PrepareCheckArtifact() followed a symlinked private-directory component")
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatalf("read outside directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("outside directory contains %d entries after rejected artifact write", len(entries))
			}
		})
	}
}

func TestFinishCheckRunRejectsArtifactBeyondDefinitionCap(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil {
		t.Fatalf("MarkCheckStarting() error = %v", err)
	}
	if started.LaunchReceipt == nil || !started.LaunchReceipt.Launchable {
		t.Fatalf("MarkCheckStarting() = %#v, want launchable receipt", started)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "record-artifact-runtime"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "mark-artifact-running"); err != nil {
		t.Fatalf("MarkCheckRunning() error = %v", err)
	}

	prepared, err := fixture.storage.PrepareCheckArtifact(context.Background(), domain.CheckArtifactStdout, bytes.Repeat([]byte("x"), 1025), 0)
	if err != nil {
		t.Fatalf("PrepareCheckArtifact(1025 globally valid bytes) error = %v", err)
	}
	fixture.advance()
	exitCode := 0
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	if _, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID:          work.Run.ID,
		Outcome:             domain.CheckOutcomePassed,
		ExitCode:            &exitCode,
		TerminalObservation: terminal,
		Artifacts:           []PreparedCheckArtifact{prepared},
		CorrelationID:       "finish-over-definition-artifact-cap",
	}); err == nil {
		t.Fatal("FinishCheckRun(1025-byte artifact, 1024-byte frozen cap) succeeded, want atomic rejection")
	}

	var results, artifacts int
	if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM check_results WHERE check_run_id=?`, work.Run.ID).Scan(&results); err != nil {
		t.Fatalf("count check results: %v", err)
	}
	if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM check_artifacts WHERE check_result_id IN (SELECT id FROM check_results WHERE check_run_id=?)`, work.Run.ID).Scan(&artifacts); err != nil {
		t.Fatalf("count check artifacts: %v", err)
	}
	if results != 0 || artifacts != 0 {
		t.Fatalf("rejected terminal bundle persisted %d results and %d artifacts, want none", results, artifacts)
	}
}

func differentCheckCommit(commit string) string {
	if commit == "" {
		return strings.Repeat("b", 40)
	}
	replacement := byte('a')
	if commit[len(commit)-1] == replacement {
		replacement = 'b'
	}
	return commit[:len(commit)-1] + string(replacement)
}
