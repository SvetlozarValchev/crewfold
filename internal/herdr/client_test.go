package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordedRunner struct {
	responses map[string]CommandResult
	errors    map[string]error
	calls     []string
	env       []map[string]string
}

func (runner *recordedRunner) Run(_ context.Context, _ string, arguments []string, environment map[string]string) (CommandResult, error) {
	key := strings.Join(arguments, " ")
	runner.calls = append(runner.calls, key)
	copyEnvironment := make(map[string]string, len(environment))
	for name, value := range environment {
		copyEnvironment[name] = value
	}
	runner.env = append(runner.env, copyEnvironment)
	return runner.responses[key], runner.errors[key]
}

func TestProbeAcceptsRecordedCompatibleSchemaAndLiveSession(t *testing.T) {
	runner := &recordedRunner{responses: map[string]CommandResult{
		"--version":         {Stdout: []byte("herdr 0.8.0\n")},
		"api schema --json": {Stdout: fixture(t, "schema-compatible.json")},
		"api snapshot":      {Stdout: fixture(t, "snapshot-initial.json")},
	}}
	report := NewClient("/opt/herdr", "crewfold-tests", runner).Probe(context.Background())
	if !report.Compatible() || report.SchemaVersion != 1 || report.Protocol != 19 || len(report.Checks) != 3 {
		t.Fatalf("Probe() = %#v", report)
	}
	if runner.env[len(runner.env)-1]["HERDR_SESSION"] != "crewfold-tests" {
		t.Fatalf("session environment = %#v", runner.env)
	}
}

func TestProbeRejectsUnsupportedSchemaBeforeAnySurfaceMutation(t *testing.T) {
	runner := &recordedRunner{responses: map[string]CommandResult{
		"--version":         {Stdout: []byte("herdr 0.9.0\n")},
		"api schema --json": {Stdout: fixture(t, "schema-incompatible.json")},
	}}
	report := NewClient("herdr", "", runner).Probe(context.Background())
	if report.Compatible() || report.Protocol != 20 || len(runner.calls) != 2 {
		t.Fatalf("Probe() = %#v, calls = %#v", report, runner.calls)
	}
	if err := report.Error(); err == nil || !strings.Contains(err.Error(), "install a compatible Herdr release") {
		t.Fatalf("Probe().Error() = %v", err)
	}
}

func TestStableTerminalIdentityResolvesMovedPane(t *testing.T) {
	var initial, moved Snapshot
	for path, target := range map[string]*Snapshot{"snapshot-initial.json": &initial, "snapshot-moved.json": &moved} {
		client := NewClient("herdr", "", &recordedRunner{responses: map[string]CommandResult{"api snapshot": {Stdout: fixture(t, path)}}})
		value, err := client.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot(%s) error = %v", path, err)
		}
		*target = value
	}
	first, firstFound := PaneByTerminal(initial, "term-fixture")
	second, secondFound := PaneByTerminal(moved, "term-fixture")
	if !firstFound || !secondFound || first.PaneID == second.PaneID || second.PaneID != "w2:p7" {
		t.Fatalf("pane resolution before=%#v after=%#v", first, second)
	}
}

func TestCommandErrorKeepsHerdrCodeAndBoundedDiagnostic(t *testing.T) {
	runner := &recordedRunner{responses: map[string]CommandResult{
		"pane close w1:p1": {Stderr: fixture(t, "error-pane-not-found.json"), ExitCode: 1},
	}}
	err := NewClient("herdr", "", runner).ClosePane(context.Background(), "w1:p1")
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != "pane_not_found" || !strings.Contains(commandError.Message, "not found") {
		t.Fatalf("ClosePane() error = %#v", err)
	}
}

func TestRunPaneSubmitsTheWrittenCommand(t *testing.T) {
	runner := &recordedRunner{responses: map[string]CommandResult{
		"pane run w1:p1 printf ready": {},
		"pane send-keys w1:p1 enter":  {},
	}}
	if err := NewClient("herdr", "", runner).RunPane(context.Background(), "w1:p1", "printf ready"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pane run w1:p1 printf ready", "pane send-keys w1:p1 enter"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("RunPane calls = %#v, want %#v", runner.calls, want)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "protocol", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
