package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	ClaudeProbeSchema          = "urn:crewfold:schema:provider:claude-probe:v1"
	claudeProviderHandlePrefix = "claude-provider:v1:"
	claudeDefaultMaxBudgetUSD  = "1.00"
	claudeMaximumBudgetUSD     = 100.0
	claudeContainerConfigDir   = "/var/lib/crewfold-claude"
)

var (
	claudeVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)\s+\(Claude Code\)$`)
	claudeRequiredHelp   = []string{
		"--allowedTools",
		"--disable-slash-commands",
		"--max-budget-usd",
		"--mcp-config",
		"--no-chrome",
		"--no-session-persistence",
		"--output-format",
		"--permission-mode",
		"--print",
		"--setting-sources",
		"--strict-mcp-config",
		"--tools",
		"--verbose",
	}
	claudeCapabilities = []string{"headless_execution", "mcp_client", "structured_events"}
)

type ClaudeProbeCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type ClaudeProbeReport struct {
	Schema       string             `json:"schema"`
	Provider     string             `json:"provider"`
	Status       string             `json:"status"`
	Binary       string             `json:"binary"`
	Version      string             `json:"version,omitempty"`
	Capabilities []string           `json:"capabilities"`
	Checks       []ClaudeProbeCheck `json:"checks"`
}

func (report ClaudeProbeReport) Compatible() bool { return report.Status == "ok" }

func (report ClaudeProbeReport) Error() error {
	if report.Compatible() {
		return nil
	}
	for _, check := range report.Checks {
		if check.Status == "failed" {
			return fmt.Errorf("Claude %s check failed: %s", check.Name, check.Detail)
		}
	}
	return errors.New("Claude compatibility check failed")
}

type ClaudeProbe struct {
	executable string
	configDir  string
	runner     ProviderCommandRunner
}

func NewClaudeProbe(executable, configDir string, runner ProviderCommandRunner) ClaudeProbe {
	if strings.TrimSpace(executable) == "" {
		executable = "claude"
	}
	if strings.TrimSpace(configDir) == "" {
		configDir = strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	}
	if runner == nil {
		runner = ProviderExecRunner{}
	}
	return ClaudeProbe{executable: executable, configDir: configDir, runner: runner}
}

func (probe ClaudeProbe) Run(ctx context.Context) ClaudeProbeReport {
	report := ClaudeProbeReport{
		Schema: ClaudeProbeSchema, Provider: "claude", Status: "ok", Binary: probe.executable,
		Capabilities: append([]string(nil), claudeCapabilities...), Checks: make([]ClaudeProbeCheck, 0, 4),
	}
	environment := map[string]string{}
	if probe.configDir != "" {
		environment["CLAUDE_CONFIG_DIR"] = probe.configDir
	}

	versionResult, err := probe.runner.Run(ctx, probe.executable, []string{"--version"}, environment)
	if err != nil {
		report.fail("binary", boundedProviderDiagnostic(err.Error()))
		return report
	}
	if versionResult.ExitCode != 0 {
		report.fail("binary", providerCommandDiagnostic(versionResult, "Claude version probe failed"))
		return report
	}
	version := strings.TrimSpace(string(versionResult.Stdout))
	if version == "" {
		report.fail("binary", "Claude returned an empty version")
		return report
	}
	report.Version = version
	report.pass("binary", version)
	match := claudeVersionPattern.FindStringSubmatch(version)
	if len(match) != 4 || match[1] != "2" {
		report.fail("compatibility", "Crewfold has tested Claude Code 2.x; install a compatible 2.x release")
		return report
	}
	report.pass("compatibility", "Claude Code 2.x command contract")

	helpResult, err := probe.runner.Run(ctx, probe.executable, []string{"--help"}, environment)
	if err != nil {
		report.fail("capabilities", boundedProviderDiagnostic(err.Error()))
		return report
	}
	if helpResult.ExitCode != 0 {
		report.fail("capabilities", providerCommandDiagnostic(helpResult, "Claude capability probe failed"))
		return report
	}
	help := string(helpResult.Stdout) + "\n" + string(helpResult.Stderr)
	missing := make([]string, 0)
	for _, capability := range claudeRequiredHelp {
		if !strings.Contains(help, capability) {
			missing = append(missing, capability)
		}
	}
	if len(missing) != 0 {
		report.fail("capabilities", "Claude is missing required options: "+strings.Join(missing, ", ")+"; install a compatible Claude Code CLI")
		return report
	}
	report.pass("capabilities", strings.Join(report.Capabilities, ", "))

	authResult, err := probe.runner.Run(ctx, probe.executable, []string{"auth", "status", "--json"}, environment)
	if err != nil {
		report.fail("authentication", boundedProviderDiagnostic(err.Error()))
		return report
	}
	if authResult.ExitCode != 0 {
		report.fail("authentication", "Claude is not authenticated; run 'claude auth login' outside Crewfold")
		return report
	}
	var auth struct {
		LoggedIn    bool   `json:"loggedIn"`
		AuthMethod  string `json:"authMethod"`
		APIProvider string `json:"apiProvider"`
	}
	if err := json.Unmarshal(authResult.Stdout, &auth); err != nil || !auth.LoggedIn {
		report.fail("authentication", "Claude did not report an authenticated session; run 'claude auth login' outside Crewfold")
		return report
	}
	detail := "authenticated"
	if auth.AuthMethod != "" {
		detail += " via " + auth.AuthMethod
	}
	if auth.APIProvider != "" {
		detail += " (" + auth.APIProvider + ")"
	}
	report.pass("authentication", boundedProviderDiagnostic(detail))
	return report
}

func (report *ClaudeProbeReport) pass(name, detail string) {
	report.Checks = append(report.Checks, ClaudeProbeCheck{Name: name, Status: "ok", Detail: detail})
}

func (report *ClaudeProbeReport) fail(name, detail string) {
	report.Status = "failed"
	report.Checks = append(report.Checks, ClaudeProbeCheck{Name: name, Status: "failed", Detail: detail})
}

type ClaudeProviderOptions struct {
	CapabilityPreparer  RunCapabilityPreparer
	ClaudeExecutable    string
	CrewfoldExecutable  string
	ClaudeConfigDir     string
	MaxBudgetUSD        string
	ExternallySandboxed bool
	ProbeRunner         ProviderCommandRunner
}

type ClaudeProvider struct {
	preparer            RunCapabilityPreparer
	claudeExecutable    string
	crewfoldExecutable  string
	claudeConfigDir     string
	maxBudgetUSD        string
	externallySandboxed bool
	probeRunner         ProviderCommandRunner
}

func NewClaudeProvider(options ClaudeProviderOptions) ClaudeProvider {
	claudeExecutable := strings.TrimSpace(options.ClaudeExecutable)
	if claudeExecutable == "" {
		claudeExecutable = "claude"
	}
	maxBudget, err := NormalizeClaudeBudgetUSD(options.MaxBudgetUSD)
	if err != nil {
		maxBudget = strings.TrimSpace(options.MaxBudgetUSD)
	}
	return ClaudeProvider{
		preparer: options.CapabilityPreparer, claudeExecutable: claudeExecutable,
		crewfoldExecutable: strings.TrimSpace(options.CrewfoldExecutable),
		claudeConfigDir:    strings.TrimSpace(options.ClaudeConfigDir), maxBudgetUSD: maxBudget,
		externallySandboxed: options.ExternallySandboxed, probeRunner: options.ProbeRunner,
	}
}

func (ClaudeProvider) Name() string { return "claude" }

func (provider ClaudeProvider) Probe(ctx context.Context) ClaudeProbeReport {
	return NewClaudeProbe(provider.claudeExecutable, provider.claudeConfigDir, provider.probeRunner).Run(ctx)
}

func (provider ClaudeProvider) Prepare(ctx context.Context, run domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	if scenario.StartFailure != "" {
		return LaunchSpec{}, &StartError{Message: scenario.StartFailure}
	}
	if provider.preparer == nil {
		return LaunchSpec{}, errors.New("Claude provider cannot prepare the run-scoped MCP capability")
	}
	maxBudget, err := NormalizeClaudeBudgetUSD(provider.maxBudgetUSD)
	if err != nil {
		return LaunchSpec{}, err
	}
	if err := provider.Probe(ctx).Error(); err != nil {
		return LaunchSpec{}, err
	}
	claudeExecutable, err := absoluteExecutable(provider.claudeExecutable)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("resolve Claude executable: %w", err)
	}
	crewfoldExecutable, err := absoluteExecutable(provider.crewfoldExecutable)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("resolve Crewfold MCP bridge executable: %w", err)
	}
	access, err := provider.preparer.PrepareRunCapability(ctx, run.ID)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("prepare Claude MCP capability: %w", err)
	}
	if strings.TrimSpace(access.SocketPath) == "" || strings.TrimSpace(access.CapabilityFile) == "" {
		return LaunchSpec{}, errors.New("prepare Claude MCP capability: socket or capability file is missing")
	}
	arguments, err := claudeLaunchArguments(crewfoldExecutable, access, run, scenario, maxBudget, provider.externallySandboxed)
	if err != nil {
		return LaunchSpec{}, err
	}
	environment := map[string]string{
		"CREWFOLD_MCP_SOCKET":          access.SocketPath,
		"CREWFOLD_MCP_CAPABILITY_FILE": access.CapabilityFile,
	}
	if provider.claudeConfigDir != "" {
		environment["CLAUDE_CONFIG_DIR"] = provider.claudeConfigDir
	}
	return LaunchSpec{Scenario: scenario, Command: &CommandSpec{
		Executable: claudeExecutable, Arguments: arguments, Environment: environment,
		Timeout: time.Duration(scenario.Process.TimeoutMillis) * time.Millisecond, OutputByteLimit: 1024 * 1024,
	}}, nil
}

func (ClaudeProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if strings.TrimSpace(binding.RuntimeHandle) == "" {
		return ProviderBinding{}, errors.New("cannot bind Claude provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: claudeProviderHandlePrefix + run.ID}, nil
}

func (ClaudeProvider) Next(_ context.Context, _ domain.Run, scenario domain.FakeScenario, snapshot RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	if snapshot.State == RuntimeStateStarting || snapshot.State == RuntimeStateRunning {
		return domain.RunObservation{}, false, nil
	}
	transcript := strings.TrimSpace(strings.Join([]string{snapshot.Diagnostic, snapshot.Stdout.Text, snapshot.Stderr.Text}, "\n"))
	diagnostic := strings.Join(claudeStructuredFailures(transcript), "\n")
	lower := strings.ToLower(diagnostic)
	if boundaryMatch(lower, []string{"mcp server", "mcp connection", "mcp startup", "mcp initialization", "crewfold mcp"}) && boundaryMatch(lower, []string{"failed", "error", "unavailable", "timed out", "disconnected"}) {
		return domain.RunObservation{}, false, fmt.Errorf("Claude MCP boundary failed: %s", boundedProviderDiagnostic(diagnostic))
	}
	if boundaryMatch(lower, []string{"authentication_failed", "not logged in", "auth login", "invalid api key", "unauthorized"}) {
		return domain.RunObservation{}, false, fmt.Errorf("Claude authentication boundary failed: %s", boundedProviderDiagnostic(diagnostic))
	}
	if boundaryMatch(lower, []string{"permission denied", "tool use denied", "not allowed", "permission prompt", "sandbox unavailable"}) {
		message := boundedProviderDiagnostic(diagnostic)
		if message == "" {
			message = "Claude stopped at a permission or sandbox boundary"
		}
		return domain.RunObservation{Kind: domain.ObservationBlocked, Message: message}, true, nil
	}
	return domain.RunObservation{}, false, nil
}

func claudeStructuredFailures(transcript string) []string {
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
		isError, _ := event["is_error"].(bool)
		if isError {
			if message := structuredProviderMessage(event); message != "" {
				result = append(result, message)
			}
		}
	}
	return result
}

func NormalizeClaudeBudgetUSD(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = claudeDefaultMaxBudgetUSD
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0.01 || amount > claudeMaximumBudgetUSD {
		return "", fmt.Errorf("Claude maximum budget must be at least 0.01 and at most %.2f USD", claudeMaximumBudgetUSD)
	}
	return strconv.FormatFloat(amount, 'f', 2, 64), nil
}

func claudeLaunchArguments(crewfoldExecutable string, access RunCapabilityAccess, run domain.Run, scenario domain.FakeScenario, maxBudget string, externallySandboxed bool) ([]string, error) {
	mcpConfig := map[string]any{"mcpServers": map[string]any{"crewfold": map[string]any{
		"type": "stdio", "command": crewfoldExecutable, "args": []string{"__mcp-stdio-bridge"},
		"env": map[string]string{"CREWFOLD_MCP_SOCKET": access.SocketPath, "CREWFOLD_MCP_CAPABILITY_FILE": access.CapabilityFile},
	}}}
	mcpJSON, err := json.Marshal(mcpConfig)
	if err != nil {
		return nil, fmt.Errorf("encode Claude MCP configuration: %w", err)
	}
	settingsJSON, err := json.Marshal(claudeRunSettings(run.Placement.CheckoutPath, externallySandboxed))
	if err != nil {
		return nil, fmt.Errorf("encode Claude run settings: %w", err)
	}
	prompt := claudeInitialPrompt(scenario.Acceptance.RequiredEvidence)
	if scenario.Name == "owner-executive" {
		prompt = claudeExecutivePrompt()
	}
	return []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence", "--strict-mcp-config", "--mcp-config", string(mcpJSON),
		"--setting-sources", "", "--settings", string(settingsJSON),
		"--permission-mode", "dontAsk", "--tools", "Read,Edit,Bash",
		"--allowedTools", "Read(/**),Edit(/**),Bash(./check.sh),Bash(git diff),Bash(git diff --check),mcp__crewfold__*",
		"--max-budget-usd", maxBudget, "--no-chrome", "--disable-slash-commands",
		prompt,
	}, nil
}

func claudeExecutivePrompt() string {
	return "You are the project's Crewfold executive. This is one short-lived exchange in a durable Crewfold conversation, not an implementation run. First call crewfold_get_briefing, then crewfold_get_executive_context. Treat the frozen owner instruction, canonical project context, manager grant, and cited records as your entire authority. Inspect the checkout read-only only. You may answer from evidence, ask one consequential owner decision, or submit bounded typed manager proposals through the granted crewfold_propose_* tools. A decision is consequential only when distinct owner choices can change authorized project state; never ask merely for acknowledgement, to resume before repair is proved, to bypass dependency order, or to choose an effect Crewfold cannot perform. A lost runtime must first be independently confirmed retired through Crewfold's exact lost-run recovery control; explain that prerequisite and do not propose around retained runtime authority. After exact retirement is recorded and no run or scheduling intent retains authority, recover a blocked task only with a reassign_task escalation naming the exact task revision and an authorized launch profile; retry_task is only for a definite start_failed run. In task proposals, include claim requirements only when the frozen manager grant lists exact allowed_claim_kinds; when that list is empty, omit claim-requirement actions entirely. Never edit files, execute project effects, accept your own proposals, launch work directly, or treat role text as authority. Finish by calling crewfold_respond_to_owner exactly once with an answer, update, decision, proposal summary, or refusal; include only citation refs and proposal IDs returned in this exchange. Terminal text is not the response."
}

func claudeRunSettings(checkoutPath string, externallySandboxed bool) map[string]any {
	home, _ := os.UserHomeDir()
	sensitive := []string{claudeContainerConfigDir}
	if home != "" {
		for _, relative := range []string{
			".aws", ".claude", ".config/gcloud", ".config/gh", ".docker", ".git-credentials",
			".gnupg", ".kube", ".netrc", ".npmrc", ".pypirc", ".ssh",
		} {
			sensitive = append(sensitive, filepath.Join(home, relative))
		}
	}
	denyRules := make([]string, 0, len(sensitive)*4+5)
	for _, path := range sensitive {
		absolutePath := "//" + strings.TrimPrefix(filepath.Clean(path), "/")
		denyRules = append(denyRules,
			"Read("+absolutePath+")", "Read("+absolutePath+"/**)",
			"Edit("+absolutePath+")", "Edit("+absolutePath+"/**)",
		)
	}
	denyRules = append(denyRules, "WebFetch", "WebSearch", "Agent", "Write", "NotebookEdit")
	sandboxDenyRead := append([]string(nil), sensitive...)
	if home != "" {
		sandboxDenyRead = append(sandboxDenyRead, home)
	}
	return map[string]any{
		"permissions": map[string]any{
			"disableAutoMode": "disable", "disableBypassPermissionsMode": "disable", "deny": denyRules,
		},
		"sandbox": map[string]any{
			"enabled": !externallySandboxed, "failIfUnavailable": !externallySandboxed,
			"autoAllowBashIfSandboxed": true, "allowUnsandboxedCommands": false,
			"filesystem": map[string]any{"denyRead": sandboxDenyRead, "allowRead": []string{checkoutPath}},
		},
	}
}

func claudeInitialPrompt(requiredEvidence []string) string {
	evidence := "none"
	if len(requiredEvidence) != 0 {
		evidence = strings.Join(requiredEvidence, ", ")
	}
	return "You are a Crewfold-managed Claude Code worker. First call the Crewfold MCP tool crewfold_get_briefing and treat that immutable briefing as the authority for your role, task, checkout, policies, durable inbox, and reporting contract. Work only on the assigned task in the current checkout. Use Crewfold MCP for progress, durable messages, artifacts, blockage, and completion; do not substitute final terminal text or provider session state for a Crewfold report. Read any applicable provider handoff from the Crewfold inbox, not another provider's transcript. If you cannot proceed, call crewfold_report_blocked and then end the run. Before completion, inspect the implementation diff and run the task's checks, then call crewfold_propose_completion with an executive handoff. In changed_paths list the exact implementation paths you inspected; if you are an independent verifier, this records reviewed paths and does not claim you edited them. Keep each array item within 128 printable characters. Required completion evidence IDs: " + evidence + ". Never push, publish, use network tools, or access unrelated projects."
}

func boundaryMatch(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
