package store

import (
	"context"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestRunContradictionReportDerivesLiveActorAndExactTaskScope(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	storage, err := Open(context.Background(), dataDirectory, Options{
		RuntimeNodeID:          "11111111111111111111111111111111",
		RuntimeNodeFingerprint: "2222222222222222222222222222222222222222222222222222222222222222",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	workspace, project, _, _, assigned := initializeRunTest(t, storage, "run-contradiction")
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "run-contradiction",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "inspect facts"}},
	}
	run := createRunTest(t, storage, workspace.ID, assigned, scenario, "run-contradiction")

	left := acceptRunContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "left", "Contact order is ascending", "Sort contacts ascending", "")
	right := acceptRunContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "right", "Contact order is descending", "Sort contacts descending", "")
	command := ReportRunKnowledgeContradictionCommand{
		RunID: run.Run.ID, LeftRevisionID: right.Revision.ID, RightRevisionID: left.Revision.ID,
		ReportNote: "The accepted ordering decisions disagree", IdempotencyKey: "run-report",
		CorrelationID: "request-run-report",
	}
	if _, err := storage.ReportRunKnowledgeContradiction(context.Background(), command); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("ReportRunKnowledgeContradiction(requested) error = %v, code = %q", err, ErrorCode(err))
	}
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "start-run-contradiction")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	result, err := storage.ReportRunKnowledgeContradiction(context.Background(), command)
	if err != nil {
		t.Fatalf("ReportRunKnowledgeContradiction() error = %v", err)
	}
	contradiction := result.Detail.Contradiction
	if contradiction.Status != domain.KnowledgeContradictionProposed || contradiction.ReportedBy != starting.ID ||
		contradiction.ReportedByType != domain.KnowledgeActorAgentRun || contradiction.WorkspaceID != workspace.ID ||
		contradiction.ProjectID != project.ID || contradiction.LeftRevisionID > contradiction.RightRevisionID {
		t.Fatalf("run-bound contradiction = %#v", contradiction)
	}
	replayCommand := command
	replayCommand.LeftRevisionID, replayCommand.RightRevisionID = command.RightRevisionID, command.LeftRevisionID
	replayed, err := storage.ReportRunKnowledgeContradiction(context.Background(), replayCommand)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("ReportRunKnowledgeContradiction(reversed replay) = %#v, %v; want %#v", replayed, err, result)
	}
	otherTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "other task", "other-contradiction-task")
	otherScoped := acceptRunContradictionKnowledge(t, storage, workspace.ID, otherTask.Task.ID, "other", "Other task override", "Only the other task uses this rule", otherTask.Task.ID)
	if _, err := storage.ReportRunKnowledgeContradiction(context.Background(), ReportRunKnowledgeContradictionCommand{
		RunID: starting.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: otherScoped.Revision.ID,
		ReportNote: "This pair is outside the reporter task", IdempotencyKey: "wrong-task-report",
		CorrelationID: "request-wrong-task-report",
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("ReportRunKnowledgeContradiction(wrong task) error = %v, code = %q", err, ErrorCode(err))
	}
	third := acceptRunContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "third", "Contact order is random", "Shuffle contacts", "")
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime-handle", "provider-handle", "start-run-report-replay"); err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "done", Handoff: "review contradiction report", LogArchive: prepareTestRunLogArchive(t, storage, starting.ID),
	}, true, nil, "complete-run-report-replay"); err != nil {
		t.Fatalf("ApplyRunObservation(completed) error = %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close(before replay) error = %v", err)
	}
	storage, err = Open(context.Background(), dataDirectory, Options{
		RuntimeNodeID:          "11111111111111111111111111111111",
		RuntimeNodeFingerprint: "2222222222222222222222222222222222222222222222222222222222222222",
	})
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	replayedAfterCompletion, err := storage.ReportRunKnowledgeContradiction(context.Background(), replayCommand)
	if err != nil || !reflect.DeepEqual(replayedAfterCompletion, result) {
		t.Fatalf("ReportRunKnowledgeContradiction(completed replay) = %#v, %v; want %#v", replayedAfterCompletion, err, result)
	}
	replayedThroughGenericBoundary, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: command.LeftRevisionID, RightRevisionID: command.RightRevisionID,
		ReportNote: command.ReportNote, Actor: domain.KnowledgeActor{ID: starting.ID, Type: domain.KnowledgeActorAgentRun},
		IdempotencyKey: command.IdempotencyKey, CorrelationID: command.CorrelationID,
	})
	if err != nil || !reflect.DeepEqual(replayedThroughGenericBoundary, result) {
		t.Fatalf("ReportKnowledgeContradiction(completed agent replay) = %#v, %v; want %#v", replayedThroughGenericBoundary, err, result)
	}

	if _, err := storage.ReportRunKnowledgeContradiction(context.Background(), ReportRunKnowledgeContradictionCommand{
		RunID: starting.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: third.Revision.ID,
		ReportNote: "A completed run cannot make a fresh report", IdempotencyKey: "completed-new-report",
		CorrelationID: "request-completed-new-report",
	}); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("ReportRunKnowledgeContradiction(new report after completion) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: third.Revision.ID,
		ReportNote:     "A completed generic actor cannot make a fresh report",
		Actor:          domain.KnowledgeActor{ID: starting.ID, Type: domain.KnowledgeActorAgentRun},
		IdempotencyKey: "completed-generic-report", CorrelationID: "request-completed-generic-report",
	}); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("ReportKnowledgeContradiction(completed agent) error = %v, code = %q", err, ErrorCode(err))
	}
}

func acceptRunContradictionKnowledge(t *testing.T, storage *Store, workspaceID, sourceTaskID, key, title, body, taskScopeID string) KnowledgeMutationResult {
	t.Helper()
	proposed, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, TaskScopeID: taskScopeID, Type: domain.KnowledgeTypeDecision,
		Title: title, Body: body, Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus: domain.KnowledgeVerificationSupported, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sourceTaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:   OwnerKnowledgeActor(), IdempotencyKey: "propose-run-contradiction-" + key,
		CorrelationID: "request-propose-run-contradiction-" + key,
	})
	if err != nil {
		t.Fatalf("ProposeKnowledge(%s) error = %v", key, err)
	}
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, RevisionID: proposed.Revision.ID,
		ExpectedStateRevision: proposed.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-run-contradiction-" + key,
		CorrelationID:  "request-accept-run-contradiction-" + key,
	})
	if err != nil {
		t.Fatalf("AcceptKnowledge(%s) error = %v", key, err)
	}
	return accepted
}
