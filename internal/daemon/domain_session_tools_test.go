package daemon

import (
	"encoding/json"
	"strings"
	"testing"
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
	agents := properties["agents"].(map[string]any)
	agent := agents["items"].(map[string]any)
	variants, ok := agent["oneOf"].([]map[string]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("proposal agent variants = %#v", agent["oneOf"])
	}
	tasks := properties["tasks"].(map[string]any)["items"].(map[string]any)
	required := tasks["required"].([]string)
	if !containsRequiredString(required, "assignee_key") || !strings.Contains(strings.ToLower(proposalDescription(domainToolProposeWork)), "do not exist before acceptance") {
		t.Fatalf("proposal tool does not freeze an inert logical team: required=%#v description=%q", required, proposalDescription(domainToolProposeWork))
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
