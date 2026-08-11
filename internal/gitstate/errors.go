package gitstate

import (
	"errors"
	"fmt"
)

const (
	CodeGitUnavailable      = "git_unavailable"
	CodeNotGitRepository    = "not_git_repository"
	CodeCheckoutUnavailable = "checkout_unavailable"
	CodeGitOutputInvalid    = "git_output_invalid"
	CodeGitCommandFailed    = "git_command_failed"
)

type Error struct {
	Code      string
	Operation string
	Path      string
	Cause     error
}

func (e *Error) Error() string {
	message := e.Operation
	if e.Path != "" {
		message += " for " + e.Path
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func ErrorCode(err error) string {
	var gitError *Error
	if errors.As(err, &gitError) {
		return gitError.Code
	}
	return CodeGitCommandFailed
}

type CommandError struct {
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("git exited with code %d: %s", e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("git exited with code %d: %v", e.ExitCode, e.Cause)
}

func (e *CommandError) Unwrap() error {
	return e.Cause
}
