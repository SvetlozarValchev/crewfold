package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestWorkspaceResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()

	for path, expectedID := range map[string]string{
		"schemas/local/v1/database-status.result.schema.json": localapi.DatabaseStatusSchema,
		"schemas/local/v1/workspace-init.result.schema.json":  localapi.WorkspaceInitSchema,
		"schemas/local/v1/workspace-show.result.schema.json":  localapi.WorkspaceShowSchema,
		"schemas/local/v1/events-list.result.schema.json":     localapi.EventsListSchema,
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

func TestEventEnvelopeKeepsActorAndEntityStructured(t *testing.T) {
	t.Parallel()

	event := domain.Event{
		EventID:       "evt_00000000000000000000000000000000",
		Sequence:      1,
		Type:          "workspace.created",
		SchemaVersion: 1,
		OccurredAt:    "2026-08-12T00:00:00Z",
		RecordedAt:    "2026-08-12T00:00:00Z",
		Actor:         domain.EventActor{ActorID: "local-owner", ActorType: "human"},
		WorkspaceID:   "ws_00000000000000000000000000000000",
		Entity: domain.EventEntity{
			Type:     "workspace",
			ID:       "ws_00000000000000000000000000000000",
			Revision: 1,
		},
		CorrelationID: "req-1",
		Data:          json.RawMessage(`{"name":"personal"}`),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(event) error = %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("json.Unmarshal(event) error = %v", err)
	}
	if _, ok := envelope["actor"].(map[string]any); !ok {
		t.Fatalf("event actor = %T, want object", envelope["actor"])
	}
	if _, ok := envelope["entity"].(map[string]any); !ok {
		t.Fatalf("event entity = %T, want object", envelope["entity"])
	}
	for _, leakedColumn := range []string{"actor_id", "actor_type", "entity_type", "entity_id", "entity_revision"} {
		if _, exists := envelope[leakedColumn]; exists {
			t.Errorf("event exposes flattened storage column %q", leakedColumn)
		}
	}
}
