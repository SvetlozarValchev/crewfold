//go:build windows

package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrEnvironmentPathCreatesNativePiCommandShim(t *testing.T) {
	root := t.TempDir()
	npmDirectory := filepath.Join(root, "npm tools")
	if err := os.MkdirAll(npmDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	piCommand := filepath.Join(npmDirectory, "pi.cmd")
	if err := os.WriteFile(piCommand, []byte("@echo original\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// This is the incompatible npm POSIX shim that PowerShell otherwise finds.
	if err := os.WriteFile(filepath.Join(npmDirectory, "pi"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npmDirectory)

	runtime := &HerdrStewardRuntime{
		crewfoldPath: filepath.Join(root, "program", "crewfold.exe"),
		dataDir:      filepath.Join(root, "state"),
	}
	pathValue, err := runtime.herdrEnvironmentPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	shimDirectory := strings.Split(pathValue, string(os.PathListSeparator))[0]
	shim, err := os.ReadFile(filepath.Join(shimDirectory, "pi.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	want := "@\"" + piCommand + "\" %*\r\n"
	if string(shim) != want {
		t.Fatalf("shim = %q, want %q", shim, want)
	}
	if _, err := runtime.herdrEnvironmentPath("pi"); err != nil {
		t.Fatalf("repeated shim preparation failed: %v", err)
	}
}

func TestHerdrEnvironmentPathDoesNotRequirePiForCodex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", "")
	runtime := &HerdrStewardRuntime{crewfoldPath: filepath.Join(root, "crewfold.exe"), dataDir: root}
	pathValue, err := runtime.herdrEnvironmentPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	if pathValue != root {
		t.Fatalf("PATH = %q, want %q", pathValue, root)
	}
}
