package protocol_test

import (
	"os"
	"reflect"
	"testing"
)

func TestCheckWatchMCPInputsAreStrictAndDerivedScope(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		"run-check":            {"requirement_id", "idempotency_key"},
		"list-check-results":   {"limit"},
		"inspect-check-result": {"check_run_id"},
		"propose-check-repair": {"check_result_id", "rationale", "idempotency_key"},
	}
	for name, requiredFields := range want {
		document := readContextSchema(t, "schemas/mcp/v1/"+name+".input.schema.json")
		if document["additionalProperties"] != false {
			t.Errorf("%s input permits undeclared fields", name)
		}
		properties := document["properties"].(map[string]any)
		for _, forbidden := range []string{
			"workspace", "project", "agent", "run", "source_run_id", "grant_id", "expected_grant_revision",
			"checkout", "command", "executable", "arguments", "environment", "profile", "evidence_class", "recipient",
		} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s input exposes trusted scope field %q", name, forbidden)
			}
		}
		required := contractStringSlice(document["required"])
		if !reflect.DeepEqual(required, requiredFields) {
			t.Errorf("%s required fields = %v, want %v", name, required, requiredFields)
		}
	}
	if _, err := os.Stat("schemas/mcp/v1/accept-check-repair.input.schema.json"); !os.IsNotExist(err) {
		t.Fatal("reserved check-repair acceptance must not be published as an MCP input schema")
	}
}

func TestCheckWatchMCPOutputsRemainBoundedTypedViews(t *testing.T) {
	t.Parallel()
	refs := map[string]string{
		"run-check.output.schema.json":            "../../domain/v1/check-run.schema.json",
		"inspect-check-result.output.schema.json": "../../domain/v1/check-run-detail.schema.json",
		"propose-check-repair.output.schema.json": "../../domain/v1/check-repair-proposal.schema.json",
	}
	for name, wantRef := range refs {
		document := readContextSchema(t, "schemas/mcp/v1/"+name)
		if document["$ref"] != wantRef {
			t.Errorf("%s ref = %v, want %q", name, document["$ref"], wantRef)
		}
	}
	list := readContextSchema(t, "schemas/mcp/v1/list-check-results.output.schema.json")
	properties := list["properties"].(map[string]any)
	items := properties["items"].(map[string]any)
	if items["maxItems"] != float64(50) || items["items"].(map[string]any)["$ref"] != "../../domain/v1/check-run-list-item.schema.json" {
		t.Fatalf("list-check-results output is not a bounded typed page: %#v", items)
	}
	cursor := properties["next_cursor"].(map[string]any)
	if cursor["maxLength"] != float64(256) {
		t.Fatalf("list-check-results cursor bound = %v", cursor["maxLength"])
	}
}
