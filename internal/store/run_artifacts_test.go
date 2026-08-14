package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"golang.org/x/sys/unix"
)

func TestRunLogPublicationSyncsTheFullDirectoryChainBeforeTerminalCommit(t *testing.T) {
	dataDirectory := t.TempDir()
	content := []byte("durable terminal output\n")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	stages := make([]string, 0, 3)
	interrupted := errors.New("simulated crash after durable artifact publish")
	var storage *Store
	storage = openTestStore(t, dataDirectory, Options{MutationHook: func(stage string) error {
		switch stage {
		case mutationAfterImmutableArtifactNamespaceParentSync,
			mutationAfterImmutableArtifactShardParentSync,
			mutationAfterImmutableArtifactPublishSync:
			stages = append(stages, stage)
		}
		if stage != mutationAfterImmutableArtifactPublishSync {
			return nil
		}
		for _, path := range []string{
			filepath.Join(dataDirectory, runArtifactNamespace),
			filepath.Join(dataDirectory, runArtifactNamespace, hash[:2]),
			filepath.Join(dataDirectory, runArtifactNamespace, hash[:2], hash),
		} {
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("durable publication stage missing %s: %v", path, err)
			}
		}
		shardPath := filepath.Join(dataDirectory, runArtifactNamespace, hash[:2])
		entries, err := os.ReadDir(shardPath)
		if err != nil || len(entries) != 1 || entries[0].Name() != hash {
			t.Errorf("durable publication shard entries = %v, %v; want only %s", entries, err, hash)
		}
		var artifactStat unix.Stat_t
		if err := unix.Stat(filepath.Join(shardPath, hash), &artifactStat); err != nil || artifactStat.Nlink != 1 {
			t.Errorf("durable publication artifact links = %d, %v; want 1", artifactStat.Nlink, err)
		}
		var references int
		if err := storage.db.QueryRow(`SELECT COUNT(*) FROM run_log_artifacts`).Scan(&references); err != nil {
			t.Errorf("count pre-commit log references: %v", err)
		} else if references != 0 {
			t.Errorf("pre-commit log references = %d, want 0", references)
		}
		return interrupted
	}})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "artifact durability")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "artifact-durability",
		Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}},
	}, "artifact-durability")
	starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "artifact-durability-starting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "artifact-durability-started"); err != nil {
		t.Fatal(err)
	}

	_, err = storage.PrepareRunLogArchive(context.Background(), created.Run.ID, domain.RunLogs{
		RunID: created.Run.ID, State: "exited",
		Stdout: domain.CapturedLog{Text: string(content), CapturedBytes: int64(len(content))},
		Stderr: domain.CapturedLog{Text: "stderr\n", CapturedBytes: 7},
	})
	if ErrorCode(err) != CodeRunLogsUnavailable || !errors.Is(err, interrupted) {
		t.Fatalf("PrepareRunLogArchive(interrupted) error = %v, code = %q", err, ErrorCode(err))
	}
	wantStages := []string{
		mutationAfterImmutableArtifactNamespaceParentSync,
		mutationAfterImmutableArtifactShardParentSync,
		mutationAfterImmutableArtifactPublishSync,
	}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("durability stages = %v, want %v", stages, wantStages)
	}
	detail, err := storage.RunDetail(context.Background(), workspace.ID, created.Run.ID)
	if err != nil || detail.Run.Status != domain.RunActive || detail.Run.FinishedAt != "" {
		t.Fatalf("run after interrupted publication = %#v, %v; want active without terminal commit", detail.Run, err)
	}
}

func TestRunLogPublicationAnonymousBoundariesAreLeakFreeAndReplayable(t *testing.T) {
	dataDirectory := t.TempDir()
	content := []byte("rename-safe terminal output\n")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	interrupted := errors.New("simulated crash during immutable artifact publication")
	var interruption atomic.Int32
	storage := openTestStore(t, dataDirectory, Options{MutationHook: func(stage string) error {
		if stage == mutationAfterImmutableArtifactContentSync && interruption.CompareAndSwap(0, 1) {
			return interrupted
		}
		if stage == mutationAfterImmutableArtifactNamePublish && interruption.CompareAndSwap(1, 2) {
			return interrupted
		}
		return nil
	}})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "artifact rename replay")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "artifact-rename-replay",
		Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}},
	}, "artifact-rename-replay")
	starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "artifact-rename-replay-starting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "artifact-rename-replay-started"); err != nil {
		t.Fatal(err)
	}
	logs := domain.RunLogs{
		RunID: created.Run.ID, State: "exited",
		Stdout: domain.CapturedLog{Text: string(content), CapturedBytes: int64(len(content))},
		Stderr: domain.CapturedLog{Text: "stderr\n", CapturedBytes: 7},
	}
	if _, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, logs); ErrorCode(err) != CodeRunLogsUnavailable || !errors.Is(err, interrupted) {
		t.Fatalf("PrepareRunLogArchive(interrupted anonymous content) error = %v, code = %q", err, ErrorCode(err))
	}
	shardPath := filepath.Join(dataDirectory, runArtifactNamespace, hash[:2])
	entries, err := os.ReadDir(shardPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("interrupted anonymous content shard entries = %v, %v; want empty", entries, err)
	}
	if _, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, logs); ErrorCode(err) != CodeRunLogsUnavailable || !errors.Is(err, interrupted) {
		t.Fatalf("PrepareRunLogArchive(interrupted name publication) error = %v, code = %q", err, ErrorCode(err))
	}
	entries, err = os.ReadDir(shardPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != hash {
		t.Fatalf("interrupted name publication shard entries = %v, %v; want only %s", entries, err, hash)
	}
	var artifactStat unix.Stat_t
	if err := unix.Stat(filepath.Join(shardPath, hash), &artifactStat); err != nil || artifactStat.Nlink != 1 {
		t.Fatalf("interrupted name publication artifact links = %d, %v; want 1", artifactStat.Nlink, err)
	}
	archive, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, logs)
	if err != nil || archive.Stdout.ContentSHA256 != hash {
		t.Fatalf("PrepareRunLogArchive(replay) = %#v, %v", archive, err)
	}
}

func TestTerminalRunLogsAreBoundedBoundToRunAndVerified(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "terminal archive")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "terminal-archive", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "terminal-archive")
	starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "archive-starting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "archive-started"); err != nil {
		t.Fatal(err)
	}
	stdoutText := "first\n" + strings.Repeat("x", 70*1024) + "\n"
	logs := domain.RunLogs{
		RunID: created.Run.ID, State: "exited",
		Stdout: domain.CapturedLog{Text: stdoutText, CapturedBytes: int64(len(stdoutText))},
		Stderr: domain.CapturedLog{Text: "one\ntwo\n", CapturedBytes: 8},
	}
	archive, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, logs)
	if err != nil {
		t.Fatalf("PrepareRunLogArchive() error = %v", err)
	}
	if archive.RunID != created.Run.ID || archive.Stdout.CapturedBytes > maximumRunLogArtifactBytes || !archive.Stdout.Truncated || archive.Stdout.OmittedBytes == 0 {
		t.Fatalf("prepared archive = %#v", archive)
	}
	swapped := archive
	swapped.RunID = "run_ffffffffffffffffffffffffffffffff"
	if _, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review", LogArchive: &swapped,
	}, true, nil, "archive-swapped"); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("ApplyRunObservation(swapped archive) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, domain.RunLogs{
		RunID: created.Run.ID, State: "exited", Stdout: domain.CapturedLog{OmittedBytes: 1},
	}); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("PrepareRunLogArchive(malformed metadata) error = %v, code = %q", err, ErrorCode(err))
	}
	completed, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review", LogArchive: &archive,
	}, true, nil, "archive-complete")
	if err != nil || completed.Run.Status != domain.RunCompleted || completed.Run.FinishedAt == "" || completed.Run.RuntimeHandle != "" {
		t.Fatalf("ApplyRunObservation() = %#v, %v", completed, err)
	}
	retained, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 1)
	if err != nil || retained.Stderr.Text != "two\n" || retained.Stdout.CapturedBytes != archive.Stdout.CapturedBytes || !utf8.ValidString(retained.Stdout.Text) {
		t.Fatalf("RunTerminalLogs() = %#v, %v", retained, err)
	}
	path := filepath.Join(filepath.Dir(storage.path), runArtifactNamespace, archive.Stdout.ContentSHA256[:2], archive.Stdout.ContentSHA256)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("run artifact path = %#v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(storage.path), checkArtifactNamespace, archive.Stdout.ContentSHA256[:2], archive.Stdout.ContentSHA256)); !os.IsNotExist(err) {
		t.Fatalf("run artifact unexpectedly used check namespace: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "artifact-alias")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 0); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("RunTerminalLogs(hard-linked artifact) error = %v, code = %q", err, ErrorCode(err))
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 0); err != nil {
		t.Fatalf("RunTerminalLogs(after hard-link removal) error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 0); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("RunTerminalLogs(public mode) error = %v, code = %q", err, ErrorCode(err))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 0); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("RunTerminalLogs(corrupt) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestDefinitiveTerminalRunCanRecordLogsUnavailableWithoutFalsifyingOutcome(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "terminal logs unavailable")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "logs-unavailable", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "logs-unavailable")
	starting, _ := storage.MarkRunStarting(context.Background(), created.Run.ID, "logs-unavailable-starting")
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "logs-unavailable-started"); err != nil {
		t.Fatal(err)
	}
	completed, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review",
		LogUnavailableReason: "runtime ended definitively but its terminal capture failed verification",
	}, true, nil, "logs-unavailable-complete")
	if err != nil || completed.Run.Status != domain.RunCompleted || completed.Run.FinishedAt == "" {
		t.Fatalf("ApplyRunObservation(unavailable logs) = %#v, %v", completed, err)
	}
	if _, err := storage.RunTerminalLogs(context.Background(), workspace.ID, created.Run.ID, 0); ErrorCode(err) != CodeRunLogsUnavailable {
		t.Fatalf("RunTerminalLogs(unavailable) error = %v, code = %q", err, ErrorCode(err))
	}
	var eventData string
	if err := storage.db.QueryRow(`SELECT data_json FROM events WHERE entity_id=? AND type='run.completed'`, created.Run.ID).Scan(&eventData); err != nil || !strings.Contains(eventData, `"logs_available":false`) || !strings.Contains(eventData, "failed verification") {
		t.Fatalf("run.completed unavailable receipt = %q, %v", eventData, err)
	}
}

func TestTerminalRunLogOutcomeReplayRequiresTheExactCommittedReceipt(t *testing.T) {
	t.Run("captured", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "captured replay")
		created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
			Schema: execution.FakeScenarioSchema, Name: "captured-replay",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}},
		}, "captured-replay")
		starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "captured-replay-starting")
		if err != nil {
			t.Fatal(err)
		}
		active, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "captured-replay-started")
		if err != nil {
			t.Fatal(err)
		}
		stopping, err := storage.RequestRunStop(context.Background(), StopRunCommand{
			WorkspaceIdentifier: workspace.ID, RunID: active.Run.ID, ExpectedRevision: active.Run.Revision,
			GracePeriodMillis: 100, IdempotencyKey: "captured-replay-stop", CorrelationID: "captured-replay-stop",
		})
		if err != nil {
			t.Fatal(err)
		}
		archive, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, domain.RunLogs{
			RunID: created.Run.ID, State: "stopped", Stdout: domain.CapturedLog{Text: "captured\n", CapturedBytes: 9},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "stopped", &archive, "", "captured-replay-terminal"); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "stopped", &archive, "", "captured-replay-same"); err != nil {
			t.Fatalf("MarkRunStopped(exact replay) error = %v", err)
		}
		if _, err := storage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "stopped", nil, "different unavailable outcome", "captured-replay-different"); ErrorCode(err) != CodeRunConflict {
			t.Fatalf("MarkRunStopped(different log outcome) error = %v, code = %q", err, ErrorCode(err))
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "unavailable replay")
		created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
			Schema: execution.FakeScenarioSchema, Name: "unavailable-replay",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}},
		}, "unavailable-replay")
		starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "unavailable-replay-starting")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "unavailable-replay-started"); err != nil {
			t.Fatal(err)
		}
		const reason = "definitive outcome had no trustworthy capture"
		if _, err := storage.FailRun(context.Background(), created.Run.ID, "provider_ended", "provider ended", nil, reason, "unavailable-replay-terminal"); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.FailRun(context.Background(), created.Run.ID, "provider_ended", "provider ended", nil, reason, "unavailable-replay-same"); err != nil {
			t.Fatalf("FailRun(exact replay) error = %v", err)
		}
		if _, err := storage.FailRun(context.Background(), created.Run.ID, "provider_ended", "provider ended", nil, "different diagnosis", "unavailable-replay-different"); ErrorCode(err) != CodeRunConflict {
			t.Fatalf("FailRun(different log outcome) error = %v, code = %q", err, ErrorCode(err))
		}
	})
}

func TestCanonicalVerifierRejectsTerminalLogReceiptReferenceDivergence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string, *domain.RunLogArchive)
	}{
		{
			name: "available receipt missing one stream",
			mutate: func(t *testing.T, storage *Store, runID string, _ *domain.RunLogArchive) {
				t.Helper()
				var triggerSQL string
				if err := storage.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='run_log_artifact_reject_delete'`).Scan(&triggerSQL); err != nil {
					t.Fatalf("read immutable run-log trigger: %v", err)
				}
				tx, err := storage.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatalf("begin missing-stream corruption fixture: %v", err)
				}
				if _, err := tx.Exec(`DROP TRIGGER run_log_artifact_reject_delete`); err != nil {
					_ = tx.Rollback()
					t.Fatalf("drop immutable run-log trigger: %v", err)
				}
				if _, err := tx.Exec(`DELETE FROM run_log_artifacts WHERE run_id=? AND kind='stderr'`, runID); err != nil {
					_ = tx.Rollback()
					t.Fatalf("delete terminal stderr reference: %v", err)
				}
				if _, err := tx.Exec(triggerSQL); err != nil {
					_ = tx.Rollback()
					t.Fatalf("restore immutable run-log trigger: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit missing-stream corruption fixture: %v", err)
				}
			},
		},
		{
			name: "unavailable receipt has artifact references",
			mutate: func(t *testing.T, storage *Store, runID string, archive *domain.RunLogArchive) {
				t.Helper()
				tx, err := storage.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatalf("begin forged terminal references: %v", err)
				}
				if err := storage.insertRunLogArchive(context.Background(), tx, runID, storage.nowText(), archive); err != nil {
					_ = tx.Rollback()
					t.Fatalf("insert forged terminal references: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit forged terminal references: %v", err)
				}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, _, assigned := initializeRunTest(t, storage, test.name)
			created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
				Schema: execution.FakeScenarioSchema, Name: "terminal-parity",
				Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}},
			}, "terminal-parity")
			starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "terminal-parity-starting")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "terminal-parity-started"); err != nil {
				t.Fatal(err)
			}
			archive, err := storage.PrepareRunLogArchive(context.Background(), created.Run.ID, domain.RunLogs{
				RunID: created.Run.ID, State: "exited",
				Stdout: domain.CapturedLog{Text: "same", CapturedBytes: 4},
				Stderr: domain.CapturedLog{Text: "same", CapturedBytes: 4},
			})
			if err != nil {
				t.Fatal(err)
			}
			observation := domain.RunObservation{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"}
			if test.name == "available receipt missing one stream" {
				observation.LogArchive = &archive
			} else {
				observation.LogUnavailableReason = "terminal capture unavailable"
			}
			if _, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, observation, true, nil, "terminal-parity-complete"); err != nil {
				t.Fatal(err)
			}
			before, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
			if err != nil || before.Status != "ok" || !before.Complete {
				t.Fatalf("VerifyCanonical(before corruption) = %#v, %v", before, err)
			}
			test.mutate(t, storage, created.Run.ID, &archive)
			after, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
			if err != nil {
				t.Fatalf("VerifyCanonical(after corruption) error = %v", err)
			}
			if after.Status != "failed" || !after.Complete || !semanticViolationNamed(after, "run", "terminal_log_receipts") {
				t.Fatalf("VerifyCanonical(after corruption) = status %q complete %t families %#v", after.Status, after.Complete, after.SemanticFamilies)
			}
		})
	}
}

func semanticViolationNamed(report CanonicalIntegrityReport, familyName, checkName string) bool {
	for _, family := range report.SemanticFamilies {
		if family.Name != familyName {
			continue
		}
		for _, violation := range family.Violations {
			if violation.Check == checkName && violation.Count > 0 {
				return true
			}
		}
	}
	return false
}
