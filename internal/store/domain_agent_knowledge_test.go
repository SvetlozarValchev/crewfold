package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestM22DurableAgentProposesSourcedKnowledgeAndOnlyOwnerMakesItCurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agent := createDomainTestAgent(t, storage, workspace.ID, "knowledge-maintainer", "owner-defined knowledge role")
	membership, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentHandsOn,
		PreferredEntry: true, IdempotencyKey: "attach-knowledge-maintainer", CorrelationID: "attach-knowledge-maintainer",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := storage.ProposeKnowledge(ctx, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		Type: domain.KnowledgeTypeFinding, Title: "Shared storage boundary",
		Body:       "The durable domain record keeps this exact cross-workstream boundary outside repository Markdown.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceDomainAgent, ID: agent.Value.ID, Role: domain.KnowledgeSourcePrimary}},
		Actor:           domain.KnowledgeActor{ID: agent.Value.ID, Type: domain.KnowledgeActorIntegration},
		IdempotencyKey:  "agent-knowledge-proposal", CorrelationID: "agent-knowledge-proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Revision.ReviewStatus != domain.KnowledgeReviewProposed || proposed.Revision.CurrencyStatus != domain.KnowledgeCurrencyPending || proposed.Revision.ProposedBy != agent.Value.ID || proposed.Revision.ProposedByType != domain.KnowledgeActorIntegration || len(proposed.Revision.Sources) != 1 || proposed.Revision.Sources[0].Type != domain.KnowledgeSourceDomainAgent || proposed.Revision.Sources[0].ID != agent.Value.ID || proposed.Revision.Sources[0].Revision != membership.Value.Revision || proposed.Revision.Sources[0].Role != domain.KnowledgeSourcePrimary {
		t.Fatalf("agent knowledge proposal = %#v", proposed.Revision)
	}
	if _, err := storage.AcceptKnowledge(ctx, AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision,
		Actor:                 domain.KnowledgeActor{ID: agent.Value.ID, Type: domain.KnowledgeActorIntegration},
		IdempotencyKey:        "agent-self-accept", CorrelationID: "agent-self-accept",
	}); ErrorCode(err) != CodeKnowledgeDenied {
		t.Fatalf("agent self-accept error = %v, code %q", err, ErrorCode(err))
	}
	accepted, err := storage.AcceptKnowledge(ctx, AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, DecisionNote: "Owner accepts the exact sourced domain record.",
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "owner-accept-agent-knowledge", CorrelationID: "owner-accept-agent-knowledge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Revision.ReviewStatus != domain.KnowledgeReviewAccepted || accepted.Revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent || accepted.Revision.AcceptedByType != domain.KnowledgeActorHuman {
		t.Fatalf("accepted agent knowledge = %#v", accepted.Revision)
	}
}
