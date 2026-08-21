package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

const (
	domainToolContext          = "crewfold_get_domain_context"
	domainToolSendMessage      = "crewfold_send_message"
	domainToolAcknowledge      = "crewfold_acknowledge_message"
	domainToolCreateChild      = "crewfold_create_durable_child"
	domainToolDelegateStaffing = "crewfold_delegate_staffing_grant"
	domainToolProposeWork      = "crewfold_propose_work"
	domainToolProposeKnowledge = "crewfold_propose_knowledge"
	domainToolControlService   = "crewfold_control_managed_service"
	domainToolInspectService   = "crewfold_inspect_managed_service"
	domainToolProposeService   = "crewfold_propose_managed_service"
	domainToolRequestService   = "crewfold_request_managed_service"
	domainToolDelegateService  = "crewfold_delegate_managed_service_grant"
	runToolSendMessage         = "crewfold_run_send_message"
	runToolProposeKnowledge    = "crewfold_run_propose_knowledge"
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

type domainSessionAcknowledgeMessageArguments struct {
	MessageID string `json:"message_id"`
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
	Summary            string                                   `json:"summary"`
	ObjectiveTitle     string                                   `json:"objective_title"`
	PrimaryCheckout    string                                   `json:"primary_checkout,omitempty"`
	ReferenceCheckouts []string                                 `json:"reference_checkouts,omitempty"`
	Agents             []domainSessionProposeWorkAgentArguments `json:"agents"`
	Tasks              []domainSessionProposeWorkTaskArguments  `json:"tasks"`
}

// domainSessionProposeWorkAgentArguments is deliberately intent-shaped. The
// model names and describes a logical coworker; Crewfold resolves the current
// grant, provider/runtime envelope, exact existing membership/profile
// revisions, budgets, and the implicit source-agent parent itself.
type domainSessionProposeWorkAgentArguments struct {
	Key              string `json:"key"`
	ExistingAgent    string `json:"existing_agent,omitempty"`
	Name             string `json:"name,omitempty"`
	Role             string `json:"role,omitempty"`
	ParentKey        string `json:"parent_key,omitempty"`
	OperatingCharter string `json:"operating_charter,omitempty"`
	DelegationPolicy string `json:"delegation_policy,omitempty"`
	TaskClass        string `json:"task_class"`
	MaxConcurrency   int    `json:"max_concurrency,omitempty"`
}

type domainSessionProposeWorkTaskArguments struct {
	Key                string            `json:"key"`
	Title              string            `json:"title"`
	Description        string            `json:"description,omitempty"`
	AssigneeKey        string            `json:"assignee_key"`
	Priority           *int              `json:"priority,omitempty"`
	DependsOn          []string          `json:"depends_on,omitempty"`
	DependencyDelivery map[string]string `json:"dependency_delivery,omitempty"`
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

type domainSessionControlServiceArguments struct {
	Action                     string `json:"action"`
	GrantID                    string `json:"grant_id"`
	ExpectedGrantRevision      int64  `json:"expected_grant_revision"`
	DefinitionID               string `json:"definition_id,omitempty"`
	ExpectedDefinitionRevision int64  `json:"expected_definition_revision,omitempty"`
	InstanceID                 string `json:"instance_id,omitempty"`
	ExpectedInstanceRevision   int64  `json:"expected_instance_revision,omitempty"`
}

type domainSessionInspectServiceArguments struct {
	Action                   string `json:"action"`
	GrantID                  string `json:"grant_id"`
	ExpectedGrantRevision    int64  `json:"expected_grant_revision"`
	InstanceID               string `json:"instance_id"`
	ExpectedInstanceRevision int64  `json:"expected_instance_revision"`
}

type domainSessionRequestServiceArguments struct {
	DefinitionID     string `json:"definition_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Summary          string `json:"summary"`
}

type domainSessionProposeServiceArguments struct {
	Name             string                                     `json:"name"`
	Description      string                                     `json:"description"`
	Checkout         string                                     `json:"checkout,omitempty"`
	WorkstreamID     string                                     `json:"workstream_id,omitempty"`
	Executable       string                                     `json:"executable"`
	Arguments        []string                                   `json:"arguments"`
	WorkingDirectory string                                     `json:"working_directory,omitempty"`
	Environment      []domain.ManagedServiceEnvironmentVariable `json:"environment,omitempty"`
	NetworkMode      string                                     `json:"network_mode"`
	Health           domainSessionProposeServiceHealthArguments `json:"health"`
	RestartPolicy    string                                     `json:"restart_policy"`
	Summary          string                                     `json:"summary"`
}

// domainSessionProposeServiceHealthArguments is intent-shaped. The durable
// agent selects the observable readiness boundary; Crewfold owns the bounded
// polling interval and timeout so harmless model-chosen timing values cannot
// make an otherwise exact process proposal invalid.
type domainSessionProposeServiceHealthArguments struct {
	Type string `json:"type"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	Path string `json:"path,omitempty"`
}

type domainSessionDelegateServiceArguments struct {
	ParentGrantID              string   `json:"parent_grant_id"`
	ExpectedParentRevision     int64    `json:"expected_parent_revision"`
	ManagerAgent               string   `json:"manager_agent"`
	ExpectedMembershipRevision int64    `json:"expected_membership_revision"`
	Actions                    []string `json:"actions"`
	MaximumInstances           int      `json:"maximum_instances"`
	ExpiresAt                  string   `json:"expires_at,omitempty"`
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
			Description: "Send one durable typed Crewfold message to another active durable agent in this same domain. Read the domain context first. Continue a related existing coordination thread with new_topic=false and its thread_id. When replying, include the exact reply_to_message_id; the committed reply also acknowledges that incoming message. Use new_topic=true with a concise subject only for a genuinely distinct topic. This records an immutable message and exact tool receipt; it does not grant authority, create knowledge, or impersonate a task run.",
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
			Type: "function", Name: domainToolAcknowledge,
			Description: "Acknowledge one delivered message after this exact durable agent has processed it and no reply is warranted. A reply with reply_to_message_id acknowledges automatically. Wake success or merely listing the inbox is not acknowledgement.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"message_id"},
				"properties": map[string]any{"message_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
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
			Description: "Submit one inert, owner-reviewable checkout-bound workstream, team, and task graph. Describe intent only: Crewfold selects a current staffing grant, freezes the checkout revision, resolves any existing agent and launch profile, assigns the permitted provider/runtime, derives bounded budgets and priorities, and treats a missing parent_key as reporting directly to this coordinator. Choose the hierarchy intentionally: when a proposed agent is accountable for another proposed agent, set that child's parent_key; do not give a flat peer a lead, manager, or coordinator title. Keep a reviewer directly under this coordinator only when independence from the delivery lead is material. Never include this coordinator in agents and never invent manager keys, IDs, revisions, profiles, providers, runtimes, or budgets. New team members do not exist before acceptance. Every task names an assignee_key; omitted dependency delivery defaults to handoff_with_evidence. Owner acceptance atomically creates and places the team with the graph.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"summary", "objective_title", "agents", "tasks"},
				"properties": map[string]any{
					"summary":         map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
					"objective_title": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"primary_checkout": map[string]any{
						"type": "string", "minLength": 1, "maxLength": 4096,
						"description": "An exact checkout ID or absolute path from domain context. Omit when the domain has exactly one available writable checkout.",
					},
					"reference_checkouts": map[string]any{"type": "array", "maxItems": 8, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}},
					"agents": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{
						"oneOf": []map[string]any{
							{
								"type": "object", "additionalProperties": false,
								"required": []string{"key", "existing_agent", "task_class"},
								"properties": map[string]any{
									"key":            map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
									"existing_agent": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Current durable agent name or ID from domain context."},
									"task_class":     map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
								},
							},
							{
								"type": "object", "additionalProperties": false,
								"required": []string{"key", "name", "role", "operating_charter", "task_class"},
								"properties": map[string]any{
									"key":               map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
									"name":              map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
									"role":              map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
									"parent_key":        map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$", "description": "Another proposed agent key. Omit for a direct child of this coordinator."},
									"operating_charter": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
									"delegation_policy": map[string]any{"type": "string", "enum": []string{"hands_on", "adaptive", "delegation_first"}, "default": "hands_on"},
									"task_class":        map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
									"max_concurrency":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 1},
								},
							},
						},
					}},
					"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"key", "title", "assignee_key"},
						"properties": map[string]any{
							"key":                 map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
							"title":               map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
							"description":         map[string]any{"type": "string", "maxLength": 4096},
							"priority":            map[string]any{"type": "integer", "minimum": 0, "maximum": 1000},
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
			Description: "Propose one canonical domain knowledge revision for explicit owner review. Use this after synthesis or knowledge-curation work to retain a sourced cross-task finding or decision that future work should rely on; do not leave durable conclusions only in artifacts or coordination threads. Crewfold injects this authenticated durable agent as the exact primary source and proposer; the caller cannot forge provenance or acceptance. Optional supporting task/meeting sources must already exist in the same domain. This does not edit repository Markdown and does not make the proposal current until the owner accepts its exact state revision.",
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
		{
			Type: "function", Name: domainToolControlService,
			Description: "Start, stop, or restart one owner-reviewed local process using this durable agent's exact current managed-service grant. This is a real local effect: use only the definition, instance, grant, and revisions returned by Crewfold domain context. Start uses a definition; stop and restart use an instance. Crewfold rejects any action outside the grant and records the exact receipt.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"action", "grant_id", "expected_grant_revision"},
				"properties": map[string]any{
					"action":                       map[string]any{"type": "string", "enum": []string{domain.ManagedServiceActionStart, domain.ManagedServiceActionStop, domain.ManagedServiceActionRestart}},
					"grant_id":                     map[string]any{"type": "string", "pattern": "^svcgrant_[0-9a-f]{32}$"},
					"expected_grant_revision":      map[string]any{"type": "integer", "minimum": 1},
					"definition_id":                map[string]any{"type": "string", "pattern": "^svcdef_[0-9a-f]{32}$"},
					"expected_definition_revision": map[string]any{"type": "integer", "minimum": 1},
					"instance_id":                  map[string]any{"type": "string", "pattern": "^svcinst_[0-9a-f]{32}$"},
					"expected_instance_revision":   map[string]any{"type": "integer", "minimum": 1},
				},
			},
		},
		{
			Type: "function", Name: domainToolInspectService,
			Description: "Inspect one managed local process or read its bounded redacted logs using this durable agent's exact current grant. This is read-only but still definition-specific and revision-bound. Use only IDs and revisions returned by Crewfold domain context.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"action", "grant_id", "expected_grant_revision", "instance_id", "expected_instance_revision"},
				"properties": map[string]any{
					"action":                     map[string]any{"type": "string", "enum": []string{domain.ManagedServiceActionInspect, domain.ManagedServiceActionLogs}},
					"grant_id":                   map[string]any{"type": "string", "pattern": "^svcgrant_[0-9a-f]{32}$"},
					"expected_grant_revision":    map[string]any{"type": "integer", "minimum": 1},
					"instance_id":                map[string]any{"type": "string", "pattern": "^svcinst_[0-9a-f]{32}$"},
					"expected_instance_revision": map[string]any{"type": "integer", "minimum": 1},
				},
			},
		},
		{
			Type: "function", Name: domainToolProposeService,
			Description: "Draft one exact generic local process from the attached checkout and raise it for owner review. Use this when the owner asks to run, preview, serve, watch, or otherwise keep a repository command alive and no definition exists. Inspect the checkout first; provide the real executable and argv, relative working directory, loopback/network boundary, readiness check, and restart policy. The definition is inert. One owner acceptance grants this durable agent bounded inspect/log/start/stop/restart authority and starts exactly this revision.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"name", "description", "executable", "arguments", "network_mode", "health", "restart_policy", "summary"},
				"properties": map[string]any{
					"name":              map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"description":       map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
					"checkout":          map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"workstream_id":     map[string]any{"type": "string", "pattern": "^obj_[0-9a-f]{32}$"},
					"executable":        map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"arguments":         map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 4096}},
					"working_directory": map[string]any{"type": "string", "maxLength": 4096},
					"environment": map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{
						"type": "object", "additionalProperties": false, "required": []string{"name", "value"},
						"properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "value": map[string]any{"type": "string", "maxLength": 4096}},
					}},
					"network_mode": map[string]any{"type": "string", "enum": []string{domain.ManagedServiceNetworkNone, domain.ManagedServiceNetworkLoopback}},
					"health": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": []string{domain.ManagedServiceHealthProcess, domain.ManagedServiceHealthTCP, domain.ManagedServiceHealthHTTP}}, "host": map[string]any{"type": "string", "enum": []string{"127.0.0.1", "localhost", "::1"}}, "port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "path": map[string]any{"type": "string", "maxLength": 2048},
					}},
					"restart_policy": map[string]any{"type": "string", "enum": []string{domain.ManagedServiceRestartNever, domain.ManagedServiceRestartOnFailure, domain.ManagedServiceRestartOnDaemon}},
					"summary":        map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				},
			},
		},
		{
			Type: "function", Name: domainToolRequestService,
			Description: "Raise one inert owner-review request to start an exact owner-reviewed local process definition when this agent has no direct start grant. This never starts a process. State why the process is needed and what the owner will be enabling.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"definition_id", "expected_revision", "summary"},
				"properties": map[string]any{
					"definition_id":     map[string]any{"type": "string", "pattern": "^svcdef_[0-9a-f]{32}$"},
					"expected_revision": map[string]any{"type": "integer", "minimum": 1},
					"summary":           map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				},
			},
		},
		{
			Type: "function", Name: domainToolDelegateService,
			Description: "Delegate a strict subset of this durable agent's exact managed-service grant to one active durable child. The child can only receive actions, instance count, and expiry already held by the parent grant.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"parent_grant_id", "expected_parent_revision", "manager_agent", "expected_membership_revision", "actions", "maximum_instances"},
				"properties": map[string]any{
					"parent_grant_id":              map[string]any{"type": "string", "pattern": "^svcgrant_[0-9a-f]{32}$"},
					"expected_parent_revision":     map[string]any{"type": "integer", "minimum": 1},
					"manager_agent":                map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"expected_membership_revision": map[string]any{"type": "integer", "minimum": 1},
					"actions": map[string]any{"type": "array", "minItems": 1, "maxItems": 6, "uniqueItems": true,
						"items": map[string]any{"type": "string", "enum": []string{domain.ManagedServiceActionInspect, domain.ManagedServiceActionLogs, domain.ManagedServiceActionStart, domain.ManagedServiceActionStop, domain.ManagedServiceActionRestart, domain.ManagedServiceActionDelegate}}},
					"maximum_instances": map[string]any{"type": "integer", "minimum": 1, "maximum": 8},
					"expires_at":        map[string]any{"type": "string", "format": "date-time"},
				},
			},
		},
	}
	for _, runTool := range scopedMCPTools() {
		name := runTool.Name
		switch name {
		case toolSend:
			name = runToolSendMessage
		case toolKnowledge:
			name = runToolProposeKnowledge
		case toolAcknowledge:
			// One acknowledgement tool is advertised. During an accepted task
			// turn it resolves through the run mailbox; otherwise it resolves
			// through the durable domain session mailbox.
			continue
		}
		tools = append(tools, execution.CodexDynamicNamespaceTool{
			Type: "function", Name: name,
			Description: "Accepted task turn only. " + runTool.Description,
			InputSchema: runTool.InputSchema,
		})
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
	if runID, ok := s.ensureDomainSessionHost().runForTool(call.ThreadID, call.TurnID); ok && isRunDynamicTool(call.Tool) {
		return s.domainRunToolResult(ctx, runID, call), nil
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
	} else if call.Tool != domainToolContext && call.Tool != domainToolSendMessage && call.Tool != domainToolAcknowledge && call.Tool != domainToolCreateChild && call.Tool != domainToolDelegateStaffing && call.Tool != domainToolProposeWork && call.Tool != domainToolProposeKnowledge && call.Tool != domainToolControlService && call.Tool != domainToolInspectService && call.Tool != domainToolProposeService && call.Tool != domainToolRequestService && call.Tool != domainToolDelegateService {
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
	} else if call.Tool == domainToolAcknowledge {
		var arguments domainSessionAcknowledgeMessageArguments
		if err := decodeStrictDomainToolArguments(call.Arguments, &arguments); err != nil || !validDomainToolText(strings.TrimSpace(arguments.MessageID), 128) {
			result = domainToolFailure("acknowledgement arguments require one bounded message_id")
		} else {
			result = s.domainAcknowledgeMessageToolResult(ctx, call, strings.TrimSpace(arguments.MessageID))
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
	} else if call.Tool == domainToolControlService {
		if arguments, err := decodeDomainControlServiceArguments(call.Arguments); err != nil {
			result = domainToolFailure("managed-service control arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainControlServiceToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolInspectService {
		if arguments, err := decodeDomainInspectServiceArguments(call.Arguments); err != nil {
			result = domainToolFailure("managed-service inspection arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainInspectServiceToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolProposeService {
		if arguments, err := decodeDomainProposeServiceArguments(call.Arguments); err != nil {
			result = domainToolFailure("managed-service proposal arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainProposeServiceToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolRequestService {
		if arguments, err := decodeDomainRequestServiceArguments(call.Arguments); err != nil {
			result = domainToolFailure("managed-service request arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainRequestServiceToolResult(ctx, call, arguments)
		}
	} else if call.Tool == domainToolDelegateService {
		if arguments, err := decodeDomainDelegateServiceArguments(call.Arguments); err != nil {
			result = domainToolFailure("managed-service delegation arguments are invalid: " + safeDomainSessionDiagnostic(err))
		} else {
			result = s.domainDelegateServiceToolResult(ctx, call, arguments)
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

func isRunDynamicTool(name string) bool {
	if name == runToolSendMessage || name == runToolProposeKnowledge {
		return true
	}
	if name == domainToolSendMessage || name == domainToolProposeKnowledge || name == domainToolContext ||
		name == domainToolCreateChild || name == domainToolDelegateStaffing || name == domainToolProposeWork ||
		name == domainToolControlService || name == domainToolInspectService || name == domainToolProposeService || name == domainToolRequestService ||
		name == domainToolDelegateService {
		return false
	}
	return knownMCPTool(name)
}

func canonicalRunDynamicTool(name string) string {
	switch name {
	case runToolSendMessage:
		return toolSend
	case runToolProposeKnowledge:
		return toolKnowledge
	default:
		return name
	}
}

func (s *server) domainRunToolResult(ctx context.Context, runID string, call domainSessionToolCall) map[string]any {
	briefing, err := s.store.AuthorizeRunCapability(ctx, runID)
	if err != nil {
		return domainToolFailure("accepted task authority is unavailable: " + safeDomainSessionDiagnostic(err))
	}
	params, err := json.Marshal(map[string]any{
		"name": canonicalRunDynamicTool(call.Tool), "arguments": json.RawMessage(call.Arguments),
	})
	if err != nil {
		return domainToolFailure("encode task tool call: " + safeDomainSessionDiagnostic(err))
	}
	requestID, _ := json.Marshal(call.CallID)
	response := s.handleMCPToolCall(mcp.Request{JSONRPC: mcp.JSONRPCVersion, ID: requestID, Method: "tools/call", Params: params}, briefing)
	if response.Error != nil {
		message := response.Error.Message
		if response.Error.Data != nil && response.Error.Data.Message != "" {
			message = response.Error.Data.Message
		}
		return domainToolFailure(message)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		return domainToolFailure("encode task tool result: " + safeDomainSessionDiagnostic(err))
	}
	var result mcp.ToolCallResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return domainToolFailure("decode task tool result: " + safeDomainSessionDiagnostic(err))
	}
	return map[string]any{"success": !result.IsError, "contentItems": domainRunToolContentItems(result)}
}

func domainRunToolContentItems(result mcp.ToolCallResult) []map[string]string {
	items := make([]map[string]string, 0, len(result.Content)+1)
	for _, content := range result.Content {
		if content.Type == "text" {
			items = append(items, map[string]string{"type": "inputText", "text": content.Text})
		}
	}
	// MCP text is the human-readable receipt; StructuredContent is the exact
	// packet the provider must reason from. Several tools intentionally return
	// both. Dropping the latter when text exists turns real task briefings into
	// opaque "accepted operation" messages and hides dependency evidence.
	if len(result.StructuredContent) != 0 {
		items = append(items, map[string]string{"type": "inputText", "text": string(result.StructuredContent)})
	}
	if len(items) == 0 {
		items = append(items, map[string]string{"type": "inputText", "text": "Crewfold recorded the task operation."})
	}
	return items
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

func (s *server) domainControlServiceToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionControlServiceArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	var result store.MutationResult[domain.ManagedServiceInstance]
	var err error
	if arguments.Action == domain.ManagedServiceActionStart {
		result, err = s.store.StartManagedServiceAsAgent(ctx, call.ThreadID, arguments.GrantID, arguments.ExpectedGrantRevision, arguments.DefinitionID, arguments.ExpectedDefinitionRevision, key, key)
	} else {
		result, err = s.store.RequestManagedServiceActionAsAgent(ctx, store.AgentManagedServiceActionCommand{
			ThreadID: call.ThreadID, GrantID: arguments.GrantID, ExpectedGrantRevision: arguments.ExpectedGrantRevision,
			InstanceID: arguments.InstanceID, ExpectedRevision: arguments.ExpectedInstanceRevision, Action: arguments.Action,
			IdempotencyKey: key, CorrelationID: key,
		})
	}
	if err != nil {
		return domainToolFailure("apply managed-service action: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:managed-service-action-result:v1", "action": arguments.Action,
		"instance": result.Value, "event_sequence": result.EventSequence, "effect": "queued_local_process_effect",
	})
	if err != nil {
		return domainToolFailure("encode managed-service action result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainInspectServiceToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionInspectServiceArguments) map[string]any {
	detail, err := s.store.ManagedServiceDetailAsAgent(ctx, call.ThreadID, arguments.GrantID, arguments.ExpectedGrantRevision, arguments.InstanceID, arguments.ExpectedInstanceRevision, arguments.Action)
	if err != nil {
		return domainToolFailure("inspect managed service: " + safeDomainSessionDiagnostic(err))
	}
	value := any(detail)
	if arguments.Action == domain.ManagedServiceActionLogs {
		logs, logErr := s.managedServiceLogs(ctx, detail)
		if logErr != nil {
			return domainToolFailure("read managed-service logs: " + safeDomainSessionDiagnostic(logErr))
		}
		// Revalidate after the filesystem read so a concurrent revocation cannot
		// disclose the captured bytes under a grant that is no longer current.
		if _, validateErr := s.store.ManagedServiceDetailAsAgent(ctx, call.ThreadID, arguments.GrantID, arguments.ExpectedGrantRevision, arguments.InstanceID, arguments.ExpectedInstanceRevision, arguments.Action); validateErr != nil {
			return domainToolFailure("revalidate managed-service logs authority: " + safeDomainSessionDiagnostic(validateErr))
		}
		value = logs
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:managed-service-inspection-result:v1", "action": arguments.Action,
		"result": value, "effect": "read_only",
	})
	if err != nil {
		return domainToolFailure("encode managed-service inspection result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainRequestServiceToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionRequestServiceArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.SubmitManagedServiceRequest(ctx, store.SubmitManagedServiceRequestCommand{
		ThreadID: call.ThreadID, DefinitionID: arguments.DefinitionID, ExpectedRevision: arguments.ExpectedRevision,
		Summary: arguments.Summary, IdempotencyKey: key, CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("submit managed-service owner request: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:managed-service-request-result:v1", "request": result.Value,
		"event_sequence": result.EventSequence, "effect": "none_until_owner_acceptance",
	})
	if err != nil {
		return domainToolFailure("encode managed-service request result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainProposeServiceToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionProposeServiceArguments) map[string]any {
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, call.ThreadID)
	if err != nil {
		return domainToolFailure("resolve managed-service proposal scope: " + safeDomainSessionDiagnostic(err))
	}
	inspection, err := s.store.InspectProject(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return domainToolFailure("inspect managed-service proposal checkout: " + safeDomainSessionDiagnostic(err))
	}
	checkout, err := resolveDomainWorkCheckout(inspection.Checkouts, arguments.Checkout, true)
	if err != nil {
		return domainToolFailure("resolve managed-service proposal checkout: " + safeDomainSessionDiagnostic(err))
	}
	executable := arguments.Executable
	if !filepath.IsAbs(executable) {
		executable, err = exec.LookPath(executable)
		if err != nil {
			return domainToolFailure("resolve managed-service executable: " + safeDomainSessionDiagnostic(err))
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return domainToolFailure("resolve managed-service executable path: " + safeDomainSessionDiagnostic(err))
	}
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	maximumRestarts := 3
	if arguments.RestartPolicy == domain.ManagedServiceRestartNever {
		maximumRestarts = 0
	}
	health := domain.ManagedServiceHealthCheck{
		Type: arguments.Health.Type, Host: arguments.Health.Host, Port: arguments.Health.Port, Path: arguments.Health.Path,
		IntervalMillis: 1000, TimeoutMillis: 500,
	}
	definition, err := s.store.CreateManagedServiceDefinitionAsAgent(ctx, call.ThreadID, store.CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, WorkstreamID: arguments.WorkstreamID,
		CheckoutID: checkout.ID, Name: arguments.Name, Description: arguments.Description, Executable: filepath.Clean(executable),
		Arguments: arguments.Arguments, WorkingDirectory: arguments.WorkingDirectory, Environment: arguments.Environment,
		Profile: "local-process", ProfileRevision: 1, NetworkMode: arguments.NetworkMode, Health: health,
		RestartPolicy: arguments.RestartPolicy, MaximumRestarts: maximumRestarts, RestartCooldownMillis: 500,
		StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 5000, OutputByteLimit: 262144,
		CapacityClass: domain.ManagedServiceCapacityLocalDevelop, IdempotencyKey: key + "-definition", CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("record inert managed-service definition: " + safeDomainSessionDiagnostic(err))
	}
	request, err := s.store.SubmitManagedServiceRequest(ctx, store.SubmitManagedServiceRequestCommand{
		ThreadID: call.ThreadID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		Summary: arguments.Summary, IdempotencyKey: key + "-request", CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("submit managed-service owner request: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:managed-service-proposal-result:v1", "definition": definition.Value,
		"request": request.Value, "event_sequence": request.EventSequence,
		"effect": "none_until_owner_acceptance", "acceptance_effect": "grant_requesting_agent_and_start_exact_definition",
	})
	if err != nil {
		return domainToolFailure("encode managed-service proposal result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainDelegateServiceToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionDelegateServiceArguments) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.DelegateManagedServiceGrant(ctx, store.DelegateManagedServiceGrantCommand{
		ThreadID: call.ThreadID, ParentGrantID: arguments.ParentGrantID, ExpectedParentRevision: arguments.ExpectedParentRevision,
		ManagerAgentIdentifier: arguments.ManagerAgent, ExpectedMembershipRevision: arguments.ExpectedMembershipRevision,
		Actions: arguments.Actions, MaximumInstances: arguments.MaximumInstances, ExpiresAt: arguments.ExpiresAt,
		IdempotencyKey: key, CorrelationID: key,
	})
	if err != nil {
		return domainToolFailure("delegate managed-service grant: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:managed-service-grant-result:v1", "grant": result.Value,
		"event_sequence": result.EventSequence,
	})
	if err != nil {
		return domainToolFailure("encode managed-service grant result: " + safeDomainSessionDiagnostic(err))
	}
	return domainToolSuccess(string(encoded))
}

func (s *server) domainProposeWorkToolResult(ctx context.Context, call domainSessionToolCall, arguments domainSessionProposeWorkArguments) map[string]any {
	grant, content, err := s.resolveDomainWorkProposal(ctx, call.ThreadID, arguments)
	if err != nil {
		return domainToolFailure("resolve work proposal intent: " + safeDomainSessionDiagnostic(err))
	}
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.SubmitDomainWorkProposal(ctx, store.SubmitDomainWorkProposalCommand{
		ThreadID: call.ThreadID, StaffingGrantID: grant.ID, Summary: arguments.Summary, Content: content,
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

func (s *server) resolveDomainWorkProposal(ctx context.Context, threadID string, arguments domainSessionProposeWorkArguments) (domain.DomainAgentStaffingGrant, domain.DomainWorkProposalContent, error) {
	var emptyGrant domain.DomainAgentStaffingGrant
	var emptyContent domain.DomainWorkProposalContent
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, threadID)
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	inspection, err := s.store.InspectProject(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	primary, err := resolveDomainWorkCheckout(inspection.Checkouts, arguments.PrimaryCheckout, true)
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	references := make([]string, 0, len(arguments.ReferenceCheckouts))
	seenCheckouts := map[string]bool{primary.ID: true}
	for _, selector := range arguments.ReferenceCheckouts {
		checkout, resolveErr := resolveDomainWorkCheckout(inspection.Checkouts, selector, false)
		if resolveErr != nil {
			return emptyGrant, emptyContent, resolveErr
		}
		if seenCheckouts[checkout.ID] {
			return emptyGrant, emptyContent, fmt.Errorf("checkout %q is selected more than once", selector)
		}
		seenCheckouts[checkout.ID] = true
		references = append(references, checkout.ID)
	}

	tree, err := s.store.DomainAgentTree(ctx, scope.Workspace.ID, scope.Project.ID)
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	launchProfiles, err := s.store.LaunchProfiles(ctx, store.ListLaunchProfilesQuery{
		WorkspaceIdentifier: scope.Workspace.ID,
		ProjectIdentifier:   scope.Project.ID,
		Status:              domain.LaunchProfileActive,
		Limit:               100,
	})
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	grants, err := s.store.DomainAgentStaffingGrants(ctx, scope.Workspace.ID, scope.Project.ID, scope.Agent.ID)
	if err != nil {
		return emptyGrant, emptyContent, err
	}
	for _, grant := range grants {
		content, resolveErr := resolveDomainWorkWithGrant(scope, primary, references, tree.Agents, launchProfiles, grant, arguments)
		if resolveErr == nil {
			return grant, content, nil
		}
	}
	return emptyGrant, emptyContent, errors.New("no current staffing grant can authorize the requested team and task classes; ask the owner to expand this coordinator's staffing scope instead of inventing grant fields")
}

func resolveDomainWorkCheckout(checkouts []domain.Checkout, selector string, primary bool) (domain.Checkout, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" && !primary {
		return domain.Checkout{}, errors.New("reference checkout cannot be empty")
	}
	if selector != "" {
		for _, checkout := range checkouts {
			if checkout.ID == selector || checkout.Path == selector {
				if primary && (checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode == domain.WriteModeReadOnly) {
					return domain.Checkout{}, fmt.Errorf("primary checkout %q is unavailable or read-only", selector)
				}
				return checkout, nil
			}
		}
		return domain.Checkout{}, fmt.Errorf("checkout %q is not attached to this domain", selector)
	}
	var selected domain.Checkout
	for _, checkout := range checkouts {
		if checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode == domain.WriteModeReadOnly {
			continue
		}
		if selected.ID != "" {
			return domain.Checkout{}, errors.New("more than one writable checkout is available; name primary_checkout by exact path from domain context")
		}
		selected = checkout
	}
	if selected.ID == "" {
		return domain.Checkout{}, errors.New("this domain has no available writable checkout")
	}
	return selected, nil
}

func resolveDomainWorkWithGrant(scope domain.DomainAgentSessionScope, primary domain.Checkout, references []string, tree []domain.DomainAgent, launchProfiles []domain.LaunchProfile, grant domain.DomainAgentStaffingGrant, arguments domainSessionProposeWorkArguments) (domain.DomainWorkProposalContent, error) {
	var content domain.DomainWorkProposalContent
	if grant.Status != domain.DomainStaffingGrantActive || grant.ManagerMembershipRevision != scope.Membership.Revision {
		return content, errors.New("staffing grant is not current")
	}
	agentsByName := make(map[string]domain.DomainAgent, len(tree)*2)
	for _, agent := range tree {
		agentsByName[agent.Definition.ID] = agent
		agentsByName[agent.Definition.Name] = agent
	}
	newAgentCount := 0
	for _, item := range arguments.Agents {
		if item.ExistingAgent == "" {
			newAgentCount++
		}
	}
	if !domainBudgetCanSplit(grant.Budget, len(arguments.Tasks)) || !domainBudgetCanSplit(grant.Budget, newAgentCount) {
		return content, errors.New("staffing grant budget is too small for this proposed graph")
	}
	resolvedAgents := make([]domain.DomainWorkProposalAgent, 0, len(arguments.Agents))
	for index, item := range arguments.Agents {
		if !containsDomainWorkValue(grant.TaskClasses, item.TaskClass) {
			return content, fmt.Errorf("task class %q is outside the staffing grant", item.TaskClass)
		}
		if item.ExistingAgent != "" {
			existing, found := agentsByName[item.ExistingAgent]
			if !found || existing.Definition.ID == scope.Agent.ID || existing.Membership.Status != domain.DomainAgentActive || existing.Membership.WorkstreamID != "" || !existing.Definition.Enabled {
				return content, fmt.Errorf("existing agent %q is not an available durable descendant", item.ExistingAgent)
			}
			var selected domain.LaunchProfile
			for _, profile := range launchProfiles {
				if profile.AgentID == existing.Definition.ID && profile.AgentRevision == existing.Definition.Revision && profile.CheckoutID == primary.ID && profile.Purpose == item.TaskClass && profile.ManagerGrantID == "" && domainWorkProfileAllows(grant.Profiles, profile.Provider, profile.Runtime, existing.Definition.MaxConcurrency) {
					selected = profile
					break
				}
			}
			if selected.ID == "" {
				return content, fmt.Errorf("existing agent %q has no current %s launch profile for checkout %s inside the staffing grant", item.ExistingAgent, item.TaskClass, primary.Path)
			}
			resolvedAgents = append(resolvedAgents, domain.DomainWorkProposalAgent{
				Key: item.Key, ExistingAgentID: existing.Definition.ID,
				ExistingMembershipRevision: existing.Membership.Revision,
				ExistingLaunchProfileID:    selected.ID,
			})
			continue
		}
		maxConcurrency := item.MaxConcurrency
		if maxConcurrency == 0 {
			maxConcurrency = 1
		}
		var selected domain.DomainAgentStaffingProfile
		for _, profile := range grant.Profiles {
			if maxConcurrency <= profile.MaxConcurrency {
				selected = profile
				break
			}
		}
		if selected.Provider == "" {
			return content, fmt.Errorf("no permitted execution profile supports agent %q concurrency %d", item.Name, maxConcurrency)
		}
		delegationPolicy := item.DelegationPolicy
		if delegationPolicy == "" {
			delegationPolicy = domain.DomainAgentHandsOn
		}
		resolvedAgents = append(resolvedAgents, domain.DomainWorkProposalAgent{
			Key: item.Key, Name: item.Name, Role: item.Role, ParentKey: item.ParentKey,
			OperatingCharter: item.OperatingCharter, DelegationPolicy: delegationPolicy,
			Provider: selected.Provider, Runtime: selected.Runtime, MaxConcurrency: maxConcurrency,
			TaskClass: item.TaskClass, Budget: domainBudgetPart(grant.Budget, newAgentCount, domainWorkNewAgentOrdinal(arguments.Agents, index)),
		})
	}
	resolvedTasks := make([]domain.DomainWorkProposalTask, 0, len(arguments.Tasks))
	agentClasses := make(map[string]string, len(arguments.Agents))
	for _, item := range arguments.Agents {
		agentClasses[item.Key] = item.TaskClass
	}
	for index, item := range arguments.Tasks {
		priority := 100 - index*5
		if item.Priority != nil {
			priority = *item.Priority
		}
		delivery := make(map[string]string, len(item.DependsOn))
		for _, dependency := range item.DependsOn {
			value := item.DependencyDelivery[dependency]
			if value == "" {
				value = domain.DependencyDeliveryHandoffWithEvidence
			}
			delivery[dependency] = value
		}
		resolvedTasks = append(resolvedTasks, domain.DomainWorkProposalTask{
			Key: item.Key, Title: item.Title, Description: item.Description,
			TaskClass: agentClasses[item.AssigneeKey], Priority: priority,
			Budget:      domainBudgetPart(grant.Budget, len(arguments.Tasks), index),
			AssigneeKey: item.AssigneeKey, DependsOn: item.DependsOn, DependencyDelivery: delivery,
		})
	}
	return domain.DomainWorkProposalContent{
		ObjectiveTitle: arguments.ObjectiveTitle, ObjectiveBudget: grant.Budget,
		PrimaryCheckoutID: primary.ID, PrimaryCheckoutRevision: primary.Revision,
		ReferenceCheckoutIDs: references, Agents: resolvedAgents, Tasks: resolvedTasks,
	}, nil
}

func domainWorkNewAgentOrdinal(items []domainSessionProposeWorkAgentArguments, through int) int {
	ordinal := 0
	for index := 0; index < through; index++ {
		if items[index].ExistingAgent == "" {
			ordinal++
		}
	}
	return ordinal
}

func domainBudgetCanSplit(value domain.Budget, count int) bool {
	if count == 0 {
		return true
	}
	return (value.TokenLimit == 0 || value.TokenLimit >= int64(count)) &&
		(value.CostCents == 0 || value.CostCents >= int64(count)) &&
		(value.TimeSeconds == 0 || value.TimeSeconds >= int64(count))
}

func domainBudgetPart(value domain.Budget, count, index int) domain.Budget {
	if count == 0 {
		return domain.Budget{}
	}
	part := func(total int64) int64 {
		if total == 0 {
			return 0
		}
		base, remainder := total/int64(count), total%int64(count)
		if int64(index) < remainder {
			base++
		}
		return base
	}
	return domain.Budget{TokenLimit: part(value.TokenLimit), CostCents: part(value.CostCents), TimeSeconds: part(value.TimeSeconds)}
}

func domainWorkProfileAllows(profiles []domain.DomainAgentStaffingProfile, provider, runtime string, concurrency int) bool {
	for _, profile := range profiles {
		if profile.Provider == provider && profile.Runtime == runtime && concurrency <= profile.MaxConcurrency {
			return true
		}
	}
	return false
}

func containsDomainWorkValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

func (s *server) domainAcknowledgeMessageToolResult(ctx context.Context, call domainSessionToolCall, messageID string) map[string]any {
	digest := sha256.Sum256([]byte(call.CallID))
	key := fmt.Sprintf("domain-tool-%x", digest[:])
	result, err := s.store.AcknowledgeDomainAgentSessionMessage(ctx, call.ThreadID, messageID, key)
	if err != nil {
		return domainToolFailure("acknowledge durable domain message: " + safeDomainSessionDiagnostic(err))
	}
	encoded, err := json.Marshal(map[string]any{
		"schema":     "urn:crewfold:schema:domain:durable-agent-message-acknowledgement:v1",
		"message_id": result.Value.Message.ID, "delivery": result.Value.Delivery,
		"event_sequence": result.EventSequence,
	})
	if err != nil {
		return domainToolFailure("encode durable domain message acknowledgement: " + safeDomainSessionDiagnostic(err))
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
	serviceDefinitions, err := s.store.ManagedServiceDefinitions(ctx, store.ListManagedServiceDefinitionsQuery{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Limit: 100,
	})
	if err != nil {
		return domainToolFailure("read managed local process definitions: " + safeDomainSessionDiagnostic(err))
	}
	serviceInstances, err := s.store.ManagedServiceInstances(ctx, store.ListManagedServiceInstancesQuery{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Limit: 100,
	})
	if err != nil {
		return domainToolFailure("read managed local process state: " + safeDomainSessionDiagnostic(err))
	}
	serviceGrants, err := s.store.ManagedServiceGrants(ctx, store.ListManagedServiceGrantsQuery{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, ManagerIdentifier: scope.Agent.ID, Limit: 100,
	})
	if err != nil {
		return domainToolFailure("read managed local process grants: " + safeDomainSessionDiagnostic(err))
	}
	serviceRequests, err := s.store.ManagedServiceRequests(ctx, store.ListManagedServiceRequestsQuery{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, AgentIdentifier: scope.Agent.ID, Limit: 100,
	})
	if err != nil {
		return domainToolFailure("read managed local process requests: " + safeDomainSessionDiagnostic(err))
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
		"launch_profiles":             launchProfiles,
		"work_proposals":              workProposals,
		"managed_service_definitions": serviceDefinitions,
		"managed_service_instances":   serviceInstances,
		"managed_service_grants":      serviceGrants,
		"managed_service_requests":    serviceRequests,
		"knowledge_revisions":         knowledge,
		"coordination_threads":        coordinationThreads,
		"bounds": map[string]any{
			"workstreams_total": objectives.Total, "workstreams_truncated": objectives.HasMore,
			"tasks_examined": len(tasks.Tasks), "project_tasks_total": tasks.Total, "project_tasks_truncated": tasks.HasMore,
			"inbox_items": len(inbox), "inbox_limit": 20, "staffing_grants": len(staffingGrants), "work_proposals": len(workProposals),
			"knowledge_revisions":         len(knowledge),
			"managed_service_definitions": len(serviceDefinitions), "managed_service_instances": len(serviceInstances),
			"managed_service_grants": len(serviceGrants), "managed_service_requests": len(serviceRequests),
			"coordination_threads": len(coordinationThreads), "coordination_thread_limit": 20,
		},
		"knowledge_authoring": map[string]any{
			"available":  true,
			"operation":  domainToolProposeKnowledge,
			"governance": "proposals are inert until the owner accepts the exact revision; this authenticated agent is recorded as primary source and proposer",
			"workflow":   "review evidence and coordination, synthesize the durable conclusion, propose each sourced finding or decision, then direct the owner to the pending Domain Home review; artifacts and messages alone are not shared memory",
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

func decodeDomainControlServiceArguments(data json.RawMessage) (domainSessionControlServiceArguments, error) {
	var value domainSessionControlServiceArguments
	if err := decodeStrictDomainToolArguments(data, &value); err != nil {
		return value, err
	}
	value.Action = strings.TrimSpace(value.Action)
	value.GrantID = strings.TrimSpace(value.GrantID)
	value.DefinitionID = strings.TrimSpace(value.DefinitionID)
	value.InstanceID = strings.TrimSpace(value.InstanceID)
	if !validDomainToolIdentifier(value.GrantID, "svcgrant_") || value.ExpectedGrantRevision < 1 {
		return value, errors.New("an exact managed-service grant and revision are required")
	}
	if value.Action == domain.ManagedServiceActionStart {
		if !validDomainToolIdentifier(value.DefinitionID, "svcdef_") || value.ExpectedDefinitionRevision < 1 || value.InstanceID != "" || value.ExpectedInstanceRevision != 0 {
			return value, errors.New("start requires one exact definition and no instance")
		}
		return value, nil
	}
	if (value.Action != domain.ManagedServiceActionStop && value.Action != domain.ManagedServiceActionRestart) || !validDomainToolIdentifier(value.InstanceID, "svcinst_") || value.ExpectedInstanceRevision < 1 || value.DefinitionID != "" || value.ExpectedDefinitionRevision != 0 {
		return value, errors.New("stop or restart requires one exact instance and no definition")
	}
	return value, nil
}

func decodeDomainInspectServiceArguments(data json.RawMessage) (domainSessionInspectServiceArguments, error) {
	var value domainSessionInspectServiceArguments
	if err := decodeStrictDomainToolArguments(data, &value); err != nil {
		return value, err
	}
	value.Action = strings.TrimSpace(value.Action)
	value.GrantID = strings.TrimSpace(value.GrantID)
	value.InstanceID = strings.TrimSpace(value.InstanceID)
	if (value.Action != domain.ManagedServiceActionInspect && value.Action != domain.ManagedServiceActionLogs) || !validDomainToolIdentifier(value.GrantID, "svcgrant_") || value.ExpectedGrantRevision < 1 || !validDomainToolIdentifier(value.InstanceID, "svcinst_") || value.ExpectedInstanceRevision < 1 {
		return value, errors.New("inspect or logs requires one exact managed-service grant and instance revision")
	}
	return value, nil
}

func decodeDomainRequestServiceArguments(data json.RawMessage) (domainSessionRequestServiceArguments, error) {
	var value domainSessionRequestServiceArguments
	if err := decodeStrictDomainToolArguments(data, &value); err != nil {
		return value, err
	}
	value.DefinitionID = strings.TrimSpace(value.DefinitionID)
	value.Summary = strings.TrimSpace(value.Summary)
	if !validDomainToolIdentifier(value.DefinitionID, "svcdef_") || value.ExpectedRevision < 1 || !validDomainToolText(value.Summary, 2048) {
		return value, errors.New("an exact definition revision and bounded owner-facing summary are required")
	}
	return value, nil
}

func decodeDomainProposeServiceArguments(data json.RawMessage) (domainSessionProposeServiceArguments, error) {
	var value domainSessionProposeServiceArguments
	if err := decodeStrictDomainToolArguments(data, &value); err != nil {
		return value, err
	}
	value.Name = strings.TrimSpace(value.Name)
	value.Description = strings.TrimSpace(value.Description)
	value.Checkout = strings.TrimSpace(value.Checkout)
	value.WorkstreamID = strings.TrimSpace(value.WorkstreamID)
	value.Executable = strings.TrimSpace(value.Executable)
	value.WorkingDirectory = strings.TrimSpace(value.WorkingDirectory)
	value.NetworkMode = strings.TrimSpace(value.NetworkMode)
	value.Health.Type = strings.TrimSpace(value.Health.Type)
	value.Health.Host = strings.TrimSpace(value.Health.Host)
	value.Health.Path = strings.TrimSpace(value.Health.Path)
	value.RestartPolicy = strings.TrimSpace(value.RestartPolicy)
	value.Summary = strings.TrimSpace(value.Summary)
	if !validDomainToolText(value.Name, 128) || !validDomainToolText(value.Description, 1024) ||
		(value.Checkout != "" && !validDomainToolText(value.Checkout, 4096)) ||
		(value.WorkstreamID != "" && !validDomainToolIdentifier(value.WorkstreamID, "obj_")) ||
		!validDomainToolText(value.Executable, 4096) || len(value.Arguments) > 64 || len(value.Environment) > 64 ||
		!validDomainToolText(value.Summary, 2048) {
		return value, errors.New("proposal requires a bounded name, checkout command, and owner-facing summary")
	}
	if value.NetworkMode != domain.ManagedServiceNetworkNone && value.NetworkMode != domain.ManagedServiceNetworkLoopback {
		return value, errors.New("network_mode must be none or loopback")
	}
	if value.RestartPolicy != domain.ManagedServiceRestartNever && value.RestartPolicy != domain.ManagedServiceRestartOnFailure && value.RestartPolicy != domain.ManagedServiceRestartOnDaemon {
		return value, errors.New("restart_policy is not a supported managed-process policy")
	}
	if !validDomainManagedServiceHealth(value.NetworkMode, value.Health) {
		return value, errors.New("health must be process-only without an endpoint, or an exact loopback TCP/HTTP endpoint")
	}
	for _, argument := range value.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') || len([]byte(argument)) > 4096 {
			return value, errors.New("process arguments must be bounded NUL-free UTF-8")
		}
	}
	return value, nil
}

func validDomainManagedServiceHealth(network string, health domainSessionProposeServiceHealthArguments) bool {
	switch health.Type {
	case domain.ManagedServiceHealthProcess:
		return network == domain.ManagedServiceNetworkNone && health.Host == "" && health.Port == 0 && health.Path == ""
	case domain.ManagedServiceHealthTCP:
		return network == domain.ManagedServiceNetworkLoopback && domainManagedServiceLoopbackHost(health.Host) && health.Port >= 1 && health.Port <= 65535 && health.Path == ""
	case domain.ManagedServiceHealthHTTP:
		return network == domain.ManagedServiceNetworkLoopback && domainManagedServiceLoopbackHost(health.Host) && health.Port >= 1 && health.Port <= 65535 && strings.HasPrefix(health.Path, "/") && validDomainToolText(health.Path, 2048)
	default:
		return false
	}
}

func domainManagedServiceLoopbackHost(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func decodeDomainDelegateServiceArguments(data json.RawMessage) (domainSessionDelegateServiceArguments, error) {
	var value domainSessionDelegateServiceArguments
	if err := decodeStrictDomainToolArguments(data, &value); err != nil {
		return value, err
	}
	value.ParentGrantID = strings.TrimSpace(value.ParentGrantID)
	value.ManagerAgent = strings.TrimSpace(value.ManagerAgent)
	value.ExpiresAt = strings.TrimSpace(value.ExpiresAt)
	if !validDomainToolIdentifier(value.ParentGrantID, "svcgrant_") || value.ExpectedParentRevision < 1 || !validDomainToolText(value.ManagerAgent, 128) || value.ExpectedMembershipRevision < 1 || len(value.Actions) < 1 || len(value.Actions) > 6 || value.MaximumInstances < 1 || value.MaximumInstances > 8 {
		return value, errors.New("delegation requires exact parent and child authority plus bounded actions and capacity")
	}
	seen := map[string]bool{}
	for index := range value.Actions {
		value.Actions[index] = strings.TrimSpace(value.Actions[index])
		action := value.Actions[index]
		if seen[action] || !map[string]bool{domain.ManagedServiceActionInspect: true, domain.ManagedServiceActionLogs: true, domain.ManagedServiceActionStart: true, domain.ManagedServiceActionStop: true, domain.ManagedServiceActionRestart: true, domain.ManagedServiceActionDelegate: true}[action] {
			return value, errors.New("delegated actions must be distinct known managed-service actions")
		}
		seen[action] = true
	}
	return value, nil
}

func decodeStrictDomainToolArguments(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
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
	value.Summary = strings.TrimSpace(value.Summary)
	value.ObjectiveTitle = strings.TrimSpace(value.ObjectiveTitle)
	value.PrimaryCheckout = strings.TrimSpace(value.PrimaryCheckout)
	for index := range value.ReferenceCheckouts {
		value.ReferenceCheckouts[index] = strings.TrimSpace(value.ReferenceCheckouts[index])
	}
	if !validDomainToolText(value.Summary, 2048) || !validDomainToolText(value.ObjectiveTitle, 256) ||
		(value.PrimaryCheckout != "" && !validDomainToolText(value.PrimaryCheckout, 4096)) ||
		len(value.ReferenceCheckouts) > 8 || len(value.Agents) < 1 || len(value.Agents) > 16 || len(value.Tasks) < 1 || len(value.Tasks) > 16 {
		return value, errors.New("work proposal fields are missing or outside their bounded types")
	}
	seenCheckouts := map[string]bool{}
	for _, checkout := range value.ReferenceCheckouts {
		if !validDomainToolText(checkout, 4096) || seenCheckouts[checkout] {
			return value, errors.New("reference checkouts must be distinct bounded paths or identifiers")
		}
		seenCheckouts[checkout] = true
	}
	agents := make(map[string]string, len(value.Agents))
	for index := range value.Agents {
		item := &value.Agents[index]
		var valid bool
		item.Key, valid = normalizeDomainWorkSlug(item.Key, 32)
		if !valid {
			return value, fmt.Errorf("agent key %q must be a bounded logical label", item.Key)
		}
		item.ExistingAgent = strings.TrimSpace(item.ExistingAgent)
		item.Name = strings.TrimSpace(item.Name)
		item.Role = strings.TrimSpace(item.Role)
		if strings.TrimSpace(item.ParentKey) != "" {
			item.ParentKey, valid = normalizeDomainWorkSlug(item.ParentKey, 32)
			if !valid {
				return value, fmt.Errorf("parent key %q must name a proposed agent", item.ParentKey)
			}
		}
		item.OperatingCharter = strings.TrimSpace(item.OperatingCharter)
		item.DelegationPolicy = strings.TrimSpace(item.DelegationPolicy)
		item.TaskClass, valid = normalizeDomainWorkSlug(item.TaskClass, 63)
		if !valid {
			return value, fmt.Errorf("task class %q must be a bounded logical label", item.TaskClass)
		}
		if _, exists := agents[item.Key]; exists {
			return value, errors.New("agent keys must be unique")
		}
		existing := item.ExistingAgent != ""
		if existing {
			if !validDomainToolText(item.ExistingAgent, 128) || item.Name != "" || item.Role != "" || item.ParentKey != "" || item.OperatingCharter != "" || item.DelegationPolicy != "" || item.MaxConcurrency != 0 {
				return value, errors.New("an existing agent needs only key, existing_agent, and task_class")
			}
		} else {
			if !validDomainWorkSlug(item.Name, 63) || !validDomainToolText(item.Role, 128) || !validDomainToolText(item.OperatingCharter, 8192) ||
				(item.DelegationPolicy != "" && item.DelegationPolicy != domain.DomainAgentHandsOn && item.DelegationPolicy != domain.DomainAgentAdaptive && item.DelegationPolicy != domain.DomainAgentDelegationFirst) ||
				item.MaxConcurrency < 0 || item.MaxConcurrency > 100 {
				return value, errors.New("new agent intent is missing a name, role, charter, or valid optional policy")
			}
		}
		agents[item.Key] = item.TaskClass
	}
	for _, item := range value.Agents {
		if item.ParentKey != "" {
			if _, exists := agents[item.ParentKey]; !exists || item.ParentKey == item.Key {
				return value, errors.New("parent_key must name a different proposed agent key")
			}
		}
	}
	tasks := make(map[string]bool, len(value.Tasks))
	for index := range value.Tasks {
		item := &value.Tasks[index]
		var valid bool
		item.Key, valid = normalizeDomainWorkSlug(item.Key, 32)
		if !valid {
			return value, fmt.Errorf("task key %q must be a bounded logical label", item.Key)
		}
		item.Title = strings.TrimSpace(item.Title)
		item.Description = strings.TrimSpace(item.Description)
		item.AssigneeKey, valid = normalizeDomainWorkSlug(item.AssigneeKey, 32)
		if !valid {
			return value, fmt.Errorf("assignee key %q must name a proposed agent", item.AssigneeKey)
		}
		if !validDomainToolText(item.Title, 256) ||
			(item.Description != "" && !validDomainToolText(item.Description, 4096)) || agents[item.AssigneeKey] == "" ||
			(item.Priority != nil && (*item.Priority < 0 || *item.Priority > 1000)) || len(item.DependsOn) > 15 {
			return value, errors.New("task intent needs a unique key, title, known assignee_key, and bounded optional fields")
		}
		if tasks[item.Key] {
			return value, errors.New("task keys must be unique")
		}
		tasks[item.Key] = true
		seenDependencies := map[string]bool{}
		for dependencyIndex := range item.DependsOn {
			item.DependsOn[dependencyIndex], valid = normalizeDomainWorkSlug(item.DependsOn[dependencyIndex], 32)
			if !valid {
				return value, errors.New("task dependencies must name bounded logical task keys")
			}
			dependency := item.DependsOn[dependencyIndex]
			if dependency == item.Key || seenDependencies[dependency] {
				return value, errors.New("task dependencies must be distinct other task keys")
			}
			seenDependencies[dependency] = true
		}
		if item.DependencyDelivery == nil {
			item.DependencyDelivery = map[string]string{}
		}
		normalizedDelivery := make(map[string]string, len(item.DependencyDelivery))
		for dependency, delivery := range item.DependencyDelivery {
			dependency, valid = normalizeDomainWorkSlug(dependency, 32)
			delivery = strings.TrimSpace(delivery)
			if !valid || !seenDependencies[dependency] || (delivery != domain.DependencyDeliveryCompletion && delivery != domain.DependencyDeliveryHandoff && delivery != domain.DependencyDeliveryHandoffWithEvidence) {
				return value, errors.New("dependency_delivery may only refine a listed dependency")
			}
			normalizedDelivery[dependency] = delivery
		}
		item.DependencyDelivery = normalizedDelivery
	}
	for _, item := range value.Tasks {
		for _, dependency := range item.DependsOn {
			if !tasks[dependency] {
				return value, errors.New("task dependency references an unknown task key")
			}
		}
	}
	return value, nil
}

func validDomainWorkSlug(value string, maximum int) bool {
	if value == "" || len(value) > maximum || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

// Proposal keys are local glue, not durable identifiers. Accept the common
// labels models naturally emit and canonicalize them before any authority is
// resolved; exact stored entities remain subject to their stricter names.
func normalizeDomainWorkSlug(value string, maximum int) (string, bool) {
	value = strings.TrimSpace(value)
	var normalized strings.Builder
	normalized.Grow(len(value))
	separator := false
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z':
			character += 'a' - 'A'
			normalized.WriteRune(character)
			separator = false
		case character >= 'a' && character <= 'z' || character >= '0' && character <= '9':
			normalized.WriteRune(character)
			separator = false
		case character == '-' || character == '_' || character == ' ' || character == '\t':
			if normalized.Len() > 0 && !separator {
				normalized.WriteByte('-')
				separator = true
			}
		default:
			return "", false
		}
	}
	result := strings.TrimSuffix(normalized.String(), "-")
	return result, validDomainWorkSlug(result, maximum)
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
