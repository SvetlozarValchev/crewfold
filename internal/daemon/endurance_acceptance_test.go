package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
)

func TestM20TwentyDeterministicDaemonRestartCyclesLeakNoAuthorityOrResources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the M20 absolute process-resource endurance gate is Linux-specific")
	}
	config := testConfig(t)
	root := filepath.Dir(config.DataDir)

	prime := startM20EnduranceServer(t, config)
	primeClient := localapi.NewClient(config.SocketPath)
	if _, err := primeClient.WorkspaceInit(context.Background(), "personal", "m20-endurance-workspace"); err != nil {
		t.Fatalf("WorkspaceInit(warm) = %v", err)
	}
	if _, err := primeClient.SystemDoctorFull(context.Background()); err != nil {
		t.Fatalf("SystemDoctorFull(prime) = %v", err)
	}
	stopM20EnduranceServer(t, prime, true)

	// Measure from a steady-state restart. The first complete startup and
	// shutdown may lazily establish process-global SQLite and race-runtime file
	// descriptors; those are part of the warm baseline, not a per-cycle leak.
	warm := startM20EnduranceServer(t, config)
	warmClient := localapi.NewClient(config.SocketPath)
	warmDoctor, err := warmClient.SystemDoctorFull(context.Background())
	if err != nil || warmDoctor.Status != "ok" {
		t.Fatalf("SystemDoctorFull(warm) = %#v, %v", warmDoctor, err)
	}
	stopM20EnduranceServer(t, warm, true)
	baselineFDs, baselineGoroutines := waitM20EnduranceResourceQuiescence(t)
	baselineChildren := m20ProcessChildren(t)

	for cycle := 0; cycle < 20; cycle++ {
		running := startM20EnduranceServer(t, config)
		client := localapi.NewClient(config.SocketPath)
		doctor, err := client.SystemDoctorFull(context.Background())
		if err != nil || doctor.Status != "ok" || doctor.EventSequence != warmDoctor.EventSequence {
			t.Fatalf("cycle %d SystemDoctorFull() = %#v, %v", cycle, doctor, err)
		}
		bundle := filepath.Join(root, fmt.Sprintf("bundle-%02d", cycle))
		created, err := client.BackupCreate(context.Background(), localapi.BackupCreateParams{
			TargetPath: bundle, IdempotencyKey: fmt.Sprintf("m20-endurance-backup-%02d", cycle),
		})
		if err != nil || created.Backup.EventSequence != warmDoctor.EventSequence {
			t.Fatalf("cycle %d BackupCreate() = %#v, %v", cycle, created, err)
		}
		if _, err := recovery.VerifyBundle(context.Background(), bundle); err != nil {
			t.Fatalf("cycle %d VerifyBundle() = %v", cycle, err)
		}

		// Half the cycles carry a deliberately truncated in-flight request into
		// shutdown. This covers handler/connection cancellation while the other
		// half use the public graceful-stop receipt.
		if cycle%2 == 1 {
			partial, err := net.Dial("unix", config.SocketPath)
			if err != nil {
				t.Fatalf("cycle %d open partial request: %v", cycle, err)
			}
			if _, err := partial.Write([]byte(`{"id":"truncated"`)); err != nil {
				_ = partial.Close()
				t.Fatalf("cycle %d write partial request: %v", cycle, err)
			}
			stopM20EnduranceServer(t, running, false)
			_ = partial.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := partial.Read(make([]byte, 1)); err == nil {
				_ = partial.Close()
				t.Fatalf("cycle %d partial request remained open after cancellation", cycle)
			}
			_ = partial.Close()
		} else {
			stopM20EnduranceServer(t, running, true)
		}
		currentFDs, currentGoroutines := waitM20EnduranceResourceQuiescence(t)
		if currentFDs > baselineFDs+3 {
			t.Fatalf("cycle %d quiescent open FDs = %d, warm baseline %d (+3 allowed)", cycle, currentFDs, baselineFDs)
		}
		if currentGoroutines > baselineGoroutines+5 {
			t.Fatalf("cycle %d quiescent goroutines = %d, warm baseline %d (+5 allowed)", cycle, currentGoroutines, baselineGoroutines)
		}
		if _, err := os.Lstat(config.SocketPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cycle %d leaked daemon socket: %v", cycle, err)
		}
		if children := m20ProcessChildren(t); !equalM20Strings(children, baselineChildren) {
			t.Fatalf("cycle %d child processes = %v, warm baseline %v", cycle, children, baselineChildren)
		}
		assertNoM20EnduranceStaging(t, root)
		assertNoM20OperationalFiles(t, config.DataDir)
		if err := os.RemoveAll(bundle); err != nil {
			t.Fatalf("cycle %d remove verified owned bundle: %v", cycle, err)
		}
	}

	currentFDs, currentGoroutines := waitM20EnduranceResourceQuiescence(t)
	if currentFDs > baselineFDs+3 || currentGoroutines > baselineGoroutines+5 {
		t.Fatalf("post-cycle resources = %d FDs/%d goroutines, warm baseline %d/%d",
			currentFDs, currentGoroutines, baselineFDs, baselineGoroutines)
	}
}

func waitM20EnduranceResourceQuiescence(t *testing.T) (int64, int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastFDs, lastGoroutines int64 = -1, -1
	stableSamples := 0
	for {
		_, currentFDs, err := daemonProcessResources()
		currentGoroutines := int64(runtime.NumGoroutine())
		if err == nil && currentFDs == lastFDs && currentGoroutines == lastGoroutines {
			stableSamples++
			if stableSamples >= 5 {
				return currentFDs, currentGoroutines
			}
		} else {
			stableSamples = 0
		}
		if time.Now().After(deadline) {
			t.Fatalf("process resources did not quiesce: %d FDs/%d goroutines: %v", currentFDs, currentGoroutines, err)
		}
		lastFDs, lastGoroutines = currentFDs, currentGoroutines
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}

type m20EnduranceServer struct {
	cancel context.CancelFunc
	done   chan error
	socket string
}

func startM20EnduranceServer(t *testing.T, config Config) m20EnduranceServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, config) }()
	client := localapi.NewClient(config.SocketPath)
	deadline := time.Now().Add(10 * time.Second)
	for {
		probeContext, cancelProbe := context.WithTimeout(context.Background(), 250*time.Millisecond)
		_, err := client.Status(probeContext)
		cancelProbe()
		if err == nil {
			return m20EnduranceServer{cancel: cancel, done: done, socket: config.SocketPath}
		}
		select {
		case runErr := <-done:
			cancel()
			t.Fatalf("daemon exited before endurance readiness: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("timed out waiting for endurance daemon: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stopM20EnduranceServer(t *testing.T, running m20EnduranceServer, graceful bool) {
	t.Helper()
	if graceful {
		if _, err := localapi.NewClient(running.socket).Stop(context.Background()); err != nil {
			running.cancel()
			t.Fatalf("request graceful daemon shutdown: %v", err)
		}
	} else {
		running.cancel()
	}
	select {
	case err := <-running.done:
		running.cancel()
		if err != nil {
			t.Fatalf("daemon shutdown = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon shutdown exceeded two seconds")
	}
}

func m20ProcessChildren(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			if _, err := strconv.Atoi(field); err == nil {
				set[field] = true
			}
		}
	}
	result := make([]string, 0, len(set))
	for child := range set {
		result = append(result, child)
	}
	sort.Strings(result)
	return result
}

func equalM20Strings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assertNoM20EnduranceStaging(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".crewfold-recovery-staging-v1-") || strings.HasPrefix(name, ".backup-") || strings.HasPrefix(name, ".restore-") {
			return fmt.Errorf("recovery staging entry leaked at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoM20OperationalFiles(t *testing.T, dataDir string) {
	t.Helper()
	for _, relative := range []string{"capabilities", "runtime", "check-runtime"} {
		root := filepath.Join(dataDir, relative)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return filepath.SkipDir
			}
			if walkErr != nil {
				return walkErr
			}
			if path != root && !entry.IsDir() {
				return fmt.Errorf("unexpected operational file %s", path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}
