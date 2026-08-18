package execution

import (
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
)

func TestM22ReadableCodexTurnsKeepsProviderWordsAndSeparatesCommands(t *testing.T) {
	thread := CodexThread{Turns: []CodexTurn{{ID: "turn-1", Status: "completed", Items: []json.RawMessage{
		json.RawMessage(`{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"owner words"}]}`),
		json.RawMessage(`{"id":"agent-1","type":"agentMessage","text":"provider words"}`),
		json.RawMessage(`{"id":"command-1","type":"commandExecution","command":"go test ./...","aggregatedOutput":"ok","status":"completed"}`),
	}}}}
	turns := ReadableCodexTurns(thread)
	if len(turns) != 1 || len(turns[0].Items) != 3 || turns[0].Items[0].Text != "owner words" ||
		turns[0].Items[1].Text != "provider words" || turns[0].Items[2].Command != "go test ./..." || turns[0].Items[2].Text != "ok" {
		t.Fatalf("ReadableCodexTurns() = %#v", turns)
	}
}

func TestM22ReadableCodexItemCoversObservableWorkWithoutPrivateReasoning(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   domain.DomainAgentSessionItem
		wantOK bool
	}{
		{name: "crewfold tool", raw: `{"id":"tool-1","type":"dynamicToolCall","tool":"crewfold_get_context","status":"inProgress"}`, want: domain.DomainAgentSessionItem{ID: "tool-1", Type: "dynamicToolCall", Command: "crewfold_get_context", Status: "inProgress"}, wantOK: true},
		{name: "mcp tool failure", raw: `{"id":"mcp-1","type":"mcpToolCall","server":"world","tool":"inspect","status":"failed","error":{"message":"world unavailable"}}`, want: domain.DomainAgentSessionItem{ID: "mcp-1", Type: "mcpToolCall", Command: "world.inspect", Text: "world unavailable", Status: "failed"}, wantOK: true},
		{name: "changed paths", raw: `{"id":"patch-1","type":"fileChange","status":"completed","changes":[{"path":"src/main.ts","kind":{"type":"update"}},{"path":"src/new.ts","kind":{"type":"add"}}]}`, want: domain.DomainAgentSessionItem{ID: "patch-1", Type: "fileChange", Text: "update src/main.ts\nadd src/new.ts", Status: "completed"}, wantOK: true},
		{name: "search", raw: `{"id":"search-1","type":"webSearch","query":"exact documentation"}`, want: domain.DomainAgentSessionItem{ID: "search-1", Type: "webSearch", Command: "exact documentation"}, wantOK: true},
		{name: "private reasoning", raw: `{"id":"reasoning-1","type":"reasoning","summary":["private summary"],"content":["private chain"]}`, want: domain.DomainAgentSessionItem{}, wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ReadableCodexItem(json.RawMessage(test.raw))
			if ok != test.wantOK || got != test.want {
				t.Fatalf("ReadableCodexItem() = %#v, %t, want %#v, %t", got, ok, test.want, test.wantOK)
			}
		})
	}
}
