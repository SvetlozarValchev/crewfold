package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/execution"
	"crewfold/internal/herdr"
	"crewfold/internal/localapi"
)

func TestExecutionSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()

	for path, expectedID := range map[string]string{
		"schemas/fixture/v1/fake-run-scenario.schema.json":           execution.FakeScenarioSchema,
		"schemas/local/v1/run-mutation.result.schema.json":           localapi.RunMutationSchema,
		"schemas/local/v1/run-show.result.schema.json":               localapi.RunShowSchema,
		"schemas/local/v1/run-list.result.schema.json":               localapi.RunListSchema,
		"schemas/local/v1/run-logs.result.schema.json":               localapi.RunLogsSchema,
		"schemas/local/v1/run-control.result.schema.json":            localapi.RunControlSchema,
		"schemas/local/v1/run-attach.result.schema.json":             localapi.RunAttachSchema,
		"schemas/cli/v1/doctor-runtime.response.schema.json":         herdr.ProbeSchema,
		"schemas/cli/v1/doctor-provider.response.schema.json":        execution.CodexProbeSchema,
		"schemas/cli/v1/doctor-claude-provider.response.schema.json": execution.ClaudeProbeSchema,
		"schemas/local/v1/task-timeline.result.schema.json":          localapi.TaskTimelineSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if header.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, header.ID, expectedID)
		}
	}
}

func TestCheckedInExecutionScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/execution/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 5 {
		t.Fatalf("execution scenario count = %d, want 5; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInDirectRuntimeScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/direct-runtime/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 8 {
		t.Fatalf("direct-runtime scenario count = %d, want 8; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInAgentMessagingScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/agent-messaging/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("agent messaging scenario count = %d, want 2; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInCrossProjectCollaborationScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/cross-project-collaboration/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("cross-project scenario count = %d, want 2; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
	templatePath := "../test/fixtures/cross-project-collaboration/engine.json.in"
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(data), "THREAD_ID", "thread_00000000000000000000000000000001")
	renderedPath := filepath.Join(t.TempDir(), "engine.json")
	if err := os.WriteFile(renderedPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.LoadScenario(renderedPath); err != nil {
		t.Errorf("execution.LoadScenario(rendered engine fixture) error = %v", err)
	}
}
