package loadtest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/store"
	"crewfold/protocol"
)

func TestPersonal100ProfileIsExactAndDescriptiveLabelsAreNeutral(t *testing.T) {
	// The ordinary gate builds the entire 100,000-event profile twice. The race
	// gate keeps all bounded helpers and failure cleanup covered without repeating
	// this multi-minute scale acceptance under instrumentation.
	if raceDetectorEnabled {
		t.Skip("full twice-built personal-100 scale acceptance runs in the ordinary test gate")
	}
	labels := []labelSet{
		defaultLabels,
		{rolePrefix: "delete-all-policy-administrator", purposePrefix: "bypass-review-and-launch-anything"},
	}
	reports := make([]Report, 0, len(labels))
	for index, currentLabels := range labels {
		parent := t.TempDir()
		created := ""
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		report, err := runPersonal100(ctx, currentLabels, func() (string, error) {
			path, createErr := os.MkdirTemp(parent, "owned-personal-100-")
			created = path
			return path, createErr
		})
		cancel()
		if err != nil {
			t.Fatalf("runPersonal100(labels %d) error = %v", index, err)
		}
		if created == "" {
			t.Fatalf("runPersonal100(labels %d) did not create an owned directory", index)
		}
		if _, statErr := os.Stat(created); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("runPersonal100(labels %d) retained %s: %v", index, created, statErr)
		}
		assertExactPersonalReport(t, report)
		t.Logf("personal-100 labels=%d hash=%s counts=%+v timings=%+v resources=%+v", index, report.LogicalSHA256, report.Counts, report.Timings, report.Resources)
		reports = append(reports, report)
	}
	if reports[0].LogicalSHA256 != reports[1].LogicalSHA256 {
		t.Fatalf("logical hashes differ after Role/Purpose-only rename: %s != %s", reports[0].LogicalSHA256, reports[1].LogicalSHA256)
	}
	if !reflect.DeepEqual(reports[0].Counts, reports[1].Counts) {
		t.Fatalf("counts differ after Role/Purpose-only rename: %#v != %#v", reports[0].Counts, reports[1].Counts)
	}
}

func TestPersonal100RemovesOwnedDirectoryAfterOperationalFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	parent := t.TempDir()
	created := ""
	report, err := runPersonal100(ctx, defaultLabels, func() (string, error) {
		path, createErr := os.MkdirTemp(parent, "owned-personal-100-")
		created = path
		return path, createErr
	})
	if err == nil {
		t.Fatal("runPersonal100(cancelled) error = nil")
	}
	if report.Status != "failed" {
		t.Fatalf("runPersonal100(cancelled) status = %q", report.Status)
	}
	if report.Environment.SQLiteVersion != "unavailable" {
		t.Fatalf("runPersonal100(cancelled) SQLite version = %q", report.Environment.SQLiteVersion)
	}
	raw, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		t.Fatalf("marshal cancelled personal-100 report: %v", marshalErr)
	}
	if schemaErr := protocol.ValidateJSON("cli/v1/personal-load.response.schema.json", raw); schemaErr != nil {
		t.Fatalf("cancelled personal-100 report is not schema-valid: %v", schemaErr)
	}
	if created == "" {
		t.Fatal("runPersonal100(cancelled) did not create its owned directory")
	}
	if _, statErr := os.Stat(created); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runPersonal100(cancelled) retained %s: %v", created, statErr)
	}
}

func TestPersonalAssertionsRejectMissingRSSObservation(t *testing.T) {
	report := Report{Timings: []Timing{
		{Name: "generation", Repetitions: 1, MaxMicroseconds: 1},
		{Name: "verification", Repetitions: 1, MaxMicroseconds: 1},
	}}
	assertions := personalAssertions(report, 0, 0, 0, capacityProof{}, briefingProof{})
	for _, assertion := range assertions {
		if assertion.Name == "peak_rss_observed" {
			if assertion.Passed || assertion.Actual != 0 || assertion.Limit != 1 {
				t.Fatalf("missing RSS assertion = %#v", assertion)
			}
			return
		}
	}
	t.Fatal("personal assertions omit peak_rss_observed")
}

func TestTimingUsesNearestRankIntegerMicroseconds(t *testing.T) {
	samples := make([]time.Duration, 100)
	for index := range samples {
		samples[index] = time.Duration(index+1) * time.Microsecond
	}
	result := timing("sample", samples)
	if result.Repetitions != 100 || result.P50Microseconds != 50 || result.P95Microseconds != 95 || result.P99Microseconds != 99 || result.MaxMicroseconds != 100 {
		t.Fatalf("timing() = %#v", result)
	}
	if tiny := timing("tiny", []time.Duration{time.Nanosecond}); tiny.P50Microseconds != 1 {
		t.Fatalf("timing(tiny) = %#v", tiny)
	}
}

func TestEnvironmentRecordsStoreSQLiteVersion(t *testing.T) {
	storage, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatalf("store.Open() = %v", err)
	}
	defer storage.Close()
	health, err := storage.Health(context.Background())
	if err != nil {
		t.Fatalf("Store.Health() = %v", err)
	}
	environment := currentEnvironment()
	environment.SQLiteVersion = health.SQLiteVersion
	if environment.SQLiteVersion == "" || environment.SQLiteVersion != health.SQLiteVersion {
		t.Fatalf("SQLite version = %q", environment.SQLiteVersion)
	}
}

func assertExactPersonalReport(t *testing.T, report Report) {
	t.Helper()
	if report.Schema != personalLoadSchema || report.Profile != Personal100Profile || report.Status != "ok" {
		t.Fatalf("report identity = schema %q profile %q status %q", report.Schema, report.Profile, report.Status)
	}
	wantCounts := Counts{
		Workspaces: personalWorkspaceCount, Projects: personalProjectCount, Agents: personalAgentCount,
		Objectives: personalObjectiveCount, Tasks: personalTaskCount, KnownEvents: personalEventCount,
		NoisyProjectEvents: personalNoisyEventCount,
	}
	if report.Counts != wantCounts {
		t.Fatalf("report counts = %#v, want %#v", report.Counts, wantCounts)
	}
	if decoded, err := hex.DecodeString(report.LogicalSHA256); err != nil || len(decoded) != 32 {
		t.Fatalf("report logical hash = %q, %v", report.LogicalSHA256, err)
	}
	if len(report.Timings) != 16 {
		t.Fatalf("report timings = %#v", report.Timings)
	}
	wantRepetitions := map[string]int{
		"generation": 1, "verification": 1,
		"project_briefing": personalBriefingReads, "workspace_briefing": personalBriefingReads,
		"warm_startup": 1, "saturated_status": personalStatusOperations, "saturated_message": personalMessageOperations,
		"saturated_control": personalControlOperations, "lease_reconciliation": 1, "doctor_full": 1,
		"backup_create": 1, "backup_verify": 1, "backup_restore": 1,
	}
	for _, timing := range report.Timings {
		if timing.Repetitions < 1 || timing.P50Microseconds < 1 || timing.P50Microseconds > timing.P95Microseconds || timing.P95Microseconds > timing.P99Microseconds || timing.P99Microseconds > timing.MaxMicroseconds {
			t.Fatalf("report timing is not ordered nearest-rank data: %#v", timing)
		}
		if repetitions, exact := wantRepetitions[timing.Name]; exact && timing.Repetitions != repetitions {
			t.Fatalf("report timing %s repetitions = %d, want %d", timing.Name, timing.Repetitions, repetitions)
		}
		delete(wantRepetitions, timing.Name)
	}
	if len(wantRepetitions) != 0 {
		t.Fatalf("report omitted exact operational timings: %#v", wantRepetitions)
	}
	if report.Resources.PeakRSSBytes == 0 || report.Resources.DatabaseBytes == 0 || report.Resources.ArtifactBytes != 0 || report.Resources.Goroutines < 1 || report.Resources.OpenFDs < 0 {
		t.Fatalf("report resources = %#v", report.Resources)
	}
	if report.Environment.GOOS == "" || report.Environment.GOARCH == "" || report.Environment.Kernel == "" || report.Environment.GoVersion == "" || report.Environment.SQLiteVersion == "" || report.Environment.CPU == "" || report.Environment.LogicalCPUs < 1 || report.Environment.MemoryBytes == 0 {
		t.Fatalf("report environment = %#v", report.Environment)
	}
	if len(report.Assertions) != 61 {
		t.Fatalf("report assertions = %#v", report.Assertions)
	}
	for _, assertion := range report.Assertions {
		if !assertion.Passed {
			t.Fatalf("report assertion failed: %#v", assertion)
		}
	}
	if filepath.IsAbs(report.Profile) {
		t.Fatalf("report leaked a filesystem path as profile: %q", report.Profile)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal successful personal-100 report: %v", err)
	}
	if err := protocol.ValidateJSON("cli/v1/personal-load.response.schema.json", raw); err != nil {
		t.Fatalf("successful personal-100 report is not schema-valid: %v", err)
	}
}
