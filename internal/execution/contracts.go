// Package execution defines provider-neutral run adapter contracts.
package execution

import (
	"context"

	"crewfold/internal/domain"
)

const FakeScenarioSchema = "urn:crewfold:schema:fixture:fake-run-scenario:v1"

type LaunchSpec struct {
	Scenario domain.FakeScenario
}

type RuntimeBinding struct {
	RuntimeHandle string
}

type ProviderBinding struct {
	ProviderHandle string
}

// RuntimeDriver controls where execution occurs. Launch must be idempotent for a
// stable operation ID; reconciliation must never invent completion authority.
type RuntimeDriver interface {
	Name() string
	Launch(context.Context, string, domain.RunPlacement, LaunchSpec) (RuntimeBinding, error)
	Reconcile(context.Context, string, string) (RuntimeBinding, error)
}

// ProviderAdapter prepares a provider for a runtime and normalizes its reports.
type ProviderAdapter interface {
	Name() string
	Prepare(context.Context, domain.Run, domain.FakeScenario) (LaunchSpec, error)
	Bind(context.Context, domain.Run, RuntimeBinding) (ProviderBinding, error)
	Next(context.Context, domain.Run, domain.FakeScenario) (domain.RunObservation, bool, error)
}
