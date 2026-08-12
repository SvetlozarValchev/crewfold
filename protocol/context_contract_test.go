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
		"schemas/domain/v1/context-packet.schema.json":           domain.ContextPacketSchemaV1,
		"schemas/domain/v1/context-packet-v2.schema.json":        domain.ContextPacketSchemaV2,
		"schemas/domain/v1/context-packet-v3.schema.json":        domain.ContextPacketSchema,
		"schemas/local/v1/context-build-v3.result.schema.json":   localapi.ContextBuildSchema,
		"schemas/local/v1/context-show-v3.result.schema.json":    localapi.ContextShowSchema,
		"schemas/local/v1/context-explain-v2.result.schema.json": localapi.ContextExplainSchema,
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

func TestContextV3PublishesBoundedExplicitKnowledgeLinks(t *testing.T) {
	t.Parallel()
	paramsData, err := os.ReadFile("schemas/local/v1/context-build-v2.params.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			MaxItems    int  `json:"maxItems"`
			UniqueItems bool `json:"uniqueItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(paramsData, &params); err != nil {
		t.Fatal(err)
	}
	knowledge := params.Properties["knowledge_revision_ids"]
	if knowledge.MaxItems != 16 || !knowledge.UniqueItems || !containsContractString(params.Required, "knowledge_revision_ids") {
		t.Fatalf("context knowledge revision parameter = %#v, required = %v", knowledge, params.Required)
	}

	packetData, err := os.ReadFile("schemas/domain/v1/context-packet-v3.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(packetData, &packet); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requested_knowledge_revision_ids", "accepted_knowledge", "budget"} {
		if !containsContractString(packet.Required, field) {
			t.Errorf("context packet v3 does not require %q", field)
		}
	}
}

func containsContractString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
