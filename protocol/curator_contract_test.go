package protocol_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

type curatorSchemaDocument struct {
	ID                   string                     `json:"$id"`
	Type                 string                     `json:"type"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Definitions          map[string]json.RawMessage `json:"$defs"`
}

func TestCuratorSchemaIDsMatchPublishedContract(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"schemas/domain/v1/curator-rule.schema.json":                 "urn:crewfold:schema:domain:curator-rule:v1",
		"schemas/domain/v1/curator-derivation.schema.json":           "urn:crewfold:schema:domain:curator-derivation:v1",
		"schemas/domain/v1/curator-auto-acceptance.schema.json":      "urn:crewfold:schema:domain:curator-auto-acceptance:v1",
		"schemas/domain/v1/curator-queue.schema.json":                "urn:crewfold:schema:domain:curator-queue:v1",
		"schemas/domain/v1/curator-process.schema.json":              "urn:crewfold:schema:domain:curator-process:v1",
		"schemas/local/v1/curator-queue.params.schema.json":          "urn:crewfold:schema:local-api:curator-queue-params:v1",
		"schemas/local/v1/curator-rule-configure.params.schema.json": "urn:crewfold:schema:local-api:curator-rule-configure-params:v1",
		"schemas/local/v1/curator-process.params.schema.json":        "urn:crewfold:schema:local-api:curator-process-params:v1",
		"schemas/local/v1/curator-queue.result.schema.json":          localapi.CuratorQueueSchema,
		"schemas/local/v1/curator-rule-mutation.result.schema.json":  localapi.CuratorRuleMutationSchema,
		"schemas/local/v1/curator-process.result.schema.json":        localapi.CuratorProcessSchema,
	}
	for path, expected := range want {
		document := readCuratorSchema(t, path)
		if document.ID != expected {
			t.Errorf("schema %q ID = %q, want %q", path, document.ID, expected)
		}
	}
}

func TestCuratorRequestsAreStrictOwnerOnlyAndBounded(t *testing.T) {
	t.Parallel()
	paths := []string{
		"schemas/local/v1/curator-queue.params.schema.json",
		"schemas/local/v1/curator-rule-configure.params.schema.json",
		"schemas/local/v1/curator-process.params.schema.json",
	}
	for _, path := range paths {
		document := readCuratorSchema(t, path)
		if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Errorf("curator request %q is not a strict object", path)
		}
		for _, forbidden := range []string{"actor", "actor_id", "actor_type", "accepted_by", "created_by", "candidate_limit", "accept_limit"} {
			if _, exposed := document.Properties[forbidden]; exposed {
				t.Errorf("curator request %q exposes trusted/internal field %q", path, forbidden)
			}
		}
	}

	queue := readCuratorSchema(t, paths[0])
	for _, field := range []string{"workspace", "project"} {
		if !containsCuratorString(queue.Required, field) {
			t.Errorf("curator queue does not require %q", field)
		}
	}
	if containsCuratorString(queue.Required, "limit") || containsCuratorString(queue.Required, "after") {
		t.Error("curator queue requires an optional pagination field")
	}
	var limit struct {
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(queue.Properties["limit"], &limit); err != nil || limit.Minimum != 1 || limit.Maximum != 200 {
		t.Errorf("curator queue limit = %#v, error=%v; want 1..200", limit, err)
	}
	var cursor struct {
		MaxLength int `json:"maxLength"`
	}
	if err := json.Unmarshal(queue.Properties["after"], &cursor); err != nil || cursor.MaxLength != 512 {
		t.Errorf("curator queue cursor = %#v, error=%v", cursor, err)
	}

	rule := readCuratorSchema(t, paths[1])
	for _, field := range []string{"workspace", "rule", "enabled", "expected_revision", "idempotency_key"} {
		if !containsCuratorString(rule.Required, field) {
			t.Errorf("curator rule configuration does not require %q", field)
		}
	}
	var ruleName struct {
		Const string `json:"const"`
	}
	if err := json.Unmarshal(rule.Properties["rule"], &ruleName); err != nil || ruleName.Const != "accepted_meeting_resolution_copy/v1" {
		t.Errorf("curator rule key = %#v, error=%v", ruleName, err)
	}
	var expectedRevision struct {
		Minimum int `json:"minimum"`
	}
	if err := json.Unmarshal(rule.Properties["expected_revision"], &expectedRevision); err != nil || expectedRevision.Minimum != 1 {
		t.Errorf("curator rule expected revision = %#v, error=%v", expectedRevision, err)
	}

	process := readCuratorSchema(t, paths[2])
	if containsCuratorString(process.Required, "apply_safe") {
		t.Error("curator process requires apply_safe instead of defaulting to derive-only")
	}
	var applySafe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(process.Properties["apply_safe"], &applySafe); err != nil || applySafe.Type != "boolean" {
		t.Errorf("curator process apply_safe = %#v, error=%v", applySafe, err)
	}
}

func TestCuratorDomainContractPinsEvidenceAuthorityAndCaps(t *testing.T) {
	t.Parallel()
	for path, propertyAndPattern := range map[string][2]string{
		"schemas/domain/v1/curator-rule.schema.json":            {"id", `^crule_[0-9a-f]{32}$`},
		"schemas/domain/v1/curator-derivation.schema.json":      {"id", `^cder_[0-9a-f]{32}$`},
		"schemas/domain/v1/curator-auto-acceptance.schema.json": {"id", `^cauto_[0-9a-f]{32}$`},
	} {
		document := readCuratorSchema(t, path)
		if document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Errorf("curator schema %q is not strict", path)
		}
		var property struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(document.Properties[propertyAndPattern[0]], &property); err != nil || property.Pattern != propertyAndPattern[1] {
			t.Errorf("curator schema %q ID = %#v, error=%v", path, property, err)
		}
	}

	derivation := readCuratorSchema(t, "schemas/domain/v1/curator-derivation.schema.json")
	for _, field := range []string{"rule_id", "rule_revision", "source_type", "source_id", "source_revision", "source_content_hash", "output_content_hash", "knowledge_revision_id", "event_sequence"} {
		if !containsCuratorString(derivation.Required, field) {
			t.Errorf("curator derivation does not require pinned evidence field %q", field)
		}
	}
	queue := readCuratorSchema(t, "schemas/domain/v1/curator-queue.schema.json")
	if !containsCuratorString(queue.Required, "rule") {
		t.Error("curator queue does not require its effective rule snapshot")
	}
	var queueEntry struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(queue.Definitions["entry"], &queueEntry); err != nil {
		t.Fatal(err)
	}
	var eligibilityReasons struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(queueEntry.Properties["eligibility_reason"], &eligibilityReasons); err != nil || !containsCuratorString(eligibilityReasons.Enum, "rule_disabled") {
		t.Errorf("curator queue does not publish distinct disabled-rule reason: %#v, error=%v", eligibilityReasons, err)
	}

	autoAcceptance := readCuratorSchema(t, "schemas/domain/v1/curator-auto-acceptance.schema.json")
	for _, field := range []string{"rule_id", "rule_revision", "derivation_id", "knowledge_revision_id", "authority_check_id", "knowledge_event_sequence", "actor"} {
		if !containsCuratorString(autoAcceptance.Required, field) {
			t.Errorf("curator auto acceptance does not require audit field %q", field)
		}
	}
	var actor struct {
		Properties map[string]struct {
			Const string `json:"const"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(autoAcceptance.Properties["actor"], &actor); err != nil || actor.Properties["id"].Const != "subsystem:curator" || actor.Properties["type"].Const != "subsystem" {
		t.Errorf("curator auto-accept actor = %#v, error=%v", actor, err)
	}

	process := readCuratorSchema(t, "schemas/domain/v1/curator-process.schema.json")
	if !containsCuratorString(process.Required, "skipped") {
		t.Error("curator process does not require explicit skipped candidates")
	}
	var candidates struct {
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(process.Properties["candidates_scanned"], &candidates); err != nil || candidates.Maximum != 100 {
		t.Errorf("curator candidate cap = %#v, error=%v", candidates, err)
	}
	for field, maximum := range map[string]int{"derived": 100, "accepted": 10, "skipped": 100} {
		var array struct {
			MaxItems int `json:"maxItems"`
		}
		if err := json.Unmarshal(process.Properties[field], &array); err != nil || array.MaxItems != maximum {
			t.Errorf("curator process %s cap = %#v, error=%v", field, array, err)
		}
	}
	var skip struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(process.Definitions["skip"], &skip); err != nil {
		t.Fatal(err)
	}
	var skipReasons struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(skip.Properties["reason"], &skipReasons); err != nil || len(skipReasons.Enum) != 2 || !containsCuratorString(skipReasons.Enum, "agenda_not_exact_safe_copy") || !containsCuratorString(skipReasons.Enum, "summary_not_exact_safe_copy") {
		t.Errorf("curator skip reasons = %#v, error=%v", skipReasons, err)
	}
}

func TestCuratorMethodsRemainProviderNeutralAndQueueLimitPreservesPresence(t *testing.T) {
	t.Parallel()
	for _, method := range []string{localapi.MethodCuratorQueue, localapi.MethodCuratorRuleConfigure, localapi.MethodCuratorProcess} {
		if method == "" {
			t.Fatal("curator method name is empty")
		}
		for _, forbidden := range []string{"codex", "claude", "herdr", "worktree", "model"} {
			if strings.Contains(method, forbidden) {
				t.Errorf("curator method %q embeds provider/runtime/layout term %q", method, forbidden)
			}
		}
	}
	omitted, err := json.Marshal(localapi.CuratorQueueParams{Workspace: "personal", Project: "engine"})
	if err != nil || strings.Contains(string(omitted), `"limit"`) {
		t.Errorf("omitted curator queue limit = %s, error=%v", omitted, err)
	}
	zero := 0
	explicit, err := json.Marshal(localapi.CuratorQueueParams{Workspace: "personal", Project: "engine", Limit: &zero})
	if err != nil || !strings.Contains(string(explicit), `"limit":0`) {
		t.Errorf("explicit curator queue zero = %s, error=%v", explicit, err)
	}
}

func readCuratorSchema(t *testing.T, path string) curatorSchemaDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read curator schema %q: %v", path, err)
	}
	var document curatorSchemaDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode curator schema %q: %v", path, err)
	}
	return document
}

func containsCuratorString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
