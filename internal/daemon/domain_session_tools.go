package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

const (
	domainToolContext          = "crewfold_get_domain_context"
	domainToolSendMessage      = "crewfold_send_message"
	domainToolCreateChild      = "crewfold_create_durable_child"
	domainToolDelegateStaffing = "crewfold_delegate_staffing_grant"
	domainToolProposeWork      = "crewfold_propose_work"
	domainToolProposeKnowledge = "crewfold_propose_knowledge"
)

type domainSessionToolCall struct {
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace"`
	ThreadID  string          `json:"threadId"`
	Tool      string          `json:"tool"`
	TurnID    string          `json:"turnId"`
}

type domainSessionSendMessageArguments struct {
	RecipientAgent   string `json:"recipient_agent"`
	Kind             string `json:"kind"`
	NewTopic         bool   `json:"new_topic"`
	Subject          string `json:"subject,omitempty"`
	Body             string `json:"body"`
	ThreadID         string `json:"thread_id,omitempty"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
}

type domainSessionCreateChildArguments struct {
	GrantID          string        `json:"grant_id"`
	Name             string        `json:"name"`
	Role             string        `json:"role"`
	Provider         string        `json:"provider"`
	Runtime          string        `json:"runtime"`
	MaxConcurrency   int           `json:"max_concurrency"`
	Workstream       string        `json:"workstream,omitempty"`
	OperatingCharter string        `json:"operating_charter"`
	DelegationPolicy string        `json:"delegation_policy"`
	TaskClass        string        `json:"task_class"`
	Budget           domain.Budget `json:"budget"`
	Checkout         string        `json:"checkout"`
}

type domainSessionDelegateStaffingArguments struct {
	ParentGrantID              string                              `json:"parent_grant_id"`
	SourceAllocationID         string                              `json:"source_allocation_id"`
	ManagerAgent               string                              `json:"manager_agent"`
	ExpectedMembershipRevision int64                               `json:"expected_membership_revision"`
	Profiles                   []domain.DomainAgentStaffingProfile `json:"profiles"`
	TaskClasses                []string                            `json:"task_classes"`
	MaxDescendants             int                                 `json:"max_descendants"`
	MaxConcurrency             int                                 `json:"max_concurrency"`
	Budget                     domain.Budget                       `json:"budget"`
	ExpiresAt                  string                              `json:"expires_at,omitempty"`
}

type domainSessionProposeWorkArguments struct {
	StaffingGrantID         string                           `json:"staffing_grant_id"`
	Summary                 string                           `json:"summary"`
	ObjectiveTitle          string                           `json:"objective_title"`
	ObjectiveBudget         domain.Budget                    `json:"objective_budget"`
	PrimaryCheckoutID       string                           `json:"primary_checkout_id"`
	PrimaryCheckoutRevision int64                            `json:"primary_checkout_revision"`
	ReferenceCheckoutIDs    []string                         `json:"reference_checkout_ids"`
	Agents                  []domain.DomainWorkProposalAgent `json:"agents"`
	Tasks                   []domain.DomainWorkProposalTask  `json:"tasks"`
}

type domainSessionProposeKnowledgeArguments struct {
	TaskScopeID          string                        `json:"task_scope_id,omitempty"`
	Type                 string                        `json:"type"`
	Title                string                        `json:"title"`
	Body                 string                        `json:"body"`
	Confidence           string                        `json:"confidence"`
	VerificationStatus   string                        `json:"verification_status"`
	FreshnessPolicy      string                        `json:"freshness_policy"`
	FreshUntil           string                        `json:"fresh_until,omitempty"`
	SupportingSources    []domain.KnowledgeSourceInput `json:"supporting_sources,omitempty"`
	SupersedesRevisionID string                        `json:"supersedes_revision_id,omitempty"`
}

func domainStaffingBudgetSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"token_limit", "cost_cents", "time_seconds"}, "properties": map[string]any{
		"token_limit": map[string]any{"type": "integer", "minimum": 0}, "cost_cents": map[string]any{"type": "integer", "minimum": 0}, "time_seconds": map[string]any{"type": "integer", "minimum": 0},
	}}
}

func domainAgentDynamicToolSpecs() []execution.CodexDynamicToolSpec {
	tools := []execution.CodexDynamicNamespaceTool{
		{
			Type: "function", Name: domainToolContext,
			Description: "Read this durable agent's bounded Crewfold domain, hierarchy position, attached resources, workstreams, assigned work, and delivered domain inbox. This is canonical Crewfold context, not provider memory.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		},
		{
			Type: "function", Name: domainToolSendMessage,
			Description: "Send one durable typed Crewfold message to another active durable agent in this same domain. Read the domain context first. Continue a related existing coordination thread with new_topic=false and its thread_id. Use new_topic=true with a concise subject only for a genuinely distinct topic. This records an immutable message and exact tool receipt; it does not grant authority, create knowledge, or impersonate a task run.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"recipient_agent", "kind", "new_topic", "body"},
				"properties": map[string]any{
					"recipient_agent":     map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"kind":                map[string]any{"type": "string", "enum": []string{"inform", "question", "request", "review_request", "handoff", "decision_notice", "risk", "conflict", "approval_request"}},
					"new_topic":           map[string]any{"type": "boolean"},
					"subject":             map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
					"body":                map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"thread_id":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"reply_to_message_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				},
			},
		},
		{
			Type: "function", Name: domainToolCreateChild,
			Description: "Create one continuing domain-level durable child immediately through a current Crewfold staffing grant. Use this only when the owner explicitly asks for persistent domain staff outside any proposed workstream. Never use it to assemble a team for a deliverable: put that inert team in crewfold_propose_work instead. This does not create a task, assign work, reserve the checkout, or start a run.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"grant_id", "name", "role", "operating_charter", "delegation_policy", "provider", "runtime", "max_concurrency", "task_class", "budget", "checkout"},
				"properties": map[string]any{
					"grant_id":          map[string]any{"type": "string", "pattern": "^staffgrant_[0-9a-f]{32}$"},
					"name":              map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
					"role":              map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"operating_charter": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
					"delegation_policy": map[string]any{"type": "string", "enum": []string{"hands_on", "adaptive", "delegation_first"}},
					"provider":          map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"runtime":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"max_concurrency":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					"workstream":        map[string]any{"type": "string", "pattern": "^obj_[0-9a-f]{32}$"},
					"task_class":        map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
					"checkout":          map[string]any{"type": "string", "pattern": "^co_[0-9a-f]{32}$"},
					"budget": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"token_limit", "cost_cents", "time_seconds"},
						"properties": map[string]any{
							"token_limit":  map[string]any{"type": "integer", "minimum": 0},
							"cost_cents":   map[string]any{"type": "integer", "minimum": 0},
							"time_seconds": map[string]any{"type": "integer", "minimum": 0},
						},
					},
				},
			},
		},
		{
			Type: "function", Name: domainToolDelegateStaffing,
			Description: "Delegate a strict subset of this durable agent's current staffing authority to one direct durable child created from the named allocation. Crewfold rejects any broader provider/runtime, task class, concurrency, descendant, budget, or expiry scope. The delegated grant is durable and independently inspectable.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"parent_grant_id", "source_allocation_id", "manager_agent", "expected_membership_revision", "profiles", "task_classes", "max_descendants", "max_concurrency", "budget"},
				"properties": map[string]any{
					"parent_grant_id": map[string]any{"type": "string", "pattern": "^staffgrant_[0-9a-f]{32}$"}, "source_allocation_id": map[string]any{"type": "string", "pattern": "^staffalloc_[0-9a-f]{32}$"},
					"manager_agent": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "expected_membership_revision": map[string]any{"type": "integer", "minimum": 1},
					"profiles":     map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"provider", "runtime", "max_concurrency"}, "properties": map[string]any{"provider": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "runtime": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "max_concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}},
					"task_classes": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"}}, "max_descendants": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "max_concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					"budget": domainStaffingBudgetSchema(), "expires_at": map[string]any{"type": "string", "format": "date-time"},
				},
			},
		},
		{
			Type: "function", Name: domainToolProposeWork,
			Description: "Submit one inert, owner-reviewable checkout-bound workstream, team, and task graph. New team members are logical proposal entries and do not exist before acceptance; existing durable agents may be referenced exactly. Every task names an assignee_key and every dependency names its structured delivery. Owner acceptance atomically creates proposed agents and profiles, places the whole team, creates tasks and delivery edges, and publishes scheduling intents against the frozen primary checkout.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"staffing_grant_id", "summary", "objective_title", "objective_budget", "primary_checkout_id", "primary_checkout_revision", "reference_checkout_ids", "agents", "tasks"},
				"properties": map[string]any{
					"staffing_grant_id":         map[string]any{"type": "string", "pattern": "^staffgrant_[0-9a-f]{32}$"},
					"summary":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
					"objective_title":           map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"objective_budget":          domainStaffingBudgetSchema(),
					"primary_checkout_id":       map[string]any{"type": "string", "pattern": "^co_[0-9a-f]{32}$"},
					"primary_checkout_revision": map[string]any{"type": "integer", "minimum": 1},
					"reference_checkout_ids":    map[string]any{"type": "array", "maxItems": 8, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^co_[0-9a-f]{32}$"}},
					"agents": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{
						"type": "object", "additionalProperties": false, "required": []string{"key", "budget"},
						"oneOf": []map[string]any{
							{"required": []string{"existing_agent_id", "existing_membership_revision", "existing_launch_profile_id"}},
							{"required": []string{"name", "role", "operating_charter", "delegation_policy", "provider", "runtime", "max_concurrency", "task_class"}},
						},
						"properties": map[string]any{
							"key":                          map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
							"existing_agent_id":            map[string]any{"type": "string", "pattern": "^agent_[0-9a-f]{32}$"},
							"existing_membership_revision": map[string]any{"type": "integer", "minimum": 1},
							"existing_launch_profile_id":   map[string]any{"type": "string", "pattern": "^lprof_[0-9a-f]{32}$"},
							"name":                         map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
							"role":                         map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
							"parent_key":                   map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
							"operating_charter":            map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
							"delegation_policy":            map[string]any{"type": "string", "enum": []string{"hands_on", "adaptive", "delegation_first"}},
							"provider":                     map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
							"runtime":                      map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
							"max_concurrency":              map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
							"task_class":                   map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
							"budget":                       domainStaffingBudgetSchema(),
						},
					}},
					"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"key", "title", "description", "task_class", "priority", "budget", "assignee_key", "depends_on", "dependency_delivery"},
						"properties": map[string]any{
							"key":                 map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
							"title":               map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
							"description":         map[string]any{"type": "string", "maxLength": 4096},
							"task_class":          map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
							"priority":            map[string]any{"type": "integer", "minimum": 0, "maximum": 1000},
							"budget":              domainStaffingBudgetSchema(),
							"assignee_key":        map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
							"depends_on":          map[string]any{"type": "array", "maxItems": 15, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"}},
							"dependency_delivery": map[string]any{"type": "object", "maxProperties": 15, "additionalProperties": map[string]any{"type": "string", "enum": []string{domain.DependencyDeliveryCompletion, domain.DependencyDeliveryHandoff, domain.DependencyDeliveryHandoffWithEvidence}}},
						},
					}},
				},
			},
		},
		{
			Type: "function", Name: domainToolProposeKnowledge,
			Description: "Propose one canonical domain knowledge revision for explicit owner review. Crewfold injects this authenticated durable agent as the exact primary source and proposer; the caller cannot forge provenance or acceptance. Optional supporting task/meeting sources must already exist in the same domain. This does not edit repository Markdown and does not make the proposal current until the owner accepts its exact state revision.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"type", "title", "body", "confidence", "verification_status", "freshness_policy"},
				"properties": map[string]any{
					"task_scope_id":          map[string]any{"type": "string", "pattern": "^task_[0-9a-f]{32}$"},
					"type":                   map[string]any{"type": "string", "enum": []string{domain.KnowledgeTypeDecision, domain.KnowledgeTypeFinding}},
					"title":                  map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
					"body":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
					"confidence":             map[string]any{"type": "string", "enum": []string{domain.KnowledgeConfidenceLow, domain.KnowledgeConfidenceMedium, domain.KnowledgeConfidenceHigh}},
					"verification_status":    map[string]any{"type": "string", "enum": []string{domain.KnowledgeVerificationUnverified, domain.KnowledgeVerificationSupported, domain.KnowledgeVerificationVerified}},
					"freshness_policy":       map[string]any{"type": "string", "enum": []string{domain.KnowledgeFreshUntilSuperseded, domain.KnowledgeFreshExpiresAt}},
					"fresh_until":            map[string]any{"type": "string", "format": "date-time"},
					"supersedes_revision_id": map[string]any{"type": "string", "pattern": "^krev_[0-9a-f]{32}$"},
					"supporting_sources": map[string]any{"type": "array", "maxItems": 15, "uniqueItems": true, "items": map[string]any{
						"type": "object", "additionalProperties": false, "required": []string{"type", "id", "role"},
						"properties": map[string]any{
							"type": map[string]any{"type": "string", "enum": []string{domain.KnowledgeSourceTask, domain.KnowledgeSourceMeeting, domain.KnowledgeSourceMeetingProposal}},
							"id":   map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
							"role": map[string]any{"type": "string", "const": domain.KnowledgeSourceSupporting},
						},
					}},
				},
			},
		},
	}
	return []execution.CodexDynamicToolSpec{{
		Type: "namespace", Name: "crewfold",
		Description: "Canonical Crewfold domain context, durable messaging, owner-bounded staffing and work proposals, and sourced knowledge proposals. Direct namespace calls and Codex code-mode tools-object calls both use the same audited app-server client-tool boundary.",
		Tools:       tools,
	}}
}

func (s *server) handleDomainSessionToolRequest(ctx context.Context, request execution.CodexAppServerRequest) (any, error) {
	var call domainSessionToolCall
	if err := decodeDomainSessionToolCall(request.Params, &call); err != nil {
		return domainToolFailure("invalid tool request: " + err.Error()), nil
	}
	receiptCommand := store.DomainAgentToolReceiptCommand{
		ThreadID: call.ThreadID, CallID: call.CallID, TurnID: call.TurnID, ToolName: call.Tool, Arguments: call.Arguments,
	}
	if receipt, found, err := s.store.ReplayDomainAgentToolReceipt(ctx, receiptCommand); err != nil {
		return domainToolFailure("durable session authority is unavailable: " + safeDomainSessionDiagnostic(err)), nil
	} else if found {
		return receipt.Response, nil
	}
	var result map[string]any
	if call.Namespace != nil && *call.Namespace != "" && *call.Namespace != "crewfold" {
		result = domainToolFailure("tool namespace is not Crewfold")
	} else if call.Tool != domainToolContext && call.Tool != domainToolSendMessage && call.Tool != domainToolCreateChild && call.Tool != domainToolDelegateStaffing && call.Tool != domainToolProposeWork && call.Tool != domainToolProposeKnowledge {
		result = domainToolFailure("Crewfold did not advertise this durable-agent tool")
	} else if call.Tool == domainToolContext {
		if err := decodeEmptyDomainToolArguments(call.Arguments); err != nil {
			result = domainToolFailure("tool arguments must be one empty object")
		} else {
			result = s.domainContextToolResult(ctx, call.ThreadID)
		}
	} else if call.Tool == domainToolSendMessage {
		if arguments, err := decodeDomainSendMessageArguments(call.Arguments); err != nil {
			result = domainToolFailure("message arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainSendMessageToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolCreateChild {
		arguments, err := decodeDomainCreateChildArguments(call.Arguments)
		if err != nil {
			result = domainToolFailure("durable child arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainCreateChildToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolDelegateStaffing {
		if arguments, err := decodeDomainDelegateStaffingArguments(call.Arguments); err != nil {
			result = domainToolFailure("delegated staffing arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainDelegateStaffingToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolProposeWork {
		if arguments, err := decodeDomainProposeWorkArguments(call.Arguments); err != nil {
			result = domainToolFailure("work proposal arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainProposeWorkToolResult(ctx, call, arguments)
		}
	} else if arguments, err := decodeDomainProposeKnowledgeArguments(call.Arguments); err != nil {
		result = domainToolFailure("knowledge proposal arguments are invalid: " + safeDomainSessionDiagnostic(err))
	} else {
		result = s.domainProposeKnowledgeToolResult(ctx, call, arguments)
	}
	receipt, err := s.store.RecordDomainAgentToolReceipt(ctx, receiptCommand, result, result["success"] == true)
	if err != nil {
		return nil, fmt.Errorf("record durable agent tool receipt: %w", err)
	}
	return receipt.Response, nil
}

func (s *server) domainProposeKnowledgeToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionProposeKnowledgeArguments) map[string]any {
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, call.ThreadID)
	if err != nil {
		return domainToolFailure("durable session authority is unavailable: " + safeDomainSessionDiagnostic(err))
	}
	sources := make([]domain.KnowledgeSourceInput, 0, len(arguments.SupportingSources)+1)
	sources = append(sources, domain.KnowledgeSourceInput{Type: domain.KnowledgeSourceDomainAgent, ID: scope.Agent.ID, Role: domain.KnowledgeSourcePrimary})
	sources = append(sources, arguments.SupportingSources...)
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.ProposeKnowledge(ctx, store.ProposeKnowledgeCommand{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, TaskScopeID: arguments.TaskScopeID,
		Type: arguments.Type, Title: arguments.Title, Body: arguments.Body, Confidence: arguments.Confidence,
		VerificationStatus: arguments.VerificationStatus, FreshnessPolicy: arguments.FreshnessPolicy, FreshUntil: arguments.FreshUntil,
		Sources: sources, SupersedesRevisionID: arguments.SupersedesRevisionID,
		Actor:          domain.KnowledgeActor{ID: scope.Agent.ID, Type: domain.KnowledgeActorIntegration},
		IdempotencyKey: key, CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("submit knowledge proposal: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:knowledge-proposal-result:v1", "revision": result.Revision,
		"event_sequence": result.EventSequence, "effect": "pending_owner_governance",
	})
	if err != nil {
		return domainToolFailure("encode knowledge proposal result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainProposeWorkToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionProposeWorkArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.SubmitDomainWorkProposal(ctx, store.SubmitDomainWorkProposalCommand{
		ThreadID: call.ThreadID, StaffingGrantID: arguments.StaffingGrantID, Summary: arguments.Summary,
		Content: domain.DomainWorkProposalContent{ObjectiveTitle: arguments.ObjectiveTitle, ObjectiveBudget: arguments.ObjectiveBudget,
			PrimaryCheckoutID: arguments.PrimaryCheckoutID, PrimaryCheckoutRevision: arguments.PrimaryCheckoutRevision,
			ReferenceCheckoutIDs: arguments.ReferenceCheckoutIDs, Agents: arguments.Agents, Tasks: arguments.Tasks},
		IdempotencyKey: key, CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("submit work proposal: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:work-proposal-result:v1", "proposal": result.Value,
		"event_sequence": result.EventSequence, "effect": "none_until_owner_acceptance",
	})
	if err != nil {
		return domainToolFailure("encode work proposal: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainDelegateStaffingToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionDelegateStaffingArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.DelegateDomainAgentStaffingGrant(ctx, store.DelegateDomainAgentStaffingGrantCommand{
		ThreadID: call.ThreadID, ParentGrantID: arguments.ParentGrantID, SourceAllocationID: arguments.SourceAllocationID,
		ManagerAgentIdentifier: arguments.ManagerAgent, ExpectedMembershipRevision: arguments.ExpectedMembershipRevision,
		Profiles: arguments.Profiles, TaskClasses: arguments.TaskClasses, MaxDescendants: arguments.MaxDescendants,
		MaxConcurrency: arguments.MaxConcurrency, Budget: arguments.Budget, ExpiresAt: arguments.ExpiresAt,
		IdempotencyKey: key, CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("delegate staffing grant: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{"schema": "urn:crewfold:schema:domain:delegated-staffing-grant-result:v1", "grant": result.Value, "event_sequence": result.EventSequence})
	if err != nil {
		return domainToolFailure("encode delegated staffing grant: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainCreateChildToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionCreateChildArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	idempotencyKey := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.CreateDomainAgentChild(ctx, store.CreateDomainAgentChildCommand{
		ThreadID: call.ThreadID, GrantID: arguments.GrantID, Name: arguments.Name, Role: arguments.Role,
		Provider: arguments.Provider, Runtime: arguments.Runtime, MaxConcurrency: arguments.MaxConcurrency,
		OperatingCharter: arguments.OperatingCharter, DelegationPolicy: arguments.DelegationPolicy,
		Workstream: arguments.Workstream, TaskClass: arguments.TaskClass, Budget: arguments.Budget,
		IdempotencyKey: idempotencyKey, CorrelationID: idempotencyKey,
	})
	if err != nil {
		return domainToolFailure("create durable child: " + safeDomainSessionDiagnostic(err))
	}
	profile, err := s.store.CreateLaunchProfile(ctx, store.CreateLaunchProfileCommand{
		WorkspaceIdentifier: result.Agent.WorkspaceID, ProjectIdentifier: result.Membership.ProjectID,
		AgentIdentifier: result.Agent.ID, ExpectedAgentRevision: result.Agent.Revision, Purpose: arguments.TaskClass,
		Runtime: result.Agent.Runtime, Provider: result.Agent.Provider, CheckoutIdentifier: arguments.Checkout,
		Scenario: ownerWorkbenchScenario(), AssignmentLeaseSeconds: 3600, CapabilityTTLSeconds: 3600,
		IdempotencyKey: idempotencyKey + "-profile", CorrelationID: idempotencyKey,
	})
	if err != nil {
		return domainToolFailure("prepare durable child execution profile: " + safeDomainSessionDiagnostic(err))
	}
	result.LaunchProfile = &profile.Value
	result.EventSequences = append(result.EventSequences, profile.EventSequence)
	session, sessionErr := s.ensureDomainAgentSessionBound(ctx, result.Agent.WorkspaceID, result.Membership.ProjectID, result.Agent.ID, arguments.Checkout)
	if sessionErr != nil {
		return domainToolFailure("activate durable child conversation: " + safeDomainSessionDiagnostic(sessionErr))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:durable-agent-child-result:v1",
		"agent":  result.Agent, "membership": result.Membership, "grant": result.Grant,
		"allocation": result.Allocation, "launch_profile": result.LaunchProfile, "session": session,
		"event_sequences": result.EventSequences,
	})
	if err != nil {
		return domainToolFailure("encode durable child result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainSendMessageToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionSendMessageArguments) map[string]any {
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, call.ThreadID)
	if err != nil {
		return domainToolFailure("durable session authority is unavailable: " + safeDomainSessionDiagnostic(err))
	}
	digest := sha256.Sum256([]byte(call.CallID))
	idempotencyKey := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.SendMessage(ctx, store.SendMessageCommand{
		WorkspaceIdentifier: scope.Workspace.ID, SenderDomainThreadID: call.ThreadID,
		RecipientAgent: arguments.RecipientAgent, ThreadID: arguments.ThreadID, Kind: arguments.Kind,
		Subject: arguments.Subject, Body: arguments.Body, ReplyToMessageID: arguments.ReplyToMessageID,
		IdempotencyKey: idempotencyKey, CorrelationID: idempotencyKey,
	})
	if err != nil {
		return domainToolFailure("send durable domain message: " + safeDomainSessionDiagnostic(err))
	}
	if result.Value.Recipient.WakeStatus == domain.WakePending {
		s.signalMessageWakeWorker()
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:durable-agent-message-result:v1",
		"thread": result.Value.Thread, "message": result.Value.Message, "recipient": result.Value.Recipient,
		"event_sequence": result.EventSequence,
	})
	if err != nil {
		return domainToolFailure("encode durable domain message: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainContextToolResult(ctx context.Context, threadID string) map[string]any {
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, threadID)
	if err != nil {
		return domainToolFailure("durable session authority is unavailable: " + safeDomainSessionDiagnostic(err))
	}
	tree, err := s.store.DomainAgentTree(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return domainToolFailure("read agent hierarchy: " + safeDomainSessionDiagnostic(err))
	}
	inspection, err := s.store.InspectProject(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return domainToolFailure("read attached resources: " + safeDomainSessionDiagnostic(err))
	}
	objectives, err := s.store.ListObjectives(ctx, store.ListObjectivesQuery{WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Limit: 50})
	if err != nil {
		return domainToolFailure("read workstreams: " + safeDomainSessionDiagnostic(err))
	}
	tasks, err := s.store.ListTasks(ctx, store.ListTasksQuery{WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Limit: 50})
	if err != nil {
		return domainToolFailure("read assigned work: " + safeDomainSessionDiagnostic(err))
	}
	assigned := make([]domain.TaskDetail, 0, len(tasks.Tasks))
	for _, detail := range tasks.Tasks {
		if detail.Task.AssignedAgentID == scope.Agent.ID {
			assigned = append(assigned, detail)
		}
	}
	staffingGrants, err := s.store.DomainAgentStaffingGrants(ctx, scope.Workspace.ID, scope.Project.ID, scope.Agent.ID)
	if err != nil {
		return domainToolFailure("read durable staffing grants: " + safeDomainSessionDiagnostic(err))
	}
	launchProfiles, err := s.store.LaunchProfiles(ctx, store.ListLaunchProfilesQuery{WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Status: domain.LaunchProfileActive, Limit: 100})
	if err != nil {
		return domainToolFailure("read exact launch profiles: " + safeDomainSessionDiagnostic(err))
	}
	workProposals, err := s.store.DomainWorkProposals(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return domainToolFailure("read work proposals: " + safeDomainSessionDiagnostic(err))
	}
	knowledge, err := s.store.ListKnowledge(ctx, store.ListKnowledgeQuery{
		WorkspaceIdentifier: scope.Workspace.ID,
		ProjectIdentifier:   scope.Project.ID,
	})
	if err != nil {
		return domainToolFailure("read domain knowledge: " + safeDomainSessionDiagnostic(err))
	}
	inbox, err := s.store.DeliverDomainAgentSessionInbox(ctx, threadID, 20)
	if err != nil {
		return domainToolFailure("read durable domain inbox: " + safeDomainSessionDiagnostic(err))
	}
	threads, err := s.store.ListThreads(ctx, scope.Workspace.ID, scope.Project.ID, 50)
	if err != nil {
		return domainToolFailure("read durable coordination threads: " + safeDomainSessionDiagnostic(err))
	}
	coordinationThreads := make([]domain.ThreadSummary, 0, 20)
	for _, summary := range threads {
		for _, agentID := range summary.AgentIDs {
			if agentID == scope.Agent.ID {
				coordinationThreads = append(coordinationThreads, summary)
				break
			}
		}
		if len(coordinationThreads) == 20 {
			break
		}
	}
	payload := map[string]any{
		"schema": "urn:crewfold:schema:domain:durable-agent-context:v1",
		"domain": scope.Project, "agent": scope.Agent, "membership": scope.Membership,
		"agent_tree": tree.Agents, "attached_repositories": inspection.Repositories, "attached_checkouts": inspection.Checkouts,
		"workstreams": objectives.Objectives, "assigned_work": assigned, "staffing_grants": staffingGrants, "inbox": inbox,
		"launch_profiles":      launchProfiles,
		"work_proposals":       workProposals,
		"knowledge_revisions":  knowledge,
		"coordination_threads": coordinationThreads,
		"bounds": map[string]any{
			"workstreams_total": objectives.Total, "workstreams_truncated": objectives.HasMore,
			"tasks_examined": len(tasks.Tasks), "project_tasks_total": tasks.Total, "project_tasks_truncated": tasks.HasMore,
			"inbox_items": len(inbox), "inbox_limit": 20, "staffing_grants": len(staffingGrants), "work_proposals": len(workProposals),
			"knowledge_revisions":  len(knowledge),
			"coordination_threads": len(coordinationThreads), "coordination_thread_limit": 20,
		},
		"knowledge_authoring": map[string]any{
			"available":  true,
			"operation":  domainToolProposeKnowledge,
			"governance": "proposals are inert until the owner accepts the exact revision; this authenticated agent is recorded as primary source and proposer",
		},
		"authority_note": "Hierarchy, names, roles, and conversation text do not grant authority. Only Crewfold grants, assignments, claims, budgets, capabilities, and accepted typed operations authorize effects.",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domainToolFailure("encode durable agent context: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func decodeDomainSessionToolCall(data json.RawMessage, target *domainSessionToolCall) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("unexpected trailing JSON")
	}
	if strings.TrimSpace(target.CallID) == "" || strings.TrimSpace(target.ThreadID) == "" || strings.TrimSpace(target.TurnID) == "" || strings.TrimSpace(target.Tool) == "" {
		return errors.New("call, thread, turn, and tool identifiers are required")
	}
	return nil
}

func decodeEmptyDomainToolArguments(data json.RawMessage) error {
	if len(data) == 0 {
		return errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value struct{}
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func decodeDomainSendMessageArguments(data json.RawMessage) (domainSessionSendMessageArguments, error) {
	var value domainSessionSendMessageArguments
	if len(data) == 0 {
		return value, errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("unexpected trailing JSON")
		}
		return value, err
	}
	value.RecipientAgent = strings.TrimSpace(value.RecipientAgent)
	value.Kind = strings.TrimSpace(value.Kind)
	value.Subject = strings.TrimSpace(value.Subject)
	value.Body = strings.TrimSpace(value.Body)
	value.ThreadID = strings.TrimSpace(value.ThreadID)
	value.ReplyToMessageID = strings.TrimSpace(value.ReplyToMessageID)
	if !validDomainToolText(value.RecipientAgent, 128) || !validDomainToolText(value.Body, 4096) {
		return value, errors.New("recipient and body are required and must be bounded UTF-8 text")
	}
	if value.Subject != "" && !validDomainToolText(value.Subject, 160) {
		return value, errors.New("subject must be bounded UTF-8 text")
	}
	if value.ThreadID != "" && !validDomainToolText(value.ThreadID, 128) {
		return value, errors.New("thread identifier must be bounded UTF-8 text")
	}
	if value.ReplyToMessageID != "" && !validDomainToolText(value.ReplyToMessageID, 128) {
		return value, errors.New("reply identifier must be bounded UTF-8 text")
	}
	if value.NewTopic {
		if value.Subject == "" {
			return value, errors.New("a genuinely new topic requires a concise subject")
		}
		if value.ThreadID != "" || value.ReplyToMessageID != "" {
			return value, errors.New("a new topic cannot also continue or reply inside an existing thread")
		}
	} else {
		if value.ThreadID == "" {
			return value, errors.New("continuing coordination requires an existing thread identifier from domain context")
		}
		if value.Subject != "" {
			return value, errors.New("an existing coordination thread retains its original subject")
		}
	}
	if !map[string]bool{
		"inform": true, "question": true, "request": true, "review_request": true,
		"handoff": true, "decision_notice": true, "risk": true, "conflict": true, "approval_request": true,
	}[value.Kind] {
		return value, errors.New("message kind is unsupported")
	}
	return value, nil
}

func decodeDomainCreateChildArguments(data json.RawMessage) (domainSessionCreateChildArguments, error) {
	var value domainSessionCreateChildArguments
	if len(data) == 0 {
		return value, errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("unexpected trailing JSON")
		}
		return value, err
	}
	value.GrantID, value.Name, value.Role = strings.TrimSpace(value.GrantID), strings.TrimSpace(value.Name), strings.TrimSpace(value.Role)
	value.Provider, value.Runtime = strings.TrimSpace(value.Provider), strings.TrimSpace(value.Runtime)
	value.Workstream, value.TaskClass = strings.TrimSpace(value.Workstream), strings.TrimSpace(value.TaskClass)
	value.Checkout = strings.TrimSpace(value.Checkout)
	if !validDomainToolText(value.GrantID, 128) || !validDomainToolText(value.Name, 63) ||
		!validDomainToolText(value.Role, 128) || !validDomainToolText(value.Provider, 128) ||
		!validDomainToolText(value.Runtime, 128) || !validDomainToolText(value.TaskClass, 63) || !validDomainToolIdentifier(value.Checkout, "co_") ||
		(value.Workstream != "" && !validDomainToolText(value.Workstream, 128)) || value.MaxConcurrency < 1 || value.MaxConcurrency > 100 ||
		value.Budget.TokenLimit < 0 || value.Budget.CostCents < 0 || value.Budget.TimeSeconds < 0 {
		return value, errors.New("child fields are missing or outside their bounded types")
	}
	return value, nil
}

func decodeDomainDelegateStaffingArguments(data json.RawMessage) (domainSessionDelegateStaffingArguments, error) {
	var value domainSessionDelegateStaffingArguments
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON")
		}
		return value, err
	}
	value.ParentGrantID = strings.TrimSpace(value.ParentGrantID)
	value.SourceAllocationID = strings.TrimSpace(value.SourceAllocationID)
	value.ManagerAgent = strings.TrimSpace(value.ManagerAgent)
	value.ExpiresAt = strings.TrimSpace(value.ExpiresAt)
	if !validDomainToolText(value.ParentGrantID, 128) || !validDomainToolText(value.SourceAllocationID, 128) || !validDomainToolText(value.ManagerAgent, 128) || value.ExpectedMembershipRevision < 1 || len(value.Profiles) < 1 || len(value.Profiles) > 32 || len(value.TaskClasses) < 1 || len(value.TaskClasses) > 32 || value.MaxDescendants < 1 || value.MaxConcurrency < 1 || value.Budget.TokenLimit < 0 || value.Budget.CostCents < 0 || value.Budget.TimeSeconds < 0 {
		return value, errors.New("delegated staffing fields are missing or outside their bounded types")
	}
	return value, nil
}

func decodeDomainProposeWorkArguments(data json.RawMessage) (domainSessionProposeWorkArguments, error) {
	var value domainSessionProposeWorkArguments
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON")
		}
		return value, err
	}
	value.StaffingGrantID = strings.TrimSpace(value.StaffingGrantID)
	value.Summary = strings.TrimSpace(value.Summary)
	value.ObjectiveTitle = strings.TrimSpace(value.ObjectiveTitle)
	value.PrimaryCheckoutID = strings.TrimSpace(value.PrimaryCheckoutID)
	if !validDomainToolText(value.StaffingGrantID, 128) || !validDomainToolText(value.Summary, 2048) || !validDomainToolText(value.ObjectiveTitle, 256) || value.PrimaryCheckoutID == "" || value.PrimaryCheckoutRevision < 1 || len(value.ReferenceCheckoutIDs) > 8 || len(value.Agents) < 1 || len(value.Agents) > 16 || len(value.Tasks) < 1 || len(value.Tasks) > 16 {
		return value, errors.New("work proposal fields are missing or outside their bounded types")
	}
	return value, nil
}

func decodeDomainProposeKnowledgeArguments(data json.RawMessage) (domainSessionProposeKnowledgeArguments, error) {
	var value domainSessionProposeKnowledgeArguments
	if len(data) == 0 {
		return value, errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON")
		}
		return value, err
	}
	value.TaskScopeID = strings.TrimSpace(value.TaskScopeID)
	value.Type = strings.TrimSpace(value.Type)
	value.Title = strings.TrimSpace(value.Title)
	value.Body = strings.TrimSpace(value.Body)
	value.Confidence = strings.TrimSpace(value.Confidence)
	value.VerificationStatus = strings.TrimSpace(value.VerificationStatus)
	value.FreshnessPolicy = strings.TrimSpace(value.FreshnessPolicy)
	value.FreshUntil = strings.TrimSpace(value.FreshUntil)
	value.SupersedesRevisionID = strings.TrimSpace(value.SupersedesRevisionID)
	if (value.TaskScopeID != "" && !validDomainToolText(value.TaskScopeID, 128)) ||
		!domain.ValidKnowledgeType(value.Type) || !validDomainToolText(value.Title, 160) || !validDomainToolText(value.Body, 16*1024) ||
		!domain.ValidKnowledgeConfidence(value.Confidence) || !domain.ValidKnowledgeVerification(value.VerificationStatus) ||
		!domain.ValidKnowledgeFreshnessPolicy(value.FreshnessPolicy) || len(value.SupportingSources) > 15 ||
		(value.SupersedesRevisionID != "" && !validDomainToolText(value.SupersedesRevisionID, 128)) {
		return value, errors.New("knowledge fields are missing or outside their bounded types")
	}
	if value.FreshnessPolicy == domain.KnowledgeFreshUntilSuperseded && value.FreshUntil != "" {
		return value, errors.New("until_superseded knowledge cannot set fresh_until")
	}
	if value.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
		if _, err := time.Parse(time.RFC3339Nano, value.FreshUntil); err != nil {
			return value, errors.New("expires_at knowledge requires an RFC3339 fresh_until")
		}
	}
	seenSources := make(map[string]struct{}, len(value.SupportingSources))
	for index := range value.SupportingSources {
		source := &value.SupportingSources[index]
		source.Type = strings.TrimSpace(source.Type)
		source.ID = strings.TrimSpace(source.ID)
		source.Role = strings.TrimSpace(source.Role)
		if (source.Type != domain.KnowledgeSourceTask && source.Type != domain.KnowledgeSourceMeeting && source.Type != domain.KnowledgeSourceMeetingProposal) ||
			!validDomainToolText(source.ID, 128) || source.Role != domain.KnowledgeSourceSupporting {
			return value, errors.New("supporting knowledge sources must be bounded task or meeting references")
		}
		key := source.Type + "\x00" + source.ID
		if _, exists := seenSources[key]; exists {
			return value, errors.New("supporting knowledge sources must be unique")
		}
		seenSources[key] = struct{}{}
	}
	return value, nil
}

func validDomainToolText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
}

func validDomainToolIdentifier(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func domainToolSuccess(text string) map[string]any {
	return map[string]any{"success": true, "contentItems": []map[string]string{{"type": "inputText", "text": text}}}
}

func domainToolFailure(message string) map[string]any {
	return map[string]any{"success": false, "contentItems": []map[string]string{{"type": "inputText", "text": safeDomainSessionDiagnostic(errors.New(message))}}}
}

func safeDomainSessionDiagnostic(err error) string {
	value := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f || character >= 0x80 && character <= 0x9f {
			return ' '
		}
		return character
	}, err.Error())
	value = strings.Join(strings.Fields(value), " ")
	const maximum = 2048
	if len(value) > maximum {
		value = value[:maximum]
	}
	if value == "" {
		return "durable session operation failed"
	}
	return value
}
