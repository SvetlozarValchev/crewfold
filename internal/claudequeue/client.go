// Package claudequeue delivers wake-ups to open Claude Code sessions through
// a short-lived, tool-restricted SendMessage relay.
package claudequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Session struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type Client struct {
	Executable string
	Model      string
}

func (c Client) ResolveExecutable() (string, error) { return c.executable() }

func (c Client) ResolveSession(ctx context.Context, directory, explicit string) (Session, error) {
	sessions, err := c.sessions(ctx)
	if err != nil {
		return Session{}, err
	}
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		for _, session := range sessions {
			if session.SessionID == explicit {
				return session, nil
			}
		}
		return Session{}, errors.New("Claude session is not currently running")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return Session{}, err
	}
	var matches []Session
	for _, session := range sessions {
		if session.Kind == "interactive" && filepath.Clean(session.CWD) == filepath.Clean(directory) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return Session{}, errors.New("no active Claude session uses this working directory; run join inside Claude or pass --session SESSION_ID")
	}
	names := make([]string, 0, len(matches))
	for _, session := range matches {
		names = append(names, session.Name+" ("+session.SessionID+")")
	}
	sort.Strings(names)
	return Session{}, fmt.Errorf("multiple active Claude sessions use this working directory; pass --session SESSION_ID: %s", strings.Join(names, ", "))
}

func (c Client) Validate(ctx context.Context, sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	_, err := c.ResolveSession(ctx, "", sessionID)
	return err
}

func (c Client) Deliver(ctx context.Context, sessionID, text string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("Claude delivery message is required")
	}
	session, err := c.ResolveSession(ctx, "", sessionID)
	if err != nil {
		return err
	}
	executable, err := c.executable()
	if err != nil {
		return err
	}
	model := strings.TrimSpace(c.Model)
	if model == "" {
		model = "haiku"
	}
	prompt := "Load the SendMessage tool through ToolSearch (query select:SendMessage), then call SendMessage exactly once with to=" + strconv.Quote(session.Name) + " and message set to the exact text between the markers, verbatim. After the tool succeeds, output exactly CREWFOLD_DELIVERED and stop.\n<<<CREWFOLD_MESSAGE\n" + text + "\nCREWFOLD_MESSAGE>>>\n"
	command := exec.CommandContext(ctx, executable,
		"-p", "-n", "crewfold-relay", "--model", model,
		"--dangerously-skip-permissions", "--no-session-persistence",
		"--allowedTools", "ToolSearch,SendMessage",
	)
	command.Stdin = strings.NewReader(prompt)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return commandError("relay Claude message", output, err)
	}
	if !strings.Contains(string(output), "CREWFOLD_DELIVERED") {
		return commandError("relay Claude message", output, errors.New("relay did not confirm SendMessage"))
	}
	return nil
}

func (c Client) sessions(ctx context.Context) ([]Session, error) {
	executable, err := c.executable()
	if err != nil {
		return nil, err
	}
	output, err := exec.CommandContext(ctx, executable, "agents", "--json").CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, commandError("list Claude sessions", output, err)
	}
	var sessions []Session
	if err := json.Unmarshal(output, &sessions); err != nil {
		return nil, fmt.Errorf("list Claude sessions: invalid JSON: %w", err)
	}
	return sessions, nil
}

func (c Client) executable() (string, error) {
	if executable := strings.TrimSpace(c.Executable); executable != "" {
		return executable, nil
	}
	if executable, err := exec.LookPath("claude"); err == nil {
		return executable, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, ".local", "bin", "claude")
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("Claude CLI is not available on PATH or at ~/.local/bin/claude")
}

func validateSessionID(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("Claude session ID is required")
	}
	if len(sessionID) > 128 || strings.ContainsAny(sessionID, " \t\r\n") {
		return errors.New("Claude session ID must be one bounded identifier")
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
