package localapi

import "crewfold/internal/domain"

const (
	MethodManagerGrantCreate = "manager.grant.create"
	MethodManagerGrantRevoke = "manager.grant.revoke"
	MethodManagerGrantShow   = "manager.grant.show"
	MethodManagerGrantList   = "manager.grant.list"

	MethodLaunchProfileCreate = "launch_profile.create"
	MethodLaunchProfileRetire = "launch_profile.retire"
	MethodLaunchProfileShow   = "launch_profile.show"
	MethodLaunchProfileList   = "launch_profile.list"

	MethodManagerInvoke = "manager.invoke"

	MethodProposalList    = "proposal.list"
	MethodProposalInspect = "proposal.inspect"
	MethodProposalAccept  = "proposal.accept"
	MethodProposalReject  = "proposal.reject"

	MethodSupervisorPolicyShow      = "supervisor.policy.show"
	MethodSupervisorPolicyConfigure = "supervisor.policy.configure"
	MethodSupervisorRun             = "supervisor.run"
	MethodSupervisorActionList      = "supervisor.action.list"
	MethodSupervisorActionShow      = "supervisor.action.show"
	MethodSupervisorExplain         = "supervisor.explain"

	MethodApprovalList    = "approval.list"
	MethodApprovalInspect = "approval.inspect"
	MethodApprovalAllow   = "approval.allow"
	MethodApprovalDeny    = "approval.deny"

	ManagerGrantMutationSchema     = "urn:crewfold:schema:local-api:manager-grant-mutation-result:v1"
	ManagerGrantShowSchema         = "urn:crewfold:schema:local-api:manager-grant-show-result:v1"
	ManagerGrantListSchema         = "urn:crewfold:schema:local-api:manager-grant-list-result:v1"
	LaunchProfileMutationSchema    = "urn:crewfold:schema:local-api:launch-profile-mutation-result:v1"
	LaunchProfileShowSchema        = "urn:crewfold:schema:local-api:launch-profile-show-result:v1"
	LaunchProfileListSchema        = "urn:crewfold:schema:local-api:launch-profile-list-result:v1"
	ManagerInvocationSchema        = "urn:crewfold:schema:local-api:manager-invocation-result:v1"
	ProposalMutationSchema         = "urn:crewfold:schema:local-api:manager-proposal-mutation-result:v1"
	ProposalShowSchema             = "urn:crewfold:schema:local-api:manager-proposal-show-result:v1"
	ProposalListSchema             = "urn:crewfold:schema:local-api:manager-proposal-list-result:v1"
	SupervisorPolicyMutationSchema = "urn:crewfold:schema:local-api:supervisor-policy-mutation-result:v1"
	SupervisorPolicyShowSchema     = "urn:crewfold:schema:local-api:supervisor-policy-show-result:v1"
	SupervisorRunSchema            = "urn:crewfold:schema:local-api:supervisor-run-result:v1"
	SupervisorActionShowSchema     = "urn:crewfold:schema:local-api:supervisor-action-show-result:v1"
	SupervisorActionListSchema     = "urn:crewfold:schema:local-api:supervisor-action-list-result:v1"
	SupervisorExplanationSchema    = "urn:crewfold:schema:local-api:supervisor-explanation-result:v1"
	ApprovalMutationSchema         = "urn:crewfold:schema:local-api:approval-mutation-result:v1"
	ApprovalShowSchema             = "urn:crewfold:schema:local-api:approval-show-result:v1"
	ApprovalListSchema             = "urn:crewfold:schema:local-api:approval-list-result:v1"
)

type ManagerGrantCreateParams struct {
	Workspace             string                       `json:"workspace"`
	Project               string                       `json:"project"`
	Objective             string                       `json:"objective"`
	Task                  string                       `json:"task"`
	Agent                 string                       `json:"agent"`
	ExpectedTaskRevision  int64                        `json:"expected_task_revision"`
	ExpectedAgentRevision int64                        `json:"expected_agent_revision"`
	ProposalKinds         []string                     `json:"proposal_kinds"`
	LaunchProfileIDs      []string                     `json:"launch_profile_ids"`
	AllowedClaimKinds     []string                     `json:"allowed_claim_kinds"`
	Limits                domain.ManagerProposalLimits `json:"limits"`
	ExpiresAt             string                       `json:"expires_at,omitempty"`
	IdempotencyKey        string                       `json:"idempotency_key"`
}

type ManagerGrantRevokeParams struct {
	Workspace        string `json:"workspace"`
	Grant            string `json:"grant"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ManagerGrantQueryParams struct {
	Workspace string `json:"workspace"`
	Grant     string `json:"grant,omitempty"`
	Project   string `json:"project,omitempty"`
	Objective string `json:"objective,omitempty"`
	Task      string `json:"task,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ManagerGrantMutationResult struct {
	Schema        string              `json:"schema"`
	Type          string              `json:"type"`
	Grant         domain.ManagerGrant `json:"grant"`
	EventSequence int64               `json:"event_sequence"`
}

type ManagerGrantShowResult struct {
	Schema string              `json:"schema"`
	Type   string              `json:"type"`
	Grant  domain.ManagerGrant `json:"grant"`
}

type ManagerGrantListResult struct {
	Schema string                `json:"schema"`
	Type   string                `json:"type"`
	Grants []domain.ManagerGrant `json:"grants"`
}

type LaunchProfileCreateParams struct {
	Workspace              string              `json:"workspace"`
	Project                string              `json:"project"`
	Agent                  string              `json:"agent"`
	ExpectedAgentRevision  int64               `json:"expected_agent_revision"`
	Purpose                string              `json:"purpose,omitempty"`
	Runtime                string              `json:"runtime"`
	Provider               string              `json:"provider"`
	Checkout               string              `json:"checkout,omitempty"`
	Scenario               domain.FakeScenario `json:"scenario"`
	AssignmentLeaseSeconds int64               `json:"assignment_lease_seconds"`
	CapabilityTTLSeconds   int64               `json:"capability_ttl_seconds"`
	ManagerGrant           string              `json:"manager_grant,omitempty"`
	IdempotencyKey         string              `json:"idempotency_key"`
}

type LaunchProfileRetireParams struct {
	Workspace        string `json:"workspace"`
	Profile          string `json:"profile"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type LaunchProfileQueryParams struct {
	Workspace    string `json:"workspace"`
	Profile      string `json:"profile,omitempty"`
	Project      string `json:"project,omitempty"`
	Agent        string `json:"agent,omitempty"`
	ManagerGrant string `json:"manager_grant,omitempty"`
	Status       string `json:"status,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type LaunchProfileMutationResult struct {
	Schema        string               `json:"schema"`
	Type          string               `json:"type"`
	Profile       domain.LaunchProfile `json:"profile"`
	EventSequence int64                `json:"event_sequence"`
}

type LaunchProfileShowResult struct {
	Schema  string               `json:"schema"`
	Type    string               `json:"type"`
	Profile domain.LaunchProfile `json:"profile"`
}

type LaunchProfileListResult struct {
	Schema   string                 `json:"schema"`
	Type     string                 `json:"type"`
	Profiles []domain.LaunchProfile `json:"profiles"`
}

type ManagerInvokeParams struct {
	Workspace               string `json:"workspace"`
	Objective               string `json:"objective"`
	PlanningTask            string `json:"planning_task,omitempty"`
	Grant                   string `json:"grant,omitempty"`
	Profile                 string `json:"profile,omitempty"`
	ExpectedTaskRevision    int64  `json:"expected_task_revision,omitempty"`
	ExpectedGrantRevision   int64  `json:"expected_grant_revision,omitempty"`
	ExpectedProfileRevision int64  `json:"expected_profile_revision,omitempty"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type ManagerInvocationResult struct {
	Schema        string               `json:"schema"`
	Type          string               `json:"type"`
	ManagerGrant  domain.ManagerGrant  `json:"manager_grant"`
	LaunchProfile domain.LaunchProfile `json:"launch_profile"`
	Detail        domain.RunDetail     `json:"detail"`
	EventSequence int64                `json:"event_sequence"`
}

type ProposalQueryParams struct {
	Workspace string `json:"workspace"`
	Proposal  string `json:"proposal,omitempty"`
	Project   string `json:"project,omitempty"`
	Objective string `json:"objective,omitempty"`
	SourceRun string `json:"source_run,omitempty"`
	Grant     string `json:"grant,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Status    string `json:"status,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ProposalDecisionParams struct {
	Workspace        string `json:"workspace"`
	Proposal         string `json:"proposal"`
	ExpectedRevision int64  `json:"expected_revision"`
	DecisionNote     string `json:"decision_note,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ProposalMutationResult struct {
	Schema        string                         `json:"schema"`
	Type          string                         `json:"type"`
	Proposal      domain.ManagerProposal         `json:"proposal"`
	Effects       []domain.ManagerProposalEffect `json:"effects"`
	EventSequence int64                          `json:"event_sequence"`
}

type ProposalShowResult struct {
	Schema   string                 `json:"schema"`
	Type     string                 `json:"type"`
	Proposal domain.ManagerProposal `json:"proposal"`
}

type ProposalListResult struct {
	Schema    string                   `json:"schema"`
	Type      string                   `json:"type"`
	Proposals []domain.ManagerProposal `json:"proposals"`
}

type SupervisorPolicyConfigureParams struct {
	Workspace            string                  `json:"workspace"`
	Enabled              bool                    `json:"enabled"`
	Limits               domain.SupervisorLimits `json:"limits"`
	AutoSchedule         bool                    `json:"auto_schedule"`
	AutoRetryLimit       int                     `json:"auto_retry_limit"`
	RetryCooldownSeconds int64                   `json:"retry_cooldown_seconds"`
	ExpectedRevision     int64                   `json:"expected_revision,omitempty"`
	IdempotencyKey       string                  `json:"idempotency_key"`
}

type WorkspaceParams struct {
	Workspace string `json:"workspace"`
}

type SupervisorRunParams struct {
	Workspace      string `json:"workspace"`
	Limit          int    `json:"limit,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type SupervisorActionQueryParams struct {
	Workspace string `json:"workspace"`
	Action    string `json:"action,omitempty"`
	Project   string `json:"project,omitempty"`
	Task      string `json:"task,omitempty"`
	Run       string `json:"run,omitempty"`
	Status    string `json:"status,omitempty"`
	Condition string `json:"condition,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type SupervisorExplainParams struct {
	Workspace string `json:"workspace"`
	Intent    string `json:"intent,omitempty"`
	Task      string `json:"task,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type SupervisorPolicyMutationResult struct {
	Schema        string                  `json:"schema"`
	Type          string                  `json:"type"`
	Policy        domain.SupervisorPolicy `json:"policy"`
	EventSequence int64                   `json:"event_sequence"`
}

type SupervisorPolicyShowResult struct {
	Schema string                  `json:"schema"`
	Type   string                  `json:"type"`
	Policy domain.SupervisorPolicy `json:"policy"`
}

type SupervisorRunResult struct {
	Schema          string                    `json:"schema"`
	Type            string                    `json:"type"`
	Policy          domain.SupervisorPolicy   `json:"policy"`
	Actions         []domain.SupervisorAction `json:"actions"`
	ScheduledRunIDs []string                  `json:"scheduled_run_ids"`
	EventSequence   int64                     `json:"event_sequence"`
}

type SupervisorActionShowResult struct {
	Schema string                  `json:"schema"`
	Type   string                  `json:"type"`
	Action domain.SupervisorAction `json:"action"`
}

type SupervisorActionListResult struct {
	Schema  string                    `json:"schema"`
	Type    string                    `json:"type"`
	Actions []domain.SupervisorAction `json:"actions"`
}

type SupervisorExplanationResult struct {
	Schema      string                       `json:"schema"`
	Type        string                       `json:"type"`
	Explanation domain.SupervisorExplanation `json:"explanation"`
}

type ApprovalQueryParams struct {
	Workspace string `json:"workspace"`
	Approval  string `json:"approval"`
}

type ApprovalListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	Action    string `json:"action,omitempty"`
	PageParams
}

type ApprovalDecisionParams struct {
	Workspace        string `json:"workspace"`
	Approval         string `json:"approval"`
	ExpectedRevision int64  `json:"expected_revision"`
	DecisionNote     string `json:"decision_note,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type ApprovalMutationResult struct {
	Schema        string                  `json:"schema"`
	Type          string                  `json:"type"`
	Approval      domain.ApprovalRequest  `json:"approval"`
	Action        domain.SupervisorAction `json:"action"`
	EventSequence int64                   `json:"event_sequence"`
}

type ApprovalShowResult struct {
	Schema   string                 `json:"schema"`
	Type     string                 `json:"type"`
	Approval domain.ApprovalRequest `json:"approval"`
}

type ApprovalListResult struct {
	Schema    string                   `json:"schema"`
	Type      string                   `json:"type"`
	Approvals []domain.ApprovalRequest `json:"approvals"`
	PageResult
}
