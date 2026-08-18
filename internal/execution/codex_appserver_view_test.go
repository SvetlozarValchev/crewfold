package execution

import (
	"encoding/json"
	"reflect"
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
		{name: "owner message", raw: `{"id":"owner-1","type":"userMessage","clientId":"owner-turn","content":[{"type":"text","text":"continue"}]}`, want: domain.DomainAgentSessionItem{ID: "owner-1", Type: "userMessage", Origin: "owner", Text: "continue"}, wantOK: true},
		{name: "durable delivery", raw: `{"id":"wake-1","type":"userMessage","clientId":"crewfold:wake:msg_0123456789abcdef0123456789abcdef","content":[{"type":"text","text":"Crewfold delivered a durable message"}]}`, want: domain.DomainAgentSessionItem{ID: "wake-1", Type: "userMessage", Origin: "crewfold_delivery", Text: "Crewfold delivered a durable message"}, wantOK: true},
		{name: "command observation", raw: `{"id":"command-1","type":"commandExecution","command":"sed -n '1,20p' README.md","cwd":"/repo","processId":"pty-1","status":"completed","exitCode":0,"durationMs":1250,"aggregatedOutput":"hello","commandActions":[{"type":"read","command":"sed","name":"README.md","path":"/repo/README.md"}]}`, want: domain.DomainAgentSessionItem{ID: "command-1", Type: "commandExecution", Text: "hello", Command: "sed -n '1,20p' README.md", Status: "completed", CWD: "/repo", ProcessID: "pty-1", ExitCode: intPointer(0), DurationMillis: 1250, CommandActions: []domain.DomainAgentSessionCommandAction{{Type: "read", Command: "sed", Name: "README.md", Path: "/repo/README.md"}}}, wantOK: true},
		{name: "mcp tool failure", raw: `{"id":"mcp-1","type":"mcpToolCall","server":"world","tool":"inspect","status":"failed","durationMs":44,"error":{"message":"world unavailable"}}`, want: domain.DomainAgentSessionItem{ID: "mcp-1", Type: "mcpToolCall", Command: "world.inspect", Text: "world unavailable", Status: "failed", DurationMillis: 44}, wantOK: true},
		{name: "changed paths and diffs", raw: `{"id":"patch-1","type":"fileChange","status":"completed","changes":[{"path":"src/main.ts","kind":{"type":"update"},"diff":"@@ -1 +1 @@\n-old\n+new"},{"path":"src/new.ts","kind":{"type":"add"},"diff":"export {}"}]}`, want: domain.DomainAgentSessionItem{ID: "patch-1", Type: "fileChange", Status: "completed", Changes: []domain.DomainAgentSessionFileChange{{Path: "src/main.ts", Kind: "update", Diff: "@@ -1 +1 @@\n-old\n+new"}, {Path: "src/new.ts", Kind: "add", Diff: "export {}"}}}, wantOK: true},
		{name: "search", raw: `{"id":"search-1","type":"webSearch","query":"exact documentation"}`, want: domain.DomainAgentSessionItem{ID: "search-1", Type: "webSearch", Command: "exact documentation"}, wantOK: true},
		{name: "private reasoning", raw: `{"id":"reasoning-1","type":"reasoning","summary":["private summary"],"content":["private chain"]}`, want: domain.DomainAgentSessionItem{}, wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ReadableCodexItem(json.RawMessage(test.raw))
			if ok != test.wantOK || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ReadableCodexItem() = %#v, %t, want %#v, %t", got, ok, test.want, test.wantOK)
			}
		})
	}
}

func intPointer(value int) *int { return &value }
