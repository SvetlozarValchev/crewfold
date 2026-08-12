package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLegacyContextPacketOmitsVersionTwoInbox(t *testing.T) {
	t.Parallel()
	legacy, err := json.Marshal(ContextPacket{Schema: ContextPacketSchemaV1})
	if err != nil {
		t.Fatalf("json.Marshal(legacy packet) error = %v", err)
	}
	if bytes.Contains(legacy, []byte(`"inbox"`)) {
		t.Fatalf("legacy packet unexpectedly gained version-two inbox: %s", legacy)
	}
	current, err := json.Marshal(ContextPacket{Schema: ContextPacketSchema, Inbox: InboxSummary{Items: []InboxSummaryItem{}}})
	if err != nil {
		t.Fatalf("json.Marshal(current packet) error = %v", err)
	}
	if !bytes.Contains(current, []byte(`"inbox":{"unseen_count":0,"items":[]}`)) {
		t.Fatalf("current packet omitted bounded inbox: %s", current)
	}
}
