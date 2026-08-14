package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestOperatorEventRowReconstructionRejectsCanonicalSchemaViolations(t *testing.T) {
	t.Parallel()
	type rawEvent struct {
		eventID, eventType, occurredAt, recordedAt string
		actorID, actorType, workspaceID            string
		entityType, entityID                       string
		correlationID, data                        string
		causationID                                *string
		sequence, schemaVersion, entityRevision    int64
	}
	valid := rawEvent{
		eventID:        "evt_" + strings.Repeat("a", 32),
		sequence:       1,
		eventType:      "task.updated",
		schemaVersion:  1,
		occurredAt:     "2026-08-14T12:00:00Z",
		recordedAt:     "2026-08-14T12:00:00Z",
		actorID:        "local-owner",
		actorType:      "human",
		workspaceID:    "ws_" + strings.Repeat("b", 32),
		entityType:     "task",
		entityID:       "task_1",
		entityRevision: 1,
		correlationID:  "request-1",
		data:           `{}`,
	}
	reconstruct := func(raw rawEvent) error {
		_, err := eventFromOperatorRow(
			raw.eventID, raw.sequence, raw.eventType, raw.schemaVersion,
			raw.occurredAt, raw.recordedAt, raw.actorID, raw.actorType,
			raw.workspaceID, raw.entityType, raw.entityID, raw.entityRevision,
			raw.correlationID, raw.causationID, raw.data,
		)
		return err
	}
	if err := reconstruct(valid); err != nil {
		t.Fatalf("valid canonical row rejected: %v", err)
	}

	empty := ""
	overlong := strings.Repeat("c", 129)
	tests := []struct {
		name   string
		mutate func(*rawEvent)
	}{
		{name: "zero sequence", mutate: func(raw *rawEvent) { raw.sequence = 0 }},
		{name: "noncanonical event id", mutate: func(raw *rawEvent) { raw.eventID = "evt_1" }},
		{name: "empty event type", mutate: func(raw *rawEvent) { raw.eventType = "" }},
		{name: "zero schema version", mutate: func(raw *rawEvent) { raw.schemaVersion = 0 }},
		{name: "invalid occurred timestamp", mutate: func(raw *rawEvent) { raw.occurredAt = "not-a-date-time" }},
		{name: "invalid recorded timestamp", mutate: func(raw *rawEvent) { raw.recordedAt = "2026-99-99T99:99:99Z" }},
		{name: "empty actor id", mutate: func(raw *rawEvent) { raw.actorID = "" }},
		{name: "unknown actor type", mutate: func(raw *rawEvent) { raw.actorType = "agent" }},
		{name: "noncanonical workspace id", mutate: func(raw *rawEvent) { raw.workspaceID = "workspace-1" }},
		{name: "empty entity type", mutate: func(raw *rawEvent) { raw.entityType = "" }},
		{name: "empty entity id", mutate: func(raw *rawEvent) { raw.entityID = "" }},
		{name: "zero entity revision", mutate: func(raw *rawEvent) { raw.entityRevision = 0 }},
		{name: "empty correlation id", mutate: func(raw *rawEvent) { raw.correlationID = "" }},
		{name: "overlong correlation id", mutate: func(raw *rawEvent) { raw.correlationID = overlong }},
		{name: "empty present causation id", mutate: func(raw *rawEvent) { raw.causationID = &empty }},
		{name: "overlong causation id", mutate: func(raw *rawEvent) { raw.causationID = &overlong }},
		{name: "invalid data", mutate: func(raw *rawEvent) { raw.data = `{` }},
		{name: "array data", mutate: func(raw *rawEvent) { raw.data = `[]` }},
		{name: "scalar data", mutate: func(raw *rawEvent) { raw.data = `"scalar"` }},
		{name: "null data", mutate: func(raw *rawEvent) { raw.data = `null` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := valid
			test.mutate(&raw)
			if err := reconstruct(raw); err == nil {
				t.Fatalf("eventFromOperatorRow accepted %s: %#v", test.name, raw)
			}
		})
	}
}

func TestOperatorEventReadsRejectOutOfContractActorType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(ctx, InitWorkspaceCommand{
		Name: "operator-event-actor", IdempotencyKey: "operator-event-actor-workspace", CorrelationID: "operator-event-actor-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspace.Workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.ExecContext(ctx, `INSERT INTO events(
event_id,type,schema_version,occurred_at,recorded_at,actor_id,actor_type,workspace_id,
entity_type,entity_id,entity_revision,correlation_id,causation_id,data_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
		"evt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "task.updated", 1, storage.nowText(), storage.nowText(),
		"agent_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "agent", workspace.Workspace.ID,
		"task", "task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, "invalid-actor-event", `{}`); err != nil {
		t.Fatalf("insert malformed event fixture: %v", err)
	}
	page, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspace.Workspace.ID, After: baseline.HighWater})
	if ErrorCode(err) != CodeStorageFailed || !reflect.DeepEqual(page, EventPage{}) {
		t.Fatalf("ListEvents(malformed actor) = %#v, %v; want zero page and %s", page, err, CodeStorageFailed)
	}
}

func TestOperatorEventReadsRejectUnknownTypesWithoutReturningCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, err := storage.InitWorkspace(ctx, InitWorkspaceCommand{
		Name: "operator-events", IdempotencyKey: "operator-events-workspace", CorrelationID: "operator-events-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: workspace.Workspace.ID, Limit: MaximumEventPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	knownBefore, err := appendEvent(ctx, tx, workspace.Workspace.ID, "task", "task_operator_event", 1,
		"task.updated", "operator-event-known-before", storage.nowText(), map[string]any{"ordinal": 1})
	if err != nil {
		t.Fatal(err)
	}
	unknownSequence, err := appendEvent(ctx, tx, workspace.Workspace.ID, "task", "task_operator_event", 1,
		"task.future_state_changed", "operator-event-unknown", storage.nowText(), map[string]any{"ordinal": 2})
	if err != nil {
		t.Fatal(err)
	}
	knownAfter, err := appendEvent(ctx, tx, workspace.Workspace.ID, "task", "task_operator_event", 1,
		"task.updated", "operator-event-known-after", storage.nowText(), map[string]any{"ordinal": 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		failed, err := storage.ListEvents(ctx, ListEventsQuery{
			WorkspaceIdentifier: workspace.Workspace.ID, After: baseline.HighWater, Limit: 1,
		})
		if ErrorCode(err) != CodeUnsupportedOperatorEvent || !reflect.DeepEqual(failed, EventPage{}) {
			t.Fatalf("forward first-page attempt %d = %#v, %v; want zero page and %s at sequence %d", attempt, failed, err, CodeUnsupportedOperatorEvent, unknownSequence)
		}
	}
	if knownBefore >= unknownSequence || unknownSequence >= knownAfter {
		t.Fatalf("fixture order = %d, %d, %d; want known/unknown/known", knownBefore, unknownSequence, knownAfter)
	}
	afterUnknown, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: workspace.Workspace.ID, After: unknownSequence, Limit: 1,
	})
	if err != nil || len(afterUnknown.Events) != 1 || afterUnknown.Events[0].Sequence != knownAfter || afterUnknown.HasMore || afterUnknown.NextCursor != "" {
		t.Fatalf("forward interval after unknown event = %#v, %v; want only sequence %d", afterUnknown, err, knownAfter)
	}

	for attempt := 0; attempt < 2; attempt++ {
		failed, err := storage.EventTimeline(ctx, EventTimelineQuery{
			WorkspaceIdentifier: workspace.Workspace.ID, EntityType: "task", EntityID: "task_operator_event",
			Limit: 1,
		})
		if ErrorCode(err) != CodeUnsupportedOperatorEvent || !reflect.DeepEqual(failed, EventPage{}) {
			t.Fatalf("timeline first-page attempt %d = %#v, %v; want zero page and %s at sequence %d", attempt, failed, err, CodeUnsupportedOperatorEvent, unknownSequence)
		}
	}
}
