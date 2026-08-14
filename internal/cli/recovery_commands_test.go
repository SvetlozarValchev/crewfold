package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
	"crewfold/protocol"
)

func TestM20FullDoctorCLIUsesExactOnlineReportAndStatus(t *testing.T) {
	t.Parallel()
	checks := make([]localapi.FullDoctorCheck, len(localapi.FullDoctorCheckOrder()))
	for index, code := range localapi.FullDoctorCheckOrder() {
		checks[index] = localapi.FullDoctorCheck{
			Code: code, Status: "ok", CheckedCount: 1, Summary: "passed",
			Samples:     []localapi.FullDoctorSample{},
			Remediation: localapi.FullDoctorRemediation{Kind: "none", Command: []string{}},
		}
	}
	client := &fakeDaemonClient{fullDoctor: localapi.FullDoctorResult{
		Schema: localapi.FullDoctorSchema, Type: "full_doctor", Status: "ok", EventSequence: 41,
		Checks: checks,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/crewfold.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	if exit := app.Run([]string{"doctor", "--full", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), localapi.FullDoctorSchema) {
		t.Fatalf("Run(doctor --full) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}

	client.fullDoctor.Status = "degraded"
	client.fullDoctor.Checks[0].Status = "warning"
	client.fullDoctor.Checks[0].IssueCount = 1
	app, _, stderr = newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"doctor", "--full", "--socket", "/tmp/crewfold.sock"}); exit != ExitFailure || stderr.Len() != 0 {
		t.Fatalf("Run(degraded doctor --full) exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestM20BackupActivateRequiresAndForwardsExplicitSourceRetirement(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "restored")
	activated := recovery.ActivatedRestore{
		Path: target, BackupID: "backup_" + strings.Repeat("a", 32), ManifestSHA256: strings.Repeat("b", 64),
		EventHighWater: 81, LogicalSHA256: strings.Repeat("c", 64), NodeFingerprint: strings.Repeat("d", 64),
		ActivationSHA256: strings.Repeat("e", 64), ActivatedAt: "2026-08-14T12:00:00Z", SourceRetired: true,
	}
	app, stdout, stderr := newTestApp()
	app.activateBackup = func(_ context.Context, path string, confirmed bool) (recovery.ActivatedRestore, error) {
		if path != target || !confirmed {
			t.Fatalf("Activate(%q,%t), want (%q,true)", path, confirmed, target)
		}
		return activated, nil
	}
	if exit := app.Run([]string{"backup", "activate", target, "--confirm-source-retired", "--output", "json"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(backup activate) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if err := protocol.ValidateJSON("cli/v1/backup-activate.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("backup activate output schema: %v\n%s", err, stdout.String())
	}

	for name, args := range map[string][]string{
		"missing confirmation":   {"backup", "activate", target},
		"duplicate confirmation": {"backup", "activate", target, "--confirm-source-retired", "--confirm-source-retired"},
		"unknown flag":           {"backup", "activate", target, "--force"},
		"missing target":         {"backup", "activate", "--confirm-source-retired"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, stderr := newTestApp()
			if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("Run(%s) exit=%d stderr=%q", name, exit, stderr.String())
			}
		})
	}
}

func TestM20RepairInspectIsOfflineBoundedAndReportsGuidance(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "damaged-data")
	inspection := recovery.RepairInspection{
		Path: target, Status: "degraded",
		Copied: recovery.RepairCopiedFiles{DatabaseBytes: 4096, WALPresent: false, SHMPresent: false},
		Integrity: store.CanonicalIntegrityReport{
			Status: "ok", Complete: true,
			Baseline:       store.BaselineIdentity{SchemaVersion: 1, SourceSHA256: strings.Repeat("a", 64), CatalogSHA256: strings.Repeat("b", 64)},
			EventHighWater: 55, LogicalSHA256: strings.Repeat("c", 64),
		},
		Artifacts: recovery.ArtifactFilesystemReport{Status: "warning", Complete: true},
		Findings:  []recovery.RepairFinding{{Code: "derived_knowledge_index", Status: "warning", Summary: "derived index differs", Remediation: "rebuild_derived_index"}},
	}
	app, stdout, stderr := newTestApp()
	app.inspectRepair = func(_ context.Context, path string) (recovery.RepairInspection, error) {
		if path != target {
			t.Fatalf("InspectOffline path = %q, want %q", path, target)
		}
		return inspection, nil
	}
	if exit := app.Run([]string{"repair", "inspect", target, "--output", "json"}); exit != ExitFailure || stderr.Len() != 0 {
		t.Fatalf("Run(repair inspect) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if err := protocol.ValidateJSON("cli/v1/repair-inspect.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("repair inspect output schema: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status":"guidance_required"`) || !strings.Contains(stdout.String(), "rebuild_derived_index") {
		t.Fatalf("repair inspect output = %q", stdout.String())
	}
}

func TestM20RepairInspectAlwaysEmitsUninspectableMachineReport(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "busy-data")
	app, stdout, stderr := newTestApp()
	app.inspectRepair = func(context.Context, string) (recovery.RepairInspection, error) {
		return recovery.RepairInspection{}, &recovery.Error{
			Code:    recovery.CodeRepairSourceInUse,
			Message: strings.Repeat("data directory is live\n\x00\x1b\u009b\u202e\u2066", 512),
		}
	}
	if exit := app.Run([]string{"repair", "inspect", target, "--output", "json"}); exit != ExitFailure || stderr.Len() == 0 {
		t.Fatalf("Run(repair inspect busy) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if err := protocol.ValidateJSON("cli/v1/repair-inspect.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("uninspectable output schema: %v\n%s", err, stdout.String())
	}
	assertBoundedRecoveryFailureMessages(t, stdout.Bytes(), stderr.Bytes())
	if !strings.Contains(stdout.String(), `"status":"uninspectable"`) || !strings.Contains(stderr.String(), recovery.CodeRepairSourceInUse) {
		t.Fatalf("repair inspect busy stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestM20RepairInspectPublishesBoundedUnreadableDatabaseGuidanceWithoutInventingBaseline(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "unreadable-data")
	app, stdout, stderr := newTestApp()
	app.inspectRepair = func(context.Context, string) (recovery.RepairInspection, error) {
		return recovery.RepairInspection{
			Path: target, Status: "failed",
			Artifacts: recovery.ArtifactFilesystemReport{Status: "failed", Issues: []recovery.ArtifactFilesystemIssue{}, Warnings: []recovery.ArtifactFilesystemIssue{}},
			Findings:  []recovery.RepairFinding{{Code: "database_unreadable", Status: "failed", Summary: "private recovery copy could not be opened", Remediation: "restore_verified_backup"}},
		}, nil
	}
	if exit := app.Run([]string{"repair", "inspect", target, "--output", "json"}); exit != ExitFailure || stderr.Len() != 0 {
		t.Fatalf("Run(repair inspect unreadable) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if err := protocol.ValidateJSON("cli/v1/repair-inspect.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("unreadable guidance output schema: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), `"baseline"`) || strings.Contains(stdout.String(), `"logical_sha256"`) {
		t.Fatalf("unreadable guidance invented canonical observations: %s", stdout.String())
	}
}

func TestM20RepairFindingPresentationIsBoundedAndTerminalSafe(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "damaged-data")
	unsafeSummary := strings.Repeat("diagnosis\n\x1b\u009b\u202e\u2066", 512)
	inspection := recovery.RepairInspection{
		Path: target, Status: "degraded",
		Artifacts: recovery.ArtifactFilesystemReport{Status: "warning"},
		Findings: []recovery.RepairFinding{{
			Code: "derived_knowledge_index", Status: "warning", Summary: unsafeSummary, Remediation: "rebuild_derived_index",
		}},
	}

	t.Run("machine", func(t *testing.T) {
		app, stdout, stderr := newTestApp()
		app.inspectRepair = func(context.Context, string) (recovery.RepairInspection, error) { return inspection, nil }
		if exit := app.Run([]string{"repair", "inspect", target, "--output", "json"}); exit != ExitFailure || stderr.Len() != 0 {
			t.Fatalf("Run(machine repair finding) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
		if err := protocol.ValidateJSON("cli/v1/repair-inspect.response.schema.json", stdout.Bytes()); err != nil {
			t.Fatalf("terminal-safe repair finding schema: %v\n%s", err, stdout.String())
		}
		var result repairInspectResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || len(result.Findings) != 1 {
			t.Fatalf("decode terminal-safe repair finding: %#v, %v", result, err)
		}
		if len(result.Findings[0].Summary) > repairFindingMaxBytes {
			t.Fatalf("repair finding summary length = %d", len(result.Findings[0].Summary))
		}
		assertRecoveryTerminalSafe(t, "machine repair finding", result.Findings[0].Summary)
	})

	t.Run("text", func(t *testing.T) {
		app, stdout, stderr := newTestApp()
		app.inspectRepair = func(context.Context, string) (recovery.RepairInspection, error) { return inspection, nil }
		if exit := app.Run([]string{"repair", "inspect", target}); exit != ExitFailure || stderr.Len() != 0 {
			t.Fatalf("Run(text repair finding) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
		if strings.Count(stdout.String(), "\n") != 3 {
			t.Fatalf("text repair finding injected a structural line: %q", stdout.String())
		}
		for _, character := range stdout.String() {
			if character != '\n' && recoveryTerminalUnsafeRune(character) {
				t.Fatalf("text repair finding retained unsafe terminal rune U+%04X: %q", character, stdout.String())
			}
		}
	})
}

func TestM20BackupVerifyCLIIsOfflineAndReportsExactVerifiedBundle(t *testing.T) {
	t.Parallel()
	bundle := filepath.Join(t.TempDir(), "bundle")
	digest := strings.Repeat("a", 64)
	verified := recovery.VerifiedBundle{
		Root: bundle, ManifestSHA256: strings.Repeat("b", 64),
		Manifest: recovery.Manifest{
			BackupID: "backup_" + strings.Repeat("c", 32), CreatedAt: "2026-08-14T12:00:00Z",
			BaselineSHA256: digest, LogicalSHA256: strings.Repeat("d", 64), EventHighWater: 91,
			Database: recovery.DatabaseEntry{SHA256: strings.Repeat("e", 64), Size: 4096}, TotalBytes: 5120,
			Entries: []recovery.ArtifactEntry{{Path: "run-artifacts/ff/" + strings.Repeat("f", 64)}},
		},
	}
	app, stdout, stderr := newTestApp()
	called := 0
	app.verifyBackup = func(_ context.Context, path string) (recovery.VerifiedBundle, error) {
		called++
		if path != bundle {
			t.Fatalf("VerifyBundle path = %q, want %q", path, bundle)
		}
		return verified, nil
	}
	if exit := app.Run([]string{"backup", "verify", bundle, "--output", "json"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(backup verify) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if called != 1 || !strings.Contains(stdout.String(), backupVerifySchema) || !strings.Contains(stdout.String(), verified.Manifest.BackupID) {
		t.Fatalf("backup verify called=%d output=%q", called, stdout.String())
	}
}

func TestM20BackupVerifyCLIAlwaysEmitsBoundedFailedMachineReport(t *testing.T) {
	t.Parallel()
	bundle := filepath.Join(t.TempDir(), "damaged")
	app, stdout, stderr := newTestApp()
	app.verifyBackup = func(context.Context, string) (recovery.VerifiedBundle, error) {
		return recovery.VerifiedBundle{}, &recovery.Error{
			Code:    recovery.CodeBackupIntegrityFailed,
			Message: strings.Repeat("manifest digest differs\n\x00\x1b\u009b\u202e\u2066", 512),
		}
	}
	if exit := app.Run([]string{"backup", "verify", bundle, "--output", "json"}); exit != ExitFailure {
		t.Fatalf("Run(backup verify damaged) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if err := protocol.ValidateJSON("cli/v1/backup-verify.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("failed backup verify output schema: %v\n%s", err, stdout.String())
	}
	assertBoundedRecoveryFailureMessages(t, stdout.Bytes(), stderr.Bytes())
	if !strings.Contains(stdout.String(), backupVerifySchema) || !strings.Contains(stdout.String(), `"status":"failed"`) ||
		!strings.Contains(stdout.String(), `"status":"not_run"`) || !strings.Contains(stderr.String(), recovery.CodeBackupIntegrityFailed) {
		t.Fatalf("Run(backup verify damaged) stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestM20BackupVerifyCLIReportsValidManifestClosureFailuresTruthfully(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{name: "extra file", mutate: func(t *testing.T, _ string, bundle string) {
			if err := os.WriteFile(filepath.Join(bundle, "undeclared"), []byte("extra\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing payload", mutate: func(t *testing.T, _ string, bundle string) {
			if err := os.Remove(filepath.Join(bundle, "crewfold.db")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe mode", mutate: func(t *testing.T, _ string, bundle string) {
			if err := os.Chmod(filepath.Join(bundle, "crewfold.db"), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link alias", mutate: func(t *testing.T, root string, bundle string) {
			if err := os.Link(filepath.Join(bundle, "crewfold.db"), filepath.Join(root, "outside-alias")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "content mismatch", mutate: func(t *testing.T, _ string, bundle string) {
			file, err := os.OpenFile(filepath.Join(bundle, "crewfold.db"), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte{0xff}, 0); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, bundle := createBackupVerifyClosureFixture(t, strings.ReplaceAll(test.name, " ", "-"))
			test.mutate(t, root, bundle)

			app, stdout, stderr := newTestApp()
			if exit := app.Run([]string{"backup", "verify", bundle, "--output", "json"}); exit != ExitFailure {
				t.Fatalf("Run(JSON closure failure) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if err := protocol.ValidateJSON("cli/v1/backup-verify.response.schema.json", stdout.Bytes()); err != nil {
				t.Fatalf("closure failure response schema: %v\n%s", err, stdout.String())
			}
			if err := protocol.ValidateJSON("cli/v1/error.response.schema.json", stderr.Bytes()); err != nil {
				t.Fatalf("closure failure error schema: %v\n%s", err, stderr.String())
			}
			var result backupVerifyResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantStatuses := []string{"ok", "failed", "not_run", "not_run"}
			for index, want := range wantStatuses {
				if result.Checks[index].Status != want {
					t.Fatalf("%s check statuses = %#v, want %v", test.name, result.Checks, wantStatuses)
				}
			}

			app, stdout, stderr = newTestApp()
			if exit := app.Run([]string{"backup", "verify", bundle}); exit != ExitFailure {
				t.Fatalf("Run(text closure failure) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "check_manifest: ok") || !strings.Contains(stdout.String(), "check_file_closure: failed") ||
				!strings.Contains(stdout.String(), "check_canonical_integrity: not_run") || !strings.Contains(stdout.String(), "check_quiescence: not_run") {
				t.Fatalf("text closure phase truth = %q", stdout.String())
			}
		})
	}
}

func createBackupVerifyClosureFixture(t *testing.T, key string) (string, string) {
	t.Helper()
	root := t.TempDir()
	dataDirectory := filepath.Join(root, "source")
	if err := os.Mkdir(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(context.Background(), dataDirectory, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "bundle")
	if _, err := recovery.CreateBundle(context.Background(), storage, dataDirectory, bundle, "cli-closure-"+key); err != nil {
		_ = storage.Close()
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	return root, bundle
}

func TestM20RecoveryCLIRejectsOverlongPathsBeforeOfflineInspection(t *testing.T) {
	t.Parallel()
	overlong := "/" + strings.Repeat("a", recoveryPathMaxBytes)
	tests := map[string][]string{
		"verify": {"backup", "verify", overlong, "--output", "json"},
		"repair": {"repair", "inspect", overlong, "--output", "json"},
	}
	for name, args := range tests {
		name, args := name, args
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, stdout, stderr := newTestApp()
			app.verifyBackup = func(context.Context, string) (recovery.VerifiedBundle, error) {
				t.Fatal("overlong verify path reached recovery")
				return recovery.VerifiedBundle{}, nil
			}
			app.inspectRepair = func(context.Context, string) (recovery.RepairInspection, error) {
				t.Fatal("overlong repair path reached recovery")
				return recovery.RepairInspection{}, nil
			}
			if exit := app.Run(args); exit != ExitUsage || stdout.Len() != 0 {
				t.Fatalf("Run(%s overlong) exit=%d stdout=%q stderr=%q", name, exit, stdout.String(), stderr.String())
			}
			if err := protocol.ValidateJSON("cli/v1/error.response.schema.json", stderr.Bytes()); err != nil {
				t.Fatalf("overlong %s error schema: %v\n%s", name, err, stderr.String())
			}
		})
	}
}

func TestM20RecoveryCLIRejectsTerminalUnsafePathsBeforeOfflineInspection(t *testing.T) {
	t.Parallel()
	for name, character := range map[string]string{
		"newline": "\n", "escape": "\x1b", "c1-csi": "\u009b", "line-separator": "\u2028",
		"bidi-override": "\u202e", "bidi-isolate": "\u2066",
	} {
		name, character := name, character
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			unsafePath := filepath.Join(t.TempDir(), "bundle"+character+"spoof")
			app, stdout, stderr := newTestApp()
			app.verifyBackup = func(context.Context, string) (recovery.VerifiedBundle, error) {
				t.Fatal("terminal-unsafe verify path reached recovery")
				return recovery.VerifiedBundle{}, nil
			}
			if exit := app.Run([]string{"backup", "verify", unsafePath, "--output", "json"}); exit != ExitUsage || stdout.Len() != 0 {
				t.Fatalf("Run(verify terminal-unsafe path) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if err := protocol.ValidateJSON("cli/v1/error.response.schema.json", stderr.Bytes()); err != nil {
				t.Fatalf("terminal-unsafe path error schema: %v\n%s", err, stderr.String())
			}
			var failure errorEnvelope
			if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil {
				t.Fatalf("decode terminal-unsafe path failure: %v", err)
			}
			assertRecoveryTerminalSafe(t, "path failure message", failure.Error.Message)
			assertRecoveryTerminalSafe(t, "path failure hint", failure.Error.Hint)
		})
	}
}

func assertBoundedRecoveryFailureMessages(t *testing.T, stdout, stderr []byte) {
	t.Helper()
	var report struct {
		Error *offlineRecoveryError `json:"error"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil || report.Error == nil {
		t.Fatalf("decode recovery failure report: %v\n%s", err, stdout)
	}
	var failure errorEnvelope
	if err := json.Unmarshal(stderr, &failure); err != nil {
		t.Fatalf("decode recovery failure envelope: %v\n%s", err, stderr)
	}
	for name, message := range map[string]string{"report": report.Error.Message, "error envelope": failure.Error.Message} {
		if message == "" || len(message) > recoveryTextMaxBytes {
			t.Fatalf("%s recovery message is not bounded and sanitized: len=%d value=%q", name, len(message), message)
		}
		assertRecoveryTerminalSafe(t, name+" recovery message", message)
	}
}

func assertRecoveryTerminalSafe(t *testing.T, description, value string) {
	t.Helper()
	if !utf8.ValidString(value) {
		t.Fatalf("%s is not valid UTF-8: %q", description, value)
	}
	for _, character := range value {
		if recoveryTerminalUnsafeRune(character) {
			t.Fatalf("%s retained unsafe terminal rune U+%04X: %q", description, character, value)
		}
	}
}

func TestM20BackupRestoreCLIIsOfflineAndLeavesPendingTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	target := filepath.Join(root, "restored")
	pending := recovery.PendingRestore{
		Path: target, BackupID: "backup_" + strings.Repeat("a", 32), ManifestSHA256: strings.Repeat("b", 64),
		EventHighWater: 72, LogicalSHA256: strings.Repeat("c", 64),
	}
	app, stdout, stderr := newTestApp()
	app.restoreBackup = func(_ context.Context, gotBundle, gotTarget string) (recovery.PendingRestore, error) {
		if gotBundle != bundle || gotTarget != target {
			t.Fatalf("RestorePending(%q,%q), want (%q,%q)", gotBundle, gotTarget, bundle, target)
		}
		return pending, nil
	}
	if exit := app.Run([]string{"backup", "restore", bundle, "--to", target, "--output", "json"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(backup restore) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), backupRestoreSchema) || !strings.Contains(stdout.String(), `"pending_activation":true`) {
		t.Fatalf("backup restore output = %q", stdout.String())
	}

	for name, args := range map[string][]string{
		"missing bundle": {"backup", "restore", "--to", target},
		"missing target": {"backup", "restore", bundle},
		"force rejected": {"backup", "restore", bundle, "--to", target, "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, stderr := newTestApp()
			if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("Run(%s) exit=%d stderr=%q", name, exit, stderr.String())
			}
		})
	}
}

func TestM20BackupCreateCLICanonicalizesAndForwardsOneTarget(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "new-bundle")
	client := &fakeDaemonClient{backupCreate: localapi.BackupCreateResult{
		Schema: localapi.BackupCreateSchema, Type: "backup",
		Backup: localapi.BackupSummary{
			ID: "backup_" + strings.Repeat("a", 32), Path: target, EventSequence: 41,
			ManifestSHA256: strings.Repeat("b", 64), TotalBytes: 4096,
		},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/crewfold.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	if exit := app.Run([]string{
		"backup", "create", "--socket", "/tmp/crewfold.sock", "--to", target,
		"--idempotency-key", "nightly-cut", "--output", "json",
	}); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), localapi.BackupCreateSchema) {
		t.Fatalf("Run(backup create) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if params := client.backupCreateParams; params.TargetPath != target || params.IdempotencyKey != "nightly-cut" {
		t.Fatalf("BackupCreate params = %#v", params)
	}

	for name, args := range map[string][]string{
		"missing socket": {"backup", "create", "--to", target},
		"missing target": {"backup", "create", "--socket", "/tmp/crewfold.sock"},
		"unknown option": {"backup", "create", "--socket", "/tmp/crewfold.sock", "--to", target, "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, stderr := newTestApp()
			if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("Run(%s) exit=%d stderr=%q", name, exit, stderr.String())
			}
		})
	}
}
