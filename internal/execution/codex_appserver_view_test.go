package execution

import (
	"encoding/json"
	"testing"
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
