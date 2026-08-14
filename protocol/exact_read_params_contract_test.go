package protocol_test

import (
	"encoding/json"
	"testing"

	"crewfold/internal/localapi"
	protocolschema "crewfold/protocol"
)

func TestExactReadParameterSchemasRequireTheirEntitySelectors(t *testing.T) {
	t.Parallel()

	hex := "00000000000000000000000000000001"
	tests := []struct {
		name     string
		path     string
		selector string
		value    any
	}{
		{name: "agent show", path: "local/v1/agent-query.params.schema.json", selector: "agent", value: localapi.AgentQueryParams{Workspace: "ws_" + hex, Agent: "engine-impl"}},
		{name: "objective show", path: "local/v1/objective-query.params.schema.json", selector: "objective", value: localapi.ObjectiveQueryParams{Workspace: "personal", Objective: "obj_" + hex}},
		{name: "task show", path: "local/v1/task-query.params.schema.json", selector: "task", value: localapi.TaskQueryParams{Workspace: "personal", Task: "task_" + hex}},
		{name: "run show", path: "local/v1/run-query.params.schema.json", selector: "run", value: localapi.RunQueryParams{Workspace: "personal", Run: "run_" + hex}},
		{name: "approval inspect", path: "local/v1/approval-query.params.schema.json", selector: "approval", value: localapi.ApprovalQueryParams{Workspace: "personal", Approval: "appr_" + hex}},
		{name: "check inspect", path: "local/v1/check-query.params.schema.json", selector: "check_run", value: localapi.CheckQueryParams{Workspace: "personal", CheckRun: "checkrun_" + hex}},
		{name: "overlap inspect", path: "local/v1/overlap-inspect.params.schema.json", selector: "overlap", value: localapi.OverlapInspectParams{Workspace: "personal", Overlap: "overlap_" + hex}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := protocolschema.ValidateJSON(test.path, raw); err != nil {
				t.Fatalf("valid exact params rejected: %v", err)
			}
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			if _, exists := object[test.selector]; !exists {
				t.Fatalf("%T omits exact selector %q from its wire shape", test.value, test.selector)
			}
			if err := protocolschema.ValidateJSON(test.path, []byte(`{"workspace":"personal"}`)); err == nil {
				t.Fatalf("%s accepted omitted selector %q", test.path, test.selector)
			}
		})
	}
}

func TestOverlapInspectAndScanPublishDisjointParameterContracts(t *testing.T) {
	t.Parallel()

	inspect := []byte(`{"workspace":"personal","overlap":"overlap_00000000000000000000000000000001"}`)
	if err := protocolschema.ValidateJSON("local/v1/overlap-inspect.params.schema.json", inspect); err != nil {
		t.Fatalf("overlap inspect params rejected: %v", err)
	}
	if err := protocolschema.ValidateJSON("local/v1/overlap-scan.params.schema.json", inspect); err == nil {
		t.Fatal("overlap scan accepted an inspect-only overlap selector")
	}

	scan := []byte(`{"workspace":"personal","project":"world-engine"}`)
	if err := protocolschema.ValidateJSON("local/v1/overlap-scan.params.schema.json", scan); err != nil {
		t.Fatalf("overlap scan params rejected: %v", err)
	}
	if err := protocolschema.ValidateJSON("local/v1/overlap-inspect.params.schema.json", scan); err == nil {
		t.Fatal("overlap inspect accepted a scan-only project filter")
	}
}
