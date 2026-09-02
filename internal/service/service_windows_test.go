//go:build windows

package service

import (
	"strings"
	"testing"
)

func TestWindowsStartupLauncherEscapesConfiguration(t *testing.T) {
	launcher := windowsStartupLauncher(Config{
		Executable: `C:\Program Files\Crewfold 100%\crewfold.exe`,
		DataDir:    `C:\Users\owner\Crewfold Data`,
		Endpoint:   `\\.\pipe\crewfold-test`,
	})
	for _, expected := range []string{
		`@echo off`,
		`title Crewfold service`,
		`"C:\Program Files\Crewfold 100%%\crewfold.exe" daemon run`,
		`--data-dir "C:\Users\owner\Crewfold Data"`,
		`--socket \\.\pipe\crewfold-test`,
	} {
		if !strings.Contains(launcher, expected) {
			t.Fatalf("startup launcher does not contain %q:\n%s", expected, launcher)
		}
	}
}
