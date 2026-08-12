package daemon

import "testing"

func TestImmutableToolAllowlistHidesLaterCapabilities(t *testing.T) {
	t.Parallel()
	legacyAllowed := []string{toolBriefing, toolStatus, toolCompletion, toolArtifact, toolBlocked, toolProgress}
	tools := allowedMCPTools(legacyAllowed)
	if len(tools) != len(legacyAllowed) {
		t.Fatalf("allowedMCPTools(legacy) count = %d, want %d", len(tools), len(legacyAllowed))
	}
	for _, tool := range tools {
		if tool.Name == toolInbox || tool.Name == toolRead || tool.Name == toolSend || tool.Name == toolAcknowledge || tool.Name == toolKnowledge {
			t.Fatalf("legacy immutable capability exposed later tool %q", tool.Name)
		}
	}
	if !knownMCPTool(toolSend) || !knownMCPTool(toolKnowledge) || !knownMCPTool(toolKnowledgeAccept) || knownMCPTool("crewfold_unknown_tool") {
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
}
