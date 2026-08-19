package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestM23WorkProposalToolStagesACompleteInertTeam(t *testing.T) {
	t.Parallel()
	var proposal map[string]any
	for _, namespace := range domainAgentDynamicToolSpecs() {
		for _, tool := range namespace.Tools {
			if tool.Name == domainToolProposeWork {
				proposal = tool.InputSchema
			}
		}
	}
	if proposal == nil {
		t.Fatal("crewfold_propose_work tool is missing")
	}
	properties := proposal["properties"].(map[string]any)
	for _, internal := range []string{"staffing_grant_id", "primary_checkout_id", "primary_checkout_revision", "objective_budget"} {
		if _, exists := properties[internal]; exists {
			t.Fatalf("proposal tool leaked internal field %q: %#v", internal, properties)
		}
	}
	agents := properties["agents"].(map[string]any)
	agent := agents["items"].(map[string]any)
	variants, ok := agent["oneOf"].([]map[string]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("proposal agent variants = %#v", agent["oneOf"])
	}
	newAgentProperties := variants[1]["properties"].(map[string]any)
	for _, internal := range []string{"manager_key", "provider", "runtime", "budget", "existing_membership_revision", "existing_launch_profile_id"} {
		if _, exists := newAgentProperties[internal]; exists {
			t.Fatalf("proposal agent leaked internal field %q: %#v", internal, newAgentProperties)
		}
	}
	tasks := properties["tasks"].(map[string]any)["items"].(map[string]any)
	required := tasks["required"].([]string)
	taskProperties := tasks["properties"].(map[string]any)
	if _, exists := taskProperties["budget"]; exists {
		t.Fatalf("proposal task leaked an internal budget: %#v", taskProperties)
	}
	description := strings.ToLower(proposalDescription(domainToolProposeWork))
	if !containsRequiredString(required, "assignee_key") || !strings.Contains(description, "do not exist before acceptance") || !strings.Contains(description, "missing parent_key") || !strings.Contains(description, "never invent") {
		t.Fatalf("proposal tool does not freeze an inert logical team: required=%#v description=%q", required, proposalDescription(domainToolProposeWork))
	}
}

func TestM23WorkProposalToolAcceptsIntentAndRejectsInternalGuesswork(t *testing.T) {
	t.Parallel()
	concise := `{
		"summary":"Build and independently verify the first slice",
		"objective_title":"Deliver the first slice",
		"agents":[
			{"key":"lead","name":"signal-lead","role":"delivery lead","operating_charter":"Coordinate the bounded slice and its evidence.","task_class":"coordination"},
			{"key":"builder","name":"signal-builder","role":"implementer","parent_key":"lead","operating_charter":"Implement the accepted slice in the attached checkout.","task_class":"implementation"},
			{"key":"reviewer","name":"signal-reviewer","role":"reviewer","parent_key":"lead","operating_charter":"Review the completed slice independently.","task_class":"review"}
		],
		"tasks":[
			{"key":"build","title":"Build the slice","assignee_key":"builder"},
			{"key":"review","title":"Review the slice","assignee_key":"reviewer","depends_on":["build"]}
		]
	}`
	value, err := decodeDomainProposeWorkArguments(json.RawMessage(concise))
	if err != nil {
		t.Fatalf("decode concise work intent: %v", err)
	}
	if value.Agents[0].ParentKey != "" || value.Agents[1].ParentKey != "lead" || value.Tasks[1].DependencyDelivery["build"] != "" {
		t.Fatalf("decoded concise work intent = %#v", value)
	}
	naturalLabels := strings.NewReplacer(
		`"key":"lead"`, `"key":"Lead Agent"`,
		`"key":"builder"`, `"key":"builder_agent"`,
		`"key":"reviewer"`, `"key":"Reviewer Agent"`,
		`"parent_key":"lead"`, `"parent_key":"Lead Agent"`,
		`"key":"build"`, `"key":"Build Slice"`,
		`"key":"review"`, `"key":"Review_Slice"`,
		`"assignee_key":"builder"`, `"assignee_key":"builder_agent"`,
		`"assignee_key":"reviewer"`, `"assignee_key":"Reviewer Agent"`,
		`"depends_on":["build"]`, `"depends_on":["Build Slice"]`,
	).Replace(concise)
	natural, err := decodeDomainProposeWorkArguments(json.RawMessage(naturalLabels))
	if err != nil {
		t.Fatalf("decode natural proposal labels: %v", err)
	}
	if natural.Agents[0].Key != "lead-agent" || natural.Agents[1].Key != "builder-agent" || natural.Tasks[1].Key != "review-slice" || natural.Tasks[1].AssigneeKey != "reviewer-agent" || natural.Tasks[1].DependsOn[0] != "build-slice" {
		t.Fatalf("normalized natural proposal labels = %#v", natural)
	}

	for name, payload := range map[string]string{
		"manager key":         strings.Replace(concise, `"task_class":"coordination"`, `"task_class":"coordination","manager_key":"coordinator"`, 1),
		"grant id":            strings.Replace(concise, `"summary":`, `"staffing_grant_id":"staffgrant_00000000000000000000000000000000","summary":`, 1),
		"membership revision": strings.Replace(concise, `"task_class":"coordination"`, `"task_class":"coordination","existing_agent_revision":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDomainProposeWorkArguments(json.RawMessage(payload)); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("internal guesswork error = %v", err)
			}
		})
	}
}

func TestM23WorkProposalResolverBindsExactAuthorityWithoutModelSuppliedInternals(t *testing.T) {
	t.Parallel()
	scope := domain.DomainAgentSessionScope{
		Agent:      domain.AgentDefinition{ID: "agent_source", Revision: 7},
		Membership: domain.DomainAgentMembership{Revision: 4},
	}
	checkout := domain.Checkout{ID: "co_primary", Path: "/work/signal", Revision: 9, Availability: domain.CheckoutAvailable, WriteMode: domain.WriteModeShared}
	existing := domain.DomainAgent{
		Definition: domain.AgentDefinition{ID: "agent_reviewer", Name: "existing-reviewer", Enabled: true, MaxConcurrency: 1, Revision: 3},
		Membership: domain.DomainAgentMembership{AgentID: "agent_reviewer", Status: domain.DomainAgentActive, Revision: 2},
	}
	grant := domain.DomainAgentStaffingGrant{
		ID: "staffgrant_current", ManagerMembershipRevision: 4, Status: domain.DomainStaffingGrantActive,
		Profiles:    []domain.DomainAgentStaffingProfile{{Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 2}},
		TaskClasses: []string{"coordination", "review"},
		Budget:      domain.Budget{TokenLimit: 300, CostCents: 30, TimeSeconds: 600},
	}
	profiles := []domain.LaunchProfile{{
		ID: "launch_reviewer", AgentID: existing.Definition.ID, AgentRevision: existing.Definition.Revision,
		CheckoutID: checkout.ID, Purpose: "review", Provider: "codex-subscription", Runtime: "herdr", Status: domain.LaunchProfileActive,
	}}
	arguments := domainSessionProposeWorkArguments{
		Summary: "Coordinate and review", ObjectiveTitle: "Deliver Signal Garden",
		Agents: []domainSessionProposeWorkAgentArguments{
			{Key: "lead", Name: "signal-lead", Role: "lead", OperatingCharter: "Coordinate the accepted work.", TaskClass: "coordination"},
			{Key: "reviewer", ExistingAgent: "existing-reviewer", TaskClass: "review"},
		},
		Tasks: []domainSessionProposeWorkTaskArguments{
			{Key: "coordinate", Title: "Coordinate delivery", AssigneeKey: "lead"},
			{Key: "review", Title: "Review delivery", AssigneeKey: "reviewer", DependsOn: []string{"coordinate"}},
		},
	}
	content, err := resolveDomainWorkWithGrant(scope, checkout, nil, []domain.DomainAgent{existing}, profiles, grant, arguments)
	if err != nil {
		t.Fatalf("resolveDomainWorkWithGrant() error = %v", err)
	}
	if content.PrimaryCheckoutID != checkout.ID || content.PrimaryCheckoutRevision != checkout.Revision || content.ObjectiveBudget != grant.Budget {
		t.Fatalf("resolved checkout/budget = %#v", content)
	}
	if got := content.Agents[0]; got.Provider != "codex-subscription" || got.Runtime != "herdr" || got.MaxConcurrency != 1 || got.DelegationPolicy != domain.DomainAgentHandsOn || got.Budget != grant.Budget {
		t.Fatalf("resolved new agent = %#v", got)
	}
	if got := content.Agents[1]; got.ExistingAgentID != existing.Definition.ID || got.ExistingMembershipRevision != existing.Membership.Revision || got.ExistingLaunchProfileID != profiles[0].ID || got.Budget != (domain.Budget{}) {
		t.Fatalf("resolved existing agent = %#v", got)
	}
	if content.Tasks[0].Priority != 100 || content.Tasks[1].Priority != 95 || content.Tasks[1].DependencyDelivery["coordinate"] != domain.DependencyDeliveryHandoffWithEvidence {
		t.Fatalf("resolved tasks = %#v", content.Tasks)
	}
	if content.Tasks[0].Budget != (domain.Budget{TokenLimit: 150, CostCents: 15, TimeSeconds: 300}) || content.Tasks[1].Budget != content.Tasks[0].Budget {
		t.Fatalf("resolved task budgets = %#v", content.Tasks)
	}
}

func proposalDescription(name string) string {
	for _, namespace := range domainAgentDynamicToolSpecs() {
		for _, tool := range namespace.Tools {
			if tool.Name == name {
				return tool.Description
			}
		}
	}
	return ""
}

func containsRequiredString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestM22DurableAgentMessageRequiresAnExplicitNewOrExistingTopic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name:    "new topic",
			payload: `{"recipient_agent":"fern","kind":"question","new_topic":true,"subject":"Shared boundary","body":"Freeze the interface."}`,
		},
		{
			name:    "continue topic",
			payload: `{"recipient_agent":"fern","kind":"inform","new_topic":false,"thread_id":"thread_00000000000000000000000000000001","body":"The boundary remains frozen."}`,
		},
		{
			name:    "implicit new topic denied",
			payload: `{"recipient_agent":"fern","kind":"inform","new_topic":false,"body":"This must not silently create a thread."}`,
			wantErr: "existing thread identifier",
		},
		{
			name:    "new topic without subject denied",
			payload: `{"recipient_agent":"fern","kind":"inform","new_topic":true,"body":"No stable subject."}`,
			wantErr: "requires a concise subject",
		},
		{
			name:    "mixed intent denied",
			payload: `{"recipient_agent":"fern","kind":"inform","new_topic":true,"subject":"New","thread_id":"thread_00000000000000000000000000000001","body":"Ambiguous."}`,
			wantErr: "cannot also continue",
		},
		{
			name:    "continuation cannot rename topic",
			payload: `{"recipient_agent":"fern","kind":"inform","new_topic":false,"subject":"Rename","thread_id":"thread_00000000000000000000000000000001","body":"Ambiguous."}`,
			wantErr: "retains its original subject",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeDomainSendMessageArguments(json.RawMessage(test.payload))
			if test.wantErr == "" && err != nil {
				t.Fatalf("decodeDomainSendMessageArguments() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("decodeDomainSendMessageArguments() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
