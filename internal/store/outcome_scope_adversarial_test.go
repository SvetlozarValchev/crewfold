package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestOutcomeDecisionReferenceRequiresApplicableKnowledgeScope(t *testing.T) {
	for _, test := range []struct {
		name      string
		scope     string
		wantError bool
	}{
		{name: "project-wide decision applies"},
		{name: "exact task decision applies", scope: "exact"},
		{name: "sibling task decision does not apply", scope: "sibling", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := newOutcomeAdversarialFixture(t, false)
			sibling, err := storage.CreateTask(context.Background(), CreateTaskCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
				ObjectiveID: fixture.objective.ID, Title: "Sibling scoped decision task",
				IdempotencyKey: "outcome-decision-sibling-task", CorrelationID: "request-outcome-decision-sibling-task",
			})
			if err != nil {
				t.Fatalf("CreateTask(sibling) = %v", err)
			}
			taskScopeID := test.scope
			switch test.scope {
			case "exact":
				taskScopeID = fixture.task.Task.ID
			case "sibling":
				taskScopeID = sibling.Detail.Task.ID
			}
			proposedDecision, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
				WorkspaceIdentifier: fixture.workspace.ID, TaskScopeID: taskScopeID,
				Type: domain.KnowledgeTypeDecision, Title: "Exact scoped decision", Body: "Use this decision only where its durable scope applies.",
				Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
				FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
				Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sibling.Detail.Task.ID, Role: domain.KnowledgeSourcePrimary}},
				Actor:           OwnerKnowledgeActor(), IdempotencyKey: "outcome-decision-propose", CorrelationID: "request-outcome-decision-propose",
			})
			if err != nil {
				t.Fatalf("ProposeKnowledge() = %v", err)
			}
			acceptedDecision, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
				WorkspaceIdentifier: fixture.workspace.ID, RevisionID: proposedDecision.Revision.ID,
				ExpectedStateRevision: proposedDecision.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: "outcome-decision-accept", CorrelationID: "request-outcome-decision-accept",
			})
			if err != nil {
				t.Fatalf("AcceptKnowledge() = %v", err)
			}
			commitment := fixture.createCommitment(t, "outcome-decision-scope")
			input := fullyBoundedUnknownOutcomeInput()
			input.DecisionRevisionIDs = []string{acceptedDecision.Revision.ID}
			result, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.task.Task.ID,
				CommitmentID: commitment.Commitment.ID, Input: input,
				IdempotencyKey: "outcome-decision-assessment", CorrelationID: "request-outcome-decision-assessment",
			})
			if test.wantError {
				if ErrorCode(err) != CodeInvalidOutcomeAssessment {
					t.Fatalf("ProposeOutcomeAssessment(cross-task decision) = %#v, %v, code %q; want %q", result, err, ErrorCode(err), CodeInvalidOutcomeAssessment)
				}
				return
			}
			if err != nil || len(result.Detail.Decisions) != 1 || result.Detail.Decisions[0].RevisionID != acceptedDecision.Revision.ID {
				t.Fatalf("ProposeOutcomeAssessment(applicable decision) = %#v, %v", result, err)
			}
		})
	}
}
