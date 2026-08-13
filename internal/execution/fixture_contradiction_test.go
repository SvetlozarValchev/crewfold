package execution

import (
	"context"
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type fixtureContradictionToolClient struct {
	testing      *testing.T
	reportCalls  int
	confirmCalls int
}

func (client *fixtureContradictionToolClient) CallTool(_ context.Context, name string, arguments any) (mcp.ToolCallResult, error) {
	client.testing.Helper()
	fields, ok := arguments.(map[string]any)
	if !ok {
		client.testing.Fatalf("arguments type = %T, want map[string]any", arguments)
	}
	for _, forbidden := range []string{"actor", "actor_id", "actor_type", "workspace", "project", "task", "run", "status"} {
		if _, exists := fields[forbidden]; exists {
			client.testing.Fatalf("fixture contradiction supplied forbidden authority field %q", forbidden)
		}
	}
	switch name {
	case "crewfold_report_contradiction":
		client.reportCalls++
		left, right := fields["left_revision"], fields["right_revision"]
		forward := left == "krev_00000000000000000000000000000001" && right == "krev_00000000000000000000000000000002"
		reverse := left == "krev_00000000000000000000000000000002" && right == "krev_00000000000000000000000000000001"
		if (!forward && !reverse) || fields["idempotency_key"] != "fixture-contradiction-report" {
			client.testing.Fatalf("report arguments = %#v", fields)
		}
		detail := domain.KnowledgeContradictionDetail{Contradiction: domain.KnowledgeContradiction{
			ID: "kcon_00000000000000000000000000000001", Status: domain.KnowledgeContradictionProposed, StateRevision: 1,
			ReportedBy: "run_00000000000000000000000000000001", ReportedByType: domain.KnowledgeActorAgentRun,
		}}
		encoded, _ := json.Marshal(detail)
		return mcp.ToolCallResult{StructuredContent: encoded}, nil
	case "crewfold_confirm_contradiction":
		client.confirmCalls++
		if fields["contradiction"] != "kcon_00000000000000000000000000000001" ||
			fields["expected_state_revision"] != int64(1) {
			client.testing.Fatalf("confirmation arguments = %#v", fields)
		}
		encoded, _ := json.Marshal(mcp.ToolError{Code: "denied_by_policy", Message: "not in immutable capability"})
		return mcp.ToolCallResult{StructuredContent: encoded, IsError: true}, nil
	default:
		client.testing.Fatalf("unexpected tool %q", name)
		return mcp.ToolCallResult{}, nil
	}
}

func (*fixtureContradictionToolClient) ReadResource(context.Context, string) ([]mcp.ResourceContents, error) {
	return nil, nil
}

func TestFixtureContradictionReportsThroughScopeAndCannotConfirm(t *testing.T) {
	t.Parallel()
	client := &fixtureContradictionToolClient{testing: t}
	plan := domain.FixtureContradiction{
		Report: &domain.FixtureContradictionReport{
			LeftRevision: "krev_00000000000000000000000000000001", RightRevision: "krev_00000000000000000000000000000002",
			Reason: "The accepted decisions disagree.",
		},
		ReportReceived: true,
		ConfirmDenied:  true,
	}
	if err := runFixtureContradiction(context.Background(), client, plan); err != nil {
		t.Fatalf("runFixtureContradiction() error = %v", err)
	}
	if client.reportCalls != 2 || client.confirmCalls != 1 {
		t.Fatalf("tool calls report=%d confirm=%d, want two idempotent reports and one confirmation denial", client.reportCalls, client.confirmCalls)
	}
}
