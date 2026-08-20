package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/servicehost"
	"crewfold/internal/store"
)

func TestManagedServiceWorkerRunsAndStopsArbitraryStructuralCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a real local process")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatalf("Chmod(data dir) error = %v", err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{
		RuntimeNodeID:          "11111111111111111111111111111111",
		RuntimeNodeFingerprint: "2222222222222222222222222222222222222222222222222222222222222222",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer storage.Close()
	workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
	fixtureExecutable := filepath.Join(checkout.Path, "managed-service-fixture")
	if err := os.WriteFile(fixtureExecutable, []byte("#!/bin/sh\nprintf '\\033[32mservice ready\\033[0m\\n'\nprintf 'service warning\\nAPI_TOKEN=top-secret\\n\\377\\n' >&2\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatalf("write service fixture: %v", err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
		Name: "sleeping-fixture", Description: "daemon worker fixture", Executable: fixtureExecutable, Arguments: []string{}, WorkingDirectory: ".", Profile: "local-process", ProfileRevision: 1,
		NetworkMode:   domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, MaximumRestarts: 1, RestartCooldownMillis: 0, StopSignal: domain.ManagedServiceStopSignalTerm,
		StopGraceMillis: 500, OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "service-worker-definition", CorrelationID: "service-worker-definition",
	})
	if err != nil {
		t.Fatalf("CreateManagedServiceDefinition() error = %v", err)
	}
	requested, err := storage.StartManagedService(ctx, store.StartManagedServiceCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		IdempotencyKey: "service-worker-start", CorrelationID: "service-worker-start",
	})
	if err != nil {
		t.Fatalf("StartManagedService() error = %v", err)
	}
	instance := &server{
		config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		store:  storage, serviceHost: servicehost.New(dataDir),
	}
	work, found, err := storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found {
		t.Fatalf("ClaimManagedServiceJob(start) = %#v, %v, %v", work, found, err)
	}
	instance.processManagedServiceWork(ctx, work)
	running, err := storage.ManagedServiceInstance(ctx, workspace.ID, requested.Value.ID)
	if err != nil || running.Status != domain.ManagedServiceHealthy || running.RuntimePID <= 1 {
		t.Fatalf("running service = %#v, %v", running, err)
	}
	originalPID := running.RuntimePID
	if _, err := storage.RequestManagedServiceAction(ctx, store.RequestManagedServiceActionCommand{
		WorkspaceIdentifier: workspace.ID, InstanceID: running.ID, ExpectedRevision: running.Revision, Action: domain.ManagedServiceJobRestart,
		IdempotencyKey: "service-worker-restart", CorrelationID: "service-worker-restart",
	}); err != nil {
		t.Fatalf("RequestManagedServiceAction(restart) error = %v", err)
	}
	work, found, err = storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Job.Action != domain.ManagedServiceJobRestart {
		t.Fatalf("ClaimManagedServiceJob(restart) = %#v, %v, %v", work, found, err)
	}
	instance.processManagedServiceWork(ctx, work)
	running, err = storage.ManagedServiceInstance(ctx, workspace.ID, running.ID)
	if err != nil || running.Status != domain.ManagedServiceHealthy || running.RuntimePID <= 1 || running.RuntimePID == originalPID || running.RestartCount != 1 {
		t.Fatalf("restarted service = %#v, original pid = %d, error = %v", running, originalPID, err)
	}
	stopping, err := storage.RequestManagedServiceAction(ctx, store.RequestManagedServiceActionCommand{
		WorkspaceIdentifier: workspace.ID, InstanceID: running.ID, ExpectedRevision: running.Revision, Action: domain.ManagedServiceJobStop,
		IdempotencyKey: "service-worker-stop", CorrelationID: "service-worker-stop",
	})
	if err != nil {
		t.Fatalf("RequestManagedServiceAction() error = %v", err)
	}
	work, found, err = storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Job.Action != domain.ManagedServiceJobStop {
		t.Fatalf("ClaimManagedServiceJob(stop) = %#v, %v, %v", work, found, err)
	}
	instance.processManagedServiceWork(ctx, work)
	stopped, err := storage.ManagedServiceInstance(ctx, workspace.ID, running.ID)
	if err != nil || stopped.Status != domain.ManagedServiceStopped || stopped.RuntimePID != 0 || stopping.Value.Status != domain.ManagedServiceStopping {
		t.Fatalf("stopped service = %#v, request = %#v, error = %v", stopped, stopping, err)
	}
	logs, err := storage.ManagedServiceTerminalLogs(ctx, workspace.ID, running.ID)
	if err != nil || logs.Stdout.Text != "service ready\n" || !strings.Contains(logs.Stderr.Text, "service warning\n") || !strings.Contains(logs.Stderr.Text, "API_TOKEN=[REDACTED]") || strings.Contains(logs.Stderr.Text, "top-secret") || strings.Contains(logs.Stdout.Text+logs.Stderr.Text, "\x1b") || !strings.Contains(logs.Stderr.Text, "\uFFFD") || logs.State != "terminal" {
		t.Fatalf("terminal service logs = %#v, %v", logs, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "service-runtime", running.ID)); !os.IsNotExist(err) {
		t.Fatalf("terminal node-local runtime remains: %v", err)
	}
}

func TestManagedServiceWorkerServesAndSupervisesGenericHTTPProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a real local process")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("1", 32), RuntimeNodeFingerprint: strings.Repeat("2", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
	if err := os.WriteFile(filepath.Join(checkout.Path, "index.html"), []byte("generic managed process\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
		Name: "generic-http", Description: "non-Vite HTTP fixture", Executable: python,
		Arguments: []string{"-m", "http.server", strconv.Itoa(port), "--bind", "127.0.0.1"}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkLoopback,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthHTTP, Host: "127.0.0.1", Port: port, Path: "/", IntervalMillis: 100, TimeoutMillis: 100},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 500,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "generic-http-definition", CorrelationID: "generic-http-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := storage.StartManagedService(ctx, store.StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: "generic-http-start", CorrelationID: "generic-http-start"})
	if err != nil {
		t.Fatal(err)
	}
	instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: servicehost.New(dataDir)}
	running := processManagedServiceUntil(t, instance, workspace.ID, requested.Value.ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceHealthy })
	response, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "generic managed process") {
		t.Fatalf("generic HTTP response status=%d body=%q error=%v", response.StatusCode, body, readErr)
	}
	stopManagedServiceFixture(t, instance, workspace.ID, running)
}

func TestManagedServiceWorkerReportsExactLocalStartupFailuresWithoutBootstrap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real local process boundaries")
	}
	python, pythonErr := exec.LookPath("python3")
	if pythonErr == nil {
		python, pythonErr = filepath.Abs(python)
	}
	tests := []struct {
		name  string
		build func(*testing.T, domain.Checkout) (string, []string, domain.ManagedServiceHealthCheck, func())
	}{
		{name: "missing executable", build: func(t *testing.T, checkout domain.Checkout) (string, []string, domain.ManagedServiceHealthCheck, func()) {
			return filepath.Join(checkout.Path, "missing-executable"), []string{}, domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50}, func() {}
		}},
		{name: "checkout disappeared", build: func(t *testing.T, checkout domain.Checkout) (string, []string, domain.ManagedServiceHealthCheck, func()) {
			if err := os.RemoveAll(checkout.Path); err != nil {
				t.Fatal(err)
			}
			return "/bin/sleep", []string{"30"}, domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50}, func() {}
		}},
	}
	if pythonErr == nil {
		tests = append(tests, struct {
			name  string
			build func(*testing.T, domain.Checkout) (string, []string, domain.ManagedServiceHealthCheck, func())
		}{name: "occupied port", build: func(t *testing.T, _ domain.Checkout) (string, []string, domain.ManagedServiceHealthCheck, func()) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			return python, []string{"-m", "http.server", strconv.Itoa(port), "--bind", "127.0.0.1"}, domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthHTTP, Host: "127.0.0.1", Port: port, Path: "/", IntervalMillis: 100, TimeoutMillis: 50}, func() { _ = listener.Close() }
		}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			if err := os.Chmod(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("9", 32), RuntimeNodeFingerprint: strings.Repeat("a", 64)})
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
			executable, arguments, health, cleanup := test.build(t, checkout)
			defer cleanup()
			definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
				WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
				Name: strings.ReplaceAll(test.name, " ", "-"), Description: test.name, Executable: executable, Arguments: arguments, WorkingDirectory: ".",
				Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkLoopback, Health: health,
				RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
				OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
				IdempotencyKey: "startup-failure-definition-" + test.name, CorrelationID: "startup-failure-definition-" + test.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			requested, err := storage.StartManagedService(ctx, store.StartManagedServiceCommand{
				WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
				IdempotencyKey: "startup-failure-start-" + test.name, CorrelationID: "startup-failure-start-" + test.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: servicehost.New(dataDir)}
			failed := processManagedServiceUntil(t, instance, workspace.ID, requested.Value.ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceFailed })
			if failed.DiagnosticCode != "service_start_failed" && failed.DiagnosticCode != "service_process_exited" {
				t.Fatalf("startup failure = %#v", failed)
			}
			if failed.Diagnostic == "" || failed.RuntimePID != 0 || failed.RestartCount != 0 {
				t.Fatalf("startup failure diagnosis/authority = %#v", failed)
			}
		})
	}
}

func TestManagedServiceWorkerRestartsFailureAndMissingDaemonGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real local processes")
	}
	for _, policy := range []string{domain.ManagedServiceRestartOnFailure, domain.ManagedServiceRestartOnDaemon} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			if err := os.Chmod(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("3", 32), RuntimeNodeFingerprint: strings.Repeat("4", 64)})
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
			fixture := filepath.Join(checkout.Path, "restart-fixture")
			script := "#!/bin/sh\n"
			if policy == domain.ManagedServiceRestartOnFailure {
				script += "n=0; test ! -f restart-count || n=$(cat restart-count); n=$((n+1)); printf '%s\\n' \"$n\" > restart-count; echo attempt-$n; test \"$n\" -gt 1 || exit 7\n"
			}
			script += "exec /bin/sleep 30\n"
			if err := os.WriteFile(fixture, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
				WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
				Name: "restart-fixture", Description: "restart policy fixture", Executable: fixture, Arguments: []string{}, WorkingDirectory: ".",
				Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
				Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
				RestartPolicy: policy, MaximumRestarts: 1, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 250,
				OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
				IdempotencyKey: "restart-definition-" + policy, CorrelationID: "restart-definition-" + policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			requested, err := storage.StartManagedService(ctx, store.StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: "restart-start-" + policy, CorrelationID: "restart-start-" + policy})
			if err != nil {
				t.Fatal(err)
			}
			oldHost := servicehost.New(dataDir)
			instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: oldHost}
			running := processManagedServiceUntil(t, instance, workspace.ID, requested.Value.ID, func(value domain.ManagedServiceInstance) bool {
				return value.Status == domain.ManagedServiceHealthy && (policy != domain.ManagedServiceRestartOnFailure || value.RestartCount == 1)
			})
			if policy == domain.ManagedServiceRestartOnDaemon {
				oldPID := running.RuntimePID
				if err := syscall.Kill(-running.RuntimeProcessGroupID, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				instance.serviceHost = servicehost.New(dataDir)
				running = processManagedServiceUntil(t, instance, workspace.ID, requested.Value.ID, func(value domain.ManagedServiceInstance) bool {
					return value.Status == domain.ManagedServiceHealthy && value.RestartCount == 1 && value.RuntimePID != oldPID
				})
			} else if running.RestartCount != 1 {
				t.Fatalf("on-failure restart count = %d, want 1", running.RestartCount)
			}
			stopManagedServiceFixture(t, instance, workspace.ID, running)
		})
	}
}

func TestManagedServiceWorkerBoundsARepeatedStartupCrashLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real local processes")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("7", 32), RuntimeNodeFingerprint: strings.Repeat("8", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
	fixture := filepath.Join(checkout.Path, "crash-loop-fixture")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\necho crash-generation >&2\nexit 17\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
		Name: "crash-loop", Description: "bounded repeated process failure", Executable: fixture, Arguments: []string{}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartOnFailure, MaximumRestarts: 2, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "crash-loop-definition", CorrelationID: "crash-loop-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := storage.StartManagedService(ctx, store.StartManagedServiceCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		IdempotencyKey: "crash-loop-start", CorrelationID: "crash-loop-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: servicehost.New(dataDir)}
	failed := processManagedServiceUntil(t, instance, workspace.ID, requested.Value.ID, func(value domain.ManagedServiceInstance) bool {
		return value.Status == domain.ManagedServiceFailed
	})
	if failed.RestartCount != 2 || (failed.DiagnosticCode != "service_start_failed" && failed.DiagnosticCode != "service_process_exited") {
		t.Fatalf("bounded crash-loop result = %#v, want two restarts and a terminal process failure", failed)
	}
	logs, err := storage.ManagedServiceTerminalLogs(ctx, workspace.ID, failed.ID)
	if err != nil || !strings.Contains(logs.Stderr.Text, "crash-generation") {
		t.Fatalf("crash-loop logs = %#v, %v", logs, err)
	}
	if work, found, err := storage.ClaimManagedServiceJob(ctx, time.Second); err != nil || found {
		t.Fatalf("post-crash-loop job = %#v, found=%v, error=%v", work, found, err)
	}
}

func TestDurableAgentManagedServiceToolsControlOneGenericProcessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a real local process")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("5", 32), RuntimeNodeFingerprint: strings.Repeat("6", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "process-owner", Role: "arbitrary local process operator", Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1,
		IdempotencyKey: "service-tool-agent", CorrelationID: "service-tool-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	membership, err := storage.AttachDomainAgent(ctx, store.AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentHandsOn,
		IdempotencyKey: "service-tool-membership", CorrelationID: "service-tool-membership",
	})
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "service-tool-thread"
	if _, err := storage.BindDomainAgentSession(ctx, store.BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: threadID, CWD: checkout.Path,
	}); err != nil {
		t.Fatal(err)
	}
	fixtureExecutable := filepath.Join(checkout.Path, "agent-service-fixture")
	if err := os.WriteFile(fixtureExecutable, []byte("#!/bin/sh\nprintf 'agent-owned service\\n'\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
		Name: "agent-service", Description: "agent-granted generic process", Executable: fixtureExecutable, Arguments: []string{}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 250,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "service-tool-definition", CorrelationID: "service-tool-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := storage.CreateManagedServiceGrant(ctx, store.CreateManagedServiceGrantCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedDefinitionRevision: definition.Value.Revision,
		ManagerAgentIdentifier: agent.Value.ID, ExpectedMembershipRevision: membership.Value.Revision,
		Actions: []string{domain.ManagedServiceActionInspect, domain.ManagedServiceActionLogs, domain.ManagedServiceActionStart, domain.ManagedServiceActionStop}, MaximumInstances: 1,
		IdempotencyKey: "service-tool-grant", CorrelationID: "service-tool-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: servicehost.New(dataDir)}
	startArguments := domainSessionControlServiceArguments{Action: domain.ManagedServiceActionStart, GrantID: grant.Value.ID, ExpectedGrantRevision: grant.Value.Revision, DefinitionID: definition.Value.ID, ExpectedDefinitionRevision: definition.Value.Revision}
	requireSuccessfulDomainServiceTool(t, instance, threadID, "agent-service-start", domainToolControlService, startArguments)
	requireSuccessfulDomainServiceTool(t, instance, threadID, "agent-service-start", domainToolControlService, startArguments)
	instances, err := storage.ManagedServiceInstances(ctx, store.ListManagedServiceInstancesQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || len(instances) != 1 || instances[0].Source.Type != domain.ManagedServiceSourceAgent || instances[0].Source.AgentID != agent.Value.ID || instances[0].Source.GrantID != grant.Value.ID {
		t.Fatalf("agent-started managed service = %#v, %v", instances, err)
	}
	running := processManagedServiceUntil(t, instance, workspace.ID, instances[0].ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceHealthy })
	requireSuccessfulDomainServiceTool(t, instance, threadID, "agent-service-inspect", domainToolInspectService, domainSessionInspectServiceArguments{
		Action: domain.ManagedServiceActionInspect, GrantID: grant.Value.ID, ExpectedGrantRevision: grant.Value.Revision, InstanceID: running.ID, ExpectedInstanceRevision: running.Revision,
	})
	requireSuccessfulDomainServiceTool(t, instance, threadID, "agent-service-stop", domainToolControlService, domainSessionControlServiceArguments{
		Action: domain.ManagedServiceActionStop, GrantID: grant.Value.ID, ExpectedGrantRevision: grant.Value.Revision, InstanceID: running.ID, ExpectedInstanceRevision: running.Revision,
	})
	stopped := processManagedServiceUntil(t, instance, workspace.ID, running.ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceStopped })
	requireSuccessfulDomainServiceTool(t, instance, threadID, "agent-service-logs", domainToolInspectService, domainSessionInspectServiceArguments{
		Action: domain.ManagedServiceActionLogs, GrantID: grant.Value.ID, ExpectedGrantRevision: grant.Value.Revision, InstanceID: stopped.ID, ExpectedInstanceRevision: stopped.Revision,
	})
}

func TestDurableAgentProposesAndOwnerAcceptsOneGenericProcessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a real local process")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{RuntimeNodeID: strings.Repeat("7", 32), RuntimeNodeFingerprint: strings.Repeat("8", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	workspace, project, checkout := createManagedServiceWorkerFixture(t, storage)
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "process-proposer", Role: "durable coordinator", Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1,
		IdempotencyKey: "service-proposal-agent", CorrelationID: "service-proposal-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AttachDomainAgent(ctx, store.AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentHandsOn,
		IdempotencyKey: "service-proposal-membership", CorrelationID: "service-proposal-membership",
	}); err != nil {
		t.Fatal(err)
	}
	const threadID = "service-proposal-thread"
	if _, err := storage.BindDomainAgentSession(ctx, store.BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: threadID, CWD: checkout.Path,
	}); err != nil {
		t.Fatal(err)
	}
	fixtureExecutable := filepath.Join(checkout.Path, "proposed-service-fixture")
	if err := os.WriteFile(fixtureExecutable, []byte("#!/bin/sh\nprintf 'agent-proposed service\\n'\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	instance := &server{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, store: storage, serviceHost: servicehost.New(dataDir)}
	requireSuccessfulDomainServiceTool(t, instance, threadID, "propose-agent-service", domainToolProposeService, domainSessionProposeServiceArguments{
		Name: "agent-proposed-service", Description: "Exact process inspected by the durable coordinator", Checkout: checkout.Path,
		Executable: fixtureExecutable, Arguments: []string{}, WorkingDirectory: ".", NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domainSessionProposeServiceHealthArguments{Type: domain.ManagedServiceHealthProcess},
		RestartPolicy: domain.ManagedServiceRestartNever, Summary: "Run the exact inspected process so the owner can test it.",
	})
	definitions, err := storage.ManagedServiceDefinitions(ctx, store.ListManagedServiceDefinitionsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || len(definitions) != 1 || definitions[0].CreatedBy != agent.Value.ID {
		t.Fatalf("agent-authored definitions = %#v, %v", definitions, err)
	}
	requests, err := storage.ManagedServiceRequests(ctx, store.ListManagedServiceRequestsQuery{WorkspaceIdentifier: workspace.ID, Status: domain.ManagedServiceRequestPending})
	if err != nil || len(requests) != 1 || requests[0].DefinitionID != definitions[0].ID {
		t.Fatalf("pending owner review = %#v, %v", requests, err)
	}
	if values, listErr := storage.ManagedServiceInstances(ctx, store.ListManagedServiceInstancesQuery{WorkspaceIdentifier: workspace.ID}); listErr != nil || len(values) != 0 {
		t.Fatalf("inert proposal instances = %#v, %v", values, listErr)
	}
	accepted, err := storage.DecideManagedServiceRequest(ctx, store.DecideManagedServiceRequestCommand{
		WorkspaceIdentifier: workspace.ID, RequestID: requests[0].ID, ExpectedRevision: requests[0].Revision, Accept: true,
		Reason: "Owner reviewed the exact process and wants it running.", IdempotencyKey: "accept-agent-service-proposal", CorrelationID: "accept-agent-service-proposal",
	})
	if err != nil || accepted.Value.Grant == nil || accepted.Value.Instance == nil {
		t.Fatalf("accepted agent process proposal = %#v, %v", accepted, err)
	}
	if accepted.Value.Grant.ManagerAgentID != agent.Value.ID || accepted.Value.Instance.Source.RequestID != requests[0].ID {
		t.Fatalf("accepted proposal authority/source = %#v / %#v", accepted.Value.Grant, accepted.Value.Instance)
	}
	running := processManagedServiceUntil(t, instance, workspace.ID, accepted.Value.Instance.ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceHealthy })
	requireSuccessfulDomainServiceTool(t, instance, threadID, "inspect-agent-proposal", domainToolInspectService, domainSessionInspectServiceArguments{
		Action: domain.ManagedServiceActionInspect, GrantID: accepted.Value.Grant.ID, ExpectedGrantRevision: accepted.Value.Grant.Revision, InstanceID: running.ID, ExpectedInstanceRevision: running.Revision,
	})
	requireSuccessfulDomainServiceTool(t, instance, threadID, "stop-agent-proposal", domainToolControlService, domainSessionControlServiceArguments{
		Action: domain.ManagedServiceActionStop, GrantID: accepted.Value.Grant.ID, ExpectedGrantRevision: accepted.Value.Grant.Revision, InstanceID: running.ID, ExpectedInstanceRevision: running.Revision,
	})
	stopped := processManagedServiceUntil(t, instance, workspace.ID, running.ID, func(value domain.ManagedServiceInstance) bool { return value.Status == domain.ManagedServiceStopped })
	requireSuccessfulDomainServiceTool(t, instance, threadID, "logs-agent-proposal", domainToolInspectService, domainSessionInspectServiceArguments{
		Action: domain.ManagedServiceActionLogs, GrantID: accepted.Value.Grant.ID, ExpectedGrantRevision: accepted.Value.Grant.Revision, InstanceID: stopped.ID, ExpectedInstanceRevision: stopped.Revision,
	})
}

func requireSuccessfulDomainServiceTool(t *testing.T, instance *server, threadID, callID, tool string, arguments any) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"arguments": arguments, "callId": callID, "threadId": threadID, "tool": tool, "turnId": "turn-" + callID})
	if err != nil {
		t.Fatal(err)
	}
	response, err := instance.handleDomainSessionToolRequest(context.Background(), execution.CodexAppServerRequest{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if encoded, ok := response.(json.RawMessage); ok {
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		response = decoded
	}
	result, ok := response.(map[string]any)
	if !ok || result["success"] != true {
		t.Fatalf("%s tool result = %#v", tool, response)
	}
}

func processManagedServiceUntil(t *testing.T, instance *server, workspaceID, instanceID string, done func(domain.ManagedServiceInstance) bool) domain.ManagedServiceInstance {
	t.Helper()
	var diagnostics bytes.Buffer
	instance.config.Logger = slog.New(slog.NewTextHandler(&diagnostics, nil))
	// Package-wide and repository-wide race/load gates can heavily delay the
	// test process even though every individual worker transition is bounded.
	// This helper is a state barrier, not a product latency assertion.
	deadline := time.Now().Add(15 * time.Second)
	var last domain.ManagedServiceInstance
	for time.Now().Before(deadline) {
		work, found, err := instance.store.ClaimManagedServiceJob(context.Background(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			instance.processManagedServiceWork(context.Background(), work)
		}
		value, err := instance.store.ManagedServiceInstance(context.Background(), workspaceID, instanceID)
		if err != nil {
			t.Fatal(err)
		}
		last = value
		if done(value) {
			return value
		}
		time.Sleep(20 * time.Millisecond)
	}
	detail, detailErr := instance.store.ManagedServiceDetail(context.Background(), workspaceID, instanceID)
	t.Fatalf("managed service %s did not reach expected state; last state=%s health=%s restart_count=%d revision=%d diagnosis=%s: %s; jobs=%#v; detail_error=%v; worker_log=%s", instanceID, last.Status, last.HealthStatus, last.RestartCount, last.Revision, last.DiagnosticCode, last.Diagnostic, detail.Jobs, detailErr, diagnostics.String())
	return domain.ManagedServiceInstance{}
}

func stopManagedServiceFixture(t *testing.T, instance *server, workspaceID string, value domain.ManagedServiceInstance) {
	t.Helper()
	if _, err := instance.store.RequestManagedServiceAction(context.Background(), store.RequestManagedServiceActionCommand{
		WorkspaceIdentifier: workspaceID, InstanceID: value.ID, ExpectedRevision: value.Revision, Action: domain.ManagedServiceJobStop,
		IdempotencyKey: "stop-" + value.ID, CorrelationID: "stop-" + value.ID,
	}); err != nil {
		t.Fatal(err)
	}
	processManagedServiceUntil(t, instance, workspaceID, value.ID, func(current domain.ManagedServiceInstance) bool {
		return current.Status == domain.ManagedServiceStopped
	})
}

func createManagedServiceWorkerFixture(t *testing.T, storage *store.Store) (domain.Workspace, domain.Project, domain.Checkout) {
	t.Helper()
	ctx := context.Background()
	initialized, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "service-worker", IdempotencyKey: "service-worker-workspace", CorrelationID: "service-worker-workspace"})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	checkoutPath := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(checkoutPath, 0o700); err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	registered, err := storage.RegisterProject(ctx, store.RegisterProjectCommand{
		WorkspaceIdentifier: initialized.Workspace.ID, Name: "service-project", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "service-worker-project", CorrelationID: "service-worker-project",
		Observation: domain.CheckoutObservation{
			Path: checkoutPath, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
			Branch: "main", HeadCommit: "2222222222222222222222222222222222222222",
			GitDir: filepath.Join(checkoutPath, ".git"), GitCommonDir: filepath.Join(checkoutPath, ".git"),
			Repository: domain.RepositoryObservation{Fingerprint: "git_1111111111111111111111111111111111111111111111111111111111111111", ObjectFormat: "sha1", RootCommits: []string{"0000000000000000000000000000000000000000"}},
		},
	})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	return initialized.Workspace, registered.Project, registered.Checkout
}
