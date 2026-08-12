package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestContextSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expectedID := range map[string]string{
		"schemas/domain/v1/context-packet.schema.json":         domain.ContextPacketSchemaV1,
		"schemas/domain/v1/context-packet-v2.schema.json":      domain.ContextPacketSchema,
		"schemas/local/v1/context-build-v2.result.schema.json": localapi.ContextBuildSchema,
		"schemas/local/v1/context-show-v2.result.schema.json":  localapi.ContextShowSchema,
		"schemas/local/v1/context-explain.result.schema.json":  localapi.ContextExplainSchema,
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

func TestScopedMCPErrorVocabularyIsBounded(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/mcp/v1/tool-error.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(tool error schema) error = %v", err)
	}
	var document struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("json.Unmarshal(tool error schema) error = %v", err)
	}
	want := []string{"invalid_input", "out_of_scope", "denied_by_policy", "temporarily_unavailable"}
	if got := document.Properties["code"].Enum; len(got) != len(want) {
		t.Fatalf("tool error codes = %v, want %v", got, want)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("tool error codes = %v, want %v", got, want)
			}
		}
	}
}
