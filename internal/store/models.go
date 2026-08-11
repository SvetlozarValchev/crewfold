package store

import "crewfold/internal/domain"

const (
	LatestSchemaVersion = 2

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

type RegisterProjectCommand struct {
	WorkspaceIdentifier string
	Name                string
	WriteMode           string
	IdempotencyKey      string
	CorrelationID       string
	Observation         domain.CheckoutObservation
}

type AddCheckoutCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	WriteMode           string
	IdempotencyKey      string
	CorrelationID       string
	Observation         domain.CheckoutObservation
}

type ProjectRegistrationResult struct {
	Project       domain.Project    `json:"project"`
	Repository    domain.Repository `json:"repository"`
	Checkout      domain.Checkout   `json:"checkout"`
	EventSequence int64             `json:"event_sequence"`
}

type CheckoutRegistrationResult struct {
	Repository        domain.Repository `json:"repository"`
	Checkout          domain.Checkout   `json:"checkout"`
	RepositoryCreated bool              `json:"repository_created"`
	EventSequence     int64             `json:"event_sequence"`
}

type ProjectInspection struct {
	Project      domain.Project      `json:"project"`
	Repositories []domain.Repository `json:"repositories"`
	Checkouts    []domain.Checkout   `json:"checkouts"`
}

type Options struct {
	// MutationHook is a deterministic fault barrier used by component tests. The
	// production daemon leaves it nil.
	MutationHook func(stage string) error
}
