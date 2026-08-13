package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCurrentContextPacketMarshalsOneCanonicalShape(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(ContextPacket{
		Schema: ContextPacketSchema, Inbox: InboxSummary{Items: []InboxSummaryItem{}},
		RequestedKnowledgeRevisionIDs: []string{}, AcceptedKnowledge: []KnowledgeRevision{},
		Budget: ContextBudget{
			Total:         ContextBudgetUsage{LimitBytes: 32768, UsedBytes: 1024, RemainingBytes: 31744},
			Knowledge:     ContextBudgetUsage{LimitBytes: 12288, RemainingBytes: 12288},
			Collaboration: ContextBudgetUsage{LimitBytes: 8192, RemainingBytes: 8192},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal(current packet) error = %v", err)
	}
	for _, field := range [][]byte{
		[]byte(`"schema":"` + ContextPacketSchema + `"`), []byte(`"dependencies":[]`),
		[]byte(`"dependents":[]`), []byte(`"dependent_task_count":0`),
		[]byte(`"participant_threads":[]`), []byte(`"requested_knowledge_revision_ids":[]`),
		[]byte(`"accepted_knowledge":[]`), []byte(`"live_context"`),
		[]byte(`"as_of_event_sequence":0`), []byte(`"assignment_id":""`), []byte(`"collaboration"`),
	} {
		if !bytes.Contains(encoded, field) {
			t.Errorf("current packet omitted required field %s: %s", field, encoded)
		}
	}
	for _, authority := range [][]byte{[]byte(`"management_grant"`), []byte(`"check_watch_grant"`)} {
		if bytes.Contains(encoded, authority) {
			t.Errorf("ordinary current packet invented delegated authority %s: %s", authority, encoded)
		}
	}
}

func TestCurrentContextPacketAuthorityFamiliesAreOptionalAndExclusive(t *testing.T) {
	t.Parallel()
	checkGrant := &ContextCheckWatchGrant{
		Schema: ContextCheckWatchGrantSchema, GrantID: "checkgrant_0123456789abcdef0123456789abcdef", GrantRevision: 1,
		WorkspaceID: "ws_0123456789abcdef0123456789abcdef", ProjectID: "prj_0123456789abcdef0123456789abcdef",
		WatcherAgentID: "agent_0123456789abcdef0123456789abcdef", WatcherAgentRevision: 1,
		Operations: []string{CheckWatchOperationInspect}, Definitions: []CheckWatchGrantDefinition{},
		MaxPending: 1, MaxInFlight: 1, ContentSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	encoded, err := json.Marshal(ContextPacket{Schema: ContextPacketSchema, CheckWatchGrant: checkGrant})
	if err != nil {
		t.Fatalf("json.Marshal(check-watch packet) error = %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"check_watch_grant"`)) || bytes.Contains(encoded, []byte(`"management_grant"`)) {
		t.Fatalf("check-watch authority shape = %s", encoded)
	}
	managerEncoded, err := json.Marshal(ContextPacket{Schema: ContextPacketSchema, ManagementGrant: &ContextManagerGrant{Schema: ContextManagerGrantSchema}})
	if err != nil {
		t.Fatalf("json.Marshal(manager packet) error = %v", err)
	}
	if !bytes.Contains(managerEncoded, []byte(`"management_grant"`)) || bytes.Contains(managerEncoded, []byte(`"check_watch_grant"`)) {
		t.Fatalf("manager authority shape = %s", managerEncoded)
	}
	if _, err := json.Marshal(ContextPacket{Schema: ContextPacketSchema, ManagementGrant: &ContextManagerGrant{}, CheckWatchGrant: checkGrant}); err == nil {
		t.Fatal("packet with both delegated authority families unexpectedly marshaled")
	}
}

func TestContextPacketRejectsNoncurrentSchema(t *testing.T) {
	t.Parallel()
	if _, err := json.Marshal(ContextPacket{Schema: "urn:example:invalid-context-packet"}); err == nil {
		t.Fatal("noncurrent context schema unexpectedly marshaled")
	}
}
