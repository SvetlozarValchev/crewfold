package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestOutcomeDecisionExpiryUsesRFC3339InstantAcrossOffsetAndFractionForms(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, false)
	beforeExpiry := time.Date(2033, 4, 5, 12, 0, 0, 500_000_000, time.UTC)
	expiry := beforeExpiry.Add(100 * time.Millisecond)
	storage.clock = func() time.Time { return beforeExpiry }
	commitment := fixture.createCommitment(t, "decision-expiry-instant")

	offset := time.FixedZone("expiry-offset", 2*60*60)
	freshUntil := expiry.In(offset).Format(time.RFC3339Nano)
	proposedDecision, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskScopeID: fixture.task.Task.ID,
		Type: domain.KnowledgeTypeDecision, Title: "Expiring outcome decision",
		Body:       "Compare the represented instant, not the RFC3339 spelling.",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
		FreshnessPolicy: domain.KnowledgeFreshExpiresAt, FreshUntil: freshUntil,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: fixture.task.Task.ID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "decision-expiry-knowledge", CorrelationID: "request-decision-expiry-knowledge",
	})
	if err != nil {
		t.Fatalf("ProposeKnowledge(offset expiry %q) = %v", freshUntil, err)
	}
	acceptedDecision, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: fixture.workspace.ID, RevisionID: proposedDecision.Revision.ID,
		ExpectedStateRevision: proposedDecision.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "decision-expiry-accept", CorrelationID: "request-decision-expiry-accept",
	})
	if err != nil {
		t.Fatalf("AcceptKnowledge() = %v", err)
	}
	input := fullyBoundedUnknownOutcomeInput()
	input.DecisionRevisionIDs = []string{acceptedDecision.Revision.ID}
	accepted := fixture.acceptOutcomeInput(t, commitment.Commitment.ID, input, "decision-expiry-outcome")
	if len(accepted.Detail.Decisions) != 1 || !accepted.Detail.Decisions[0].Current {
		t.Fatalf("accepted outcome decision before expiry = %#v, want current", accepted.Detail.Decisions)
	}

	storage.clock = func() time.Time { return expiry }
	read, err := storage.OutcomeAssessment(context.Background(), fixture.workspace.ID, accepted.Detail.Assessment.ID)
	if err != nil {
		t.Fatalf("OutcomeAssessment(at exact expiry) = %v", err)
	}
	if len(read.Decisions) != 1 || read.Decisions[0].Current {
		t.Fatalf("outcome decision at exact instant %s from offset spelling %s = %#v, want stale", expiry.Format(time.RFC3339Nano), freshUntil, read.Decisions)
	}
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: fixture.workspace.ID, ScopeType: domain.OwnerCheckpointTask, ScopeIdentifier: fixture.task.Task.ID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing(at exact expiry) = %v", err)
	}
	foundGap := false
	for _, claim := range briefing.Claims {
		if claim.Kind != domain.BriefingClaimVerificationGap || claim.Status != domain.BriefingClaimStatusStale {
			continue
		}
		for _, source := range claim.Sources {
			if source.EntityType == "knowledge_revision" && source.EntityID == acceptedDecision.Revision.ID {
				foundGap = true
			}
		}
	}
	if !foundGap {
		t.Fatalf("briefing at decision expiry omitted stale verification gap for %s: %#v", acceptedDecision.Revision.ID, briefing.Claims)
	}
}
