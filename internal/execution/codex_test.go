package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type recordedCodexRunner struct {
	results map[string]CodexCommandResult
	errors  map[string]error
	calls   []string
	env     []map[string]string
}

func (runner *recordedCodexRunner) Run(_ context.Context, _ string, arguments []string, environment map[string]string) (CodexCommandResult, error) {
	key := strings.Join(arguments, " ")
	runner.calls = append(runner.calls, key)
	copyEnvironment := make(map[string]string, len(environment))
	for name, value := range environment {
		copyEnvironment[name] = value
	}
	runner.env = append(runner.env, copyEnvironment)
	return runner.results[key], runner.errors[key]
}

func TestCodexProbeAcceptsRecordedCapabilitiesAndAuthentication(t *testing.T) {
	t.Parallel()

	runner := compatibleCodexRunner(t)
	report := NewCodexProbe("/opt/codex", "/private/codex", runner).Run(context.Background())
	if !report.Compatible() || report.Version != "codex-cli 1.2.3" || len(report.Checks) != 3 || len(report.Capabilities) != 3 {
		t.Fatalf("Run() = %#v", report)
	}
	if len(runner.calls) != 4 || runner.env[3]["CODEX_HOME"] != "/private/codex" {
		t.Fatalf("calls = %#v, env = %#v", runner.calls, runner.env)
	}
}

func TestCodexProbeRejectsAnUnavailableWorkspaceSandbox(t *testing.T) {
	t.Parallel()

	runner := compatibleCodexRunner(t)
	runner.results["sandbox -- /bin/sh -c exit 0"] = CodexCommandResult{Stderr: []byte("bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted\n"), ExitCode: 1}
	report := NewCodexProbe("/opt/codex", "/private/codex", runner).Run(context.Background())
	if report.Compatible() || len(report.Checks) != 2 || report.Checks[1].Name != "capabilities" {
		t.Fatalf("Run() = %#v", report)
	}
	if err := report.Error(); err == nil || !strings.Contains(err.Error(), "workspace sandbox is unavailable") || !strings.Contains(err.Error(), "AppArmor") {
		t.Fatalf("Run().Error() = %v", err)
	}
	for _, call := range runner.calls {
		if call == "login status" {
			t.Fatalf("authentication was probed after sandbox failure: %#v", runner.calls)
		}
	}
}

func TestCodexProbeDiagnosesAuthenticationBoundary(t *testing.T) {
	t.Parallel()

	runner := compatibleCodexRunner(t)
	runner.results["login status"] = CodexCommandResult{Stderr: []byte("Not logged in\n"), ExitCode: 1}
	report := NewCodexProbe("/opt/codex", "/private/codex", runner).Run(context.Background())
	if report.Compatible() || len(report.Checks) != 3 || report.Checks[2].Name != "authentication" {
		t.Fatalf("Run() = %#v", report)
	}
	if err := report.Error(); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("Run().Error() = %v", err)
	}
}

func TestCodexProviderBuildsIsolatedRequiredMCPLaunch(t *testing.T) {
	t.Parallel()

	preparer := &recordedCapabilityPreparer{access: RunCapabilityAccess{SocketPath: "/tmp/crewfold.sock", CapabilityFile: "/tmp/run.token"}}
	provider := NewCodexProvider(CodexProviderOptions{
		CapabilityPreparer: preparer, CodexExecutable: "/opt/codex", CrewfoldExecutable: "/opt/crewfold",
		CodexHome: "/private/codex", ProbeRunner: compatibleCodexRunner(t),
	})
	run := domain.Run{ID: "run_11111111111111111111111111111111", Placement: domain.RunPlacement{CheckoutPath: "/work/project"}}
	scenario := codexScenario()
	launch, err := provider.Prepare(context.Background(), run, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if preparer.runID != run.ID || launch.Command == nil || launch.Command.Executable != "/opt/codex" {
		t.Fatalf("Prepare() = %#v, preparer = %#v", launch, preparer)
	}
	joined := strings.Join(launch.Command.Arguments, "\n")
	for _, wanted := range []string{
		"exec", "--json", "--ephemeral", "--ignore-user-config", "workspace-write", `approval_policy="never"`,
		`sandbox_workspace_write.network_access=false`,
		`mcp_servers.crewfold.command="/opt/crewfold"`, `mcp_servers.crewfold.args=["__mcp-stdio-bridge"]`,
		`mcp_servers.crewfold.required=true`, "implementation_complete, tests_passed", "/work/project",
	} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("launch arguments omit %q: %#v", wanted, launch.Command.Arguments)
		}
	}
	if launch.Command.Environment["CREWFOLD_MCP_SOCKET"] != "/tmp/crewfold.sock" || launch.Command.Environment["CODEX_HOME"] != "/private/codex" {
		t.Fatalf("environment = %#v", launch.Command.Environment)
	}
	if strings.Contains(joined, "cf1.") || strings.Contains(strings.Join(mapValues(launch.Command.Environment), "\n"), "cf1.") {
		t.Fatal("launch manifest exposed the run capability token")
	}
}

func TestCodexProviderCanEnableWorkspaceToolNetwork(t *testing.T) {
	t.Parallel()

	run := domain.Run{Placement: domain.RunPlacement{CheckoutPath: "/work/project"}}
	arguments := codexLaunchArguments("/opt/crewfold", run, codexScenario(), CodexSandboxWorkspaceWrite, true)
	if !strings.Contains(strings.Join(arguments, "\n"), `sandbox_workspace_write.network_access=true`) {
		t.Fatalf("codexLaunchArguments() = %#v", arguments)
	}
}

func TestCodexProviderSupportsAnExplicitExternalSandboxMode(t *testing.T) {
	t.Parallel()

	run := domain.Run{Placement: domain.RunPlacement{CheckoutPath: "/work/project"}}
	arguments := codexLaunchArguments("/opt/crewfold", run, codexScenario(), CodexSandboxDangerFullAccess, false)
	joined := strings.Join(arguments, "\n")
	if !strings.Contains(joined, CodexSandboxDangerFullAccess) || strings.Contains(joined, "sandbox_workspace_write.network_access") {
		t.Fatalf("codexLaunchArguments() = %#v", arguments)
	}
	if err := ValidateCodexSandboxMode("unbounded"); err == nil {
		t.Fatal("ValidateCodexSandboxMode(unbounded) error = nil")
	}
}

func TestCodexProviderRefusesFullAccessWithoutExternalSandbox(t *testing.T) {
	t.Parallel()

	provider := NewCodexProvider(CodexProviderOptions{
		CapabilityPreparer: &recordedCapabilityPreparer{},
		CodexExecutable:    "/opt/codex", CrewfoldExecutable: "/opt/crewfold",
		SandboxMode: CodexSandboxDangerFullAccess, ProbeRunner: compatibleCodexRunner(t),
	})
	_, err := provider.Prepare(context.Background(), domain.Run{}, codexScenario())
	if err == nil || !strings.Contains(err.Error(), "independently enforced external sandbox") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestCodexProviderRefusesMissingMCPAccessBeforeLaunch(t *testing.T) {
	t.Parallel()

	provider := NewCodexProvider(CodexProviderOptions{
		CapabilityPreparer: &recordedCapabilityPreparer{}, CodexExecutable: "/opt/codex",
		CrewfoldExecutable: "/opt/crewfold", ProbeRunner: compatibleCodexRunner(t),
	})
	_, err := provider.Prepare(context.Background(), domain.Run{ID: "run_22222222222222222222222222222222"}, codexScenario())
	if err == nil || !strings.Contains(err.Error(), "socket or capability file is missing") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestCodexProviderNormalizesOnlyExplicitBoundaries(t *testing.T) {
	t.Parallel()

	provider := CodexProvider{}
	blocked, found, err := provider.Next(context.Background(), domain.Run{}, codexScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, Stdout: domain.CapturedLog{Text: string(codexFixture(t, "approval-blocked.jsonl"))},
	})
	if err != nil || !found || blocked.Kind != domain.ObservationBlocked || !strings.Contains(blocked.Message, "approval required") {
		t.Fatalf("Next(blocked) = %#v, %t, %v", blocked, found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, codexScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, Stderr: domain.CapturedLog{Text: string(codexFixture(t, "mcp-startup-failed.jsonl"))},
	})
	if found || err == nil || !strings.Contains(err.Error(), "Codex MCP boundary failed") {
		t.Fatalf("Next(MCP failure) found=%t error=%v", found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, codexScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, ExitKnown: true, ExitCode: 0, Stdout: domain.CapturedLog{Text: `{"type":"turn.completed"}`},
	})
	if found || err != nil {
		t.Fatalf("Next(unreported success) found=%t error=%v", found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, codexScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, ExitKnown: true, ExitCode: 0, Stdout: domain.CapturedLog{Text: strings.Join([]string{
			`{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"The project excludes authentication and the earlier check failed."}}`,
			`{"type":"item.completed","item":{"id":"command","type":"command_execution","command":"rg files","aggregated_output":"rg: command not found","exit_code":127,"status":"failed"}}`,
			`{"type":"turn.completed"}`,
		}, "\n")},
	})
	if found || err != nil {
		t.Fatalf("Next(project text and command failure) found=%t error=%v; want no fabricated provider boundary", found, err)
	}
}

func TestMCPStdioBridgeInjectsCapabilityWithoutWritingItToStdio(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "crewfold.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	capabilityPath := filepath.Join(t.TempDir(), "run.token")
	token := "cf1.run_33333333333333333333333333333333.secret"
	if err := os.WriteFile(capabilityPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWFOLD_MCP_SOCKET", socketPath)
	t.Setenv("CREWFOLD_MCP_CAPABILITY_FILE", capabilityPath)

	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer connection.Close()
		scanner := bufio.NewScanner(connection)
		encoder := json.NewEncoder(connection)
		for index := 0; index < 2; index++ {
			if !scanner.Scan() {
				serverResult <- scanner.Err()
				return
			}
			var request map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				serverResult <- err
				return
			}
			params, _ := request["params"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			if meta[mcp.CapabilityMeta] != token {
				serverResult <- errorsNew("bridge omitted the scoped capability")
				return
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{}}); err != nil {
				serverResult <- err
				return
			}
		}
		serverResult <- nil
	}()

	input := strings.NewReader("" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	var output, diagnostics bytes.Buffer
	if exit := RunMCPStdioBridge(input, &output, &diagnostics); exit != 0 {
		t.Fatalf("RunMCPStdioBridge() exit=%d diagnostics=%q", exit, diagnostics.String())
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), token) || strings.Contains(diagnostics.String(), token) || strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("output=%q diagnostics=%q", output.String(), diagnostics.String())
	}
}

type recordedCapabilityPreparer struct {
	access RunCapabilityAccess
	err    error
	runID  string
}

func (preparer *recordedCapabilityPreparer) PrepareRunCapability(_ context.Context, runID string) (RunCapabilityAccess, error) {
	preparer.runID = runID
	return preparer.access, preparer.err
}

func compatibleCodexRunner(t *testing.T) *recordedCodexRunner {
	t.Helper()
	return &recordedCodexRunner{results: map[string]CodexCommandResult{
		"--version":                    {Stdout: codexFixture(t, "version.txt")},
		"exec --help":                  {Stdout: codexFixture(t, "exec-help.txt")},
		"sandbox -- /bin/sh -c exit 0": {},
		"login status":                 {Stdout: codexFixture(t, "login-status.txt")},
	}}
}

func codexFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "providers", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func codexScenario() domain.FakeScenario {
	return domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "codex-canary", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"implementation_complete", "tests_passed"}},
		Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "provider report authority", Handoff: "provider report authority"}},
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func errorsNew(value string) error { return &bridgeTestError{value: value} }

type bridgeTestError struct{ value string }

func (err *bridgeTestError) Error() string { return err.value }
