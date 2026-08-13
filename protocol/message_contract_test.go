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
		"schemas/local/v1/message-send.result.schema.json":                localapi.MessageSendSchema,
		"schemas/local/v1/inbox-list.result.schema.json":                  localapi.InboxListSchema,
		"schemas/local/v1/thread-show.result.schema.json":                 localapi.ThreadShowSchema,
		"schemas/local/v1/participant-thread-mutation.result.schema.json": localapi.ParticipantThreadMutationSchema,
		"schemas/local/v1/participant-thread.result.schema.json":          localapi.ParticipantThreadSchema,
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
	for _, method := range []string{
		localapi.MethodMessageSend, localapi.MethodInboxList, localapi.MethodThreadCreate,
		localapi.MethodThreadInvite, localapi.MethodThreadParticipants, localapi.MethodThreadShow,
	} {
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

func TestParticipantThreadSchemasAreStrictAndBounded(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/thread-participant.schema.json",
		"schemas/domain/v1/participant-thread.schema.json",
		"schemas/local/v1/thread-participant-input.schema.json",
		"schemas/local/v1/thread-create.params.schema.json",
		"schemas/local/v1/thread-invite.params.schema.json",
		"schemas/local/v1/thread-participants.params.schema.json",
		"schemas/local/v1/participant-thread-mutation.result.schema.json",
		"schemas/local/v1/participant-thread.result.schema.json",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if additional, ok := document["additionalProperties"].(bool); !ok || additional {
			t.Errorf("schema %q additionalProperties = %#v, want false", path, document["additionalProperties"])
		}
	}

	data, err := os.ReadFile("schemas/domain/v1/thread-participant.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var participantSchema struct {
		Properties map[string]struct {
			Pattern string `json:"pattern"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &participantSchema); err != nil {
		t.Fatal(err)
	}
	if got := participantSchema.Properties["id"].Pattern; got != "^participant_[0-9a-f]{32}$" {
		t.Errorf("participant id pattern = %q", got)
	}

	data, err = os.ReadFile("schemas/local/v1/thread-create.params.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var createSchema struct {
		Properties map[string]struct {
			MinItems int `json:"minItems"`
			MaxItems int `json:"maxItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &createSchema); err != nil {
		t.Fatal(err)
	}
	participants := createSchema.Properties["participants"]
	if participants.MinItems != 2 || participants.MaxItems != 8 {
		t.Errorf("participant bounds = %d..%d, want 2..8", participants.MinItems, participants.MaxItems)
	}
}
