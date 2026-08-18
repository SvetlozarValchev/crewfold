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
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

const (
	domainToolContext     = "crewfold_get_domain_context"
	domainToolSendMessage = "crewfold_send_message"
	domainToolCreateChild = "crewfold_create_durable_child"
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
			Description: "Send one durable typed Crewfold message to another active durable agent in this same domain. This records an immutable message and exact tool receipt; it does not grant authority or impersonate a task run.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"recipient_agent", "kind", "body"},
				"properties": map[string]any{
					"recipient_agent":     map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"kind":                map[string]any{"type": "string", "enum": []string{"inform", "question", "request", "review_request", "handoff", "decision_notice", "risk", "conflict", "approval_request"}},
					"subject":             map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
					"body":                map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"thread_id":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"reply_to_message_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				},
			},
		},
		{
			Type: "function", Name: domainToolCreateChild,
			Description: "Create one continuing durable child only through a current owner-authored Crewfold staffing grant. This creates the durable definition and hierarchy membership only; it does not assign a task, reserve a checkout, or start a run. The grant, not hierarchy or role text, bounds the domain, provider/runtime, task class, descendants, concurrency, budget, and expiry.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"grant_id", "name", "role", "operating_charter", "delegation_policy", "provider", "runtime", "max_concurrency", "task_class", "budget"},
				"properties": map[string]any{
					"grant_id":          map[string]any{"type": "string", "pattern": "^staffgrant_[0-9a-f]{32}$"},
					"name":              map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
					"role":              map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"operating_charter": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
					"delegation_policy": map[string]any{"type": "string", "enum": []string{"hands_on", "adaptive", "delegation_first"}},
					"provider":          map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"runtime":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"max_concurrency":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					"workstream":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"task_class":        map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
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
	}
	return []execution.CodexDynamicToolSpec{{
		Type: "namespace", Name: "crewfold",
		Description: "Canonical Crewfold domain context, durable messaging, and owner-bounded staffing. These tools are direct audited calls rather than programmatic code-mode tools.",
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
	} else if call.Tool != domainToolContext && call.Tool != domainToolSendMessage && call.Tool != domainToolCreateChild {
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
	} else if arguments, err := decodeDomainCreateChildArguments(call.Arguments); err != nil {
		result = domainToolFailure("durable child arguments are invalid: " + safeDomainSessionDiagnostic(err))
	} else {
		result = s.domainCreateChildToolResult(ctx, call, arguments)
	}
	receipt, err := s.store.RecordDomainAgentToolReceipt(ctx, receiptCommand, result, result["success"] == true)
	if err != nil {
		return nil, fmt.Errorf("record durable agent tool receipt: %w", err)
	}
	return receipt.Response, nil
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
	encoded, err := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:domain:durable-agent-child-result:v1",
		"agent":  result.Agent, "membership": result.Membership, "grant": result.Grant,
		"allocation": result.Allocation, "event_sequences": result.EventSequences,
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
	inbox, err := s.store.DeliverDomainAgentSessionInbox(ctx, threadID, 20)
	if err != nil {
		return domainToolFailure("read durable domain inbox: " + safeDomainSessionDiagnostic(err))
	}
	payload := map[string]any{
		"schema": "urn:crewfold:schema:domain:durable-agent-context:v1",
		"domain": scope.Project, "agent": scope.Agent, "membership": scope.Membership,
		"agent_tree": tree.Agents, "attached_repositories": inspection.Repositories, "attached_checkouts": inspection.Checkouts,
		"workstreams": objectives.Objectives, "assigned_work": assigned, "staffing_grants": staffingGrants, "inbox": inbox,
		"bounds": map[string]any{
			"workstreams_total": objectives.Total, "workstreams_truncated": objectives.HasMore,
			"tasks_examined": len(tasks.Tasks), "project_tasks_total": tasks.Total, "project_tasks_truncated": tasks.HasMore,
			"inbox_items": len(inbox), "inbox_limit": 20, "staffing_grants": len(staffingGrants),
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
	if !validDomainToolText(value.GrantID, 128) || !validDomainToolText(value.Name, 63) ||
		!validDomainToolText(value.Role, 128) || !validDomainToolText(value.Provider, 128) ||
		!validDomainToolText(value.Runtime, 128) || !validDomainToolText(value.TaskClass, 63) ||
		(value.Workstream != "" && !validDomainToolText(value.Workstream, 128)) || value.MaxConcurrency < 1 || value.MaxConcurrency > 100 ||
		value.Budget.TokenLimit < 0 || value.Budget.CostCents < 0 || value.Budget.TimeSeconds < 0 {
		return value, errors.New("child fields are missing or outside their bounded types")
	}
	return value, nil
}

func validDomainToolText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
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
