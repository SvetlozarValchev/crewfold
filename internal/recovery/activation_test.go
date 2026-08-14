package recovery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

func TestRestoreActivationIsExplicitFreshExactAndFirstStartConsumed(t *testing.T) {
	ctx := context.Background()
	target := restoreRecoveryTestBundle(t)
	databaseBefore, err := os.ReadFile(filepath.Join(target, "crewfold.db"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := CheckActivationState(target)
	if err != nil || pending.Status != ActivationStatePending || pending.BackupID == "" {
		t.Fatalf("CheckActivationState(pending) = %#v, %v", pending, err)
	}
	if _, err := Activate(ctx, target, false); ErrorCode(err) != CodeRestoreSourceRetirementUnconfirmed {
		t.Fatalf("Activate(unconfirmed) error = %v, code = %q", err, ErrorCode(err))
	}
	for _, path := range []string{"node.id", "node.key", "capabilities", "runtime", "check-runtime", restoreActivationIntentMarker, restoreActivatedMarker} {
		if _, err := os.Lstat(filepath.Join(target, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unconfirmed activation created %s: %v", path, err)
		}
	}

	activated, err := Activate(ctx, target, true)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if activated.Path != target || !activated.SourceRetired || activated.NodeFingerprint == "" || activated.ActivationSHA256 == "" {
		t.Fatalf("Activate() = %#v", activated)
	}
	if _, err := os.Lstat(filepath.Join(target, restorePendingMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending marker remains after activation: %v", err)
	}
	for _, path := range []string{"node.id", "node.key", restoreActivatedMarker} {
		info, err := os.Lstat(filepath.Join(target, path))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("activated %s = %#v, %v", path, info, err)
		}
	}
	for _, path := range []string{"capabilities", "runtime", "check-runtime"} {
		entries, err := os.ReadDir(filepath.Join(target, path))
		if err != nil || len(entries) != 0 {
			t.Fatalf("activated root %s = %#v, %v, want empty", path, entries, err)
		}
	}
	databaseAfter, err := os.ReadFile(filepath.Join(target, "crewfold.db"))
	if err != nil || !bytes.Equal(databaseAfter, databaseBefore) {
		t.Fatalf("activation changed canonical database bytes: %v", err)
	}

	state, err := CheckActivationState(target)
	if err != nil || state.Status != ActivationStateActivated || state.ActivationSHA256 != activated.ActivationSHA256 {
		t.Fatalf("CheckActivationState(activated) = %#v, %v", state, err)
	}
	verified, err := VerifyActivated(ctx, target)
	if err != nil || verified != activated {
		t.Fatalf("VerifyActivated() = %#v, %v, want %#v", verified, err, activated)
	}
	if err := ConsumeActivated(target, verified.ActivationSHA256); err != nil {
		t.Fatalf("ConsumeActivated() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, restoreActivatedMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("full activation marker remains after consumption: %v", err)
	}
	consumedInfo, err := os.Lstat(filepath.Join(target, restoreConsumedMarker))
	if err != nil || !consumedInfo.Mode().IsRegular() || consumedInfo.Mode().Perm() != 0o600 || consumedInfo.Size() > 1024 {
		t.Fatalf("compact consumed marker = %#v, %v", consumedInfo, err)
	}
	consumed, err := CheckActivationState(target)
	if err != nil || consumed.Status != ActivationStateConsumed || consumed.ActivationSHA256 != activated.ActivationSHA256 {
		t.Fatalf("CheckActivationState(consumed) = %#v, %v", consumed, err)
	}
	if _, err := VerifyActivated(ctx, target); ErrorCode(err) != CodeRestoreNotActivated {
		t.Fatalf("VerifyActivated(consumed) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestConsumeActivatedResumesAfterCompactSealCrashBoundary(t *testing.T) {
	target := restoreRecoveryTestBundle(t)
	activated, err := Activate(context.Background(), target, true)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyActivated(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected activation consumption crash")
	err = consumeActivatedWithHooks(target, verified.ActivationSHA256, consumptionHooks{
		afterConsumedSeal: func() error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("consumeActivatedWithHooks() error = %v, want injected", err)
	}
	for _, marker := range []string{restoreActivatedMarker, restoreConsumedMarker} {
		if info, err := os.Lstat(filepath.Join(target, marker)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("crash-boundary marker %s = %#v, %v", marker, info, err)
		}
	}
	state, err := CheckActivationState(target)
	if err != nil || state.Status != ActivationStateActivated || state.BackupID != activated.BackupID || state.ActivationSHA256 != activated.ActivationSHA256 {
		t.Fatalf("CheckActivationState(crash boundary) = %#v, %v", state, err)
	}
	if _, err := VerifyActivated(context.Background(), target); err != nil {
		t.Fatalf("VerifyActivated(crash boundary) error = %v", err)
	}
	if err := ConsumeActivated(target, verified.ActivationSHA256); err != nil {
		t.Fatalf("ConsumeActivated(resume) error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, restoreActivatedMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconciled full activation marker remains: %v", err)
	}
	state, err = CheckActivationState(target)
	if err != nil || state.Status != ActivationStateConsumed || state.BackupID != activated.BackupID || state.ActivationSHA256 != activated.ActivationSHA256 {
		t.Fatalf("CheckActivationState(reconciled) = %#v, %v", state, err)
	}
}

func TestRestoreActivationGeneratesANodeKeyDistinctFromTheSource(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	if err := os.Chmod(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceKey, err := execution.CreateNodeKey(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.CreateNodeID(sourceDir); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, sourceDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "distinct-key", IdempotencyKey: "distinct-key-workspace", CorrelationID: "request-distinct-key-workspace"}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	if _, err := CreateBundle(ctx, storage, sourceDir, bundle, "distinct-key-backup"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestorePending(ctx, bundle, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, "node.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending restore copied source node key: %v", err)
	}
	if _, err := Activate(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	targetKey, err := os.ReadFile(filepath.Join(target, "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(targetKey) != 32 || bytes.Equal(targetKey, sourceKey) {
		t.Fatalf("activation node key was not fresh: source=%x target=%x", sourceKey, targetKey)
	}
}

func TestRestoreActivationResumesEveryDurableCrashBoundary(t *testing.T) {
	stages := []struct {
		name  string
		hooks func(error) activationHooks
	}{
		{name: "intent", hooks: func(injected error) activationHooks {
			return activationHooks{afterIntent: func() error { return injected }}
		}},
		{name: "node-id", hooks: func(injected error) activationHooks {
			return activationHooks{afterNodeID: func() error { return injected }}
		}},
		{name: "node-key", hooks: func(injected error) activationHooks {
			return activationHooks{afterNodeKey: func() error { return injected }}
		}},
		{name: "operational-roots", hooks: func(injected error) activationHooks {
			count := 0
			return activationHooks{afterOperationalRoot: func() error {
				count++
				if count == 2 {
					return injected
				}
				return nil
			}}
		}},
		{name: "activated-seal", hooks: func(injected error) activationHooks {
			return activationHooks{afterActivatedSeal: func() error { return injected }}
		}},
		{name: "pending-removal", hooks: func(injected error) activationHooks {
			return activationHooks{afterPendingRemoval: func() error { return injected }}
		}},
	}
	for _, stage := range stages {
		stage := stage
		t.Run(stage.name, func(t *testing.T) {
			target := restoreRecoveryTestBundle(t)
			injected := errors.New("injected activation crash")
			if _, err := activateWithHooks(context.Background(), target, true, stage.hooks(injected)); !errors.Is(err, injected) {
				t.Fatalf("activateWithHooks() error = %v, want injected", err)
			}
			keyBefore, _ := os.ReadFile(filepath.Join(target, "node.key"))
			activated, err := Activate(context.Background(), target, true)
			if err != nil {
				t.Fatalf("Activate(resume) error = %v", err)
			}
			keyAfter, err := os.ReadFile(filepath.Join(target, "node.key"))
			if err != nil || len(keyAfter) != 32 {
				t.Fatalf("resumed node key = %x, %v", keyAfter, err)
			}
			if len(keyBefore) != 0 && !bytes.Equal(keyBefore, keyAfter) {
				t.Fatal("resumed activation replaced an already-durable node key")
			}
			if _, err := VerifyActivated(context.Background(), target); err != nil {
				t.Fatalf("VerifyActivated(resumed) error = %v; activation = %#v", err, activated)
			}
		})
	}
}

func TestActivationAndFirstStartupRejectTamperAndInjectedNonterminalWork(t *testing.T) {
	t.Run("pending database tamper", func(t *testing.T) {
		target := restoreRecoveryTestBundle(t)
		tamperRecoveryDatabase(t, target)
		before := snapshotRecoveryTree(t, target)
		if _, err := Activate(context.Background(), target, true); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("Activate(tampered) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, target, before)
	})
	t.Run("activated node key tamper", func(t *testing.T) {
		target := restoreRecoveryTestBundle(t)
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "node.key"), bytes.Repeat([]byte{0xa5}, 32), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeRestoreNotActivated {
			t.Fatalf("VerifyActivated(tampered key) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("pending nonterminal run", func(t *testing.T) {
		target, workspaceID, task := restoreAssignedTaskBundle(t)
		storage, err := store.Open(context.Background(), target, store.Options{})
		if err != nil {
			t.Fatalf("open restored database fixture: %v", err)
		}
		_, err = storage.CreateRun(context.Background(), store.CreateRunCommand{
			WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, Runtime: "fake", Provider: "fake",
			Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "restored-live-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "still running"}}},
			ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "restored-live-run", CorrelationID: "request-restored-live-run",
		})
		closeErr := storage.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("inject nonterminal run = %v, close = %v", err, closeErr)
		}
		before := snapshotRecoveryTree(t, target)
		if _, err := Activate(context.Background(), target, true); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("Activate(nonterminal) error = %v, code = %q", err, ErrorCode(err))
		}
		assertRecoveryTreeUnchanged(t, target, before)
	})
	t.Run("first-start nonterminal run", func(t *testing.T) {
		target, workspaceID, task := restoreAssignedTaskBundle(t)
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		storage, err := store.Open(context.Background(), target, store.Options{})
		if err != nil {
			t.Fatalf("open activated database fixture: %v", err)
		}
		_, err = storage.CreateRun(context.Background(), store.CreateRunCommand{
			WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, Runtime: "fake", Provider: "fake",
			Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "first-start-live-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "still running"}}},
			ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "first-start-live-run", CorrelationID: "request-first-start-live-run",
		})
		closeErr := storage.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("inject first-start nonterminal run = %v, close = %v", err, closeErr)
		}
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(nonterminal) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("pending nonterminal check before identity creation", func(t *testing.T) {
		target, workspaceID, task := restoreAssignedTaskBundle(t)
		checkRunID := injectRestoredNonterminalCheck(t, target, workspaceID, task)
		databasePath := filepath.Join(target, "crewfold.db")
		databaseBefore, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		nodeIdentityReached := false
		_, err = activateWithHooks(context.Background(), target, true, activationHooks{
			afterNodeID: func() error { nodeIdentityReached = true; return nil },
		})
		if ErrorCode(err) != CodeBackupIntegrityFailed || nodeIdentityReached {
			t.Fatalf("activateWithHooks(nonterminal check %s) error = %v, code = %q, node identity reached = %t", checkRunID, err, ErrorCode(err), nodeIdentityReached)
		}
		databaseAfter, readErr := os.ReadFile(databasePath)
		if readErr != nil || !bytes.Equal(databaseAfter, databaseBefore) {
			t.Fatalf("failed activation changed canonical database bytes: %v", readErr)
		}
		for _, path := range []string{"node.id", "node.key", "capabilities", "runtime", "check-runtime"} {
			if _, statErr := os.Lstat(filepath.Join(target, path)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed check activation created %s: %v", path, statErr)
			}
		}
	})
	t.Run("first-start nonterminal check", func(t *testing.T) {
		target, workspaceID, task := restoreAssignedTaskBundle(t)
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		checkRunID := injectRestoredNonterminalCheck(t, target, workspaceID, task)
		databasePath := filepath.Join(target, "crewfold.db")
		databaseBefore, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(nonterminal check %s) error = %v, code = %q", checkRunID, err, ErrorCode(err))
		}
		databaseAfter, readErr := os.ReadFile(databasePath)
		if readErr != nil || !bytes.Equal(databaseAfter, databaseBefore) {
			t.Fatalf("failed first-start verification changed canonical database bytes: %v", readErr)
		}
	})
	t.Run("first-start database tamper", func(t *testing.T) {
		target := restoreRecoveryTestBundle(t)
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		tamperRecoveryDatabase(t, target)
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(tampered database) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("pending undeclared artifact before identity creation", func(t *testing.T) {
		target := restoreRecoveryTestBundle(t)
		injectedPath := filepath.Join(target, "check-artifacts", "aa", strings.Repeat("a", 64))
		if err := os.MkdirAll(filepath.Dir(injectedPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(injectedPath, []byte("undeclared"), 0o600); err != nil {
			t.Fatal(err)
		}
		nodeIdentityReached := false
		_, err := activateWithHooks(context.Background(), target, true, activationHooks{
			afterNodeID: func() error { nodeIdentityReached = true; return nil },
		})
		if ErrorCode(err) != CodeBackupIntegrityFailed || nodeIdentityReached {
			t.Fatalf("activateWithHooks(injected artifact) error = %v, code = %q, node identity reached = %t", err, ErrorCode(err), nodeIdentityReached)
		}
		for _, path := range []string{"node.id", "node.key", "capabilities", "runtime", "check-runtime"} {
			if _, err := os.Lstat(filepath.Join(target, path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed activation created %s: %v", path, err)
			}
		}
	})
	t.Run("first-start undeclared artifact", func(t *testing.T) {
		target := restoreRecoveryTestBundle(t)
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		injectedPath := filepath.Join(target, "run-artifacts", "bb", strings.Repeat("b", 64))
		if err := os.MkdirAll(filepath.Dir(injectedPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(injectedPath, []byte("undeclared"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(injected artifact) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("pending referenced artifact tamper", func(t *testing.T) {
		bundle, manifest := createRecoveryTestBundleWithRunArtifacts(t)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := RestorePending(context.Background(), bundle, target); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(manifest.Entries[0].Path)), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Activate(context.Background(), target, true); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("Activate(tampered artifact) error = %v, code = %q", err, ErrorCode(err))
		}
		if _, err := os.Lstat(filepath.Join(target, "node.id")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tampered artifact activation created node identity: %v", err)
		}
	})
	t.Run("first-start referenced artifact tamper", func(t *testing.T) {
		bundle, manifest := createRecoveryTestBundleWithRunArtifacts(t)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := RestorePending(context.Background(), bundle, target); err != nil {
			t.Fatal(err)
		}
		if _, err := Activate(context.Background(), target, true); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(manifest.Entries[0].Path)), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyActivated(context.Background(), target); ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(tampered artifact) error = %v, code = %q", err, ErrorCode(err))
		}
	})
	t.Run("first-start live node binding", func(t *testing.T) {
		target, workspaceID, task := restoreAssignedTaskBundle(t)
		activated, err := Activate(context.Background(), target, true)
		if err != nil {
			t.Fatal(err)
		}
		nodeIDBytes, err := os.ReadFile(filepath.Join(target, "node.id"))
		if err != nil {
			t.Fatal(err)
		}
		storage, err := store.Open(context.Background(), target, store.Options{
			RuntimeNodeID: strings.TrimSpace(string(nodeIDBytes)), RuntimeNodeFingerprint: activated.NodeFingerprint,
		})
		if err != nil {
			t.Fatal(err)
		}
		created, err := storage.CreateRun(context.Background(), store.CreateRunCommand{
			WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, Runtime: "fake", Provider: "fake",
			Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "restored-live-binding", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "active"}}},
			ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "restored-live-binding", CorrelationID: "restored-live-binding",
		})
		if err != nil {
			_ = storage.Close()
			t.Fatal(err)
		}
		if _, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "restored-live-binding-starting"); err != nil {
			_ = storage.Close()
			t.Fatal(err)
		}
		if _, err := storage.MarkRunStarted(context.Background(), created.Detail.Run.ID, "runtime-handle", "provider-handle", "restored-live-binding-started"); err != nil {
			_ = storage.Close()
			t.Fatal(err)
		}
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = VerifyActivated(context.Background(), target)
		if ErrorCode(err) != CodeBackupIntegrityFailed {
			t.Fatalf("VerifyActivated(live binding) error = %v, code = %q", err, ErrorCode(err))
		}
	})
}

func restoreRecoveryTestBundle(t *testing.T) string {
	t.Helper()
	bundle := createRecoveryTestBundle(t)
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestorePending(context.Background(), bundle, target); err != nil {
		t.Fatalf("RestorePending() error = %v", err)
	}
	return target
}

func tamperRecoveryDatabase(t *testing.T, target string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(target, "crewfold.db"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0xff}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func injectRestoredNonterminalCheck(t *testing.T, target, workspaceID string, task domain.TaskDetail) string {
	t.Helper()
	ctx := context.Background()
	storage, err := store.Open(ctx, target, store.Options{})
	if err != nil {
		t.Fatalf("open restored database fixture: %v", err)
	}
	defer func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close restored database fixture: %v", err)
		}
	}()
	inspection, err := storage.InspectProject(ctx, workspaceID, task.Task.ProjectID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v, want one checkout", inspection, err)
	}
	definition, err := storage.CreateCheckDefinition(ctx, store.CreateCheckDefinitionCommand{
		WorkspaceIdentifier: workspaceID, ProjectIdentifier: task.Task.ProjectID, Name: "restored-live-check",
		Executable: "/bin/true", Arguments: []string{"--version"}, WorkingDirectory: ".", TimeoutMillis: 1_000, OutputByteLimit: 1_024,
		IdempotencyKey: "restored-live-check-definition", CorrelationID: "restored-live-check-definition",
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() error = %v", err)
	}
	requirement, err := storage.CreateTaskCheckRequirement(ctx, store.CreateTaskCheckRequirementCommand{
		WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, CriterionKey: "restored-live-check",
		Statement: "the restored check settles", CheckDefinitionID: definition.Value.ID, DefinitionContentRevision: definition.Value.ContentRevision,
		ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: "restored-live-check-requirement", CorrelationID: "restored-live-check-requirement",
	})
	if err != nil {
		t.Fatalf("CreateTaskCheckRequirement() error = %v", err)
	}
	requested, err := storage.RequestCheckRun(ctx, store.RequestCheckRunCommand{
		WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, RequirementID: requirement.Value.ID,
		CheckDefinitionIdentifier: definition.Value.ID, CheckoutIdentifier: inspection.Checkouts[0].ID,
		ExpectedRequirementRevision: requirement.Value.Revision, ExpectedDefinitionContentRevision: definition.Value.ContentRevision,
		ExpectedCheckoutRevision: inspection.Checkouts[0].Revision,
		IdempotencyKey:           "restored-live-check-run", CorrelationID: "restored-live-check-run",
	})
	if err != nil || requested.Value.Status != domain.CheckRunRequested {
		t.Fatalf("RequestCheckRun() = %#v, %v, want requested", requested, err)
	}
	cut, err := storage.CheckQuiescentCut(ctx)
	if err != nil {
		t.Fatalf("CheckQuiescentCut() error = %v", err)
	}
	if cut.Quiescent || cut.Counts.UnfinishedCheckRuns != 1 || cut.Counts.UnsettledCheckJobs != 1 ||
		cut.Counts.NonterminalRuns != 0 || cut.Counts.UnsettledRunJobs != 0 || cut.Counts.RuntimeBindings != 0 ||
		cut.Counts.OpenWakeJobs != 0 || cut.Counts.OpenSchedulingIntents != 0 || cut.Counts.OpenSupervisorActions != 0 || cut.Counts.OpenApprovals != 0 || cut.Counts.OpenOwnerManagerReviews != 0 {
		t.Fatalf("nonterminal check cut = %#v, want exact one check run and one check job", cut)
	}
	return requested.Value.ID
}

func restoreAssignedTaskBundle(t *testing.T) (string, string, domain.TaskDetail) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "restore-live", IdempotencyKey: "restore-live-workspace", CorrelationID: "request-restore-live-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	checkoutPath := filepath.Join(t.TempDir(), "checkout")
	project, err := storage.RegisterProject(ctx, store.RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "restore-live", IdempotencyKey: "restore-live-project", CorrelationID: "request-restore-live-project",
		Observation: domain.CheckoutObservation{
			Path: checkoutPath, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
			Branch: "main", HeadCommit: "2222222222222222222222222222222222222222", GitDir: filepath.Join(checkoutPath, ".git"), GitCommonDir: filepath.Join(checkoutPath, ".git"),
			Repository: domain.RepositoryObservation{Fingerprint: "git_1111111111111111111111111111111111111111111111111111111111111111", ObjectFormat: "sha1", RootCommits: []string{"0000000000000000000000000000000000000000"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{WorkspaceIdentifier: workspace.Workspace.ID, Name: "runner", Role: "arbitrary-label", Provider: "fake", Runtime: "fake", IdempotencyKey: "restore-live-agent", CorrelationID: "request-restore-live-agent"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := storage.CreateTask(ctx, store.CreateTaskCommand{WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID, Title: "restore live task", Priority: 100, IdempotencyKey: "restore-live-task", CorrelationID: "request-restore-live-task"})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := storage.AssignTask(ctx, store.AssignTaskCommand{WorkspaceIdentifier: workspace.Workspace.ID, TaskID: created.Detail.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 300, ExpectedRevision: created.Detail.Task.Revision, IdempotencyKey: "restore-live-assignment", CorrelationID: "request-restore-live-assignment"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	if _, err := CreateBundle(ctx, storage, dataDir, bundle, "restore-live-bundle"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestorePending(ctx, bundle, target); err != nil {
		t.Fatal(err)
	}
	return target, workspace.Workspace.ID, assigned.Detail
}
