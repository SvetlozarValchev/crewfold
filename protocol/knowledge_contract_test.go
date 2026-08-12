package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestKnowledgeSchemaIDsMatchPublishedContract(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"schemas/domain/v1/knowledge-item.schema.json":             "urn:crewfold:schema:domain:knowledge-item:v1",
		"schemas/domain/v1/knowledge-source.schema.json":           "urn:crewfold:schema:domain:knowledge-source:v1",
		"schemas/domain/v1/knowledge-revision.schema.json":         "urn:crewfold:schema:domain:knowledge-revision:v1",
		"schemas/domain/v1/knowledge-authority.schema.json":        "urn:crewfold:schema:domain:knowledge-authority:v1",
		"schemas/domain/v1/knowledge-detail.schema.json":           "urn:crewfold:schema:domain:knowledge-detail:v1",
		"schemas/domain/v1/knowledge-list.schema.json":             "urn:crewfold:schema:domain:knowledge-list:v1",
		"schemas/local/v1/knowledge-propose.params.schema.json":    "urn:crewfold:schema:local-api:knowledge-propose-params:v1",
		"schemas/local/v1/knowledge-show.params.schema.json":       "urn:crewfold:schema:local-api:knowledge-show-params:v1",
		"schemas/local/v1/knowledge-list.params.schema.json":       "urn:crewfold:schema:local-api:knowledge-list-params:v1",
		"schemas/local/v1/knowledge-accept.params.schema.json":     "urn:crewfold:schema:local-api:knowledge-accept-params:v1",
		"schemas/local/v1/knowledge-reject.params.schema.json":     "urn:crewfold:schema:local-api:knowledge-reject-params:v1",
		"schemas/local/v1/knowledge-mark-stale.params.schema.json": "urn:crewfold:schema:local-api:knowledge-mark-stale-params:v1",
		"schemas/local/v1/knowledge-mutation.result.schema.json":   localapi.KnowledgeMutationSchema,
		"schemas/local/v1/knowledge-show.result.schema.json":       localapi.KnowledgeShowSchema,
		"schemas/local/v1/knowledge-list.result.schema.json":       localapi.KnowledgeListSchema,
	}
	for path, expectedID := range want {
		document := readKnowledgeSchema(t, path)
		if document.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, document.ID, expectedID)
		}
	}
}

func TestKnowledgeRevisionPublishesIndependentReviewAndCurrencyAxes(t *testing.T) {
	t.Parallel()

	document := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-revision.schema.json")
	if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
		t.Fatalf("knowledge revision must be a strict object: %#v", document)
	}
	assertKnowledgeEnum(t, document.Properties["type"], []string{"decision", "finding"})
	assertKnowledgeEnum(t, document.Properties["review_status"], []string{"proposed", "accepted", "rejected"})
	assertKnowledgeEnum(t, document.Properties["currency_status"], []string{"pending", "current", "stale", "superseded"})
	assertKnowledgeEnum(t, document.Properties["freshness_policy"], []string{"until_superseded", "expires_at"})
	for _, field := range []string{"id", "item_id", "revision_number", "state_revision", "content_hash", "sources"} {
		if !containsKnowledgeString(document.Required, field) {
			t.Errorf("knowledge revision required fields omit %q", field)
		}
	}

	var sources struct {
		MinItems    int             `json:"minItems"`
		MaxItems    int             `json:"maxItems"`
		MinContains int             `json:"minContains"`
		MaxContains int             `json:"maxContains"`
		Contains    json.RawMessage `json:"contains"`
	}
	if err := json.Unmarshal(document.Properties["sources"], &sources); err != nil {
		t.Fatalf("decode knowledge revision sources: %v", err)
	}
	if sources.MinItems != 1 || sources.MaxItems != 16 || sources.MinContains != 1 || sources.MaxContains != 1 || len(sources.Contains) == 0 {
		t.Errorf("knowledge revision source bounds = %#v, want 1..16 with exactly one primary", sources)
	}
}

func TestKnowledgeIdentifiersAreStableAndAuthorityIsInspectable(t *testing.T) {
	t.Parallel()

	for path, propertyAndPattern := range map[string][2]string{
		"schemas/domain/v1/knowledge-item.schema.json":      {"id", `^know_[0-9a-f]{32}$`},
		"schemas/domain/v1/knowledge-revision.schema.json":  {"id", `^krev_[0-9a-f]{32}$`},
		"schemas/domain/v1/knowledge-authority.schema.json": {"id", `^kauth_[0-9a-f]{32}$`},
	} {
		document := readKnowledgeSchema(t, path)
		var property struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(document.Properties[propertyAndPattern[0]], &property); err != nil {
			t.Fatalf("decode %q identifier: %v", path, err)
		}
		if property.Pattern != propertyAndPattern[1] {
			t.Errorf("schema %q identifier pattern = %q, want %q", path, property.Pattern, propertyAndPattern[1])
		}
	}

	show := readKnowledgeSchema(t, "schemas/local/v1/knowledge-show.result.schema.json")
	if !containsKnowledgeString(show.Required, "detail") {
		t.Fatal("knowledge show result does not require detail")
	}
	var detailReference struct {
		Reference string `json:"$ref"`
	}
	if err := json.Unmarshal(show.Properties["detail"], &detailReference); err != nil {
		t.Fatalf("decode knowledge show detail reference: %v", err)
	}
	if detailReference.Reference != "../../domain/v1/knowledge-detail.schema.json" {
		t.Errorf("knowledge show detail reference = %q", detailReference.Reference)
	}

	detail := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-detail.schema.json")
	for _, field := range []string{"revision", "authority_checks"} {
		if !containsKnowledgeString(detail.Required, field) {
			t.Errorf("knowledge detail does not require %q", field)
		}
	}

	list := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-list.schema.json")
	var revisions struct {
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(list.Properties["revisions"], &revisions); err != nil {
		t.Fatalf("decode bounded knowledge list: %v", err)
	}
	if revisions.MaxItems != 200 {
		t.Errorf("knowledge list maxItems = %d, want 200", revisions.MaxItems)
	}
}

func TestKnowledgeMutationParamsNeverAcceptCallerSelectedActor(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"schemas/local/v1/knowledge-propose.params.schema.json",
		"schemas/local/v1/knowledge-accept.params.schema.json",
		"schemas/local/v1/knowledge-reject.params.schema.json",
		"schemas/local/v1/knowledge-mark-stale.params.schema.json",
	} {
		document := readKnowledgeSchema(t, path)
		if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Errorf("schema %q is not a strict request object", path)
		}
		for _, forbidden := range []string{"actor", "actor_id", "actor_type", "accepted_by", "rejected_by", "stale_by"} {
			if _, exists := document.Properties[forbidden]; exists {
				t.Errorf("schema %q exposes trusted field %q", path, forbidden)
			}
		}
	}

	for _, path := range []string{
		"schemas/local/v1/knowledge-accept.params.schema.json",
		"schemas/local/v1/knowledge-reject.params.schema.json",
		"schemas/local/v1/knowledge-mark-stale.params.schema.json",
	} {
		document := readKnowledgeSchema(t, path)
		if !containsKnowledgeString(document.Required, "expected_state_revision") {
			t.Errorf("schema %q does not require optimistic state revision", path)
		}
	}
}

func TestKnowledgeSourceTypesAreBoundedAndRevisionPinned(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("schemas/domain/v1/knowledge-source.schema.json")
	if err != nil {
		t.Fatalf("read knowledge source schema: %v", err)
	}
	var document struct {
		Definitions map[string]knowledgeSchemaDocument `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode knowledge source schema: %v", err)
	}
	resolved := document.Definitions["source"]
	input := document.Definitions["sourceInput"]
	assertKnowledgeEnum(t, resolved.Properties["type"], []string{"task", "meeting", "meeting_proposal"})
	assertKnowledgeEnum(t, resolved.Properties["role"], []string{"primary", "supporting"})
	if !containsKnowledgeString(resolved.Required, "revision") || !containsKnowledgeString(resolved.Required, "ordinal") {
		t.Errorf("resolved source is not revision-pinned and ordered: %v", resolved.Required)
	}
	if _, exists := input.Properties["revision"]; exists {
		t.Error("proposal source input lets callers select the authoritative source revision")
	}
}

type knowledgeSchemaDocument struct {
	ID                   string                     `json:"$id"`
	Type                 string                     `json:"type"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
}

func readKnowledgeSchema(t *testing.T, path string) knowledgeSchemaDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	var document knowledgeSchemaDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
	}
	return document
}

func assertKnowledgeEnum(t *testing.T, property json.RawMessage, want []string) {
	t.Helper()
	var decoded struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(property, &decoded); err != nil {
		t.Fatalf("decode knowledge enum: %v", err)
	}
	if len(decoded.Enum) != len(want) {
		t.Fatalf("knowledge enum = %v, want %v", decoded.Enum, want)
	}
	for index := range want {
		if decoded.Enum[index] != want[index] {
			t.Fatalf("knowledge enum = %v, want %v", decoded.Enum, want)
		}
	}
}

func containsKnowledgeString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
