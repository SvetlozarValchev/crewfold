package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	"crewfold/internal/domain"
)

var scenarioNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type StartError struct{ Message string }

func (e *StartError) Error() string { return e.Message }

type OutcomeUnknownError struct{ Message string }

func (e *OutcomeUnknownError) Error() string { return e.Message }

type FakeRuntime struct {
	mu       sync.Mutex
	launches map[string]RuntimeBinding
	stopped  map[string]bool
}

func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{launches: make(map[string]RuntimeBinding), stopped: make(map[string]bool)}
}

func (runtime *FakeRuntime) Name() string { return "fake" }

func (runtime *FakeRuntime) Launch(_ context.Context, operationID string, _ domain.RunPlacement, spec LaunchSpec) (RuntimeBinding, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if binding, exists := runtime.launches[operationID]; exists {
		return binding, nil
	}
	if spec.Command != nil {
		return RuntimeBinding{}, &StartError{Message: "fake runtime cannot execute a command specification"}
	}
	if spec.Scenario.StartFailure != "" {
		return RuntimeBinding{}, &StartError{Message: spec.Scenario.StartFailure}
	}
	binding := RuntimeBinding{RuntimeHandle: "fake-runtime:" + operationID}
	runtime.launches[operationID] = binding
	return binding, nil
}

func (runtime *FakeRuntime) Reconcile(_ context.Context, operationID, handle string) (RuntimeBinding, error) {
	if strings.TrimSpace(handle) == "" {
		return RuntimeBinding{}, errors.New("fake runtime handle is empty")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	binding := RuntimeBinding{RuntimeHandle: handle}
	runtime.launches[operationID] = binding
	return binding, nil
}

func (runtime *FakeRuntime) Inspect(_ context.Context, operationID, handle string) (RuntimeSnapshot, error) {
	if strings.TrimSpace(handle) == "" {
		return RuntimeSnapshot{}, errors.New("fake runtime handle is empty")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.stopped[operationID] {
		return RuntimeSnapshot{State: RuntimeStateStopped, ExitCode: 0, ExitKnown: true}, nil
	}
	return RuntimeSnapshot{State: RuntimeStateRunning, CompletionReady: true}, nil
}

func (runtime *FakeRuntime) Stop(_ context.Context, operationID, handle string, _ StopSpec) (StopResult, error) {
	if strings.TrimSpace(handle) == "" {
		return StopResult{}, errors.New("fake runtime handle is empty")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.stopped[operationID] = true
	return StopResult{Diagnostic: "fake runtime stopped"}, nil
}

func (runtime *FakeRuntime) Logs(_ context.Context, operationID, handle string, _ int) (domain.RunLogs, error) {
	if strings.TrimSpace(handle) == "" {
		return domain.RunLogs{}, errors.New("fake runtime handle is empty")
	}
	return domain.RunLogs{RunID: operationID, State: RuntimeStateRunning}, nil
}

func (runtime *FakeRuntime) LaunchCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.launches)
}

type FakeProvider struct{}

func (FakeProvider) Name() string { return "fake" }

func (FakeProvider) Prepare(_ context.Context, _ domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	return LaunchSpec{Scenario: scenario}, nil
}

func (FakeProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return ProviderBinding{}, errors.New("cannot bind fake provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: "fake-provider:" + run.ID}, nil
}

func (FakeProvider) Next(_ context.Context, run domain.Run, scenario domain.FakeScenario, _ RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	if run.StepCursor < 0 || run.StepCursor >= len(scenario.Steps) {
		return domain.RunObservation{}, false, nil
	}
	step := scenario.Steps[run.StepCursor]
	return domain.RunObservation{Kind: step.Kind, Message: step.Message, Evidence: append([]string(nil), step.Evidence...), Handoff: step.Handoff, Pause: step.WaitForResume}, true, nil
}

func LoadScenario(path string) (domain.FakeScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.FakeScenario{}, fmt.Errorf("read fake scenario: %w", err)
	}
	if len(data) > 64*1024 {
		return domain.FakeScenario{}, errors.New("fake scenario exceeds 64 KiB")
	}
	var scenario domain.FakeScenario
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return domain.FakeScenario{}, fmt.Errorf("decode fake scenario: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.FakeScenario{}, errors.New("fake scenario contains more than one JSON value")
		}
		return domain.FakeScenario{}, fmt.Errorf("decode fake scenario trailing data: %w", err)
	}
	if err := ValidateScenario(scenario); err != nil {
		return domain.FakeScenario{}, err
	}
	return scenario, nil
}

func ValidateScenario(scenario domain.FakeScenario) error {
	if scenario.Schema != FakeScenarioSchema {
		return fmt.Errorf("fake scenario schema must be %q", FakeScenarioSchema)
	}
	if !scenarioNamePattern.MatchString(scenario.Name) {
		return errors.New("fake scenario name must be lowercase letters, digits, or hyphens")
	}
	if len(scenario.StartFailure) > 1024 || len(scenario.Steps) > 32 || (scenario.StartFailure == "" && len(scenario.Steps) == 0) {
		return errors.New("fake scenario must contain 1 to 32 steps or one bounded start failure")
	}
	if scenario.StartFailure != "" && len(scenario.Steps) != 0 {
		return errors.New("start-failure scenarios cannot contain observation steps")
	}
	for _, item := range scenario.Acceptance.RequiredEvidence {
		if !validEvidence(item) {
			return errors.New("acceptance evidence names must contain 1 to 128 printable characters")
		}
	}
	if scenario.Process.ExitCode < 0 || scenario.Process.ExitCode > 125 || scenario.Process.StepDelayMillis < 0 || scenario.Process.StepDelayMillis > 5000 || scenario.Process.TimeoutMillis < 0 || scenario.Process.TimeoutMillis > 60000 || scenario.Process.StdoutNoiseBytes < 0 || scenario.Process.StdoutNoiseBytes > 2*1024*1024 || scenario.Process.StderrNoiseBytes < 0 || scenario.Process.StderrNoiseBytes > 2*1024*1024 {
		return errors.New("fixture process controls exceed their bounded limits")
	}
	if scenario.Process.IgnoreTermination && !scenario.Process.HangAfterSteps {
		return errors.New("fixture process can ignore termination only while hanging")
	}
	if scenario.StartFailure != "" && scenario.Process != (domain.FixtureProcess{}) {
		return errors.New("start-failure scenarios cannot contain process controls")
	}
	if scenario.StartFailure != "" && scenario.Mailbox != (domain.FixtureMailbox{}) {
		return errors.New("start-failure scenarios cannot contain mailbox controls")
	}
	if err := validateFixtureMailbox(scenario.Mailbox); err != nil {
		return err
	}
	for index, step := range scenario.Steps {
		if step.Kind != domain.ObservationProgress && step.Kind != domain.ObservationBlocked && step.Kind != domain.ObservationCompletion {
			return fmt.Errorf("fake scenario step %d has unsupported kind %q", index, step.Kind)
		}
		if len(step.Message) > 1024 || len(step.Handoff) > 4096 || len(step.Evidence) > 32 {
			return fmt.Errorf("fake scenario step %d exceeds a field limit", index)
		}
		for _, item := range step.Evidence {
			if !validEvidence(item) {
				return fmt.Errorf("fake scenario step %d has invalid evidence", index)
			}
		}
		if step.Kind == domain.ObservationBlocked && strings.TrimSpace(step.Message) == "" {
			return fmt.Errorf("fake scenario blocked step %d requires a question", index)
		}
		if step.Kind == domain.ObservationCompletion && strings.TrimSpace(step.Message) == "" {
			return fmt.Errorf("fake scenario completion step %d requires a summary", index)
		}
		if step.Kind == domain.ObservationCompletion && strings.TrimSpace(step.Handoff) == "" {
			return fmt.Errorf("fake scenario completion step %d requires a handoff", index)
		}
		if step.WaitForResume && step.Kind != domain.ObservationProgress {
			return fmt.Errorf("fake scenario step %d can wait for resume only after progress", index)
		}
	}
	return nil
}

func validateFixtureMailbox(mailbox domain.FixtureMailbox) error {
	if mailbox == (domain.FixtureMailbox{}) {
		return nil
	}
	if mailbox.WaitTimeoutMillis < 0 || mailbox.WaitTimeoutMillis > 30000 {
		return errors.New("fixture mailbox wait must not exceed 30 seconds")
	}
	if mailbox.Reply != nil && mailbox.WaitForKind == "" {
		return errors.New("fixture mailbox reply requires an incoming message kind")
	}
	if mailbox.AcknowledgeReceived && mailbox.WaitForKind == "" {
		return errors.New("fixture mailbox acknowledgement requires an incoming message kind")
	}
	if mailbox.RequireInboxSummary && mailbox.WaitForKind == "" {
		return errors.New("fixture inbox summary assertion requires an incoming message kind")
	}
	if mailbox.DeniedArtifactProbe && (mailbox.Send == nil || strings.TrimSpace(mailbox.Send.ThreadID) == "") {
		return errors.New("fixture denied artifact probe requires an initial participant-thread send")
	}
	for label, message := range map[string]*domain.FixtureMailboxMessage{"send": mailbox.Send, "reply": mailbox.Reply} {
		if message == nil {
			continue
		}
		if label == "send" && strings.TrimSpace(message.RecipientAgent) == "" {
			return errors.New("fixture mailbox send requires one recipient agent")
		}
		if label == "reply" && strings.TrimSpace(message.ThreadID) != "" {
			return errors.New("fixture mailbox reply inherits its incoming thread")
		}
		if message.ThreadID != "" && (!strings.HasPrefix(message.ThreadID, "thread_") || len(message.ThreadID) > 128 || strings.ContainsAny(message.ThreadID, "\r\n\x00")) {
			return fmt.Errorf("fixture mailbox %s contains an invalid thread ID", label)
		}
		if !supportedFixtureMessageKind(message.Kind) || strings.TrimSpace(message.Body) == "" || len(message.Body) > 4096 || len(message.Subject) > 160 {
			return fmt.Errorf("fixture mailbox %s contains invalid or unbounded fields", label)
		}
	}
	if mailbox.WaitForKind != "" && !supportedFixtureMessageKind(mailbox.WaitForKind) {
		return errors.New("fixture mailbox wait kind is unsupported")
	}
	for _, probe := range []string{mailbox.DeniedRecipientProbe, mailbox.OversizedRecipientProbe} {
		if len(probe) > 128 || strings.ContainsAny(probe, "\r\n\x00") {
			return errors.New("fixture mailbox recipient probe is invalid")
		}
	}
	return nil
}

func supportedFixtureMessageKind(kind string) bool {
	switch kind {
	case domain.MessageInform, domain.MessageQuestion, domain.MessageRequest, domain.MessageReviewRequest,
		domain.MessageHandoff, domain.MessageDecisionNotice, domain.MessageRisk, domain.MessageConflict,
		domain.MessageApprovalRequest:
		return true
	default:
		return false
	}
}

func AcceptancePasses(rule domain.AcceptanceRule, evidence []string) (bool, []string) {
	observed := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		observed[item] = struct{}{}
	}
	missing := make([]string, 0)
	for _, required := range rule.RequiredEvidence {
		if _, exists := observed[required]; !exists {
			missing = append(missing, required)
		}
	}
	return len(missing) == 0, missing
}

func validEvidence(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}
