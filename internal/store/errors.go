package store

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidWorkspace    = "invalid_workspace"
	CodeWorkspaceExists     = "workspace_already_exists"
	CodeWorkspaceNotFound   = "workspace_not_found"
	CodeIdempotencyConflict = "idempotency_conflict"
	CodeStorageFailed       = "storage_failed"
)

// Error is a stable domain/storage error that can cross the local API boundary.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func ErrorCode(err error) string {
	var storeError *Error
	if errors.As(err, &storeError) {
		return storeError.Code
	}
	return CodeStorageFailed
}
