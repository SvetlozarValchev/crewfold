// Package domain defines provider-, transport-, and storage-neutral Crewfold records.
package domain

import "encoding/json"

type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
}

type Event struct {
	EventID       string          `json:"event_id"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	RecordedAt    string          `json:"recorded_at"`
	Actor         EventActor      `json:"actor"`
	WorkspaceID   string          `json:"workspace_id"`
	Entity        EventEntity     `json:"entity"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	Data          json.RawMessage `json:"data"`
}

type EventActor struct {
	ActorID   string `json:"actor_id"`
	ActorType string `json:"actor_type"`
}

type EventEntity struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
