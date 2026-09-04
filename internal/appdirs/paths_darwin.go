//go:build darwin

package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"crewfold/internal/localipc"
)

const maximumDarwinSocketBytes = 100

// Default resolves the current owner's native macOS library paths.
func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve owner home directory: %w", err)
	}
	return ResolveDarwin(home, os.Getenv)
}

// ResolveDarwin derives the Crewfold layout from a macOS home directory and
// environment lookup.
func ResolveDarwin(home string, getenv func(string) string) (Paths, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	home, err := exactAbsoluteDirectoryRoot("home directory", home)
	if err != nil {
		return Paths{}, err
	}
	applicationSupport := filepath.Join(home, "Library", "Application Support")
	stateHome, err := environmentRoot("CREWFOLD_STATE_HOME", getenv("CREWFOLD_STATE_HOME"), applicationSupport)
	if err != nil {
		return Paths{}, err
	}
	configHome, err := environmentRoot("CREWFOLD_CONFIG_HOME", getenv("CREWFOLD_CONFIG_HOME"), applicationSupport)
	if err != nil {
		return Paths{}, err
	}
	stateDir := filepath.Join(stateHome, "Crewfold")
	runtimeDir := filepath.Join(home, "Library", "Caches", "Crewfold", "runtime")
	if override := getenv("CREWFOLD_RUNTIME_DIR"); strings.TrimSpace(override) != "" {
		runtimeDir, err = exactAbsoluteDirectoryRoot("CREWFOLD_RUNTIME_DIR", override)
		if err != nil {
			return Paths{}, err
		}
	}
	result, err := validatePaths(Paths{
		StateHome:  stateHome,
		ConfigHome: configHome,
		StateDir:   stateDir,
		ConfigDir:  filepath.Join(configHome, "Crewfold"),
		RuntimeDir: runtimeDir,
		DataDir:    stateDir,
		SocketPath: localipc.Endpoint(runtimeDir),
		UnitPath:   filepath.Join(home, "Library", "LaunchAgents", "dev.crewfold.Crewfold.plist"),
	})
	if err != nil {
		return Paths{}, err
	}
	if len(result.SocketPath) > maximumDarwinSocketBytes {
		return Paths{}, fmt.Errorf("owner-local socket path exceeds %d bytes; choose a shorter CREWFOLD_RUNTIME_DIR", maximumDarwinSocketBytes)
	}
	return result, nil
}
