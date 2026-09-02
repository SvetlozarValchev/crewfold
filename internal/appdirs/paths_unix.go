//go:build !windows

package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maximumUnixSocketBytes = 100

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

	stateHome, err := environmentRoot("XDG_STATE_HOME", getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	configHome, err := environmentRoot("XDG_CONFIG_HOME", getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
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

	result, err := validatePaths(Paths{
		StateHome:  stateHome,
		ConfigHome: configHome,
		StateDir:   stateDir,
		ConfigDir:  configDir,
		RuntimeDir: runtimeDir,
		DataDir:    stateDir,
		SocketPath: filepath.Join(runtimeDir, "crewfold.sock"),
		UnitPath:   filepath.Join(configHome, "systemd", "user", "crewfold.service"),
	})
	if err != nil {
		return Paths{}, err
	}
	if len(result.SocketPath) > maximumUnixSocketBytes {
		return Paths{}, fmt.Errorf("owner-local socket path exceeds %d bytes; choose a shorter XDG_RUNTIME_DIR", maximumUnixSocketBytes)
	}
	return result, nil
}
