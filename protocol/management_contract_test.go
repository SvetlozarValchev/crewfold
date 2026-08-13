package protocol_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestManagerSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expected := range map[string]string{
		"schemas/domain/v1/context-manager-grant.schema.json":           domain.ContextManagerGrantSchema,
		"schemas/local/v1/manager-grant-mutation.result.schema.json":    localapi.ManagerGrantMutationSchema,
		"schemas/local/v1/manager-invocation.result.schema.json":        localapi.ManagerInvocationSchema,
		"schemas/local/v1/manager-proposal-mutation.result.schema.json": localapi.ProposalMutationSchema,
		"schemas/local/v1/supervisor-run.result.schema.json":            localapi.SupervisorRunSchema,
		"schemas/local/v1/approval-mutation.result.schema.json":         localapi.ApprovalMutationSchema,
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

func TestManagementSchemasCoverEveryGoJSONField(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path  string
		value any
	}{
		{path: "schemas/domain/v1/manager-grant.schema.json", value: domain.ManagerGrant{}},
		{path: "schemas/domain/v1/launch-profile.schema.json", value: domain.LaunchProfile{}},
		{path: "schemas/domain/v1/manager-proposal.schema.json", value: domain.ManagerProposal{}},
		{path: "schemas/domain/v1/supervisor-action.schema.json", value: domain.SupervisorAction{}},
	} {
		document := readContextSchema(t, test.path)
		properties := document["properties"].(map[string]any)
		required := contractStringSlice(document["required"])
		sort.Strings(required)
		typeOf := reflect.TypeOf(test.value)
		wantProperties, wantRequired := make([]string, 0, typeOf.NumField()), make([]string, 0, typeOf.NumField())
		for index := 0; index < typeOf.NumField(); index++ {
			tag := typeOf.Field(index).Tag.Get("json")
			parts := strings.Split(tag, ",")
			if parts[0] == "" || parts[0] == "-" {
				continue
			}
			wantProperties = append(wantProperties, parts[0])
			if len(parts) == 1 || parts[1] != "omitempty" {
				wantRequired = append(wantRequired, parts[0])
			}
		}
		gotProperties := make([]string, 0, len(properties))
		for name := range properties {
			gotProperties = append(gotProperties, name)
		}
		sort.Strings(gotProperties)
		sort.Strings(wantProperties)
		sort.Strings(wantRequired)
		if !reflect.DeepEqual(gotProperties, wantProperties) {
			t.Errorf("%s properties = %v, want Go fields %v", test.path, gotProperties, wantProperties)
		}
		if !reflect.DeepEqual(required, wantRequired) {
			t.Errorf("%s required = %v, want non-omitempty Go fields %v", test.path, required, wantRequired)
		}
	}
}

func TestCurrentPacketFreezesManagerGrantDerivedToolsWithoutRoleAuthority(t *testing.T) {
	t.Parallel()
	packet := readContextSchema(t, "schemas/domain/v1/context-packet.schema.json")
	required := contractStringSlice(packet["required"])
	if containsContractString(required, "management_grant") {
		t.Fatal("current packet requires delegated manager authority")
	}
	properties := packet["properties"].(map[string]any)
	if _, exists := properties["role_authority"]; exists {
		t.Fatal("current packet exposes role authority")
	}
	policy := packet["$defs"].(map[string]any)["policy"].(map[string]any)["properties"].(map[string]any)
	allowed := policy["allowed_tools"].(map[string]any)
	suffix := contractStringSlice(allowed["items"].(map[string]any)["enum"])
	for _, wanted := range []string{"crewfold_propose_assignment", "crewfold_propose_escalation", "crewfold_propose_review", "crewfold_propose_tasks"} {
		if !containsContractString(suffix, wanted) {
			t.Errorf("current packet proposal suffix omits %q: %v", wanted, suffix)
		}
	}
	grant := readContextSchema(t, "schemas/domain/v1/context-manager-grant.schema.json")
	grantProperties := grant["properties"].(map[string]any)
	grantRequired := contractStringSlice(grant["required"])
	if !containsContractString(grantRequired, "objective_revision") || grantProperties["objective_revision"].(map[string]any)["minimum"] != float64(1) {
		t.Fatal("current packet manager grant snapshot does not require a positive objective_revision")
	}
	packetGrant := properties["management_grant"].(map[string]any)
	if packetGrant["$ref"] != "context-manager-grant.schema.json" {
		t.Fatalf("current packet management_grant ref = %v; want exact context-manager-grant schema", packetGrant["$ref"])
	}
	for _, forbidden := range []string{"role", "purpose", "eligible_agents", "runtime", "provider", "scenario", "checkout_id"} {
		if _, exists := grantProperties[forbidden]; exists {
			t.Errorf("manager grant snapshot exposes forbidden authority field %q", forbidden)
		}
	}
}

func TestManagerAuthoritySchemasRequirePositiveObjectiveRevision(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/manager-grant.schema.json",
		"schemas/domain/v1/context-manager-grant.schema.json",
		"schemas/domain/v1/manager-proposal.schema.json",
	} {
		document := readContextSchema(t, path)
		required := contractStringSlice(document["required"])
		property, exists := document["properties"].(map[string]any)["objective_revision"].(map[string]any)
		if !containsContractString(required, "objective_revision") || !exists || property["type"] != "integer" || property["minimum"] != float64(1) {
			t.Errorf("%s does not require a positive integer objective_revision", path)
		}
	}
}

func TestManagerMCPInputsAreProposalOnlyAndDerivedScope(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"propose-tasks", "propose-assignment", "propose-review", "propose-escalation"} {
		document := readContextSchema(t, "schemas/mcp/v1/"+name+".input.schema.json")
		properties := document["properties"].(map[string]any)
		for _, forbidden := range []string{"workspace", "project", "objective", "run", "agent", "runtime", "provider", "checkout", "scenario", "grant_id", "expected_grant_revision"} {
			if _, exists := properties[forbidden]; exists {
				t.Errorf("%s exposes trusted scope field %q", name, forbidden)
			}
		}
		actions := properties["actions"].(map[string]any)
		if actions["maxItems"] != float64(32) {
			t.Errorf("%s max actions = %v, want 32", name, actions["maxItems"])
		}
	}
	if _, err := os.Stat("schemas/mcp/v1/accept-manager-proposal.input.schema.json"); !os.IsNotExist(err) {
		t.Fatal("reserved manager proposal acceptance must not be published as an MCP input schema")
	}
}

func TestStoredManagerActionsRequireCanonicalCrewfoldIdentity(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/manager-proposal-action.schema.json")
	definitions := document["$defs"].(map[string]any)
	for _, name := range []string{"create_task_action", "add_dependency_action", "declare_claim_requirement_action", "assign_task_action", "request_review_action", "request_action_action"} {
		branch := definitions[name].(map[string]any)
		required := contractStringSlice(branch["required"])
		if !containsContractString(required, "id") || !containsContractString(required, "ordinal") {
			t.Errorf("%s does not require server-owned id and ordinal", name)
		}
		properties := branch["properties"].(map[string]any)
		id := properties["id"].(map[string]any)
		ordinal := properties["ordinal"].(map[string]any)
		if id["pattern"] != "^mpact_[0-9a-f]{32}$" || ordinal["minimum"] != float64(0) || ordinal["maximum"] != float64(31) {
			t.Errorf("%s action identity schema = %#v / %#v", name, id, ordinal)
		}
	}
}

func TestSupervisorConcurrencySchemaCapsEveryDimensionAtOneHundred(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/supervisor-policy.schema.json")
	limits := document["$defs"].(map[string]any)["limits"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"max_active_runs", "max_starting_runs", "default_project_concurrency", "default_provider_concurrency"} {
		if limits[name].(map[string]any)["maximum"] != float64(100) {
			t.Errorf("%s maximum = %v, want 100", name, limits[name].(map[string]any)["maximum"])
		}
	}
	for _, name := range []string{"project_concurrency", "provider_concurrency"} {
		entry := limits[name].(map[string]any)["additionalProperties"].(map[string]any)
		if entry["maximum"] != float64(100) {
			t.Errorf("%s entry maximum = %v, want 100", name, entry["maximum"])
		}
	}
}

func TestSupervisorRetryActionSchemaDistinguishesPriorAndFreshRuns(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/supervisor-action.schema.json")
	properties := document["properties"].(map[string]any)
	required := contractStringSlice(document["required"])
	for _, name := range []string{"prior_run_id", "run_id"} {
		property, exists := properties[name].(map[string]any)
		if !exists || property["type"] != "string" || property["pattern"] != "^run_[0-9a-f]{32}$" {
			t.Errorf("%s schema = %#v; want optional Crewfold run ID", name, property)
		}
	}
	if containsContractString(required, "prior_run_id") {
		t.Fatal("prior_run_id must remain optional for non-retry supervisor actions")
	}
	for _, name := range []string{"condition_key", "content_sha256"} {
		property, exists := properties[name].(map[string]any)
		if !exists || property["pattern"] != "^[0-9a-f]{64}$" || !containsContractString(required, name) {
			t.Errorf("%s must be a required canonical SHA-256 value", name)
		}
	}
}

func TestSupervisorEscalationActionSchemaFreezesTypedProposalSource(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/supervisor-action.schema.json")
	properties := document["properties"].(map[string]any)
	for name, pattern := range map[string]string{
		"source_proposal_id": "^mprop_[0-9a-f]{32}$",
		"source_action_id":   "^mpact_[0-9a-f]{32}$",
	} {
		property, exists := properties[name].(map[string]any)
		if !exists || property["type"] != "string" || property["pattern"] != pattern {
			t.Errorf("%s schema = %#v; want optional typed source identity", name, property)
		}
	}
	conditions := contractStringSlice(properties["condition"].(map[string]any)["enum"])
	if !containsContractString(conditions, "manager_escalation") {
		t.Fatal("supervisor condition union omits manager_escalation")
	}
}
