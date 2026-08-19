package localapi

import "crewfold/internal/domain"

type DomainAgentSpecDraftParams struct {
	Workspace      string `json:"workspace,omitempty"`
	Project        string `json:"project,omitempty"`
	Checkout       string `json:"checkout,omitempty"`
	RepositoryPath string `json:"repository_path,omitempty"`
	DomainName     string `json:"domain_name,omitempty"`
	OwnerIntent    string `json:"owner_intent"`
}

type DomainAgentSpecDraft struct {
	Name             string `json:"name"`
	Role             string `json:"role"`
	OperatingCharter string `json:"operating_charter"`
	DelegationPolicy string `json:"delegation_policy"`
	Rationale        string `json:"rationale"`
}

type DomainAgentSpecDraftResult struct {
	Schema string               `json:"schema"`
	Type   string               `json:"type"`
	Draft  DomainAgentSpecDraft `json:"draft"`
}

type DomainAgentCreateParams struct {
	Workspace        string `json:"workspace"`
	Project          string `json:"project"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	Provider         string `json:"provider"`
	Runtime          string `json:"runtime"`
	MaxConcurrency   int    `json:"max_concurrency"`
	ParentAgent      string `json:"parent_agent,omitempty"`
	Workstream       string `json:"workstream,omitempty"`
	OperatingCharter string `json:"operating_charter"`
	DelegationPolicy string `json:"delegation_policy"`
	PreferredEntry   bool   `json:"preferred_entry,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type DomainAgentCreateResult struct {
	Schema         string             `json:"schema"`
	Type           string             `json:"type"`
	Agent          domain.DomainAgent `json:"agent"`
	EventSequences []int64            `json:"event_sequences"`
}

type DomainAgentAttachParams struct {
	Workspace        string `json:"workspace"`
	Project          string `json:"project"`
	Agent            string `json:"agent"`
	ParentAgent      string `json:"parent_agent,omitempty"`
	Workstream       string `json:"workstream,omitempty"`
	OperatingCharter string `json:"operating_charter"`
	DelegationPolicy string `json:"delegation_policy"`
	PreferredEntry   bool   `json:"preferred_entry,omitempty"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type DomainAgentUpdateParams struct {
	Workspace        string  `json:"workspace"`
	Project          string  `json:"project"`
	Agent            string  `json:"agent"`
	ParentAgent      *string `json:"parent_agent,omitempty"`
	Workstream       *string `json:"workstream,omitempty"`
	OperatingCharter *string `json:"operating_charter,omitempty"`
	DelegationPolicy *string `json:"delegation_policy,omitempty"`
	PreferredEntry   *bool   `json:"preferred_entry,omitempty"`
	Status           *string `json:"status,omitempty"`
	ExpectedRevision int64   `json:"expected_revision"`
	IdempotencyKey   string  `json:"idempotency_key"`
}

type DomainAgentTreeParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type DomainAgentMutationResult struct {
	Schema        string                       `json:"schema"`
	Type          string                       `json:"type"`
	Membership    domain.DomainAgentMembership `json:"membership"`
	EventSequence int64                        `json:"event_sequence"`
}

type DomainAgentTreeResult struct {
	Schema    string               `json:"schema"`
	Type      string               `json:"type"`
	ProjectID string               `json:"project_id"`
	Agents    []domain.DomainAgent `json:"agents"`
}

type DomainAgentSessionParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Agent     string `json:"agent"`
	Epoch     int64  `json:"epoch,omitempty"`
}

type DomainAgentSessionOpenParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	Agent          string `json:"agent"`
	Checkout       string `json:"checkout,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type DomainAgentSessionSendParams struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	Agent          string `json:"agent"`
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key"`
}

type DomainAgentSessionInterruptParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Agent     string `json:"agent"`
	TurnID    string `json:"turn_id"`
}

type DomainAgentSessionCompactParams struct {
	Workspace     string `json:"workspace"`
	Project       string `json:"project"`
	Agent         string `json:"agent"`
	ExpectedEpoch int64  `json:"expected_epoch"`
}

type DomainAgentSessionRotateParams struct {
	Workspace     string `json:"workspace"`
	Project       string `json:"project"`
	Agent         string `json:"agent"`
	ExpectedEpoch int64  `json:"expected_epoch"`
	Reason        string `json:"reason"`
}

type DomainAgentSessionResult struct {
	Schema       string                         `json:"schema"`
	Type         string                         `json:"type"`
	View         domain.DomainAgentSessionView  `json:"view"`
	AcceptedTurn *domain.DomainAgentSessionTurn `json:"accepted_turn,omitempty"`
}

type DomainStaffingGrantCreateParams struct {
	Workspace                  string                              `json:"workspace"`
	Project                    string                              `json:"project"`
	ManagerAgent               string                              `json:"manager_agent"`
	ExpectedMembershipRevision int64                               `json:"expected_membership_revision"`
	Profiles                   []domain.DomainAgentStaffingProfile `json:"profiles"`
	TaskClasses                []string                            `json:"task_classes"`
	MaxDescendants             int                                 `json:"max_descendants"`
	MaxConcurrency             int                                 `json:"max_concurrency"`
	Budget                     domain.Budget                       `json:"budget"`
	ExpiresAt                  string                              `json:"expires_at,omitempty"`
	IdempotencyKey             string                              `json:"idempotency_key"`
}

type DomainStaffingGrantListParams struct {
	Workspace    string `json:"workspace"`
	Project      string `json:"project"`
	ManagerAgent string `json:"manager_agent"`
}

type DomainStaffingGrantRevokeParams struct {
	Workspace        string `json:"workspace"`
	GrantID          string `json:"grant_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type DomainStaffingGrantMutationResult struct {
	Schema        string                          `json:"schema"`
	Type          string                          `json:"type"`
	Grant         domain.DomainAgentStaffingGrant `json:"grant"`
	EventSequence int64                           `json:"event_sequence"`
}

type DomainStaffingGrantListResult struct {
	Schema string                            `json:"schema"`
	Type   string                            `json:"type"`
	Grants []domain.DomainAgentStaffingGrant `json:"grants"`
}

type DomainWorkProposalListParams struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
}

type DomainWorkProposalDecisionParams struct {
	Workspace        string `json:"workspace"`
	ProposalID       string `json:"proposal_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	DecisionNote     string `json:"decision_note"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type DomainWorkProposalListResult struct {
	Schema    string                      `json:"schema"`
	Type      string                      `json:"type"`
	Proposals []domain.DomainWorkProposal `json:"proposals"`
}

type DomainWorkProposalDecisionResult struct {
	Schema   string                            `json:"schema"`
	Type     string                            `json:"type"`
	Decision domain.DomainWorkProposalDecision `json:"decision"`
}
