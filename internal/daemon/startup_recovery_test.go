package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

func TestDaemonRejectsReservedRecoveryComponentsBeforeMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reservedNames := []string{
		".crewfold-recovery-staging-v1-" + strings.Repeat("a", sha256.Size*2),
		fmt.Sprintf("crewfold-recovery-v1-%d-%d", os.Geteuid(), os.Getpid()),
		fmt.Sprintf(".crewfold-recovery-v1-stage-%d-%d-%s", os.Geteuid(), os.Getpid(), strings.Repeat("b", 24)),
	}
	for _, reservedName := range reservedNames {
		reservedName := reservedName
		t.Run(reservedName, func(t *testing.T) {
			reserved := filepath.Join(root, reservedName)
			selected := filepath.Join(reserved, "selected")
			if err := os.MkdirAll(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			witness := filepath.Join(selected, "witness")
			if err := os.WriteFile(witness, []byte("must remain unchanged\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotStartupTree(t, reserved)
			for index, candidate := range []string{reserved, selected} {
				config := testConfig(t)
				config.DataDir = candidate
				config.SocketPath = filepath.Join(root, fmt.Sprintf("reserved-%d.sock", index))
				if err := Run(context.Background(), config); ErrorCode(err) != CodeInvalidConfiguration {
					t.Fatalf("Run(%q) error = %v, code = %q", candidate, err, ErrorCode(err))
				}
				if _, err := os.Lstat(config.SocketPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("reserved data directory created socket: %v", err)
				}
			}
			if after := snapshotStartupTree(t, reserved); !reflect.DeepEqual(after, before) {
				t.Fatalf("reserved recovery tree changed: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestPendingRestoreRefusesDaemonBeforeOperationalIdentityOrState(t *testing.T) {
	t.Parallel()
	config := testConfig(t)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(config.DataDir, ".restore-pending.json")
	if err := os.WriteFile(marker, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotStartupTree(t, config.DataDir)
	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeRestoreNotActivated {
		t.Fatalf("Run(pending restore) error = %v, code = %q", err, ErrorCode(err))
	}
	if after := snapshotStartupTree(t, config.DataDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("pending restore tree changed before activation: before=%#v after=%#v", before, after)
	}
	for _, relative := range []string{"node.id", "node.key", "crewfold.db", "capabilities", "runtime", "check-runtime"} {
		if _, statErr := os.Lstat(filepath.Join(config.DataDir, relative)); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("pending restore created %s: %v", relative, statErr)
		}
	}
	if _, statErr := os.Lstat(config.SocketPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("pending restore created socket: %v", statErr)
	}
}

func TestPendingRestoreUnsafeLockOrSymlinkAncestorCannotMutateSelectedBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	makePending := func(t *testing.T, root, suffix string) string {
		t.Helper()
		sourceDir := filepath.Join(root, "source-"+suffix)
		if err := os.Mkdir(sourceDir, 0o700); err != nil {
			t.Fatalf("Mkdir(source) error = %v", err)
		}
		source, err := store.Open(ctx, sourceDir, store.Options{})
		if err != nil {
			t.Fatalf("store.Open(source) error = %v", err)
		}
		bundle := filepath.Join(root, "bundle-"+suffix)
		if _, err := recovery.CreateBundle(ctx, source, sourceDir, bundle, "startup-lock-"+suffix); err != nil {
			_ = source.Close()
			t.Fatalf("CreateBundle() error = %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatalf("source.Close() error = %v", err)
		}
		target := filepath.Join(root, "restored-"+suffix)
		if _, err := recovery.RestorePending(ctx, bundle, target); err != nil {
			t.Fatalf("RestorePending() error = %v", err)
		}
		return target
	}

	t.Run("hard-linked lock to restored database", func(t *testing.T) {
		root := t.TempDir()
		target := makePending(t, root, "hardlink")
		databasePath := filepath.Join(target, "crewfold.db")
		before, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatalf("ReadFile(database before) error = %v", err)
		}
		lockPath := filepath.Join(target, "daemon.lock")
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("Remove(restored lock) error = %v", err)
		}
		if err := os.Link(databasePath, lockPath); err != nil {
			t.Fatalf("Link(database, lock) error = %v", err)
		}
		config := testConfig(t)
		config.DataDir = target
		config.SocketPath = filepath.Join(root, "hardlink.sock")
		err = Run(ctx, config)
		if ErrorCode(err) != CodeInvalidConfiguration {
			t.Fatalf("Run(hard-linked restored lock) error = %v, code = %q", err, ErrorCode(err))
		}
		after, readErr := os.ReadFile(databasePath)
		if readErr != nil || !bytes.Equal(after, before) {
			t.Fatalf("restored database changed through hard-linked lock: equal=%t error=%v", bytes.Equal(after, before), readErr)
		}
		if _, statErr := os.Lstat(config.SocketPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unsafe restored lock created listener: %v", statErr)
		}
	})

	t.Run("symlinked ancestor", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatalf("Mkdir(real parent) error = %v", err)
		}
		target := makePending(t, realParent, "ancestor")
		lockPath := filepath.Join(target, "daemon.lock")
		before, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatalf("ReadFile(lock before) error = %v", err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(realParent, alias); err != nil {
			t.Fatalf("Symlink(real parent) error = %v", err)
		}
		config := testConfig(t)
		config.DataDir = filepath.Join(alias, filepath.Base(target))
		config.SocketPath = filepath.Join(root, "symlink.sock")
		err = Run(ctx, config)
		if ErrorCode(err) != CodeInvalidConfiguration {
			t.Fatalf("Run(symlink ancestor) error = %v, code = %q", err, ErrorCode(err))
		}
		after, readErr := os.ReadFile(lockPath)
		if readErr != nil || !bytes.Equal(after, before) {
			t.Fatalf("restored lock changed through symlink ancestor: before=%q after=%q error=%v", before, after, readErr)
		}
		if _, statErr := os.Lstat(config.SocketPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("symlinked restored path created listener: %v", statErr)
		}
	})
}

func TestActivatedRestoreIsFullyVerifiedAndConsumedBeforeFirstServe(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatalf("Mkdir(source) error = %v", err)
	}
	source, err := store.Open(ctx, sourceDir, store.Options{})
	if err != nil {
		t.Fatalf("Open(source) error = %v", err)
	}
	bundlePath := filepath.Join(root, "bundle")
	verified, err := recovery.CreateBundle(ctx, source, sourceDir, bundlePath, "startup-activation-test")
	if closeErr := source.Close(); closeErr != nil {
		t.Fatalf("Close(source) error = %v", closeErr)
	}
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	target := filepath.Join(root, "restored")
	pending, err := recovery.RestorePending(ctx, bundlePath, target)
	if err != nil {
		t.Fatalf("RestorePending() error = %v", err)
	}
	config := testConfig(t)
	config.DataDir = target
	config.SocketPath = filepath.Join(root, "restored.sock")
	if err := Run(ctx, config); ErrorCode(err) != CodeRestoreNotActivated {
		t.Fatalf("Run(pending) error = %v, code = %q", err, ErrorCode(err))
	}
	activated, err := recovery.Activate(ctx, target, true)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if activated.BackupID != pending.BackupID || activated.EventHighWater != verified.Manifest.EventHighWater ||
		len(activated.NodeFingerprint) != 64 || strings.Trim(activated.NodeFingerprint, "0123456789abcdef") != "" {
		t.Fatalf("activated restore = %#v", activated)
	}

	running := startTestServer(t, config)
	doctor, err := localapi.NewClient(config.SocketPath).SystemDoctorFull(ctx)
	if err != nil {
		t.Fatalf("SystemDoctorFull(restored) error = %v", err)
	}
	if doctor.EventSequence != activated.EventHighWater || doctor.Status != "ok" {
		t.Fatalf("restored doctor status=%q sequence=%d checks=%#v, want ok/%d", doctor.Status, doctor.EventSequence, doctor.Checks, activated.EventHighWater)
	}
	state, err := recovery.CheckActivationState(target)
	if err != nil || state.Status != recovery.ActivationStateConsumed || state.ActivationSHA256 != activated.ActivationSHA256 {
		t.Fatalf("CheckActivationState() = %#v, %v", state, err)
	}
	if _, err := localapi.NewClient(config.SocketPath).Stop(ctx); err != nil {
		t.Fatalf("Stop(restored) error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run(restored) error = %v", err)
	}

	// A consumed activation is an ordinary current installation on every later
	// start; it is not reactivated and never creates another node identity.
	warm := startTestServer(t, config)
	if _, err := localapi.NewClient(config.SocketPath).Stop(ctx); err != nil {
		t.Fatalf("Stop(warm restored) error = %v", err)
	}
	if err := warm.wait(); err != nil {
		t.Fatalf("Run(warm restored) error = %v", err)
	}
}

func TestActivatedRestoreStartupPreservesExactTrailingSpaceDataDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatalf("Mkdir(source) error = %v", err)
	}
	source, err := store.Open(ctx, sourceDir, store.Options{})
	if err != nil {
		t.Fatalf("Open(source) error = %v", err)
	}
	bundlePath := filepath.Join(root, "bundle")
	if _, err := recovery.CreateBundle(ctx, source, sourceDir, bundlePath, "startup-visible-space-path"); err != nil {
		_ = source.Close()
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close(source) error = %v", err)
	}

	for _, testCase := range []struct {
		name string
		leaf string
	}{
		{name: "ASCII space", leaf: "ascii-restored "},
		{name: "Unicode space", leaf: "unicode-restored\u00a0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			target := filepath.Join(root, testCase.leaf)
			adjacent := filepath.Join(root, strings.TrimSpace(testCase.leaf))
			if err := os.Mkdir(adjacent, 0o700); err != nil {
				t.Fatalf("Mkdir(trimmed adjacent) error = %v", err)
			}
			sentinel := filepath.Join(adjacent, "must-remain-only-entry")
			if err := os.WriteFile(sentinel, []byte("adjacent path must remain untouched\n"), 0o600); err != nil {
				t.Fatalf("write adjacent sentinel: %v", err)
			}
			if _, err := recovery.RestorePending(ctx, bundlePath, target); err != nil {
				t.Fatalf("RestorePending(exact path) error = %v", err)
			}
			activated, err := recovery.Activate(ctx, target, true)
			if err != nil {
				t.Fatalf("Activate(exact path) error = %v", err)
			}
			for _, name := range []string{"node.id", "node.key"} {
				if info, err := os.Lstat(filepath.Join(target, name)); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("exact activated %s info = %#v, %v", name, info, err)
				}
			}

			config := testConfig(t)
			config.DataDir = target
			config.SocketPath = filepath.Join(root, testCase.name+".sock")
			running := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			doctor, err := client.SystemDoctorFull(ctx)
			if err != nil || doctor.Status != "ok" || doctor.EventSequence != activated.EventHighWater {
				t.Fatalf("SystemDoctorFull(exact path) = %#v, %v", doctor, err)
			}
			state, err := recovery.CheckActivationState(target)
			if err != nil || state.Status != recovery.ActivationStateConsumed || state.ActivationSHA256 != activated.ActivationSHA256 {
				t.Fatalf("CheckActivationState(exact path) = %#v, %v", state, err)
			}
			entries, err := os.ReadDir(adjacent)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(sentinel) {
				t.Fatalf("trimmed adjacent directory was touched: entries=%v error=%v", entries, err)
			}
			contents, err := os.ReadFile(sentinel)
			if err != nil || string(contents) != "adjacent path must remain untouched\n" {
				t.Fatalf("adjacent sentinel = %q, %v", contents, err)
			}
			if _, err := client.Stop(ctx); err != nil {
				t.Fatalf("Stop(exact path) error = %v", err)
			}
			if err := running.wait(); err != nil {
				t.Fatalf("Run(exact path) error = %v", err)
			}
		})
	}
}

func TestTamperedActivatedRestoreFailsBeforeConfigDriversProvidersOrListener(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	source, err := store.Open(ctx, sourceDir, store.Options{})
	if err != nil {
		t.Fatalf("store.Open(source) error = %v", err)
	}
	if _, err := source.InitWorkspace(ctx, store.InitWorkspaceCommand{
		Name: "restore-spy", IdempotencyKey: "restore-spy-workspace", CorrelationID: "restore-spy-workspace-request",
	}); err != nil {
		_ = source.Close()
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	bundle := filepath.Join(root, "bundle")
	if _, err := recovery.CreateBundle(ctx, source, sourceDir, bundle, "restore-spy-backup"); err != nil {
		_ = source.Close()
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("source.Close() error = %v", err)
	}
	target := filepath.Join(root, "restored")
	if _, err := recovery.RestorePending(ctx, bundle, target); err != nil {
		t.Fatalf("RestorePending() error = %v", err)
	}
	if _, err := recovery.Activate(ctx, target, true); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	database, err := os.OpenFile(filepath.Join(target, "crewfold.db"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open restored database for injected corruption: %v", err)
	}
	if _, err := database.WriteAt([]byte{0xff}, 4096); err != nil {
		_ = database.Close()
		t.Fatalf("inject restored database corruption: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close corrupted restored database: %v", err)
	}
	before := snapshotStartupTree(t, target)

	runtimeSpy := &startupExternalSpy{name: "startup-spy-runtime"}
	checkSpy := &startupExternalSpy{name: "direct"}
	providerSpy := &startupExternalSpy{name: "startup-spy-provider"}
	config := testConfig(t)
	config.DataDir = target
	config.SocketPath = filepath.Join(root, "must-not-listen.sock")
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"startup-spy-runtime": runtimeSpy}
	config.CheckRuntimeDriver = checkSpy
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"startup-spy-provider": providerSpy}
	err = Run(ctx, config)
	if code := ErrorCode(err); code != recovery.CodeBackupIntegrityFailed && code != recovery.CodeRestoreUnsafeNonterminal {
		t.Fatalf("Run(tampered activated restore) error = %v, code = %q", err, ErrorCode(err))
	}
	if calls := runtimeSpy.calls.Load() + checkSpy.calls.Load() + providerSpy.calls.Load(); calls != 0 {
		t.Fatalf("startup touched injected driver/provider registry %d times before restore verification", calls)
	}
	if _, err := os.Lstat(config.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered activated restore created a listener path: %v", err)
	}
	if after := snapshotStartupTree(t, target); !reflect.DeepEqual(after, before) {
		t.Fatalf("tampered activated restore tree changed before verification: before=%#v after=%#v", before, after)
	}
}

type startupTreeEntry struct {
	Mode       os.FileMode
	Digest     [sha256.Size]byte
	LinkTarget string
}

func snapshotStartupTree(t *testing.T, root string) map[string]startupTreeEntry {
	t.Helper()
	result := make(map[string]startupTreeEntry)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := startupTreeEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.Digest = sha256.Sum256(contents)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry.LinkTarget = target
		}
		result[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot startup tree %q: %v", root, err)
	}
	return result
}

type startupExternalSpy struct {
	name  string
	calls atomic.Int64
}

func (spy *startupExternalSpy) touched() { spy.calls.Add(1) }

func (spy *startupExternalSpy) Name() string {
	spy.touched()
	return spy.name
}

func (spy *startupExternalSpy) PrepareLaunch(context.Context, string, domain.RunPlacement, execution.LaunchSpec) (execution.RuntimeLaunchPreparation, error) {
	spy.touched()
	return execution.RuntimeLaunchPreparation{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Launch(context.Context, string, domain.RunPlacement, execution.LaunchSpec) (execution.RuntimeBinding, error) {
	spy.touched()
	return execution.RuntimeBinding{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Reconcile(context.Context, string, string) (execution.RuntimeBinding, error) {
	spy.touched()
	return execution.RuntimeBinding{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Inspect(context.Context, string, string) (execution.RuntimeSnapshot, error) {
	spy.touched()
	return execution.RuntimeSnapshot{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) InspectStatus(context.Context, string, string) (execution.RuntimeStatus, error) {
	spy.touched()
	return execution.RuntimeStatus{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Stop(context.Context, string, string, execution.StopSpec) (execution.StopResult, error) {
	spy.touched()
	return execution.StopResult{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Logs(context.Context, string, string, int) (domain.RunLogs, error) {
	spy.touched()
	return domain.RunLogs{}, errors.New("startup runtime spy was called")
}

func (spy *startupExternalSpy) Prepare(context.Context, domain.Run, domain.FakeScenario) (execution.LaunchSpec, error) {
	spy.touched()
	return execution.LaunchSpec{}, errors.New("startup provider spy was called")
}

func (spy *startupExternalSpy) Bind(context.Context, domain.Run, execution.RuntimeBinding) (execution.ProviderBinding, error) {
	spy.touched()
	return execution.ProviderBinding{}, errors.New("startup provider spy was called")
}

func (spy *startupExternalSpy) Next(context.Context, domain.Run, domain.FakeScenario, execution.RuntimeSnapshot) (domain.RunObservation, bool, error) {
	spy.touched()
	return domain.RunObservation{}, false, errors.New("startup provider spy was called")
}
