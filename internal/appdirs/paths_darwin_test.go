//go:build darwin

package appdirs

import "testing"

func TestResolveDarwinUsesNativeLibraryLayout(t *testing.T) {
	paths, err := ResolveDarwin("/Users/owner", nil)
	if err != nil {
		t.Fatalf("ResolveDarwin() error = %v", err)
	}
	if paths.DataDir != "/Users/owner/Library/Application Support/Crewfold" {
		t.Fatalf("DataDir = %q", paths.DataDir)
	}
	if paths.ConfigDir != "/Users/owner/Library/Application Support/Crewfold" {
		t.Fatalf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.RuntimeDir != "/Users/owner/Library/Caches/Crewfold/runtime" {
		t.Fatalf("RuntimeDir = %q", paths.RuntimeDir)
	}
	if paths.UnitPath != "/Users/owner/Library/LaunchAgents/dev.crewfold.Crewfold.plist" {
		t.Fatalf("UnitPath = %q", paths.UnitPath)
	}
}

func TestResolveDarwinAcceptsCrewfoldOverrides(t *testing.T) {
	environment := map[string]string{
		"CREWFOLD_STATE_HOME":  "/Volumes/Work/Crewfold State",
		"CREWFOLD_CONFIG_HOME": "/Volumes/Work/Crewfold Config",
		"CREWFOLD_RUNTIME_DIR": "/tmp/crewfold-owner",
	}
	paths, err := ResolveDarwin("/Users/owner", func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("ResolveDarwin() error = %v", err)
	}
	if paths.DataDir != "/Volumes/Work/Crewfold State/Crewfold" {
		t.Fatalf("DataDir = %q", paths.DataDir)
	}
	if paths.ConfigDir != "/Volumes/Work/Crewfold Config/Crewfold" {
		t.Fatalf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.RuntimeDir != environment["CREWFOLD_RUNTIME_DIR"] {
		t.Fatalf("RuntimeDir = %q", paths.RuntimeDir)
	}
}
