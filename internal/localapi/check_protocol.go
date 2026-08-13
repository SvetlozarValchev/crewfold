package localapi

import "crewfold/internal/domain"

const (
	MethodCheckDefinitionCreate = "check.definition.create"
	MethodCheckDefinitionRetire = "check.definition.retire"
	MethodCheckDefinitionShow   = "check.definition.show"
	MethodCheckDefinitionList   = "check.definition.list"

	MethodCheckRequirementCreate = "check.requirement.create"
	MethodCheckRequirementRetire = "check.requirement.retire"
	MethodCheckRequirementList   = "check.requirement.list"

	MethodCheckGrantCreate = "check.grant.create"
	MethodCheckGrantRevoke = "check.grant.revoke"
	MethodCheckGrantShow   = "check.grant.show"
	MethodCheckGrantList   = "check.grant.list"

	MethodCheckRouteCreate = "check.route.create"
	MethodCheckRouteRetire = "check.route.retire"
	MethodCheckRouteList   = "check.route.list"

	MethodCheckPolicyShow      = "check.policy.show"
	MethodCheckPolicyConfigure = "check.policy.configure"

	MethodCheckRun     = "check.run"
	MethodCheckList    = "check.list"
	MethodCheckInspect = "check.inspect"
	MethodCheckLogs    = "check.logs"
	MethodCheckWatch   = "check.watch"

	MethodCheckRepairList    = "check.repair.list"
	MethodCheckRepairInspect = "check.repair.inspect"
	MethodCheckRepairAccept  = "check.repair.accept"
	MethodCheckRepairReject  = "check.repair.reject"

	CheckDefinitionMutationSchema  = "urn:crewfold:schema:local-api:check-definition-mutation-result:v1"
	CheckDefinitionShowSchema      = "urn:crewfold:schema:local-api:check-definition-show-result:v1"
	CheckDefinitionListSchema      = "urn:crewfold:schema:local-api:check-definition-list-result:v1"
	CheckRequirementMutationSchema = "urn:crewfold:schema:local-api:check-requirement-mutation-result:v1"
	CheckRequirementListSchema     = "urn:crewfold:schema:local-api:check-requirement-list-result:v1"
	CheckGrantMutationSchema       = "urn:crewfold:schema:local-api:check-grant-mutation-result:v1"
	CheckGrantShowSchema           = "urn:crewfold:schema:local-api:check-grant-show-result:v1"
	CheckGrantListSchema           = "urn:crewfold:schema:local-api:check-grant-list-result:v1"
	CheckRouteMutationSchema       = "urn:crewfold:schema:local-api:check-route-mutation-result:v1"
	CheckRouteListSchema           = "urn:crewfold:schema:local-api:check-route-list-result:v1"
	CheckPolicyMutationSchema      = "urn:crewfold:schema:local-api:check-policy-mutation-result:v1"
	CheckPolicyShowSchema          = "urn:crewfold:schema:local-api:check-policy-show-result:v1"
	CheckRunMutationSchema         = "urn:crewfold:schema:local-api:check-run-mutation-result:v1"
	CheckRunListSchema             = "urn:crewfold:schema:local-api:check-run-list-result:v1"
	CheckInspectSchema             = "urn:crewfold:schema:local-api:check-inspect-result:v1"
	CheckLogsSchema                = "urn:crewfold:schema:local-api:check-logs-result:v1"
	CheckWatchSchema               = "urn:crewfold:schema:local-api:check-watch-result:v1"
	CheckRepairMutationSchema      = "urn:crewfold:schema:local-api:check-repair-mutation-result:v1"
	CheckRepairShowSchema          = "urn:crewfold:schema:local-api:check-repair-show-result:v1"
	CheckRepairListSchema          = "urn:crewfold:schema:local-api:check-repair-list-result:v1"
)

type CheckDefinitionCreateParams struct {
	Workspace        string   `json:"workspace"`
	Project          string   `json:"project"`
	Name             string   `json:"name"`
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutMillis    int64    `json:"timeout_millis"`
	OutputByteLimit  int64    `json:"output_byte_limit"`
	IdempotencyKey   string   `json:"idempotency_key"`
}

type CheckDefinitionRetireParams struct {
	Workspace        string `json:"workspace"`
	Definition       string `json:"definition"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CheckDefinitionQueryParams struct {
	Workspace  string `json:"workspace"`
	Definition string `json:"definition,omitempty"`
	Project    string `json:"project,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type CheckRequirementCreateParams struct {
	Workspace                 string `json:"workspace"`
	Task                      string `json:"task"`
	CriterionKey              string `json:"criterion_key"`
	Statement                 string `json:"statement"`
	Definition                string `json:"definition"`
	DefinitionContentRevision int64  `json:"definition_content_revision"`
	ExpectedTaskRevision      int64  `json:"expected_task_revision"`
	IdempotencyKey            string `json:"idempotency_key"`
}

type CheckRequirementRetireParams struct {
	Workspace        string `json:"workspace"`
	Requirement      string `json:"requirement"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CheckRequirementQueryParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type CheckGrantCreateParams struct {
	Workspace             string                                `json:"workspace"`
	Project               string                                `json:"project"`
	Agent                 string                                `json:"agent"`
	ExpectedAgentRevision int64                                 `json:"expected_agent_revision"`
	Definitions           []CheckDefinitionContentRevisionParam `json:"definitions"`
	Operations            []string                              `json:"operations"`
	MaxPending            int                                   `json:"max_pending"`
	MaxInFlight           int                                   `json:"max_in_flight"`
	ExpiresAt             string                                `json:"expires_at,omitempty"`
	IdempotencyKey        string                                `json:"idempotency_key"`
}

type CheckDefinitionContentRevisionParam struct {
	Definition      string `json:"definition"`
	ContentRevision int64  `json:"content_revision"`
}

type CheckGrantRevokeParams struct {
	Workspace        string `json:"workspace"`
	Grant            string `json:"grant"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CheckGrantQueryParams struct {
	Workspace string `json:"workspace"`
	Grant     string `json:"grant,omitempty"`
	Project   string `json:"project,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type CheckRouteCreateParams struct {
	Workspace                 string `json:"workspace"`
	Project                   string `json:"project"`
	Definition                string `json:"definition,omitempty"`
	DefinitionContentRevision int64  `json:"definition_content_revision,omitempty"`
	Trigger                   string `json:"trigger"`
	Duty                      string `json:"duty"`
	Agent                     string `json:"agent"`
	ExpectedAgentRevision     int64  `json:"expected_agent_revision"`
	IdempotencyKey            string `json:"idempotency_key"`
}

type CheckRouteRetireParams struct {
	Workspace        string `json:"workspace"`
	Route            string `json:"route"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CheckRouteQueryParams struct {
	Workspace  string `json:"workspace"`
	Project    string `json:"project,omitempty"`
	Definition string `json:"definition,omitempty"`
	Trigger    string `json:"trigger,omitempty"`
	Duty       string `json:"duty,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type CheckPolicyQueryParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type CheckPolicyConfigureParams struct {
	Workspace              string `json:"workspace"`
	Project                string `json:"project"`
	RepairProposalsEnabled bool   `json:"repair_proposals_enabled"`
	RepairProfile          string `json:"repair_profile,omitempty"`
	RepairProfileRevision  int64  `json:"repair_profile_revision,omitempty"`
	MaxOpenRepairs         int    `json:"max_open_repairs"`
	ExpectedRevision       int64  `json:"expected_revision"`
	IdempotencyKey         string `json:"idempotency_key"`
}

type CheckRunParams struct {
	Workspace                         string `json:"workspace"`
	Task                              string `json:"task"`
	Definition                        string `json:"definition"`
	Checkout                          string `json:"checkout,omitempty"`
	ExpectedRequirementRevision       int64  `json:"expected_requirement_revision,omitempty"`
	ExpectedDefinitionContentRevision int64  `json:"expected_definition_content_revision,omitempty"`
	ExpectedCheckoutRevision          int64  `json:"expected_checkout_revision,omitempty"`
	IdempotencyKey                    string `json:"idempotency_key"`
}

type CheckQueryParams struct {
	Workspace   string `json:"workspace"`
	CheckRun    string `json:"check_run,omitempty"`
	Project     string `json:"project,omitempty"`
	Task        string `json:"task,omitempty"`
	Requirement string `json:"requirement,omitempty"`
	Definition  string `json:"definition,omitempty"`
	Status      string `json:"status,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type CheckLogsParams struct {
	Workspace string `json:"workspace"`
	CheckRun  string `json:"check_run"`
}

type CheckWatchParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type CheckRepairQueryParams struct {
	Workspace string `json:"workspace"`
	Repair    string `json:"repair,omitempty"`
	Project   string `json:"project,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type CheckRepairDecisionParams struct {
	Workspace        string `json:"workspace"`
	Repair           string `json:"repair"`
	ExpectedRevision int64  `json:"expected_revision"`
	DecisionNote     string `json:"decision_note,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type CheckDefinitionMutationResult struct {
	Schema        string                 `json:"schema"`
	Type          string                 `json:"type"`
	Definition    domain.CheckDefinition `json:"definition"`
	EventSequence int64                  `json:"event_sequence"`
}

type CheckDefinitionShowResult struct {
	Schema     string                 `json:"schema"`
	Type       string                 `json:"type"`
	Definition domain.CheckDefinition `json:"definition"`
}

type CheckDefinitionListResult struct {
	Schema      string                   `json:"schema"`
	Type        string                   `json:"type"`
	Definitions []domain.CheckDefinition `json:"definitions"`
}

type CheckRequirementMutationResult struct {
	Schema        string                      `json:"schema"`
	Type          string                      `json:"type"`
	Requirement   domain.TaskCheckRequirement `json:"requirement"`
	EventSequence int64                       `json:"event_sequence"`
}

type CheckRequirementListResult struct {
	Schema       string                            `json:"schema"`
	Type         string                            `json:"type"`
	Requirements []domain.TaskCheckRequirementView `json:"requirements"`
}

type CheckGrantMutationResult struct {
	Schema        string                 `json:"schema"`
	Type          string                 `json:"type"`
	Grant         domain.CheckWatchGrant `json:"grant"`
	EventSequence int64                  `json:"event_sequence"`
}

type CheckGrantShowResult struct {
	Schema string                 `json:"schema"`
	Type   string                 `json:"type"`
	Grant  domain.CheckWatchGrant `json:"grant"`
}

type CheckGrantListResult struct {
	Schema string                   `json:"schema"`
	Type   string                   `json:"type"`
	Grants []domain.CheckWatchGrant `json:"grants"`
}

type CheckRouteMutationResult struct {
	Schema        string            `json:"schema"`
	Type          string            `json:"type"`
	Route         domain.CheckRoute `json:"route"`
	EventSequence int64             `json:"event_sequence"`
}

type CheckRouteListResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Routes []domain.CheckRoute `json:"routes"`
}

type CheckPolicyMutationResult struct {
	Schema        string             `json:"schema"`
	Type          string             `json:"type"`
	Policy        domain.CheckPolicy `json:"policy"`
	EventSequence int64              `json:"event_sequence"`
}

type CheckPolicyShowResult struct {
	Schema string             `json:"schema"`
	Type   string             `json:"type"`
	Policy domain.CheckPolicy `json:"policy"`
}

type CheckRunMutationResult struct {
	Schema        string          `json:"schema"`
	Type          string          `json:"type"`
	Run           domain.CheckRun `json:"run"`
	EventSequence int64           `json:"event_sequence"`
}

type CheckRunListResult struct {
	Schema string                    `json:"schema"`
	Type   string                    `json:"type"`
	Runs   []domain.CheckRunListItem `json:"runs"`
}

type CheckInspectResult struct {
	Schema string                `json:"schema"`
	Type   string                `json:"type"`
	Detail domain.CheckRunDetail `json:"detail"`
}

type CheckLogsResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Logs   domain.CheckRunLogs `json:"logs"`
}

type CheckWatchResult struct {
	Schema        string                   `json:"schema"`
	Type          string                   `json:"type"`
	Receipt       domain.CheckWatchReceipt `json:"receipt"`
	EventSequence int64                    `json:"event_sequence"`
}

type CheckRepairMutationResult struct {
	Schema        string                   `json:"schema"`
	Type          string                   `json:"type"`
	Detail        domain.CheckRepairDetail `json:"detail"`
	EventSequence int64                    `json:"event_sequence"`
}

type CheckRepairShowResult struct {
	Schema string                   `json:"schema"`
	Type   string                   `json:"type"`
	Detail domain.CheckRepairDetail `json:"detail"`
}

type CheckRepairListResult struct {
	Schema  string                     `json:"schema"`
	Type    string                     `json:"type"`
	Repairs []domain.CheckRepairDetail `json:"repairs"`
}
