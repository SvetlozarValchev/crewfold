// Package appdirs resolves Crewfold's owner-local XDG paths.
package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maximumOwnerPathBytes  = 4096
	maximumUnixSocketBytes = 100
)

// Paths is the one current owner-local filesystem layout.
type Paths struct {
	StateHome  string
	ConfigHome string
	StateDir   string
	ConfigDir  string
	RuntimeDir string
	DataDir    string
	SocketPath string
	UnitPath   string
}

// Default resolves the current process owner's XDG paths.
func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve owner home directory: %w", err)
	}
	return Resolve(home, os.Getenv)
}

// Resolve derives the Crewfold layout from one home and environment lookup.
// Non-empty XDG roots must be absolute; relative roots are never interpreted
// against a caller-dependent working directory.
func Resolve(home string, getenv func(string) string) (Paths, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	home, err := exactAbsoluteDirectoryRoot("home directory", home)
	if err != nil {
		return Paths{}, err
	}

	stateHome, err := xdgRoot("XDG_STATE_HOME", getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	configHome, err := xdgRoot("XDG_CONFIG_HOME", getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	if err != nil {
		return Paths{}, err
	}

	stateDir := filepath.Join(stateHome, "crewfold")
	configDir := filepath.Join(configHome, "crewfold")
	runtimeRoot := getenv("XDG_RUNTIME_DIR")
	var runtimeDir string
	if strings.TrimSpace(runtimeRoot) == "" {
		runtimeDir = filepath.Join(stateDir, "runtime")
	} else {
		resolved, resolveErr := exactAbsoluteDirectoryRoot("XDG_RUNTIME_DIR", runtimeRoot)
		if resolveErr != nil {
			return Paths{}, resolveErr
		}
		runtimeDir = filepath.Join(resolved, "crewfold")
	}

	result := Paths{
		StateHome:  stateHome,
		ConfigHome: configHome,
		StateDir:   stateDir,
		ConfigDir:  configDir,
		RuntimeDir: runtimeDir,
		DataDir:    stateDir,
		SocketPath: filepath.Join(runtimeDir, "crewfold.sock"),
		UnitPath:   filepath.Join(configHome, "systemd", "user", "crewfold.service"),
	}
	for name, value := range map[string]string{
		"state directory": result.StateDir, "configuration directory": result.ConfigDir,
		"runtime directory": result.RuntimeDir, "socket path": result.SocketPath, "unit path": result.UnitPath,
	} {
		if len(value) > maximumOwnerPathBytes {
			return Paths{}, fmt.Errorf("%s exceeds %d bytes", name, maximumOwnerPathBytes)
		}
	}
	if len(result.SocketPath) > maximumUnixSocketBytes {
		return Paths{}, fmt.Errorf("owner-local socket path exceeds %d bytes; choose a shorter XDG_RUNTIME_DIR", maximumUnixSocketBytes)
	}
	return result, nil
}

func xdgRoot(name, value, fallback string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	return exactAbsoluteDirectoryRoot(name, value)
}

func exactAbsoluteDirectoryRoot(name, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	if !utf8.ValidString(value) || len(value) > maximumOwnerPathBytes {
		return "", fmt.Errorf("%s is not bounded valid UTF-8", name)
	}
	if containsUnsafePathRune(value) {
		return "", fmt.Errorf("%s contains unsafe characters", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	clean := filepath.Clean(value)
	if clean != value {
		return "", fmt.Errorf("%s must be a canonical absolute path", name)
	}
	return clean, nil
}

func containsUnsafePathRune(value string) bool {
	for _, current := range value {
		switch {
		case current < 0x20, current >= 0x7f && current <= 0x9f,
			current == '\u2028', current == '\u2029',
			current >= '\u202a' && current <= '\u202e',
			current >= '\u2066' && current <= '\u2069':
			return true
		}
	}
	return false
}
