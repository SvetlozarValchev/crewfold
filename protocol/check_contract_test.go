package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestCheckSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expected := range map[string]string{
		"schemas/local/v1/check-definition-mutation.result.schema.json":  localapi.CheckDefinitionMutationSchema,
		"schemas/local/v1/check-definition-show.result.schema.json":      localapi.CheckDefinitionShowSchema,
		"schemas/local/v1/check-definition-list.result.schema.json":      localapi.CheckDefinitionListSchema,
		"schemas/local/v1/check-requirement-mutation.result.schema.json": localapi.CheckRequirementMutationSchema,
		"schemas/local/v1/check-requirement-list.result.schema.json":     localapi.CheckRequirementListSchema,
		"schemas/local/v1/check-grant-mutation.result.schema.json":       localapi.CheckGrantMutationSchema,
		"schemas/local/v1/check-grant-show.result.schema.json":           localapi.CheckGrantShowSchema,
		"schemas/local/v1/check-grant-list.result.schema.json":           localapi.CheckGrantListSchema,
		"schemas/local/v1/check-route-mutation.result.schema.json":       localapi.CheckRouteMutationSchema,
		"schemas/local/v1/check-route-list.result.schema.json":           localapi.CheckRouteListSchema,
		"schemas/local/v1/check-policy-mutation.result.schema.json":      localapi.CheckPolicyMutationSchema,
		"schemas/local/v1/check-policy-show.result.schema.json":          localapi.CheckPolicyShowSchema,
		"schemas/local/v1/check-run-mutation.result.schema.json":         localapi.CheckRunMutationSchema,
		"schemas/local/v1/check-run-list.result.schema.json":             localapi.CheckRunListSchema,
		"schemas/local/v1/check-inspect.result.schema.json":              localapi.CheckInspectSchema,
		"schemas/local/v1/check-logs.result.schema.json":                 localapi.CheckLogsSchema,
		"schemas/local/v1/check-watch.result.schema.json":                localapi.CheckWatchSchema,
		"schemas/local/v1/check-repair-mutation.result.schema.json":      localapi.CheckRepairMutationSchema,
		"schemas/local/v1/check-repair-show.result.schema.json":          localapi.CheckRepairShowSchema,
		"schemas/local/v1/check-repair-list.result.schema.json":          localapi.CheckRepairListSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if header.ID != expected {
			t.Errorf("%s ID = %q, want %q", path, header.ID, expected)
		}
	}
}

func TestCheckSchemasCoverEveryGoJSONField(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path  string
		value any
	}{
		{"schemas/domain/v1/check-definition.schema.json", domain.CheckDefinition{}},
		{"schemas/domain/v1/task-check-requirement.schema.json", domain.TaskCheckRequirement{}},
		{"schemas/domain/v1/check-watch-grant.schema.json", domain.CheckWatchGrant{}},
		{"schemas/domain/v1/check-policy.schema.json", domain.CheckPolicy{}},
		{"schemas/domain/v1/check-route.schema.json", domain.CheckRoute{}},
		{"schemas/domain/v1/check-git-observation.schema.json", domain.CheckGitObservation{}},
		{"schemas/domain/v1/check-run-source.schema.json", domain.CheckRunSource{}},
		{"schemas/domain/v1/check-run.schema.json", domain.CheckRun{}},
		{"schemas/domain/v1/check-job.schema.json", domain.CheckJob{}},
		{"schemas/domain/v1/check-launch-receipt.schema.json", domain.CheckLaunchReceipt{}},
		{"schemas/domain/v1/check-result.schema.json", domain.CheckResult{}},
		{"schemas/domain/v1/check-artifact.schema.json", domain.CheckArtifact{}},
		{"schemas/domain/v1/check-result-freshness.schema.json", domain.CheckResultFreshness{}},
		{"schemas/domain/v1/check-requirement-evidence.schema.json", domain.CheckRequirementEvidence{}},
		{"schemas/domain/v1/check-evidence-buckets.schema.json", domain.CheckEvidenceBuckets{}},
		{"schemas/domain/v1/task-check-requirement-view.schema.json", domain.TaskCheckRequirementView{}},
		{"schemas/domain/v1/check-notification-receipt.schema.json", domain.CheckNotificationReceipt{}},
		{"schemas/domain/v1/check-route-failure.schema.json", domain.CheckRouteFailure{}},
		{"schemas/domain/v1/check-repair-proposal.schema.json", domain.CheckRepairProposal{}},
		{"schemas/domain/v1/check-run-detail.schema.json", domain.CheckRunDetail{}},
		{"schemas/domain/v1/check-run-list-item.schema.json", domain.CheckRunListItem{}},
		{"schemas/domain/v1/check-watch-receipt.schema.json", domain.CheckWatchReceipt{}},
		{"schemas/domain/v1/check-repair-decision.schema.json", domain.CheckRepairDecision{}},
		{"schemas/domain/v1/check-repair-effect.schema.json", domain.CheckRepairEffect{}},
		{"schemas/domain/v1/check-repair-detail.schema.json", domain.CheckRepairDetail{}},
		{"schemas/domain/v1/check-captured-log.schema.json", domain.CheckCapturedLog{}},
		{"schemas/domain/v1/check-run-logs.schema.json", domain.CheckRunLogs{}},
		{"schemas/local/v1/check-definition-create.params.schema.json", localapi.CheckDefinitionCreateParams{}},
		{"schemas/local/v1/check-definition-retire.params.schema.json", localapi.CheckDefinitionRetireParams{}},
		{"schemas/local/v1/check-definition-query.params.schema.json", localapi.CheckDefinitionQueryParams{}},
		{"schemas/local/v1/check-requirement-create.params.schema.json", localapi.CheckRequirementCreateParams{}},
		{"schemas/local/v1/check-requirement-retire.params.schema.json", localapi.CheckRequirementRetireParams{}},
		{"schemas/local/v1/check-requirement-query.params.schema.json", localapi.CheckRequirementQueryParams{}},
		{"schemas/local/v1/check-grant-create.params.schema.json", localapi.CheckGrantCreateParams{}},
		{"schemas/local/v1/check-grant-revoke.params.schema.json", localapi.CheckGrantRevokeParams{}},
		{"schemas/local/v1/check-grant-query.params.schema.json", localapi.CheckGrantQueryParams{}},
		{"schemas/local/v1/check-route-create.params.schema.json", localapi.CheckRouteCreateParams{}},
		{"schemas/local/v1/check-route-retire.params.schema.json", localapi.CheckRouteRetireParams{}},
		{"schemas/local/v1/check-route-query.params.schema.json", localapi.CheckRouteQueryParams{}},
		{"schemas/local/v1/check-policy-query.params.schema.json", localapi.CheckPolicyQueryParams{}},
		{"schemas/local/v1/check-policy-configure.params.schema.json", localapi.CheckPolicyConfigureParams{}},
		{"schemas/local/v1/check-run.params.schema.json", localapi.CheckRunParams{}},
		{"schemas/local/v1/check-query.params.schema.json", localapi.CheckQueryParams{}},
		{"schemas/local/v1/check-logs.params.schema.json", localapi.CheckLogsParams{}},
		{"schemas/local/v1/check-watch.params.schema.json", localapi.CheckWatchParams{}},
		{"schemas/local/v1/check-repair-query.params.schema.json", localapi.CheckRepairQueryParams{}},
		{"schemas/local/v1/check-repair-decision.params.schema.json", localapi.CheckRepairDecisionParams{}},
		{"schemas/local/v1/run-start.params.schema.json", localapi.RunStartParams{}},
		{"schemas/local/v1/check-definition-mutation.result.schema.json", localapi.CheckDefinitionMutationResult{}},
		{"schemas/local/v1/check-definition-show.result.schema.json", localapi.CheckDefinitionShowResult{}},
		{"schemas/local/v1/check-definition-list.result.schema.json", localapi.CheckDefinitionListResult{}},
		{"schemas/local/v1/check-requirement-mutation.result.schema.json", localapi.CheckRequirementMutationResult{}},
		{"schemas/local/v1/check-requirement-list.result.schema.json", localapi.CheckRequirementListResult{}},
		{"schemas/local/v1/check-grant-mutation.result.schema.json", localapi.CheckGrantMutationResult{}},
		{"schemas/local/v1/check-grant-show.result.schema.json", localapi.CheckGrantShowResult{}},
		{"schemas/local/v1/check-grant-list.result.schema.json", localapi.CheckGrantListResult{}},
		{"schemas/local/v1/check-route-mutation.result.schema.json", localapi.CheckRouteMutationResult{}},
		{"schemas/local/v1/check-route-list.result.schema.json", localapi.CheckRouteListResult{}},
		{"schemas/local/v1/check-policy-mutation.result.schema.json", localapi.CheckPolicyMutationResult{}},
		{"schemas/local/v1/check-policy-show.result.schema.json", localapi.CheckPolicyShowResult{}},
		{"schemas/local/v1/check-run-mutation.result.schema.json", localapi.CheckRunMutationResult{}},
		{"schemas/local/v1/check-run-list.result.schema.json", localapi.CheckRunListResult{}},
		{"schemas/local/v1/check-inspect.result.schema.json", localapi.CheckInspectResult{}},
		{"schemas/local/v1/check-logs.result.schema.json", localapi.CheckLogsResult{}},
		{"schemas/local/v1/check-watch.result.schema.json", localapi.CheckWatchResult{}},
		{"schemas/local/v1/check-repair-mutation.result.schema.json", localapi.CheckRepairMutationResult{}},
		{"schemas/local/v1/check-repair-show.result.schema.json", localapi.CheckRepairShowResult{}},
		{"schemas/local/v1/check-repair-list.result.schema.json", localapi.CheckRepairListResult{}},
	} {
		assertCheckSchemaFields(t, test.path, test.value)
	}
}

func TestCheckRepairDecisionIsAClosedImmutableOwnerFact(t *testing.T) {
	t.Parallel()
	decision := readContextSchema(t, "schemas/domain/v1/check-repair-decision.schema.json")
	properties := decision["properties"].(map[string]any)
	if got := contractStringSlice(properties["decision"].(map[string]any)["enum"]); !reflect.DeepEqual(got, []string{"accepted", "rejected"}) {
		t.Fatalf("check repair decisions = %v", got)
	}
	if properties["created_by"].(map[string]any)["const"] != "local-owner" {
		t.Fatalf("check repair decision creator = %v", properties["created_by"])
	}
	note := properties["note"].(map[string]any)
	if note["maxLength"] != float64(4096) || note["pattern"] != `^[^\x00]*$` {
		t.Fatalf("check repair decision note bound = %#v", note)
	}
	if !strings.Contains(note["description"].(string), "4096 bytes") {
		t.Fatalf("check repair decision note omits encoded-byte caveat: %#v", note)
	}
	params := readContextSchema(t, "schemas/local/v1/check-repair-decision.params.schema.json")
	decisionNote := params["properties"].(map[string]any)["decision_note"].(map[string]any)
	if decisionNote["maxLength"] != float64(4096) || decisionNote["pattern"] != `^[^\x00]*$` ||
		!strings.Contains(decisionNote["description"].(string), "4096 encoded bytes") {
		t.Fatalf("check repair decision input note bound = %#v", decisionNote)
	}
	detail := readContextSchema(t, "schemas/domain/v1/check-repair-detail.schema.json")
	detailProperties := detail["properties"].(map[string]any)
	if _, exists := detailProperties["policy"]; exists {
		t.Fatal("check repair detail exposes mutable current policy next to frozen proposal policy revision")
	}
	if detailProperties["decision"].(map[string]any)["$ref"] != "check-repair-decision.schema.json" {
		t.Fatalf("check repair detail decision ref = %v", detailProperties["decision"])
	}
	for _, required := range contractStringSlice(detail["required"]) {
		if required == "decision" {
			t.Fatal("pending check repair detail requires an absent decision")
		}
	}
}

func TestCheckRepairProposalRequiresExactGrantedRunProvenance(t *testing.T) {
	t.Parallel()
	proposal := readContextSchema(t, "schemas/domain/v1/check-repair-proposal.schema.json")
	properties := proposal["properties"].(map[string]any)
	required := contractStringSlice(proposal["required"])
	for _, name := range []string{"source_run_id", "source_agent_id", "source_agent_revision", "source_grant_id", "source_grant_revision"} {
		if !containsContractString(required, name) {
			t.Errorf("check repair proposal does not require %s", name)
		}
	}
	for _, name := range []string{"created_by", "updated_by"} {
		if properties[name].(map[string]any)["pattern"] != `^agent:agent_[0-9a-f]{32}$` {
			t.Errorf("check repair proposal %s is not an exact agent actor: %v", name, properties[name])
		}
	}
	for name, bytes := range map[string]float64{"rationale": 4096, "repair_task_title": 256, "repair_task_description": 4096} {
		property := properties[name].(map[string]any)
		if property["maxLength"] != bytes || property["pattern"] != `^[^\x00]*$` || !strings.Contains(property["description"].(string), strconv.Itoa(int(bytes))+" encoded bytes") {
			t.Errorf("check repair proposal %s byte contract = %#v", name, property)
		}
	}
	comment, _ := proposal["$comment"].(string)
	for _, fact := range []string{"authenticated current-packet", "created_by", "source_agent_id"} {
		if !strings.Contains(comment, fact) {
			t.Errorf("check repair proposal provenance comment %q omits %q", comment, fact)
		}
	}
}

func TestCheckSchemasRejectUnknownFieldsAtEveryObjectBoundary(t *testing.T) {
	t.Parallel()
	paths := []string{
		"schemas/domain/v1/context-check-watch-grant.schema.json",
		"schemas/local/v1/run-start.params.schema.json",
		"schemas/local/v1/context-show.result.schema.json",
	}
	for _, pattern := range []string{"schemas/domain/v1/check-*.schema.json", "schemas/local/v1/check-*.schema.json"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		assertStrictCheckSchemaObjects(t, path, "$", readContextSchema(t, path))
	}
}

func assertStrictCheckSchemaObjects(t *testing.T, path, location string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "object" && typed["additionalProperties"] != false {
			t.Errorf("%s %s does not reject unknown fields", path, location)
		}
		for name, child := range typed {
			assertStrictCheckSchemaObjects(t, path, location+"."+name, child)
		}
	case []any:
		for index, child := range typed {
			assertStrictCheckSchemaObjects(t, path, location+"["+strconv.Itoa(index)+"]", child)
		}
	}
}

func assertCheckSchemaFields(t *testing.T, path string, value any) {
	t.Helper()
	document := readContextSchema(t, path)
	properties := document["properties"].(map[string]any)
	required := contractStringSlice(document["required"])
	sort.Strings(required)
	wantProperties, wantRequired := checkJSONFields(reflect.TypeOf(value))
	gotProperties := make([]string, 0, len(properties))
	for name := range properties {
		gotProperties = append(gotProperties, name)
	}
	sort.Strings(gotProperties)
	sort.Strings(wantProperties)
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Errorf("%s properties = %v, want Go fields %v", path, gotProperties, wantProperties)
	}
	if !reflect.DeepEqual(required, wantRequired) {
		t.Errorf("%s required = %v, want non-omitempty Go fields %v", path, required, wantRequired)
	}
}

func checkJSONFields(typeOf reflect.Type) ([]string, []string) {
	properties, required := []string{}, []string{}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		tag := field.Tag.Get("json")
		if field.Anonymous && tag == "" && field.Type.Kind() == reflect.Struct {
			embeddedProperties, embeddedRequired := checkJSONFields(field.Type)
			properties = append(properties, embeddedProperties...)
			required = append(required, embeddedRequired...)
			continue
		}
		parts := strings.Split(tag, ",")
		if parts[0] == "" || parts[0] == "-" {
			continue
		}
		properties = append(properties, parts[0])
		if len(parts) == 1 || parts[1] != "omitempty" {
			required = append(required, parts[0])
		}
	}
	return properties, required
}

func TestCheckInputsExcludeCallerSelectedAuthorityProcessAndDescriptiveFields(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/local/v1/check-definition-create.params.schema.json",
		"schemas/local/v1/check-requirement-create.params.schema.json",
		"schemas/local/v1/check-grant-create.params.schema.json",
		"schemas/local/v1/check-route-create.params.schema.json",
		"schemas/local/v1/check-run.params.schema.json",
		"schemas/local/v1/check-watch.params.schema.json",
	} {
		properties := readContextSchema(t, path)["properties"].(map[string]any)
		for _, forbidden := range []string{"actor", "agent_role", "role", "launch_profile_purpose", "purpose", "environment", "stdin", "command", "provider", "mcp", "credential"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s exposes forbidden caller field %q", path, forbidden)
			}
		}
	}
}

func TestCheckDomainContractsDoNotEncodeRoleOrPurposeAuthority(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{"schemas/domain/v1/check-*.schema.json", "schemas/local/v1/check-*.schema.json"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{`"role"`, `"agent_role"`, `"purpose"`, `"launch_profile_purpose"`} {
				if strings.Contains(string(data), forbidden) {
					t.Errorf("%s encodes descriptive %s as check contract input or state", path, forbidden)
				}
			}
		}
	}
}

func TestCheckDefinitionRevisionTupleSchemasMatchGoFields(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path       string
		definition string
		value      any
	}{
		{path: "schemas/local/v1/check-grant-create.params.schema.json", definition: "definition", value: localapi.CheckDefinitionContentRevisionParam{}},
		{path: "schemas/domain/v1/check-watch-grant.schema.json", definition: "definition", value: domain.CheckWatchGrantDefinition{}},
	} {
		document := readContextSchema(t, test.path)
		definition := document["$defs"].(map[string]any)[test.definition].(map[string]any)
		properties := definition["properties"].(map[string]any)
		required := contractStringSlice(definition["required"])
		typeOf := reflect.TypeOf(test.value)
		want := make([]string, 0, typeOf.NumField())
		for index := 0; index < typeOf.NumField(); index++ {
			want = append(want, strings.Split(typeOf.Field(index).Tag.Get("json"), ",")[0])
		}
		got := make([]string, 0, len(properties))
		for name := range properties {
			got = append(got, name)
		}
		sort.Strings(got)
		sort.Strings(want)
		sort.Strings(required)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(required, want) {
			t.Errorf("%s tuple fields/properties/required = %v/%v/%v", test.path, want, got, required)
		}
	}
}

func TestRunStartCheckWatchGrantIsExactPairedAndContextExclusive(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/local/v1/run-start.params.schema.json")
	properties := document["properties"].(map[string]any)
	for _, name := range []string{"check_watch_grant", "expected_check_watch_grant_revision"} {
		if _, exists := properties[name]; !exists {
			t.Fatalf("run.start schema omits %s", name)
		}
	}
	dependent := document["dependentRequired"].(map[string]any)
	if !containsContractString(contractStringSlice(dependent["check_watch_grant"]), "expected_check_watch_grant_revision") ||
		!containsContractString(contractStringSlice(dependent["expected_check_watch_grant_revision"]), "check_watch_grant") {
		t.Fatal("run.start check-watch grant fields are not an exact pair")
	}
	forbiddenPair := contractStringSlice(document["not"].(map[string]any)["required"])
	if !containsContractString(forbiddenPair, "context") || !containsContractString(forbiddenPair, "check_watch_grant") {
		t.Fatal("run.start does not forbid caller-selected context with a check-watch grant")
	}
}

func TestCheckPolicyAndWatchBoundsMatchDurableContracts(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/check-policy.schema.json",
		"schemas/local/v1/check-policy-configure.params.schema.json",
	} {
		document := readContextSchema(t, path)
		properties := document["properties"].(map[string]any)
		name := "max_open_repair_proposals"
		if strings.Contains(path, "configure") {
			name = "max_open_repairs"
		}
		bound := properties[name].(map[string]any)
		if bound["minimum"] != float64(1) || bound["maximum"] != float64(32) {
			t.Errorf("%s %s bounds = %v, want 1..32", path, name, bound)
		}
		if len(document["allOf"].([]any)) == 0 {
			t.Errorf("%s does not bind enabled repair policy to an exact profile pair", path)
		}
	}
	watch := readContextSchema(t, "schemas/domain/v1/check-watch-receipt.schema.json")
	properties := watch["properties"].(map[string]any)
	for _, name := range []string{"notifications_created", "route_failures_created"} {
		if maximum := properties[name].(map[string]any)["maximum"]; maximum != float64(300) {
			t.Errorf("check-watch receipt %s maximum = %v, want 300", name, maximum)
		}
	}
}

func TestCheckLaunchAndArtifactSchemasBindConditionalFields(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/check-launch-receipt.schema.json",
		"schemas/domain/v1/check-artifact.schema.json",
		"schemas/domain/v1/check-captured-log.schema.json",
	} {
		document := readContextSchema(t, path)
		if len(document["allOf"].([]any)) == 0 {
			t.Errorf("%s omits its boolean-dependent field contract", path)
		}
	}
}

func TestCheckLaunchReceiptPreflightCodesMatchClosedWorkerContract(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/check-launch-receipt.schema.json")
	conditional := document["allOf"].([]any)[0].(map[string]any)
	nonlaunchable := conditional["else"].(map[string]any)["properties"].(map[string]any)["preflight_failure_code"].(map[string]any)
	got := contractStringSlice(nonlaunchable["enum"])
	want := []string{
		domain.CheckPreflightWorkingDirectoryInvalid,
		domain.CheckPreflightAuthorityRevoked,
		domain.CheckPreflightDefinitionRetired,
		domain.CheckPreflightRequirementRetired,
		domain.CheckPreflightCheckoutChanged,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonlaunchable check receipt codes = %v, want exact closed set %v", got, want)
	}
}

func TestCheckRunViewsExposeFullFreshnessAndRunningState(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/task-check-requirement-view.schema.json",
		"schemas/domain/v1/check-run-list-item.schema.json",
		"schemas/domain/v1/check-run-detail.schema.json",
	} {
		document := readContextSchema(t, path)
		properties := document["properties"].(map[string]any)
		state := properties["state"]
		if state == nil {
			state = properties["requirement_state"]
		}
		if !containsContractString(contractStringSlice(state.(map[string]any)["enum"]), domain.CheckRequirementRunning) {
			t.Errorf("%s omits running requirement state", path)
		}
	}
	detail := readContextSchema(t, "schemas/domain/v1/check-run-detail.schema.json")
	properties := detail["properties"].(map[string]any)
	history := properties["freshness_history"].(map[string]any)
	if history["type"] != "array" || history["items"].(map[string]any)["$ref"] != "check-result-freshness.schema.json" {
		t.Fatalf("check-run detail freshness_history = %#v, want typed full history", history)
	}
	if _, capped := history["maxItems"]; capped {
		t.Fatalf("check-run detail freshness_history is capped rather than complete: %#v", history)
	}
	if !containsContractString(contractStringSlice(detail["required"]), "freshness_history") {
		t.Fatal("check-run detail does not require an explicit freshness history array")
	}
}

func TestCheckGitObservationBoundsMatchFrozenInspectorContract(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/check-git-observation.schema.json")
	dirtyPaths := document["properties"].(map[string]any)["dirty_paths"].(map[string]any)
	if dirtyPaths["maxItems"] != float64(256) || dirtyPaths["items"].(map[string]any)["maxLength"] != float64(1024) {
		t.Fatalf("dirty_paths bounds = %#v, want 256 items of at most 1024 characters", dirtyPaths)
	}
	comment, _ := document["$comment"].(string)
	if !strings.Contains(comment, "256 KiB aggregate UTF-8") || !strings.Contains(comment, "semantic validation") {
		t.Fatalf("dirty_paths aggregate semantic-validation note = %q", comment)
	}
}

func TestCheckRequirementStatementBoundMatchesOwnerMutation(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/task-check-requirement.schema.json",
		"schemas/local/v1/check-requirement-create.params.schema.json",
	} {
		statement := readContextSchema(t, path)["properties"].(map[string]any)["statement"].(map[string]any)
		if statement["maxLength"] != float64(2048) {
			t.Errorf("%s statement maximum = %v, want 2048", path, statement["maxLength"])
		}
	}
}

func TestCheckLifecycleReasonBoundsMatchOwnerMutations(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/local/v1/check-definition-retire.params.schema.json",
		"schemas/local/v1/check-requirement-retire.params.schema.json",
		"schemas/local/v1/check-grant-revoke.params.schema.json",
		"schemas/local/v1/check-route-retire.params.schema.json",
	} {
		reason := readContextSchema(t, path)["properties"].(map[string]any)["reason"].(map[string]any)
		if reason["maxLength"] != float64(2048) {
			t.Errorf("%s reason maximum = %v, want 2048", path, reason["maxLength"])
		}
	}
}

func TestDurableCheckWatchGrantDeclaresCanonicalSemanticValidation(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/check-watch-grant.schema.json")
	comment, _ := document["$comment"].(string)
	for _, rule := range []string{"canonical operation order", "definitions sorted uniquely by definition_id", "max_in_flight <= max_pending"} {
		if !strings.Contains(comment, rule) {
			t.Errorf("durable check-watch grant semantic note %q omits %q", comment, rule)
		}
	}
	definitions := document["properties"].(map[string]any)["definitions"].(map[string]any)
	if definitions["uniqueItems"] != true {
		t.Fatal("durable check-watch grant schema does not enforce shape-level definition uniqueness")
	}
}

func TestCheckRunSelectorsUseExactEntityIdentifiers(t *testing.T) {
	t.Parallel()
	run := readContextSchema(t, "schemas/local/v1/check-run.params.schema.json")["properties"].(map[string]any)
	if run["checkout"].(map[string]any)["pattern"] != `^co_[0-9a-f]{32}$` {
		t.Errorf("check.run checkout selector = %v", run["checkout"])
	}
	list := readContextSchema(t, "schemas/local/v1/check-list.params.schema.json")["properties"].(map[string]any)
	if list["definition"].(map[string]any)["pattern"] != `^checkdef_[0-9a-f]{32}$` {
		t.Errorf("check.list definition selector = %v", list["definition"])
	}
}
