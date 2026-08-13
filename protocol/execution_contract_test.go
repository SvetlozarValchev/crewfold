package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/execution"
	"crewfold/internal/herdr"
	"crewfold/internal/localapi"
)

func TestExecutionSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()

	for path, expectedID := range map[string]string{
		"schemas/fixture/v1/fake-run-scenario.schema.json":           execution.FakeScenarioSchema,
		"schemas/local/v1/run-mutation.result.schema.json":           localapi.RunMutationSchema,
		"schemas/local/v1/run-show.result.schema.json":               localapi.RunShowSchema,
		"schemas/local/v1/run-list.result.schema.json":               localapi.RunListSchema,
		"schemas/local/v1/run-logs.result.schema.json":               localapi.RunLogsSchema,
		"schemas/local/v1/run-control.result.schema.json":            localapi.RunControlSchema,
		"schemas/local/v1/run-attach.result.schema.json":             localapi.RunAttachSchema,
		"schemas/cli/v1/doctor-runtime.response.schema.json":         herdr.ProbeSchema,
		"schemas/cli/v1/doctor-provider.response.schema.json":        execution.CodexProbeSchema,
		"schemas/cli/v1/doctor-claude-provider.response.schema.json": execution.ClaudeProbeSchema,
		"schemas/local/v1/task-timeline.result.schema.json":          localapi.TaskTimelineSchema,
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

func TestCheckedInExecutionScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/execution/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 5 {
		t.Fatalf("execution scenario count = %d, want 5; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInDirectRuntimeScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/direct-runtime/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 8 {
		t.Fatalf("direct-runtime scenario count = %d, want 8; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInAgentMessagingScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/agent-messaging/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("agent messaging scenario count = %d, want 2; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
}

func TestCheckedInCrossProjectCollaborationScenariosSatisfySemanticContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../test/fixtures/cross-project-collaboration/*.json")
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("cross-project scenario count = %d, want 2; paths = %v", len(paths), paths)
	}
	for _, path := range paths {
		if _, err := execution.LoadScenario(path); err != nil {
			t.Errorf("execution.LoadScenario(%q) error = %v", path, err)
		}
	}
	templatePath := "../test/fixtures/cross-project-collaboration/engine.json.in"
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(data), "THREAD_ID", "thread_00000000000000000000000000000001")
	renderedPath := filepath.Join(t.TempDir(), "engine.json")
	if err := os.WriteFile(renderedPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.LoadScenario(renderedPath); err != nil {
		t.Errorf("execution.LoadScenario(rendered engine fixture) error = %v", err)
	}
}

func TestCheckedInCuratorAgentScenarioKeepsAuthorityFieldsOutOfFixture(t *testing.T) {
	t.Parallel()
	if _, err := execution.LoadScenario("../test/fixtures/curator/agent-proposal.json"); err != nil {
		t.Fatalf("execution.LoadScenario(curator agent proposal) error = %v", err)
	}
	if _, err := execution.LoadScenario("../test/fixtures/curator/forged-source.json"); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("execution.LoadScenario(curator forged source) error = %v, want strict unknown-field rejection", err)
	}
}

func TestKnowledgeContradictionScenarioTemplateRendersStrictDynamicRevisions(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../test/scenarios/knowledge-contradictions/report.json.in")
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.NewReplacer(
		"__LEFT_REVISION__", "krev_00000000000000000000000000000001",
		"__RIGHT_REVISION__", "krev_00000000000000000000000000000002",
	).Replace(string(data))
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	scenario, err := execution.LoadScenario(path)
	if err != nil {
		t.Fatalf("execution.LoadScenario(rendered contradiction fixture) error = %v", err)
	}
	if !scenario.Contradiction.ReportReceived || !scenario.Contradiction.ConfirmDenied || scenario.Contradiction.Report == nil {
		t.Fatalf("rendered contradiction assertions = %#v", scenario.Contradiction)
	}
}

func TestFixtureKnowledgeSchemaCannotSelectAuthorityOrProvenance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/fixture/v1/fake-run-scenario.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			AdditionalProperties *bool                      `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	proposal, ok := schema.Definitions["knowledge_proposal"]
	if !ok {
		t.Fatal("fixture schema omits the scoped knowledge proposal")
	}
	if proposal.AdditionalProperties == nil || *proposal.AdditionalProperties {
		t.Fatal("fixture knowledge proposal permits unknown authority fields")
	}
	want := map[string]bool{
		"type": true, "title": true, "body": true, "confidence": true,
		"verification_status": true, "freshness_policy": true,
	}
	if len(proposal.Properties) != len(want) {
		t.Fatalf("fixture knowledge proposal properties = %v, want exact bounded set %v", proposal.Properties, want)
	}
	for name := range proposal.Properties {
		if !want[name] {
			t.Errorf("fixture knowledge proposal unexpectedly exposes %q", name)
		}
	}
}

func TestFixtureContradictionSchemaIsStrictAndCannotSelectAuthority(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/fixture/v1/fake-run-scenario.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties  map[string]json.RawMessage `json:"properties"`
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	type strictObject struct {
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	var plan strictObject
	if err := json.Unmarshal(schema.Properties["contradiction"], &plan); err != nil {
		t.Fatal(err)
	}
	if plan.AdditionalProperties == nil || *plan.AdditionalProperties {
		t.Fatal("fixture contradiction permits unknown authority fields")
	}
	wantPlan := map[string]bool{"report": true, "report_received": true, "confirm_denied": true}
	if len(plan.Properties) != len(wantPlan) || len(plan.Required) != len(wantPlan) {
		t.Fatalf("fixture contradiction properties=%v required=%v, want exact %v", plan.Properties, plan.Required, wantPlan)
	}
	for name := range wantPlan {
		if _, exists := plan.Properties[name]; !exists || !containsKnowledgeString(plan.Required, name) {
			t.Errorf("fixture contradiction does not require exact field %q", name)
		}
	}
	for _, name := range []string{"report_received", "confirm_denied"} {
		var assertion struct {
			Const bool `json:"const"`
		}
		if err := json.Unmarshal(plan.Properties[name], &assertion); err != nil || !assertion.Const {
			t.Errorf("fixture contradiction assertion %q=%#v error=%v, want const true", name, assertion, err)
		}
	}

	var report strictObject
	if err := json.Unmarshal(schema.Definitions["contradiction_report"], &report); err != nil {
		t.Fatal(err)
	}
	if report.AdditionalProperties == nil || *report.AdditionalProperties {
		t.Fatal("fixture contradiction report permits unknown authority fields")
	}
	wantReport := map[string]bool{"left_revision": true, "right_revision": true, "reason": true}
	if len(report.Properties) != len(wantReport) || len(report.Required) != len(wantReport) {
		t.Fatalf("fixture contradiction report properties=%v required=%v, want exact %v", report.Properties, report.Required, wantReport)
	}
	for name := range wantReport {
		if _, exists := report.Properties[name]; !exists || !containsKnowledgeString(report.Required, name) {
			t.Errorf("fixture contradiction report does not require exact field %q", name)
		}
	}
	for _, forbidden := range []string{"actor", "workspace", "project", "task", "run", "status"} {
		if _, exists := report.Properties[forbidden]; exists {
			t.Errorf("fixture contradiction report exposes authority field %q", forbidden)
		}
	}
}

func TestLiveContextDeltaScenarioTemplateRendersTypedKnowledgeTransitions(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../test/fixtures/live-context-deltas/main.json.in")
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.NewReplacer(
		"@DELTA_THREAD@", "thread_00000000000000000000000000000001",
		"@MESSAGE_PREVIEW@", "bounded preview",
		"@NEW_REVISION@", "krev_00000000000000000000000000000001",
		"@REVISION_A@", "krev_00000000000000000000000000000002",
		"@REVISION_B@", "krev_00000000000000000000000000000003",
		"@LIVE_DEPENDENT@", "task_00000000000000000000000000000001",
		"@CONTRADICTION@", "kcon_00000000000000000000000000000001",
	).Replace(string(data))
	path := filepath.Join(t.TempDir(), "live-context.json")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	scenario, err := execution.LoadScenario(path)
	if err != nil {
		t.Fatalf("execution.LoadScenario(live context fixture) error = %v", err)
	}
	if len(scenario.ContextDelta.Expectations) != 3 {
		t.Fatalf("live context expectations = %d, want 3", len(scenario.ContextDelta.Expectations))
	}
	withdrawal := scenario.ContextDelta.Expectations[1]
	reoffer := scenario.ContextDelta.Expectations[2]
	if len(withdrawal.WithdrawalRevisionIDs) != 2 || len(reoffer.KnowledgeRevisionIDs) != 2 {
		t.Fatalf("withdrawal IDs=%v reoffer IDs=%v, want two exact revisions each", withdrawal.WithdrawalRevisionIDs, reoffer.KnowledgeRevisionIDs)
	}
}

func TestFixtureContextDeltaSchemaIsStrictAndCannotSelectAuthority(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/fixture/v1/fake-run-scenario.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties  map[string]json.RawMessage `json:"properties"`
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	type strictObject struct {
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	var plan, expectation strictObject
	if err := json.Unmarshal(schema.Properties["context_delta"], &plan); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schema.Definitions["context_delta_expectation"], &expectation); err != nil {
		t.Fatal(err)
	}
	for name, object := range map[string]strictObject{"context_delta": plan, "context_delta_expectation": expectation} {
		if object.AdditionalProperties == nil || *object.AdditionalProperties {
			t.Errorf("fixture %s permits unknown authority fields", name)
		}
		for _, forbidden := range []string{"actor", "workspace", "project", "task", "run", "agent", "cursor"} {
			if _, exists := object.Properties[forbidden]; exists {
				t.Errorf("fixture %s exposes authority field %q", name, forbidden)
			}
		}
	}
	for _, field := range []string{"knowledge_revision_ids", "withdrawal_revision_ids"} {
		if _, exists := expectation.Properties[field]; !exists {
			t.Errorf("fixture delta expectation omits typed field %q", field)
		}
	}
}
