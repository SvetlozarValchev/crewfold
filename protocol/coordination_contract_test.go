package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestCoordinationResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()

	for path, expectedID := range map[string]string{
		"schemas/local/v1/agent-mutation.result.schema.json":      localapi.AgentMutationSchema,
		"schemas/local/v1/agent-show.result.schema.json":          localapi.AgentShowSchema,
		"schemas/local/v1/agent-list.result.schema.json":          localapi.AgentListSchema,
		"schemas/local/v1/objective-mutation.result.schema.json":  localapi.ObjectiveMutationSchema,
		"schemas/local/v1/objective-show.result.schema.json":      localapi.ObjectiveShowSchema,
		"schemas/local/v1/objective-list.result.schema.json":      localapi.ObjectiveListSchema,
		"schemas/local/v1/task-mutation.result.schema.json":       localapi.TaskMutationSchema,
		"schemas/local/v1/task-show.result.schema.json":           localapi.TaskShowSchema,
		"schemas/local/v1/task-list.result.schema.json":           localapi.TaskListSchema,
		"schemas/local/v1/task-timeline.result.schema.json":       localapi.TaskTimelineSchema,
		"schemas/local/v1/run-mutation.result.schema.json":        localapi.RunMutationSchema,
		"schemas/local/v1/run-show.result.schema.json":            localapi.RunShowSchema,
		"schemas/local/v1/run-list.result.schema.json":            localapi.RunListSchema,
		"schemas/local/v1/coordination-status.result.schema.json": localapi.CoordinationStatusSchema,
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

func TestCoordinationMethodNamesRemainProviderNeutral(t *testing.T) {
	t.Parallel()

	methods := []string{
		localapi.MethodAgentCreate,
		localapi.MethodAgentUpdate,
		localapi.MethodAgentShow,
		localapi.MethodAgentList,
		localapi.MethodObjectiveCreate,
		localapi.MethodObjectiveUpdate,
		localapi.MethodObjectiveShow,
		localapi.MethodObjectiveList,
		localapi.MethodTaskCreate,
		localapi.MethodTaskUpdate,
		localapi.MethodTaskShow,
		localapi.MethodTaskList,
		localapi.MethodTaskDepend,
		localapi.MethodTaskAssign,
		localapi.MethodTaskTransition,
		localapi.MethodTaskTimeline,
		localapi.MethodRunStart,
		localapi.MethodRunShow,
		localapi.MethodRunList,
		localapi.MethodRunResume,
		localapi.MethodCoordinationStatus,
	}
	for _, method := range methods {
		if method == "" {
			t.Fatal("coordination method name is empty")
		}
		for _, forbidden := range []string{"codex", "claude", "herdr", "worktree"} {
			if contains(method, forbidden) {
				t.Errorf("method %q embeds provider/runtime/source-layout term %q", method, forbidden)
			}
		}
	}
}

func TestRunListPublishesBoundedSummaryShape(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/local/v1/run-list.result.schema.json")
	properties := document["properties"].(map[string]any)
	runs := properties["runs"].(map[string]any)
	if runs["maxItems"] != float64(200) {
		t.Fatalf("run list maxItems = %v, want 200", runs["maxItems"])
	}
	items := runs["items"].(map[string]any)
	if items["$ref"] != "../../domain/v1/run-summary.schema.json" {
		t.Fatalf("run list item reference = %v", items["$ref"])
	}
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
