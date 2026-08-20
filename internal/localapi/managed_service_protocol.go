package localapi

import "crewfold/internal/domain"

const (
	MethodManagedServiceDefinitionCreate = "managed_service.definition.create"
	MethodManagedServiceDefinitionRetire = "managed_service.definition.retire"
	MethodManagedServiceDefinitionShow   = "managed_service.definition.show"
	MethodManagedServiceDefinitionList   = "managed_service.definition.list"
	MethodManagedServiceStart            = "managed_service.start"
	MethodManagedServiceShow             = "managed_service.show"
	MethodManagedServiceList             = "managed_service.list"
	MethodManagedServiceStop             = "managed_service.stop"
	MethodManagedServiceRestart          = "managed_service.restart"
	MethodManagedServiceResolveUnknown   = "managed_service.resolve_unknown"
	MethodManagedServiceLogs             = "managed_service.logs"
	MethodManagedServiceGrantCreate      = "managed_service.grant.create"
	MethodManagedServiceGrantRevoke      = "managed_service.grant.revoke"
	MethodManagedServiceGrantList        = "managed_service.grant.list"
	MethodManagedServiceRequestList      = "managed_service.request.list"
	MethodManagedServiceRequestAccept    = "managed_service.request.accept"
	MethodManagedServiceRequestReject    = "managed_service.request.reject"

	ManagedServiceDefinitionMutationSchema = "urn:crewfold:schema:local-api:managed-service-definition-mutation-result:v1"
	ManagedServiceDefinitionShowSchema     = "urn:crewfold:schema:local-api:managed-service-definition-show-result:v1"
	ManagedServiceDefinitionListSchema     = "urn:crewfold:schema:local-api:managed-service-definition-list-result:v1"
	ManagedServiceMutationSchema           = "urn:crewfold:schema:local-api:managed-service-mutation-result:v1"
	ManagedServiceShowSchema               = "urn:crewfold:schema:local-api:managed-service-show-result:v1"
	ManagedServiceListSchema               = "urn:crewfold:schema:local-api:managed-service-list-result:v1"
	ManagedServiceLogsSchema               = "urn:crewfold:schema:local-api:managed-service-logs-result:v1"
	ManagedServiceGrantMutationSchema      = "urn:crewfold:schema:local-api:managed-service-grant-mutation-result:v1"
	ManagedServiceGrantListSchema          = "urn:crewfold:schema:local-api:managed-service-grant-list-result:v1"
	ManagedServiceRequestListSchema        = "urn:crewfold:schema:local-api:managed-service-request-list-result:v1"
	ManagedServiceRequestMutationSchema    = "urn:crewfold:schema:local-api:managed-service-request-mutation-result:v1"
)

type ManagedServiceDefinitionCreateParams struct {
	Workspace             string                                     `json:"workspace"`
	Project               string                                     `json:"project"`
	Workstream            string                                     `json:"workstream,omitempty"`
	Checkout              string                                     `json:"checkout"`
	Name                  string                                     `json:"name"`
	Description           string                                     `json:"description"`
	Executable            string                                     `json:"executable"`
	Arguments             []string                                   `json:"arguments"`
	WorkingDirectory      string                                     `json:"working_directory"`
	Environment           []domain.ManagedServiceEnvironmentVariable `json:"environment"`
	Profile               string                                     `json:"profile"`
	ProfileRevision       int64                                      `json:"profile_revision"`
	NetworkMode           string                                     `json:"network_mode"`
	Health                domain.ManagedServiceHealthCheck           `json:"health"`
	RestartPolicy         string                                     `json:"restart_policy"`
	MaximumRestarts       int                                        `json:"maximum_restarts"`
	RestartCooldownMillis int64                                      `json:"restart_cooldown_millis"`
	StopSignal            string                                     `json:"stop_signal"`
	StopGraceMillis       int64                                      `json:"stop_grace_millis"`
	OutputByteLimit       int64                                      `json:"output_byte_limit"`
	CapacityClass         string                                     `json:"capacity_class"`
	IdempotencyKey        string                                     `json:"idempotency_key"`
}

type ManagedServiceDefinitionRetireParams struct {
	Workspace        string `json:"workspace"`
	Definition       string `json:"definition"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagedServiceDefinitionQueryParams struct {
	Workspace  string `json:"workspace"`
	Definition string `json:"definition,omitempty"`
	Project    string `json:"project,omitempty"`
	Workstream string `json:"workstream,omitempty"`
	Checkout   string `json:"checkout,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ManagedServiceStartParams struct {
	Workspace        string `json:"workspace"`
	Definition       string `json:"definition"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagedServiceActionParams struct {
	Workspace        string `json:"workspace"`
	Instance         string `json:"instance"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagedServiceResolveUnknownParams struct {
	Workspace               string `json:"workspace"`
	Instance                string `json:"instance"`
	ExpectedRevision        int64  `json:"expected_revision"`
	RuntimeRetiredConfirmed bool   `json:"runtime_retired_confirmed"`
	Reason                  string `json:"reason"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type ManagedServiceQueryParams struct {
	Workspace  string `json:"workspace"`
	Instance   string `json:"instance,omitempty"`
	Project    string `json:"project,omitempty"`
	Workstream string `json:"workstream,omitempty"`
	Checkout   string `json:"checkout,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ManagedServiceLogsParams struct {
	Workspace string `json:"workspace"`
	Instance  string `json:"instance"`
}

type ManagedServiceGrantCreateParams struct {
	Workspace                  string   `json:"workspace"`
	Definition                 string   `json:"definition"`
	ExpectedDefinitionRevision int64    `json:"expected_definition_revision"`
	ManagerAgent               string   `json:"manager_agent"`
	ExpectedMembershipRevision int64    `json:"expected_membership_revision"`
	Actions                    []string `json:"actions"`
	MaximumInstances           int      `json:"maximum_instances"`
	ExpiresAt                  string   `json:"expires_at,omitempty"`
	IdempotencyKey             string   `json:"idempotency_key"`
}

type ManagedServiceGrantRevokeParams struct {
	Workspace        string `json:"workspace"`
	Grant            string `json:"grant"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagedServiceGrantQueryParams struct {
	Workspace  string `json:"workspace"`
	Project    string `json:"project,omitempty"`
	Manager    string `json:"manager,omitempty"`
	Definition string `json:"definition,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ManagedServiceRequestQueryParams struct {
	Workspace  string `json:"workspace"`
	Project    string `json:"project,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Definition string `json:"definition,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ManagedServiceRequestDecisionParams struct {
	Workspace        string `json:"workspace"`
	Request          string `json:"request"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagedServiceDefinitionMutationResult struct {
	Schema        string                          `json:"schema"`
	Type          string                          `json:"type"`
	Definition    domain.ManagedServiceDefinition `json:"definition"`
	EventSequence int64                           `json:"event_sequence"`
}

type ManagedServiceDefinitionShowResult struct {
	Schema     string                          `json:"schema"`
	Type       string                          `json:"type"`
	Definition domain.ManagedServiceDefinition `json:"definition"`
}

type ManagedServiceDefinitionListResult struct {
	Schema      string                            `json:"schema"`
	Type        string                            `json:"type"`
	Definitions []domain.ManagedServiceDefinition `json:"definitions"`
}

type ManagedServiceMutationResult struct {
	Schema        string                        `json:"schema"`
	Type          string                        `json:"type"`
	Instance      domain.ManagedServiceInstance `json:"instance"`
	EventSequence int64                         `json:"event_sequence"`
}

type ManagedServiceShowResult struct {
	Schema string                      `json:"schema"`
	Type   string                      `json:"type"`
	Detail domain.ManagedServiceDetail `json:"detail"`
}

type ManagedServiceListResult struct {
	Schema    string                          `json:"schema"`
	Type      string                          `json:"type"`
	Instances []domain.ManagedServiceInstance `json:"instances"`
}

type ManagedServiceLogsResult struct {
	Schema string                    `json:"schema"`
	Type   string                    `json:"type"`
	Logs   domain.ManagedServiceLogs `json:"logs"`
}

type ManagedServiceGrantMutationResult struct {
	Schema        string                     `json:"schema"`
	Type          string                     `json:"type"`
	Grant         domain.ManagedServiceGrant `json:"grant"`
	EventSequence int64                      `json:"event_sequence"`
}

type ManagedServiceGrantListResult struct {
	Schema string                       `json:"schema"`
	Type   string                       `json:"type"`
	Grants []domain.ManagedServiceGrant `json:"grants"`
}

type ManagedServiceRequestListResult struct {
	Schema   string                         `json:"schema"`
	Type     string                         `json:"type"`
	Requests []domain.ManagedServiceRequest `json:"requests"`
}

type ManagedServiceRequestMutationResult struct {
	Schema        string                               `json:"schema"`
	Type          string                               `json:"type"`
	Decision      domain.ManagedServiceRequestDecision `json:"decision"`
	EventSequence int64                                `json:"event_sequence"`
}
