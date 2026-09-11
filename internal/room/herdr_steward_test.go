package room

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStewardCodexArgumentsResumeInitializedThread(t *testing.T) {
	fresh := stewardCodexArguments(HostedSteward{ManagedWorkingDirectory: true})
	if slices.Contains(fresh, "resume") || slices.Contains(fresh, "--last") {
		t.Fatalf("fresh steward unexpectedly resumes: %#v", fresh)
	}
	if !slices.Contains(fresh, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("managed steward lost its sandbox argument: %#v", fresh)
	}

	resuming := stewardCodexArguments(HostedSteward{ManagedWorkingDirectory: true, InitializedAt: "2026-09-04T00:00:00Z"})
	if len(resuming) < 2 || resuming[len(resuming)-2] != "resume" || resuming[len(resuming)-1] != "--last" {
		t.Fatalf("initialized steward does not resume its prior thread: %#v", resuming)
	}
}

func TestStewardInspectionRepairsLostHerdrAgentName(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "herdr")
	logPath := filepath.Join(directory, "calls.log")
	markerPath := filepath.Join(directory, "renamed")
	t.Setenv("HERDR_TEST_LOG", logPath)
	t.Setenv("HERDR_TEST_MARKER", markerPath)
	writeFakeHerdr(t, script, `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$HERDR_TEST_LOG"
case "$*" in
  *"agent get cf_steward")
    if [ -f "$HERDR_TEST_MARKER" ]; then
      printf '%s\n' '{"result":{"agent":{"agent":"codex","agent_status":"idle","cwd":"/tmp/steward","foreground_cwd":"/tmp/steward","pane_id":"w1:p1","workspace_id":"w1"}}}'
    else
      printf '%s\n' '{"error":{"code":"agent_not_found","message":"agent target cf_steward not found"}}' >&2
      exit 1
    fi
    ;;
  *"agent get w1:p1")
    printf '%s\n' '{"result":{"agent":{"agent":"codex","agent_status":"idle","cwd":"/tmp/steward","foreground_cwd":"/tmp/steward","pane_id":"w1:p1","workspace_id":"w1"}}}'
    ;;
  *"agent rename w1:p1 cf_steward")
    : > "$HERDR_TEST_MARKER"
    printf '%s\n' '{"result":{"agent":{"agent":"codex","agent_status":"idle","cwd":"/tmp/steward","foreground_cwd":"/tmp/steward","pane_id":"w1:p1","workspace_id":"w1","name":"cf_steward"}}}'
    ;;
  *"agent read cf_steward"*)
    printf 'preserved steward transcript\n'
    ;;
  *)
    printf 'unexpected command: %s\n' "$*" >&2
    exit 2
    ;;
esac
`)

	runtime := &HerdrStewardRuntime{herdrPath: script}
	state, err := runtime.Inspect(context.Background(), HostedSteward{
		HerdrSession:     "crewfold-room",
		HerdrPaneID:      "w1:p1",
		AgentName:        "cf_steward",
		WorkingDirectory: "/tmp/steward",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.PaneID != "w1:p1" || state.AgentStatus != "idle" || state.Output != "preserved steward transcript\n" {
		t.Fatalf("unexpected repaired steward state: %#v", state)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"agent get cf_steward",
		"agent get w1:p1",
		"agent rename w1:p1 cf_steward",
		"agent read cf_steward",
	} {
		if !strings.Contains(string(calls), expected) {
			t.Fatalf("missing repair call %q in:\n%s", expected, calls)
		}
	}
}

func TestStewardInspectionDoesNotRenameUnexpectedPane(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "herdr")
	logPath := filepath.Join(directory, "calls.log")
	t.Setenv("HERDR_TEST_LOG", logPath)
	writeFakeHerdr(t, script, `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$HERDR_TEST_LOG"
case "$*" in
  *"agent get cf_steward")
    printf '%s\n' '{"error":{"code":"agent_not_found","message":"agent target cf_steward not found"}}' >&2
    exit 1
    ;;
  *"agent get w1:p1")
    printf '%s\n' '{"result":{"agent":{"agent":"codex","agent_status":"idle","cwd":"/tmp/someone-else","foreground_cwd":"/tmp/someone-else","pane_id":"w1:p1","workspace_id":"w1"}}}'
    ;;
  *)
    printf 'unexpected command: %s\n' "$*" >&2
    exit 2
    ;;
esac
`)

	runtime := &HerdrStewardRuntime{herdrPath: script}
	_, err := runtime.Inspect(context.Background(), HostedSteward{
		HerdrSession:     "crewfold-room",
		HerdrPaneID:      "w1:p1",
		AgentName:        "cf_steward",
		WorkingDirectory: "/tmp/steward",
	})
	if err == nil || !strings.Contains(err.Error(), "is not the expected Codex steward") {
		t.Fatalf("expected safe pane mismatch, got %v", err)
	}
	calls, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(calls), "agent rename") {
		t.Fatalf("unexpected pane was renamed:\n%s", calls)
	}
}

func writeFakeHerdr(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
