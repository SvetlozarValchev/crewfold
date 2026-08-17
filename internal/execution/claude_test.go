package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

type recordedClaudeRunner struct {
	results map[string]ProviderCommandResult
	calls   []string
	env     []map[string]string
}

func (runner *recordedClaudeRunner) Run(_ context.Context, _ string, arguments []string, environment map[string]string) (ProviderCommandResult, error) {
	key := strings.Join(arguments, " ")
	runner.calls = append(runner.calls, key)
	copyEnvironment := make(map[string]string, len(environment))
	for name, value := range environment {
		copyEnvironment[name] = value
	}
	runner.env = append(runner.env, copyEnvironment)
	return runner.results[key], nil
}

func TestClaudeProbeAcceptsSupportedAuthenticatedCLI(t *testing.T) {
	t.Parallel()

	runner := compatibleClaudeRunner()
	report := NewClaudeProbe("/opt/claude", "/private/claude", runner).Run(context.Background())
	if !report.Compatible() || report.Version != "2.1.220 (Claude Code)" || len(report.Checks) != 4 || len(report.Capabilities) != 3 {
		t.Fatalf("Run() = %#v", report)
	}
	if len(runner.calls) != 3 || runner.env[2]["CLAUDE_CONFIG_DIR"] != "/private/claude" {
		t.Fatalf("calls = %#v, env = %#v", runner.calls, runner.env)
	}
	if strings.Contains(report.Checks[3].Detail, "owner@example") || !strings.Contains(report.Checks[3].Detail, "oauth") {
		t.Fatalf("authentication detail = %q", report.Checks[3].Detail)
	}
}

func TestClaudeProbeRefusesUnsupportedMajorAndMissingAuthentication(t *testing.T) {
	t.Parallel()

	unsupported := compatibleClaudeRunner()
	unsupported.results["--version"] = ProviderCommandResult{Stdout: []byte("3.0.0 (Claude Code)\n")}
	report := NewClaudeProbe("/opt/claude", "", unsupported).Run(context.Background())
	if report.Compatible() || len(report.Checks) != 2 || report.Checks[1].Name != "compatibility" {
		t.Fatalf("unsupported Run() = %#v", report)
	}

	missing := compatibleClaudeRunner()
	missing.results["auth status --json"] = ProviderCommandResult{Stdout: []byte(`{"loggedIn":false}`)}
	report = NewClaudeProbe("/opt/claude", "", missing).Run(context.Background())
	if report.Compatible() || report.Checks[len(report.Checks)-1].Name != "authentication" {
		t.Fatalf("missing auth Run() = %#v", report)
	}
}

func TestClaudeProviderBuildsStrictScopedLaunch(t *testing.T) {
	t.Parallel()

	preparer := &recordedCapabilityPreparer{access: RunCapabilityAccess{SocketPath: "/tmp/crewfold.sock", CapabilityFile: "/tmp/run.token"}}
	provider := NewClaudeProvider(ClaudeProviderOptions{
		CapabilityPreparer: preparer, ClaudeExecutable: "/opt/claude", CrewfoldExecutable: "/opt/crewfold",
		ClaudeConfigDir: "/private/claude", MaxBudgetUSD: "2.5", ProbeRunner: compatibleClaudeRunner(),
	})
	run := domain.Run{ID: "run_33333333333333333333333333333333", Placement: domain.RunPlacement{CheckoutPath: "/work/project"}}
	launch, err := provider.Prepare(context.Background(), run, claudeScenario())
	if err != nil {
		t.Fatal(err)
	}
	if preparer.runID != run.ID || launch.Command == nil || launch.Command.Executable != "/opt/claude" {
		t.Fatalf("Prepare() = %#v, preparer = %#v", launch, preparer)
	}
	joined := strings.Join(launch.Command.Arguments, "\n")
	for _, wanted := range []string{
		"-p", "stream-json", "--strict-mcp-config", "--no-session-persistence", "dontAsk",
		"Read,Edit,Bash", "Read(/**),Edit(/**)", "Bash(git diff --check)", "mcp__crewfold__*", "2.50", "implementation_complete, tests_passed",
	} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("launch arguments omit %q: %#v", wanted, launch.Command.Arguments)
		}
	}
	if launch.Command.Environment["CLAUDE_CONFIG_DIR"] != "/private/claude" || launch.Command.Environment["CREWFOLD_MCP_SOCKET"] != "/tmp/crewfold.sock" || launch.Command.Environment["CREWFOLD_MCP_CAPABILITY_FILE"] != "/tmp/run.token" {
		t.Fatalf("environment = %#v", launch.Command.Environment)
	}
	if strings.Contains(joined, "cf1.") || strings.Contains(strings.Join(mapValues(launch.Command.Environment), "\n"), "cf1.") {
		t.Fatal("launch manifest exposed the run capability token")
	}

	mcpValue := argumentAfter(t, launch.Command.Arguments, "--mcp-config")
	var mcpConfig struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpValue), &mcpConfig); err != nil {
		t.Fatal(err)
	}
	crewfold := mcpConfig.Servers["crewfold"]
	if crewfold.Command != "/opt/crewfold" || len(crewfold.Args) != 1 || crewfold.Args[0] != "__mcp-stdio-bridge" || crewfold.Env["CREWFOLD_MCP_CAPABILITY_FILE"] != "/tmp/run.token" {
		t.Fatalf("MCP config = %#v", crewfold)
	}

	settingsValue := argumentAfter(t, launch.Command.Arguments, "--settings")
	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Sandbox struct {
			Enabled           bool `json:"enabled"`
			FailIfUnavailable bool `json:"failIfUnavailable"`
			AllowUnsandboxed  bool `json:"allowUnsandboxedCommands"`
			Filesystem        struct {
				DenyRead  []string `json:"denyRead"`
				AllowRead []string `json:"allowRead"`
			} `json:"filesystem"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(settingsValue), &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Sandbox.Enabled || !settings.Sandbox.FailIfUnavailable || settings.Sandbox.AllowUnsandboxed {
		t.Fatalf("settings = %#v", settings)
	}
	if !strings.Contains(strings.Join(settings.Permissions.Deny, "\n"), "Edit(//var/lib/crewfold-claude/**)") {
		t.Fatalf("settings do not protect copied provider credentials: %#v", settings.Permissions.Deny)
	}
	if len(settings.Sandbox.Filesystem.AllowRead) != 1 || settings.Sandbox.Filesystem.AllowRead[0] != "/work/project" {
		t.Fatalf("sandbox checkout read exception = %#v", settings.Sandbox.Filesystem.AllowRead)
	}
	if home, _ := os.UserHomeDir(); home != "" && !containsString(settings.Sandbox.Filesystem.DenyRead, home) {
		t.Fatalf("sandbox does not deny the host home: %#v", settings.Sandbox.Filesystem.DenyRead)
	}
}

func TestClaudeProviderSupportsAnExplicitOuterSandbox(t *testing.T) {
	t.Parallel()

	arguments, err := claudeLaunchArguments("/opt/crewfold", RunCapabilityAccess{SocketPath: "/tmp/socket", CapabilityFile: "/tmp/token"}, domain.Run{Placement: domain.RunPlacement{CheckoutPath: "/work/project"}}, claudeScenario(), "1.00", true)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Sandbox struct {
			Enabled bool `json:"enabled"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(argumentAfter(t, arguments, "--settings")), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Sandbox.Enabled {
		t.Fatal("external sandbox launch retained the nested Claude sandbox")
	}
}

func TestClaudeProviderRefusesInvalidBudgetAndMissingCapability(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeClaudeBudgetUSD("0"); err == nil {
		t.Fatal("NormalizeClaudeBudgetUSD(0) error = nil")
	}
	if _, err := NormalizeClaudeBudgetUSD("0.001"); err == nil {
		t.Fatal("NormalizeClaudeBudgetUSD(0.001) error = nil")
	}
	if _, err := NormalizeClaudeBudgetUSD("100.01"); err == nil {
		t.Fatal("NormalizeClaudeBudgetUSD(100.01) error = nil")
	}
	provider := NewClaudeProvider(ClaudeProviderOptions{
		CapabilityPreparer: &recordedCapabilityPreparer{}, ClaudeExecutable: "/opt/claude",
		CrewfoldExecutable: "/opt/crewfold", ProbeRunner: compatibleClaudeRunner(),
	})
	_, err := provider.Prepare(context.Background(), domain.Run{ID: "run_44444444444444444444444444444444"}, claudeScenario())
	if err == nil || !strings.Contains(err.Error(), "socket or capability file is missing") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestClaudeProviderNormalizesOnlyExplicitBoundaries(t *testing.T) {
	t.Parallel()

	provider := ClaudeProvider{}
	blocked, found, err := provider.Next(context.Background(), domain.Run{}, claudeScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, Stdout: domain.CapturedLog{Text: string(claudeFixture(t, "permission-blocked.jsonl"))},
	})
	if err != nil || !found || blocked.Kind != domain.ObservationBlocked {
		t.Fatalf("Next(blocked) = %#v, %t, %v", blocked, found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, claudeScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, Stderr: domain.CapturedLog{Text: string(claudeFixture(t, "mcp-startup-failed.jsonl"))},
	})
	if found || err == nil || !strings.Contains(err.Error(), "Claude MCP boundary failed") {
		t.Fatalf("Next(MCP failure) found=%t error=%v", found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, claudeScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, ExitKnown: true, ExitCode: 0, Stdout: domain.CapturedLog{Text: `{"type":"result","subtype":"success"}`},
	})
	if found || err != nil {
		t.Fatalf("Next(unreported success) found=%t error=%v", found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, claudeScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, ExitKnown: true, ExitCode: 1, Stderr: domain.CapturedLog{Text: "Crewfold task check returned an error"},
	})
	if found || err != nil {
		t.Fatalf("Next(task error) found=%t error=%v", found, err)
	}
	_, found, err = provider.Next(context.Background(), domain.Run{}, claudeScenario(), RuntimeSnapshot{
		State: RuntimeStateExited, ExitKnown: true, ExitCode: 0, Stdout: domain.CapturedLog{Text: `{"type":"result","subtype":"success","is_error":false,"result":"The project excludes authentication; an earlier command failed."}`},
	})
	if found || err != nil {
		t.Fatalf("Next(project result text) found=%t error=%v; want no fabricated provider boundary", found, err)
	}
}

func compatibleClaudeRunner() *recordedClaudeRunner {
	return &recordedClaudeRunner{results: map[string]ProviderCommandResult{
		"--version":          {Stdout: []byte("2.1.220 (Claude Code)\n")},
		"--help":             {Stdout: []byte(strings.Join(claudeRequiredHelp, "\n"))},
		"auth status --json": {Stdout: []byte(`{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty","email":"owner@example"}`)},
	}}
}

func claudeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "providers", "claude", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func claudeScenario() domain.FakeScenario {
	return domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "claude-canary",
		Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"implementation_complete", "tests_passed"}},
		Steps:      []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "provider report authority", Handoff: "provider report authority"}},
	}
}

func argumentAfter(t *testing.T, arguments []string, flag string) string {
	t.Helper()
	for index, argument := range arguments {
		if argument == flag && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	t.Fatalf("arguments omit %s: %#v", flag, arguments)
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
