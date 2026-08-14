package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	localOwnerActorID = "local-owner"
	localActorType    = "human"
	workspaceCreated  = "workspace.created"
)

var workspaceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Store) InitWorkspace(ctx context.Context, command InitWorkspaceCommand) (WorkspaceInitResult, error) {
	name := strings.TrimSpace(command.Name)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if !workspaceNamePattern.MatchString(name) {
		return WorkspaceInitResult{}, &Error{
			Code:    CodeInvalidWorkspace,
			Message: "workspace name must start with a lowercase letter and contain at most 63 lowercase letters, digits, or hyphens",
		}
	}
	if key == "" || len(key) > 128 {
		return WorkspaceInitResult{}, &Error{Code: CodeInvalidWorkspace, Message: "idempotency key must contain 1 to 128 characters"}
	}
	if correlationID == "" || len(correlationID) > 128 {
		return WorkspaceInitResult{}, &Error{Code: CodeInvalidWorkspace, Message: "correlation id must contain 1 to 128 characters"}
	}

	requestHash := hashWorkspaceInit(name)
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("begin workspace initialization", err)
	}
	defer transaction.Rollback()

	var replay WorkspaceInitResult
	if found, err := lookupIdempotency(ctx, transaction, key, "workspace.init", requestHash, &replay); err != nil {
		return WorkspaceInitResult{}, err
	} else if found {
		return replay, nil
	}

	var existingID string
	err = transaction.QueryRowContext(ctx, "SELECT id FROM workspaces WHERE name = ?", name).Scan(&existingID)
	if err == nil {
		return WorkspaceInitResult{}, &Error{
			Code:    CodeWorkspaceExists,
			Message: fmt.Sprintf("workspace %q already exists as %s", name, existingID),
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkspaceInitResult{}, storageFailure("check workspace name", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	workspaceID, err := randomID("ws_")
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("generate workspace id", err)
	}
	eventID, err := randomID("evt_")
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("generate event id", err)
	}
	workspace := Workspace{
		ID:        workspaceID,
		Name:      name,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: localOwnerActorID,
		UpdatedBy: localOwnerActorID,
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workspaces(id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspace.ID,
		workspace.Name,
		workspace.Revision,
		workspace.CreatedAt,
		workspace.UpdatedAt,
		workspace.CreatedBy,
		workspace.UpdatedBy,
	); err != nil {
		return WorkspaceInitResult{}, storageFailure("insert workspace projection", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return WorkspaceInitResult{}, err
	}

	eventData, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("encode workspace event", err)
	}
	inserted, err := transaction.ExecContext(ctx, `
INSERT INTO events(
    event_id, type, schema_version, occurred_at, recorded_at,
    actor_id, actor_type, workspace_id, entity_type, entity_id,
    entity_revision, correlation_id, causation_id, data_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		eventID,
		workspaceCreated,
		1,
		now,
		now,
		localOwnerActorID,
		localActorType,
		workspace.ID,
		"workspace",
		workspace.ID,
		workspace.Revision,
		correlationID,
		string(eventData),
	)
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("append workspace event", err)
	}
	sequence, err := inserted.LastInsertId()
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("read workspace event sequence", err)
	}
	result := WorkspaceInitResult{Workspace: workspace, EventID: eventID, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return WorkspaceInitResult{}, err
	}

	response, err := json.Marshal(result)
	if err != nil {
		return WorkspaceInitResult{}, storageFailure("encode idempotent workspace response", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO idempotency_keys(key, command, request_hash, response_json, created_at)
VALUES (?, ?, ?, ?, ?)`, key, "workspace.init", requestHash, string(response), now); err != nil {
		return WorkspaceInitResult{}, storageFailure("record workspace idempotency result", err)
	}
	if err := transaction.Commit(); err != nil {
		return WorkspaceInitResult{}, storageFailure("commit workspace initialization", err)
	}
	return result, nil
}

func (s *Store) Workspace(ctx context.Context, identifier string) (Workspace, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || len(identifier) > 128 {
		return Workspace{}, &Error{Code: CodeInvalidWorkspace, Message: "workspace identifier must contain 1 to 128 characters"}
	}

	workspace, err := queryWorkspace(ctx, s.db, "SELECT id, name, revision, created_at, updated_at, created_by, updated_by FROM workspaces WHERE id = ?", identifier)
	if err == nil {
		return workspace, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, storageFailure("query workspace by id", err)
	}
	workspace, err = queryWorkspace(ctx, s.db, "SELECT id, name, revision, created_at, updated_at, created_by, updated_by FROM workspaces WHERE name = ?", identifier)
	if err == nil {
		return workspace, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, &Error{Code: CodeWorkspaceNotFound, Message: fmt.Sprintf("workspace %q was not found", identifier)}
	}
	return Workspace{}, storageFailure("query workspace by name", err)
}

func lookupIdempotency(ctx context.Context, transaction *sql.Tx, key, command, requestHash string, target any) (bool, error) {
	var storedCommand, storedHash, response string
	err := transaction.QueryRowContext(ctx,
		"SELECT command, request_hash, response_json FROM idempotency_keys WHERE key = ?",
		key,
	).Scan(&storedCommand, &storedHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storageFailure("read idempotency record", err)
	}
	if storedCommand != command || storedHash != requestHash {
		return false, &Error{
			Code:    CodeIdempotencyConflict,
			Message: "idempotency key was already used for a different command payload",
		}
	}
	if err := json.Unmarshal([]byte(response), target); err != nil {
		return false, storageFailure("decode idempotent response", err)
	}
	return true, nil
}

// lookupIdempotencyBeforeEffects gives an exact replay precedence over any
// time-driven reconciliation that a fresh mutation performs. The mutation
// still repeats the lookup in its write transaction after reconciliation to
// close races with another caller using the same key.
func (s *Store) lookupIdempotencyBeforeEffects(ctx context.Context, key, command, requestHash string, target any) (bool, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, storageFailure("begin idempotency replay lookup", err)
	}
	defer transaction.Rollback()
	return lookupIdempotency(ctx, transaction, key, command, requestHash, target)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func queryWorkspace(ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, argument any) (Workspace, error) {
	var workspace Workspace
	err := scanWorkspace(database.QueryRowContext(ctx, query, argument), &workspace)
	return workspace, err
}

func scanWorkspace(row rowScanner, workspace *Workspace) error {
	return row.Scan(
		&workspace.ID,
		&workspace.Name,
		&workspace.Revision,
		&workspace.CreatedAt,
		&workspace.UpdatedAt,
		&workspace.CreatedBy,
		&workspace.UpdatedBy,
	)
}

func hashWorkspaceInit(name string) string {
	digest := sha256.Sum256([]byte("workspace.init\n" + name))
	return hex.EncodeToString(digest[:])
}

func randomID(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

func (s *Store) runMutationHook(stage string) error {
	if s.mutationHook == nil {
		return nil
	}
	if err := s.mutationHook(stage); err != nil {
		return storageFailure("mutation interrupted at "+stage, err)
	}
	return nil
}
