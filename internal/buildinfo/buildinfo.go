// Package buildinfo exposes build metadata without consulting Git or the network
// at runtime. Release builds may replace the unexported variables with -ldflags.
package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

const VersionSchema = "urn:crewfold:schema:cli:version-response:v1"

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

// Info is the stable machine-readable version response.
type Info struct {
	Schema    string `json:"schema"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Current returns metadata embedded in the running binary and facts provided by
// the Go runtime. It never invokes Git or performs network access.
func Current() Info {
	return Info{
		Schema:    VersionSchema,
		Version:   normalized(version, "dev"),
		Commit:    normalized(commit, "unknown"),
		BuiltAt:   normalized(builtAt, "unknown"),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// Validate checks the internal consistency needed by the M0 self-diagnostic.
func (i Info) Validate() error {
	if i.Schema != VersionSchema {
		return fmt.Errorf("unexpected version schema %q", i.Schema)
	}

	fields := map[string]string{
		"version":    i.Version,
		"commit":     i.Commit,
		"built_at":   i.BuiltAt,
		"go_version": i.GoVersion,
		"platform":   i.Platform,
	}
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", name)
		}
	}

	if i.BuiltAt != "unknown" {
		if _, err := time.Parse(time.RFC3339, i.BuiltAt); err != nil {
			return fmt.Errorf("built_at is not RFC3339: %w", err)
		}
	}

	return nil
}

func normalized(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
