package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

// FixtureMCPProvider is the first provider-neutral direct subprocess that uses
// Crewfold's scoped MCP capability instead of encoding reports in stdout.
type FixtureMCPProvider struct {
	preparer   RunCapabilityPreparer
	executable string
	arguments  []string
}

func NewFixtureMCPProvider(preparer RunCapabilityPreparer) FixtureMCPProvider {
	executable, _ := os.Executable()
	return FixtureMCPProvider{preparer: preparer, executable: executable, arguments: []string{"__fixture-mcp-provider"}}
}

func (FixtureMCPProvider) Name() string { return "fixture-mcp" }

func (provider FixtureMCPProvider) Prepare(ctx context.Context, run domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	if scenario.StartFailure != "" {
		return LaunchSpec{}, &StartError{Message: scenario.StartFailure}
	}
	if provider.preparer == nil || strings.TrimSpace(provider.executable) == "" {
		return LaunchSpec{}, errors.New("scoped fixture provider is unavailable")
	}
	access, err := provider.preparer.PrepareRunCapability(ctx, run.ID)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("prepare scoped run capability: %w", err)
	}
	input, err := json.Marshal(scenario)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("encode fixture scenario: %w", err)
	}
	command := &CommandSpec{
		Executable: provider.executable, Arguments: append([]string(nil), provider.arguments...),
		StandardInput: input,
		Environment: map[string]string{
			"CREWFOLD_MCP_SOCKET":          access.SocketPath,
			"CREWFOLD_MCP_CAPABILITY_FILE": access.CapabilityFile,
		},
		Timeout: time.Duration(scenario.Process.TimeoutMillis) * time.Millisecond, OutputByteLimit: 64 * 1024,
	}
	return LaunchSpec{Scenario: scenario, Command: command}, nil
}

func (FixtureMCPProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return ProviderBinding{}, errors.New("cannot bind scoped fixture provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: "fixture-mcp-provider:" + run.ID}, nil
}

func (FixtureMCPProvider) Next(_ context.Context, _ domain.Run, scenario domain.FakeScenario, _ RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	return domain.RunObservation{}, false, nil
}

func RunFixtureMCPProvider(input io.Reader, output, diagnostics io.Writer) int {
	scenario, err := readFixtureScenario(input)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	configureFixtureSignals(scenario.Process.IgnoreTermination)
	writeFixtureMetadata(output)

	tokenBytes, err := os.ReadFile(os.Getenv("CREWFOLD_MCP_CAPABILITY_FILE"))
	if err != nil || len(tokenBytes) > 1024 || strings.TrimSpace(string(tokenBytes)) == "" {
		fmt.Fprintln(diagnostics, "read scoped capability file failed")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mcp.Dial(ctx, os.Getenv("CREWFOLD_MCP_SOCKET"), strings.TrimSpace(string(tokenBytes)))
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	if result, err := client.CallTool(ctx, "crewfold_get_briefing", map[string]any{}); err != nil || result.IsError {
		fmt.Fprintln(diagnostics, "get scoped briefing failed")
		return 1
	}
	if scenario.Process.CrossRunProbe {
		_, err := client.ReadResource(ctx, "crewfold://runs/run_ffffffffffffffffffffffffffffffff/briefing")
		var rpcError *mcp.RPCError
		if !errors.As(err, &rpcError) || rpcError.Data == nil || rpcError.Data.Code != "out_of_scope" {
			fmt.Fprintln(diagnostics, "cross-run resource probe was not denied")
			return 1
		}
	}
	if scenario.Process.PublishArtifact {
		result, err := client.CallTool(ctx, "crewfold_publish_artifact", map[string]any{
			"name": "fixture-evidence", "media_type": "text/plain", "content": "scoped fixture evidence", "idempotency_key": "fixture-artifact",
		})
		if err != nil || result.IsError {
			fmt.Fprintln(diagnostics, "publish scoped artifact failed")
			return 1
		}
	}
	for index, step := range scenario.Steps {
		result, err := reportFixtureStep(ctx, client, index, step)
		if err != nil || result.IsError {
			fmt.Fprintf(diagnostics, "report fixture step %d failed\n", index)
			return 1
		}
		if scenario.Process.DuplicateReport && index == 0 {
			duplicate, duplicateErr := reportFixtureStep(ctx, client, index, step)
			var originalReport, duplicateReport domain.RunReport
			originalErr := json.Unmarshal(result.StructuredContent, &originalReport)
			duplicateDecodeErr := json.Unmarshal(duplicate.StructuredContent, &duplicateReport)
			if duplicateErr != nil || duplicate.IsError || originalErr != nil || duplicateDecodeErr != nil || originalReport.ID == "" || originalReport.ID != duplicateReport.ID {
				fmt.Fprintln(diagnostics, "duplicate fixture report was not idempotent")
				return 1
			}
		}
		if scenario.Process.StepDelayMillis > 0 {
			time.Sleep(time.Duration(scenario.Process.StepDelayMillis) * time.Millisecond)
		}
	}
	writeNoise(output, 'x', scenario.Process.StdoutNoiseBytes)
	writeNoise(diagnostics, 'e', scenario.Process.StderrNoiseBytes)
	if scenario.Process.HangAfterSteps {
		for {
			time.Sleep(time.Hour)
		}
	}
	return scenario.Process.ExitCode
}

func reportFixtureStep(ctx context.Context, client *mcp.Client, index int, step domain.FakeStep) (mcp.ToolCallResult, error) {
	key := fmt.Sprintf("fixture-step-%d", index)
	switch step.Kind {
	case domain.ObservationProgress:
		return client.CallTool(ctx, "crewfold_report_progress", map[string]any{
			"summary": step.Message, "completed": []string{step.Message}, "next": []string{}, "risks": []string{}, "evidence_ids": step.Evidence, "idempotency_key": key,
		})
	case domain.ObservationBlocked:
		return client.CallTool(ctx, "crewfold_report_blocked", map[string]any{
			"reason": step.Message, "needs": []string{"owner resolution"}, "severity": "blocking", "related_ids": step.Evidence, "idempotency_key": key,
		})
	case domain.ObservationCompletion:
		return client.CallTool(ctx, "crewfold_propose_completion", map[string]any{
			"summary": step.Message, "handoff": step.Handoff, "evidence_ids": step.Evidence,
			"changed_paths": []string{}, "checks": []string{}, "remaining_risks": []string{}, "unknowns": []string{}, "idempotency_key": key,
		})
	default:
		return mcp.ToolCallResult{}, fmt.Errorf("unsupported fixture step %q", step.Kind)
	}
}

func readFixtureScenario(input io.Reader) (domain.FakeScenario, error) {
	data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return domain.FakeScenario{}, errors.New("fixture input is unreadable or exceeds 64 KiB")
	}
	var scenario domain.FakeScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return domain.FakeScenario{}, fmt.Errorf("decode fixture input: %w", err)
	}
	if err := ValidateScenario(scenario); err != nil {
		return domain.FakeScenario{}, fmt.Errorf("validate fixture input: %w", err)
	}
	return scenario, nil
}

func configureFixtureSignals(ignoreTermination bool) {
	if !ignoreTermination {
		return
	}
	ignored := make(chan os.Signal, 4)
	signal.Notify(ignored, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range ignored {
		}
	}()
}

func writeFixtureMetadata(output io.Writer) {
	workingDirectory, _ := os.Getwd()
	environmentNames := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, _, ok := strings.Cut(entry, "="); ok {
			environmentNames = append(environmentNames, name)
		}
	}
	sort.Strings(environmentNames)
	_ = json.NewEncoder(output).Encode(fixtureRuntimeMetadata{Schema: fixtureRuntimeMetadataSchema, WorkingDirectory: workingDirectory, EnvironmentNames: environmentNames})
}
