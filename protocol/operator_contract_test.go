package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestOperatorReadSchemasPublishCurrentBoundedSurface(t *testing.T) {
	t.Parallel()
	results := map[string]string{
		"schemas/local/v1/workspace-list.result.schema.json":  localapi.WorkspaceListSchema,
		"schemas/local/v1/project-show.result.schema.json":    localapi.ProjectShowSchema,
		"schemas/local/v1/project-list.result.schema.json":    localapi.ProjectListSchema,
		"schemas/local/v1/agent-list.result.schema.json":      localapi.AgentListSchema,
		"schemas/local/v1/objective-list.result.schema.json":  localapi.ObjectiveListSchema,
		"schemas/local/v1/task-list.result.schema.json":       localapi.TaskListSchema,
		"schemas/local/v1/run-list.result.schema.json":        localapi.RunListSchema,
		"schemas/local/v1/claim-list.result.schema.json":      localapi.ClaimListSchema,
		"schemas/local/v1/overlap-list.result.schema.json":    localapi.OverlapListSchema,
		"schemas/local/v1/drift-list.result.schema.json":      localapi.DriftListSchema,
		"schemas/local/v1/meeting-list.result.schema.json":    localapi.MeetingListSchema,
		"schemas/local/v1/approval-list.result.schema.json":   localapi.ApprovalListSchema,
		"schemas/local/v1/check-run-list.result.schema.json":  localapi.CheckRunListSchema,
		"schemas/local/v1/events-list.result.schema.json":     localapi.EventsListSchema,
		"schemas/local/v1/events-timeline.result.schema.json": localapi.EventsTimelineSchema,
	}
	for path, schemaID := range results {
		document := readOperatorSchema(t, path)
		if document["$id"] != schemaID {
			t.Errorf("%s id = %v, want %s", path, document["$id"], schemaID)
		}
		properties := document["properties"].(map[string]any)
		for _, field := range []string{"next_cursor", "has_more", "total"} {
			if _, exists := properties[field]; !exists && path != "schemas/local/v1/project-show.result.schema.json" {
				t.Errorf("%s omits page field %s", path, field)
			}
		}
	}

	for _, path := range []string{
		"schemas/local/v1/workspace-list.params.schema.json",
		"schemas/local/v1/project-list.params.schema.json",
		"schemas/local/v1/agent-list.params.schema.json",
		"schemas/local/v1/objective-list.params.schema.json",
		"schemas/local/v1/task-list.params.schema.json",
		"schemas/local/v1/run-list.params.schema.json",
		"schemas/local/v1/claim-list.params.schema.json",
		"schemas/local/v1/overlap-list.params.schema.json",
		"schemas/local/v1/drift-list.params.schema.json",
		"schemas/local/v1/meeting-list.params.schema.json",
		"schemas/local/v1/approval-list.params.schema.json",
		"schemas/local/v1/check-list.params.schema.json",
		"schemas/local/v1/events-list.params.schema.json",
		"schemas/local/v1/events-timeline.params.schema.json",
	} {
		document := readOperatorSchema(t, path)
		properties := document["properties"].(map[string]any)
		if _, exists := properties["limit"]; !exists {
			t.Errorf("%s omits limit", path)
		}
		if _, exists := properties["cursor"]; !exists {
			t.Errorf("%s omits cursor", path)
		}
	}
}

func readOperatorSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}
