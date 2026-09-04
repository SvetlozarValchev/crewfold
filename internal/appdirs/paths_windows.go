//go:build windows

package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"crewfold/internal/localipc"

	"golang.org/x/sys/windows"
)

// Default resolves the current owner's native Windows Known Folder paths.
func Default() (Paths, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve Local AppData: %w", err)
	}
	roaming, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve Roaming AppData: %w", err)
	}
	startup, err := windows.KnownFolderPath(windows.FOLDERID_Startup, 0)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve Startup folder: %w", err)
	}
	return ResolveWindows(local, roaming, startup, os.Getenv)
}

// ResolveWindows derives the Crewfold layout from native Windows folder roots
// and an environment lookup. It is exposed to keep path policy independently
// testable without calling Windows shell APIs.
func ResolveWindows(localAppData, roamingAppData, startup string, getenv func(string) string) (Paths, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	localAppData, err := exactAbsoluteDirectoryRoot("Local AppData", localAppData)
	if err != nil {
		return Paths{}, err
	}
	roamingAppData, err = exactAbsoluteDirectoryRoot("Roaming AppData", roamingAppData)
	if err != nil {
		return Paths{}, err
	}
	startup, err = environmentRoot("CREWFOLD_SERVICE_DIR", getenv("CREWFOLD_SERVICE_DIR"), startup)
	if err != nil {
		return Paths{}, err
	}
	stateHome, err := environmentRoot("CREWFOLD_STATE_HOME", getenv("CREWFOLD_STATE_HOME"), localAppData)
	if err != nil {
		return Paths{}, err
	}
	configHome, err := environmentRoot("CREWFOLD_CONFIG_HOME", getenv("CREWFOLD_CONFIG_HOME"), roamingAppData)
	if err != nil {
		return Paths{}, err
	}
	stateDir := filepath.Join(stateHome, "crewfold")
	runtimeDir := filepath.Join(stateDir, "runtime")
	if override := getenv("CREWFOLD_RUNTIME_DIR"); strings.TrimSpace(override) != "" {
		runtimeDir, err = exactAbsoluteDirectoryRoot("CREWFOLD_RUNTIME_DIR", override)
		if err != nil {
			return Paths{}, err
		}
	}
	return validatePaths(Paths{
		StateHome:  stateHome,
		ConfigHome: configHome,
		StateDir:   stateDir,
		ConfigDir:  filepath.Join(configHome, "crewfold"),
		RuntimeDir: runtimeDir,
		DataDir:    stateDir,
		SocketPath: localipc.Endpoint(runtimeDir),
		UnitPath:   filepath.Join(startup, "Crewfold.cmd"),
	})
}
