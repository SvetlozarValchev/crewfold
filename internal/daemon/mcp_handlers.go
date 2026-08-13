package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

const (
	toolBriefing            = "crewfold_get_briefing"
	toolStatus              = "crewfold_get_status"
	toolContextDelta        = "crewfold_get_context_delta"
	toolContextDeltaAck     = "crewfold_acknowledge_context_delta"
	toolInbox               = "crewfold_list_inbox"
	toolRead                = "crewfold_read_message"
	toolSend                = "crewfold_send_message"
	toolAcknowledge         = "crewfold_acknowledge_message"
	toolProgress            = "crewfold_report_progress"
	toolBlocked             = "crewfold_report_blocked"
	toolArtifact            = "crewfold_publish_artifact"
	toolKnowledge           = "crewfold_propose_knowledge"
	toolContradictionReport = "crewfold_report_contradiction"
	// toolKnowledgeAccept is a reserved governance operation. It is recognized
	// so attempts receive a durable policy denial, but it is never advertised or
	// included in a run capability; acceptance remains local-owner-only.
	toolKnowledgeAccept = "crewfold_accept_knowledge"
	// toolContradictionConfirm is likewise recognized but never advertised or
	// included in a run capability. Only the local owner can confirm a report.
	toolContradictionConfirm  = "crewfold_confirm_contradiction"
	toolManagerProposalAccept = "crewfold_accept_manager_proposal"
	toolProposeTasks          = "crewfold_propose_tasks"
	toolProposeAssignment     = "crewfold_propose_assignment"
	toolProposeReview         = "crewfold_propose_review"
	toolProposeEscalation     = "crewfold_propose_escalation"
	toolCompletion            = "crewfold_propose_completion"
	toolRunCheck              = "crewfold_run_check"
	toolListCheckResults      = "crewfold_list_check_results"
	toolInspectCheckResult    = "crewfold_inspect_check_result"
	toolProposeCheckRepair    = "crewfold_propose_check_repair"
	// Repair acceptance remains local-owner-only. Recognizing the name makes a
	// probe auditable while the immutable packet allowlist always denies it.
	toolCheckRepairAccept = "crewfold_accept_check_repair"
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
			"instructions":    "Use only the run-scoped Crewfold briefing, mailbox, status, reporting, artifact, and completion capabilities exposed here.",
		})
	case "ping":
		return mcp.Success(request.ID, map[string]any{})
	case "tools/list":
		allowed := briefing.Packet.Policy.AllowedTools
		if briefing.Packet.CheckWatchGrant != nil {
			if _, err := s.store.AuthorizeRunCheckWatchGrant(context.Background(), briefing.Run.ID, ""); err != nil {
				allowed = withoutCheckWatchTools(allowed)
			}
		}
		return mcp.Success(request.ID, map[string]any{"tools": allowedMCPTools(allowed)})
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
	if !knownMCPTool(name) {
		return s.mcpDenied(request, briefing.Run.ID, name, "invalid_input", "tool is not supported", "error")
	}
	if !containsString(briefing.Packet.Policy.AllowedTools, name) {
		return s.mcpDenied(request, briefing.Run.ID, name, "denied_by_policy", "tool is absent from this immutable run capability", "denied")
	}
	var value any
	var err error
	switch name {
	case toolBriefing:
		err = decodeEmptyToolArguments(params.Arguments)
		value = briefing
	case toolStatus:
		err = decodeEmptyToolArguments(params.Arguments)
		if err == nil {
			var contextState domain.ContextDeltaFetchResult
			contextState, err = s.store.FetchRunContextDelta(context.Background(), briefing.Run.ID)
			contextStatus := map[string]any(nil)
			if err == nil {
				contextStatus = liveContextStatus(briefing.Packet, contextState)
			}
			if err == nil {
				value = map[string]any{
					"run_id": briefing.Run.ID, "run_status": briefing.Run.Status, "run_revision": briefing.Run.Revision,
					"task_id": briefing.Task.ID, "task_status": briefing.Task.Status, "task_revision": briefing.Task.Revision,
					"budget": briefing.Task.Budget, "blocked_question": briefing.Run.BlockedQuestion, "context": contextStatus,
				}
			}
		}
	case toolContextDelta:
		err = decodeEmptyToolArguments(params.Arguments)
		if err == nil {
			value, err = s.store.FetchRunContextDelta(context.Background(), briefing.Run.ID)
		}
	case toolContextDeltaAck:
		var arguments acknowledgeContextDeltaArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			value, err = s.store.AcknowledgeRunContextDelta(context.Background(), store.AcknowledgeContextDeltaCommand{
				RunID: briefing.Run.ID, DeltaID: arguments.DeltaID, ExpectedSequence: arguments.ExpectedSequence,
				IdempotencyKey: arguments.IdempotencyKey,
			})
		}
	case toolInbox:
		var arguments inboxArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			value, err = s.store.RunInbox(context.Background(), briefing.Run.ID, arguments.Limit)
		}
	case toolRead:
		var arguments messageTransitionArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			var result store.MutationResult[domain.InboxItem]
			result, err = s.store.ReadRunMessage(context.Background(), briefing.Run.ID, arguments.MessageID, arguments.IdempotencyKey)
			value = result.Value
		}
	case toolAcknowledge:
		var arguments messageTransitionArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			var result store.MutationResult[domain.InboxItem]
			result, err = s.store.AcknowledgeRunMessage(context.Background(), briefing.Run.ID, arguments.MessageID, arguments.IdempotencyKey)
			value = result.Value
		}
	case toolSend:
		var arguments sendMessageArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			var result store.MutationResult[domain.MessageMutation]
			result, err = s.store.SendMessage(context.Background(), store.SendMessageCommand{
				WorkspaceIdentifier: briefing.Run.WorkspaceID, SenderRunID: briefing.Run.ID,
				RecipientAgent: arguments.RecipientAgent, ThreadID: arguments.ThreadID,
				Kind: arguments.Kind, Subject: arguments.Subject, Body: arguments.Body,
				ArtifactIDs: arguments.ArtifactIDs, ReplyToMessageID: arguments.ReplyToMessageID,
				IdempotencyKey: arguments.IdempotencyKey, CorrelationID: "mcp-" + mcpRequestID(request.ID),
			})
			value = result.Value
			if err == nil {
				s.processMessageWakeJobs()
			}
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
	case toolKnowledge:
		var arguments proposeKnowledgeArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			var result store.KnowledgeMutationResult
			result, err = s.store.ProposeKnowledge(context.Background(), store.ProposeKnowledgeCommand{
				WorkspaceIdentifier: briefing.Run.WorkspaceID, ProjectIdentifier: briefing.Run.ProjectID,
				TaskScopeID: arguments.TaskScopeID, Type: arguments.Type, Title: arguments.Title, Body: arguments.Body,
				Confidence: arguments.Confidence, VerificationStatus: arguments.VerificationStatus,
				FreshnessPolicy: arguments.FreshnessPolicy, FreshUntil: arguments.FreshUntil,
				Sources:              []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: briefing.Task.ID, Role: domain.KnowledgeSourcePrimary}},
				SupersedesRevisionID: arguments.SupersedesRevisionID,
				Actor:                domain.KnowledgeActor{ID: briefing.Run.ID, Type: domain.KnowledgeActorAgentRun},
				IdempotencyKey:       arguments.IdempotencyKey, CorrelationID: "mcp-" + mcpRequestID(request.ID),
			})
			value = result.Revision
		}
	case toolContradictionReport:
		var arguments reportContradictionArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			var result store.KnowledgeContradictionMutationResult
			result, err = s.store.ReportRunKnowledgeContradiction(context.Background(), store.ReportRunKnowledgeContradictionCommand{
				RunID: briefing.Run.ID, LeftRevisionID: arguments.LeftRevision,
				RightRevisionID: arguments.RightRevision, ReportNote: arguments.Reason,
				IdempotencyKey: arguments.IdempotencyKey,
				CorrelationID:  "mcp-" + mcpRequestID(request.ID),
			})
			value = result.Detail
		}
	case toolProposeTasks:
		value, err = s.submitManagerProposal(request, briefing, params.Arguments, domain.ManagerProposalTaskDecomposition)
	case toolProposeAssignment:
		value, err = s.submitManagerProposal(request, briefing, params.Arguments, domain.ManagerProposalAssignment)
	case toolProposeReview:
		value, err = s.submitManagerProposal(request, briefing, params.Arguments, domain.ManagerProposalReview)
	case toolProposeEscalation:
		value, err = s.submitManagerProposal(request, briefing, params.Arguments, domain.ManagerProposalEscalation)
	case toolRunCheck:
		var arguments runCheckArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		var grant *domain.ContextCheckWatchGrant
		if err == nil {
			grant, err = checkWatchGrantForOperation(briefing, domain.CheckWatchOperationRun)
		}
		if err == nil {
			_, err = s.store.AuthorizeRunCheckWatchGrant(context.Background(), briefing.Run.ID, domain.CheckWatchOperationRun)
		}
		if err == nil {
			var result store.MutationResult[domain.CheckRun]
			result, err = s.store.RunGrantedCheck(context.Background(), store.RequestGrantedCheckRunCommand{
				SourceRunID: briefing.Run.ID, CheckWatchGrantID: grant.GrantID, ExpectedGrantRevision: grant.GrantRevision,
				RequirementID: arguments.RequirementID, IdempotencyKey: arguments.IdempotencyKey,
				CorrelationID: "mcp-" + mcpRequestID(request.ID),
			})
			value = result.Value
		}
	case toolListCheckResults:
		var arguments listCheckResultsArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			_, err = checkWatchGrantForOperation(briefing, domain.CheckWatchOperationInspect)
		}
		if err == nil {
			_, err = s.store.AuthorizeRunCheckWatchGrant(context.Background(), briefing.Run.ID, domain.CheckWatchOperationInspect)
		}
		if err == nil {
			value, err = s.store.ListGrantedCheckResults(context.Background(), store.ListGrantedCheckResultsQuery{
				SourceRunID: briefing.Run.ID, After: arguments.Cursor, Limit: arguments.Limit,
			})
		}
	case toolInspectCheckResult:
		var arguments inspectCheckResultArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			_, err = checkWatchGrantForOperation(briefing, domain.CheckWatchOperationInspect)
		}
		if err == nil {
			_, err = s.store.AuthorizeRunCheckWatchGrant(context.Background(), briefing.Run.ID, domain.CheckWatchOperationInspect)
		}
		if err == nil {
			value, err = s.store.InspectGrantedCheckResult(context.Background(), briefing.Run.ID, arguments.CheckRunID)
		}
	case toolProposeCheckRepair:
		var arguments proposeCheckRepairArguments
		err = decodeToolArguments(params.Arguments, &arguments)
		if err == nil {
			err = arguments.validate()
		}
		if err == nil {
			_, err = checkWatchGrantForOperation(briefing, domain.CheckWatchOperationProposeRepair)
		}
		if err == nil {
			_, err = s.store.AuthorizeRunCheckWatchGrant(context.Background(), briefing.Run.ID, domain.CheckWatchOperationProposeRepair)
		}
		if err == nil {
			var result store.MutationResult[domain.CheckRepairProposal]
			result, err = s.store.ProposeGrantedCheckRepair(context.Background(), store.ProposeGrantedCheckRepairCommand{
				SourceRunID: briefing.Run.ID, CheckResultID: arguments.CheckResultID, Rationale: arguments.Rationale,
				IdempotencyKey: arguments.IdempotencyKey, CorrelationID: "mcp-" + mcpRequestID(request.ID),
			})
			value = result.Value
		}
	default:
		return s.mcpDenied(request, briefing.Run.ID, name, "out_of_scope", "tool is outside this run capability", "denied")
	}
	if err != nil {
		errorBody := mcpErrorFromStore(err)
		if !isStoreError(err) {
			errorBody = mcp.ToolError{Code: "invalid_input", Message: err.Error()}
		}
		outcome := "error"
		if errorBody.Code == "out_of_scope" || errorBody.Code == "denied_by_policy" {
			outcome = "denied"
		}
		return s.mcpDenied(request, briefing.Run.ID, name, errorBody.Code, errorBody.Message, outcome)
	}
	structured, err := json.Marshal(value)
	if err != nil {
		return s.mcpDenied(request, briefing.Run.ID, name, "temporarily_unavailable", "encode tool result", "error")
	}
	if _, err := s.store.RecordRunToolCall(context.Background(), briefing.Run.ID, mcpRequestID(request.ID), name, briefing.Run.ID, "allowed", ""); err != nil {
		return mcp.Failure(request.ID, -32603, "record tool audit failed", nil)
	}
	message := "Crewfold accepted the scoped operation."
	if name == toolContextDelta {
		message = "Crewfold returned this run's pending context state."
	} else if name == toolContextDeltaAck {
		message = "Crewfold recorded this run's exact context delta acknowledgement."
	}
	return mcp.Success(request.ID, mcp.ToolCallResult{
		Content: []mcp.Content{{Type: "text", Text: message}}, StructuredContent: structured,
	})
}

func liveContextStatus(packet domain.ContextPacket, state domain.ContextDeltaFetchResult) map[string]any {
	chain := state.Chain
	status := map[string]any{
		"base_packet_id": packet.ID, "base_schema": packet.Schema,
		"base_as_of_event_sequence":      packet.AsOfEventSequence,
		"state_revision":                 state.StateRevision,
		"scanned_through_event_sequence": state.ScannedThroughEventSequence,
		"latest_delta_sequence":          chain.LatestSequence,
		"acknowledged_delta_sequence":    chain.LastAcknowledgedSequence,
		"delta_count":                    chain.DeltaCount,
		"cumulative_byte_size":           chain.CumulativeByteSize,
		"status":                         state.Status,
		"rebase_required":                state.Status == domain.ContextDeltaRebaseRequired,
	}
	if chain.LatestDeltaID != "" {
		status["latest_delta_id"] = chain.LatestDeltaID
	}
	if chain.PendingDeltaID != "" {
		status["pending_delta_id"] = chain.PendingDeltaID
		status["pending_delta_sequence"] = chain.PendingSequence
	}
	if chain.LastAcknowledgedDeltaID != "" {
		status["acknowledged_delta_id"] = chain.LastAcknowledgedDeltaID
	}
	if state.RebaseReason != "" {
		status["rebase_reason"] = state.RebaseReason
	}
	return status
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
		{Name: toolContextDelta, Description: "Fetch this run's sole owner-built pending context delta. This tool never scans events or expands the run's authority.", InputSchema: empty},
		{Name: toolContextDeltaAck, Description: "Acknowledge the exact pending context delta after incorporating it into this run's work.", InputSchema: objectSchema([]string{"delta_id", "expected_sequence", "idempotency_key"}, map[string]any{"delta_id": contextDeltaIDSchema(), "expected_sequence": map[string]any{"type": "integer", "minimum": 1}, "idempotency_key": stringSchema(1, 128)})},
		{Name: toolInbox, Description: "List this agent's bounded durable inbox. Visibility is normally project-scoped; an owner-created thread may additionally authorize this exact agent, project, and task as a cross-project participant. Listing marks queued items delivered to this run.", InputSchema: objectSchema([]string{"limit"}, map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}})},
		{Name: toolRead, Description: "Read one message visible to this run's exact agent, project, and task, including an authorized cross-project participant-thread message.", InputSchema: objectSchema([]string{"message_id", "idempotency_key"}, map[string]any{"message_id": stringSchema(1, 128), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolSend, Description: "Send one bounded durable project-scoped message to an enabled workspace agent, or send within an owner-created thread that binds both agents to their exact projects and tasks. Runs cannot create a cross-project thread or invite participants.", InputSchema: objectSchema([]string{"recipient_agent", "kind", "body", "artifact_ids", "idempotency_key"}, map[string]any{"recipient_agent": stringSchema(1, 128), "thread_id": stringSchema(1, 128), "subject": stringSchema(1, 160), "kind": map[string]any{"type": "string", "enum": []string{"inform", "question", "request", "review_request", "handoff", "decision_notice", "risk", "conflict", "approval_request"}}, "body": stringSchema(1, 4096), "artifact_ids": boundedStringArraySchema(16), "reply_to_message_id": stringSchema(1, 128), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolAcknowledge, Description: "Acknowledge one message visible to this run's exact agent, project, and task, including an authorized cross-project participant-thread message.", InputSchema: objectSchema([]string{"message_id", "idempotency_key"}, map[string]any{"message_id": stringSchema(1, 128), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolProgress, Description: "Submit a structured progress report for this run.", InputSchema: objectSchema([]string{"summary", "completed", "next", "risks", "evidence_ids", "idempotency_key"}, map[string]any{"summary": stringSchema(1, 1024), "completed": stringArraySchema(), "next": stringArraySchema(), "risks": stringArraySchema(), "evidence_ids": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolBlocked, Description: "Report that this run needs an owner or coordinator decision.", InputSchema: objectSchema([]string{"reason", "needs", "severity", "related_ids", "idempotency_key"}, map[string]any{"reason": stringSchema(1, 1024), "needs": stringArraySchema(), "severity": map[string]any{"type": "string", "enum": []string{"blocking", "high", "medium", "low"}}, "related_ids": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolArtifact, Description: "Publish bounded evidence owned by this run.", InputSchema: objectSchema([]string{"name", "media_type", "content", "idempotency_key"}, map[string]any{"name": stringSchema(1, 128), "media_type": stringSchema(1, 128), "content": stringSchema(0, 32768), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolKnowledge, Description: "Propose one concise decision or finding sourced from this run's task; owner acceptance is still required.", InputSchema: objectSchema([]string{"type", "title", "body", "confidence", "verification_status", "freshness_policy", "idempotency_key"}, map[string]any{"type": map[string]any{"type": "string", "enum": []string{"decision", "finding"}}, "title": stringSchema(1, 160), "body": stringSchema(1, 16384), "confidence": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}}, "verification_status": map[string]any{"type": "string", "enum": []string{"unverified", "supported", "verified"}}, "freshness_policy": map[string]any{"type": "string", "enum": []string{"until_superseded", "expires_at"}}, "fresh_until": stringSchema(1, 64), "task_scope_id": stringSchema(1, 128), "supersedes_revision_id": stringSchema(1, 128), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolContradictionReport, Description: "Report a reasoned contradiction between two exact accepted/current knowledge revisions that both apply to this run's project and task. The local owner must still confirm it before either revision is quarantined.", InputSchema: objectSchema([]string{"left_revision", "right_revision", "reason", "idempotency_key"}, map[string]any{"left_revision": knowledgeRevisionIDSchema(), "right_revision": knowledgeRevisionIDSchema(), "reason": stringSchema(1, 2048), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolProposeAssignment, Description: "Propose assignment of an existing task through one exact owner-authored launch profile. This does not assign or launch work until local-owner acceptance and supervision.", InputSchema: managerProposalInputSchema([]string{domain.ProposalActionAssignTask})},
		{Name: toolProposeEscalation, Description: "Propose one closed supervisor response for local-owner review. This never grants open-ended control of another run or task.", InputSchema: managerProposalInputSchema([]string{domain.ProposalActionRequestAction})},
		{Name: toolProposeReview, Description: "Propose a review task through one exact owner-authored launch profile. This does not create or launch the review until local-owner acceptance and supervision.", InputSchema: managerProposalInputSchema([]string{domain.ProposalActionRequestReview})},
		{Name: toolProposeTasks, Description: "Propose a bounded task decomposition using only exact owner-allowed launch profiles. This does not create tasks until the local owner accepts it.", InputSchema: managerProposalInputSchema([]string{domain.ProposalActionCreateTask, domain.ProposalActionAddDependency, domain.ProposalActionDeclareClaimRequirement})},
		{Name: toolCompletion, Description: "Propose completion with an executive handoff and evidence.", InputSchema: objectSchema([]string{"summary", "handoff", "evidence_ids", "changed_paths", "checks", "remaining_risks", "unknowns", "idempotency_key"}, map[string]any{"summary": stringSchema(1, 1024), "handoff": stringSchema(1, 4096), "evidence_ids": stringArraySchema(), "changed_paths": stringArraySchema(), "checks": stringArraySchema(), "remaining_risks": stringArraySchema(), "unknowns": stringArraySchema(), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolRunCheck, Description: "Request one exact active project requirement whose frozen definition revision is present in this run's check-watch grant.", InputSchema: objectSchema([]string{"requirement_id", "idempotency_key"}, map[string]any{"requirement_id": checkEntityIDSchema("checkreq_"), "idempotency_key": stringSchema(1, 128)})},
		{Name: toolListCheckResults, Description: "List a bounded page of check results visible through this run's exact check-watch grant.", InputSchema: objectSchema([]string{"limit"}, map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "cursor": stringSchema(1, 256)})},
		{Name: toolInspectCheckResult, Description: "Inspect one check run and its bounded structured evidence when visible through this run's exact grant.", InputSchema: objectSchema([]string{"check_run_id"}, map[string]any{"check_run_id": checkEntityIDSchema("checkrun_")})},
		{Name: toolProposeCheckRepair, Description: "Propose one inert repair for an exact latest nonpass check result. Only the local owner may accept it.", InputSchema: objectSchema([]string{"check_result_id", "rationale", "idempotency_key"}, map[string]any{"check_result_id": checkEntityIDSchema("checkresult_"), "rationale": stringSchema(1, 4096), "idempotency_key": stringSchema(1, 128)})},
	}
}

func allowedMCPTools(allowed []string) []mcp.Tool {
	result := make([]mcp.Tool, 0, len(allowed))
	for _, tool := range scopedMCPTools() {
		if containsString(allowed, tool.Name) {
			result = append(result, tool)
		}
	}
	return result
}

func withoutCheckWatchTools(allowed []string) []string {
	result := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if name != toolRunCheck && name != toolListCheckResults && name != toolInspectCheckResult && name != toolProposeCheckRepair {
			result = append(result, name)
		}
	}
	return result
}

func knownMCPTool(name string) bool {
	if name == toolKnowledgeAccept || name == toolContradictionConfirm || name == toolManagerProposalAccept || name == toolCheckRepairAccept {
		return true
	}
	for _, tool := range scopedMCPTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func managerProposalInputSchema(actionTypes []string) map[string]any {
	actionChoices := make([]map[string]any, 0, len(actionTypes))
	for _, actionType := range actionTypes {
		actionChoices = append(actionChoices, objectSchema([]string{"type", actionType}, map[string]any{
			"type": map[string]any{"const": actionType}, actionType: managerActionPayloadSchema(actionType),
		}))
	}
	return objectSchema([]string{"summary", "actions", "idempotency_key"}, map[string]any{
		"summary": stringSchema(1, 1024),
		"actions": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 32,
			"items": map[string]any{"oneOf": actionChoices},
		},
		"idempotency_key": stringSchema(1, 128),
	})
}

// The canonical store validates the closed action union, exact task revisions,
// and launch-profile scope. The transport still closes each nested payload so a
// model cannot smuggle trusted run/workspace/provider fields through MCP.
func managerActionPayloadSchema(actionType string) map[string]any {
	existingTaskRef := func() map[string]any {
		return objectSchema([]string{"task_id", "expected_task_revision"}, map[string]any{
			"task_id": managerEntityIDSchema("task_"), "expected_task_revision": map[string]any{"type": "integer", "minimum": 1},
		})
	}
	taskRef := func(proposedAllowed bool) map[string]any {
		if !proposedAllowed {
			return existingTaskRef()
		}
		return map[string]any{"oneOf": []map[string]any{
			existingTaskRef(), objectSchema([]string{"proposal_task_key"}, map[string]any{"proposal_task_key": stringSchema(1, 64)}),
		}}
	}
	budget := objectSchema([]string{"token_limit", "cost_cents", "time_seconds"}, map[string]any{
		"token_limit":  map[string]any{"type": "integer", "minimum": 0},
		"cost_cents":   map[string]any{"type": "integer", "minimum": 0},
		"time_seconds": map[string]any{"type": "integer", "minimum": 0},
	})
	switch actionType {
	case domain.ProposalActionCreateTask:
		return objectSchema([]string{"task_key", "launch_profile_id", "title", "priority", "budget"}, map[string]any{
			"task_key": stringSchema(1, 64), "launch_profile_id": managerEntityIDSchema("lprof_"), "title": stringSchema(1, 256),
			"description": stringSchema(0, 4096), "priority": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000}, "budget": budget,
		})
	case domain.ProposalActionAddDependency:
		return objectSchema([]string{"task", "depends_on"}, map[string]any{"task": taskRef(true), "depends_on": taskRef(true)})
	case domain.ProposalActionDeclareClaimRequirement:
		return objectSchema([]string{"task", "kind", "target", "mode", "conflict_policy"}, map[string]any{
			"task": taskRef(true), "kind": map[string]any{"type": "string", "enum": []string{domain.ClaimKindPath, domain.ClaimKindComponent, domain.ClaimKindOperation}}, "target": stringSchema(1, 512),
			"mode":            map[string]any{"type": "string", "enum": []string{domain.ClaimModeExclusive, domain.ClaimModeShared, domain.ClaimModeAdvisory}},
			"conflict_policy": map[string]any{"type": "string", "enum": []string{domain.ClaimPolicyNotify, domain.ClaimPolicyDenyNew, domain.ClaimPolicyPauseScheduling, domain.ClaimPolicyRequestResolution}},
		})
	case domain.ProposalActionAssignTask:
		return objectSchema([]string{"task", "launch_profile_id"}, map[string]any{"task": taskRef(false), "launch_profile_id": managerEntityIDSchema("lprof_")})
	case domain.ProposalActionRequestReview:
		return objectSchema([]string{"task", "launch_profile_id", "title", "priority", "budget"}, map[string]any{
			"task": taskRef(false), "launch_profile_id": managerEntityIDSchema("lprof_"), "title": stringSchema(1, 256),
			"description": stringSchema(0, 4096), "priority": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000}, "budget": budget,
		})
	case domain.ProposalActionRequestAction:
		common := func(response string, required []string, extra map[string]any) map[string]any {
			properties := map[string]any{
				"response": map[string]any{"const": response}, "reason": stringSchema(1, 1024),
				"expected_revision": map[string]any{"type": "integer", "minimum": 1},
			}
			for name, schema := range extra {
				properties[name] = schema
			}
			return objectSchema(append([]string{"response", "reason", "expected_revision"}, required...), properties)
		}
		return map[string]any{"oneOf": []map[string]any{
			common(domain.ProposalResponseResumeRun, []string{"target_run_id"}, map[string]any{"target_run_id": managerEntityIDSchema("run_")}),
			common(domain.ProposalResponseStopRun, []string{"target_run_id"}, map[string]any{"target_run_id": managerEntityIDSchema("run_")}),
			common(domain.ProposalResponseRetryTask, []string{"target_task_id"}, map[string]any{"target_task_id": managerEntityIDSchema("task_")}),
			common(domain.ProposalResponseReassignTask, []string{"target_task_id", "launch_profile_id"}, map[string]any{"target_task_id": managerEntityIDSchema("task_"), "launch_profile_id": managerEntityIDSchema("lprof_")}),
		}}
	default:
		return objectSchema(nil, nil)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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

func knowledgeRevisionIDSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": `^krev_[0-9a-f]{32}$`}
}

func contextDeltaIDSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": `^cdelta_[0-9a-f]{32}$`}
}

func managerEntityIDSchema(prefix string) map[string]any {
	return map[string]any{"type": "string", "pattern": "^" + prefix + "[0-9a-f]{32}$"}
}

func checkEntityIDSchema(prefix string) map[string]any {
	return map[string]any{"type": "string", "pattern": "^" + prefix + "[0-9a-f]{32}$"}
}

func validManagerEntityID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func stringArraySchema() map[string]any {
	return boundedStringArraySchema(32)
}

func boundedStringArraySchema(maximum int) map[string]any {
	return map[string]any{"type": "array", "maxItems": maximum, "items": stringSchema(1, 128)}
}

type managerProposalArguments struct {
	Summary        string                         `json:"summary"`
	Actions        []domain.ManagerProposalAction `json:"actions"`
	IdempotencyKey string                         `json:"idempotency_key"`
}

type managerProposalWireArguments struct {
	Summary        string            `json:"summary"`
	Actions        []json.RawMessage `json:"actions"`
	IdempotencyKey string            `json:"idempotency_key"`
}

type managerProposalBudgetInput struct {
	TokenLimit  *int64 `json:"token_limit"`
	CostCents   *int64 `json:"cost_cents"`
	TimeSeconds *int64 `json:"time_seconds"`
}

func (input *managerProposalBudgetInput) value() (domain.Budget, error) {
	if input == nil || input.TokenLimit == nil || input.CostCents == nil || input.TimeSeconds == nil ||
		*input.TokenLimit < 0 || *input.CostCents < 0 || *input.TimeSeconds < 0 {
		return domain.Budget{}, errors.New("manager proposal budget requires three non-negative limits")
	}
	return domain.Budget{TokenLimit: *input.TokenLimit, CostCents: *input.CostCents, TimeSeconds: *input.TimeSeconds}, nil
}

type managerProposalTaskRefInput struct {
	TaskID               string `json:"task_id,omitempty"`
	ProposalTaskKey      string `json:"proposal_task_key,omitempty"`
	ExpectedTaskRevision *int64 `json:"expected_task_revision,omitempty"`
}

func (input *managerProposalTaskRefInput) value(proposedAllowed bool) (domain.ProposalTaskRef, error) {
	if input == nil {
		return domain.ProposalTaskRef{}, errors.New("manager proposal task reference is required")
	}
	if input.TaskID != "" {
		if input.ProposalTaskKey != "" || input.ExpectedTaskRevision == nil || *input.ExpectedTaskRevision < 1 || !validManagerEntityID(input.TaskID, "task_") {
			return domain.ProposalTaskRef{}, errors.New("existing task reference requires exactly task_id and expected_task_revision")
		}
		return domain.ProposalTaskRef{TaskID: input.TaskID, ExpectedTaskRevision: *input.ExpectedTaskRevision}, nil
	}
	if proposedAllowed && input.ProposalTaskKey != "" && len(input.ProposalTaskKey) <= 64 && input.ExpectedTaskRevision == nil {
		return domain.ProposalTaskRef{ProposalTaskKey: input.ProposalTaskKey}, nil
	}
	return domain.ProposalTaskRef{}, errors.New("manager proposal task reference does not match an allowed exact branch")
}

type managerProposalCreateTaskInput struct {
	TaskKey         string                      `json:"task_key"`
	LaunchProfileID string                      `json:"launch_profile_id"`
	Title           string                      `json:"title"`
	Description     string                      `json:"description,omitempty"`
	Priority        *int                        `json:"priority"`
	Budget          *managerProposalBudgetInput `json:"budget"`
}

func (input *managerProposalCreateTaskInput) value() (domain.ProposalCreateTaskAction, error) {
	if input == nil || len(input.TaskKey) < 1 || len(input.TaskKey) > 64 || !validManagerEntityID(input.LaunchProfileID, "lprof_") ||
		len(input.Title) < 1 || len(input.Title) > 256 || len(input.Description) > 4096 || input.Priority == nil || *input.Priority < 0 || *input.Priority > 1000 {
		return domain.ProposalCreateTaskAction{}, errors.New("create_task action does not match its strict input schema")
	}
	budget, err := input.Budget.value()
	if err != nil {
		return domain.ProposalCreateTaskAction{}, err
	}
	return domain.ProposalCreateTaskAction{TaskKey: input.TaskKey, LaunchProfileID: input.LaunchProfileID, Title: input.Title, Description: input.Description, Priority: *input.Priority, Budget: budget}, nil
}

type managerProposalAddDependencyInput struct {
	Task      *managerProposalTaskRefInput `json:"task"`
	DependsOn *managerProposalTaskRefInput `json:"depends_on"`
}

func (input *managerProposalAddDependencyInput) value() (domain.ProposalAddDependencyAction, error) {
	if input == nil {
		return domain.ProposalAddDependencyAction{}, errors.New("add_dependency action requires its exact payload")
	}
	task, err := input.Task.value(true)
	if err != nil {
		return domain.ProposalAddDependencyAction{}, err
	}
	dependsOn, err := input.DependsOn.value(true)
	if err != nil {
		return domain.ProposalAddDependencyAction{}, err
	}
	return domain.ProposalAddDependencyAction{Task: task, DependsOn: dependsOn}, nil
}

type managerProposalClaimRequirementInput struct {
	Task           *managerProposalTaskRefInput `json:"task"`
	Kind           string                       `json:"kind"`
	Target         string                       `json:"target"`
	Mode           string                       `json:"mode"`
	ConflictPolicy string                       `json:"conflict_policy"`
}

func (input *managerProposalClaimRequirementInput) value() (domain.ProposalDeclareClaimRequirementAction, error) {
	if input == nil || !domain.ValidClaimKind(input.Kind) || len(input.Target) < 1 || len(input.Target) > 512 ||
		!domain.ValidClaimMode(input.Mode) || !domain.ValidClaimPolicy(input.ConflictPolicy) {
		return domain.ProposalDeclareClaimRequirementAction{}, errors.New("declare_claim_requirement action does not match its strict input schema")
	}
	task, err := input.Task.value(true)
	if err != nil {
		return domain.ProposalDeclareClaimRequirementAction{}, err
	}
	return domain.ProposalDeclareClaimRequirementAction{Task: task, Kind: input.Kind, Target: input.Target, Mode: input.Mode, ConflictPolicy: input.ConflictPolicy}, nil
}

type managerProposalAssignTaskInput struct {
	Task            *managerProposalTaskRefInput `json:"task"`
	LaunchProfileID string                       `json:"launch_profile_id"`
}

func (input *managerProposalAssignTaskInput) value() (domain.ProposalAssignTaskAction, error) {
	if input == nil || !validManagerEntityID(input.LaunchProfileID, "lprof_") {
		return domain.ProposalAssignTaskAction{}, errors.New("assign_task action does not match its strict input schema")
	}
	task, err := input.Task.value(false)
	if err != nil {
		return domain.ProposalAssignTaskAction{}, err
	}
	return domain.ProposalAssignTaskAction{Task: task, LaunchProfileID: input.LaunchProfileID}, nil
}

type managerProposalRequestReviewInput struct {
	Task            *managerProposalTaskRefInput `json:"task"`
	LaunchProfileID string                       `json:"launch_profile_id"`
	Title           string                       `json:"title"`
	Description     string                       `json:"description,omitempty"`
	Priority        *int                         `json:"priority"`
	Budget          *managerProposalBudgetInput  `json:"budget"`
}

func (input *managerProposalRequestReviewInput) value() (domain.ProposalRequestReviewAction, error) {
	if input == nil || !validManagerEntityID(input.LaunchProfileID, "lprof_") || len(input.Title) < 1 || len(input.Title) > 256 ||
		len(input.Description) > 4096 || input.Priority == nil || *input.Priority < 0 || *input.Priority > 1000 {
		return domain.ProposalRequestReviewAction{}, errors.New("request_review action does not match its strict input schema")
	}
	task, err := input.Task.value(false)
	if err != nil {
		return domain.ProposalRequestReviewAction{}, err
	}
	budget, err := input.Budget.value()
	if err != nil {
		return domain.ProposalRequestReviewAction{}, err
	}
	return domain.ProposalRequestReviewAction{Task: task, LaunchProfileID: input.LaunchProfileID, Title: input.Title, Description: input.Description, Priority: *input.Priority, Budget: budget}, nil
}

type managerProposalRequestActionInput struct {
	Response         string `json:"response"`
	TargetRunID      string `json:"target_run_id,omitempty"`
	TargetTaskID     string `json:"target_task_id,omitempty"`
	LaunchProfileID  string `json:"launch_profile_id,omitempty"`
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func decodeManagerProposalArguments(data json.RawMessage) (managerProposalArguments, error) {
	var wire managerProposalWireArguments
	if err := decodeToolArguments(data, &wire); err != nil {
		return managerProposalArguments{}, err
	}
	result := managerProposalArguments{Summary: wire.Summary, IdempotencyKey: wire.IdempotencyKey, Actions: make([]domain.ManagerProposalAction, 0, len(wire.Actions))}
	for _, raw := range wire.Actions {
		var discriminator struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.Type == "" {
			return managerProposalArguments{}, errors.New("manager proposal action requires a supported type")
		}
		action := domain.ManagerProposalAction{Type: discriminator.Type}
		switch discriminator.Type {
		case domain.ProposalActionCreateTask:
			var input struct {
				Type       string                          `json:"type"`
				CreateTask *managerProposalCreateTaskInput `json:"create_task"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			value, err := input.CreateTask.value()
			if err != nil {
				return managerProposalArguments{}, err
			}
			action.CreateTask = &value
		case domain.ProposalActionAddDependency:
			var input struct {
				Type          string                             `json:"type"`
				AddDependency *managerProposalAddDependencyInput `json:"add_dependency"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			value, err := input.AddDependency.value()
			if err != nil {
				return managerProposalArguments{}, err
			}
			action.AddDependency = &value
		case domain.ProposalActionDeclareClaimRequirement:
			var input struct {
				Type                    string                                `json:"type"`
				DeclareClaimRequirement *managerProposalClaimRequirementInput `json:"declare_claim_requirement"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			value, err := input.DeclareClaimRequirement.value()
			if err != nil {
				return managerProposalArguments{}, err
			}
			action.DeclareClaimRequirement = &value
		case domain.ProposalActionAssignTask:
			var input struct {
				Type       string                          `json:"type"`
				AssignTask *managerProposalAssignTaskInput `json:"assign_task"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			value, err := input.AssignTask.value()
			if err != nil {
				return managerProposalArguments{}, err
			}
			action.AssignTask = &value
		case domain.ProposalActionRequestReview:
			var input struct {
				Type          string                             `json:"type"`
				RequestReview *managerProposalRequestReviewInput `json:"request_review"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			value, err := input.RequestReview.value()
			if err != nil {
				return managerProposalArguments{}, err
			}
			action.RequestReview = &value
		case domain.ProposalActionRequestAction:
			var input struct {
				Type          string                             `json:"type"`
				RequestAction *managerProposalRequestActionInput `json:"request_action"`
			}
			if err := decodeMCPParams(raw, &input); err != nil {
				return managerProposalArguments{}, err
			}
			if input.RequestAction == nil {
				return managerProposalArguments{}, errors.New("request_action action requires its exact payload")
			}
			if err := input.RequestAction.validate(); err != nil {
				return managerProposalArguments{}, err
			}
			action.RequestAction = &domain.ProposalRequestAction{
				Response: input.RequestAction.Response, TargetRunID: input.RequestAction.TargetRunID,
				TargetTaskID: input.RequestAction.TargetTaskID, LaunchProfileID: input.RequestAction.LaunchProfileID,
				Reason: input.RequestAction.Reason, ExpectedRevision: input.RequestAction.ExpectedRevision,
			}
		default:
			return managerProposalArguments{}, errors.New("manager proposal action type is unsupported")
		}
		result.Actions = append(result.Actions, action)
	}
	return result, nil
}

func (input managerProposalRequestActionInput) validate() error {
	if input.ExpectedRevision < 1 || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 1024 ||
		!utf8.ValidString(input.Reason) || strings.ContainsRune(input.Reason, '\x00') {
		return errors.New("manager escalation requires a bounded reason and positive expected revision")
	}
	switch input.Response {
	case domain.ProposalResponseResumeRun, domain.ProposalResponseStopRun:
		if input.TargetRunID == "" || input.TargetTaskID != "" || input.LaunchProfileID != "" {
			return errors.New("resume_run and stop_run require exactly target_run_id")
		}
	case domain.ProposalResponseRetryTask:
		if input.TargetTaskID == "" || input.TargetRunID != "" || input.LaunchProfileID != "" {
			return errors.New("retry_task requires exactly target_task_id")
		}
	case domain.ProposalResponseReassignTask:
		if input.TargetTaskID == "" || input.LaunchProfileID == "" || input.TargetRunID != "" {
			return errors.New("reassign_task requires exactly target_task_id and launch_profile_id")
		}
	default:
		return errors.New("manager escalation response is unsupported")
	}
	return nil
}

func (s *server) submitManagerProposal(request mcp.Request, briefing domain.RunBriefing, data json.RawMessage, kind string) (any, error) {
	arguments, err := decodeManagerProposalArguments(data)
	if err != nil {
		return nil, err
	}
	grant := briefing.Packet.ManagementGrant
	if briefing.Packet.Schema != domain.ContextPacketSchema || grant == nil || briefing.Packet.CheckWatchGrant != nil || grant.Schema != domain.ContextManagerGrantSchema {
		return nil, &store.Error{Code: store.CodeManagerGrantDenied, Message: "manager proposals require an exact context grant snapshot"}
	}
	if !containsString(grant.AllowedProposalKinds, kind) {
		return nil, &store.Error{Code: store.CodeManagerGrantDenied, Message: "proposal kind is absent from this exact manager grant"}
	}
	if strings.TrimSpace(arguments.Summary) == "" || len(arguments.Summary) > 1024 || !utf8.ValidString(arguments.Summary) || strings.ContainsRune(arguments.Summary, '\x00') {
		return nil, &store.Error{Code: store.CodeInvalidManagerProposal, Message: "manager proposal summary must contain 1 to 1024 UTF-8 bytes without NUL"}
	}
	if len(arguments.Actions) < 1 || len(arguments.Actions) > grant.MaxActions {
		return nil, &store.Error{Code: store.CodeInvalidManagerProposal, Message: "manager proposal action count is outside this grant's bound"}
	}
	if strings.TrimSpace(arguments.IdempotencyKey) == "" || len(arguments.IdempotencyKey) > 128 || !utf8.ValidString(arguments.IdempotencyKey) || strings.ContainsRune(arguments.IdempotencyKey, '\x00') {
		return nil, &store.Error{Code: store.CodeInvalidManagerProposal, Message: "manager proposal requires a bounded idempotency key"}
	}
	for _, action := range arguments.Actions {
		if action.ID != "" || action.Ordinal != 0 {
			return nil, &store.Error{Code: store.CodeInvalidManagerProposal, Message: "manager proposal action identity and ordering are assigned by Crewfold"}
		}
		if !managerActionAllowedForKind(kind, action.Type) {
			return nil, &store.Error{Code: store.CodeInvalidManagerProposal, Message: "manager proposal contains an action outside the selected proposal kind"}
		}
	}
	result, err := s.store.SubmitManagerProposal(context.Background(), store.SubmitManagerProposalCommand{
		RunID: briefing.Run.ID, ManagerGrantID: grant.GrantID, ExpectedGrantRevision: grant.GrantRevision,
		Kind: kind, Summary: arguments.Summary, AsOfEventSequence: briefing.Packet.AsOfEventSequence,
		Actions: arguments.Actions, IdempotencyKey: arguments.IdempotencyKey,
		CorrelationID: "mcp-" + mcpRequestID(request.ID),
	})
	if err != nil {
		return nil, err
	}
	return result.Proposal, nil
}

func managerActionAllowedForKind(kind, actionType string) bool {
	switch kind {
	case domain.ManagerProposalTaskDecomposition:
		return actionType == domain.ProposalActionCreateTask || actionType == domain.ProposalActionAddDependency || actionType == domain.ProposalActionDeclareClaimRequirement
	case domain.ManagerProposalAssignment:
		return actionType == domain.ProposalActionAssignTask
	case domain.ManagerProposalReview:
		return actionType == domain.ProposalActionRequestReview
	case domain.ManagerProposalEscalation:
		return actionType == domain.ProposalActionRequestAction
	default:
		return false
	}
}

func checkWatchGrantForOperation(briefing domain.RunBriefing, operation string) (*domain.ContextCheckWatchGrant, error) {
	grant := briefing.Packet.CheckWatchGrant
	if briefing.Packet.Schema != domain.ContextPacketSchema || grant == nil || briefing.Packet.ManagementGrant != nil || grant.Schema != domain.ContextCheckWatchGrantSchema ||
		grant.GrantID == "" || grant.GrantRevision < 1 ||
		grant.WorkspaceID != briefing.Run.WorkspaceID || grant.ProjectID != briefing.Run.ProjectID ||
		grant.WatcherAgentID != briefing.Run.AgentID || grant.WatcherAgentRevision != briefing.Packet.Role.Revision {
		return nil, &store.Error{Code: store.CodeCheckWatchGrantDenied, Message: "check tools require an exact context check-watch grant snapshot"}
	}
	if !containsString(grant.Operations, operation) {
		return nil, &store.Error{Code: store.CodeCheckWatchGrantDenied, Message: "check operation is absent from this exact check-watch grant"}
	}
	return grant, nil
}

type runCheckArguments struct {
	RequirementID  string `json:"requirement_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (arguments runCheckArguments) validate() error {
	if !validMCPCheckEntityID(arguments.RequirementID, "checkreq_") || !validMCPIdempotencyKey(arguments.IdempotencyKey) {
		return errors.New("check run requires an exact requirement ID and bounded idempotency key")
	}
	return nil
}

type listCheckResultsArguments struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

func (arguments listCheckResultsArguments) validate() error {
	if arguments.Limit < 1 || arguments.Limit > 50 || len(arguments.Cursor) > 256 ||
		!utf8.ValidString(arguments.Cursor) || strings.ContainsRune(arguments.Cursor, '\x00') || strings.TrimSpace(arguments.Cursor) != arguments.Cursor {
		return errors.New("check result pagination requires limit 1 through 50 and an optional bounded opaque cursor")
	}
	return nil
}

type inspectCheckResultArguments struct {
	CheckRunID string `json:"check_run_id"`
}

func (arguments inspectCheckResultArguments) validate() error {
	if !validMCPCheckEntityID(arguments.CheckRunID, "checkrun_") {
		return errors.New("check inspection requires one exact check run ID")
	}
	return nil
}

type proposeCheckRepairArguments struct {
	CheckResultID  string `json:"check_result_id"`
	Rationale      string `json:"rationale"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (arguments proposeCheckRepairArguments) validate() error {
	if !validMCPCheckEntityID(arguments.CheckResultID, "checkresult_") || strings.TrimSpace(arguments.Rationale) == "" ||
		len(arguments.Rationale) > 4096 || !utf8.ValidString(arguments.Rationale) || strings.ContainsRune(arguments.Rationale, '\x00') ||
		!validMCPIdempotencyKey(arguments.IdempotencyKey) {
		return errors.New("check repair proposal requires an exact result, bounded rationale, and bounded idempotency key")
	}
	return nil
}

func validMCPCheckEntityID(value, prefix string) bool {
	if strings.TrimSpace(value) != value || len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validMCPIdempotencyKey(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

type inboxArguments struct {
	Limit int `json:"limit"`
}

func (arguments inboxArguments) validate() error {
	if arguments.Limit < 1 || arguments.Limit > 50 {
		return errors.New("inbox limit must be from 1 to 50")
	}
	return nil
}

type messageTransitionArguments struct {
	MessageID      string `json:"message_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type acknowledgeContextDeltaArguments struct {
	DeltaID          string `json:"delta_id"`
	ExpectedSequence int64  `json:"expected_sequence"`
	IdempotencyKey   string `json:"idempotency_key"`
}

func (arguments acknowledgeContextDeltaArguments) validate() error {
	if arguments.DeltaID != strings.TrimSpace(arguments.DeltaID) || !validMCPContextDeltaID(arguments.DeltaID) || arguments.ExpectedSequence < 1 ||
		strings.TrimSpace(arguments.IdempotencyKey) == "" || len(arguments.IdempotencyKey) > 128 ||
		!utf8.ValidString(arguments.IdempotencyKey) || strings.ContainsRune(arguments.IdempotencyKey, '\x00') {
		return errors.New("context delta acknowledgement requires an exact delta ID, positive expected_sequence, and bounded idempotency key")
	}
	return nil
}

func validMCPContextDeltaID(value string) bool {
	if len(value) != len("cdelta_")+32 || !strings.HasPrefix(value, "cdelta_") {
		return false
	}
	for _, character := range value[len("cdelta_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type sendMessageArguments struct {
	RecipientAgent   string   `json:"recipient_agent"`
	ThreadID         string   `json:"thread_id,omitempty"`
	Subject          string   `json:"subject,omitempty"`
	Kind             string   `json:"kind"`
	Body             string   `json:"body"`
	ArtifactIDs      []string `json:"artifact_ids"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	IdempotencyKey   string   `json:"idempotency_key"`
}

func (arguments sendMessageArguments) validate() error {
	if arguments.ArtifactIDs == nil {
		return errors.New("message artifact_ids is required; use an empty array when no artifacts are linked")
	}
	return nil
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

type proposeKnowledgeArguments struct {
	Type                 string `json:"type"`
	Title                string `json:"title"`
	Body                 string `json:"body"`
	Confidence           string `json:"confidence"`
	VerificationStatus   string `json:"verification_status"`
	FreshnessPolicy      string `json:"freshness_policy"`
	FreshUntil           string `json:"fresh_until,omitempty"`
	TaskScopeID          string `json:"task_scope_id,omitempty"`
	SupersedesRevisionID string `json:"supersedes_revision_id,omitempty"`
	IdempotencyKey       string `json:"idempotency_key"`
}

type reportContradictionArguments struct {
	LeftRevision   string `json:"left_revision"`
	RightRevision  string `json:"right_revision"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (arguments reportContradictionArguments) validate() error {
	if !validMCPKnowledgeRevisionID(strings.TrimSpace(arguments.LeftRevision)) ||
		!validMCPKnowledgeRevisionID(strings.TrimSpace(arguments.RightRevision)) ||
		strings.TrimSpace(arguments.LeftRevision) == strings.TrimSpace(arguments.RightRevision) {
		return errors.New("contradiction report requires two distinct exact knowledge revision IDs")
	}
	if strings.TrimSpace(arguments.Reason) == "" || len(arguments.Reason) > 2048 ||
		!utf8.ValidString(arguments.Reason) || strings.ContainsRune(arguments.Reason, '\x00') {
		return errors.New("contradiction reason must contain 1 to 2048 UTF-8 bytes without NUL")
	}
	if strings.TrimSpace(arguments.IdempotencyKey) == "" || len(arguments.IdempotencyKey) > 128 ||
		!utf8.ValidString(arguments.IdempotencyKey) || strings.ContainsRune(arguments.IdempotencyKey, '\x00') {
		return errors.New("contradiction report requires a bounded idempotency key")
	}
	return nil
}

func validMCPKnowledgeRevisionID(value string) bool {
	if len(value) != len("krev_")+32 || !strings.HasPrefix(value, "krev_") {
		return false
	}
	for _, character := range value[len("krev_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (arguments proposeKnowledgeArguments) validate() error {
	if arguments.FreshnessPolicy == domain.KnowledgeFreshExpiresAt && strings.TrimSpace(arguments.FreshUntil) == "" {
		return errors.New("expires_at knowledge requires fresh_until")
	}
	if arguments.FreshnessPolicy == domain.KnowledgeFreshUntilSuperseded && strings.TrimSpace(arguments.FreshUntil) != "" {
		return errors.New("until_superseded knowledge cannot set fresh_until")
	}
	return nil
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
	case store.CodeInvalidContext, store.CodeInvalidContextDelta, store.CodeInvalidReport, store.CodeInvalidRun, store.CodeInvalidMessage, store.CodeInvalidKnowledge,
		store.CodeKnowledgeConflict, store.CodeContradictionConflict, store.CodeInvalidManagerProposal, store.CodeManagerProposalConflict,
		store.CodeInvalidManagerGrant, store.CodeInvalidCheckRequirement, store.CodeInvalidCheckWatchGrant,
		store.CodeCheckRequirementConflict, store.CodeCheckRunConflict, store.CodeCheckRepairConflict, store.CodeIdempotencyConflict:
		result.Code = "invalid_input"
	case store.CodeContextNotFound, store.CodeContextDeltaNotFound, store.CodeRunNotFound, store.CodeMessageNotFound, store.CodeKnowledgeNotFound, store.CodeContradictionNotFound,
		store.CodeManagerGrantNotFound, store.CodeManagerProposalNotFound, store.CodeCheckRequirementNotFound,
		store.CodeCheckWatchGrantNotFound, store.CodeCheckRunNotFound, store.CodeCheckRepairNotFound:
		result.Code = "out_of_scope"
	case store.CodeCapabilityExpired, store.CodeCapabilityInactive, store.CodeRunConflict, store.CodeMessageDenied, store.CodeKnowledgeDenied, store.CodeContradictionDenied,
		store.CodeManagerGrantDenied, store.CodeManagerProposalDenied, store.CodeCheckWatchGrantDenied, store.CodeCheckRepairDenied:
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
