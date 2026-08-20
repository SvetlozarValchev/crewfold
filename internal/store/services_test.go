package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestManagedServiceDefinitionRejectsOpaqueOrUnboundedAuthority(t *testing.T) {
	t.Parallel()
	base := CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: "workspace", ProjectIdentifier: "project", CheckoutID: "checkout",
		Name: "fixture", Description: "fixture", Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
	}
	tests := []struct {
		name   string
		change func(*CreateManagedServiceDefinitionCommand)
	}{
		{name: "opaque shell", change: func(command *CreateManagedServiceDefinitionCommand) {
			command.Executable = "/bin/bash"
			command.Arguments = []string{"-lc", "npm run dev"}
		}},
		{name: "secret environment", change: func(command *CreateManagedServiceDefinitionCommand) {
			command.Environment = []domain.ManagedServiceEnvironmentVariable{{Name: "OPENAI_API_KEY", Value: "secret"}}
		}},
		{name: "endpoint without exposure", change: func(command *CreateManagedServiceDefinitionCommand) {
			command.Health = domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthTCP, Host: "127.0.0.1", Port: 4312, IntervalMillis: 100, TimeoutMillis: 50}
		}},
		{name: "remote health", change: func(command *CreateManagedServiceDefinitionCommand) {
			command.NetworkMode = domain.ManagedServiceNetworkLoopback
			command.Health = domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthHTTP, Host: "0.0.0.0", Port: 4312, Path: "/", IntervalMillis: 100, TimeoutMillis: 50}
		}},
		{name: "local network", change: func(command *CreateManagedServiceDefinitionCommand) {
			command.NetworkMode = "local"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := base
			test.change(&command)
			if err := validateManagedServiceDefinition(command); ErrorCode(err) != CodeInvalidManagedService {
				t.Fatalf("validateManagedServiceDefinition() error = %v, want %q", err, CodeInvalidManagedService)
			}
		})
	}
}

func TestManagedServiceStartEnforcesFixedProjectCapacityBeforeEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	for index := 0; index < managedServiceProjectCapacity; index++ {
		definition, createErr := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
			Name: fmt.Sprintf("fixture-%d", index), Description: "capacity fixture", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".",
			Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
			Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
			RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
			OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
			IdempotencyKey: fmt.Sprintf("capacity-definition-%d", index), CorrelationID: fmt.Sprintf("capacity-definition-%d", index),
		})
		if createErr != nil {
			t.Fatalf("CreateManagedServiceDefinition(%d) error = %v", index, createErr)
		}
		if _, startErr := storage.StartManagedService(ctx, StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: fmt.Sprintf("capacity-start-%d", index), CorrelationID: fmt.Sprintf("capacity-start-%d", index)}); startErr != nil {
			t.Fatalf("StartManagedService(%d) error = %v", index, startErr)
		}
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
		Name: "fixture-refused", Description: "capacity refusal fixture", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "capacity-definition-refused", CorrelationID: "capacity-definition-refused",
	})
	if err != nil {
		t.Fatalf("CreateManagedServiceDefinition(refused) error = %v", err)
	}
	before := m19StoreEventCount(t, storage)
	_, err = storage.StartManagedService(ctx, StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: "capacity-start-refused", CorrelationID: "capacity-start-refused"})
	if ErrorCode(err) != CodeManagedServiceCapacity || m19StoreEventCount(t, storage) != before {
		t.Fatalf("StartManagedService(over capacity) error = %v code=%q events=%d want=%d", err, ErrorCode(err), m19StoreEventCount(t, storage), before)
	}
}

func TestManagedServiceDefinitionAndStartAreExactReplayableAndBlockBackup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace := initializeSourceTestWorkspace(t, storage).Workspace
	registered, err := storage.RegisterProject(ctx, RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "service-demo", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "service-project", CorrelationID: "service-project",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "service-demo"), "main"),
	})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	workstream, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: registered.Project.ID, PrimaryCheckoutID: registered.Checkout.ID,
		Title: "Operate the development surface", Budget: domain.Budget{}, IdempotencyKey: "service-workstream", CorrelationID: "service-workstream",
	})
	if err != nil {
		t.Fatalf("CreateObjective() error = %v", err)
	}
	command := CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: registered.Project.ID, WorkstreamID: workstream.Value.ID, CheckoutID: registered.Checkout.ID,
		Name: "vite-dev", Description: "Signal Garden local preview", Executable: "/usr/bin/npm", Arguments: []string{"run", "dev", "--", "--host", "127.0.0.1"}, WorkingDirectory: ".",
		Environment: []domain.ManagedServiceEnvironmentVariable{{Name: "BROWSER", Value: "none"}}, Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkLoopback,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthHTTP, Host: "127.0.0.1", Port: 5173, Path: "/", IntervalMillis: 1000, TimeoutMillis: 500},
		RestartPolicy: domain.ManagedServiceRestartOnFailure, MaximumRestarts: 3, RestartCooldownMillis: 500, StopSignal: domain.ManagedServiceStopSignalTerm,
		StopGraceMillis: 5000, OutputByteLimit: 1 << 20, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "service-definition", CorrelationID: "service-definition",
	}
	created, err := storage.CreateManagedServiceDefinition(ctx, command)
	if err != nil {
		t.Fatalf("CreateManagedServiceDefinition() error = %v", err)
	}
	if created.Value.Arguments == nil || created.Value.Environment == nil {
		t.Fatalf("CreateManagedServiceDefinition() emitted nullable collections: arguments=%#v environment=%#v", created.Value.Arguments, created.Value.Environment)
	}
	replay := command
	replay.CorrelationID = "service-definition-replay"
	replayed, err := storage.CreateManagedServiceDefinition(ctx, replay)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("CreateManagedServiceDefinition(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	queried, err := storage.ManagedServiceDefinition(ctx, workspace.ID, created.Value.ID)
	if err != nil || !reflect.DeepEqual(queried, created.Value) {
		t.Fatalf("ManagedServiceDefinition() = %#v, %v; want %#v", queried, err, created.Value)
	}
	listed, err := storage.ManagedServiceDefinitions(ctx, ListManagedServiceDefinitionsQuery{WorkspaceIdentifier: workspace.ID, WorkstreamID: workstream.Value.ID})
	if err != nil || len(listed) != 1 || listed[0].ID != created.Value.ID {
		t.Fatalf("ManagedServiceDefinitions() = %#v, %v", listed, err)
	}
	started, err := storage.StartManagedService(ctx, StartManagedServiceCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: created.Value.ID, ExpectedRevision: created.Value.Revision,
		IdempotencyKey: "service-start", CorrelationID: "service-start",
	})
	if err != nil || started.Value.Status != domain.ManagedServiceRequested {
		t.Fatalf("StartManagedService() = %#v, %v", started, err)
	}
	cut, err := storage.CheckQuiescentCut(ctx)
	if err != nil {
		t.Fatalf("CheckQuiescentCut() error = %v", err)
	}
	if cut.Quiescent || cut.Counts.NonterminalManagedServices != 1 || cut.Counts.UnsettledManagedServiceJobs != 1 || cut.Counts.ManagedServiceBindings != 0 {
		t.Fatalf("service cut = %#v, want exact requested service and start job", cut)
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" {
		t.Fatalf("VerifyCanonical() = %#v, %v", report, err)
	}
}

func TestExecutionHealthReportsOnlyTheLatestManagedServiceDefinitionDiagnosis(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace := initializeSourceTestWorkspace(t, storage).Workspace
	registered, err := storage.RegisterProject(ctx, RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "service-health", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "service-health-project", CorrelationID: "service-health-project",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "service-health"), "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: registered.Project.ID, CheckoutID: registered.Checkout.ID,
		Name: "preview", Description: "diagnostic fixture", Executable: "/bin/false", Arguments: []string{}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "service-health-definition", CorrelationID: "service-health-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := storage.StartManagedService(ctx, StartManagedServiceCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		IdempotencyKey: "service-health-start", CorrelationID: "service-health-start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := storage.ClaimManagedServiceJob(ctx, time.Second); err != nil || !claimed {
		t.Fatalf("ClaimManagedServiceJob() claimed=%v error=%v", claimed, err)
	}
	if _, err := storage.MarkManagedServiceStarting(ctx, requested.Value.ID, "service-health-starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.FailManagedService(ctx, requested.Value.ID, "service_start_failed", "executable is unavailable", "service-health-failed"); err != nil {
		t.Fatal(err)
	}
	health, err := storage.ExecutionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Services.DefinitionCount != 1 || health.Services.InstanceCount != 1 || health.Services.IssueCount != 1 || len(health.Services.Issues) != 1 ||
		health.Services.Issues[0].InstanceID != requested.Value.ID || health.Services.Issues[0].DiagnosticCode != "service_start_failed" {
		t.Fatalf("managed-service execution health = %#v", health.Services)
	}
	if _, err := storage.StartManagedService(ctx, StartManagedServiceCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		IdempotencyKey: "service-health-retry", CorrelationID: "service-health-retry",
	}); err != nil {
		t.Fatal(err)
	}
	health, err = storage.ExecutionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Services.InstanceCount != 2 || health.Services.IssueCount != 0 || len(health.Services.Issues) != 0 {
		t.Fatalf("resolved managed-service execution health = %#v", health.Services)
	}
}

func TestManagedServiceWorkerLifecycleKeepsExactBindingAndStopAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{RuntimeNodeID: "11111111111111111111111111111111", RuntimeNodeFingerprint: "2222222222222222222222222222222222222222222222222222222222222222"})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	checkout := inspection.Checkouts[0]
	definition, err := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: checkout.ID,
		Name: "fixture", Description: "worker lifecycle fixture", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".", Profile: "local-process", ProfileRevision: 1,
		NetworkMode: domain.ManagedServiceNetworkNone, Health: domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, RestartCooldownMillis: 0, StopSignal: domain.ManagedServiceStopSignalTerm,
		StopGraceMillis: 100, OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "worker-definition", CorrelationID: "worker-definition",
	})
	if err != nil {
		t.Fatalf("CreateManagedServiceDefinition() error = %v", err)
	}
	requested, err := storage.StartManagedService(ctx, StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: "worker-start", CorrelationID: "worker-start"})
	if err != nil {
		t.Fatalf("StartManagedService() error = %v", err)
	}
	work, found, err := storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Instance.ID != requested.Value.ID || work.Job.Action != domain.ManagedServiceJobStart {
		t.Fatalf("ClaimManagedServiceJob() = %#v, %v, %v", work, found, err)
	}
	work, err = storage.MarkManagedServiceStarting(ctx, work.Instance.ID, "worker-starting")
	if err != nil || work.Instance.Status != domain.ManagedServiceStarting {
		t.Fatalf("MarkManagedServiceStarting() = %#v, %v", work, err)
	}
	instance, err := storage.RecordManagedServiceStarted(ctx, work.Instance.ID, ManagedServiceRuntimeBindingInput{
		PID: 4001, ProcessGroupID: 4001, ProcessStartTicks: 77,
		StdoutPath: "service-runtime/fixture/stdout.log", StderrPath: "service-runtime/fixture/stderr.log",
	}, domain.ManagedServiceHealthHealthy, "", "worker-started")
	if err != nil || instance.Status != domain.ManagedServiceHealthy || instance.RuntimePID != 4001 {
		t.Fatalf("RecordManagedServiceStarted() = %#v, %v", instance, err)
	}
	probe, found, err := storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || probe.Instance.ID != instance.ID || probe.Job.Action != domain.ManagedServiceJobProbe || probe.Job.Status != domain.ManagedServiceJobLeased {
		t.Fatalf("ClaimManagedServiceJob(probe) = %#v, %v, %v", probe, found, err)
	}
	stopping, err := storage.RequestManagedServiceAction(ctx, RequestManagedServiceActionCommand{
		WorkspaceIdentifier: workspace.ID, InstanceID: instance.ID, ExpectedRevision: instance.Revision, Action: domain.ManagedServiceJobStop,
		IdempotencyKey: "worker-stop", CorrelationID: "worker-stop",
	})
	if err != nil || stopping.Value.Status != domain.ManagedServiceStopping {
		t.Fatalf("RequestManagedServiceAction() = %#v, %v", stopping, err)
	}
	work, found, err = storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Job.Action != domain.ManagedServiceJobStop {
		t.Fatalf("ClaimManagedServiceJob(stop) = %#v, %v, %v", work, found, err)
	}
	archive, err := storage.PrepareManagedServiceLogArchive(ctx, instance.ID, domain.ManagedServiceLogs{
		InstanceID: instance.ID,
		State:      "terminal",
		Stdout:     domain.CapturedLog{Text: "ready at http://127.0.0.1:7777\n", CapturedBytes: 31},
		Stderr:     domain.CapturedLog{Text: "", CapturedBytes: 0},
	}, 4096)
	if err != nil {
		t.Fatalf("PrepareManagedServiceLogArchive() error = %v", err)
	}
	stopped, err := storage.StopManagedServiceWithLogs(ctx, instance.ID, 0, "stopped by owner", &archive, "", "worker-stopped")
	if err != nil || stopped.Status != domain.ManagedServiceStopped || stopped.RuntimePID != 0 {
		t.Fatalf("StopManagedServiceWithLogs() = %#v, %v", stopped, err)
	}
	terminalLogs, err := storage.ManagedServiceTerminalLogs(ctx, workspace.ID, instance.ID)
	if err != nil || terminalLogs.State != "terminal" || terminalLogs.Stdout.Text != "ready at http://127.0.0.1:7777\n" || terminalLogs.Stderr.Text != "" {
		t.Fatalf("ManagedServiceTerminalLogs() = %#v, %v", terminalLogs, err)
	}
	report, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" || !report.Quiescence.Quiescent {
		t.Fatalf("VerifyCanonical() = %#v, %v", report, err)
	}
}

func TestManagedServiceUnknownRequiresExplicitOwnerRetirementBeforeFreshStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{RuntimeNodeID: "11111111111111111111111111111111", RuntimeNodeFingerprint: "2222222222222222222222222222222222222222222222222222222222222222"})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
		Name: "unknown-fixture", Description: "unknown lifecycle fixture", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".", Profile: "local-process", ProfileRevision: 1,
		NetworkMode: domain.ManagedServiceNetworkNone, Health: domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100, OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "unknown-definition", CorrelationID: "unknown-definition",
	})
	if err != nil {
		t.Fatalf("CreateManagedServiceDefinition() error = %v", err)
	}
	requested, err := storage.StartManagedService(ctx, StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: 1, IdempotencyKey: "unknown-start", CorrelationID: "unknown-start"})
	if err != nil {
		t.Fatalf("StartManagedService() error = %v", err)
	}
	work, found, err := storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Instance.ID != requested.Value.ID {
		t.Fatalf("ClaimManagedServiceJob(start) = %#v, %v, %v", work, found, err)
	}
	if _, err = storage.MarkManagedServiceStarting(ctx, work.Instance.ID, "unknown-starting"); err != nil {
		t.Fatalf("MarkManagedServiceStarting() error = %v", err)
	}
	instance, err := storage.RecordManagedServiceStarted(ctx, work.Instance.ID, ManagedServiceRuntimeBindingInput{PID: 4002, ProcessGroupID: 4002, ProcessStartTicks: 78, StdoutPath: "service-runtime/unknown/stdout.log", StderrPath: "service-runtime/unknown/stderr.log"}, domain.ManagedServiceHealthHealthy, "", "unknown-started")
	if err != nil {
		t.Fatalf("RecordManagedServiceStarted() error = %v", err)
	}
	work, found, err = storage.ClaimManagedServiceJob(ctx, time.Second)
	if err != nil || !found || work.Job.Action != domain.ManagedServiceJobProbe {
		t.Fatalf("ClaimManagedServiceJob(probe) = %#v, %v, %v", work, found, err)
	}
	unknown, err := storage.LoseManagedService(ctx, instance.ID, "daemon could not prove the process identity", "unknown-lost")
	if err != nil || unknown.Status != domain.ManagedServiceUnknown || unknown.RuntimePID != 4002 {
		t.Fatalf("LoseManagedService() = %#v, %v", unknown, err)
	}
	before := m19StoreEventCount(t, storage)
	_, err = storage.ResolveManagedServiceUnknown(ctx, ResolveManagedServiceUnknownCommand{WorkspaceIdentifier: workspace.ID, InstanceID: unknown.ID, ExpectedRevision: unknown.Revision, Reason: "the owner verified the old process exited", IdempotencyKey: "unknown-resolve-refused", CorrelationID: "unknown-resolve-refused"})
	if ErrorCode(err) != CodeInvalidManagedService || m19StoreEventCount(t, storage) != before {
		t.Fatalf("ResolveManagedServiceUnknown(without confirmation) error=%v events=%d want=%d", err, m19StoreEventCount(t, storage), before)
	}
	command := ResolveManagedServiceUnknownCommand{WorkspaceIdentifier: workspace.ID, InstanceID: unknown.ID, ExpectedRevision: unknown.Revision, RuntimeRetiredConfirmed: true, Reason: "the owner verified the old process exited", IdempotencyKey: "unknown-resolve", CorrelationID: "unknown-resolve"}
	resolved, err := storage.ResolveManagedServiceUnknown(ctx, command)
	if err != nil || resolved.Value.Status != domain.ManagedServiceFailed || resolved.Value.RuntimePID != 0 || resolved.Value.DiagnosticCode != "service_runtime_owner_resolved" {
		t.Fatalf("ResolveManagedServiceUnknown() = %#v, %v", resolved, err)
	}
	replay := command
	replay.CorrelationID = "unknown-resolve-replay"
	replayed, err := storage.ResolveManagedServiceUnknown(ctx, replay)
	if err != nil || !reflect.DeepEqual(replayed, resolved) {
		t.Fatalf("ResolveManagedServiceUnknown(replay) = %#v, %v; want %#v", replayed, err, resolved)
	}
	cut, err := storage.CheckQuiescentCut(ctx)
	if err != nil || !cut.Quiescent {
		t.Fatalf("CheckQuiescentCut() = %#v, %v", cut, err)
	}
	fresh, err := storage.StartManagedService(ctx, StartManagedServiceCommand{WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision, IdempotencyKey: "unknown-fresh-start", CorrelationID: "unknown-fresh-start"})
	if err != nil || fresh.Value.ID == unknown.ID || fresh.Value.Status != domain.ManagedServiceRequested {
		t.Fatalf("StartManagedService(fresh) = %#v, %v", fresh, err)
	}
}

func TestManagedServiceDefinitionRejectsShellAndCrossProjectCheckout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	other, err := storage.RegisterProject(ctx, RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "other", WriteMode: domain.WriteModeShared,
		IdempotencyKey: "service-other-project", CorrelationID: "service-other-project",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "other"), "other"),
	})
	if err != nil {
		t.Fatalf("RegisterProject(other) error = %v", err)
	}
	base := CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: other.Checkout.ID,
		Name: "unsafe", Description: "invalid fixture", Executable: "/bin/bash", Arguments: []string{"-lc", "npm run dev"}, WorkingDirectory: ".", Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkLoopback,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 1000, TimeoutMillis: 500},
		RestartPolicy: domain.ManagedServiceRestartNever, RestartCooldownMillis: 0, StopSignal: domain.ManagedServiceStopSignalTerm,
		StopGraceMillis: 5000, OutputByteLimit: 65536, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "invalid-service", CorrelationID: "invalid-service",
	}
	if _, err := storage.CreateManagedServiceDefinition(ctx, base); ErrorCode(err) != CodeInvalidManagedService {
		t.Fatalf("CreateManagedServiceDefinition(shell) error = %v, code = %q", err, ErrorCode(err))
	}
	base.Executable = "/bin/true"
	base.Arguments = []string{}
	if _, err := storage.CreateManagedServiceDefinition(ctx, base); ErrorCode(err) != CodeInvalidManagedService {
		t.Fatalf("CreateManagedServiceDefinition(cross-project checkout) error = %v, code = %q", err, ErrorCode(err))
	}
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	workstream, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, PrimaryCheckoutID: inspection.Checkouts[0].ID,
		Title: "Exact process checkout", Budget: domain.Budget{}, IdempotencyKey: "service-workstream-checkout", CorrelationID: "service-workstream-checkout",
	})
	if err != nil {
		t.Fatal(err)
	}
	adjacent, err := storage.AddCheckout(ctx, AddCheckoutCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeShared,
		IdempotencyKey: "service-adjacent-checkout", CorrelationID: "service-adjacent-checkout",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "adjacent"), "adjacent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	base.CheckoutID = adjacent.Checkout.ID
	base.WorkstreamID = workstream.Value.ID
	base.IdempotencyKey = "invalid-workstream-checkout"
	base.CorrelationID = "invalid-workstream-checkout"
	if _, err := storage.CreateManagedServiceDefinition(ctx, base); ErrorCode(err) != CodeInvalidManagedService {
		t.Fatalf("CreateManagedServiceDefinition(non-primary workstream checkout) error = %v, code = %q", err, ErrorCode(err))
	}
}
