package appdirs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesExactXDGLayout(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"XDG_STATE_HOME":  "/private/state",
		"XDG_CONFIG_HOME": "/private/config",
		"XDG_RUNTIME_DIR": "/run/user/1000",
	}
	paths, err := Resolve("/home/owner", func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if paths.DataDir != "/private/state/crewfold" || paths.ConfigDir != "/private/config/crewfold" || paths.RuntimeDir != "/run/user/1000/crewfold" {
		t.Fatalf("Resolve() paths = %#v", paths)
	}
	if paths.SocketPath != "/run/user/1000/crewfold/crewfold.sock" {
		t.Fatalf("SocketPath = %q", paths.SocketPath)
	}
	if paths.UnitPath != "/private/config/systemd/user/crewfold.service" {
		t.Fatalf("UnitPath = %q", paths.UnitPath)
	}
}

func TestResolveFallsBackBelowOwnerHomeAndState(t *testing.T) {
	t.Parallel()

	paths, err := Resolve("/home/owner", func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if paths.StateDir != filepath.FromSlash("/home/owner/.local/state/crewfold") {
		t.Fatalf("StateDir = %q", paths.StateDir)
	}
	if paths.ConfigDir != filepath.FromSlash("/home/owner/.config/crewfold") {
		t.Fatalf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.RuntimeDir != filepath.Join(paths.StateDir, "runtime") {
		t.Fatalf("RuntimeDir = %q, want below state", paths.RuntimeDir)
	}
}

func TestResolvePreservesVisibleWhitespaceInExactPaths(t *testing.T) {
	t.Parallel()

	paths, err := Resolve("/home/owner", func(name string) string {
		if name == "XDG_RUNTIME_DIR" {
			return "/run/user/1000 "
		}
		return ""
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if paths.RuntimeDir != "/run/user/1000 /crewfold" {
		t.Fatalf("RuntimeDir = %q, want exact visible whitespace preserved", paths.RuntimeDir)
	}
}

func TestResolveRejectsRelativeOrNoncanonicalRoots(t *testing.T) {
	t.Parallel()

	for name, environment := range map[string]map[string]string{
		"relative state":      {"XDG_STATE_HOME": "state"},
		"noncanonical config": {"XDG_CONFIG_HOME": "/private/../config"},
		"relative runtime":    {"XDG_RUNTIME_DIR": "run/user/1000"},
		"terminal control":    {"XDG_RUNTIME_DIR": "/run/user/1000/\x1bspoof"},
		"invalid UTF-8":       {"XDG_RUNTIME_DIR": "/run/user/1000/\xff"},
		"oversized runtime":   {"XDG_RUNTIME_DIR": "/" + strings.Repeat("a", 4097)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Resolve("/home/owner", func(key string) string { return environment[key] }); err == nil {
				t.Fatal("Resolve() error = nil, want refusal")
			}
		})
	}
}
