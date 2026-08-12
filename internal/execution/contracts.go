// Package execution defines provider-neutral run adapter contracts.
package execution

import (
	"context"
	"time"

	"crewfold/internal/domain"
)

const FakeScenarioSchema = "urn:crewfold:schema:fixture:fake-run-scenario:v1"

type LaunchSpec struct {
	Scenario domain.FakeScenario
	Command  *CommandSpec
}

type CommandSpec struct {
	Executable      string
	Arguments       []string
	StandardInput   []byte
	Environment     map[string]string
	Timeout         time.Duration
	OutputByteLimit int64
}

type RuntimeBinding struct {
	RuntimeHandle string
}

type ProviderBinding struct {
	ProviderHandle string
}

type RunCapabilityAccess struct {
	SocketPath     string
	CapabilityFile string
}

type RunCapabilityPreparer interface {
	PrepareRunCapability(context.Context, string) (RunCapabilityAccess, error)
}

const (
	RuntimeStateStarting = "starting"
	RuntimeStateRunning  = "running"
	RuntimeStateExited   = "exited"
	RuntimeStateStopped  = "stopped"
	RuntimeStateTimedOut = "timed_out"
	RuntimeStateUnknown  = "unknown"
)

type RuntimeSnapshot struct {
	State           string
	ExitCode        int
	ExitKnown       bool
	CompletionReady bool
	Forced          bool
	Diagnostic      string
	Stdout          domain.CapturedLog
	Stderr          domain.CapturedLog
}

type StopSpec struct {
	GracePeriod time.Duration
}

type StopResult struct {
	Forced     bool
	Diagnostic string
}

// RuntimeUnavailableError identifies a temporary control-plane outage. The
// durable worker may retry it; it must not reinterpret the outage as process
// completion.
type RuntimeUnavailableError struct{ Message string }

func (e *RuntimeUnavailableError) Error() string { return e.Message }

type AttachSpec struct {
	Executable  string            `json:"executable"`
	Arguments   []string          `json:"arguments"`
	Environment map[string]string `json:"environment,omitempty"`
}

// RuntimeDriver controls where execution occurs. Launch must be idempotent for a
// stable operation ID; reconciliation must never invent completion authority.
type RuntimeDriver interface {
	Name() string
	Launch(context.Context, string, domain.RunPlacement, LaunchSpec) (RuntimeBinding, error)
	Reconcile(context.Context, string, string) (RuntimeBinding, error)
	Inspect(context.Context, string, string) (RuntimeSnapshot, error)
	Stop(context.Context, string, string, StopSpec) (StopResult, error)
	Logs(context.Context, string, string, int) (domain.RunLogs, error)
}

// Optional runtime capabilities keep the core lifecycle provider-neutral while
// allowing interactive runtimes to expose richer controls.
type RuntimePrompter interface {
	Prompt(context.Context, string, string, string) error
}

type RuntimeInterrupter interface {
	Interrupt(context.Context, string, string) error
}

type RuntimeAttacher interface {
	Attach(context.Context, string, string, bool) (AttachSpec, error)
}

// ProviderAdapter prepares a provider for a runtime and normalizes its reports.
type ProviderAdapter interface {
	Name() string
	Prepare(context.Context, domain.Run, domain.FakeScenario) (LaunchSpec, error)
	Bind(context.Context, domain.Run, RuntimeBinding) (ProviderBinding, error)
	Next(context.Context, domain.Run, domain.FakeScenario, RuntimeSnapshot) (domain.RunObservation, bool, error)
}
