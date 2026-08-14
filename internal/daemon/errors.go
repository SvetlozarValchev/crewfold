package daemon

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidConfiguration = "invalid_daemon_configuration"
	CodeDataDirInUse         = "data_dir_in_use"
	CodeSocketInUse          = "socket_in_use"
	CodeSocketPathOccupied   = "socket_path_occupied"
	CodeSocketUnavailable    = "socket_unavailable"
	CodeDatabaseUnavailable  = "database_unavailable"
	CodeRestoreNotActivated  = "restore_not_activated"
)

// StartupError gives the CLI a stable code while retaining the underlying cause.
type StartupError struct {
	Code    string
	Message string
	Cause   error
}

func (e *StartupError) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *StartupError) Unwrap() error {
	return e.Cause
}

func ErrorCode(err error) string {
	var startupError *StartupError
	if errors.As(err, &startupError) {
		return startupError.Code
	}
	return "daemon_failed"
}
