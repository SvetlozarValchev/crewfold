package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestOutcomeResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expected := range map[string]string{
		"schemas/local/v1/outcome-commitment-mutation.result.schema.json": localapi.OutcomeCommitmentMutationSchema,
		"schemas/local/v1/outcome-commitment-show.result.schema.json":     localapi.OutcomeCommitmentShowSchema,
		"schemas/local/v1/outcome-commitment-list.result.schema.json":     localapi.OutcomeCommitmentListSchema,
		"schemas/local/v1/outcome-mutation.result.schema.json":            localapi.OutcomeMutationSchema,
		"schemas/local/v1/outcome-show.result.schema.json":                localapi.OutcomeShowSchema,
		"schemas/local/v1/outcome-list.result.schema.json":                localapi.OutcomeListSchema,
		"schemas/local/v1/checkpoint-mutation.result.schema.json":         localapi.CheckpointMutationSchema,
		"schemas/local/v1/checkpoint-show.result.schema.json":             localapi.CheckpointShowSchema,
		"schemas/local/v1/checkpoint-list.result.schema.json":             localapi.CheckpointListSchema,
		"schemas/local/v1/briefing-show.result.schema.json":               localapi.BriefingShowSchema,
		"schemas/local/v1/briefing-explain.result.schema.json":            localapi.BriefingExplainSchema,
	} {
		document := readContextSchema(t, path)
		if document["$id"] != expected {
			t.Errorf("%s ID = %v, want %q", path, document["$id"], expected)
		}
	}
}

func TestOutcomeWireMethodsUseExplicitCurrentAssessmentNamespace(t *testing.T) {
	t.Parallel()
	want := []string{
		"outcome.assessment.propose",
		"outcome.assessment.show",
		"outcome.assessment.list",
		"outcome.assessment.accept",
		"outcome.assessment.reject",
	}
	got := []string{
		localapi.MethodOutcomePropose,
		localapi.MethodOutcomeShow,
		localapi.MethodOutcomeList,
		localapi.MethodOutcomeAccept,
		localapi.MethodOutcomeReject,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assessment wire methods = %v, want %v", got, want)
	}
}

func TestOutcomeSchemasCoverExactGoJSONShapes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path       string
		definition string
		value      any
	}{
		{"schemas/domain/v1/deliverable-commitment.schema.json", "", domain.DeliverableCommitment{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "", domain.OutcomeAssessmentInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "evidence", domain.OutcomeEvidenceInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "effect", domain.OutcomeEffectInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "deviation", domain.OutcomeDeviationInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "risk", domain.OutcomeRiskInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "unknown", domain.OutcomeUnknownInput{}},
		{"schemas/domain/v1/outcome-assessment-input.schema.json", "owner_attention", domain.OutcomeOwnerAttentionInput{}},
		{"schemas/domain/v1/outcome-assessment.schema.json", "", domain.OutcomeAssessment{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "", domain.OutcomeAssessmentDetail{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "decision", domain.OutcomeDecisionReference{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "evidence", domain.OutcomeEvidenceReference{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "effect", domain.OutcomeEffect{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "deviation", domain.OutcomeDeviation{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "risk", domain.OutcomeRisk{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "unknown", domain.OutcomeUnknownRecord{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "follow_up_task", domain.OutcomeFollowUpTask{}},
		{"schemas/domain/v1/outcome-assessment-detail.schema.json", "owner_attention", domain.OutcomeOwnerAttention{}},
		{"schemas/domain/v1/owner-checkpoint.schema.json", "", domain.OwnerCheckpoint{}},
		{"schemas/domain/v1/management-briefing.schema.json", "", domain.ManagementBriefing{}},
		{"schemas/domain/v1/management-briefing.schema.json", "scope", domain.BriefingScope{}},
		{"schemas/domain/v1/management-briefing.schema.json", "claim_source", domain.BriefingClaimSource{}},
		{"schemas/domain/v1/management-briefing.schema.json", "claim", domain.BriefingClaim{}},
		{"schemas/domain/v1/management-briefing.schema.json", "omission", domain.BriefingOmission{}},
		{"schemas/domain/v1/briefing-claim-explanation.schema.json", "", domain.BriefingClaimExplanation{}},
		{"schemas/local/v1/outcome-commitment-create.params.schema.json", "", localapi.OutcomeCommitmentCreateParams{}},
		{"schemas/local/v1/outcome-commitment-query.params.schema.json", "", localapi.OutcomeCommitmentQueryParams{}},
		{"schemas/local/v1/outcome-propose.params.schema.json", "", localapi.OutcomeProposeParams{}},
		{"schemas/local/v1/outcome-query.params.schema.json", "", localapi.OutcomeQueryParams{}},
		{"schemas/local/v1/outcome-decision.params.schema.json", "", localapi.OutcomeDecisionParams{}},
		{"schemas/local/v1/checkpoint-create.params.schema.json", "", localapi.CheckpointCreateParams{}},
		{"schemas/local/v1/checkpoint-query.params.schema.json", "", localapi.CheckpointQueryParams{}},
		{"schemas/local/v1/briefing-show.params.schema.json", "", localapi.BriefingShowParams{}},
		{"schemas/local/v1/briefing-explain.params.schema.json", "", localapi.BriefingExplainParams{}},
		{"schemas/local/v1/outcome-commitment-mutation.result.schema.json", "", localapi.OutcomeCommitmentMutationResult{}},
		{"schemas/local/v1/outcome-commitment-show.result.schema.json", "", localapi.OutcomeCommitmentShowResult{}},
		{"schemas/local/v1/outcome-commitment-list.result.schema.json", "", localapi.OutcomeCommitmentListResult{}},
		{"schemas/local/v1/outcome-mutation.result.schema.json", "", localapi.OutcomeMutationResult{}},
		{"schemas/local/v1/outcome-show.result.schema.json", "", localapi.OutcomeShowResult{}},
		{"schemas/local/v1/outcome-list.result.schema.json", "", localapi.OutcomeListResult{}},
		{"schemas/local/v1/checkpoint-mutation.result.schema.json", "", localapi.CheckpointMutationResult{}},
		{"schemas/local/v1/checkpoint-show.result.schema.json", "", localapi.CheckpointShowResult{}},
		{"schemas/local/v1/checkpoint-list.result.schema.json", "", localapi.CheckpointListResult{}},
		{"schemas/local/v1/briefing-show.result.schema.json", "", localapi.BriefingShowResult{}},
		{"schemas/local/v1/briefing-explain.result.schema.json", "", localapi.BriefingExplainResult{}},
	} {
		document := readContextSchema(t, test.path)
		if test.definition != "" {
			document = document["$defs"].(map[string]any)[test.definition].(map[string]any)
		}
		assertOutcomeGoShape(t, test.path+"#"+test.definition, document, test.value)
	}
}

func TestBriefingOmissionsAreClosedPerSectionAndReason(t *testing.T) {
	t.Parallel()
	schema := readContextSchema(t, "schemas/domain/v1/management-briefing.schema.json")
	omitted := schema["properties"].(map[string]any)["omitted"].(map[string]any)
	if maximum, ok := omitted["maxItems"].(float64); !ok || maximum != 14 {
		t.Fatalf("briefing omitted maxItems = %#v, want 14 section/reason pairs", omitted["maxItems"])
	}
	omission := schema["$defs"].(map[string]any)["omission"].(map[string]any)
	properties := omission["properties"].(map[string]any)
	sections := contractStringSlice(properties["section"].(map[string]any)["enum"])
	wantSections := []string{
		"required_decisions", "contradictions", "risks_unknowns", "verification_gaps",
		"deviations_unmet", "accepted_delivery", "rationale_change",
	}
	if !reflect.DeepEqual(sections, wantSections) {
		t.Fatalf("briefing omission sections = %v, want %v", sections, wantSections)
	}
	reasons := contractStringSlice(properties["reason"].(map[string]any)["enum"])
	if !reflect.DeepEqual(reasons, []string{"claim_limit", "byte_limit"}) {
		t.Fatalf("briefing omission reasons = %v", reasons)
	}
}

func TestOutcomeSchemasAreStrictCurrentDraft202012(t *testing.T) {
	t.Parallel()
	paths := outcomeSchemaPaths(t)
	for _, path := range paths {
		document := readContextSchema(t, path)
		if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("%s does not declare Draft 2020-12", path)
		}
		assertEveryTypedOutcomeObjectIsClosed(t, path, document)
		id, _ := document["$id"].(string)
		if !strings.HasSuffix(id, ":v1") || strings.Contains(id, "legacy") || strings.Contains(id, "deprecated") {
			t.Errorf("%s publishes a noncurrent schema identity %q", path, id)
		}
	}
	if matches, err := filepath.Glob("schemas/domain/v[2-9]/*outcome*.schema.json"); err != nil || len(matches) != 0 {
		t.Fatalf("outcome domain schema version ladder exists: %v, %v", matches, err)
	}
	if matches, err := filepath.Glob("schemas/local/v[2-9]/*outcome*.schema.json"); err != nil || len(matches) != 0 {
		t.Fatalf("outcome local schema version ladder exists: %v, %v", matches, err)
	}
}

func TestOutcomeAssessmentInputDoesNotAcceptDerivedAuthority(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/outcome-assessment-input.schema.json")
	evidence := document["$defs"].(map[string]any)["evidence"].(map[string]any)
	types := contractStringSlice(evidence["properties"].(map[string]any)["source_type"].(map[string]any)["enum"])
	want := []string{domain.OutcomeEvidenceHandoff, domain.OutcomeEvidenceCheckRequirementEvidence}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("owner evidence input types = %v, want exact record identifiers %v", types, want)
	}
	for _, forbidden := range []string{"class", "effect", "freshness", "current", "disputed", "contradictory", "content_sha256", "event_sequence", "revision"} {
		if _, exists := evidence["properties"].(map[string]any)[forbidden]; exists {
			t.Errorf("owner evidence input exposes derived field %q", forbidden)
		}
	}
}

func TestBriefingContractFreezesWholeClaimAndByteBudgets(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/management-briefing.schema.json")
	properties := document["properties"].(map[string]any)
	if properties["claims"].(map[string]any)["maxItems"] != float64(128) {
		t.Fatalf("briefing claim cap = %v, want 128", properties["claims"])
	}
	if properties["byte_size"].(map[string]any)["maximum"] != float64(65536) {
		t.Fatalf("briefing byte cap = %v, want 65536", properties["byte_size"])
	}
	claim := document["$defs"].(map[string]any)["claim"].(map[string]any)
	if claim["properties"].(map[string]any)["id"].(map[string]any)["pattern"] != "^bclaim_[0-9a-f]{64}$" {
		t.Fatal("briefing claim identity is not its frozen deterministic hash")
	}
	sources := claim["properties"].(map[string]any)["sources"].(map[string]any)
	if sources["minItems"] != float64(1) || sources["maxItems"] != float64(32) {
		t.Fatalf("material claim source bounds = %v, want 1..32", sources)
	}
	omissionReasons := contractStringSlice(document["$defs"].(map[string]any)["omission"].(map[string]any)["properties"].(map[string]any)["reason"].(map[string]any)["enum"])
	if !reflect.DeepEqual(omissionReasons, []string{domain.BriefingOmittedClaimLimit, domain.BriefingOmittedByteLimit}) {
		t.Fatalf("briefing omission reasons = %v", omissionReasons)
	}
}

func TestBriefingEvidenceProvenanceIsDerivedAndClosed(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/management-briefing.schema.json")
	source := document["$defs"].(map[string]any)["claim_source"].(map[string]any)
	properties := source["properties"].(map[string]any)
	entityTypes := contractStringSlice(properties["entity_type"].(map[string]any)["enum"])
	if slicesContainOutcomeString(entityTypes, "check_result") {
		t.Fatal("briefing provenance publishes dead check_result source instead of exact check_requirement_evidence")
	}

	wantEnums := map[string][]string{
		"evidence_class":    {domain.EvidenceAgentSelfReport, domain.EvidenceMechanicalCheck, domain.EvidenceIndependentReview},
		"evidence_effect":   {domain.CheckEvidenceSupports, domain.CheckEvidenceContradicts, domain.CheckEvidenceInconclusive},
		"pinned_freshness":  {domain.OutcomeEvidenceFresh, domain.OutcomeEvidenceStale, domain.OutcomeEvidenceUnknown},
		"current_freshness": {domain.OutcomeEvidenceFresh, domain.OutcomeEvidenceStale, domain.OutcomeEvidenceUnknown},
	}
	for field, want := range wantEnums {
		got := contractStringSlice(properties[field].(map[string]any)["enum"])
		if !reflect.DeepEqual(got, want) {
			t.Errorf("briefing source %s values = %v, want exact derived union %v", field, got, want)
		}
	}

	conditions := source["allOf"].([]any)
	if len(conditions) != 1 {
		t.Fatalf("briefing claim source evidence conditions = %d, want 1", len(conditions))
	}
	condition := conditions[0].(map[string]any)
	ifTypes := contractStringSlice(condition["if"].(map[string]any)["properties"].(map[string]any)["entity_type"].(map[string]any)["enum"])
	wantTypes := []string{domain.OutcomeEvidenceHandoff, domain.OutcomeEvidenceCheckRequirementEvidence}
	if !reflect.DeepEqual(ifTypes, wantTypes) {
		t.Fatalf("briefing evidence provenance source types = %v, want %v", ifTypes, wantTypes)
	}
	wantFields := []string{"evidence_class", "evidence_effect", "pinned_freshness", "current_freshness"}
	if got := contractStringSlice(condition["then"].(map[string]any)["required"]); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("briefing evidence provenance required fields = %v, want %v", got, wantFields)
	}
	forbidden := condition["else"].(map[string]any)["not"].(map[string]any)["anyOf"].([]any)
	if len(forbidden) != len(wantFields) {
		t.Fatalf("briefing non-evidence provenance forbidden fields = %d, want %d", len(forbidden), len(wantFields))
	}
	for index, item := range forbidden {
		if got := contractStringSlice(item.(map[string]any)["required"]); !reflect.DeepEqual(got, []string{wantFields[index]}) {
			t.Errorf("briefing non-evidence forbidden field %d = %v, want %q", index, got, wantFields[index])
		}
	}
}

func slicesContainOutcomeString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestBriefingReadsUseCurrentHighWaterAndOptionalCheckpointOnly(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/local/v1/briefing-show.params.schema.json")
	properties := document["properties"].(map[string]any)
	if _, exists := properties["at_event_sequence"]; exists {
		t.Fatal("briefing request exposes a caller-selected event cursor")
	}
	for _, field := range []string{"event_cursor", "cutoff_event_sequence", "since_event_sequence"} {
		if _, exists := properties[field]; exists {
			t.Errorf("briefing request exposes derived cursor field %q", field)
		}
	}
	if properties["since_checkpoint"].(map[string]any)["pattern"] != "^outcpnt_[0-9a-f]{32}$" {
		t.Fatal("briefing request does not bind its optional lower bound to one exact checkpoint")
	}
}

func TestOutcomeAuthorityIsOwnerLocalOnly(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{
		"schemas/mcp/v1/*outcome*", "schemas/mcp/v1/*commitment*", "schemas/mcp/v1/*checkpoint*", "schemas/mcp/v1/*briefing*",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Errorf("owner-only outcome capability leaked into MCP: %v", matches)
		}
	}
	for _, path := range []string{
		"schemas/local/v1/outcome-commitment-create.params.schema.json",
		"schemas/local/v1/outcome-propose.params.schema.json",
		"schemas/local/v1/outcome-decision.params.schema.json",
		"schemas/local/v1/checkpoint-create.params.schema.json",
	} {
		properties := readContextSchema(t, path)["properties"].(map[string]any)
		for _, forbidden := range []string{"agent", "run", "grant", "role", "purpose", "profile", "provider", "runtime"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s publishes inert or delegated authority field %q", path, forbidden)
			}
		}
	}
}

func outcomeSchemaPaths(t *testing.T) []string {
	t.Helper()
	paths := []string{
		"schemas/domain/v1/deliverable-commitment.schema.json",
		"schemas/domain/v1/outcome-assessment-input.schema.json",
		"schemas/domain/v1/outcome-assessment.schema.json",
		"schemas/domain/v1/outcome-assessment-detail.schema.json",
		"schemas/domain/v1/owner-checkpoint.schema.json",
		"schemas/domain/v1/management-briefing.schema.json",
		"schemas/domain/v1/briefing-claim-explanation.schema.json",
	}
	for _, pattern := range []string{"schemas/local/v1/outcome-*.schema.json", "schemas/local/v1/checkpoint-*.schema.json", "schemas/local/v1/briefing-*.schema.json"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	return paths
}

func assertOutcomeGoShape(t *testing.T, label string, schema map[string]any, value any) {
	t.Helper()
	typeOf := reflect.TypeOf(value)
	properties := schema["properties"].(map[string]any)
	wantProperties := make([]string, 0, typeOf.NumField())
	wantRequired := make([]string, 0, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		tag := typeOf.Field(index).Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "" || parts[0] == "-" {
			continue
		}
		wantProperties = append(wantProperties, parts[0])
		if !containsOutcomeTagOption(parts[1:], "omitempty") {
			wantRequired = append(wantRequired, parts[0])
		}
	}
	gotProperties := make([]string, 0, len(properties))
	for name := range properties {
		gotProperties = append(gotProperties, name)
	}
	gotRequired := contractStringSlice(schema["required"])
	sort.Strings(gotProperties)
	sort.Strings(gotRequired)
	sort.Strings(wantProperties)
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Errorf("%s properties = %v, want exact Go JSON fields %v", label, gotProperties, wantProperties)
	}
	if !reflect.DeepEqual(gotRequired, wantRequired) {
		t.Errorf("%s required = %v, want exact non-omitempty Go fields %v", label, gotRequired, wantRequired)
	}
}

func containsOutcomeTagOption(options []string, wanted string) bool {
	for _, option := range options {
		if option == wanted {
			return true
		}
	}
	return false
}

func assertEveryTypedOutcomeObjectIsClosed(t *testing.T, label string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "object" && typed["additionalProperties"] != false {
			t.Errorf("%s contains an object schema that permits undeclared fields", label)
		}
		for _, child := range typed {
			assertEveryTypedOutcomeObjectIsClosed(t, label, child)
		}
	case []any:
		for _, child := range typed {
			assertEveryTypedOutcomeObjectIsClosed(t, label, child)
		}
	}
}

func TestOutcomeSchemaDocumentsAreValidJSON(t *testing.T) {
	t.Parallel()
	for _, path := range outcomeSchemaPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		assertOutcomeFileReferencesExist(t, path, document)
	}
}

func assertOutcomeFileReferencesExist(t *testing.T, schemaPath string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok && reference != "" && !strings.HasPrefix(reference, "#") && !strings.Contains(reference, "://") {
			filePart := strings.SplitN(reference, "#", 2)[0]
			if _, err := os.Stat(filepath.Join(filepath.Dir(schemaPath), filepath.FromSlash(filePart))); err != nil {
				t.Errorf("%s has unresolved file reference %q: %v", schemaPath, reference, err)
			}
		}
		for _, child := range typed {
			assertOutcomeFileReferencesExist(t, schemaPath, child)
		}
	case []any:
		for _, child := range typed {
			assertOutcomeFileReferencesExist(t, schemaPath, child)
		}
	}
}
