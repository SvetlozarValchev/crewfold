package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestMessageResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expectedID := range map[string]string{
		"schemas/local/v1/message-send.result.schema.json": localapi.MessageSendSchema,
		"schemas/local/v1/inbox-list.result.schema.json":   localapi.InboxListSchema,
		"schemas/local/v1/thread-show.result.schema.json":  localapi.ThreadShowSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if header.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, header.ID, expectedID)
		}
	}
}

func TestMessageMethodsRemainProviderNeutral(t *testing.T) {
	t.Parallel()
	for _, method := range []string{localapi.MethodMessageSend, localapi.MethodInboxList, localapi.MethodThreadShow} {
		if method == "" {
			t.Fatal("message method name is empty")
		}
		for _, forbidden := range []string{"codex", "claude", "herdr", "worktree"} {
			if contains(method, forbidden) {
				t.Errorf("method %q embeds provider/runtime/source-layout term %q", method, forbidden)
			}
		}
	}
}
