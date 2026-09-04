//go:build windows

package appdirs

import (
	"path/filepath"
	"testing"
)

func TestResolveWindowsUsesKnownFolderLayout(t *testing.T) {
	paths, err := ResolveWindows(`C:\Users\owner\AppData\Local`, `C:\Users\owner\AppData\Roaming`, `C:\Users\owner\Startup`, nil)
	if err != nil {
		t.Fatalf("ResolveWindows() error = %v", err)
	}
	if paths.DataDir != `C:\Users\owner\AppData\Local\crewfold` {
		t.Fatalf("DataDir = %q", paths.DataDir)
	}
	if paths.ConfigDir != `C:\Users\owner\AppData\Roaming\crewfold` {
		t.Fatalf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.RuntimeDir != `C:\Users\owner\AppData\Local\crewfold\runtime` {
		t.Fatalf("RuntimeDir = %q", paths.RuntimeDir)
	}
	if len(paths.SocketPath) <= len(`\\.\pipe\crewfold-`) || paths.SocketPath[:len(`\\.\pipe\crewfold-`)] != `\\.\pipe\crewfold-` {
		t.Fatalf("SocketPath = %q", paths.SocketPath)
	}
}

func TestResolveWindowsAcceptsCrewfoldOverrides(t *testing.T) {
	environment := map[string]string{
		"CREWFOLD_STATE_HOME":  `D:\Crewfold State`,
		"CREWFOLD_CONFIG_HOME": `D:\Crewfold Config`,
		"CREWFOLD_RUNTIME_DIR": `D:\Crewfold Runtime`,
	}
	paths, err := ResolveWindows(`C:\Local`, `C:\Roaming`, `C:\Startup`, func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("ResolveWindows() error = %v", err)
	}
	if paths.StateDir != filepath.Join(environment["CREWFOLD_STATE_HOME"], "crewfold") {
		t.Fatalf("StateDir = %q", paths.StateDir)
	}
	if paths.ConfigDir != filepath.Join(environment["CREWFOLD_CONFIG_HOME"], "crewfold") {
		t.Fatalf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.RuntimeDir != environment["CREWFOLD_RUNTIME_DIR"] {
		t.Fatalf("RuntimeDir = %q", paths.RuntimeDir)
	}
}

func TestResolveWindowsRejectsRelativeOverride(t *testing.T) {
	_, err := ResolveWindows(`C:\Local`, `C:\Roaming`, `C:\Startup`, func(name string) string {
		if name == "CREWFOLD_STATE_HOME" {
			return `relative\state`
		}
		return ""
	})
	if err == nil {
		t.Fatal("ResolveWindows() error = nil, want refusal")
	}
}
