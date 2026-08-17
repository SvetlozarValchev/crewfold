package localapi

import (
	"context"

	"crewfold/internal/domain"
)

const (
	MethodOwnerCrewConfigure = "owner.crew.configure"
	OwnerCrewMutationSchema  = "urn:crewfold:schema:local-api:owner-crew-mutation-result:v1"
)

type OwnerCrewConfigureParams struct {
	Workspace               string `json:"workspace"`
	Project                 string `json:"project"`
	Action                  string `json:"action"`
	ExpectedBindingRevision int64  `json:"expected_binding_revision"`
	Name                    string `json:"name,omitempty"`
	Agent                   string `json:"agent,omitempty"`
	Provider                string `json:"provider,omitempty"`
	Runtime                 string `json:"runtime,omitempty"`
	MaxConcurrency          int    `json:"max_concurrency,omitempty"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type OwnerCrewMutationResult struct {
	Schema         string                       `json:"schema"`
	Type           string                       `json:"type"`
	Action         string                       `json:"action"`
	Binding        domain.OwnerExecutiveBinding `json:"binding"`
	Agent          domain.AgentDefinition       `json:"agent"`
	WorkerProfiles []domain.LaunchProfile       `json:"worker_profiles"`
	EventSequence  int64                        `json:"event_sequence"`
}

func (c *Client) OwnerCrewConfigure(ctx context.Context, params OwnerCrewConfigureParams) (OwnerCrewMutationResult, error) {
	var result OwnerCrewMutationResult
	err := c.callParamsStrict(ctx, MethodOwnerCrewConfigure, params, &result)
	return result, err
}
