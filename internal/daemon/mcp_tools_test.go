package daemon

import (
	"strings"
	"testing"

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
			tool.Name == toolKnowledge || tool.Name == toolContradictionReport {
			t.Fatalf("legacy immutable capability exposed later tool %q", tool.Name)
		}
	}
	if !knownMCPTool(toolSend) || !knownMCPTool(toolKnowledge) || !knownMCPTool(toolKnowledgeAccept) ||
		!knownMCPTool(toolContradictionReport) || !knownMCPTool(toolContradictionConfirm) || knownMCPTool("crewfold_unknown_tool") {
		t.Fatal("known MCP tool classification is inconsistent")
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
