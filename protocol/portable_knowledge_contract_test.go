package protocol_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestPortableKnowledgeSchemaIDsMatchPublicTypes(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"schemas/portable/v1/knowledge-bundle-manifest.schema.json": domain.PortableKnowledgeBundleManifestSchema,
		"schemas/portable/v1/knowledge-import-receipt.schema.json":  "urn:crewfold:schema:portable:knowledge-import-receipt:v1",
		"schemas/local/v1/knowledge-export.params.schema.json":      "urn:crewfold:schema:local-api:knowledge-export-params:v1",
		"schemas/local/v1/knowledge-import.params.schema.json":      "urn:crewfold:schema:local-api:knowledge-import-params:v1",
		"schemas/local/v1/knowledge-export.result.schema.json":      localapi.KnowledgeExportSchema,
		"schemas/local/v1/knowledge-import.result.schema.json":      localapi.KnowledgeImportSchema,
	}
	for path, expected := range want {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil || header.ID != expected {
			t.Errorf("schema %s id=%q error=%v, want %q", path, header.ID, err, expected)
		}
	}
}

func TestPortableKnowledgeRequestsAreStrictScopedAndNonAuthoritative(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/local/v1/knowledge-export.params.schema.json",
		"schemas/local/v1/knowledge-import.params.schema.json",
	} {
		document := readKnowledgeSchema(t, path)
		if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Fatalf("%s is not a strict object", path)
		}
		for _, field := range []string{"workspace", "project", "directory"} {
			if !containsKnowledgeString(document.Required, field) {
				t.Errorf("%s does not require %s", path, field)
			}
		}
		for _, forbidden := range []string{"actor", "provider", "manifest", "markdown", "bundle_bytes"} {
			if _, exists := document.Properties[forbidden]; exists {
				t.Errorf("%s exposes forbidden field %s", path, forbidden)
			}
		}
	}
	importSchema := readKnowledgeSchema(t, "schemas/local/v1/knowledge-import.params.schema.json")
	for _, field := range []string{"expected_content_sha256", "create_scope", "idempotency_key"} {
		if !containsKnowledgeString(importSchema.Required, field) {
			t.Errorf("import does not require %s", field)
		}
	}
}

func TestPortableManifestIsStrictBoundedAndContainsNoLocalRuntimeState(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/portable/v1/knowledge-bundle-manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"event_sequence\"", "authority_checks", "context_packet", "transcript", "credential", "provider_state", "fts", "embedding"} {
		if containsFold(text, forbidden) {
			t.Errorf("portable manifest schema leaks local/derived field %q", forbidden)
		}
	}
	var schema struct {
		AdditionalProperties *bool `json:"additionalProperties"`
		Definitions          map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatal("portable manifest envelope must reject unknown fields")
	}
	counts := schema.Definitions["counts"].Properties
	for name, wantMaximum := range map[string]int{"task_scope_anchors": 4096, "items": 4096, "revisions": 16384, "contradictions": 8192} {
		var bounds struct {
			Maximum int `json:"maximum"`
		}
		if err := json.Unmarshal(counts[name], &bounds); err != nil || bounds.Maximum != wantMaximum {
			t.Errorf("counts.%s maximum=%d error=%v, want %d", name, bounds.Maximum, err, wantMaximum)
		}
	}
}

func TestPortableLocalResultsExposeFlatDigestsCountsAndReceipts(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/local/v1/knowledge-export.result.schema.json",
		"schemas/local/v1/knowledge-import.result.schema.json",
	} {
		document := readKnowledgeSchema(t, path)
		if document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Fatalf("%s is not a strict result", path)
		}
		for _, field := range []string{"directory", "bundle_id", "content_sha256", "manifest_sha256", "manifest_bytes", "markdown_sha256", "markdown_bytes", "counts"} {
			if !containsKnowledgeString(document.Required, field) {
				t.Errorf("%s does not require %s", path, field)
			}
		}
		for _, nested := range []string{"manifest", "markdown", "bundle_sha256"} {
			if _, exists := document.Properties[nested]; exists {
				t.Errorf("%s exposes unfrozen result field %s", path, nested)
			}
		}
	}
	importResult := readKnowledgeSchema(t, "schemas/local/v1/knowledge-import.result.schema.json")
	for _, field := range []string{"receipt", "status", "created", "event_sequence"} {
		if !containsKnowledgeString(importResult.Required, field) {
			t.Errorf("import result does not require %s", field)
		}
	}
}

func containsFold(text, fragment string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(fragment))
}
