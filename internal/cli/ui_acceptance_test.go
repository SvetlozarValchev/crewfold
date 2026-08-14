package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/tui"
)

func TestM19UICommandDispatchesExactCurrentConfiguration(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	var got tui.Config
	called := 0
	app.runTUI = func(_ context.Context, config tui.Config) error {
		called++
		got = config
		return nil
	}
	want := tui.Config{SocketPath: "/tmp/crewfold.sock", Workspace: "personal", Project: "world-engine", Color: tui.ColorNever}
	exitCode := app.Run([]string{
		"ui", "--socket", want.SocketPath, "--workspace", want.Workspace,
		"--project", want.Project, "--color", string(want.Color),
	})
	if exitCode != ExitOK || called != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("ui dispatch exit=%d called=%d config=%#v, want 0/1/%#v", exitCode, called, got, want)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("successful interactive dispatch wrote stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestM19UICommandRejectsOutputInEveryGlobalPosition(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"ui", "--output", "text"},
		{"ui", "--output=json"},
		{"--output", "text", "ui"},
		{"--output=json", "ui"},
	}
	for _, args := range tests {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app, _, stderr := newTestApp()
			called := false
			app.runTUI = func(context.Context, tui.Config) error {
				called = true
				return nil
			}
			if exitCode := app.Run(args); exitCode != ExitUsage || called || !strings.Contains(stderr.String(), "does not support --output") {
				t.Fatalf("Run(%q) exit=%d called=%t stderr=%q", args, exitCode, called, stderr.String())
			}
		})
	}
}

func TestM19UICommandRejectsAmbiguousOrUnknownOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "project without workspace", args: []string{"ui", "--project", "game"}, want: "--project requires --workspace"},
		{name: "unknown color", args: []string{"ui", "--color", "always"}, want: "unsupported UI color mode"},
		{name: "unknown option", args: []string{"ui", "--legacy-ui", "true"}, want: "unknown option"},
		{name: "positional", args: []string{"ui", "dashboard"}, want: "unexpected positional"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			called := false
			app.runTUI = func(context.Context, tui.Config) error {
				called = true
				return nil
			}
			if exitCode := app.Run(test.args); exitCode != ExitUsage || called || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("Run(%q) exit=%d called=%t stderr=%q, want %q", test.args, exitCode, called, stderr.String(), test.want)
			}
		})
	}
}

func TestM19UIHelpAndFailureAreStable(t *testing.T) {
	t.Parallel()
	t.Run("help", func(t *testing.T) {
		app, stdout, stderr := newTestApp()
		called := false
		app.runTUI = func(context.Context, tui.Config) error {
			called = true
			return nil
		}
		if exitCode := app.Run([]string{"ui", "--help"}); exitCode != ExitOK || called || !strings.Contains(stdout.String(), "crewfold ui") || stderr.Len() != 0 {
			t.Fatalf("ui help exit=%d called=%t stdout=%q stderr=%q", exitCode, called, stdout.String(), stderr.String())
		}
	})
	t.Run("runtime failure", func(t *testing.T) {
		app, _, stderr := newTestApp()
		app.runTUI = func(context.Context, tui.Config) error { return errors.New("terminal unavailable") }
		if exitCode := app.Run([]string{"ui", "--socket", "/tmp/crewfold.sock"}); exitCode != ExitFailure || !strings.Contains(stderr.String(), "operator UI failed") || !strings.Contains(stderr.String(), "terminal unavailable") {
			t.Fatalf("ui failure exit=%d stderr=%q", exitCode, stderr.String())
		}
	})
}
