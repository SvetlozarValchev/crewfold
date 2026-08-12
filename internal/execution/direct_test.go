package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestDirectEnvironmentIsAllowlistedAndRejectsSecretOverrides(t *testing.T) {
	t.Parallel()

	environment, err := buildDirectEnvironment([]string{
		"PATH=/usr/bin", "LANG=C", "HOME=/private/home", "API_TOKEN=must-not-pass",
	}, map[string]string{"LANG": "en_US.UTF-8"}, "run_test")
	if err != nil {
		t.Fatalf("buildDirectEnvironment() error = %v", err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "LANG=en_US.UTF-8") || !strings.Contains(joined, "CREWFOLD_RUN_ID=run_test") {
		t.Fatalf("allowlisted environment = %q", joined)
	}
	if strings.Contains(joined, "HOME=") || strings.Contains(joined, "API_TOKEN") || strings.Contains(joined, "must-not-pass") {
		t.Fatalf("environment leaked a non-allowlisted value: %q", joined)
	}
	if _, err := buildDirectEnvironment(nil, map[string]string{"PASSWORD": "not-allowed"}, "run_test"); err == nil {
		t.Fatal("secret override error = nil, want allowlist rejection")
	}
}

func TestBoundedCaptureReportsOmittedBytes(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	capture := &boundedCapture{writer: buffer, limit: 5}
	if written, err := capture.Write([]byte("abcdefghij")); err != nil || written != 10 {
		t.Fatalf("Write(first) = %d, %v", written, err)
	}
	if written, err := capture.Write([]byte("zz")); err != nil || written != 2 {
		t.Fatalf("Write(second) = %d, %v", written, err)
	}
	if buffer.String() != "abcde" || capture.captured != 5 || capture.omitted != 7 {
		t.Fatalf("capture = text %q captured %d omitted %d", buffer.String(), capture.captured, capture.omitted)
	}
}

func TestDirectLogRedactionPreservesDiagnosisWithoutSecretValue(t *testing.T) {
	t.Parallel()

	redacted := redactDirectOutput("API_TOKEN=top-secret\nAuthorization: Bearer credential\npassword: hunter2\n")
	for _, secret := range []string{"top-secret", "credential", "hunter2"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted output %q contains %q", redacted, secret)
		}
	}
	if strings.Count(redacted, "[REDACTED]") != 3 {
		t.Fatalf("redacted output = %q", redacted)
	}
}

func TestFixtureProviderRunsScenarioAsStructuredProcessReports(t *testing.T) {
	t.Parallel()

	scenario := domain.FakeScenario{
		Schema: FakeScenarioSchema,
		Name:   "direct-reports",
		Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "working"},
			{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review"},
		},
		Process: domain.FixtureProcess{StdoutNoiseBytes: 16, StderrNoiseBytes: 8},
	}
	input := &bytes.Buffer{}
	if err := jsonEncode(input, scenario); err != nil {
		t.Fatalf("encode scenario: %v", err)
	}
	output, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	if code := RunFixtureProvider(input, output, diagnostics); code != 0 {
		t.Fatalf("RunFixtureProvider() code = %d, stderr = %q", code, diagnostics.String())
	}
	reports, err := decodeFixtureReports(output.String())
	if err != nil || len(reports) != 2 || reports[1].Kind != domain.ObservationCompletion {
		t.Fatalf("decodeFixtureReports() = %#v, %v", reports, err)
	}
	provider := FixtureProvider{Executable: "/fixture", Arguments: []string{"worker"}}
	spec, err := provider.Prepare(context.Background(), domain.Run{}, scenario)
	if err != nil || spec.Command == nil || spec.Command.Executable != "/fixture" || spec.Command.OutputByteLimit == 0 {
		t.Fatalf("Prepare() = %#v, %v", spec, err)
	}
	observation, found, err := provider.Next(context.Background(), domain.Run{StepCursor: 1}, scenario, RuntimeSnapshot{Stdout: domain.CapturedLog{Text: output.String()}})
	if err != nil || !found || observation.Kind != domain.ObservationCompletion || observation.Handoff != "review" {
		t.Fatalf("Next() = %#v, %t, %v", observation, found, err)
	}
}

func TestFixtureProviderMapsStartFailureBeforeLaunchingAProcess(t *testing.T) {
	t.Parallel()

	provider := NewFixtureProvider()
	_, err := provider.Prepare(context.Background(), domain.Run{}, domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "refused", StartFailure: "fixture refused",
	})
	var startError *StartError
	if !errors.As(err, &startError) || startError.Error() != "fixture refused" {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestFixtureTimeoutIsCarriedIntoDirectCommand(t *testing.T) {
	t.Parallel()

	provider := FixtureProvider{Executable: "/fixture"}
	spec, err := provider.Prepare(context.Background(), domain.Run{}, domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "timeout", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}},
		Process: domain.FixtureProcess{TimeoutMillis: 250},
	})
	if err != nil || spec.Command.Timeout != 250*time.Millisecond {
		t.Fatalf("Prepare() timeout = %v, error = %v", spec.Command.Timeout, err)
	}
}

func TestDirectInspectionReportsUnknownWhenRecordedSupervisorDisappears(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runID := "run_missing_supervisor"
	runDirectory := filepath.Join(root, runID)
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := writeDirectState(runDirectory, directSupervisorState{
		Schema:          directSupervisorStateSchema,
		OperationID:     runID,
		Status:          RuntimeStateRunning,
		SupervisorPID:   2147483647,
		SupervisorStart: "unreachable",
	}); err != nil {
		t.Fatalf("writeDirectState() error = %v", err)
	}
	runtime := NewDirectRuntime(DirectRuntimeOptions{StateRoot: root})
	_, stopErr := runtime.Stop(context.Background(), runID, directHandle(runID), StopSpec{GracePeriod: time.Millisecond})
	var unknownError *OutcomeUnknownError
	if !errors.As(stopErr, &unknownError) {
		t.Fatalf("Stop() error = %v, want OutcomeUnknownError", stopErr)
	}
	snapshot, err := runtime.Inspect(context.Background(), runID, directHandle(runID))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != RuntimeStateUnknown || snapshot.CompletionReady || !strings.Contains(snapshot.Diagnostic, "disappeared") {
		t.Fatalf("Inspect() snapshot = %#v", snapshot)
	}
}

func jsonEncode(buffer *bytes.Buffer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = buffer.Write(data)
	return err
}
