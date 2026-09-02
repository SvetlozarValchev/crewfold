//go:build windows

package room

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *HerdrStewardRuntime) herdrEnvironmentPath(agentKind string) (string, error) {
	paths := []string{filepath.Dir(r.crewfoldPath)}
	if agentKind == "pi" {
		shimDirectory, err := r.ensureWindowsPiShim()
		if err != nil {
			return "", err
		}
		paths = append([]string{shimDirectory}, paths...)
	}
	if inherited := os.Getenv("PATH"); inherited != "" {
		paths = append(paths, inherited)
	}
	return strings.Join(paths, string(os.PathListSeparator)), nil
}

// Herdr currently starts agents through PowerShell's Start-Process. npm places
// an extensionless POSIX shim before pi.cmd in the same directory, and
// Start-Process selects that shim even though CreateProcess cannot execute it.
// A private earlier PATH entry containing only pi.cmd lets PATHEXT select the
// native command shim without changing the user's installation.
func (r *HerdrStewardRuntime) ensureWindowsPiShim() (string, error) {
	piCommand, err := exec.LookPath("pi.cmd")
	if err != nil {
		return "", fmt.Errorf("find pi.cmd for Herdr steward: %w", err)
	}
	shimDirectory := filepath.Join(r.dataDir, "runtime-bin")
	if err := os.MkdirAll(shimDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create Herdr runtime directory: %w", err)
	}
	// Percent signs are expanded by cmd.exe even inside quotes.
	target := strings.ReplaceAll(piCommand, "%", "%%")
	content := []byte("@\"" + target + "\" %*\r\n")
	shimPath := filepath.Join(shimDirectory, "pi.cmd")
	if existing, readErr := os.ReadFile(shimPath); readErr == nil && bytes.Equal(existing, content) {
		return shimDirectory, nil
	}
	temporaryPath := shimPath + ".tmp"
	if err := os.WriteFile(temporaryPath, content, 0o600); err != nil {
		return "", fmt.Errorf("write Pi command shim: %w", err)
	}
	if err := os.Remove(shimPath); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(temporaryPath)
		return "", fmt.Errorf("replace Pi command shim: %w", err)
	}
	if err := os.Rename(temporaryPath, shimPath); err != nil {
		_ = os.Remove(temporaryPath)
		return "", fmt.Errorf("install Pi command shim: %w", err)
	}
	return shimDirectory, nil
}
