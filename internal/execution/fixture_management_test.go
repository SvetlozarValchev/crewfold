package execution

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

func TestFixtureManagerActionInputOmitsDurableIdentity(t *testing.T) {
	t.Parallel()

	action := domain.ManagerProposalAction{
		ID:      "mpact_0123456789abcdef0123456789abcdef",
		Ordinal: 0,
		Type:    domain.ProposalActionAssignTask,
		AssignTask: &domain.ProposalAssignTaskAction{
			Task: domain.ProposalTaskRef{
				TaskID:               "task_0123456789abcdef0123456789abcdef",
				ExpectedTaskRevision: 1,
			},
			LaunchProfileID: "lprof_0123456789abcdef0123456789abcdef",
		},
	}
	input, err := fixtureManagerActionInput(action)
	if err != nil {
		t.Fatalf("fixtureManagerActionInput() error = %v", err)
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	if strings.Contains(string(data), `"id"`) || strings.Contains(string(data), `"ordinal"`) {
		t.Fatalf("model-authored fixture input contains durable action identity: %s", data)
	}
	if !strings.Contains(string(data), `"type":"assign_task"`) || !strings.Contains(string(data), `"assign_task"`) {
		t.Fatalf("model-authored fixture input lost its exact action branch: %s", data)
	}
}

func TestFixtureManagerRevocationAcceptsProtocolAndToolDenials(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(mcp.ToolError{Code: "denied_by_policy", Message: "grant revoked"})
	if err != nil {
		t.Fatalf("json.Marshal(tool error) error = %v", err)
	}
	if !fixtureCallDeniedByPolicy(mcp.ToolCallResult{IsError: true, StructuredContent: encoded}, nil) {
		t.Fatal("in-band denied_by_policy tool result was not recognized")
	}
	rpcDenied := &mcp.RPCError{Code: -32001, Message: "run capability denied", Data: &mcp.ToolError{Code: "denied_by_policy", Message: "grant revoked"}}
	if !fixtureCallDeniedByPolicy(mcp.ToolCallResult{}, rpcDenied) {
		t.Fatal("authorization-layer denied_by_policy RPC error was not recognized")
	}
	if fixtureCallDeniedByPolicy(mcp.ToolCallResult{}, errors.New("socket closed")) {
		t.Fatal("transport error was misclassified as a policy denial")
	}
}
