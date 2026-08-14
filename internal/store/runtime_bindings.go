package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"crewfold/internal/domain"
)

const maximumRuntimeHandleBytes = 8 * 1024

func (s *Store) validateRuntimeNodeIdentity() error {
	if !validRuntimeNodeIdentity(s.runtimeNodeID, s.runtimeNodeFingerprint) {
		return &Error{Code: CodeInvalidRun, Message: "runtime binding requires the daemon's canonical node identity and fingerprint"}
	}
	return nil
}

func validRuntimeNodeIdentity(nodeID, nodeFingerprint string) bool {
	return len(nodeID) == 32 && strings.Trim(nodeID, "0123456789abcdef") == "" &&
		len(nodeFingerprint) == 64 && strings.Trim(nodeFingerprint, "0123456789abcdef") == ""
}

func validRuntimeHandle(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]byte(value)) <= maximumRuntimeHandleBytes && !strings.ContainsRune(value, '\x00')
}

func (s *Store) insertRunRuntimeBinding(ctx context.Context, tx *sql.Tx, run *domain.Run, runtimeHandle, providerHandle, now string) error {
	if err := s.validateRuntimeNodeIdentity(); err != nil {
		return err
	}
	runtimeHandle, providerHandle = strings.TrimSpace(runtimeHandle), strings.TrimSpace(providerHandle)
	if !validRuntimeHandle(runtimeHandle) || providerHandle != "" && !validRuntimeHandle(providerHandle) {
		return &Error{Code: CodeInvalidRun, Message: "runtime binding contains an invalid handle"}
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO run_runtime_bindings(
 run_id,node_id,node_fingerprint,operation_id,runtime_handle,provider_handle,revision,created_at,updated_at
) VALUES(?,?,?,?,?,NULLIF(?,''),1,?,?)`, run.ID, s.runtimeNodeID, s.runtimeNodeFingerprint, run.ID, runtimeHandle, providerHandle, now, now)
	if err != nil {
		return storageFailure("insert run runtime binding", err)
	}
	run.RuntimeHandle = runtimeHandle
	run.ProviderHandle = providerHandle
	run.RuntimeNodeID = s.runtimeNodeID
	run.RuntimeNodeFingerprint = s.runtimeNodeFingerprint
	run.RuntimeOperationID = run.ID
	return nil
}

func (s *Store) bindRunProvider(ctx context.Context, tx *sql.Tx, run *domain.Run, providerHandle, now string) error {
	providerHandle = strings.TrimSpace(providerHandle)
	if !validRuntimeHandle(providerHandle) || !s.runBindingIsCurrent(*run) {
		return &Error{Code: CodeRunConflict, Message: "provider binding requires this node's exact runtime binding"}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE run_runtime_bindings
SET provider_handle=?,revision=revision+1,updated_at=?
WHERE run_id=? AND node_id=? AND node_fingerprint=? AND operation_id=?
 AND runtime_handle=? AND provider_handle IS NULL`, providerHandle, now, run.ID,
		s.runtimeNodeID, s.runtimeNodeFingerprint, run.ID, run.RuntimeHandle)
	if err != nil {
		return storageFailure("bind run provider", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return &Error{Code: CodeRunConflict, Message: "provider binding no longer matches this node's runtime binding", Cause: err}
	}
	run.ProviderHandle = providerHandle
	return nil
}

func deleteRunRuntimeBinding(ctx context.Context, tx *sql.Tx, runID string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM run_runtime_bindings WHERE run_id=?", strings.TrimSpace(runID)); err != nil {
		return storageFailure("clear run runtime binding", err)
	}
	return nil
}

func (s *Store) runBindingIsCurrent(run domain.Run) bool {
	return run.RuntimeHandle != "" && run.RuntimeNodeID == s.runtimeNodeID &&
		run.RuntimeNodeFingerprint == s.runtimeNodeFingerprint && run.RuntimeOperationID == run.ID
}

// clearRunRuntimeProjection keeps in-memory values consistent with the
// dedicated binding row after it is deleted, and keeps idempotent mutation
// responses independent of node-local state that is deliberately omitted
// from their serialized receipt.
func clearRunRuntimeProjection(run *domain.Run) {
	run.RuntimeHandle = ""
	run.ProviderHandle = ""
	run.RuntimeNodeID = ""
	run.RuntimeNodeFingerprint = ""
	run.RuntimeOperationID = ""
}

func clearCheckRuntimeProjection(run *domain.CheckRun) {
	run.RuntimeHandle = ""
	run.RuntimeNodeID = ""
	run.RuntimeNodeFingerprint = ""
	run.RuntimeOperationID = ""
}

// RunBindingIsCurrent reports whether the internal handle projection belongs
// to this exact daemon node and operation. Callers must check it immediately
// before any runtime or provider adapter contact.
func (s *Store) RunBindingIsCurrent(run domain.Run) bool {
	return s.runBindingIsCurrent(run)
}

// CheckBindingIsCurrent is the check-runtime equivalent of
// RunBindingIsCurrent.
func (s *Store) CheckBindingIsCurrent(run domain.CheckRun) bool {
	return run.RuntimeHandle != "" && run.RuntimeNodeID == s.runtimeNodeID &&
		run.RuntimeNodeFingerprint == s.runtimeNodeFingerprint && run.RuntimeOperationID == run.ID
}

func runtimeBindingConflict(kind, id string) error {
	return &Error{Code: CodeRunConflict, Message: fmt.Sprintf("%s %s runtime binding does not belong to the current node and operation", kind, id)}
}

func runtimeBindingUnavailable(kind, id string) error {
	return &Error{Code: CodeRuntimeBindingUnavailable, Message: fmt.Sprintf("%s %s control requires a live runtime binding on the current node and operation", kind, id)}
}
