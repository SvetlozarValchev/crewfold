package execution

import (
	"bufio"
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
)

const fixtureReportSchema = "urn:crewfold:fixture-report:v1"
const fixtureRuntimeMetadataSchema = "urn:crewfold:fixture-runtime-metadata:v1"

// FixtureProvider turns the deterministic scenario into a real child process.
// Executable and Arguments are configurable so component tests can use a helper
// process; production defaults to the current Crewfold binary's hidden worker.
type FixtureProvider struct {
	Executable string
	Arguments  []string
}

func NewFixtureProvider() FixtureProvider {
	executable, _ := os.Executable()
	return FixtureProvider{Executable: executable, Arguments: []string{"__fixture-provider"}}
}

func (FixtureProvider) Name() string { return "fixture" }

func (provider FixtureProvider) Prepare(_ context.Context, _ domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	if scenario.StartFailure != "" {
		return LaunchSpec{}, &StartError{Message: scenario.StartFailure}
	}
	if strings.TrimSpace(provider.Executable) == "" {
		return LaunchSpec{}, errors.New("fixture provider executable is unavailable")
	}
	input, err := json.Marshal(scenario)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("encode fixture scenario: %w", err)
	}
	command := &CommandSpec{
		Executable:      provider.Executable,
		Arguments:       append([]string(nil), provider.Arguments...),
		StandardInput:   input,
		Timeout:         time.Duration(scenario.Process.TimeoutMillis) * time.Millisecond,
		OutputByteLimit: 64 * 1024,
	}
	return LaunchSpec{Scenario: scenario, Command: command}, nil
}

func (FixtureProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return ProviderBinding{}, errors.New("cannot bind fixture provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: "fixture-provider:" + run.ID}, nil
}

func (FixtureProvider) Next(_ context.Context, run domain.Run, scenario domain.FakeScenario, snapshot RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	reports, err := decodeFixtureReports(snapshot.Stdout.Text)
	if err != nil {
		return domain.RunObservation{}, false, err
	}
	if run.StepCursor < 0 || run.StepCursor >= len(reports) {
		return domain.RunObservation{}, false, nil
	}
	step := reports[run.StepCursor]
	return domain.RunObservation{
		Kind:     step.Kind,
		Message:  step.Message,
		Evidence: append([]string(nil), step.Evidence...),
		Handoff:  step.Handoff,
		Pause:    step.WaitForResume,
	}, true, nil
}

type fixtureReport struct {
	Schema string          `json:"schema"`
	Step   domain.FakeStep `json:"step"`
}

type fixtureRuntimeMetadata struct {
	Schema           string   `json:"schema"`
	WorkingDirectory string   `json:"working_directory"`
	EnvironmentNames []string `json:"environment_names"`
}

func decodeFixtureReports(output string) ([]domain.FakeStep, error) {
	reports := make([]domain.FakeStep, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 128*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var header struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(line, &header); err != nil || header.Schema != fixtureReportSchema {
			continue
		}
		var report fixtureReport
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil {
			return nil, fmt.Errorf("decode fixture provider report: %w", err)
		}
		if report.Step.Kind != domain.ObservationProgress && report.Step.Kind != domain.ObservationBlocked && report.Step.Kind != domain.ObservationCompletion {
			return nil, fmt.Errorf("fixture provider reported unsupported kind %q", report.Step.Kind)
		}
		reports = append(reports, report.Step)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan fixture provider output: %w", err)
	}
	return reports, nil
}

// RunFixtureProvider executes the provider-free child used by direct-runtime
// tests. It reads one bounded scenario from stdin and emits normalized JSON lines.
func RunFixtureProvider(input io.Reader, output, diagnostics io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		fmt.Fprintln(diagnostics, "fixture input is unreadable or exceeds 64 KiB")
		return 2
	}
	var scenario domain.FakeScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		fmt.Fprintf(diagnostics, "decode fixture input: %v\n", err)
		return 2
	}
	if err := ValidateScenario(scenario); err != nil {
		fmt.Fprintf(diagnostics, "validate fixture input: %v\n", err)
		return 2
	}

	if scenario.Process.IgnoreTermination {
		ignored := make(chan os.Signal, 4)
		signal.Notify(ignored, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ignored)
		go func() {
			for range ignored {
			}
		}()
	}

	encoder := json.NewEncoder(output)
	workingDirectory, _ := os.Getwd()
	environmentNames := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, _, ok := strings.Cut(entry, "="); ok {
			environmentNames = append(environmentNames, name)
		}
	}
	sort.Strings(environmentNames)
	if err := encoder.Encode(fixtureRuntimeMetadata{Schema: fixtureRuntimeMetadataSchema, WorkingDirectory: workingDirectory, EnvironmentNames: environmentNames}); err != nil {
		fmt.Fprintf(diagnostics, "write fixture runtime metadata: %v\n", err)
		return 1
	}
	for _, step := range scenario.Steps {
		if err := encoder.Encode(fixtureReport{Schema: fixtureReportSchema, Step: step}); err != nil {
			fmt.Fprintf(diagnostics, "write fixture report: %v\n", err)
			return 1
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

func writeNoise(writer io.Writer, value byte, count int) {
	block := bytes.Repeat([]byte{value}, 4096)
	for count > 0 {
		length := len(block)
		if count < length {
			length = count
		}
		_, _ = writer.Write(block[:length])
		count -= length
	}
}
