package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

const (
	toolBriefing   = "crewfold_get_briefing"
	toolStatus     = "crewfold_get_status"
	toolProgress   = "crewfold_report_progress"
	toolBlocked    = "crewfold_report_blocked"
	toolArtifact   = "crewfold_publish_artifact"
	toolCompletion = "crewfold_propose_completion"
)

func (s *server) handleMCPRequest(request mcp.Request) mcp.Response {
	if request.JSONRPC != mcp.JSONRPCVersion || len(request.ID) == 0 || strings.TrimSpace(request.Method) == "" {
		return mcp.Failure(request.ID, -32600, "invalid JSON-RPC request", nil)
	}
	token, err := capabilityFromParams(request.Params)
	if err != nil {
		return mcp.Failure(request.ID, -32602, "scoped capability metadata is required", &mcp.ToolError{Code: "denied_by_policy", Message: err.Error()})
	}
	runID, err := s.capabilities.authenticate(token)
	if err != nil {
		return mcp.Failure(request.ID, -32001, "run capability denied", &mcp.ToolError{Code: "denied_by_policy", Message: err.Error()})
	}
	briefing, err := s.store.AuthorizeRunCapability(context.Background(), runID)
	if err != nil {
		errorBody := mcpErrorFromStore(err)
		_, _ = s.store.RecordRunToolCall(context.Background(), runID, mcpRequestID(request.ID), request.Method, runID, "denied", errorBody.Code)
		return mcp.Failure(request.ID, -32001, "run capability denied", &errorBody)
	}

	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]any             `json:"capabilities"`
			ClientInfo      map[string]any             `json:"clientInfo"`
			Meta            map[string]json.RawMessage `json:"_meta"`
		}
		if err := decodeMCPParams(request.Params, &params); err != nil || params.ProtocolVersion != mcp.ProtocolVersion {
			return mcp.Failure(request.ID, -32602, "unsupported MCP protocol version", &mcp.ToolError{Code: "invalid_input", Message: "Crewfold supports MCP protocol " + mcp.ProtocolVersion})
		}
		return mcp.Success(request.ID, map[string]any{
			"protocolVersion": mcp.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}, "resources": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "crewfold", "version": s.config.Version.Version},
			"instructions":    "Use only the run-scoped Crewfold briefing, status, reporting, artifact, and completion capabilities exposed here.",
		})
	case "ping":
		return mcp.Success(request.ID, map[string]any{})
	case "tools/list":
		return mcp.Success(request.ID, map[string]any{"tools": scopedMCPTools()})
	case "resources/list":
		return mcp.Success(request.ID, map[string]any{"resources": scopedMCPResources(briefing)})
	case "resources/read":
		return s.handleMCPResourceRead(request, briefing)
	case "tools/call":
		return s.handleMCPToolCall(request, briefing)
	default:
		return mcp.Failure(request.ID, -32601, "MCP method not found", nil)
	}
}

func capabilityFromParams(data json.RawMessage) (string, error) {
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(data) == 0 || json.Unmarshal(data, &params) != nil {
		return "", errors.New("params are invalid")
	}
	encoded, exists := params.Meta[mcp.CapabilityMeta]
	if !exists {
		return "", errors.New("capability metadata is absent")
	}
	var token string
	if json.Unmarshal(encoded, &token) != nil || strings.TrimSpace(token) == "" {
		return "", errors.New("capability metadata is invalid")
	}
	return token, nil
}

func (s *server) handleMCPResourceRead(request mcp.Request, briefing domain.RunBriefing) mcp.Response {
	var params struct {
		URI  string                     `json:"uri"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := decodeMCPParams(request.Params, &params); err != nil || strings.TrimSpace(params.URI) == "" {
		return s.mcpResourceDenied(request, briefing.Run.ID, "", "invalid_input", "resources/read requires a URI", "error")
	}
	uri := strings.TrimSpace(params.URI)
	briefingURI := "crewfold://runs/" + briefing.Run.ID + "/briefing"
	packetURI := "crewfold://context-packets/" + briefing.Packet.ID
	if uri != briefingURI && uri != packetURI {
		return s.mcpResourceDenied(request, briefing.Run.ID, uri, "out_of_scope", "resource is outside this run capability", "denied")
	}
	var value any = briefing
	if uri == packetURI {
		value = briefing.Packet
	}
	data, err := json.Marshal(value)
	if err != nil {
		return s.mcpResourceDenied(request, briefing.Run.ID, uri, "temporarily_unavailable", "encode scoped resource", "error")
	}
	if _, err := s.store.RecordRunToolCall(context.Background(), briefing.Run.ID, mcpRequestID(request.ID), request.Method, uri, "allowed", ""); err != nil {
		return mcp.Failure(request.ID, -32603, "record resource audit failed", nil)
	}
	return mcp.Success(request.ID, map[string]any{"contents": []mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}})
}

func (s *server) handleMCPToolCall(request mcp.Request, briefing domain.RunBriefing) mcp.Response {
	var params struct {
		Name      string                     `json:"name"`
		Arguments json.RawMessage            `json:"arguments"`
		Meta      map[string]json.RawMessage `json:"_meta"`
	}
	if err := decodeMCPParams(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
		return s.mcpDenied(request, briefing.Run.ID, "", "invalid_input", "tools/call requires a tool name and arguments", "error")
	}
	name := strings.TrimSpace(params.Name)
	var value any
	var err error
	switch name {
	case toolBriefing:
		err = decodeEmptyToolArguments(params.Arguments)
		value = briefing
	case toolStatus:
		err = decodeEmptyToolArguments(params.Arguments)
		value = map[string]any{
			"run_id": briefing.Run.ID, "run_status": briefing.Run.Status, "run_revision": briefing.Run.Revision,
			"task_id": briefing.Task.ID, "task_status": briefing.Task.Status, "task_revision": briefing.Task.Revision,
			"budget": briefing.Task.Budget, "blocked_question": briefing.Run.BlockedQuestion,
		}
	case toolProgress:
		var arguments progressArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			value, err = s.store.SubmitRunReport(context.Background(), store.CreateRunReportCommand{
				RunID: briefing.Run.ID, Kind: domain.ObservationProgress, Message: arguments.Summary,
				Evidence: arguments.EvidenceIDs, Payload: arguments, IdempotencyKey: arguments.IdempotencyKey,
			})
		}
	case toolBlocked:
		var arguments blockedArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			value, err = s.store.SubmitRunReport(context.Background(), store.CreateRunReportCommand{
				RunID: briefing.Run.ID, Kind: domain.ObservationBlocked, Message: arguments.Reason,
				Evidence: arguments.RelatedIDs, Payload: arguments, IdempotencyKey: arguments.IdempotencyKey,
			})
		}
	case toolCompletion:
		var arguments completionArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			value, err = s.store.SubmitRunReport(context.Background(), store.CreateRunReportCommand{
				RunID: briefing.Run.ID, Kind: domain.ObservationCompletion, Message: arguments.Summary,
				Evidence: arguments.EvidenceIDs, Handoff: arguments.Handoff, Payload: arguments, IdempotencyKey: arguments.IdempotencyKey,
			})
		}
	case toolArtifact:
		var arguments artifactArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			value, err = s.store.PublishRunArtifact(context.Background(), store.PublishRunArtifactCommand{
				RunID: briefing.Run.ID, Name: arguments.Name, MediaType: arguments.MediaType,
				Content: arguments.Content, IdempotencyKey: arguments.IdempotencyKey,
			})
		}
	default:
		return s.mcpDenied(request, briefing.Run.ID, name, "out_of_scope", "tool is outside this run capability", "denied")
	}
	if err != nil {
		errorBody := mcpErrorFromStore(err)
		if !isStoreError(err) {
			errorBody = mcp.ToolError{Code: "invalid_input", Message: err.Error()}
		}
		return s.mcpDenied(request, briefing.Run.ID, name, errorBody.Code, errorBody.Message, "error")
	}
	structured, err := json.Marshal(value)
	if err != nil {
		return s.mcpDenied(request, briefing.Run.ID, name, "temporarily_unavailable", "encode tool result", "error")
	}
	if _, err := s.store.RecordRunToolCall(context.Background(), briefing.Run.ID, mcpRequestID(request.ID), name, briefing.Run.ID, "allowed", ""); err != nil {
		return mcp.Failure(request.ID, -32603, "record tool audit failed", nil)
	}
	return mcp.Success(request.ID, mcp.ToolCallResult{
		Content: []mcp.Content{{Type: "text", Text: "Crewfold accepted the scoped operation."}}, StructuredContent: structured,
	})
}

func (s *server) mcpDenied(request mcp.Request, runID, targetID, code, message, outcome string) mcp.Response {
	method := request.Method
	if request.Method == "tools/call" && targetID != "" {
		method = targetID
	}
	_, auditErr := s.store.RecordRunToolCall(context.Background(), runID, mcpRequestID(request.ID), method, targetID, outcome, code)
	if auditErr != nil {
		return mcp.Failure(request.ID, -32603, "record tool audit failed", nil)
	}
	errorBody := mcp.ToolError{Code: code, Message: message, Retryable: code == "temporarily_unavailable"}
	data, _ := json.Marshal(errorBody)
	return mcp.Success(request.ID, mcp.ToolCallResult{Content: []mcp.Content{{Type: "text", Text: message}}, StructuredContent: data, IsError: true})
}

func (s *server) mcpResourceDenied(request mcp.Request, runID, targetID, code, message, outcome string) mcp.Response {
	_, auditErr := s.store.RecordRunToolCall(context.Background(), runID, mcpRequestID(request.ID), request.Method, targetID, outcome, code)
	if auditErr != nil {
		return mcp.Failure(request.ID, -32603, "record resource audit failed", nil)
	}
	errorBody := mcp.ToolError{Code: code, Message: message, Retryable: code == "temporarily_unavailable"}
	return mcp.Failure(request.ID, -32002, message, &errorBody)
}

func decodeMCPParams(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func decodeToolArguments(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return errors.New("tool arguments are required")
	}
	return decodeMCPParams(data, target)
}

func decodeEmptyToolArguments(data json.RawMessage) error {
	var arguments struct{}
	if len(data) == 0 {
		data = []byte("{}")
	}
	return decodeMCPParams(data, &arguments)
}

func scopedMCPResources(briefing domain.RunBriefing) []mcp.Resource {
	return []mcp.Resource{
		{URI: "crewfold://runs/" + briefing.Run.ID + "/briefing", Name: "Run briefing", Description: "Immutable context plus current run and task state", MIMEType: "application/json"},
		{URI: "crewfold://context-packets/" + briefing.Packet.ID, Name: "Context packet", Description: "Immutable context authority bound to this run", MIMEType: "application/json"},
	}
}

func scopedMCPTools() []mcp.Tool {
	empty := objectSchema(nil, nil)
	return []mcp.Tool{
		{Name: toolBriefing, Description: "Read this run's briefing and immutable context packet.", InputSchema: empty},
		{Name: toolStatus, Description: "Read current status and revisions for this run and task.", InputSchema: empty},
		{Name: toolProgress, Description: "Submit a structured progress report for this run.", InputSchema: objectSchema([]string{"summary", "completed", "next", "risks", "evidence_ids", "idempotency_key"}, map[string]any{"summary": stringSchema(1, 1024), "completed": stringArraySchema(), "next": stringArraySchema(), "risks": stringArraySchema(), "evidence_ids": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolBlocked, Description: "Report that this run needs an owner or coordinator decision.", InputSchema: objectSchema([]string{"reason", "needs", "severity", "related_ids", "idempotency_key"}, map[string]any{"reason": stringSchema(1, 1024), "needs": stringArraySchema(), "severity": map[string]any{"type": "string", "enum": []string{"blocking", "high", "medium", "low"}}, "related_ids": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolArtifact, Description: "Publish bounded evidence owned by this run.", InputSchema: objectSchema([]string{"name", "media_type", "content", "idempotency_key"}, map[string]any{"name": stringSchema(1, 128), "media_type": stringSchema(1, 128), "content": stringSchema(0, 32768), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolCompletion, Description: "Propose completion with an executive handoff and evidence.", InputSchema: objectSchema([]string{"summary", "handoff", "evidence_ids", "changed_paths", "checks", "remaining_risks", "unknowns", "idempotency_key"}, map[string]any{"summary": stringSchema(1, 1024), "handoff": stringSchema(1, 4096), "evidence_ids": stringArraySchema(), "changed_paths": stringArraySchema(), "checks": stringArraySchema(), "remaining_risks": stringArraySchema(), "unknowns": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
	}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

func stringSchema(minimum, maximum int) map[string]any {
	return map[string]any{"type": "string", "minLength": minimum, "maxLength": maximum}
}

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": 32, "items": stringSchema(1, 128)}
}

type progressArguments struct {
	Summary        string   `json:"summary"`
	Completed      []string `json:"completed"`
	Next           []string `json:"next"`
	Risks          []string `json:"risks"`
	EvidenceIDs    []string `json:"evidence_ids"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type blockedArguments struct {
	Reason         string   `json:"reason"`
	Needs          []string `json:"needs"`
	Severity       string   `json:"severity"`
	RelatedIDs     []string `json:"related_ids"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type completionArguments struct {
	Summary        string   `json:"summary"`
	Handoff        string   `json:"handoff"`
	EvidenceIDs    []string `json:"evidence_ids"`
	ChangedPaths   []string `json:"changed_paths"`
	Checks         []string `json:"checks"`
	RemainingRisks []string `json:"remaining_risks"`
	Unknowns       []string `json:"unknowns"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type artifactArguments struct {
	Name           string `json:"name"`
	MediaType      string `json:"media_type"`
	Content        string `json:"content"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (arguments progressArguments) validate() error {
	if err := validateMCPStringItems(arguments.Completed, arguments.Next, arguments.Risks, arguments.EvidenceIDs); err != nil {
		return err
	}
	return nil
}

func (arguments blockedArguments) validate() error {
	if arguments.Severity != "blocking" && arguments.Severity != "high" && arguments.Severity != "medium" && arguments.Severity != "low" {
		return errors.New("severity must be blocking, high, medium, or low")
	}
	return validateMCPStringItems(arguments.Needs, arguments.RelatedIDs)
}

func (arguments completionArguments) validate() error {
	return validateMCPStringItems(arguments.EvidenceIDs, arguments.ChangedPaths, arguments.Checks, arguments.RemainingRisks, arguments.Unknowns)
}

func validateMCPStringItems(collections ...[]string) error {
	for _, collection := range collections {
		if len(collection) > 32 {
			return errors.New("tool array contains more than 32 items")
		}
		for _, item := range collection {
			if strings.TrimSpace(item) == "" || len(item) > 128 || strings.ContainsAny(item, "\r\n\x00") {
				return errors.New("tool array item must contain 1 to 128 printable characters")
			}
		}
	}
	return nil
}

func mcpRequestID(id json.RawMessage) string {
	value := strings.Trim(strings.TrimSpace(string(id)), `"`)
	if value == "" || len(value) > 128 {
		return "invalid-mcp-request"
	}
	return value
}

func mcpErrorFromStore(err error) mcp.ToolError {
	code := store.ErrorCode(err)
	result := mcp.ToolError{Message: err.Error()}
	switch code {
	case store.CodeInvalidContext, store.CodeInvalidReport, store.CodeInvalidRun, store.CodeIdempotencyConflict:
		result.Code = "invalid_input"
	case store.CodeContextNotFound, store.CodeRunNotFound:
		result.Code = "out_of_scope"
	case store.CodeCapabilityExpired, store.CodeCapabilityInactive, store.CodeRunConflict:
		result.Code = "denied_by_policy"
	default:
		result.Code, result.Retryable = "temporarily_unavailable", true
	}
	return result
}

func isStoreError(err error) bool {
	var target *store.Error
	return errors.As(err, &target)
}
