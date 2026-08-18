package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestM22DomainSessionHostProjectsExactLiveCodexActivity(t *testing.T) {
	host := &domainSessionHost{activity: make(map[string]*domainSessionLiveActivity)}
	record := func(method, params string) {
		host.recordNotification(execution.CodexAppServerNotification{Method: method, Params: json.RawMessage(params)})
	}
	record("turn/started", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress","items":[]}}`)
	record("turn/plan/updated", `{"threadId":"thread-1","turnId":"turn-1","explanation":"Working in three bounded steps.","plan":[{"step":"Read project docs","status":"completed"},{"step":"Inspect implementation","status":"inProgress"},{"step":"Report distribution","status":"pending"}]}`)
	record("item/started", `{"threadId":"thread-1","turnId":"turn-1","startedAtMs":1,"item":{"id":"command-1","type":"commandExecution","command":"find . -name '*.md'","commandActions":[],"cwd":"/work","status":"inProgress"}}`)
	record("item/commandExecution/outputDelta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"command-1","delta":"./README.md\n"}`)
	record("item/completed", `{"threadId":"thread-1","turnId":"turn-1","completedAtMs":2,"item":{"id":"command-1","type":"commandExecution","command":"find . -name '*.md'","commandActions":[],"cwd":"/work","aggregatedOutput":"./README.md\n./docs/plan.md\n","status":"completed"}}`)
	record("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"I found the project documentation. "}`)
	record("item/completed", `{"threadId":"thread-1","turnId":"turn-1","completedAtMs":3,"item":{"id":"message-1","type":"agentMessage","text":"I found the project documentation. I am reading it now."}}`)
	record("item/fileChange/patchUpdated", `{"threadId":"thread-1","turnId":"turn-1","itemId":"change-1","changes":[{"path":"docs/plan.md","kind":{"type":"update"},"diff":"@@ -1 +1 @@\n-old\n+new"}]}`)
	record("item/completed", `{"threadId":"thread-1","turnId":"turn-1","completedAtMs":4,"item":{"id":"change-1","type":"fileChange","status":"completed","changes":[{"path":"docs/plan.md","kind":{"type":"update"},"diff":"@@ -1 +1 @@\n-old\n+new"}]}}`)
	record("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`)

	live := host.liveActivity("thread-1")
	if len(live) != 1 || live[0].Status != "completed" || len(live[0].Items) != 4 {
		t.Fatalf("live activity = %#v", live)
	}
	if live[0].Items[0].Type != "plan" || live[0].Items[0].Text != "Working in three bounded steps.\n[completed] Read project docs\n[inProgress] Inspect implementation\n[pending] Report distribution" {
		t.Fatalf("plan activity = %#v", live[0].Items[0])
	}
	if live[0].Items[1].Command != "find . -name '*.md'" || live[0].Items[1].Text != "./README.md\n./docs/plan.md\n" || live[0].Items[1].Status != "completed" {
		t.Fatalf("command activity = %#v", live[0].Items[1])
	}
	if live[0].Items[2].Type != "agentMessage" || live[0].Items[2].Text != "I found the project documentation. I am reading it now." {
		t.Fatalf("message activity = %#v", live[0].Items[2])
	}
	if live[0].Items[3].Type != "fileChange" || len(live[0].Items[3].Changes) != 1 ||
		live[0].Items[3].Changes[0].Path != "docs/plan.md" || live[0].Items[3].Changes[0].Diff != "@@ -1 +1 @@\n-old\n+new" {
		t.Fatalf("file activity = %#v", live[0].Items[3])
	}

	merged := mergeDomainSessionTurns([]domain.DomainAgentSessionTurn{{
		ID: "turn-1", Status: "inProgress", Items: []domain.DomainAgentSessionItem{{ID: "owner-1", Type: "userMessage", Text: "read the docs"}},
	}}, live)
	if len(merged) != 1 || merged[0].Status != "completed" || len(merged[0].Items) != 5 || merged[0].Items[0].Text != "read the docs" {
		t.Fatalf("merged activity = %#v", merged)
	}
}

func TestM22DomainSessionHostShowsProviderErrorsWithoutInventingState(t *testing.T) {
	host := &domainSessionHost{activity: make(map[string]*domainSessionLiveActivity)}
	host.recordNotification(execution.CodexAppServerNotification{
		Method: "error",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"provider connection failed","additionalDetails":"retrying exact turn"}}`),
	})
	live := host.liveActivity("thread-1")
	if len(live) != 1 || len(live[0].Items) != 1 || live[0].Items[0].Type != "error" ||
		live[0].Items[0].Status != "retrying" || live[0].Items[0].Text != "provider connection failed\nretrying exact turn" {
		t.Fatalf("error activity = %#v", live)
	}
}

func TestM22DomainSessionMergeDeduplicatesPersistedAndLiveMessageIDs(t *testing.T) {
	persisted := []domain.DomainAgentSessionTurn{{
		ID: "turn-1", Status: "completed", Items: []domain.DomainAgentSessionItem{
			{ID: "item-7", Type: "userMessage", Text: "inspect Crewfold state"},
			{ID: "item-8", Type: "agentMessage", Text: "No durable children exist."},
		},
	}}
	live := []domain.DomainAgentSessionTurn{{
		ID: "turn-1", Status: "completed", Items: []domain.DomainAgentSessionItem{
			{ID: "msg-user-provider", Type: "userMessage", Text: "inspect Crewfold state", Status: "completed"},
			{ID: "tool-context", Type: "dynamicToolCall", Command: domainToolContext, Status: "completed"},
			{ID: "msg-agent-provider", Type: "agentMessage", Text: "No durable children exist."},
		},
	}}
	merged := mergeDomainSessionTurns(persisted, live)
	if len(merged) != 1 || len(merged[0].Items) != 3 {
		t.Fatalf("merged turns = %#v", merged)
	}
	if merged[0].Items[0].ID != "msg-user-provider" || merged[0].Items[0].Status != "completed" ||
		merged[0].Items[1].ID != "tool-context" || merged[0].Items[2].ID != "msg-agent-provider" {
		t.Fatalf("merged items = %#v", merged[0].Items)
	}
}

func TestM22DomainSessionMergeSerializesAnEmptyLiveTurnAsAnArray(t *testing.T) {
	turns := mergeDomainSessionTurns(nil, []domain.DomainAgentSessionTurn{{ID: "turn-live", Status: "inProgress"}})
	if len(turns) != 1 || turns[0].Items == nil || len(turns[0].Items) != 0 {
		t.Fatalf("merged turns = %#v", turns)
	}
	encoded, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"items":null`) || !strings.Contains(string(encoded), `"items":[]`) {
		t.Fatalf("encoded turns = %s", encoded)
	}
}
