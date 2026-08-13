package daemon

import (
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

func TestImmutableToolAllowlistHidesLaterCapabilities(t *testing.T) {
	t.Parallel()
	legacyAllowed := []string{toolBriefing, toolStatus, toolCompletion, toolArtifact, toolBlocked, toolProgress}
	tools := allowedMCPTools(legacyAllowed)
	if len(tools) != len(legacyAllowed) {
		t.Fatalf("allowedMCPTools(legacy) count = %d, want %d", len(tools), len(legacyAllowed))
	}
	for _, tool := range tools {
		if tool.Name == toolInbox || tool.Name == toolRead || tool.Name == toolSend || tool.Name == toolAcknowledge ||
			tool.Name == toolKnowledge || tool.Name == toolContradictionReport || tool.Name == toolContextDelta || tool.Name == toolContextDeltaAck {
			t.Fatalf("legacy immutable capability exposed later tool %q", tool.Name)
		}
	}
	if !knownMCPTool(toolSend) || !knownMCPTool(toolKnowledge) || !knownMCPTool(toolKnowledgeAccept) ||
		!knownMCPTool(toolContradictionReport) || !knownMCPTool(toolContradictionConfirm) ||
		!knownMCPTool(toolContextDelta) || !knownMCPTool(toolContextDeltaAck) || knownMCPTool("crewfold_unknown_tool") {
		t.Fatal("known MCP tool classification is inconsistent")
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

func TestLegacyRunStatusReportsRebaseWithoutFetchingLiveState(t *testing.T) {
	t.Parallel()
	packet := domain.ContextPacket{Schema: domain.ContextPacketSchemaV3, ID: "ctx_0123456789abcdef0123456789abcdef"}
	status := legacyContextStatus(packet)
	if status["base_packet_id"] != packet.ID || status["base_schema"] != domain.ContextPacketSchemaV3 ||
		status["status"] != domain.ContextDeltaRebaseRequired || status["rebase_reason"] != domain.ContextRebaseUnsupportedPacket ||
		status["rebase_required"] != true {
		t.Fatalf("legacy context status = %#v", status)
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
