package store

import "crewfold/internal/domain"

const (
	LatestSchemaVersion = 1

	MutationAfterProjection = "after_projection"
	MutationAfterEvent      = "after_event"
)

type Workspace = domain.Workspace

type Event = domain.Event

type WorkspaceInitResult struct {
	Workspace     Workspace `json:"workspace"`
	EventID       string    `json:"event_id"`
	EventSequence int64     `json:"event_sequence"`
}

type DatabaseHealth struct {
	Status              string `json:"status"`
	SchemaVersion       int    `json:"schema_version"`
	LatestSchemaVersion int    `json:"latest_schema_version"`
	JournalMode         string `json:"journal_mode"`
	ForeignKeys         bool   `json:"foreign_keys"`
	IntegrityCheck      string `json:"integrity_check"`
}

type InitWorkspaceCommand struct {
	Name           string
	IdempotencyKey string
	CorrelationID  string
}

type Options struct {
	// MutationHook is a deterministic fault barrier used by component tests. The
	// production daemon leaves it nil.
	MutationHook func(stage string) error
}
