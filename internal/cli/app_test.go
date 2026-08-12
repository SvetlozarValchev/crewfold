package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestNoArgumentsShowsRootHelp(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run(nil); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q, want root usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionText(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"version"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "crewfold dev\n") {
		t.Fatalf("stdout = %q, want development version", stdout.String())
	}
	if !strings.Contains(stdout.String(), "platform: ") {
		t.Fatalf("stdout = %q, want platform", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionJSONAcceptsGlobalOptionBeforeOrAfterCommand(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"before": {"--output", "json", "version"},
		"after":  {"version", "--output=json"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}

			var response buildinfo.Info
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != buildinfo.VersionSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, buildinfo.VersionSchema)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response.Validate() error = %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDoctorSelfJSON(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"doctor", "--self", "--output", "json"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Schema != doctorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, doctorSchema)
	}
	if response.Status != "ok" {
		t.Fatalf("response.Status = %q, want ok; checks = %#v", response.Status, response.Checks)
	}
	if len(response.Checks) != 3 {
		t.Fatalf("len(response.Checks) = %d, want 3", len(response.Checks))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDoctorSelfFailureIsVisibleAndNonZero(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.executablePath = func() (string, error) {
		return "", errors.New("injected executable lookup failure")
	}

	if exitCode := app.Run([]string{"doctor", "--self", "--output=json"}); exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Status != "failed" {
		t.Fatalf("response.Status = %q, want failed", response.Status)
	}
	if !strings.Contains(stdout.String(), "injected executable lookup failure") {
		t.Fatalf("stdout = %q, want injected failure detail", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestUnknownCommandHasStableTextErrorAndUsageExit(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"definitely-not-a-command"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `error: unknown command "definitely-not-a-command"`) {
		t.Fatalf("stderr = %q, want unknown-command error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "crewfold help") {
		t.Fatalf("stderr = %q, want help hint", stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "goroutine") {
		t.Fatalf("stderr = %q, must not contain a stack trace", stderr.String())
	}
}

func TestUnknownCommandHasStructuredJSONError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"--output=json", "nope"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Schema != errorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, errorSchema)
	}
	if response.Error.Code != "unknown_command" {
		t.Fatalf("response.Error.Code = %q, want unknown_command", response.Error.Code)
	}
}

func TestInvalidOutputAndCommandArgumentsAreUsageErrors(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"invalid output":      {"version", "--output", "yaml"},
		"duplicate output":    {"--output", "json", "version", "--output=json"},
		"version argument":    {"version", "extra"},
		"doctor missing self": {"doctor"},
		"too much help":       {"help", "version", "doctor"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestHelpTopics(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{"version", "doctor", "daemon", "status", "workspace", "project", "checkout", "agent", "objective", "task", "events", "help"} {
		t.Run(topic, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run([]string{"help", topic}); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("stdout = %q, want usage", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestProjectAndCheckoutCommandsUseExplicitScopeAndStructuredResults(t *testing.T) {
	t.Parallel()

	project := domain.Project{ID: "prj_1234", WorkspaceID: "ws_1234", Name: "world-engine", Revision: 1}
	repository := domain.Repository{ID: "repo_1234", WorkspaceID: "ws_1234", Fingerprint: "git_1234", Revision: 1}
	checkout := domain.Checkout{ID: "co_1234", ProjectID: project.ID, RepositoryID: repository.ID, Path: "/work/world-engine", WriteMode: "exclusive", CheckoutKind: "standalone", Availability: "available", Revision: 1}
	for name, test := range map[string]struct {
		args       []string
		wantSchema string
		configure  func(*fakeDaemonClient)
		assert     func(*testing.T, *fakeDaemonClient)
	}{
		"project add": {
			args:       []string{"project", "add", "world-engine", "--workspace", "personal", "--repo", "/work/world-engine", "--mode", "claimed", "--socket", "/tmp/sources.sock", "--idempotency-key", "project-key", "--output=json"},
			wantSchema: localapi.ProjectAddSchema,
			configure: func(client *fakeDaemonClient) {
				client.projectAdd = localapi.ProjectAddResult{Schema: localapi.ProjectAddSchema, Type: "project_registered", Project: project, Repository: repository, Checkout: checkout}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				want := []string{"personal", "world-engine", "/work/world-engine", "claimed", "project-key"}
				if strings.Join(client.projectArgs, "|") != strings.Join(want, "|") {
					t.Fatalf("ProjectAdd args = %q, want %q", client.projectArgs, want)
				}
			},
		},
		"project inspect": {
			args:       []string{"project", "inspect", "world-engine", "--workspace", "personal", "--socket", "/tmp/sources.sock", "--output=json"},
			wantSchema: localapi.ProjectInspectSchema,
			configure: func(client *fakeDaemonClient) {
				client.projectInspect = localapi.ProjectInspectResult{Schema: localapi.ProjectInspectSchema, Type: "project_inspection", Project: project, Repositories: []domain.Repository{repository}, Checkouts: []domain.Checkout{checkout}}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if strings.Join(client.projectArgs, "|") != "personal|world-engine" {
					t.Fatalf("ProjectInspect args = %q", client.projectArgs)
				}
			},
		},
		"checkout add adjacent clone": {
			args:       []string{"checkout", "add", "world-engine", "/work/world-engine-2", "--workspace", "personal", "--mode", "exclusive", "--socket", "/tmp/sources.sock", "--idempotency-key", "checkout-key", "--output=json"},
			wantSchema: localapi.CheckoutAddSchema,
			configure: func(client *fakeDaemonClient) {
				client.checkoutAdd = localapi.CheckoutAddResult{Schema: localapi.CheckoutAddSchema, Type: "checkout_registered", Repository: repository, Checkout: checkout}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				want := []string{"personal", "world-engine", "/work/world-engine-2", "exclusive", "checkout-key"}
				if strings.Join(client.checkoutArgs, "|") != strings.Join(want, "|") {
					t.Fatalf("CheckoutAdd args = %q, want %q", client.checkoutArgs, want)
				}
			},
		},
		"checkout list": {
			args:       []string{"checkout", "list", "world-engine", "--workspace", "personal", "--socket", "/tmp/sources.sock", "--output=json"},
			wantSchema: localapi.CheckoutListSchema,
			configure: func(client *fakeDaemonClient) {
				client.checkoutList = localapi.CheckoutListResult{Schema: localapi.CheckoutListSchema, Type: "checkout_list", Project: project, Checkouts: []domain.Checkout{checkout}}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if strings.Join(client.checkoutArgs, "|") != "personal|world-engine" {
					t.Fatalf("CheckoutList args = %q", client.checkoutArgs)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{}
			test.configure(client)
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/sources.sock" {
					t.Fatalf("socketPath = %q, want /tmp/sources.sock", socketPath)
				}
				return client
			}
			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			var envelope struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if envelope.Schema != test.wantSchema {
				t.Fatalf("schema = %q, want %q", envelope.Schema, test.wantSchema)
			}
			test.assert(t, client)
		})
	}
}

func TestProjectAndCheckoutCommandsRejectMissingScopeAndPaths(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"project subcommand":      {"project"},
		"project repository":      {"project", "add", "demo", "--workspace", "personal", "--socket", "/tmp/socket"},
		"project workspace":       {"project", "inspect", "demo", "--socket", "/tmp/socket"},
		"checkout subcommand":     {"checkout"},
		"checkout path":           {"checkout", "add", "demo", "--workspace", "personal", "--socket", "/tmp/socket"},
		"checkout list workspace": {"checkout", "list", "demo", "--socket", "/tmp/socket"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestCoordinationCommandsPreserveScopeBudgetsLeasesAndRevisions(t *testing.T) {
	t.Parallel()

	client := &fakeDaemonClient{
		agentMutation:     localapi.AgentMutationResult{Schema: localapi.AgentMutationSchema, Type: "agent_mutation"},
		objectiveMutation: localapi.ObjectiveMutationResult{Schema: localapi.ObjectiveMutationSchema, Type: "objective_mutation"},
		taskMutation:      localapi.TaskMutationResult{Schema: localapi.TaskMutationSchema, Type: "task_mutation"},
		coordination:      localapi.CoordinationStatusResult{Schema: localapi.CoordinationStatusSchema, Type: "coordination_status", Workspace: "personal"},
	}
	run := func(args []string, wantSchema string) {
		t.Helper()
		app, stdout, stderr := newTestApp()
		app.newClient = func(socketPath string) daemonClient {
			if socketPath != "/tmp/coordination.sock" {
				t.Fatalf("socketPath = %q, want /tmp/coordination.sock", socketPath)
			}
			return client
		}
		if exitCode := app.Run(args); exitCode != ExitOK {
			t.Fatalf("Run(%q) exit code = %d; stderr = %q", args, exitCode, stderr.String())
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v; stdout = %q", args, err, stdout.String())
		}
		if envelope.Schema != wantSchema {
			t.Fatalf("Run(%q) schema = %q, want %q", args, envelope.Schema, wantSchema)
		}
	}

	run([]string{"agent", "create", "implementer", "--workspace", "personal", "--role", "implementer", "--provider", "codex", "--runtime", "herdr", "--max-concurrency", "2", "--socket", "/tmp/coordination.sock", "--idempotency-key", "agent-key", "--output=json"}, localapi.AgentMutationSchema)
	if params := client.agentCreateParams; params.Workspace != "personal" || params.Name != "implementer" || params.Provider != "codex" || params.Runtime != "herdr" || params.MaxConcurrency != 2 || params.IdempotencyKey != "agent-key" {
		t.Fatalf("AgentCreate params = %#v", params)
	}

	run([]string{"objective", "create", "Ship greeting", "--workspace", "personal", "--project", "demo", "--budget-tokens", "20000", "--budget-cents", "500", "--budget-seconds", "3600", "--socket", "/tmp/coordination.sock", "--idempotency-key", "objective-key", "--output=json"}, localapi.ObjectiveMutationSchema)
	if params := client.objectiveCreateParams; params.Workspace != "personal" || params.Project != "demo" || params.Budget != (domain.Budget{TokenLimit: 20000, CostCents: 500, TimeSeconds: 3600}) {
		t.Fatalf("ObjectiveCreate params = %#v", params)
	}

	run([]string{"task", "create", "--workspace", "personal", "--project", "demo", "--objective", "obj_1234", "--title", "Implement greeting", "--description", "deterministic", "--priority", "200", "--budget-tokens", "5000", "--socket", "/tmp/coordination.sock", "--idempotency-key", "task-key", "--output=json"}, localapi.TaskMutationSchema)
	if params := client.taskCreateParams; params.Workspace != "personal" || params.Project != "demo" || params.Objective != "obj_1234" || params.Priority != 200 || params.Budget.TokenLimit != 5000 {
		t.Fatalf("TaskCreate params = %#v", params)
	}

	run([]string{"task", "assign", "task_1234", "implementer", "--workspace", "personal", "--lease-seconds", "300", "--expected-revision", "7", "--socket", "/tmp/coordination.sock", "--idempotency-key", "assign-key", "--output=json"}, localapi.TaskMutationSchema)
	if params := client.taskAssignParams; params.Task != "task_1234" || params.Agent != "implementer" || params.LeaseSeconds != 300 || params.ExpectedRevision != 7 {
		t.Fatalf("TaskAssign params = %#v", params)
	}

	run([]string{"status", "--workspace", "personal", "--socket", "/tmp/coordination.sock", "--output=json"}, localapi.CoordinationStatusSchema)
	if client.coordinationWorkspace != "personal" {
		t.Fatalf("CoordinationStatus workspace = %q, want personal", client.coordinationWorkspace)
	}
}

func TestCoordinationCommandsRejectAmbiguousBudgetReplacementAndInvalidConcurrency(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"zero concurrency":         {"agent", "create", "implementer", "--workspace", "personal", "--role", "implementer", "--provider", "fake", "--max-concurrency", "0", "--socket", "/tmp/socket"},
		"partial objective budget": {"objective", "update", "obj_1234", "--workspace", "personal", "--budget-tokens", "10", "--expected-revision", "1", "--socket", "/tmp/socket"},
		"partial task budget":      {"task", "update", "task_1234", "--workspace", "personal", "--budget-cents", "10", "--expected-revision", "1", "--socket", "/tmp/socket"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestWorkspaceCommandsUseExplicitSocketAndStructuredResults(t *testing.T) {
	t.Parallel()

	workspace := domain.Workspace{
		ID:        "ws_1234",
		Name:      "personal",
		Revision:  1,
		CreatedAt: "2026-08-12T01:00:00Z",
		UpdatedAt: "2026-08-12T01:00:00Z",
		CreatedBy: "local-owner",
		UpdatedBy: "local-owner",
	}
	for name, test := range map[string]struct {
		args       []string
		wantSchema string
		configure  func(*fakeDaemonClient)
		assert     func(*testing.T, *fakeDaemonClient)
	}{
		"database doctor": {
			args:       []string{"doctor", "--database", "--socket", "/tmp/workspace.sock", "--output=json"},
			wantSchema: localapi.DatabaseStatusSchema,
			configure: func(client *fakeDaemonClient) {
				client.databaseStatus = localapi.DatabaseStatusResult{
					Schema:              localapi.DatabaseStatusSchema,
					Type:                "database_status",
					Status:              "ok",
					SchemaVersion:       1,
					LatestSchemaVersion: 1,
					JournalMode:         "wal",
					ForeignKeys:         true,
					IntegrityCheck:      "ok",
				}
			},
		},
		"workspace init": {
			args:       []string{"workspace", "init", "personal", "--socket", "/tmp/workspace.sock", "--idempotency-key", "init-personal", "--output=json"},
			wantSchema: localapi.WorkspaceInitSchema,
			configure: func(client *fakeDaemonClient) {
				client.workspaceInit = localapi.WorkspaceInitResult{
					Schema:        localapi.WorkspaceInitSchema,
					Type:          "workspace_initialized",
					Workspace:     workspace,
					EventID:       "evt_1234",
					EventSequence: 1,
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.initName != "personal" || client.initKey != "init-personal" {
					t.Fatalf("WorkspaceInit args = %q, %q", client.initName, client.initKey)
				}
			},
		},
		"workspace show": {
			args:       []string{"workspace", "show", "personal", "--socket=/tmp/workspace.sock", "--output=json"},
			wantSchema: localapi.WorkspaceShowSchema,
			configure: func(client *fakeDaemonClient) {
				client.workspaceShow = localapi.WorkspaceShowResult{
					Schema:    localapi.WorkspaceShowSchema,
					Type:      "workspace",
					Workspace: workspace,
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.showIdentifier != "personal" {
					t.Fatalf("WorkspaceShow identifier = %q", client.showIdentifier)
				}
			},
		},
		"events list": {
			args:       []string{"events", "list", "--socket", "/tmp/workspace.sock", "--after", "7", "--limit", "25", "--output=json"},
			wantSchema: localapi.EventsListSchema,
			configure: func(client *fakeDaemonClient) {
				client.eventsList = localapi.EventsListResult{
					Schema:    localapi.EventsListSchema,
					Type:      "event_list",
					After:     7,
					NextAfter: 7,
					Events:    []domain.Event{},
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.eventsAfter != 7 || client.eventsLimit != 25 {
					t.Fatalf("EventsList args = %d, %d", client.eventsAfter, client.eventsLimit)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{}
			if test.configure != nil {
				test.configure(client)
			}
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/workspace.sock" {
					t.Fatalf("socketPath = %q, want /tmp/workspace.sock", socketPath)
				}
				return client
			}
			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			var response struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != test.wantSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, test.wantSchema)
			}
			if test.assert != nil {
				test.assert(t, client)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestWorkspaceCommandsRejectInvalidArguments(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"workspace subcommand":  {"workspace"},
		"workspace name":        {"workspace", "init", "--socket", "/tmp/socket"},
		"workspace socket":      {"workspace", "show", "personal"},
		"events subcommand":     {"events"},
		"events after missing":  {"events", "list", "--socket", "/tmp/socket"},
		"events after negative": {"events", "list", "--socket", "/tmp/socket", "--after", "-1"},
		"events limit zero":     {"events", "list", "--socket", "/tmp/socket", "--after", "0", "--limit", "0"},
		"database socket":       {"doctor", "--database"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestDaemonRunPassesRequiredConfiguration(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	var received daemon.Config
	app.runDaemon = func(_ context.Context, config daemon.Config) error {
		received = config
		return nil
	}

	exitCode := app.Run([]string{
		"daemon", "run",
		"--data-dir", "/tmp/crewfold-test-data",
		"--socket=/tmp/crewfold-test.sock",
	})
	if exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
	}
	if received.DataDir != "/tmp/crewfold-test-data" {
		t.Fatalf("received.DataDir = %q", received.DataDir)
	}
	if received.SocketPath != "/tmp/crewfold-test.sock" {
		t.Fatalf("received.SocketPath = %q", received.SocketPath)
	}
	if received.Logger == nil {
		t.Fatal("received.Logger = nil")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want empty", stdout.String(), stderr.String())
	}
}

func TestDaemonRunReportsStableStartupError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.runDaemon = func(context.Context, daemon.Config) error {
		return &daemon.StartupError{
			Code:    daemon.CodeSocketInUse,
			Message: "socket is served by a live process",
		}
	}

	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket", "--output=json",
	})
	if exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Error.Code != daemon.CodeSocketInUse {
		t.Fatalf("response.Error.Code = %q, want %q", response.Error.Code, daemon.CodeSocketInUse)
	}
}

func TestStatusAndStopUseExplicitSocket(t *testing.T) {
	t.Parallel()

	status := localapi.StatusResult{
		Schema:        localapi.StatusSchema,
		Type:          "system_status",
		Status:        "ok",
		Protocol:      1,
		PID:           42,
		StartedAt:     time.Unix(1, 0).UTC().Format(time.RFC3339),
		UptimeMillis:  12,
		ServerVersion: buildinfo.Current(),
	}
	client := &fakeDaemonClient{
		status: status,
		stop: localapi.StopResult{
			Schema: localapi.StopSchema,
			Type:   "stop_acknowledgement",
			Status: "stopping",
		},
	}

	for name, test := range map[string]struct {
		args       []string
		wantSchema string
	}{
		"status": {[]string{"status", "--socket", "/tmp/test.sock", "--output=json"}, localapi.StatusSchema},
		"stop":   {[]string{"daemon", "stop", "--socket=/tmp/test.sock", "--output", "json"}, localapi.StopSchema},
	} {
		t.Run(name, func(t *testing.T) {
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/test.sock" {
					t.Fatalf("socketPath = %q, want /tmp/test.sock", socketPath)
				}
				return client
			}

			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
			}
			var response struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != test.wantSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, test.wantSchema)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDaemonCommandsRejectMissingAndUnknownOptions(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"daemon subcommand": {"daemon"},
		"run data dir":      {"daemon", "run", "--socket", "/tmp/socket"},
		"run socket":        {"daemon", "run", "--data-dir", "/tmp/data"},
		"status socket":     {"status"},
		"stop socket":       {"daemon", "stop"},
		"unknown option":    {"status", "--bogus", "value"},
		"duplicate option":  {"status", "--socket", "one", "--socket", "two"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

type fakeDaemonClient struct {
	status                localapi.StatusResult
	statusErr             error
	stop                  localapi.StopResult
	stopErr               error
	databaseStatus        localapi.DatabaseStatusResult
	databaseStatusErr     error
	workspaceInit         localapi.WorkspaceInitResult
	workspaceInitErr      error
	workspaceShow         localapi.WorkspaceShowResult
	workspaceShowErr      error
	projectAdd            localapi.ProjectAddResult
	projectAddErr         error
	projectInspect        localapi.ProjectInspectResult
	projectInspectErr     error
	checkoutAdd           localapi.CheckoutAddResult
	checkoutAddErr        error
	checkoutList          localapi.CheckoutListResult
	checkoutListErr       error
	eventsList            localapi.EventsListResult
	eventsListErr         error
	agentMutation         localapi.AgentMutationResult
	agentShow             localapi.AgentShowResult
	agentList             localapi.AgentListResult
	objectiveMutation     localapi.ObjectiveMutationResult
	objectiveShow         localapi.ObjectiveShowResult
	objectiveList         localapi.ObjectiveListResult
	taskMutation          localapi.TaskMutationResult
	taskShow              localapi.TaskShowResult
	taskList              localapi.TaskListResult
	coordination          localapi.CoordinationStatusResult
	agentCreateParams     localapi.AgentCreateParams
	objectiveCreateParams localapi.ObjectiveCreateParams
	taskCreateParams      localapi.TaskCreateParams
	taskAssignParams      localapi.TaskAssignParams
	coordinationWorkspace string
	initName              string
	initKey               string
	showIdentifier        string
	projectArgs           []string
	checkoutArgs          []string
	eventsAfter           int64
	eventsLimit           int
}

func (client *fakeDaemonClient) Status(context.Context) (localapi.StatusResult, error) {
	return client.status, client.statusErr
}

func (client *fakeDaemonClient) Stop(context.Context) (localapi.StopResult, error) {
	return client.stop, client.stopErr
}

func (client *fakeDaemonClient) DatabaseStatus(context.Context) (localapi.DatabaseStatusResult, error) {
	return client.databaseStatus, client.databaseStatusErr
}

func (client *fakeDaemonClient) WorkspaceInit(_ context.Context, name, key string) (localapi.WorkspaceInitResult, error) {
	client.initName = name
	client.initKey = key
	return client.workspaceInit, client.workspaceInitErr
}

func (client *fakeDaemonClient) WorkspaceShow(_ context.Context, identifier string) (localapi.WorkspaceShowResult, error) {
	client.showIdentifier = identifier
	return client.workspaceShow, client.workspaceShowErr
}

func (client *fakeDaemonClient) ProjectAdd(_ context.Context, workspace, name, path, mode, key string) (localapi.ProjectAddResult, error) {
	client.projectArgs = []string{workspace, name, path, mode, key}
	return client.projectAdd, client.projectAddErr
}

func (client *fakeDaemonClient) ProjectInspect(_ context.Context, workspace, project string) (localapi.ProjectInspectResult, error) {
	client.projectArgs = []string{workspace, project}
	return client.projectInspect, client.projectInspectErr
}

func (client *fakeDaemonClient) CheckoutAdd(_ context.Context, workspace, project, path, mode, key string) (localapi.CheckoutAddResult, error) {
	client.checkoutArgs = []string{workspace, project, path, mode, key}
	return client.checkoutAdd, client.checkoutAddErr
}

func (client *fakeDaemonClient) CheckoutList(_ context.Context, workspace, project string) (localapi.CheckoutListResult, error) {
	client.checkoutArgs = []string{workspace, project}
	return client.checkoutList, client.checkoutListErr
}

func (client *fakeDaemonClient) AgentCreate(_ context.Context, params localapi.AgentCreateParams) (localapi.AgentMutationResult, error) {
	client.agentCreateParams = params
	return client.agentMutation, nil
}

func (client *fakeDaemonClient) AgentUpdate(context.Context, localapi.AgentUpdateParams) (localapi.AgentMutationResult, error) {
	return client.agentMutation, nil
}

func (client *fakeDaemonClient) AgentShow(context.Context, string, string) (localapi.AgentShowResult, error) {
	return client.agentShow, nil
}

func (client *fakeDaemonClient) AgentList(context.Context, string) (localapi.AgentListResult, error) {
	return client.agentList, nil
}

func (client *fakeDaemonClient) ObjectiveCreate(_ context.Context, params localapi.ObjectiveCreateParams) (localapi.ObjectiveMutationResult, error) {
	client.objectiveCreateParams = params
	return client.objectiveMutation, nil
}

func (client *fakeDaemonClient) ObjectiveUpdate(context.Context, localapi.ObjectiveUpdateParams) (localapi.ObjectiveMutationResult, error) {
	return client.objectiveMutation, nil
}

func (client *fakeDaemonClient) ObjectiveShow(context.Context, string, string) (localapi.ObjectiveShowResult, error) {
	return client.objectiveShow, nil
}

func (client *fakeDaemonClient) ObjectiveList(context.Context, string, string) (localapi.ObjectiveListResult, error) {
	return client.objectiveList, nil
}

func (client *fakeDaemonClient) TaskCreate(_ context.Context, params localapi.TaskCreateParams) (localapi.TaskMutationResult, error) {
	client.taskCreateParams = params
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskUpdate(context.Context, localapi.TaskUpdateParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskShow(context.Context, string, string) (localapi.TaskShowResult, error) {
	return client.taskShow, nil
}

func (client *fakeDaemonClient) TaskList(context.Context, string, string, bool) (localapi.TaskListResult, error) {
	return client.taskList, nil
}

func (client *fakeDaemonClient) TaskDepend(context.Context, localapi.TaskDependencyParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskAssign(_ context.Context, params localapi.TaskAssignParams) (localapi.TaskMutationResult, error) {
	client.taskAssignParams = params
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskTransition(context.Context, localapi.TaskTransitionParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) CoordinationStatus(_ context.Context, workspace string) (localapi.CoordinationStatusResult, error) {
	client.coordinationWorkspace = workspace
	return client.coordination, nil
}

func (client *fakeDaemonClient) EventsList(_ context.Context, after int64, limit int) (localapi.EventsListResult, error) {
	client.eventsAfter = after
	client.eventsLimit = limit
	return client.eventsList, client.eventsListErr
}

func newTestApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return New(stdout, stderr, buildinfo.Current()), stdout, stderr
}
