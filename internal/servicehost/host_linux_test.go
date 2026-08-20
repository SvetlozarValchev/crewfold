//go:build linux

package servicehost

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestManagedServiceHostHelper(t *testing.T) {
	for index, argument := range os.Args {
		if argument != "crewfold-managed-service-test-helper" || index+1 >= len(os.Args) {
			continue
		}
		switch os.Args[index+1] {
		case "hold":
			fmt.Fprintln(os.Stdout, "fixture ready")
			time.Sleep(time.Hour)
		case "group":
			child := exec.Command("sleep", "60")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			fmt.Fprintln(os.Stdout, child.Process.Pid)
			_ = child.Wait()
			os.Exit(0)
		case "ignore":
			signal.Ignore(syscall.SIGTERM)
			fmt.Fprintln(os.Stdout, "ignoring term")
			time.Sleep(time.Hour)
		case "output":
			fmt.Fprint(os.Stdout, strings.Repeat("o", 32))
			fmt.Fprint(os.Stderr, strings.Repeat("e", 24))
			os.Exit(0)
		case "cwd":
			cwd, _ := os.Getwd()
			fmt.Fprintln(os.Stdout, cwd)
			os.Exit(0)
		case "env":
			fmt.Fprintf(os.Stdout, "ambient=%s\nreviewed=%s\n", os.Getenv("CREWFOLD_M24_AMBIENT_SECRET"), os.Getenv("M24_REVIEWED_VALUE"))
			os.Exit(0)
		case "tcp":
			listener, err := net.Listen("tcp", "127.0.0.1:"+os.Args[index+2])
			if err != nil {
				os.Exit(2)
			}
			defer listener.Close()
			for {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					os.Exit(0)
				}
				_ = connection.Close()
			}
		case "http":
			server := &http.Server{Addr: "127.0.0.1:" + os.Args[index+2], Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/ready" {
					http.NotFound(response, request)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			})}
			if err := server.ListenAndServe(); err != nil {
				os.Exit(2)
			}
		}
		return
	}
}

func managedServiceTestDefinition(executable string, arguments ...string) domain.ManagedServiceDefinition {
	return domain.ManagedServiceDefinition{
		Executable: executable, Arguments: arguments, WorkingDirectory: ".", OutputByteLimit: 4096,
		Health: domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
	}
}

func managedServiceTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	return executable
}

func managedServiceTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func newManagedServiceTestHost(t *testing.T) *Host {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatalf("chmod managed service data directory: %v", err)
	}
	return New(dataDir)
}

func waitManagedServiceSnapshot(t *testing.T, host *Host, instanceID string, binding Binding, health domain.ManagedServiceHealthCheck, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := host.Inspect(context.Background(), instanceID, binding, health)
		if predicate(snapshot) {
			return snapshot
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("managed service snapshot did not reach expected state")
	return Snapshot{}
}

func TestManagedServiceHostUsesPinnedContainedWorkingDirectory(t *testing.T) {
	t.Parallel()
	checkout := t.TempDir()
	workdir := filepath.Join(checkout, "app")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(checkout, "linked")); err != nil {
		t.Fatal(err)
	}
	if file, err := openContainedWorkingDirectory(checkout, "linked"); err == nil {
		file.Close()
		t.Fatal("openContainedWorkingDirectory(symlink) error = nil")
	}
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "cwd")
	definition.WorkingDirectory = "app"
	binding, err := host.Start(context.Background(), "svcinst_cwd", 1, checkout, definition)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitManagedServiceSnapshot(t, host, "svcinst_cwd", binding, definition.Health, func(snapshot Snapshot) bool { return snapshot.ExitKnown })
	stdout, _, _, _, err := host.ReadLogs("svcinst_cwd", binding, 4096)
	if err != nil || strings.TrimSpace(string(stdout)) != workdir {
		t.Fatalf("ReadLogs() stdout=%q error=%v, want cwd %q", stdout, err, workdir)
	}
	host.Forget("svcinst_cwd")
}

func TestManagedServiceHostDoesNotInheritUnreviewedAmbientEnvironment(t *testing.T) {
	t.Setenv("CREWFOLD_M24_AMBIENT_SECRET", "must-not-cross-process-boundary")
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "env")
	definition.Environment = []domain.ManagedServiceEnvironmentVariable{{Name: "M24_REVIEWED_VALUE", Value: "visible"}}
	binding, err := host.Start(context.Background(), "svcinst_environment", 1, t.TempDir(), definition)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitManagedServiceSnapshot(t, host, "svcinst_environment", binding, definition.Health, func(snapshot Snapshot) bool { return snapshot.ExitKnown })
	stdout, _, _, _, err := host.ReadLogs("svcinst_environment", binding, 4096)
	if err != nil || string(stdout) != "ambient=\nreviewed=visible\n" {
		t.Fatalf("ReadLogs() stdout=%q error=%v", stdout, err)
	}
	host.Forget("svcinst_environment")
}

func TestManagedServiceHostSupportsProcessTCPAndHTTPHealth(t *testing.T) {
	t.Parallel()
	for _, healthType := range []string{domain.ManagedServiceHealthProcess, domain.ManagedServiceHealthTCP, domain.ManagedServiceHealthHTTP} {
		t.Run(healthType, func(t *testing.T) {
			port := managedServiceTestPort(t)
			mode := "hold"
			health := domain.ManagedServiceHealthCheck{Type: healthType, IntervalMillis: 100, TimeoutMillis: 100}
			if healthType != domain.ManagedServiceHealthProcess {
				mode = healthType
				health.Host = "127.0.0.1"
				health.Port = port
			}
			if healthType == domain.ManagedServiceHealthHTTP {
				health.Path = "/ready"
			}
			definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", mode, strconv.Itoa(port))
			definition.Health = health
			host := newManagedServiceTestHost(t)
			instanceID := "svcinst_" + healthType
			binding, err := host.Start(context.Background(), instanceID, 1, t.TempDir(), definition)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			waitManagedServiceSnapshot(t, host, instanceID, binding, health, func(snapshot Snapshot) bool {
				return snapshot.Running && snapshot.Health == domain.ManagedServiceHealthHealthy
			})
			if _, err := host.Stop(context.Background(), instanceID, binding, 250*time.Millisecond); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			waitManagedServiceSnapshot(t, host, instanceID, binding, health, func(snapshot Snapshot) bool { return snapshot.ExitKnown || !snapshot.Running })
			host.Forget(instanceID)
		})
	}
}

func TestManagedServiceHostBoundsBothOutputStreams(t *testing.T) {
	t.Parallel()
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "output")
	definition.OutputByteLimit = 16
	binding, err := host.Start(context.Background(), "svcinst_output", 1, t.TempDir(), definition)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitManagedServiceSnapshot(t, host, "svcinst_output", binding, definition.Health, func(snapshot Snapshot) bool { return snapshot.ExitKnown })
	stdout, stderr, stdoutOmitted, stderrOmitted, err := host.ReadLogs("svcinst_output", binding, 4096)
	if err != nil || len(stdout) != 16 || len(stderr) != 16 || stdoutOmitted != 16 || stderrOmitted != 8 {
		t.Fatalf("ReadLogs() = stdout:%d stderr:%d omitted:%d/%d error=%v", len(stdout), len(stderr), stdoutOmitted, stderrOmitted, err)
	}
	host.Forget("svcinst_output")
}

func TestManagedServiceHostRemovesOnlyAnExactTerminalRuntimeTree(t *testing.T) {
	t.Parallel()
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "output")
	binding, err := host.Start(context.Background(), "svcinst_cleanup", 1, t.TempDir(), definition)
	if err != nil {
		t.Fatal(err)
	}
	waitManagedServiceSnapshot(t, host, "svcinst_cleanup", binding, definition.Health, func(snapshot Snapshot) bool { return snapshot.ExitKnown })
	host.Forget("svcinst_cleanup")
	if err := host.RemoveRuntime("svcinst_cleanup"); err != nil {
		t.Fatalf("RemoveRuntime() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(host.dataDir, "service-runtime", "svcinst_cleanup")); !os.IsNotExist(err) {
		t.Fatalf("terminal runtime remains: %v", err)
	}

	unexpected := filepath.Join(host.dataDir, "service-runtime", "svcinst_unexpected", "1")
	if err := os.MkdirAll(unexpected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unexpected, "unowned"), []byte("witness"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := host.RemoveRuntime("svcinst_unexpected"); err == nil {
		t.Fatal("RemoveRuntime(unexpected entry) error = nil")
	}
	content, err := os.ReadFile(filepath.Join(unexpected, "unowned"))
	if err != nil || string(content) != "witness" {
		t.Fatalf("unexpected witness changed: %q, %v", content, err)
	}
}

func TestManagedServiceHostStopsTheWholeProcessGroup(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep unavailable")
	}
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "group")
	binding, err := host.Start(context.Background(), "svcinst_group", 1, t.TempDir(), definition)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	var childPID int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stdout, _, _, _, readErr := host.ReadLogs("svcinst_group", binding, 4096)
		if readErr == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(stdout)))
		}
		if childPID > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 1 {
		t.Fatal("managed service helper did not publish its child PID")
	}
	if _, err := host.Stop(context.Background(), "svcinst_group", binding, 250*time.Millisecond); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		parentErr := syscall.Kill(binding.PID, 0)
		childErr := syscall.Kill(childPID, 0)
		if parentErr != nil && childErr != nil {
			host.Forget("svcinst_group")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed service process group remained after stop")
}

func TestManagedServiceHostKillsAProcessGroupAfterTheStopGraceExpires(t *testing.T) {
	t.Parallel()
	host := newManagedServiceTestHost(t)
	definition := managedServiceTestDefinition(managedServiceTestExecutable(t), "-test.run=^TestManagedServiceHostHelper$", "--", "crewfold-managed-service-test-helper", "ignore")
	binding, err := host.Start(context.Background(), "svcinst_ignore", 1, t.TempDir(), definition)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitManagedServiceSnapshot(t, host, "svcinst_ignore", binding, definition.Health, func(snapshot Snapshot) bool {
		stdout, _, _, _, readErr := host.ReadLogs("svcinst_ignore", binding, 4096)
		return readErr == nil && snapshot.Running && strings.Contains(string(stdout), "ignoring term")
	})
	forced, err := host.Stop(context.Background(), "svcinst_ignore", binding, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !forced {
		t.Fatal("Stop() forced = false, want true after ignored termination signal")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(binding.PID, 0) != nil {
			host.Forget("svcinst_ignore")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed service remained alive after forced stop")
}
