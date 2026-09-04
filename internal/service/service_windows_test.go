//go:build windows

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsStartupLauncherEscapesConfiguration(t *testing.T) {
	launcher, err := windowsStartupLauncher(Config{
		Executable: `C:\Program Files\Crew&fold 100%\crewfold.exe`,
		DataDir:    `C:\Users\owner\Crewfold Data`,
		Endpoint:   `\\.\pipe\crewfold-test`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`@echo off`,
		`setlocal DisableDelayedExpansion`,
		`title Crewfold service`,
		`"C:\Program Files\Crew&fold 100%%\crewfold.exe" daemon run`,
		`--data-dir "C:\Users\owner\Crewfold Data"`,
		`--socket \\.\pipe\crewfold-test`,
	} {
		if !strings.Contains(launcher, expected) {
			t.Fatalf("startup launcher does not contain %q:\n%s", expected, launcher)
		}
	}
}

func TestEscapeWindowsBatchArgumentProtectsUnquotedMetacharacters(t *testing.T) {
	t.Parallel()
	got, err := escapeWindowsBatchArgument(`C:\Crew&fold^(preview)\100%\crewfold.exe`)
	if err != nil {
		t.Fatal(err)
	}
	want := `C:\Crew^&fold^^^(preview^)\100%%\crewfold.exe`
	if got != want {
		t.Fatalf("escaped argument = %q, want %q", got, want)
	}
	if _, err := escapeWindowsBatchArgument("bad\npath"); err == nil {
		t.Fatal("expected newline rejection")
	}
}

func TestWindowsLauncherUpgradeAndUninstall(t *testing.T) {
	root := t.TempDir()
	definition := filepath.Join(root, "Startup Folder", "Crewfold.cmd")
	config := Config{
		Executable:     filepath.Join(root, "Program & 100%", "crewfold.exe"),
		DataDir:        filepath.Join(root, "Data ユニコード"),
		Endpoint:       `\\.\pipe\crewfold-uninstall-test`,
		DefinitionPath: definition,
	}
	if err := installLauncher(config); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := installLauncher(config); err != nil {
		t.Fatalf("repeated installation failed: %v", err)
	}
	second, err := os.ReadFile(definition)
	if err != nil || string(first) != string(second) {
		t.Fatalf("launcher upgrade changed content: %v", err)
	}
	status, err := managePlatform(context.Background(), "uninstall", config)
	if err != nil || status != "not-installed" {
		t.Fatalf("uninstall = %q, %v", status, err)
	}
	if _, err := os.Stat(definition); !os.IsNotExist(err) {
		t.Fatalf("startup launcher still exists: %v", err)
	}
	if _, err := os.Stat(config.DataDir); err != nil {
		t.Fatalf("uninstall removed data directory: %v", err)
	}
	if status, err := managePlatform(context.Background(), "uninstall", config); err != nil || status != "not-installed" {
		t.Fatalf("repeated uninstall = %q, %v", status, err)
	}
}
