package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestAgentObjectiveAndTaskDefinitionsAreIdempotentAndQueryable(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agentCommand := CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "implementer", Role: "implementer", Provider: "fake", IdempotencyKey: "agent-implementer", CorrelationID: "request-agent"}
	agent, err := storage.CreateAgent(context.Background(), agentCommand)
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	replayedAgent, err := storage.CreateAgent(context.Background(), agentCommand)
	if err != nil || replayedAgent != agent {
		t.Fatalf("CreateAgent(replay) = %#v, %v, want %#v", replayedAgent, err, agent)
	}
	if agent.Value.Runtime != "unconfigured" || !agent.Value.Enabled || agent.Value.MaxConcurrency != 1 {
		t.Fatalf("agent defaults = %#v", agent.Value)
	}

	objective, err := storage.CreateObjective(context.Background(), CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		Title: "Ship deterministic contacts", Budget: domain.Budget{TokenLimit: 10000, CostCents: 500, TimeSeconds: 3600},
		IdempotencyKey: "objective-contacts", CorrelationID: "request-objective",
	})
	if err != nil {
		t.Fatalf("CreateObjective() error = %v", err)
	}
	task, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ObjectiveID: objective.Value.ID,
		Title: "Add contact cache", Description: "Keep deterministic ordering", Priority: 200,
		Budget: domain.Budget{TokenLimit: 5000, TimeSeconds: 1800}, IdempotencyKey: "task-contact-cache", CorrelationID: "request-task",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if !task.Detail.Readiness.Ready || task.Detail.Task.Status != domain.TaskReady {
		t.Fatalf("created task = %#v, want ready", task.Detail)
	}

	agentPage, err := storage.ListAgents(context.Background(), ListAgentsQuery{WorkspaceIdentifier: workspace.Name})
	agents := agentPage.Agents
	if err != nil || len(agents) != 1 || agents[0].ID != agent.Value.ID {
		t.Fatalf("Agents() = %#v, %v", agents, err)
	}
	objectivePage, err := storage.ListObjectives(context.Background(), ListObjectivesQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.Name})
	objectives := objectivePage.Objectives
	if err != nil || len(objectives) != 1 || objectives[0].ID != objective.Value.ID {
		t.Fatalf("Objectives() = %#v, %v", objectives, err)
	}
	taskPage, err := storage.ListTasks(context.Background(), ListTasksQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ReadyOnly: true})
	tasks := taskPage.Tasks
	if err != nil || len(tasks) != 1 || tasks[0].Task.ID != task.Detail.Task.ID {
		t.Fatalf("Tasks(ready) = %#v, %v", tasks, err)
	}
}

func TestDefinitionUpdatesUseExpectedRevisions(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agent, _ := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "reviewer", Role: "reviewer", Provider: "fake", IdempotencyKey: "agent-reviewer", CorrelationID: "request-agent"})
	newRole := "lead-reviewer"
	disabled := false
	updatedAgent, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{WorkspaceIdentifier: workspace.ID, AgentIdentifier: agent.Value.ID, Role: &newRole, Enabled: &disabled, ExpectedRevision: 1, IdempotencyKey: "update-agent", CorrelationID: "request-update-agent"})
	if err != nil || updatedAgent.Value.Revision != 2 || updatedAgent.Value.Role != newRole || updatedAgent.Value.Enabled {
		t.Fatalf("UpdateAgent() = %#v, %v", updatedAgent, err)
	}
	if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{WorkspaceIdentifier: workspace.ID, AgentIdentifier: agent.Value.ID, Role: &newRole, ExpectedRevision: 1, IdempotencyKey: "stale-agent", CorrelationID: "request-stale-agent"}); ErrorCode(err) != CodeRevisionConflict {
		t.Fatalf("UpdateAgent(stale) error = %v, code = %q", err, ErrorCode(err))
	}
	disabledTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "disabled agent assignment", "disabled-agent-task")
	if _, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: disabledTask.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 60, ExpectedRevision: 1, IdempotencyKey: "assign-disabled-agent", CorrelationID: "request-assign-disabled-agent"}); ErrorCode(err) != CodeAssignmentConflict {
		t.Fatalf("AssignTask(disabled agent) error = %v, code = %q", err, ErrorCode(err))
	}

	objective, _ := storage.CreateObjective(context.Background(), CreateObjectiveCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Title: "Initial objective", IdempotencyKey: "objective", CorrelationID: "request-objective"})
	newTitle := "Updated objective"
	updatedObjective, err := storage.UpdateObjective(context.Background(), UpdateObjectiveCommand{WorkspaceIdentifier: workspace.ID, ObjectiveID: objective.Value.ID, Title: &newTitle, ExpectedRevision: 1, IdempotencyKey: "update-objective", CorrelationID: "request-update-objective"})
	if err != nil || updatedObjective.Value.Title != newTitle || updatedObjective.Value.Revision != 2 {
		t.Fatalf("UpdateObjective() = %#v, %v", updatedObjective, err)
	}

	task, _ := storage.CreateTask(context.Background(), CreateTaskCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Title: "Initial task", Priority: 10, IdempotencyKey: "task", CorrelationID: "request-task"})
	description := "updated contract"
	priority := 20
	updatedTask, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Detail.Task.ID, Description: &description, Priority: &priority, ExpectedRevision: 1, IdempotencyKey: "update-task", CorrelationID: "request-update-task"})
	if err != nil || updatedTask.Detail.Task.Description != description || updatedTask.Detail.Task.Priority != priority || updatedTask.Detail.Task.Revision != 2 {
		t.Fatalf("UpdateTask() = %#v, %v", updatedTask, err)
	}
}

func TestTaskDependenciesRejectCyclesAndExplainReadiness(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	first := createWorkTestTask(t, storage, workspace.ID, project.ID, "foundation", "task-foundation")
	second := createWorkTestTask(t, storage, workspace.ID, project.ID, "consumer", "task-consumer")
	dependent, err := storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{WorkspaceIdentifier: workspace.ID, TaskID: second.Task.ID, DependsOnTaskID: first.Task.ID, ExpectedRevision: 1, IdempotencyKey: "depend-consumer-foundation", CorrelationID: "request-depend"})
	if err != nil {
		t.Fatalf("AddTaskDependency() error = %v", err)
	}
	if dependent.Detail.Readiness.Ready || !strings.Contains(dependent.Detail.Readiness.Reason, first.Task.ID) {
		t.Fatalf("dependent readiness = %#v", dependent.Detail.Readiness)
	}
	_, err = storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: second.Task.ID, AgentIdentifier: "missing", LeaseSeconds: 60, ExpectedRevision: 2, IdempotencyKey: "assign-not-ready", CorrelationID: "request-assign-not-ready"})
	if ErrorCode(err) != CodeAgentNotFound {
		t.Fatalf("AssignTask(missing agent) error = %v, code = %q", err, ErrorCode(err))
	}
	_, err = storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{WorkspaceIdentifier: workspace.ID, TaskID: first.Task.ID, DependsOnTaskID: second.Task.ID, ExpectedRevision: 1, IdempotencyKey: "depend-cycle", CorrelationID: "request-cycle"})
	if ErrorCode(err) != CodeDependencyCycle {
		t.Fatalf("AddTaskDependency(cycle) error = %v, code = %q", err, ErrorCode(err))
	}
	readyPage, err := storage.ListTasks(context.Background(), ListTasksQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ReadyOnly: true})
	ready := readyPage.Tasks
	if err != nil || len(ready) != 1 || ready[0].Task.ID != first.Task.ID {
		t.Fatalf("Tasks(ready) = %#v, %v, want only foundation", ready, err)
	}
}

func TestM23DependencyDeliveryGatesReadinessAndBecomesSuccessorContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	checkout := inspection.Checkouts[0]
	agent, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "delivery-agent", Role: "implementer", Provider: "fake", Runtime: "fake",
		IdempotencyKey: "m23-delivery-agent", CorrelationID: "m23-delivery-agent",
	})
	if err != nil {
		t.Fatal(err)
	}

	missingSource := createWorkTestTask(t, storage, workspace.ID, project.ID, "source without output", "m23-missing-source")
	missingSuccessor := createWorkTestTask(t, storage, workspace.ID, project.ID, "successor requiring output", "m23-missing-successor")
	missingSuccessorMutation, err := storage.AddTaskDependency(ctx, AddTaskDependencyCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: missingSuccessor.Task.ID, DependsOnTaskID: missingSource.Task.ID,
		DeliveryRequirement: domain.DependencyDeliveryHandoffWithEvidence, ExpectedRevision: missingSuccessor.Task.Revision,
		IdempotencyKey: "m23-missing-dependency", CorrelationID: "m23-missing-dependency",
	})
	if err != nil {
		t.Fatal(err)
	}
	missingAssigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: missingSource.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: missingSource.Task.Revision,
		IdempotencyKey: "m23-assign-missing-source", CorrelationID: "m23-assign-missing-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	missingPacket, err := storage.BuildContextPacket(ctx, BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: missingSource.Task.ID, AgentIdentifier: agent.Value.ID,
		CheckoutIdentifier: checkout.ID, ExpectedTaskRevision: missingAssigned.Detail.Task.Revision,
		IdempotencyKey: "m23-missing-context", CorrelationID: "m23-missing-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	missingRun, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: missingSource.Task.ID, CheckoutIdentifier: checkout.ID,
		ContextPacketID: missingPacket.Value.ID, Runtime: "fake", Provider: "fake",
		Scenario: managementProgressScenario("m23-missing-delivery"), ExpectedTaskRevision: missingAssigned.Detail.Task.Revision,
		IdempotencyKey: "m23-missing-run", CorrelationID: "m23-missing-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	missingStarting, err := storage.MarkRunStarting(ctx, missingRun.Detail.Run.ID, "m23-missing-starting")
	if err != nil {
		t.Fatal(err)
	}
	missingRunning, err := storage.MarkRunStarted(ctx, missingStarting.ID, "m23-missing-runtime", "m23-missing-provider", "m23-missing-started")
	if err != nil {
		t.Fatal(err)
	}
	missingReport, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: missingRunning.Run.ID, Kind: domain.ObservationCompletion, Message: "completed without structured evidence",
		Handoff: "a narrative handoff without evidence", Payload: map[string]any{}, IdempotencyKey: "m23-missing-completion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ApplyQueuedRunReport(ctx, missingRunning.Run.ID, missingReport.ID, true, nil,
		prepareTestRunLogArchive(t, storage, missingRunning.Run.ID), "", "m23-apply-missing-completion"); err != nil {
		t.Fatal(err)
	}
	missingAfter, err := storage.TaskDetail(ctx, workspace.ID, missingSuccessorMutation.Detail.Task.ID)
	if err != nil || missingAfter.Readiness.Ready || !strings.Contains(missingAfter.Readiness.Reason, "waiting for handoff_with_evidence delivery") {
		t.Fatalf("successor without delivery = %#v, %v", missingAfter.Readiness, err)
	}

	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source with exact output", "m23-source")
	successor := createWorkTestTask(t, storage, workspace.ID, project.ID, "successor receives output", "m23-successor")
	successorMutation, err := storage.AddTaskDependency(ctx, AddTaskDependencyCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: successor.Task.ID, DependsOnTaskID: source.Task.ID,
		DeliveryRequirement: domain.DependencyDeliveryHandoffWithEvidence, ExpectedRevision: successor.Task.Revision,
		IdempotencyKey: "m23-delivery-dependency", CorrelationID: "m23-delivery-dependency",
	})
	if err != nil {
		t.Fatal(err)
	}
	assignedSource, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: source.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: source.Task.Revision,
		IdempotencyKey: "m23-assign-source", CorrelationID: "m23-assign-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := storage.BuildContextPacket(ctx, BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: source.Task.ID, AgentIdentifier: agent.Value.ID,
		CheckoutIdentifier: checkout.ID, ExpectedTaskRevision: assignedSource.Detail.Task.Revision,
		IdempotencyKey: "m23-source-context", CorrelationID: "m23-source-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: source.Task.ID, CheckoutIdentifier: checkout.ID,
		ContextPacketID: packet.Value.ID, Runtime: "fake", Provider: "fake",
		Scenario: managementProgressScenario("m23-delivery"), ExpectedTaskRevision: assignedSource.Detail.Task.Revision,
		IdempotencyKey: "m23-source-run", CorrelationID: "m23-source-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	starting, err := storage.MarkRunStarting(ctx, run.Detail.Run.ID, "m23-source-starting")
	if err != nil {
		t.Fatal(err)
	}
	active, err := storage.MarkRunStarted(ctx, starting.ID, "m23-runtime", "m23-provider", "m23-source-started")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := storage.PublishRunArtifact(ctx, PublishRunArtifactCommand{
		RunID: active.Run.ID, Name: "test report", MediaType: "text/plain", Content: "27 checks passed",
		IdempotencyKey: "m24-source-evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerArtifact, err := storage.RunArtifactContent(ctx, workspace.ID, artifact.ID)
	if err != nil || ownerArtifact.Content != "27 checks passed" || ownerArtifact.TaskID != source.Task.ID {
		t.Fatalf("RunArtifactContent() = %#v, %v", ownerArtifact, err)
	}
	report, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: active.Run.ID, Kind: domain.ObservationCompletion, Message: "implemented the storage seam",
		Evidence: []string{artifact.ID}, Handoff: "review the adapter boundary before changing the format",
		Payload: map[string]any{
			"changed_paths": []string{"src/storage.ts"}, "checks": []string{"npm test"},
			"remaining_risks": []string{"migration compatibility"}, "unknowns": []string{"upstream format revision"},
		},
		IdempotencyKey: "m23-source-completion",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := storage.ApplyQueuedRunReport(ctx, active.Run.ID, report.ID, true, nil,
		prepareTestRunLogArchive(t, storage, active.Run.ID), "", "m23-apply-completion")
	if err != nil || completed.Run.Status != domain.RunCompleted {
		t.Fatalf("ApplyQueuedRunReport() = %#v, %v", completed, err)
	}
	successorAfter, err := storage.TaskDetail(ctx, workspace.ID, successorMutation.Detail.Task.ID)
	if err != nil || !successorAfter.Readiness.Ready {
		t.Fatalf("successor after exact delivery = %#v, %v", successorAfter.Readiness, err)
	}
	assignedSuccessor, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: successorAfter.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: successorAfter.Task.Revision,
		IdempotencyKey: "m23-assign-successor", CorrelationID: "m23-assign-successor",
	})
	if err != nil {
		t.Fatal(err)
	}
	successorPacket, err := storage.BuildContextPacket(ctx, BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: successorAfter.Task.ID, AgentIdentifier: agent.Value.ID,
		CheckoutIdentifier: checkout.ID, ExpectedTaskRevision: assignedSuccessor.Detail.Task.Revision,
		IdempotencyKey: "m23-successor-context", CorrelationID: "m23-successor-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedDependencies, canonicalDependencies string
	if err := storage.db.QueryRowContext(ctx, `SELECT json_extract(packet.packet_json,'$.dependencies'),(
		SELECT json_group_array(json_object(
			'task_id', dependency_task.id, 'title', dependency_task.title,
			'status', dependency_task.status, 'revision', dependency_task.revision,
			'delivery_requirement', edge.delivery_requirement,
			'output', CASE WHEN dependency_task.status='completed' AND edge.delivery_requirement<>'completion' THEN json((
				SELECT json_object(
					'run_id', completed_run.id, 'summary', completion_report.message,
					'handoff', completed_handoff.summary, 'evidence_ids', json(completion_report.evidence_json),
					'changed_paths', COALESCE(json_extract(completion_report.payload_json,'$.changed_paths'),json('[]')),
					'checks', COALESCE(json_extract(completion_report.payload_json,'$.checks'),json('[]')),
					'remaining_risks', COALESCE(json_extract(completion_report.payload_json,'$.remaining_risks'),json('[]')),
					'unknowns', COALESCE(json_extract(completion_report.payload_json,'$.unknowns'),json('[]')))
				FROM runs completed_run JOIN run_handoffs completed_handoff ON completed_handoff.run_id=completed_run.id
				JOIN run_reports completion_report ON completion_report.run_id=completed_run.id AND completion_report.kind='completion' AND completion_report.status='applied'
				WHERE completed_run.task_id=dependency_task.id AND completed_run.status='completed'
				ORDER BY completed_run.finished_at DESC,completion_report.sequence DESC LIMIT 1))
			ELSE NULL END))
		FROM (SELECT depends_on_task_id,delivery_requirement FROM task_dependencies WHERE task_id=task.id ORDER BY depends_on_task_id) edge
		JOIN tasks dependency_task ON dependency_task.id=edge.depends_on_task_id)
		FROM context_packets packet JOIN tasks task ON task.id=packet.task_id WHERE packet.id=?`, successorPacket.Value.ID).Scan(&storedDependencies, &canonicalDependencies); err != nil {
		t.Fatal(err)
	}
	if storedDependencies != canonicalDependencies {
		t.Fatalf("stored dependency output = %s, canonical trigger output = %s", storedDependencies, canonicalDependencies)
	}
	if len(successorPacket.Value.Dependencies) != 1 {
		t.Fatalf("successor dependencies = %#v", successorPacket.Value.Dependencies)
	}
	dependency := successorPacket.Value.Dependencies[0]
	if dependency.DeliveryRequirement != domain.DependencyDeliveryHandoffWithEvidence || dependency.Output == nil ||
		dependency.Output.RunID != active.Run.ID || dependency.Output.Summary != "implemented the storage seam" ||
		dependency.Output.Handoff != "review the adapter boundary before changing the format" ||
		strings.Join(dependency.Output.EvidenceIDs, ",") != artifact.ID ||
		strings.Join(dependency.Output.ChangedPaths, ",") != "src/storage.ts" ||
		strings.Join(dependency.Output.Checks, ",") != "npm test" ||
		strings.Join(dependency.Output.RemainingRisks, ",") != "migration compatibility" ||
		strings.Join(dependency.Output.Unknowns, ",") != "upstream format revision" {
		t.Fatalf("successor dependency output = %#v", dependency)
	}
	successorRun, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: successorAfter.Task.ID, CheckoutIdentifier: checkout.ID,
		ContextPacketID: successorPacket.Value.ID, Runtime: "fake", Provider: "fake",
		Scenario: managementProgressScenario("m24-read-evidence"), ExpectedTaskRevision: assignedSuccessor.Detail.Task.Revision,
		IdempotencyKey: "m24-successor-run", CorrelationID: "m24-successor-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	successorStarting, err := storage.MarkRunStarting(ctx, successorRun.Detail.Run.ID, "m24-successor-starting")
	if err != nil {
		t.Fatal(err)
	}
	read, err := storage.ReadRunArtifactAsRun(ctx, successorStarting.ID, artifact.ID)
	if err != nil || read.Content != "27 checks passed" || read.Artifact.ContentHash != artifact.ContentHash {
		t.Fatalf("ReadRunArtifactAsRun() = %#v, %v", read, err)
	}
	if _, err := storage.ReadRunArtifactAsRun(ctx, successorStarting.ID, "artifact_00000000000000000000000000000000"); ErrorCode(err) != CodeRunArtifactNotFound {
		t.Fatalf("ReadRunArtifactAsRun(unknown) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestTaskAssignmentTransitionsAndDoubleAssignment(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	agent, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "implementer", Role: "implementer", Provider: "fake", IdempotencyKey: "agent", CorrelationID: "request-agent"})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "implement", "task")
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.Value.Name, LeaseSeconds: 300, ExpectedRevision: 1, IdempotencyKey: "assign", CorrelationID: "request-assign"})
	if err != nil || assigned.Detail.Task.Status != domain.TaskAssigned || assigned.Detail.Assignment == nil {
		t.Fatalf("AssignTask() = %#v, %v", assigned, err)
	}
	_, err = storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 300, ExpectedRevision: 2, IdempotencyKey: "assign-again", CorrelationID: "request-assign-again"})
	if ErrorCode(err) != CodeAssignmentConflict {
		t.Fatalf("AssignTask(double) error = %v, code = %q", err, ErrorCode(err))
	}
	started := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "start", "", 2)
	if started.Task.Status != domain.TaskActive {
		t.Fatalf("started task = %#v", started.Task)
	}
	blocked := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "block", "waiting for decision", 3)
	if blocked.Task.Status != domain.TaskBlocked || blocked.Task.BlockedReason == "" {
		t.Fatalf("blocked task = %#v", blocked.Task)
	}
	unblocked := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "unblock", "", 4)
	if unblocked.Task.Status != domain.TaskAssigned || unblocked.Assignment == nil {
		t.Fatalf("unblocked task = %#v", unblocked)
	}
	cancelled := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "cancel", "", 5)
	if cancelled.Task.Status != domain.TaskCancelled || cancelled.Assignment != nil || cancelled.Task.AssignedAgentID != "" {
		t.Fatalf("cancelled task = %#v", cancelled)
	}
	var released int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM task_assignments WHERE task_id = ? AND status = 'released'", task.Task.ID).Scan(&released); err != nil || released != 1 {
		t.Fatalf("released assignment count = %d, %v", released, err)
	}
}

func TestInvalidTaskTransitionsLeaveStateAndEventsUnchanged(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "state validation", "task-state-validation")
	eventsBefore := testWorkspaceEvents(t, storage, workspace.ID, 0, 100)
	for _, attempt := range []struct {
		action string
		reason string
	}{
		{action: "start"},
		{action: "unblock"},
		{action: "block"},
		{action: "complete"},
	} {
		_, mutationErr := storage.TransitionTask(context.Background(), TransitionTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, Action: attempt.action, Reason: attempt.reason, ExpectedRevision: 1, IdempotencyKey: "invalid-" + attempt.action, CorrelationID: "request-invalid-" + attempt.action})
		if ErrorCode(mutationErr) != CodeInvalidTransition {
			t.Errorf("TransitionTask(%q) error = %v, code = %q, want %q", attempt.action, mutationErr, ErrorCode(mutationErr), CodeInvalidTransition)
		}
	}
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID)
	if err != nil || detail.Task.Status != domain.TaskReady || detail.Task.Revision != 1 {
		t.Fatalf("TaskDetail(after invalid transitions) = %#v, %v", detail, err)
	}
	eventsAfter := testWorkspaceEvents(t, storage, workspace.ID, 0, 100)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("events after invalid transitions = %#v; before = %#v", eventsAfter, eventsBefore)
	}
}

func TestAssignmentExpiryUsesControlledClockAndRetainsHistory(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	agent, _ := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "implementer", Role: "implementer", Provider: "fake", IdempotencyKey: "agent", CorrelationID: "request-agent"})
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "leased", "task")
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 60, ExpectedRevision: 1, IdempotencyKey: "assign", CorrelationID: "request-assign"})
	if err != nil {
		t.Fatalf("AssignTask() error = %v", err)
	}
	now = now.Add(61 * time.Second)
	count, err := storage.ReconcileExpiredAssignments(context.Background(), workspace.ID, "request-expire")
	if err != nil || count != 1 {
		t.Fatalf("ReconcileExpiredAssignments() = %d, %v", count, err)
	}
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID)
	if err != nil || detail.Task.Status != domain.TaskReady || detail.Task.Revision != 3 || detail.Assignment != nil {
		t.Fatalf("TaskDetail(after expiry) = %#v, %v", detail, err)
	}
	var expired int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM task_assignments WHERE id = ? AND status = 'expired'", assigned.Detail.Assignment.ID).Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expired assignment count = %d, %v", expired, err)
	}
	count, err = storage.ReconcileExpiredAssignments(context.Background(), workspace.ID, "request-expire-again")
	if err != nil || count != 0 {
		t.Fatalf("second reconciliation = %d, %v", count, err)
	}
}

func TestM20LeaseReconciliationBatchesBoundPerWorkspaceWork(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	agent, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "lease-batcher", Role: "implementer", Provider: "fake",
		IdempotencyKey: "m20-lease-batcher-agent", CorrelationID: "m20-lease-batcher-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"one", "two", "three"} {
		task := createWorkTestTask(t, storage, workspace.ID, project.ID, "batch "+suffix, "m20-batch-task-"+suffix)
		if _, err := storage.AssignTask(context.Background(), AssignTaskCommand{
			WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.Value.ID,
			LeaseSeconds: 1, ExpectedRevision: task.Task.Revision,
			IdempotencyKey: "m20-batch-assignment-" + suffix, CorrelationID: "m20-batch-assignment-" + suffix,
		}); err != nil {
			t.Fatal(err)
		}
		addClaimTest(t, storage, AddClaimCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: task.Task.ID, CheckoutIdentifier: checkout.ID,
			Kind: domain.ClaimKindPath, Target: "batch/" + suffix, Mode: domain.ClaimModeExclusive,
			ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Second,
			IdempotencyKey: "m20-batch-claim-" + suffix, CorrelationID: "m20-batch-claim-" + suffix,
		})
	}
	now = now.Add(2 * time.Second)

	if count, err := storage.ReconcileExpiredAssignmentsBatch(context.Background(), workspace.ID, "m20-batch-assignments-one", 2); err != nil || count != 2 {
		t.Fatalf("first assignment batch = %d, %v; want 2", count, err)
	}
	if count, err := storage.ReconcileExpiredClaimsBatch(context.Background(), workspace.ID, "m20-batch-claims-one", 2); err != nil || count != 2 {
		t.Fatalf("first claim batch = %d, %v; want 2", count, err)
	}
	var activeAssignments, activeClaims int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM task_assignments WHERE status = 'active'").Scan(&activeAssignments); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM work_claims WHERE status = 'active'").Scan(&activeClaims); err != nil {
		t.Fatal(err)
	}
	if activeAssignments != 1 || activeClaims != 1 {
		t.Fatalf("active rows after first bounded pass = assignments %d claims %d, want 1/1", activeAssignments, activeClaims)
	}
	if count, err := storage.ReconcileExpiredAssignmentsBatch(context.Background(), workspace.ID, "m20-batch-assignments-two", 2); err != nil || count != 1 {
		t.Fatalf("second assignment batch = %d, %v; want 1", count, err)
	}
	if count, err := storage.ReconcileExpiredClaimsBatch(context.Background(), workspace.ID, "m20-batch-claims-two", 2); err != nil || count != 1 {
		t.Fatalf("second claim batch = %d, %v; want 1", count, err)
	}
	if _, err := storage.ReconcileExpiredAssignmentsBatch(context.Background(), workspace.ID, "m20-invalid-assignment-batch", MaximumLeaseReconciliationBatchLimit+1); ErrorCode(err) != CodeInvalidTask {
		t.Fatalf("oversized assignment batch error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.ReconcileExpiredClaimsBatch(context.Background(), workspace.ID, "m20-invalid-claim-batch", 0); ErrorCode(err) != CodeInvalidClaim {
		t.Fatalf("zero claim batch error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestBlockedTaskStaysBlockedWhenItsAssignmentExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	agent, _ := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "implementer", Role: "implementer", Provider: "fake", IdempotencyKey: "blocked-agent", CorrelationID: "request-blocked-agent"})
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "blocked lease", "blocked-task")
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.Value.ID, LeaseSeconds: 60, ExpectedRevision: 1, IdempotencyKey: "blocked-assign", CorrelationID: "request-blocked-assign"})
	if err != nil {
		t.Fatalf("AssignTask() error = %v", err)
	}
	blocked := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "block", "waiting for input", assigned.Detail.Task.Revision)
	now = now.Add(61 * time.Second)
	count, err := storage.ReconcileExpiredAssignments(context.Background(), workspace.ID, "request-blocked-expire")
	if err != nil || count != 1 {
		t.Fatalf("ReconcileExpiredAssignments() = %d, %v", count, err)
	}
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID)
	if err != nil || detail.Task.Status != domain.TaskBlocked || detail.Task.Revision != blocked.Task.Revision+1 || detail.Assignment != nil {
		t.Fatalf("TaskDetail(blocked after expiry) = %#v, %v", detail, err)
	}
	unblocked := transitionWorkTestTask(t, storage, workspace.ID, task.Task.ID, "unblock", "", detail.Task.Revision)
	if unblocked.Task.Status != domain.TaskReady || !unblocked.Readiness.Ready {
		t.Fatalf("unblocked task after lease expiry = %#v, want ready", unblocked)
	}
}

func TestConcurrentTaskUpdatesProduceOneRevisionConflict(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "race", "task-race")
	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index := range errorsByCall {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByCall[index] = storage.TransitionTask(context.Background(), TransitionTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, Action: "block", Reason: "writer", ExpectedRevision: 1, IdempotencyKey: "writer-" + string(rune('a'+index)), CorrelationID: "request-writer"})
		}(index)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range errorsByCall {
		if err == nil {
			successes++
		} else if ErrorCode(err) == CodeRevisionConflict {
			conflicts++
		} else {
			t.Fatalf("concurrent update error = %v, code = %q", err, ErrorCode(err))
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: successes=%d conflicts=%d errors=%v", successes, conflicts, errorsByCall)
	}
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID)
	if err != nil || detail.Task.Revision != 2 || detail.Task.Status != domain.TaskBlocked {
		t.Fatalf("final task = %#v, %v", detail, err)
	}
}

func initializeWorkTestProject(t *testing.T, storage *Store) (domain.Workspace, domain.Project) {
	t.Helper()
	workspace := initializeSourceTestWorkspace(t, storage).Workspace
	registered, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{WorkspaceIdentifier: workspace.ID, Name: "demo", IdempotencyKey: "project-demo", CorrelationID: "request-project", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "demo"), "main")})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	return workspace, registered.Project
}

func createWorkTestTask(t *testing.T, storage *Store, workspaceID, projectID, title, key string) domain.TaskDetail {
	t.Helper()
	result, err := storage.CreateTask(context.Background(), CreateTaskCommand{WorkspaceIdentifier: workspaceID, ProjectIdentifier: projectID, Title: title, Priority: 100, IdempotencyKey: key, CorrelationID: "request-" + key})
	if err != nil {
		t.Fatalf("CreateTask(%s) error = %v", title, err)
	}
	return result.Detail
}

func transitionWorkTestTask(t *testing.T, storage *Store, workspaceID, taskID, action, reason string, revision int64) domain.TaskDetail {
	t.Helper()
	result, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{WorkspaceIdentifier: workspaceID, TaskID: taskID, Action: action, Reason: reason, ExpectedRevision: revision, IdempotencyKey: action + "-task-" + taskID, CorrelationID: "request-" + action})
	if err != nil {
		t.Fatalf("TransitionTask(%s) error = %v", action, err)
	}
	return result.Detail
}
