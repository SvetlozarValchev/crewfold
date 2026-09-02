//go:build windows

package room

import (
	"path/filepath"
	"strings"
)

func agentShellExecutable(agentKind, path string) string {
	if agentKind == "pi" {
		return shellQuote(msysPath(path))
	}
	return "& " + powershellQuote(path)
}

func msysPath(path string) string {
	slashed := filepath.ToSlash(path)
	volume := filepath.VolumeName(path)
	if len(volume) == 2 && volume[1] == ':' {
		return "/" + strings.ToLower(volume[:1]) + strings.TrimPrefix(slashed, volume)
	}
	return slashed
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
