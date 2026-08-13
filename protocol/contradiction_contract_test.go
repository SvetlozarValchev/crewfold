package protocol_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestContradictionLifecycleSchemaRequiresAndForbidsExactStateFields(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/domain/v1/knowledge-contradiction.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) != 4 {
		t.Fatalf("lifecycle branches=%#v, want exact four statuses", schema["oneOf"])
	}
	matchesLifecycle := func(value map[string]any) bool {
		matched := 0
		for _, branch := range branches {
			if contradictionRuleMatches(value, branch) {
				matched++
			}
		}
		return matched == 1
	}
	base := func(status string, stateRevision int) map[string]any {
		return map[string]any{"status": status, "state_revision": float64(stateRevision)}
	}
	confirmation := map[string]any{"confirmed_at": "time", "confirmed_by": "local-owner", "confirmed_by_type": "human", "confirm_event_sequence": float64(2)}
	dismissal := map[string]any{"dismissed_at": "time", "dismissed_by": "local-owner", "dismissed_by_type": "human", "dismiss_event_sequence": float64(3)}
	resolution := map[string]any{
		"resolution_reason": "participant_stale", "resolved_at": "time", "resolved_by": "local-owner",
		"resolved_by_type": "human", "resolution_note": "revision became stale",
		"resolution_event_sequence": float64(4), "resolution_cause_event_sequence": float64(3),
	}
	with := func(value map[string]any, additions ...map[string]any) map[string]any {
		result := make(map[string]any, len(value)+8)
		for key, item := range value {
			result[key] = item
		}
		for _, addition := range additions {
			for key, item := range addition {
				result[key] = item
			}
		}
		return result
	}
	valid := map[string]map[string]any{
		"proposed":             base("proposed", 1),
		"open":                 with(base("open", 2), confirmation),
		"direct dismissal":     with(base("dismissed", 2), dismissal),
		"post-open dismissal":  with(base("dismissed", 3), confirmation, dismissal),
		"automatic resolution": with(base("resolved", 3), confirmation, resolution),
	}
	for name, value := range valid {
		if !matchesLifecycle(value) {
			t.Errorf("valid %s lifecycle does not match exactly one schema branch: %#v", name, value)
		}
	}
	invalid := map[string]map[string]any{
		"proposed with confirmation":    with(base("proposed", 1), confirmation),
		"open missing event link":       with(base("open", 2), map[string]any{"confirmed_at": "time", "confirmed_by": "local-owner", "confirmed_by_type": "human"}),
		"open impossible revision":      with(base("open", 3), confirmation),
		"open with dismissal":           with(base("open", 2), confirmation, dismissal),
		"dismissed partial confirm":     with(base("dismissed", 2), dismissal, map[string]any{"confirmed_at": "time"}),
		"dismissed rev2 confirmed":      with(base("dismissed", 2), confirmation, dismissal),
		"dismissed rev3 unconfirmed":    with(base("dismissed", 3), dismissal),
		"dismissed impossible revision": with(base("dismissed", 4), confirmation, dismissal),
		"dismissed with resolution":     with(base("dismissed", 3), dismissal, resolution),
		"resolved missing cause":        with(base("resolved", 3), confirmation, map[string]any{"resolution_reason": "participant_stale", "resolved_at": "time", "resolved_by": "local-owner", "resolved_by_type": "human", "resolution_note": "stale", "resolution_event_sequence": float64(4)}),
		"resolved impossible revision":  with(base("resolved", 4), confirmation, resolution),
		"resolved with dismissal":       with(base("resolved", 3), confirmation, resolution, dismissal),
	}
	for name, value := range invalid {
		if matchesLifecycle(value) {
			t.Errorf("invalid %s lifecycle matched a schema branch: %#v", name, value)
		}
	}
}

func TestContradictionSchemaActorAndOrderingContractsMatchRuntime(t *testing.T) {
	t.Parallel()
	contradiction := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction.schema.json")
	for _, field := range []string{"confirmed_by_type", "dismissed_by_type", "resolved_by_type"} {
		var value struct {
			Const string `json:"const"`
		}
		if err := json.Unmarshal(contradiction.Properties[field], &value); err != nil || value.Const != "human" {
			t.Errorf("%s=%#v error=%v, want owner human", field, value, err)
		}
	}
	authority := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction-authority.schema.json")
	var actor struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(authority.Properties["actor"], &actor); err != nil {
		t.Fatal(err)
	}
	assertKnowledgeEnum(t, actor.Properties["type"], []string{"human", "agent_run"})
	assertKnowledgeEnum(t, authority.Properties["reason"], []string{"workspace_owner", "actor_not_workspace_owner"})

	listData, err := os.ReadFile("schemas/domain/v1/knowledge-contradiction-list.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"reported_at descending", "ID descending", "newest first", "no continuation cursor"} {
		if !strings.Contains(string(listData), phrase) {
			t.Errorf("list schema does not publish %q ordering/paging contract", phrase)
		}
	}
}

// contradictionRuleMatches evaluates the small JSON Schema subset used by the
// lifecycle branches. It intentionally stays narrow instead of adding a schema
// runtime dependency to the protocol package.
func contradictionRuleMatches(value map[string]any, rawRule any) bool {
	rule, ok := rawRule.(map[string]any)
	if !ok {
		return false
	}
	if required, ok := rule["required"].([]any); ok {
		for _, rawName := range required {
			name, _ := rawName.(string)
			if _, exists := value[name]; !exists {
				return false
			}
		}
	}
	if properties, ok := rule["properties"].(map[string]any); ok {
		for name, rawProperty := range properties {
			actual, exists := value[name]
			if !exists {
				continue
			}
			property, _ := rawProperty.(map[string]any)
			if expected, hasConst := property["const"]; hasConst && actual != expected {
				return false
			}
			if minimum, hasMinimum := property["minimum"].(float64); hasMinimum {
				number, numeric := actual.(float64)
				if !numeric || number < minimum {
					return false
				}
			}
			if allowed, hasEnum := property["enum"].([]any); hasEnum {
				found := false
				for _, candidate := range allowed {
					found = found || actual == candidate
				}
				if !found {
					return false
				}
			}
		}
	}
	if negated, ok := rule["not"]; ok && contradictionRuleMatches(value, negated) {
		return false
	}
	if alternatives, ok := rule["anyOf"].([]any); ok {
		matched := false
		for _, alternative := range alternatives {
			matched = matched || contradictionRuleMatches(value, alternative)
		}
		if !matched {
			return false
		}
	}
	if alternatives, ok := rule["oneOf"].([]any); ok {
		matched := 0
		for _, alternative := range alternatives {
			if contradictionRuleMatches(value, alternative) {
				matched++
			}
		}
		if matched != 1 {
			return false
		}
	}
	return true
}

func TestContradictionSchemaIDsMatchPublishedContract(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"schemas/domain/v1/knowledge-contradiction.schema.json":           "urn:crewfold:schema:domain:knowledge-contradiction:v1",
		"schemas/domain/v1/knowledge-contradiction-authority.schema.json": "urn:crewfold:schema:domain:knowledge-contradiction-authority:v1",
		"schemas/domain/v1/knowledge-contradiction-detail.schema.json":    "urn:crewfold:schema:domain:knowledge-contradiction-detail:v1",
		"schemas/domain/v1/knowledge-contradiction-list.schema.json":      "urn:crewfold:schema:domain:knowledge-contradiction-list:v1",
		"schemas/domain/v1/knowledge-revision-dispute.schema.json":        "urn:crewfold:schema:domain:knowledge-revision-dispute:v1",
		"schemas/local/v1/knowledge-dispute.params.schema.json":           "urn:crewfold:schema:local-api:knowledge-dispute-params:v1",
		"schemas/local/v1/knowledge-dispute.result.schema.json":           localapi.KnowledgeDisputeSchema,
		"schemas/local/v1/contradiction-report.params.schema.json":        "urn:crewfold:schema:local-api:contradiction-report-params:v1",
		"schemas/local/v1/contradiction-show.params.schema.json":          "urn:crewfold:schema:local-api:contradiction-show-params:v1",
		"schemas/local/v1/contradiction-list.params.schema.json":          "urn:crewfold:schema:local-api:contradiction-list-params:v1",
		"schemas/local/v1/contradiction-confirm.params.schema.json":       "urn:crewfold:schema:local-api:contradiction-confirm-params:v1",
		"schemas/local/v1/contradiction-dismiss.params.schema.json":       "urn:crewfold:schema:local-api:contradiction-dismiss-params:v1",
		"schemas/local/v1/contradiction-mutation.result.schema.json":      localapi.ContradictionMutationSchema,
		"schemas/local/v1/contradiction-show.result.schema.json":          localapi.ContradictionShowSchema,
		"schemas/local/v1/contradiction-list.result.schema.json":          localapi.ContradictionListSchema,
		"schemas/mcp/v1/report-contradiction.input.schema.json":           "urn:crewfold:schema:mcp:report-contradiction-input:v1",
	}
	for path, expected := range want {
		if document := readKnowledgeSchema(t, path); document.ID != expected {
			t.Errorf("schema %q ID=%q, want %q", path, document.ID, expected)
		}
	}
}

func TestRunContradictionReportInputIsStrictExactAndNonAuthoritative(t *testing.T) {
	t.Parallel()
	document := readKnowledgeSchema(t, "schemas/mcp/v1/report-contradiction.input.schema.json")
	if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
		t.Fatal("run contradiction report input is not a strict object")
	}
	for _, field := range []string{"left_revision", "right_revision", "reason", "idempotency_key"} {
		if !containsKnowledgeString(document.Required, field) {
			t.Errorf("run contradiction report does not require %q", field)
		}
	}
	for _, forbidden := range []string{"actor", "actor_id", "actor_type", "workspace", "project", "task", "status", "state_revision"} {
		if _, found := document.Properties[forbidden]; found {
			t.Errorf("run contradiction report exposes trusted field %q", forbidden)
		}
	}
}

func TestContradictionRequestsAreStrictScopedBoundedAndNonAuthoritative(t *testing.T) {
	t.Parallel()
	paths := []string{
		"schemas/local/v1/contradiction-report.params.schema.json",
		"schemas/local/v1/contradiction-show.params.schema.json",
		"schemas/local/v1/contradiction-list.params.schema.json",
		"schemas/local/v1/contradiction-confirm.params.schema.json",
		"schemas/local/v1/contradiction-dismiss.params.schema.json",
	}
	for _, path := range paths {
		document := readKnowledgeSchema(t, path)
		if document.Type != "object" || document.AdditionalProperties == nil || *document.AdditionalProperties {
			t.Errorf("schema %q is not a strict object", path)
		}
		if !containsKnowledgeString(document.Required, "workspace") {
			t.Errorf("schema %q does not require workspace scope", path)
		}
		for _, forbidden := range []string{"actor", "actor_id", "actor_type", "reported_by", "confirmed_by", "dismissed_by", "status_override"} {
			if _, found := document.Properties[forbidden]; found {
				t.Errorf("schema %q exposes trusted field %q", path, forbidden)
			}
		}
	}

	report := readKnowledgeSchema(t, "schemas/local/v1/contradiction-report.params.schema.json")
	for _, field := range []string{"left_revision", "right_revision", "reason", "idempotency_key"} {
		if !containsKnowledgeString(report.Required, field) {
			t.Errorf("contradiction report does not require %q", field)
		}
	}
	for _, forbidden := range []string{"project", "status", "state_revision"} {
		if _, found := report.Properties[forbidden]; found {
			t.Errorf("contradiction report lets caller select %q", forbidden)
		}
	}
	var reasonBounds struct {
		MinLength int `json:"minLength"`
		MaxLength int `json:"maxLength"`
	}
	if err := json.Unmarshal(report.Properties["reason"], &reasonBounds); err != nil || reasonBounds.MinLength != 1 || reasonBounds.MaxLength != 2048 {
		t.Errorf("report reason bounds=%#v error=%v", reasonBounds, err)
	}

	list := readKnowledgeSchema(t, "schemas/local/v1/contradiction-list.params.schema.json")
	if !containsKnowledgeString(list.Required, "project") {
		t.Error("contradiction list does not require project scope despite returning exact revision bodies")
	}
	assertKnowledgeEnum(t, list.Properties["status"], []string{"proposed", "open", "resolved", "dismissed"})
	var listBounds struct {
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(list.Properties["limit"], &listBounds); err != nil || listBounds.Minimum != 1 || listBounds.Maximum != 200 {
		t.Errorf("contradiction list limit=%#v error=%v", listBounds, err)
	}

	for _, action := range []string{"confirm", "dismiss"} {
		decision := readKnowledgeSchema(t, "schemas/local/v1/contradiction-"+action+".params.schema.json")
		for _, field := range []string{"contradiction", "expected_state_revision", "idempotency_key"} {
			if !containsKnowledgeString(decision.Required, field) {
				t.Errorf("%s request does not require %q", action, field)
			}
		}
	}
}

func TestContradictionIsSeparateFromKnowledgeCurrencyAndDetailIsExplainable(t *testing.T) {
	t.Parallel()
	revision := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-revision.schema.json")
	assertKnowledgeEnum(t, revision.Properties["currency_status"], []string{"pending", "current", "stale", "superseded"})
	for _, forbidden := range []string{"disputed", "contradiction_ids", "contradiction_status"} {
		if _, found := revision.Properties[forbidden]; found {
			t.Errorf("knowledge revision embeds separate contradiction axis %q", forbidden)
		}
	}

	contradiction := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction.schema.json")
	assertKnowledgeEnum(t, contradiction.Properties["status"], []string{"proposed", "open", "resolved", "dismissed"})
	for _, field := range []string{"left_revision_id", "right_revision_id", "state_revision", "report_note", "reported_by", "reported_by_type"} {
		if !containsKnowledgeString(contradiction.Required, field) {
			t.Errorf("contradiction record omits %q", field)
		}
	}

	detail := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction-detail.schema.json")
	for _, field := range []string{"contradiction", "left_revision", "right_revision", "authority_check_count", "authority_checks"} {
		if !containsKnowledgeString(detail.Required, field) {
			t.Errorf("contradiction detail omits %q", field)
		}
	}
	var checks struct {
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(detail.Properties["authority_checks"], &checks); err != nil || checks.MaxItems != 200 {
		t.Errorf("authority check sample bound=%#v error=%v", checks, err)
	}
	list := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction-list.schema.json")
	var details struct {
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(list.Properties["details"], &details); err != nil || details.MaxItems != 200 {
		t.Errorf("contradiction details bound=%#v error=%v", details, err)
	}

	authority := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-contradiction-authority.schema.json")
	assertKnowledgeEnum(t, authority.Properties["action"], []string{"confirm", "dismiss"})
	var identifier struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(authority.Properties["id"], &identifier); err != nil || identifier.Pattern != `^kcauth_[0-9a-f]{32}$` {
		t.Errorf("contradiction authority ID=%#v error=%v", identifier, err)
	}
}

func TestKnowledgeDisputeReadIsStandaloneBoundedAndNonMutating(t *testing.T) {
	t.Parallel()
	params := readKnowledgeSchema(t, "schemas/local/v1/knowledge-dispute.params.schema.json")
	if params.Type != "object" || params.AdditionalProperties == nil || *params.AdditionalProperties {
		t.Fatal("knowledge dispute query is not strict")
	}
	for _, field := range []string{"workspace", "knowledge_revision"} {
		if !containsKnowledgeString(params.Required, field) {
			t.Errorf("knowledge dispute query omits %q", field)
		}
	}
	for _, forbidden := range []string{"actor", "status", "currency_status", "contradiction_id"} {
		if _, found := params.Properties[forbidden]; found {
			t.Errorf("knowledge dispute query exposes mutation field %q", forbidden)
		}
	}
	dispute := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-revision-dispute.schema.json")
	for _, field := range []string{"revision_id", "disputed", "open_contradiction_count", "open_contradiction_ids"} {
		if !containsKnowledgeString(dispute.Required, field) {
			t.Errorf("knowledge dispute result omits %q", field)
		}
	}
	var ids struct {
		MaxItems int `json:"maxItems"`
	}
	if err := json.Unmarshal(dispute.Properties["open_contradiction_ids"], &ids); err != nil || ids.MaxItems != 200 {
		t.Errorf("open contradiction ID bound=%#v error=%v", ids, err)
	}
	revision := readKnowledgeSchema(t, "schemas/domain/v1/knowledge-revision.schema.json")
	for _, forbidden := range []string{"disputed", "open_contradiction_count", "open_contradiction_ids"} {
		if _, found := revision.Properties[forbidden]; found {
			t.Errorf("knowledge revision v1 changed to embed dispute field %q", forbidden)
		}
	}
}
