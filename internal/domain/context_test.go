package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLegacyContextPacketsOmitLaterSections(t *testing.T) {
	t.Parallel()
	legacy, err := json.Marshal(ContextPacket{Schema: ContextPacketSchemaV1})
	if err != nil {
		t.Fatalf("json.Marshal(legacy packet) error = %v", err)
	}
	if bytes.Contains(legacy, []byte(`"inbox"`)) {
		t.Fatalf("legacy packet unexpectedly gained version-two inbox: %s", legacy)
	}
	versionTwo, err := json.Marshal(ContextPacket{Schema: ContextPacketSchemaV2, Inbox: InboxSummary{Items: []InboxSummaryItem{}}})
	if err != nil {
		t.Fatalf("json.Marshal(version-two packet) error = %v", err)
	}
	if !bytes.Contains(versionTwo, []byte(`"inbox":{"unseen_count":0,"items":[]}`)) || bytes.Contains(versionTwo, []byte(`"requested_knowledge_revision_ids"`)) || bytes.Contains(versionTwo, []byte(`"accepted_knowledge"`)) || bytes.Contains(versionTwo, []byte(`"budget":{"total"`)) {
		t.Fatalf("version-two packet compatibility changed: %s", versionTwo)
	}
	current, err := json.Marshal(ContextPacket{
		Schema: ContextPacketSchema, Inbox: InboxSummary{Items: []InboxSummaryItem{}},
		RequestedKnowledgeRevisionIDs: []string{}, AcceptedKnowledge: []KnowledgeRevision{},
		Budget: ContextBudget{
			Total:     ContextBudgetUsage{LimitBytes: 32768, UsedBytes: 1024, RemainingBytes: 31744},
			Knowledge: ContextBudgetUsage{LimitBytes: 12288, UsedBytes: 0, RemainingBytes: 12288},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(current packet) error = %v", err)
	}
	if !bytes.Contains(current, []byte(`"requested_knowledge_revision_ids":[]`)) || !bytes.Contains(current, []byte(`"accepted_knowledge":[]`)) || !bytes.Contains(current, []byte(`"budget":{"total"`)) {
		t.Fatalf("current packet omitted version-three sections: %s", current)
	}
}
