// Package loadtest builds and measures Crewfold's provider-free personal-scale
// acceptance fixture. It never accepts a caller-owned path or runtime endpoint.
package loadtest

import (
	"fmt"
	"strings"
)

const (
	Personal100Profile = "personal-100"
	personalLoadSchema = "urn:crewfold:schema:cli:personal-load-report:v1"
)

// Report is the complete machine-readable result of the personal-100 profile.
type Report struct {
	Schema        string      `json:"schema"`
	Profile       string      `json:"profile"`
	Status        string      `json:"status"`
	Environment   Environment `json:"environment"`
	Counts        Counts      `json:"counts"`
	LogicalSHA256 string      `json:"logical_sha256"`
	Timings       []Timing    `json:"timings"`
	Resources     Resources   `json:"resources"`
	Assertions    []Assertion `json:"assertions"`
}

// Environment records the host and SQLite runtime that executed the
// measured run. The fixed M20 thresholds are absolute; this metadata is
// diagnostic rather than a hardware-relative substitute for them.
type Environment struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Kernel        string `json:"kernel"`
	GoVersion     string `json:"go_version"`
	SQLiteVersion string `json:"sqlite_version"`
	CPU           string `json:"cpu"`
	LogicalCPUs   int    `json:"logical_cpus"`
	MemoryBytes   uint64 `json:"memory_bytes"`
}

// Counts contains the exact public profile cardinalities. Workspace- and
// agent-owned event facts are deliberately accounted for as an assertion,
// rather than being silently assigned to a project.
type Counts struct {
	Workspaces         int64 `json:"workspaces"`
	Projects           int64 `json:"projects"`
	Agents             int64 `json:"agents"`
	Objectives         int64 `json:"objectives"`
	Tasks              int64 `json:"tasks"`
	KnownEvents        int64 `json:"known_events"`
	NoisyProjectEvents int64 `json:"noisy_project_events"`
}

// Timing uses nearest-rank percentiles and integer microseconds.
type Timing struct {
	Name            string `json:"name"`
	Repetitions     int    `json:"repetitions"`
	P50Microseconds int64  `json:"p50_microseconds"`
	P95Microseconds int64  `json:"p95_microseconds"`
	P99Microseconds int64  `json:"p99_microseconds"`
	MaxMicroseconds int64  `json:"max_microseconds"`
}

// Resources records bounded process and fixture-storage observations.
type Resources struct {
	PeakRSSBytes  uint64 `json:"peak_rss_bytes"`
	DatabaseBytes int64  `json:"database_bytes"`
	ArtifactBytes int64  `json:"artifact_bytes"`
	Goroutines    int    `json:"goroutines"`
	OpenFDs       int    `json:"open_fds"`
}

// Assertion records one exact cardinality or absolute M20 resource limit.
type Assertion struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Actual int64  `json:"actual"`
	Limit  int64  `json:"limit"`
	Unit   string `json:"unit"`
}

// FailedAssertionsError preserves a complete report while making a completed
// profile with failed fixed assertions observable as a non-successful command.
type FailedAssertionsError struct {
	Names []string
}

func (e *FailedAssertionsError) Error() string {
	if e == nil || len(e.Names) == 0 {
		return "personal-100 assertions failed"
	}
	return fmt.Sprintf("personal-100 assertions failed: %s", strings.Join(e.Names, ", "))
}
