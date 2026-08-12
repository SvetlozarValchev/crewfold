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

	agents, err := storage.Agents(context.Background(), workspace.Name)
	if err != nil || len(agents) != 1 || agents[0].ID != agent.Value.ID {
		t.Fatalf("Agents() = %#v, %v", agents, err)
	}
	objectives, err := storage.Objectives(context.Background(), workspace.ID, project.Name)
	if err != nil || len(objectives) != 1 || objectives[0].ID != objective.Value.ID {
		t.Fatalf("Objectives() = %#v, %v", objectives, err)
	}
	tasks, err := storage.Tasks(context.Background(), workspace.ID, project.ID, "request-list", true)
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
	ready, err := storage.Tasks(context.Background(), workspace.ID, project.ID, "request-ready", true)
	if err != nil || len(ready) != 1 || ready[0].Task.ID != first.Task.ID {
		t.Fatalf("Tasks(ready) = %#v, %v, want only foundation", ready, err)
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
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID, "request-show")
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
	detail, err := storage.TaskDetail(context.Background(), workspace.ID, task.Task.ID, "request-final")
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
