package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestKnowledgePresentationResolvesProducerTaskAndAppliedRunEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, _, assigned := initializeRunTest(t, storage, "knowledge-presentation")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "knowledge-presentation",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "researching"}},
	}, "knowledge-presentation-run")
	starting, err := storage.MarkRunStarting(ctx, created.Run.ID, "knowledge-presentation-starting")
	if err != nil {
		t.Fatal(err)
	}
	active, err := storage.MarkRunStarted(ctx, starting.ID, "runtime", "provider", "knowledge-presentation-started")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := storage.PublishRunArtifact(ctx, PublishRunArtifactCommand{
		RunID: active.Run.ID, Name: "indie-game-research.md", MediaType: "text/markdown",
		Content: "# Research\n\nThe sourced opportunity map.", IdempotencyKey: "knowledge-presentation-artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: active.Run.ID, Kind: domain.ObservationProgress, Message: "Research synthesized.",
		Evidence: []string{artifact.ID}, Payload: map[string]any{"next": []string{"owner review"}},
		IdempotencyKey: "knowledge-presentation-progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ApplyQueuedRunReport(ctx, active.Run.ID, report.ID, true, nil, nil, "", "knowledge-presentation-apply"); err != nil {
		t.Fatal(err)
	}
	proposed, err := storage.ProposeKnowledge(ctx, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		Type: domain.KnowledgeTypeFinding, Title: "Evidence-to-decision loop",
		Body:       "The detailed research is in " + artifact.ID + ".",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: assigned.Task.ID, Role: domain.KnowledgeSourcePrimary}},
		Actor:           domain.KnowledgeActor{ID: active.Run.ID, Type: domain.KnowledgeActorAgentRun},
		IdempotencyKey:  "knowledge-presentation-proposal", CorrelationID: "knowledge-presentation-proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	presentations, err := storage.KnowledgePresentations(ctx, workspace.ID, []domain.KnowledgeRevision{proposed.Revision})
	if err != nil || len(presentations) != 1 {
		t.Fatalf("KnowledgePresentations() = %#v, %v", presentations, err)
	}
	presentation := presentations[0]
	if presentation.RevisionID != proposed.Revision.ID || presentation.Producer.Label != agent.Name ||
		presentation.Producer.AgentID != agent.ID || presentation.Producer.TaskID != assigned.Task.ID ||
		presentation.Producer.TaskTitle != assigned.Task.Title || len(presentation.Evidence) != 1 ||
		presentation.Evidence[0].ID != artifact.ID || presentation.Evidence[0].Name != artifact.Name ||
		presentation.Evidence[0].ContentHash != artifact.ContentHash {
		t.Fatalf("resolved knowledge presentation = %#v", presentation)
	}
}
