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

// ProviderAdapter prepares a provider for a runtime and normalizes its reports.
type ProviderAdapter interface {
	Name() string
	Prepare(context.Context, domain.Run, domain.FakeScenario) (LaunchSpec, error)
	Bind(context.Context, domain.Run, RuntimeBinding) (ProviderBinding, error)
	Next(context.Context, domain.Run, domain.FakeScenario, RuntimeSnapshot) (domain.RunObservation, bool, error)
}
