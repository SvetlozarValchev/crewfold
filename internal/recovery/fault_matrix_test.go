package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

func TestCreateBundleCrashBoundariesAreAbsentOrCompleteAndReplayable(t *testing.T) {
	stages := []struct {
		name      string
		published bool
		hooks     func(error) createHooks
	}{
		{name: "database snapshot", hooks: func(injected error) createHooks {
			return createHooks{afterSnapshot: func() error { return injected }}
		}},
		{name: "artifact copy", hooks: func(injected error) createHooks {
			return createHooks{afterArtifacts: func() error { return injected }}
		}},
		{name: "manifest", hooks: func(injected error) createHooks {
			return createHooks{afterManifest: func() error { return injected }}
		}},
		{name: "publish", published: true, hooks: func(injected error) createHooks {
			return createHooks{afterPublish: func() error { return injected }}
		}},
		{name: "before response", published: true, hooks: func(injected error) createHooks {
			return createHooks{beforeResponse: func() error { return injected }}
		}},
	}
	for _, stage := range stages {
		stage := stage
		t.Run(stage.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			if err := os.Chmod(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			storage, err := store.Open(ctx, dataDir, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			if _, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
				Name: "fault-boundary", IdempotencyKey: "fault-workspace", CorrelationID: "fault-workspace",
			}); err != nil {
				t.Fatal(err)
			}

			parent := t.TempDir()
			target := filepath.Join(parent, "bundle")
			injected := errors.New("injected process loss")
			if _, err := createBundleWithHooks(ctx, storage, dataDir, target, "fault-key", stage.hooks(injected)); !errors.Is(err, injected) {
				t.Fatalf("createBundleWithHooks() error = %v, want injected process loss", err)
			}
			assertNoRecoveryStaging(t, parent)
			if stage.published {
				if _, err := VerifyBundle(ctx, target); err != nil {
					t.Fatalf("published crash-boundary bundle is not fully verifiable: %v", err)
				}
			} else if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-publication target exists: %v", err)
			}

			replayed, err := CreateBundle(ctx, storage, dataDir, target, "fault-key")
			if err != nil {
				t.Fatalf("CreateBundle(retry) error = %v", err)
			}
			verified, err := VerifyBundle(ctx, target)
			if err != nil || replayed.ManifestSHA256 != verified.ManifestSHA256 || replayed.Manifest.BackupID != verified.Manifest.BackupID {
				t.Fatalf("retry result = %#v, verified = %#v, error = %v", replayed, verified, err)
			}
		})
	}
}

func TestCreateBundleCancellationAndResourceFailureCleanPrivateStage(t *testing.T) {
	tests := []struct {
		name string
		hook func(context.CancelFunc) func() error
		code string
	}{
		{name: "cancelled", code: CodeOperationCancelled, hook: func(cancel context.CancelFunc) func() error {
			return func() error { cancel(); return nil }
		}},
		{name: "disk full", hook: func(context.CancelFunc) func() error {
			return func() error { return unix.ENOSPC }
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dataDir := t.TempDir()
			if err := os.Chmod(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			storage, err := store.Open(context.Background(), dataDir, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			parent := t.TempDir()
			target := filepath.Join(parent, "bundle")
			_, err = createBundleWithHooks(ctx, storage, dataDir, target, "resource-fault", createHooks{
				afterArtifacts: test.hook(cancel),
			})
			if test.code != "" && ErrorCode(err) != test.code {
				t.Fatalf("createBundleWithHooks() error = %v, code = %q", err, ErrorCode(err))
			}
			if test.code == "" && !errors.Is(err, unix.ENOSPC) {
				t.Fatalf("createBundleWithHooks() error = %v, want ENOSPC", err)
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed operation published a target: %v", err)
			}
			assertNoRecoveryStaging(t, parent)
		})
	}
}

func TestRestorePendingCrashBoundariesAreAbsentOrComplete(t *testing.T) {
	stages := []struct {
		name      string
		published bool
		hooks     func(error) restoreHooks
	}{
		{name: "database copy", hooks: func(injected error) restoreHooks {
			return restoreHooks{afterDatabase: func() error { return injected }}
		}},
		{name: "artifact copy", hooks: func(injected error) restoreHooks {
			return restoreHooks{afterArtifacts: func() error { return injected }}
		}},
		{name: "pending seal", hooks: func(injected error) restoreHooks {
			return restoreHooks{afterSeal: func() error { return injected }}
		}},
		{name: "publish", published: true, hooks: func(injected error) restoreHooks {
			return restoreHooks{afterPublish: func() error { return injected }}
		}},
		{name: "before response", published: true, hooks: func(injected error) restoreHooks {
			return restoreHooks{beforeResponse: func() error { return injected }}
		}},
	}
	for _, stage := range stages {
		stage := stage
		t.Run(stage.name, func(t *testing.T) {
			bundle := createRecoveryTestBundle(t)
			parent := t.TempDir()
			witness := filepath.Join(parent, "existing-sibling")
			if err := os.WriteFile(witness, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "restored")
			injected := errors.New("injected process loss")
			if _, err := restorePendingWithHooks(context.Background(), bundle, target, stage.hooks(injected)); !errors.Is(err, injected) {
				t.Fatalf("restorePendingWithHooks() error = %v, want injected process loss", err)
			}
			assertNoRecoveryStaging(t, parent)
			if content, err := os.ReadFile(witness); err != nil || string(content) != "unchanged" {
				t.Fatalf("restore fault changed existing sibling: %q, %v", content, err)
			}
			if stage.published {
				state, err := CheckActivationState(target)
				if err != nil || state.Status != ActivationStatePending {
					t.Fatalf("published restored target state = %#v, %v", state, err)
				}
				if _, err := RestorePending(context.Background(), bundle, target); ErrorCode(err) != CodeRestoreTargetExists {
					t.Fatalf("RestorePending(retry) error = %v, code = %q", err, ErrorCode(err))
				}
			} else if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-publication restore target exists: %v", err)
			}
		})
	}
}

func TestTwentyFullRecoveryFaultCyclesLeakNoStageFDOrGoroutine(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	bundle := filepath.Join(t.TempDir(), "seed-bundle")
	if _, err := CreateBundle(ctx, storage, dataDir, bundle, "endurance-seed"); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	baselineFDs := recoveryTestOpenFDs(t)
	baselineGoroutines := runtime.NumGoroutine()
	injected := errors.New("deterministic cycle interruption")

	for cycle := 0; cycle < 20; cycle++ {
		target := filepath.Join(parent, "restore-"+twoDigit(cycle))
		hooks := restoreHooks{afterDatabase: func() error { return injected }}
		if cycle%4 == 1 {
			hooks = restoreHooks{afterArtifacts: func() error { return injected }}
		} else if cycle%4 == 2 {
			hooks = restoreHooks{afterSeal: func() error { return injected }}
		} else if cycle%4 == 3 {
			hooks = restoreHooks{afterPublish: func() error { return injected }}
		}
		_, err := restorePendingWithHooks(ctx, bundle, target, hooks)
		if !errors.Is(err, injected) {
			t.Fatalf("cycle %d error = %v", cycle, err)
		}
		if cycle%4 == 3 {
			if _, err := CheckActivationState(target); err != nil {
				t.Fatalf("cycle %d published target is invalid: %v", cycle, err)
			}
		}
		assertNoRecoveryStaging(t, parent)
	}
	if current := recoveryTestOpenFDs(t); current > baselineFDs+3 {
		t.Fatalf("open FDs grew from %d to %d", baselineFDs, current)
	}
	if current := runtime.NumGoroutine(); current > baselineGoroutines+5 {
		t.Fatalf("goroutines grew from %d to %d", baselineGoroutines, current)
	}
}

func assertNoRecoveryStaging(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if validRecoveryStagingName(entry.Name()) {
			t.Fatalf("recovery staging directory leaked: %s", entry.Name())
		}
	}
}

func twoDigit(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}
