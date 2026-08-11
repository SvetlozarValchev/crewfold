package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/localapi"
)

func TestNoArgumentsShowsRootHelp(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run(nil); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q, want root usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionText(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"version"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "crewfold dev\n") {
		t.Fatalf("stdout = %q, want development version", stdout.String())
	}
	if !strings.Contains(stdout.String(), "platform: ") {
		t.Fatalf("stdout = %q, want platform", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionJSONAcceptsGlobalOptionBeforeOrAfterCommand(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"before": {"--output", "json", "version"},
		"after":  {"version", "--output=json"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}

			var response buildinfo.Info
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != buildinfo.VersionSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, buildinfo.VersionSchema)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response.Validate() error = %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDoctorSelfJSON(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"doctor", "--self", "--output", "json"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Schema != doctorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, doctorSchema)
	}
	if response.Status != "ok" {
		t.Fatalf("response.Status = %q, want ok; checks = %#v", response.Status, response.Checks)
	}
	if len(response.Checks) != 3 {
		t.Fatalf("len(response.Checks) = %d, want 3", len(response.Checks))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDoctorSelfFailureIsVisibleAndNonZero(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.executablePath = func() (string, error) {
		return "", errors.New("injected executable lookup failure")
	}

	if exitCode := app.Run([]string{"doctor", "--self", "--output=json"}); exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Status != "failed" {
		t.Fatalf("response.Status = %q, want failed", response.Status)
	}
	if !strings.Contains(stdout.String(), "injected executable lookup failure") {
		t.Fatalf("stdout = %q, want injected failure detail", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestUnknownCommandHasStableTextErrorAndUsageExit(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"definitely-not-a-command"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `error: unknown command "definitely-not-a-command"`) {
		t.Fatalf("stderr = %q, want unknown-command error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "crewfold help") {
		t.Fatalf("stderr = %q, want help hint", stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "goroutine") {
		t.Fatalf("stderr = %q, must not contain a stack trace", stderr.String())
	}
}

func TestUnknownCommandHasStructuredJSONError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"--output=json", "nope"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Schema != errorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, errorSchema)
	}
	if response.Error.Code != "unknown_command" {
		t.Fatalf("response.Error.Code = %q, want unknown_command", response.Error.Code)
	}
}

func TestInvalidOutputAndCommandArgumentsAreUsageErrors(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"invalid output":      {"version", "--output", "yaml"},
		"duplicate output":    {"--output", "json", "version", "--output=json"},
		"version argument":    {"version", "extra"},
		"doctor missing self": {"doctor"},
		"too much help":       {"help", "version", "doctor"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestHelpTopics(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{"version", "doctor", "daemon", "status", "help"} {
		t.Run(topic, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run([]string{"help", topic}); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("stdout = %q, want usage", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDaemonRunPassesRequiredConfiguration(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	var received daemon.Config
	app.runDaemon = func(_ context.Context, config daemon.Config) error {
		received = config
		return nil
	}

	exitCode := app.Run([]string{
		"daemon", "run",
		"--data-dir", "/tmp/crewfold-test-data",
		"--socket=/tmp/crewfold-test.sock",
	})
	if exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
	}
	if received.DataDir != "/tmp/crewfold-test-data" {
		t.Fatalf("received.DataDir = %q", received.DataDir)
	}
	if received.SocketPath != "/tmp/crewfold-test.sock" {
		t.Fatalf("received.SocketPath = %q", received.SocketPath)
	}
	if received.Logger == nil {
		t.Fatal("received.Logger = nil")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want empty", stdout.String(), stderr.String())
	}
}

func TestDaemonRunReportsStableStartupError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.runDaemon = func(context.Context, daemon.Config) error {
		return &daemon.StartupError{
			Code:    daemon.CodeSocketInUse,
			Message: "socket is served by a live process",
		}
	}

	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket", "--output=json",
	})
	if exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Error.Code != daemon.CodeSocketInUse {
		t.Fatalf("response.Error.Code = %q, want %q", response.Error.Code, daemon.CodeSocketInUse)
	}
}

func TestStatusAndStopUseExplicitSocket(t *testing.T) {
	t.Parallel()

	status := localapi.StatusResult{
		Schema:        localapi.StatusSchema,
		Type:          "system_status",
		Status:        "ok",
		Protocol:      1,
		PID:           42,
		StartedAt:     time.Unix(1, 0).UTC().Format(time.RFC3339),
		UptimeMillis:  12,
		ServerVersion: buildinfo.Current(),
	}
	client := &fakeDaemonClient{
		status: status,
		stop: localapi.StopResult{
			Schema: localapi.StopSchema,
			Type:   "stop_acknowledgement",
			Status: "stopping",
		},
	}

	for name, test := range map[string]struct {
		args       []string
		wantSchema string
	}{
		"status": {[]string{"status", "--socket", "/tmp/test.sock", "--output=json"}, localapi.StatusSchema},
		"stop":   {[]string{"daemon", "stop", "--socket=/tmp/test.sock", "--output", "json"}, localapi.StopSchema},
	} {
		t.Run(name, func(t *testing.T) {
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/test.sock" {
					t.Fatalf("socketPath = %q, want /tmp/test.sock", socketPath)
				}
				return client
			}

			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
			}
			var response struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != test.wantSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, test.wantSchema)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestM1CommandsRejectMissingAndUnknownOptions(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"daemon subcommand": {"daemon"},
		"run data dir":      {"daemon", "run", "--socket", "/tmp/socket"},
		"run socket":        {"daemon", "run", "--data-dir", "/tmp/data"},
		"status socket":     {"status"},
		"stop socket":       {"daemon", "stop"},
		"unknown option":    {"status", "--bogus", "value"},
		"duplicate option":  {"status", "--socket", "one", "--socket", "two"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

type fakeDaemonClient struct {
	status    localapi.StatusResult
	statusErr error
	stop      localapi.StopResult
	stopErr   error
}

func (client *fakeDaemonClient) Status(context.Context) (localapi.StatusResult, error) {
	return client.status, client.statusErr
}

func (client *fakeDaemonClient) Stop(context.Context) (localapi.StopResult, error) {
	return client.stop, client.stopErr
}

func newTestApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return New(stdout, stderr, buildinfo.Current()), stdout, stderr
}
