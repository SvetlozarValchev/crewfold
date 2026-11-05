// Package codexqueue delivers messages to open Codex sessions through the
// Codex CLI queue.
package codexqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Client struct {
	Executable string
}

func (c Client) Validate(ctx context.Context, threadID string) error {
	if err := validateThreadID(threadID); err != nil {
		return err
	}
	executable, err := c.executable()
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, executable, "queue", "--help").CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return commandError("verify Codex queue support", output, err)
	}
	return nil
}

func (c Client) ResolveExecutable() (string, error) {
	return c.executable()
}

func (c Client) Deliver(ctx context.Context, threadID, text string) error {
	if err := validateThreadID(threadID); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("Codex queue message is required")
	}
	executable, err := c.executable()
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, executable, "queue", "--thread", threadID, "--message", text).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return commandError("queue Codex message", output, err)
	}
	return nil
}

func (c Client) executable() (string, error) {
	if executable := strings.TrimSpace(c.Executable); executable != "" {
		return executable, nil
	}
	executable, err := exec.LookPath("codex")
	if err == nil {
		return executable, nil
	}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		candidate := filepath.Join(home, ".local", "bin", "codex")
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("Codex CLI is not available on PATH or at ~/.local/bin/codex")
}

func validateThreadID(threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("Codex thread ID is required")
	}
	if len(threadID) > 128 || strings.ContainsAny(threadID, " \t\r\n") {
		return errors.New("Codex thread ID must be one bounded UUID or exact session name")
	}
	return nil
}

func commandError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	if len(detail) > 1000 {
		detail = detail[:1000] + "…"
	}
	return fmt.Errorf("%s: %s: %w", action, detail, err)
}
