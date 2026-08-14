package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"crewfold/internal/loadtest"
	"crewfold/protocol"
)

func TestPersonalLoadCommandPublishesExactReport(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	want := testPersonalLoadReport("ok")
	called := false
	app.runPersonalLoad = func(context.Context) (loadtest.Report, error) {
		called = true
		return want, nil
	}
	if exit := app.Run([]string{"test", "load", "--profile", "personal-100", "--output", "json"}); exit != ExitOK {
		t.Fatalf("Run(test load) exit = %d, stderr = %q", exit, stderr.String())
	}
	if !called {
		t.Fatal("personal load runner was not called")
	}
	var got loadtest.Report
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode report: %v; stdout = %q", err, stdout.String())
	}
	if err := protocol.ValidateJSON("cli/v1/personal-load.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("published schema rejected report: %v", err)
	}
	if got.Schema != want.Schema || got.Profile != loadtest.Personal100Profile || got.Status != "ok" || got.Counts.KnownEvents != 100000 {
		t.Fatalf("report = %#v", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPersonalLoadCommandEmitsPartialReportAndFailure(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	app.runPersonalLoad = func(context.Context) (loadtest.Report, error) {
		return testPersonalLoadReport("failed"), errors.New("fixed budget exceeded")
	}
	if exit := app.Run([]string{"--output=json", "test", "load", "--profile=personal-100"}); exit != ExitFailure {
		t.Fatalf("Run(test load failure) exit = %d", exit)
	}
	var got loadtest.Report
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Status != "failed" {
		t.Fatalf("partial report = %#v, %v; stdout = %q", got, err, stdout.String())
	}
	if err := protocol.ValidateJSON("cli/v1/personal-load.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("published schema rejected partial failure report: %v", err)
	}
	if !strings.Contains(stderr.String(), `"code":"personal_load_failed"`) || !strings.Contains(stderr.String(), "fixed budget exceeded") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPersonalLoadCommandHasOneClosedProfileAndNoExternalPaths(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"missing":  {"test", "load"},
		"unknown":  {"test", "load", "--profile", "larger"},
		"socket":   {"test", "load", "--profile", "personal-100", "--socket", "/tmp/daemon.sock"},
		"data_dir": {"test", "load", "--profile", "personal-100", "--data-dir", "/tmp/data"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, _ := newTestApp()
			app.runPersonalLoad = func(context.Context) (loadtest.Report, error) {
				t.Fatal("runner called for invalid command")
				return loadtest.Report{}, nil
			}
			if exit := app.Run(args); exit != ExitUsage {
				t.Fatalf("Run(%v) exit = %d", args, exit)
			}
		})
	}
}

func testPersonalLoadReport(status string) loadtest.Report {
	timings := make([]loadtest.Timing, 5)
	for index, name := range []string{"generation", "verification", "store_mutation", "event_page_read", "projection_read"} {
		timings[index] = loadtest.Timing{Name: name, Repetitions: 1, P50Microseconds: 1, P95Microseconds: 1, P99Microseconds: 1, MaxMicroseconds: 1}
	}
	return loadtest.Report{
		Schema: "urn:crewfold:schema:cli:personal-load-report:v1", Profile: loadtest.Personal100Profile, Status: status,
		Environment:   loadtest.Environment{GOOS: "linux", GOARCH: "amd64", Kernel: "test", GoVersion: "go1.test", SQLiteVersion: "3.53.4", CPU: "test", LogicalCPUs: 1, MemoryBytes: 1},
		Counts:        loadtest.Counts{Workspaces: 1, Projects: 10, Agents: 100, Objectives: 10, Tasks: 1000, KnownEvents: 100000, NoisyProjectEvents: 80000},
		LogicalSHA256: strings.Repeat("a", 64), Timings: timings,
		Resources: loadtest.Resources{PeakRSSBytes: 1, DatabaseBytes: 1, Goroutines: 1, OpenFDs: 1},
		Assertions: []loadtest.Assertion{
			{Name: "known_events", Passed: status == "ok", Actual: 100000, Limit: 100000, Unit: "count"},
			{Name: "refusal_event_delta", Passed: true, Actual: 0, Limit: 0, Unit: "events"},
		},
	}
}
