package protocol_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestKnowledgeSchemaIDsMatchPublishedContract(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"schemas/domain/v1/knowledge-item.schema.json":                "urn:crewfold:schema:domain:knowledge-item:v1",
		"schemas/domain/v1/knowledge-source.schema.json":              "urn:crewfold:schema:domain:knowledge-source:v1",
		"schemas/domain/v1/knowledge-revision.schema.json":            "urn:crewfold:schema:domain:knowledge-revision:v1",
		"schemas/domain/v1/knowledge-authority.schema.json":           "urn:crewfold:schema:domain:knowledge-authority:v1",
		"schemas/domain/v1/knowledge-producer.schema.json":            "urn:crewfold:schema:domain:knowledge-producer:v1",
		"schemas/domain/v1/knowledge-evidence.schema.json":            "urn:crewfold:schema:domain:knowledge-evidence:v1",
		"schemas/domain/v1/knowledge-presentation.schema.json":        "urn:crewfold:schema:domain:knowledge-presentation:v1",
		"schemas/domain/v1/knowledge-detail.schema.json":              "urn:crewfold:schema:domain:knowledge-detail:v1",
		"schemas/domain/v1/knowledge-list.schema.json":                "urn:crewfold:schema:domain:knowledge-list:v1",
		"schemas/local/v1/knowledge-propose.params.schema.json":       "urn:crewfold:schema:local-api:knowledge-propose-params:v1",
		"schemas/local/v1/knowledge-show.params.schema.json":          "urn:crewfold:schema:local-api:knowledge-show-params:v1",
		"schemas/local/v1/knowledge-list.params.schema.json":          "urn:crewfold:schema:local-api:knowledge-list-params:v1",
		"schemas/local/v1/knowledge-accept.params.schema.json":        "urn:crewfold:schema:local-api:knowledge-accept-params:v1",
		"schemas/local/v1/knowledge-reject.params.schema.json":        "urn:crewfold:schema:local-api:knowledge-reject-params:v1",
		"schemas/local/v1/knowledge-mark-stale.params.schema.json":    "urn:crewfold:schema:local-api:knowledge-mark-stale-params:v1",
		"schemas/local/v1/knowledge-mutation.result.schema.json":      localapi.KnowledgeMutationSchema,
		"schemas/local/v1/knowledge-show.result.schema.json":          localapi.KnowledgeShowSchema,
		"schemas/local/v1/knowledge-list.result.schema.json":          localapi.KnowledgeListSchema,
		"schemas/local/v1/knowledge-search.params.schema.json":        "urn:crewfold:schema:local-api:knowledge-search-params:v1",
		"schemas/local/v1/knowledge-search.result.schema.json":        localapi.KnowledgeSearchSchema,
		"schemas/local/v1/knowledge-index-status.params.schema.json":  "urn:crewfold:schema:local-api:knowledge-index-status-params:v1",
		"schemas/local/v1/knowledge-index-status.result.schema.json":  localapi.KnowledgeIndexStatusSchema,
		"schemas/local/v1/knowledge-index-rebuild.params.schema.json": "urn:crewfold:schema:local-api:knowledge-index-rebuild-params:v1",
		"schemas/local/v1/knowledge-index-rebuild.result.schema.json": localapi.KnowledgeIndexRebuildSchema,
	}
	for path, expectedID := range want {
		document := readKnowledgeSchema(t, path)
		if document.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, document.ID, expectedID)
		}
	}
}

func TestKnowledgeRetrievalRequestsAreStrictScopedAndNonAuthoritative(t *testing.T) {
	t.Parallel()

	search := readKnowledgeSchema(t, "schemas/local/v1/knowledge-search.params.schema.json")
	if search.Type != "object" || search.AdditionalProperties == nil || *search.AdditionalProperties {
		t.Fatalf("knowledge search must be a strict request object: %#v", search)
	}
	for _, field := range []string{"workspace", "project", "query"} {
		if !containsKnowledgeString(search.Required, field) {
			t.Errorf("knowledge search does not require hard-scope field %q", field)
		}
	}
	for _, forbidden := range []string{"review_status", "currency_status", "accepted", "actor", "auto_accept"} {
		if _, exists := search.Properties[forbidden]; exists {
			t.Errorf("knowledge search exposes authority control %q", forbidden)
		}
	}
	var queryBounds struct {
		MinLength int `json:"minLength"`
		MaxLength int `json:"maxLength"`
	}
	if err := json.Unmarshal(search.Properties["query"], &queryBounds); err != nil || queryBounds.MinLength != 1 || queryBounds.MaxLength != 256 {
		t.Errorf("knowledge search query bounds = %#v, error=%v; want 1..256", queryBounds, err)
	}
	var limitBounds struct {
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(search.Properties["limit"], &limitBounds); err != nil || limitBounds.Minimum != 1 || limitBounds.Maximum != 100 {
		t.Errorf("knowledge search limit bounds = %#v, error=%v; want 1..100", limitBounds, err)
	}
	if containsKnowledgeString(search.Required, "limit") {
		t.Error("knowledge search requires limit instead of allowing the deterministic default")
	}
	omittedLimit, err := json.Marshal(localapi.KnowledgeSearchParams{Workspace: "personal", Project: "demo", Query: "term"})
	if err != nil || strings.Contains(string(omittedLimit), `"limit"`) {
		t.Errorf("knowledge search omitted limit encoding = %s, error=%v", omittedLimit, err)
	}
	zero := 0
	explicitZero, err := json.Marshal(localapi.KnowledgeSearchParams{Workspace: "personal", Project: "demo", Query: "term", Limit: &zero})
	if err != nil || !strings.Contains(string(explicitZero), `"limit":0`) {
		t.Errorf("knowledge search explicit zero encoding = %s, error=%v", explicitZero, err)
	}

	rebuild := readKnowledgeSchema(t, "schemas/local/v1/knowledge-index-rebuild.params.schema.json")
	if rebuild.AdditionalProperties == nil || *rebuild.AdditionalProperties || !containsKnowledgeString(rebuild.Required, "idempotency_key") {
		t.Fatalf("knowledge index rebuild is not strict and idempotent: %#v", rebuild)
	}
	for _, forbidden := range []string{"revision", "body", "review_status", "currency_status"} {
		if _, exists := rebuild.Properties[forbidden]; exists {
			t.Errorf("knowledge index rebuild can mutate canonical field %q", forbidden)
		}
	}
}

func TestKnowledgeRetrievalResultsExposeExactRevisionsRankingAndIndexHealth(t *testing.T) {
	t.Parallel()

	status := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-index-status.schema.json")
	if status.Type != "object" || status.AdditionalProperties == nil || *status.AdditionalProperties {
		t.Fatalf("knowledge index status must be strict: %#v", status)
	}
	assertKnowledgeEnum(t, status.Properties["status"], []string{"ok", "degraded"})
	for _, field := range []string{"status", "generation", "source_event_sequence", "source_count"} {
		if !containsKnowledgeString(status.Required, field) {
			t.Errorf("knowledge index status omits %q", field)
		}
	}

	data, err := os.ReadFile("schemas/domain/v1/knowledge-search.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]knowledgeSchemaDocument `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	result := document.Definitions["result"]
	match := document.Definitions["match"]
	explanation := document.Definitions["explanation"]
	for _, field := range []string{"normalized_query", "evaluated_at", "canonical_event_sequence", "rank_policy", "index", "matches"} {
		if !containsKnowledgeString(result.Required, field) {
			t.Errorf("knowledge search result omits %q", field)
		}
	}
	for _, field := range []string{"ordinal", "revision", "explanation"} {
		if !containsKnowledgeString(match.Required, field) {
			t.Errorf("knowledge search match omits %q", field)
		}
	}
	for _, field := range []string{"scope", "authority", "freshness", "provenance", "quality", "text", "tie_breaker"} {
		if !containsKnowledgeString(explanation.Required, field) {
			t.Errorf("knowledge search explanation omits %q", field)
		}
	}
	for definition, fieldAndMaximum := range map[string]struct {
		field   string
		maximum int
	}{
		"scope":      {field: "rank", maximum: 1},
		"freshness":  {field: "class", maximum: 1},
		"provenance": {field: "rank", maximum: 4},
		"quality":    {field: "confidence_rank", maximum: 2},
	} {
		var bounds struct {
			Minimum int `json:"minimum"`
			Maximum int `json:"maximum"`
		}
		if err := json.Unmarshal(document.Definitions[definition].Properties[fieldAndMaximum.field], &bounds); err != nil || bounds.Minimum != 0 || bounds.Maximum != fieldAndMaximum.maximum {
			t.Errorf("knowledge search %s.%s bounds = %#v, error=%v", definition, fieldAndMaximum.field, bounds, err)
		}
	}
	for _, path := range []string{
		"schemas/local/v1/knowledge-search.result.schema.json",
		"schemas/local/v1/knowledge-index-status.result.schema.json",
		"schemas/local/v1/knowledge-index-rebuild.result.schema.json",
	} {
		wrapper := readKnowledgeSchema(t, path)
		if wrapper.Type != "object" || wrapper.AdditionalProperties == nil || *wrapper.AdditionalProperties {
			t.Errorf("retrieval wrapper %q is not strict", path)
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
	for _, field := range []string{"revision", "authority_checks", "presentation"} {
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
	var presentations struct {
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(list.Properties["presentations"], &presentations); err != nil {
		t.Fatalf("decode bounded knowledge presentations: %v", err)
	}
	if presentations.MaxItems != 200 {
		t.Errorf("knowledge presentations maxItems = %d, want 200", presentations.MaxItems)
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
	assertKnowledgeEnum(t, resolved.Properties["type"], []string{"task", "meeting", "meeting_proposal", "domain_agent"})
	assertKnowledgeEnum(t, input.Properties["type"], []string{"task", "meeting", "meeting_proposal"})
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
