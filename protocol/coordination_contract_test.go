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

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
