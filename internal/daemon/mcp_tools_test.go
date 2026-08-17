package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

func TestImmutableToolAllowlistHidesLaterCapabilities(t *testing.T) {
	t.Parallel()
	frozenAllowed := []string{toolBriefing, toolStatus, toolCompletion, toolArtifact, toolBlocked, toolProgress}
	tools := allowedMCPTools(frozenAllowed, nil)
	if len(tools) != len(frozenAllowed) {
		t.Fatalf("allowedMCPTools(frozen) count = %d, want %d", len(tools), len(frozenAllowed))
	}
	for _, tool := range tools {
		if tool.Name == toolInbox || tool.Name == toolRead || tool.Name == toolSend || tool.Name == toolAcknowledge ||
			tool.Name == toolKnowledge || tool.Name == toolContradictionReport || tool.Name == toolContextDelta || tool.Name == toolContextDeltaAck ||
			tool.Name == toolRunCheck || tool.Name == toolListCheckResults || tool.Name == toolInspectCheckResult || tool.Name == toolProposeCheckRepair {
			t.Fatalf("frozen immutable capability exposed an ungranted tool %q", tool.Name)
		}
	}
	if !knownMCPTool(toolSend) || !knownMCPTool(toolKnowledge) || !knownMCPTool(toolKnowledgeAccept) ||
		!knownMCPTool(toolContradictionReport) || !knownMCPTool(toolContradictionConfirm) ||
		!knownMCPTool(toolContextDelta) || !knownMCPTool(toolContextDeltaAck) ||
		!knownMCPTool(toolProposeAssignment) || !knownMCPTool(toolProposeEscalation) || !knownMCPTool(toolProposeReview) ||
		!knownMCPTool(toolProposeTasks) || !knownMCPTool(toolManagerProposalAccept) ||
		!knownMCPTool(toolRunCheck) || !knownMCPTool(toolListCheckResults) || !knownMCPTool(toolInspectCheckResult) ||
		!knownMCPTool(toolProposeCheckRepair) || !knownMCPTool(toolCheckRepairAccept) || knownMCPTool("crewfold_unknown_tool") {
		t.Fatal("known MCP tool classification is inconsistent")
	}
}

func TestCheckWatchToolsAreOperationDerivedAndExposeNoTrustedScope(t *testing.T) {
	t.Parallel()
	wantNames := []string{toolRunCheck, toolListCheckResults, toolInspectCheckResult, toolProposeCheckRepair}
	var found []mcp.Tool
	for _, tool := range scopedMCPTools() {
		if containsString(wantNames, tool.Name) {
			found = append(found, tool)
		}
	}
	if len(found) != len(wantNames) {
		t.Fatalf("check-watch tools = %d, want %d", len(found), len(wantNames))
	}
	for index, tool := range found {
		if tool.Name != wantNames[index] {
			t.Fatalf("check-watch tool %d = %q, want %q", index, tool.Name, wantNames[index])
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		for _, forbidden := range []string{"workspace", "project", "agent", "run", "source_run_id", "grant_id", "expected_grant_revision", "checkout", "command", "executable", "arguments", "environment", "profile", "evidence_class", "recipient"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s exposes trusted scope field %q", tool.Name, forbidden)
			}
		}
	}

	base := []string{
		toolContextDeltaAck, toolAcknowledge, toolBriefing, toolContextDelta, toolStatus, toolInbox,
		toolKnowledge, toolCompletion, toolArtifact, toolRead, toolBlocked, toolContradictionReport, toolProgress, toolSend,
	}
	allowed := allowedMCPTools(append(append([]string(nil), base...), wantNames...), nil)
	gotSuffix := make([]string, 0, len(wantNames))
	for _, tool := range allowed {
		if containsString(wantNames, tool.Name) {
			gotSuffix = append(gotSuffix, tool.Name)
		}
	}
	if !reflect.DeepEqual(gotSuffix, wantNames) {
		t.Fatalf("check-watch tool order = %v, want %v", gotSuffix, wantNames)
	}
	filtered := withoutCheckWatchTools(append(append([]string(nil), base...), wantNames...))
	if !reflect.DeepEqual(filtered, base) {
		t.Fatalf("stale grant discovery filter = %v, want surviving immutable base %v", filtered, base)
	}
}

func TestCheckWatchAuthorityIsExactAndNeverDerivedFromRole(t *testing.T) {
	t.Parallel()
	const arbitraryRole = "weather-vane and evidence poet"
	exact := domain.RunBriefing{
		Run: domain.Run{ID: "run_0123456789abcdef0123456789abcdef", WorkspaceID: "ws_0123456789abcdef0123456789abcdef", ProjectID: "prj_0123456789abcdef0123456789abcdef", AgentID: "agent_0123456789abcdef0123456789abcdef"},
		Packet: domain.ContextPacket{
			Schema: domain.ContextPacketSchema, WorkspaceID: "ws_0123456789abcdef0123456789abcdef", ProjectID: "prj_0123456789abcdef0123456789abcdef", AgentID: "agent_0123456789abcdef0123456789abcdef",
			Role:            domain.ContextRole{AgentID: "agent_0123456789abcdef0123456789abcdef", Revision: 7, Role: arbitraryRole},
			CheckWatchGrant: &domain.ContextCheckWatchGrant{Schema: domain.ContextCheckWatchGrantSchema, GrantID: "checkgrant_0123456789abcdef0123456789abcdef", GrantRevision: 1, WorkspaceID: "ws_0123456789abcdef0123456789abcdef", ProjectID: "prj_0123456789abcdef0123456789abcdef", WatcherAgentID: "agent_0123456789abcdef0123456789abcdef", WatcherAgentRevision: 7, Operations: []string{domain.CheckWatchOperationInspect}},
		},
	}
	if _, err := checkWatchGrantForOperation(exact, domain.CheckWatchOperationInspect); err != nil {
		t.Fatalf("exact check-watch grant rejected: %v", err)
	}

	ungranted := exact
	ungranted.Run.AgentID = "agent_fedcba9876543210fedcba9876543210"
	ungranted.Packet = domain.ContextPacket{Schema: domain.ContextPacketSchema, AgentID: ungranted.Run.AgentID, Role: domain.ContextRole{AgentID: ungranted.Run.AgentID, Revision: 7, Role: arbitraryRole}}
	if _, err := checkWatchGrantForOperation(ungranted, domain.CheckWatchOperationInspect); store.ErrorCode(err) != store.CodeCheckWatchGrantDenied {
		t.Fatalf("same-role ungranted run error = %v, code = %q", err, store.ErrorCode(err))
	}
	for _, tool := range allowedMCPTools(baseRunToolNamesForTest(), nil) {
		if tool.Name == toolRunCheck || tool.Name == toolListCheckResults || tool.Name == toolInspectCheckResult || tool.Name == toolProposeCheckRepair {
			t.Fatalf("same-role ungranted run advertised check tool %q", tool.Name)
		}
	}
}

func TestCheckWatchArgumentsRejectCallerOwnedScopeAndUnboundedValues(t *testing.T) {
	t.Parallel()
	requirementID := "checkreq_0123456789abcdef0123456789abcdef"
	checkRunID := "checkrun_0123456789abcdef0123456789abcdef"
	checkResultID := "checkresult_0123456789abcdef0123456789abcdef"
	if err := (runCheckArguments{RequirementID: requirementID, IdempotencyKey: "run-one"}).validate(); err != nil {
		t.Fatalf("valid run-check arguments error = %v", err)
	}
	if err := (listCheckResultsArguments{Limit: 50, Cursor: checkRunID}).validate(); err != nil {
		t.Fatalf("valid list-check arguments error = %v", err)
	}
	if err := (inspectCheckResultArguments{CheckRunID: checkRunID}).validate(); err != nil {
		t.Fatalf("valid inspect-check arguments error = %v", err)
	}
	if err := (proposeCheckRepairArguments{CheckResultID: checkResultID, Rationale: "The exact failure needs bounded repair work.", IdempotencyKey: "repair-one"}).validate(); err != nil {
		t.Fatalf("valid repair arguments error = %v", err)
	}
	for name, err := range map[string]error{
		"padded requirement":  (runCheckArguments{RequirementID: " " + requirementID, IdempotencyKey: "run"}).validate(),
		"missing key":         (runCheckArguments{RequirementID: requirementID}).validate(),
		"zero limit":          (listCheckResultsArguments{}).validate(),
		"oversized cursor":    (listCheckResultsArguments{Limit: 1, Cursor: strings.Repeat("x", 257)}).validate(),
		"wrong run prefix":    (inspectCheckResultArguments{CheckRunID: requirementID}).validate(),
		"empty rationale":     (proposeCheckRepairArguments{CheckResultID: checkResultID, IdempotencyKey: "repair"}).validate(),
		"oversized rationale": (proposeCheckRepairArguments{CheckResultID: checkResultID, Rationale: strings.Repeat("x", 4097), IdempotencyKey: "repair"}).validate(),
	} {
		if err == nil {
			t.Errorf("%s unexpectedly validated", name)
		}
	}

	var injected runCheckArguments
	if err := decodeToolArguments(json.RawMessage(`{"requirement_id":"`+requirementID+`","idempotency_key":"run","project":"prj_0123456789abcdef0123456789abcdef"}`), &injected); err == nil {
		t.Fatal("run-check decoder accepted caller-selected project scope")
	}
}

func baseRunToolNamesForTest() []string {
	return []string{toolContextDeltaAck, toolAcknowledge, toolBriefing, toolContextDelta, toolStatus, toolInbox,
		toolKnowledge, toolCompletion, toolArtifact, toolRead, toolBlocked, toolContradictionReport, toolProgress, toolSend}
}

func TestManagerProposalToolsAreBoundedDerivedScopeAndCanonical(t *testing.T) {
	t.Parallel()
	wantNames := []string{toolProposeAssignment, toolProposeEscalation, toolProposeReview, toolProposeTasks}
	found := make([]mcp.Tool, 0, len(wantNames))
	for _, tool := range scopedMCPTools() {
		if containsString(wantNames, tool.Name) {
			found = append(found, tool)
		}
	}
	if len(found) != len(wantNames) {
		t.Fatalf("manager tools = %d, want %d", len(found), len(wantNames))
	}
	for index, tool := range found {
		if tool.Name != wantNames[index] {
			t.Fatalf("manager tool %d = %q, want %q", index, tool.Name, wantNames[index])
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		for _, forbidden := range []string{"workspace", "project", "objective", "run", "agent", "grant_id", "expected_grant_revision", "runtime", "provider", "scenario"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s exposes trusted scope field %q", tool.Name, forbidden)
			}
		}
		actions := properties["actions"].(map[string]any)
		if actions["minItems"] != 1 || actions["maxItems"] != 32 {
			t.Errorf("%s action bounds = %v..%v, want 1..32", tool.Name, actions["minItems"], actions["maxItems"])
		}
		choices := actions["items"].(map[string]any)["oneOf"].([]map[string]any)
		for _, choice := range choices {
			choiceProperties := choice["properties"].(map[string]any)
			if _, exists := choiceProperties["id"]; exists {
				t.Errorf("%s lets caller choose action id", tool.Name)
			}
			if _, exists := choiceProperties["ordinal"]; exists {
				t.Errorf("%s lets caller choose action ordinal", tool.Name)
			}
		}
	}
	allowed := allowedMCPTools(append([]string(nil), wantNames...), nil)
	for index, tool := range allowed {
		if tool.Name != wantNames[index] {
			t.Fatalf("allowed manager tool %d = %q, want %q", index, tool.Name, wantNames[index])
		}
	}
}

func TestManagerTaskProposalSchemaCannotExceedFrozenClaimGrant(t *testing.T) {
	t.Parallel()
	find := func(grant *domain.ContextManagerGrant) mcp.Tool {
		for _, tool := range allowedMCPTools([]string{toolProposeTasks}, grant) {
			return tool
		}
		t.Fatal("task proposal tool was not exposed")
		return mcp.Tool{}
	}
	actionTypes := func(tool mcp.Tool) []string {
		choices := tool.InputSchema["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)["oneOf"].([]map[string]any)
		values := make([]string, 0, len(choices))
		for _, choice := range choices {
			values = append(values, choice["properties"].(map[string]any)["type"].(map[string]any)["const"].(string))
		}
		return values
	}

	withoutClaims := find(&domain.ContextManagerGrant{})
	if got := actionTypes(withoutClaims); !reflect.DeepEqual(got, []string{domain.ProposalActionCreateTask, domain.ProposalActionAddDependency}) {
		t.Fatalf("zero-claim proposal actions = %v", got)
	}
	if !strings.Contains(withoutClaims.Description, "unavailable") {
		t.Fatalf("zero-claim proposal description = %q", withoutClaims.Description)
	}

	withPathClaims := find(&domain.ContextManagerGrant{AllowedClaimKinds: []string{domain.ClaimKindPath}})
	if got := actionTypes(withPathClaims); !reflect.DeepEqual(got, []string{domain.ProposalActionCreateTask, domain.ProposalActionAddDependency, domain.ProposalActionDeclareClaimRequirement}) {
		t.Fatalf("path-claim proposal actions = %v", got)
	}
	choices := withPathClaims.InputSchema["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)["oneOf"].([]map[string]any)
	claimPayload := choices[2]["properties"].(map[string]any)[domain.ProposalActionDeclareClaimRequirement].(map[string]any)
	kinds := claimPayload["properties"].(map[string]any)["kind"].(map[string]any)["enum"].([]string)
	if !reflect.DeepEqual(kinds, []string{domain.ClaimKindPath}) {
		t.Fatalf("claim kind enum = %v", kinds)
	}
}

func TestOwnerExecutiveDecisionSchemaMatchesTheStoreContract(t *testing.T) {
	t.Parallel()
	var response mcp.Tool
	for _, tool := range allowedMCPTools([]string{toolRespondToOwner}, nil) {
		response = tool
	}
	if response.Name != toolRespondToOwner {
		t.Fatal("owner response tool was not exposed")
	}
	variants := response.InputSchema["oneOf"].([]map[string]any)
	if len(variants) != 5 {
		t.Fatalf("owner response variants = %d, want 5", len(variants))
	}
	byKind := make(map[string]map[string]any, len(variants))
	for _, variant := range variants {
		properties := variant["properties"].(map[string]any)
		kind := properties["kind"].(map[string]any)["const"].(string)
		byKind[kind] = variant
	}
	for _, kind := range []string{"answer", "update", "proposal", "refusal"} {
		properties := byKind[kind]["properties"].(map[string]any)
		if properties["answer"] == nil || properties["question"] != nil || properties["choices"] != nil {
			t.Fatalf("%s response shape = %#v", kind, properties)
		}
	}
	proposalIDs := byKind["proposal"]["properties"].(map[string]any)["proposal_ids"].(map[string]any)
	if proposalIDs["minItems"] != 1 || proposalIDs["maxItems"] != 32 {
		t.Fatalf("proposal links = %#v", proposalIDs)
	}
	decisionProperties := byKind["decision"]["properties"].(map[string]any)
	if decisionProperties["answer"] != nil || decisionProperties["question"] == nil {
		t.Fatalf("decision response shape = %#v", decisionProperties)
	}
	choices := decisionProperties["choices"].(map[string]any)
	if choices["minItems"] != 2 || choices["maxItems"] != 4 {
		t.Fatalf("owner choice bounds = %v..%v, want 2..4", choices["minItems"], choices["maxItems"])
	}
	key := choices["items"].(map[string]any)["properties"].(map[string]any)["key"].(map[string]any)
	if key["pattern"] != "^[a-z][a-z0-9-]{0,31}$" {
		t.Fatalf("owner choice key pattern = %v", key["pattern"])
	}
}

func TestM21WorkbenchCompletionRequiresStructuredChecksAndChangedPaths(t *testing.T) {
	t.Parallel()

	run := domain.Run{ScenarioName: "owner-workbench"}
	valid := completionArguments{Checks: []string{"node --test"}, ChangedPaths: []string{"src/domain.js"}}
	if err := valid.validateForRun(run); err != nil {
		t.Fatalf("valid workbench completion = %v", err)
	}
	for name, arguments := range map[string]completionArguments{
		"missing checks":        {ChangedPaths: []string{"src/domain.js"}},
		"missing changed paths": {Checks: []string{"node --test"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := arguments.validateForRun(run); err == nil {
				t.Fatal("incomplete workbench completion was accepted")
			}
		})
	}
	if err := (completionArguments{}).validateForRun(domain.Run{ScenarioName: "fixture"}); err != nil {
		t.Fatalf("unrelated scenario inherited workbench acceptance: %v", err)
	}
}

func TestManagerProposalActionKindsAreClosed(t *testing.T) {
	t.Parallel()
	allowed := map[string][]string{
		domain.ManagerProposalTaskDecomposition: {domain.ProposalActionCreateTask, domain.ProposalActionAddDependency, domain.ProposalActionDeclareClaimRequirement},
		domain.ManagerProposalAssignment:        {domain.ProposalActionAssignTask},
		domain.ManagerProposalReview:            {domain.ProposalActionRequestReview},
		domain.ManagerProposalEscalation:        {domain.ProposalActionRequestAction},
	}
	all := []string{domain.ProposalActionCreateTask, domain.ProposalActionAddDependency, domain.ProposalActionDeclareClaimRequirement, domain.ProposalActionAssignTask, domain.ProposalActionRequestReview, domain.ProposalActionRequestAction}
	for kind, permitted := range allowed {
		for _, actionType := range all {
			if got, want := managerActionAllowedForKind(kind, actionType), containsString(permitted, actionType); got != want {
				t.Errorf("managerActionAllowedForKind(%q, %q) = %v, want %v", kind, actionType, got, want)
			}
		}
	}
	if managerActionAllowedForKind("unknown", domain.ProposalActionCreateTask) {
		t.Fatal("unknown proposal kind accepted")
	}
}

func TestManagerProposalDecoderRejectsCallerOwnedActionMetadataAndEscalationScope(t *testing.T) {
	t.Parallel()
	valid := `{"summary":"assign exact task","actions":[{"type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef","expected_task_revision":1},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"proposal-one"}`
	decoded, err := decodeManagerProposalArguments(json.RawMessage(valid))
	if err != nil || len(decoded.Actions) != 1 || decoded.Actions[0].AssignTask == nil || decoded.Actions[0].ID != "" || decoded.Actions[0].Ordinal != 0 {
		t.Fatalf("valid manager proposal decode = %#v, %v", decoded, err)
	}
	for name, raw := range map[string]string{
		"action id":             `{"summary":"x","actions":[{"id":"mpact_0123456789abcdef0123456789abcdef","type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef","expected_task_revision":1},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"x"}`,
		"zero ordinal":          `{"summary":"x","actions":[{"ordinal":0,"type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef","expected_task_revision":1},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"x"}`,
		"mixed task ref":        `{"summary":"x","actions":[{"type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef","proposal_task_key":"A","expected_task_revision":1},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"x"}`,
		"proposal assignment":   `{"summary":"x","actions":[{"type":"assign_task","assign_task":{"task":{"proposal_task_key":"A"},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"x"}`,
		"missing task revision": `{"summary":"x","actions":[{"type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef"},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"}}],"idempotency_key":"x"}`,
		"missing create budget": `{"summary":"x","actions":[{"type":"create_task","create_task":{"task_key":"A","launch_profile_id":"lprof_0123456789abcdef0123456789abcdef","title":"A","priority":0}}],"idempotency_key":"x"}`,
		"partial create budget": `{"summary":"x","actions":[{"type":"create_task","create_task":{"task_key":"A","launch_profile_id":"lprof_0123456789abcdef0123456789abcdef","title":"A","priority":0,"budget":{"token_limit":0,"cost_cents":0}}}],"idempotency_key":"x"}`,
		"target agent":          `{"summary":"x","actions":[{"type":"request_action","request_action":{"response":"reassign_task","target_task_id":"task_0123456789abcdef0123456789abcdef","target_agent_id":"agent_0123456789abcdef0123456789abcdef","launch_profile_id":"lprof_0123456789abcdef0123456789abcdef","reason":"retry elsewhere","expected_revision":1}}],"idempotency_key":"x"}`,
		"mixed targets":         `{"summary":"x","actions":[{"type":"request_action","request_action":{"response":"resume_run","target_run_id":"run_0123456789abcdef0123456789abcdef","target_task_id":"task_0123456789abcdef0123456789abcdef","reason":"resume","expected_revision":1}}],"idempotency_key":"x"}`,
		"irrelevant payload":    `{"summary":"x","actions":[{"type":"assign_task","assign_task":{"task":{"task_id":"task_0123456789abcdef0123456789abcdef","expected_task_revision":1},"launch_profile_id":"lprof_0123456789abcdef0123456789abcdef"},"request_review":{}}],"idempotency_key":"x"}`,
		"missing payload":       `{"summary":"x","actions":[{"type":"assign_task"}],"idempotency_key":"x"}`,
	} {
		if _, err := decodeManagerProposalArguments(json.RawMessage(raw)); err == nil {
			t.Errorf("%s payload unexpectedly decoded", name)
		}
	}
}

func TestContextDeltaToolsDeriveScopeAndRequireExactAcknowledgement(t *testing.T) {
	t.Parallel()
	var fetch, acknowledge *mcp.Tool
	for _, tool := range scopedMCPTools() {
		switch tool.Name {
		case toolContextDelta:
			copy := tool
			fetch = &copy
		case toolContextDeltaAck:
			copy := tool
			acknowledge = &copy
		}
	}
	if fetch == nil || acknowledge == nil {
		t.Fatalf("context delta tools missing: fetch=%#v acknowledge=%#v", fetch, acknowledge)
	}
	if properties := fetch.InputSchema["properties"].(map[string]any); len(properties) != 0 {
		t.Fatalf("fetch tool exposes caller-selected arguments: %#v", properties)
	}
	properties := acknowledge.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"delta_id", "expected_sequence", "idempotency_key"} {
		if _, exists := properties[field]; !exists {
			t.Errorf("acknowledgement schema omits %q", field)
		}
	}
	for _, forbidden := range []string{"workspace", "run", "task", "agent", "context_packet_id"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("acknowledgement schema exposes trusted field %q", forbidden)
		}
	}
	validID := "cdelta_0123456789abcdef0123456789abcdef"
	if err := (acknowledgeContextDeltaArguments{DeltaID: validID, ExpectedSequence: 1, IdempotencyKey: "ack-one"}).validate(); err != nil {
		t.Fatalf("valid acknowledgement error = %v", err)
	}
	for name, value := range map[string]acknowledgeContextDeltaArguments{
		"missing delta":    {ExpectedSequence: 1, IdempotencyKey: "ack"},
		"wrong prefix":     {DeltaID: "ctx_0123456789abcdef0123456789abcdef", ExpectedSequence: 1, IdempotencyKey: "ack"},
		"padded delta":     {DeltaID: " " + validID, ExpectedSequence: 1, IdempotencyKey: "ack"},
		"zero sequence":    {DeltaID: validID, IdempotencyKey: "ack"},
		"missing key":      {DeltaID: validID, ExpectedSequence: 1},
		"key contains NUL": {DeltaID: validID, ExpectedSequence: 1, IdempotencyKey: "bad\x00key"},
	} {
		if err := value.validate(); err == nil {
			t.Errorf("%s acknowledgement unexpectedly validated", name)
		}
	}
}

func TestLiveRunStatusExposesPendingChainWithoutExpandingAuthority(t *testing.T) {
	t.Parallel()
	packet := domain.ContextPacket{Schema: domain.ContextPacketSchema, ID: "ctx_0123456789abcdef0123456789abcdef", AsOfEventSequence: 7}
	deltaID := "cdelta_0123456789abcdef0123456789abcdef"
	status := liveContextStatus(packet, domain.ContextDeltaFetchResult{
		Status: domain.ContextDeltaPending, StateRevision: 3, ScannedThroughEventSequence: 12,
		Chain: domain.ContextDeltaChain{
			LatestDeltaID: deltaID, LatestSequence: 2, PendingDeltaID: deltaID, PendingSequence: 2,
			LastAcknowledgedDeltaID: "cdelta_fedcba9876543210fedcba9876543210", LastAcknowledgedSequence: 1,
			DeltaCount: 2, CumulativeByteSize: 2048,
		},
	})
	for name, wanted := range map[string]any{
		"base_packet_id": packet.ID, "base_schema": domain.ContextPacketSchema,
		"base_as_of_event_sequence": int64(7), "state_revision": int64(3),
		"scanned_through_event_sequence": int64(12), "latest_delta_id": deltaID,
		"latest_delta_sequence": int64(2), "pending_delta_id": deltaID,
		"pending_delta_sequence": int64(2), "acknowledged_delta_sequence": int64(1),
		"delta_count": int64(2), "cumulative_byte_size": 2048,
		"status": domain.ContextDeltaPending, "rebase_required": false,
	} {
		if status[name] != wanted {
			t.Errorf("live context status %s = %#v, want %#v", name, status[name], wanted)
		}
	}
	for _, forbidden := range []string{"workspace_id", "project_id", "task_id", "agent_id", "delta"} {
		if _, exists := status[forbidden]; exists {
			t.Errorf("live context status expands authority with %q", forbidden)
		}
	}
}

func TestMessageToolArgumentsEnforceAdvertisedRequiredFields(t *testing.T) {
	t.Parallel()
	if err := (inboxArguments{}).validate(); err == nil {
		t.Fatal("missing inbox limit unexpectedly validated")
	}
	if err := (inboxArguments{Limit: 20}).validate(); err != nil {
		t.Fatalf("bounded inbox limit error = %v", err)
	}
	if err := (sendMessageArguments{}).validate(); err == nil {
		t.Fatal("missing artifact_ids unexpectedly validated")
	}
	if err := (sendMessageArguments{ArtifactIDs: []string{}}).validate(); err != nil {
		t.Fatalf("empty explicit artifact_ids error = %v", err)
	}
	if err := (proposeKnowledgeArguments{FreshnessPolicy: "expires_at"}).validate(); err == nil {
		t.Fatal("expires_at knowledge without fresh_until unexpectedly validated")
	}
	if err := (proposeKnowledgeArguments{FreshnessPolicy: "until_superseded", FreshUntil: "2026-08-14T00:00:00Z"}).validate(); err == nil {
		t.Fatal("until_superseded knowledge with fresh_until unexpectedly validated")
	}
	validRevision := "krev_0123456789abcdef0123456789abcdef"
	otherRevision := "krev_fedcba9876543210fedcba9876543210"
	if err := (reportContradictionArguments{LeftRevision: validRevision, RightRevision: otherRevision, Reason: "exact facts disagree", IdempotencyKey: "report"}).validate(); err != nil {
		t.Fatalf("bounded contradiction report error = %v", err)
	}
	for name, arguments := range map[string]reportContradictionArguments{
		"same revision": {LeftRevision: validRevision, RightRevision: validRevision, Reason: "same", IdempotencyKey: "report"},
		"invalid id":    {LeftRevision: "krev_short", RightRevision: otherRevision, Reason: "bad", IdempotencyKey: "report"},
		"invalid UTF-8": {LeftRevision: validRevision, RightRevision: otherRevision, Reason: string([]byte{0xff}), IdempotencyKey: "report"},
		"oversized":     {LeftRevision: validRevision, RightRevision: otherRevision, Reason: strings.Repeat("x", 2049), IdempotencyKey: "report"},
	} {
		if err := arguments.validate(); err == nil {
			t.Errorf("%s contradiction arguments unexpectedly validated", name)
		}
	}
}

func TestContradictionReportToolIsAdvertisedWithoutGovernanceFields(t *testing.T) {
	t.Parallel()
	var found *mcp.Tool
	for _, tool := range scopedMCPTools() {
		if tool.Name == toolContradictionReport {
			copy := tool
			found = &copy
			break
		}
	}
	if found == nil {
		t.Fatal("run-scoped contradiction report tool is missing")
	}
	properties, ok := found.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("contradiction report properties = %#v", found.InputSchema["properties"])
	}
	for _, field := range []string{"left_revision", "right_revision", "reason", "idempotency_key"} {
		if _, exists := properties[field]; !exists {
			t.Errorf("contradiction report schema omits %q", field)
		}
	}
	for _, forbidden := range []string{"actor", "actor_id", "actor_type", "workspace", "project", "task", "status"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("contradiction report schema exposes trusted field %q", forbidden)
		}
	}
}

func TestMessageToolDescriptionsExposeParticipantBoundCrossProjectException(t *testing.T) {
	t.Parallel()
	wanted := map[string]string{
		toolInbox:       "cross-project participant",
		toolRead:        "cross-project participant-thread",
		toolSend:        "Runs cannot create a cross-project thread or invite participants",
		toolAcknowledge: "cross-project participant-thread",
	}
	for _, tool := range scopedMCPTools() {
		fragment, exists := wanted[tool.Name]
		if !exists {
			continue
		}
		if !strings.Contains(tool.Description, fragment) {
			t.Errorf("%s description %q does not contain %q", tool.Name, tool.Description, fragment)
		}
		delete(wanted, tool.Name)
	}
	if len(wanted) != 0 {
		t.Fatalf("message tool descriptions missing for %v", wanted)
	}
}
