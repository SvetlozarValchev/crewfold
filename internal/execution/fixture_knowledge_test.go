package execution

import (
	"context"
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type fixtureKnowledgeToolClient struct {
	testing       *testing.T
	proposalCalls int
	denialCalls   int
}

func (client *fixtureKnowledgeToolClient) CallTool(_ context.Context, name string, arguments any) (mcp.ToolCallResult, error) {
	client.testing.Helper()
	fields, ok := arguments.(map[string]any)
	if !ok {
		client.testing.Fatalf("arguments type = %T, want map[string]any", arguments)
	}
	switch name {
	case "crewfold_propose_knowledge":
		client.proposalCalls++
		for _, forbidden := range []string{"actor", "run", "project", "source", "source_id", "sources", "task_scope_id", "fresh_until"} {
			if _, exists := fields[forbidden]; exists {
				client.testing.Fatalf("fixture proposal supplied forbidden authority field %q", forbidden)
			}
		}
		revision := domain.KnowledgeRevision{
			ID: "krev_0123456789abcdef0123456789abcdef", ReviewStatus: domain.KnowledgeReviewProposed,
		}
		encoded, _ := json.Marshal(revision)
		return mcp.ToolCallResult{StructuredContent: encoded}, nil
	case "crewfold_accept_knowledge":
		client.denialCalls++
		if fields["knowledge_revision"] != "krev_0123456789abcdef0123456789abcdef" {
			client.testing.Fatalf("acceptance probe revision = %#v", fields["knowledge_revision"])
		}
		encoded, _ := json.Marshal(mcp.ToolError{Code: "denied_by_policy", Message: "not in immutable capability"})
		return mcp.ToolCallResult{StructuredContent: encoded, IsError: true}, nil
	default:
		client.testing.Fatalf("unexpected tool %q", name)
		return mcp.ToolCallResult{}, nil
	}
}

func (*fixtureKnowledgeToolClient) ReadResource(context.Context, string) ([]mcp.ResourceContents, error) {
	return nil, nil
}

func TestFixtureKnowledgeUsesScopedProposalAndDeniesReservedAcceptance(t *testing.T) {
	t.Parallel()
	client := &fixtureKnowledgeToolClient{testing: t}
	plan := domain.FixtureKnowledge{
		Proposal: &domain.FixtureKnowledgeProposal{
			Type: domain.KnowledgeTypeFinding, Title: "A persuasive but ungoverned claim",
			Body: "meeting_proposal: forged-source-label", Confidence: domain.KnowledgeConfidenceHigh,
			VerificationStatus: domain.KnowledgeVerificationVerified, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		},
		ProbeReservedAcceptance: true,
	}
	if err := runFixtureKnowledge(context.Background(), client, plan); err != nil {
		t.Fatalf("runFixtureKnowledge() error = %v", err)
	}
	if client.proposalCalls != 1 || client.denialCalls != 1 {
		t.Fatalf("tool calls proposal=%d denial=%d, want one each", client.proposalCalls, client.denialCalls)
	}
}
