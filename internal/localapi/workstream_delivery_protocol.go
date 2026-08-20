package localapi

import "crewfold/internal/domain"

const (
	MethodWorkstreamDeliveryShow   = "workstream.delivery.show"
	MethodWorkstreamDeliveryAccept = "workstream.delivery.accept"
	MethodWorkstreamDeliveryReject = "workstream.delivery.reject"

	WorkstreamDeliveryShowSchema     = "urn:crewfold:schema:local-api:workstream-delivery-show-result:v1"
	WorkstreamDeliveryMutationSchema = "urn:crewfold:schema:local-api:workstream-delivery-mutation-result:v1"
)

type WorkstreamDeliveryQueryParams struct {
	Workspace string `json:"workspace"`
	Objective string `json:"objective"`
}

type WorkstreamDeliveryDecisionParams struct {
	Workspace                 string `json:"workspace"`
	Objective                 string `json:"objective"`
	ExpectedObjectiveRevision int64  `json:"expected_objective_revision"`
	ExpectedSHA256            string `json:"expected_sha256"`
	Reason                    string `json:"reason,omitempty"`
	IdempotencyKey            string `json:"idempotency_key"`
}

type WorkstreamDeliveryShowResult struct {
	Schema   string                    `json:"schema"`
	Type     string                    `json:"type"`
	Delivery domain.WorkstreamDelivery `json:"delivery"`
}

type WorkstreamDeliveryMutationResult struct {
	Schema        string                    `json:"schema"`
	Type          string                    `json:"type"`
	Delivery      domain.WorkstreamDelivery `json:"delivery"`
	EventSequence int64                     `json:"event_sequence"`
}
