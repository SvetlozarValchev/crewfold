package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestContextPacketBindingReportsArtifactsAndScopeAreDurable(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, adjacent, assigned := initializeRunTest(t, storage, "scoped context")
	command := BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "build-scoped-context", CorrelationID: "request-build-scoped-context",
	}
	built, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil {
		t.Fatalf("BuildContextPacket() error = %v", err)
	}
	replayed, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, built) {
		t.Fatalf("BuildContextPacket(replay) = %#v, %v; want %#v", replayed, err, built)
	}
	packet := built.Value
	if packet.Schema != domain.ContextPacketSchema || packet.ContentHash == "" || packet.ByteSize <= 0 || packet.ByteSize > maximumContextBytes || packet.Task.Revision != assigned.Task.Revision || packet.Role.Revision != agent.Revision || packet.Checkout.Revision != adjacent.Revision {
		t.Fatalf("context packet = %#v", packet)
	}
	wantedExclusions := map[string]bool{"accepted_knowledge": false, "messages": false, "claims": false, "transcripts": false}
	for _, exclusion := range packet.Excluded {
		if _, exists := wantedExclusions[exclusion.Section]; exists {
			wantedExclusions[exclusion.Section] = true
		}
	}
	for section, found := range wantedExclusions {
		if !found {
			t.Errorf("context exclusion %q is absent", section)
		}
	}
	secondCommand := command
	secondCommand.IdempotencyKey = "build-equivalent-scoped-context"
	secondCommand.CorrelationID = "request-build-equivalent-scoped-context"
	equivalent, err := storage.BuildContextPacket(context.Background(), secondCommand)
	if err != nil || equivalent.Value.ID == packet.ID || equivalent.Value.ContentHash != packet.ContentHash || !reflect.DeepEqual(equivalent.Value.Included, packet.Included) || !reflect.DeepEqual(equivalent.Value.Excluded, packet.Excluded) {
		t.Fatalf("BuildContextPacket(equivalent) = %#v, %v; want different identity and stable semantic selection/hash", equivalent, err)
	}
	explanation, err := storage.ExplainContextPacket(context.Background(), workspace.ID, packet.ID)
	if err != nil || explanation.ContentHash != packet.ContentHash || explanation.ByteSize != packet.ByteSize || !reflect.DeepEqual(explanation.Included, packet.Included) || !reflect.DeepEqual(explanation.Excluded, packet.Excluded) {
		t.Fatalf("ExplainContextPacket() = %#v, %v", explanation, err)
	}

	otherTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "other scoped task", "other-scoped-task")
	otherAssigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: otherTask.Task.ID, AgentIdentifier: agent.ID, LeaseSeconds: 300, ExpectedRevision: otherTask.Task.Revision, IdempotencyKey: "assign-other-scoped-task", CorrelationID: "request-assign-other-scoped-task"})
	if err != nil {
		t.Fatalf("AssignTask(other) error = %v", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "scoped-context", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: otherAssigned.Detail.Task.ID, ContextPacketID: packet.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: otherAssigned.Detail.Task.Revision, IdempotencyKey: "wrong-context-run", CorrelationID: "request-wrong-context-run"}); ErrorCode(err) != CodeInvalidContext {
		t.Fatalf("CreateRun(wrong context) error = %v, code = %q", err, ErrorCode(err))
	}

	created, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, ContextPacketID: packet.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "scoped-context-run", CorrelationID: "request-scoped-context-run"})
	if err != nil || created.Detail.Run.ContextPacketID != packet.ID {
		t.Fatalf("CreateRun(scoped) = %#v, %v", created, err)
	}
	if _, err := storage.AuthorizeRunCapability(context.Background(), created.Detail.Run.ID); ErrorCode(err) != CodeCapabilityInactive {
		t.Fatalf("AuthorizeRunCapability(requested) error = %v, code = %q", err, ErrorCode(err))
	}
	starting, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting-scoped")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	briefing, err := storage.AuthorizeRunCapability(context.Background(), starting.ID)
	if err != nil || briefing.Packet.ID != packet.ID || briefing.Run.ID != starting.ID || briefing.Resource != "crewfold://runs/"+starting.ID+"/briefing" {
		t.Fatalf("AuthorizeRunCapability() = %#v, %v", briefing, err)
	}
	reportCommand := CreateRunReportCommand{RunID: starting.ID, Kind: domain.ObservationProgress, Message: "implemented slice", Evidence: []string{"tests_passed"}, Payload: map[string]any{"next": []string{}}, IdempotencyKey: "scoped-progress"}
	report, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil {
		t.Fatalf("SubmitRunReport() error = %v", err)
	}
	reportReplay, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil || reportReplay.ID != report.ID {
		t.Fatalf("SubmitRunReport(replay) = %#v, %v; want ID %s", reportReplay, err, report.ID)
	}
	changedReport := reportCommand
	changedReport.Message = "different content"
	if _, err := storage.SubmitRunReport(context.Background(), changedReport); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("SubmitRunReport(conflict) error = %v, code = %q", err, ErrorCode(err))
	}
	artifactCommand := PublishRunArtifactCommand{RunID: starting.ID, Name: "test evidence", MediaType: "text/plain", Content: "bounded evidence", IdempotencyKey: "scoped-artifact"}
	artifact, err := storage.PublishRunArtifact(context.Background(), artifactCommand)
	artifactReplay, replayErr := storage.PublishRunArtifact(context.Background(), artifactCommand)
	if err != nil || replayErr != nil || artifact.ID == "" || artifactReplay.ID != artifact.ID || artifact.ContentHash == "" {
		t.Fatalf("PublishRunArtifact() = %#v, %v; replay = %#v, %v", artifact, err, artifactReplay, replayErr)
	}
	active, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "worker-started-scoped")
	if err != nil || active.Run.Status != domain.RunActive {
		t.Fatalf("MarkRunStarted() = %#v, %v", active, err)
	}
	pending, found, err := storage.NextPendingRunReport(context.Background(), active.Run.ID)
	if err != nil || !found || pending.ID != report.ID {
		t.Fatalf("NextPendingRunReport() = %#v, %t, %v", pending, found, err)
	}
	applied, err := storage.ApplyQueuedRunReport(context.Background(), active.Run.ID, pending.ID, true, nil, "worker-apply-scoped")
	if err != nil || applied.Run.StepCursor != 1 {
		t.Fatalf("ApplyQueuedRunReport() = %#v, %v", applied, err)
	}
	reportAfterApply, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil || reportAfterApply.ID != report.ID || reportAfterApply.Status != "applied" {
		t.Fatalf("SubmitRunReport(replay after apply) = %#v, %v", reportAfterApply, err)
	}
	if _, found, err := storage.NextPendingRunReport(context.Background(), active.Run.ID); err != nil || found {
		t.Fatalf("NextPendingRunReport(after apply) found = %t, error = %v", found, err)
	}
}

func TestRunCapabilityExpiresAndBecomesInactiveAfterStop(t *testing.T) {
	base := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	now := base
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "expiring capability")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "expiring-capability", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, CapabilityTTL: time.Second, IdempotencyKey: "expiring-capability-run", CorrelationID: "request-expiring-capability-run"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	starting, _ := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting-expiry")
	if _, err := storage.AuthorizeRunCapability(context.Background(), starting.ID); err != nil {
		t.Fatalf("AuthorizeRunCapability(before expiry) error = %v", err)
	}
	now = base.Add(time.Second)
	if _, err := storage.AuthorizeRunCapability(context.Background(), starting.ID); ErrorCode(err) != CodeCapabilityExpired {
		t.Fatalf("AuthorizeRunCapability(expired) error = %v, code = %q", err, ErrorCode(err))
	}

	now = base
	secondStorage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	secondWorkspace, _, _, _, secondAssigned := initializeRunTest(t, secondStorage, "stopped capability")
	second, err := secondStorage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: secondWorkspace.ID, TaskID: secondAssigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: secondAssigned.Task.Revision, IdempotencyKey: "stopped-capability-run", CorrelationID: "request-stopped-capability-run"})
	if err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}
	secondStarting, _ := secondStorage.MarkRunStarting(context.Background(), second.Detail.Run.ID, "worker-starting-stop")
	active, _ := secondStorage.MarkRunStarted(context.Background(), secondStarting.ID, "runtime", "provider", "worker-started-stop")
	stopping, err := secondStorage.RequestRunStop(context.Background(), StopRunCommand{WorkspaceIdentifier: secondWorkspace.ID, RunID: active.Run.ID, ExpectedRevision: active.Run.Revision, GracePeriodMillis: 100, IdempotencyKey: "stop-scoped-run", CorrelationID: "request-stop-scoped-run"})
	if err != nil {
		t.Fatalf("RequestRunStop() error = %v", err)
	}
	if _, err := secondStorage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "stopped", "worker-stopped-scoped"); err != nil {
		t.Fatalf("MarkRunStopped() error = %v", err)
	}
	if _, err := secondStorage.AuthorizeRunCapability(context.Background(), second.Detail.Run.ID); ErrorCode(err) != CodeCapabilityInactive {
		t.Fatalf("AuthorizeRunCapability(stopped) error = %v, code = %q", err, ErrorCode(err))
	}
}
