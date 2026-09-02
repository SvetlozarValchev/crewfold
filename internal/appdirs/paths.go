// Package appdirs resolves Crewfold's owner-local paths.
package appdirs

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maximumOwnerPathBytes = 4096

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

func environmentRoot(name, value, fallback string) (string, error) {
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

func validatePaths(paths Paths) (Paths, error) {
	for name, value := range map[string]string{
		"state directory": paths.StateDir, "configuration directory": paths.ConfigDir,
		"runtime directory": paths.RuntimeDir, "socket path": paths.SocketPath, "unit path": paths.UnitPath,
	} {
		if len(value) > maximumOwnerPathBytes {
			return Paths{}, fmt.Errorf("%s exceeds %d bytes", name, maximumOwnerPathBytes)
		}
	}
	return paths, nil
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
