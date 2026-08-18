package protocol_test

import (
	"encoding/json"
	"os"
	"reflect"
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

func TestMessageSenderContractIncludesOnlyExactOwnerRunDurableAgentAndCheckSubsystemShapes(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/domain/v1/message.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		AllOf []struct {
			If struct {
				Properties map[string]struct {
					Const string `json:"const"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Const   string `json:"const"`
					Pattern string `json:"pattern"`
				} `json:"properties"`
				Not struct {
					AnyOf []struct {
						Required []string `json:"required"`
					} `json:"anyOf"`
				} `json:"not"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if got, want := document.Properties["sender_type"].Enum, []string{"owner", "agent_run", "durable_agent", "subsystem"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("message sender_type = %v, want exact closed set %v", got, want)
	}
	conditions := make(map[string]struct {
		id        string
		required  []string
		forbidden []string
		pattern   string
	}, len(document.AllOf))
	for _, conditional := range document.AllOf {
		forbidden := make([]string, 0, len(conditional.Then.Not.AnyOf))
		for _, item := range conditional.Then.Not.AnyOf {
			forbidden = append(forbidden, item.Required...)
		}
		conditions[conditional.If.Properties["sender_type"].Const] = struct {
			id        string
			required  []string
			forbidden []string
			pattern   string
		}{
			id: conditional.Then.Properties["sender_id"].Const, required: conditional.Then.Required,
			forbidden: forbidden, pattern: conditional.Then.Properties["sender_id"].Pattern,
		}
	}
	privateFields := []string{"sender_agent_id", "sender_agent_name", "sender_run_id"}
	if got := conditions["owner"]; got.id != "local-owner" || !reflect.DeepEqual(got.forbidden, privateFields) {
		t.Fatalf("owner sender shape = %#v", got)
	}
	if got := conditions["subsystem"]; got.id != "crewfold-check-worker" || !reflect.DeepEqual(got.forbidden, privateFields) {
		t.Fatalf("check subsystem sender shape = %#v", got)
	}
	if got := conditions["agent_run"]; !reflect.DeepEqual(got.required, privateFields) || got.pattern != "^run_[0-9a-f]{32}$" {
		t.Fatalf("agent-run sender shape = %#v", got)
	}
	if got := conditions["durable_agent"]; !reflect.DeepEqual(got.required, []string{"project_id", "sender_agent_id", "sender_agent_name"}) ||
		!reflect.DeepEqual(got.forbidden, []string{"task_id", "sender_run_id"}) || got.pattern != "^agent_[0-9a-f]{32}$" {
		t.Fatalf("durable-agent sender shape = %#v", got)
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
