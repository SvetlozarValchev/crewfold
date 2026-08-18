package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	CodexProbeSchema             = "urn:crewfold:schema:provider:codex-probe:v1"
	CodexSandboxReadOnly         = "read-only"
	CodexSandboxWorkspaceWrite   = "workspace-write"
	CodexSandboxDangerFullAccess = "danger-full-access"
	codexProviderHandlePrefix    = "codex-provider:v1:"
)

var codexRequiredExecHelp = []string{
	"--config",
	"--ephemeral",
	"--ignore-user-config",
	"--json",
	"--sandbox",
}

var codexCapabilities = []string{
	"headless_execution",
	"mcp_client",
	"structured_events",
}

type CodexCommandResult = ProviderCommandResult
type CodexCommandRunner = ProviderCommandRunner
type CodexExecRunner = ProviderExecRunner

type CodexProbeCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type CodexProbeReport struct {
	Schema       string            `json:"schema"`
	Provider     string            `json:"provider"`
	Status       string            `json:"status"`
	Binary       string            `json:"binary"`
	Version      string            `json:"version,omitempty"`
	Capabilities []string          `json:"capabilities"`
	Checks       []CodexProbeCheck `json:"checks"`
}

func (report CodexProbeReport) Compatible() bool { return report.Status == "ok" }

func (report CodexProbeReport) Error() error {
	if report.Compatible() {
		return nil
	}
	for _, check := range report.Checks {
		if check.Status == "failed" {
			return fmt.Errorf("Codex %s check failed: %s", check.Name, check.Detail)
		}
	}
	return errors.New("Codex compatibility check failed")
}

type CodexProbe struct {
	executable string
	codexHome  string
	runner     CodexCommandRunner
}

func NewCodexProbe(executable, codexHome string, runner CodexCommandRunner) CodexProbe {
	if strings.TrimSpace(executable) == "" {
		executable = "codex"
	}
	if strings.TrimSpace(codexHome) == "" {
		codexHome = defaultCodexHome()
	}
	if runner == nil {
		runner = CodexExecRunner{}
	}
	return CodexProbe{executable: executable, codexHome: codexHome, runner: runner}
}

func (probe CodexProbe) Run(ctx context.Context) CodexProbeReport {
	return probe.run(ctx, true)
}

func (probe CodexProbe) run(ctx context.Context, requireWorkspaceSandbox bool) CodexProbeReport {
	report := CodexProbeReport{
		Schema: CodexProbeSchema, Provider: "codex", Status: "ok", Binary: probe.executable,
		Capabilities: append([]string(nil), codexCapabilities...), Checks: make([]CodexProbeCheck, 0, 3),
	}
	environment := map[string]string{}
	if probe.codexHome != "" {
		environment["CODEX_HOME"] = probe.codexHome
	}

	versionResult, err := probe.runner.Run(ctx, probe.executable, []string{"--version"}, environment)
	if err != nil {
		report.fail("binary", boundedCodexDiagnostic(err.Error()))
		return report
	}
	if versionResult.ExitCode != 0 {
		report.fail("binary", codexCommandDiagnostic(versionResult, "Codex version probe failed"))
		return report
	}
	version := strings.TrimSpace(string(versionResult.Stdout))
	if version == "" {
		report.fail("binary", "Codex returned an empty version")
		return report
	}
	report.Version = version
	report.pass("binary", version)

	helpResult, err := probe.runner.Run(ctx, probe.executable, []string{"exec", "--help"}, environment)
	if err != nil {
		report.fail("capabilities", boundedCodexDiagnostic(err.Error()))
		return report
	}
	if helpResult.ExitCode != 0 {
		report.fail("capabilities", codexCommandDiagnostic(helpResult, "Codex exec capability probe failed"))
		return report
	}
	help := string(helpResult.Stdout) + "\n" + string(helpResult.Stderr)
	missing := make([]string, 0)
	for _, capability := range codexRequiredExecHelp {
		if !strings.Contains(help, capability) {
			missing = append(missing, capability)
		}
	}
	if len(missing) != 0 {
		report.fail("capabilities", "Codex exec is missing required options: "+strings.Join(missing, ", ")+"; install a compatible Codex CLI")
		return report
	}
	if requireWorkspaceSandbox {
		sandboxResult, err := probe.runner.Run(ctx, probe.executable, []string{"sandbox", "--", "/bin/sh", "-c", "exit 0"}, environment)
		if err != nil {
			report.fail("capabilities", "Codex workspace sandbox probe failed: "+boundedCodexDiagnostic(err.Error()))
			return report
		}
		if sandboxResult.ExitCode != 0 {
			detail := codexCommandDiagnostic(sandboxResult, "Codex workspace sandbox probe failed")
			report.fail("capabilities", "Codex workspace sandbox is unavailable: "+detail+"; on Ubuntu verify the unprivileged user-namespace/AppArmor policy before starting Crewfold work")
			return report
		}
	}
	report.pass("capabilities", strings.Join(report.Capabilities, ", "))

	authResult, err := probe.runner.Run(ctx, probe.executable, []string{"login", "status"}, environment)
	if err != nil {
		report.fail("authentication", boundedCodexDiagnostic(err.Error()))
		return report
	}
	if authResult.ExitCode != 0 {
		report.fail("authentication", codexCommandDiagnostic(authResult, "Codex is not authenticated; run 'codex login' outside Crewfold"))
		return report
	}
	authDetail := strings.TrimSpace(string(authResult.Stdout))
	if authDetail == "" {
		authDetail = strings.TrimSpace(string(authResult.Stderr))
	}
	if authDetail == "" {
		authDetail = "Codex reported an authenticated session"
	}
	report.pass("authentication", boundedCodexDiagnostic(authDetail))
	return report
}

func (report *CodexProbeReport) pass(name, detail string) {
	report.Checks = append(report.Checks, CodexProbeCheck{Name: name, Status: "ok", Detail: detail})
}

func (report *CodexProbeReport) fail(name, detail string) {
	report.Status = "failed"
	report.Checks = append(report.Checks, CodexProbeCheck{Name: name, Status: "failed", Detail: detail})
}

func codexCommandDiagnostic(result CodexCommandResult, fallback string) string {
	return providerCommandDiagnostic(result, fallback)
}

func boundedCodexDiagnostic(value string) string { return boundedProviderDiagnostic(value) }

func defaultCodexHome() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

type CodexProviderOptions struct {
	CapabilityPreparer  RunCapabilityPreparer
	CodexExecutable     string
	CrewfoldExecutable  string
	CodexHome           string
	SandboxMode         string
	ExternallySandboxed bool
	ToolNetworkAccess   bool
	ProbeRunner         CodexCommandRunner
}

type CodexProvider struct {
	preparer            RunCapabilityPreparer
	codexExecutable     string
	crewfoldExecutable  string
	codexHome           string
	sandboxMode         string
	externallySandboxed bool
	toolNetworkAccess   bool
	probeRunner         CodexCommandRunner
}

func NewCodexProvider(options CodexProviderOptions) CodexProvider {
	codexExecutable := strings.TrimSpace(options.CodexExecutable)
	if codexExecutable == "" {
		codexExecutable = "codex"
	}
	sandboxMode := strings.TrimSpace(options.SandboxMode)
	if sandboxMode == "" {
		sandboxMode = CodexSandboxWorkspaceWrite
	}
	return CodexProvider{
		preparer: options.CapabilityPreparer, codexExecutable: codexExecutable,
		crewfoldExecutable:  strings.TrimSpace(options.CrewfoldExecutable),
		codexHome:           firstNonEmpty(strings.TrimSpace(options.CodexHome), defaultCodexHome()),
		sandboxMode:         sandboxMode,
		externallySandboxed: options.ExternallySandboxed,
		toolNetworkAccess:   options.ToolNetworkAccess,
		probeRunner:         options.ProbeRunner,
	}
}

func (CodexProvider) Name() string { return "codex" }

func (provider CodexProvider) Probe(ctx context.Context) CodexProbeReport {
	requireWorkspaceSandbox := provider.sandboxMode == CodexSandboxWorkspaceWrite
	return NewCodexProbe(provider.codexExecutable, provider.codexHome, provider.probeRunner).run(ctx, requireWorkspaceSandbox)
}

func (provider CodexProvider) Prepare(ctx context.Context, run domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	if scenario.StartFailure != "" {
		return LaunchSpec{}, &StartError{Message: scenario.StartFailure}
	}
	if provider.preparer == nil {
		return LaunchSpec{}, errors.New("Codex provider cannot prepare the run-scoped MCP capability")
	}
	if err := ValidateCodexSandboxMode(provider.sandboxMode); err != nil {
		return LaunchSpec{}, err
	}
	if provider.sandboxMode == CodexSandboxDangerFullAccess && !provider.externallySandboxed {
		return LaunchSpec{}, errors.New("Codex danger-full-access requires an independently enforced external sandbox")
	}
	report := provider.Probe(ctx)
	if err := report.Error(); err != nil {
		return LaunchSpec{}, err
	}
	codexExecutable, err := absoluteExecutable(provider.codexExecutable)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("resolve Codex executable: %w", err)
	}
	crewfoldExecutable, err := absoluteExecutable(provider.crewfoldExecutable)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("resolve Crewfold MCP bridge executable: %w", err)
	}
	access, err := provider.preparer.PrepareRunCapability(ctx, run.ID)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("prepare Codex MCP capability: %w", err)
	}
	if strings.TrimSpace(access.SocketPath) == "" || strings.TrimSpace(access.CapabilityFile) == "" {
		return LaunchSpec{}, errors.New("prepare Codex MCP capability: socket or capability file is missing")
	}

	arguments := codexLaunchArguments(crewfoldExecutable, run, scenario, provider.sandboxMode, provider.toolNetworkAccess)
	environment := map[string]string{
		"CREWFOLD_MCP_SOCKET":          access.SocketPath,
		"CREWFOLD_MCP_CAPABILITY_FILE": access.CapabilityFile,
	}
	if provider.codexHome != "" {
		environment["CODEX_HOME"] = provider.codexHome
	}
	return LaunchSpec{Scenario: scenario, Command: &CommandSpec{
		Executable: codexExecutable, Arguments: arguments, Environment: environment,
		Timeout: time.Duration(scenario.Process.TimeoutMillis) * time.Millisecond, OutputByteLimit: 1024 * 1024,
	}}, nil
}

func (CodexProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if strings.TrimSpace(binding.RuntimeHandle) == "" {
		return ProviderBinding{}, errors.New("cannot bind Codex provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: codexProviderHandlePrefix + run.ID}, nil
}

func (CodexProvider) Next(_ context.Context, _ domain.Run, scenario domain.FakeScenario, snapshot RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	if snapshot.State == RuntimeStateStarting || snapshot.State == RuntimeStateRunning {
		return domain.RunObservation{}, false, nil
	}
	transcript := strings.TrimSpace(strings.Join([]string{snapshot.Diagnostic, snapshot.Stdout.Text, snapshot.Stderr.Text}, "\n"))
	diagnostic := strings.Join(codexStructuredFailures(transcript), "\n")
	lower := strings.ToLower(diagnostic)
	if codexBoundaryMatch(lower, []string{"mcp server", "mcp startup", "mcp initialization", "crewfold mcp"}) && codexBoundaryMatch(lower, []string{"failed", "error", "unavailable", "timed out"}) {
		return domain.RunObservation{}, false, fmt.Errorf("Codex MCP boundary failed: %s", boundedCodexDiagnostic(diagnostic))
	}
	if codexBoundaryMatch(lower, []string{"not logged in", "authentication", "unauthorized", "login required"}) {
		return domain.RunObservation{}, false, fmt.Errorf("Codex authentication boundary failed: %s", boundedCodexDiagnostic(diagnostic))
	}
	if codexBoundaryMatch(lower, []string{"approval required", "requires approval", "permission denied", "sandbox denied", "approval was denied"}) {
		message := boundedCodexDiagnostic(diagnostic)
		if message == "" {
			message = "Codex stopped at an approval or permission boundary"
		}
		return domain.RunObservation{Kind: domain.ObservationBlocked, Message: message}, true, nil
	}
	return domain.RunObservation{}, false, nil
}

func codexStructuredFailures(transcript string) []string {
	result := make([]string, 0, 2)
	for _, line := range strings.Split(transcript, "\n") {
		start := strings.IndexByte(line, '{')
		if start < 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line[start:]), &event); err != nil {
			continue
		}
		typeName, _ := event["type"].(string)
		switch typeName {
		case "error", "turn.failed":
			if message := structuredProviderMessage(event); message != "" {
				result = append(result, message)
			}
		case "item.completed":
			item, _ := event["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			status, _ := item["status"].(string)
			if itemType == "mcp_tool_call" && status == "failed" {
				message := structuredProviderMessage(item)
				server, _ := item["server"].(string)
				tool, _ := item["tool"].(string)
				result = append(result, strings.TrimSpace(strings.Join([]string{server, tool, message}, " ")))
			}
		}
	}
	return result
}

func structuredProviderMessage(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case map[string]any:
		for _, key := range []string{"message", "error", "result"} {
			if message := structuredProviderMessage(current[key]); message != "" {
				return message
			}
		}
	}
	return ""
}

func codexBoundaryMatch(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func ValidateCodexSandboxMode(value string) error {
	switch strings.TrimSpace(value) {
	case CodexSandboxWorkspaceWrite, CodexSandboxDangerFullAccess:
		return nil
	default:
		return fmt.Errorf("Codex sandbox must be %q or %q", CodexSandboxWorkspaceWrite, CodexSandboxDangerFullAccess)
	}
}

func codexLaunchArguments(crewfoldExecutable string, run domain.Run, scenario domain.FakeScenario, sandboxMode string, toolNetworkAccess bool) []string {
	bridgeArguments := tomlStringArray([]string{"__mcp-stdio-bridge"})
	forwardedEnvironment := tomlStringArray([]string{"CREWFOLD_MCP_SOCKET", "CREWFOLD_MCP_CAPABILITY_FILE"})
	arguments := []string{
		"exec", "--json", "--color", "never", "--ephemeral", "--ignore-user-config", "--sandbox", sandboxMode, "-C", run.Placement.CheckoutPath,
		"-c", `approval_policy="never"`,
	}
	if sandboxMode == CodexSandboxWorkspaceWrite {
		arguments = append(arguments, "-c", "sandbox_workspace_write.network_access="+strconv.FormatBool(toolNetworkAccess))
	}
	prompt := codexInitialPrompt(scenario.Acceptance.RequiredEvidence)
	if scenario.Name == "owner-executive" {
		prompt = codexExecutivePrompt()
	}
	return append(arguments,
		"-c", "mcp_servers.crewfold.command="+strconv.Quote(crewfoldExecutable),
		"-c", "mcp_servers.crewfold.args="+bridgeArguments,
		"-c", "mcp_servers.crewfold.env_vars="+forwardedEnvironment,
		"-c", `mcp_servers.crewfold.required=true`,
		"-c", `mcp_servers.crewfold.default_tools_approval_mode="approve"`,
		prompt,
	)
}

func codexExecutivePrompt() string {
	return "You are the project's Crewfold executive. This is one short-lived exchange in a durable Crewfold conversation, not a disposable form interpretation and not an implementation run. First call crewfold_get_briefing, then crewfold_get_executive_context. Treat the frozen owner instruction, canonical project context, manager grant, and cited records as your entire authority. You may inspect the checkout read-only, answer from evidence, ask one consequential owner decision, or submit bounded typed manager proposals through the granted crewfold_propose_* tools. A decision is consequential only when distinct owner choices can change authorized project state; never ask merely for acknowledgement, to resume before repair is proved, to bypass dependency order, or to choose an effect Crewfold cannot perform. Resume preserves the exact existing runtime and sandbox; never present it as a way to adopt changed service, provider, runtime, or network policy. When a blocked run needs a replacement environment, tell the owner to stop that exact authority holder and then use the workbench's fresh-run action on the retained assignment. A lost runtime must first be independently confirmed retired through Crewfold's exact lost-run recovery control; explain that prerequisite and do not propose around retained runtime authority. After exact retirement is recorded and no run or scheduling intent retains authority, recover a blocked task only with a reassign_task escalation naming the exact task revision and an authorized launch profile; retry_task is only for a definite start_failed run. In task proposals, include claim requirements only when the frozen manager grant lists exact allowed_claim_kinds; when that list is empty, omit claim-requirement actions entirely. Never edit files, execute project effects, accept your own proposals, launch work directly, or treat role text as authority. Finish by calling crewfold_respond_to_owner exactly once with an answer, update, decision, proposal summary, or refusal; include only citation refs and proposal IDs returned in this exchange. Your terminal text is not the response."
}

func codexInitialPrompt(requiredEvidence []string) string {
	evidence := "none"
	if len(requiredEvidence) != 0 {
		evidence = strings.Join(requiredEvidence, ", ")
	}
	return "You are a Crewfold-managed Codex worker. First call the Crewfold MCP tool crewfold_get_briefing and treat that immutable briefing as the authority for your role, task, checkout, policies, and reporting contract. Work only on the assigned task in the current checkout. Use Crewfold MCP for progress, durable messages, artifacts, blockage, and completion; do not substitute your final terminal text for a Crewfold report. If you cannot proceed, call crewfold_report_blocked and then end the run. Before completion, inspect the diff and run the task's checks, then call crewfold_propose_completion with an executive handoff. Required completion evidence IDs: " + evidence + ". Never push, publish, or access unrelated projects."
}

func tomlStringArray(values []string) string {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, strconv.Quote(value))
	}
	return "[" + strings.Join(encoded, ",") + "]"
}

func absoluteExecutable(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("executable is not configured")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
