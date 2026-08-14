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

// RuntimeStatus is the lifecycle-only view of a runtime operation. It omits
// process output so callers that need only mechanical completion state do not
// receive the provider's raw capture as a side effect of inspection.
type RuntimeStatus struct {
	State           string
	ExitCode        int
	ExitKnown       bool
	CompletionReady bool
	Forced          bool
	Diagnostic      string
}

// RuntimeStatusInspector is an optional, narrower runtime capability for
// lifecycle consumers that must not inspect raw provider output. Text output is
// available separately through RuntimeDriver.Logs, which applies the runtime's
// bounds and redaction policy.
type RuntimeStatusInspector interface {
	InspectStatus(context.Context, string, string) (RuntimeStatus, error)
}

// RuntimeLaunchPreparation is the immutable runtime-normalized authority for one
// launch. Durable workers may record this digest before the external effect and
// then require Launch to use the same operation, placement, and specification.
type RuntimeLaunchPreparation struct {
	SpecSHA256 string
}

// RuntimeLaunchPreparer is an optional pre-effect capability. PrepareLaunch must
// be side-effect free: it normalizes and seals the exact effective specification
// that Launch will use, but it must not create runtime state or start a child.
type RuntimeLaunchPreparer interface {
	PrepareLaunch(context.Context, string, domain.RunPlacement, LaunchSpec) (RuntimeLaunchPreparation, error)
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
	Attach(context.Context, string, string) (AttachSpec, error)
}

// ProviderAdapter prepares a provider for a runtime and normalizes its reports.
type ProviderAdapter interface {
	Name() string
	Prepare(context.Context, domain.Run, domain.FakeScenario) (LaunchSpec, error)
	Bind(context.Context, domain.Run, RuntimeBinding) (ProviderBinding, error)
	Next(context.Context, domain.Run, domain.FakeScenario, RuntimeSnapshot) (domain.RunObservation, bool, error)
}
