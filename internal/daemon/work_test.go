package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestCoordinationThroughLocalAPIPersistsAcrossRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	baseTime := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	var clockNanos atomic.Int64
	clockNanos.Store(baseTime.UnixNano())
	config := testConfig(t)
	config.StoreOptions.Clock = func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() }
	config.LeaseReconcileInterval = 20 * time.Millisecond

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "coordination-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeExclusive, "coordination-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	agent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "implementer", Role: "implementer", Provider: "fake", Runtime: "fake", MaxConcurrency: 2, IdempotencyKey: "coordination-agent"})
	if err != nil {
		t.Fatalf("AgentCreate() error = %v", err)
	}
	replayedAgent, err := client.AgentCreate(context.Background(), localapi.AgentCreateParams{Workspace: "personal", Name: "implementer", Role: "implementer", Provider: "fake", Runtime: "fake", MaxConcurrency: 2, IdempotencyKey: "coordination-agent"})
	if err != nil || !reflect.DeepEqual(replayedAgent, agent) {
		t.Fatalf("AgentCreate(replay) = %#v, %v; want %#v", replayedAgent, err, agent)
	}
	objective, err := client.ObjectiveCreate(context.Background(), localapi.ObjectiveCreateParams{Workspace: "personal", Project: "demo", Title: "Ship greeting", Budget: domain.Budget{TokenLimit: 20_000, CostCents: 500, TimeSeconds: 3_600}, IdempotencyKey: "coordination-objective"})
	if err != nil {
		t.Fatalf("ObjectiveCreate() error = %v", err)
	}

	createTask := func(title, key string) localapi.TaskMutationResult {
		t.Helper()
		result, createErr := client.TaskCreate(context.Background(), localapi.TaskCreateParams{Workspace: "personal", Project: "demo", Objective: objective.Objective.ID, Title: title, Priority: 100, Budget: domain.Budget{TokenLimit: 5_000}, IdempotencyKey: key})
		if createErr != nil {
			t.Fatalf("TaskCreate(%q) error = %v", title, createErr)
		}
		return result
	}
	lifecycle := createTask("Implement greeting", "coordination-task-lifecycle")
	dependent := createTask("Document greeting", "coordination-task-dependent")
	expiring := createTask("Investigate edge case", "coordination-task-expiring")
	conflicting := createTask("Choose wording", "coordination-task-conflicting")

	dependent, err = client.TaskDepend(context.Background(), localapi.TaskDependencyParams{Workspace: "personal", Task: dependent.Detail.Task.ID, DependsOn: lifecycle.Detail.Task.ID, ExpectedRevision: dependent.Detail.Task.Revision, IdempotencyKey: "coordination-dependency"})
	if err != nil {
		t.Fatalf("TaskDepend() error = %v", err)
	}
	if dependent.Detail.Readiness.Ready || dependent.Detail.Readiness.Reason == "" {
		t.Fatalf("dependent readiness = %#v, want a deterministic waiting explanation", dependent.Detail.Readiness)
	}
	if _, err := client.TaskDepend(context.Background(), localapi.TaskDependencyParams{Workspace: "personal", Task: lifecycle.Detail.Task.ID, DependsOn: dependent.Detail.Task.ID, ExpectedRevision: lifecycle.Detail.Task.Revision, IdempotencyKey: "coordination-cycle"}); localAPIErrorCode(err) != store.CodeDependencyCycle {
		t.Fatalf("TaskDepend(cycle) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	ready, err := client.TaskList(context.Background(), localapi.TaskListParams{Workspace: "personal", Project: "demo", ReadyOnly: true})
	if err != nil {
		t.Fatalf("TaskList(ready) error = %v", err)
	}
	if len(ready.Tasks) != 3 {
		t.Fatalf("ready task count = %d, want 3; tasks = %#v", len(ready.Tasks), ready.Tasks)
	}

	lifecycle, err = client.TaskAssign(context.Background(), localapi.TaskAssignParams{Workspace: "personal", Task: lifecycle.Detail.Task.ID, Agent: "implementer", LeaseSeconds: 300, ExpectedRevision: lifecycle.Detail.Task.Revision, IdempotencyKey: "coordination-assign-lifecycle"})
	if err != nil {
		t.Fatalf("TaskAssign() error = %v", err)
	}
	if _, err := client.TaskAssign(context.Background(), localapi.TaskAssignParams{Workspace: "personal", Task: lifecycle.Detail.Task.ID, Agent: agent.Agent.ID, LeaseSeconds: 300, ExpectedRevision: lifecycle.Detail.Task.Revision, IdempotencyKey: "coordination-double-assign"}); localAPIErrorCode(err) != store.CodeAssignmentConflict {
		t.Fatalf("TaskAssign(double) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	for _, transition := range []struct {
		action string
		reason string
		status string
	}{
		{action: "start", status: domain.TaskActive},
		{action: "block", reason: "waiting for owner input", status: domain.TaskBlocked},
		{action: "unblock", status: domain.TaskAssigned},
		{action: "cancel", status: domain.TaskCancelled},
	} {
		lifecycle, err = client.TaskTransition(context.Background(), localapi.TaskTransitionParams{Workspace: "personal", Task: lifecycle.Detail.Task.ID, Action: transition.action, Reason: transition.reason, ExpectedRevision: lifecycle.Detail.Task.Revision, IdempotencyKey: "coordination-" + transition.action})
		if err != nil {
			t.Fatalf("TaskTransition(%s) error = %v", transition.action, err)
		}
		if lifecycle.Detail.Task.Status != transition.status {
			t.Fatalf("TaskTransition(%s) status = %q, want %q", transition.action, lifecycle.Detail.Task.Status, transition.status)
		}
	}

	expiring, err = client.TaskAssign(context.Background(), localapi.TaskAssignParams{Workspace: "personal", Task: expiring.Detail.Task.ID, Agent: "implementer", LeaseSeconds: 60, ExpectedRevision: expiring.Detail.Task.Revision, IdempotencyKey: "coordination-assign-expiring"})
	if err != nil {
		t.Fatalf("TaskAssign(expiring) error = %v", err)
	}
	clockNanos.Store(baseTime.Add(61 * time.Second).UnixNano())
	var expired localapi.TaskShowResult
	deadline := time.Now().Add(2*config.LeaseReconcileInterval + time.Second)
	for {
		expired, err = client.TaskShow(context.Background(), "personal", expiring.Detail.Task.ID)
		if err != nil {
			t.Fatalf("TaskShow(expired) error = %v", err)
		}
		if expired.Detail.Assignment == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease reconciler did not expire task before %s", deadline)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if expired.Detail.Task.Status != domain.TaskReady || expired.Detail.Assignment != nil || expired.Detail.Task.Revision != expiring.Detail.Task.Revision+1 {
		t.Fatalf("expired task = %#v, want ready with retained revision history and no active assignment", expired.Detail)
	}

	titles := []string{"Wording A", "Wording B"}
	var wait sync.WaitGroup
	errorsByWriter := make([]error, len(titles))
	resultsByWriter := make([]localapi.TaskMutationResult, len(titles))
	for index, title := range titles {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resultsByWriter[index], errorsByWriter[index] = client.TaskUpdate(context.Background(), localapi.TaskUpdateParams{Workspace: "personal", Task: conflicting.Detail.Task.ID, Title: &title, ExpectedRevision: conflicting.Detail.Task.Revision, IdempotencyKey: "coordination-writer-" + title})
		}()
	}
	wait.Wait()
	successes, conflicts := 0, 0
	for index, updateErr := range errorsByWriter {
		if updateErr == nil {
			successes++
			conflicting = resultsByWriter[index]
		} else if localAPIErrorCode(updateErr) == store.CodeRevisionConflict {
			conflicts++
		} else {
			t.Fatalf("concurrent TaskUpdate(%d) unexpected error = %v", index, updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent updates: successes=%d conflicts=%d, want 1 and 1", successes, conflicts)
	}

	status, err := client.CoordinationStatus(context.Background(), "personal")
	if err != nil {
		t.Fatalf("CoordinationStatus() error = %v", err)
	}
	if status.Status.AgentsRegistered != 1 || status.Status.TasksRegistered != 4 || status.Status.TasksCancelled != 1 || status.Status.TasksReady != 2 {
		t.Fatalf("CoordinationStatus() = %#v, want one agent and four classified tasks", status.Status)
	}
	agentsBefore, _ := client.AgentList(context.Background(), localapi.AgentListParams{Workspace: "personal"})
	objectivesBefore, _ := client.ObjectiveList(context.Background(), localapi.ObjectiveListParams{Workspace: "personal", Project: project.Project.ID})
	tasksBefore, _ := client.TaskList(context.Background(), localapi.TaskListParams{Workspace: "personal", Project: project.Project.ID})
	eventsBefore, err := client.EventsList(context.Background(), localapi.EventsListParams{Workspace: "personal", After: 0, PageParams: localapi.PageParams{Limit: 100}})
	if err != nil || len(eventsBefore.Events) < 15 {
		t.Fatalf("EventsList() = %#v, %v, want complete coordination history", eventsBefore, err)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}

	second := startTestServer(t, config)
	restarted := localapi.NewClient(config.SocketPath)
	agentsAfter, err := restarted.AgentList(context.Background(), localapi.AgentListParams{Workspace: "personal"})
	if err != nil || !reflect.DeepEqual(agentsAfter, agentsBefore) {
		t.Fatalf("AgentList(after restart) = %#v, %v; want %#v", agentsAfter, err, agentsBefore)
	}
	objectivesAfter, err := restarted.ObjectiveList(context.Background(), localapi.ObjectiveListParams{Workspace: "personal", Project: project.Project.ID})
	if err != nil || !reflect.DeepEqual(objectivesAfter, objectivesBefore) {
		t.Fatalf("ObjectiveList(after restart) = %#v, %v; want %#v", objectivesAfter, err, objectivesBefore)
	}
	tasksAfter, err := restarted.TaskList(context.Background(), localapi.TaskListParams{Workspace: "personal", Project: project.Project.ID})
	if err != nil || !reflect.DeepEqual(tasksAfter, tasksBefore) {
		t.Fatalf("TaskList(after restart) = %#v, %v; want %#v", tasksAfter, err, tasksBefore)
	}
	eventsAfter, err := restarted.EventsList(context.Background(), localapi.EventsListParams{Workspace: "personal", After: 0, PageParams: localapi.PageParams{Limit: 100}})
	if err != nil || !reflect.DeepEqual(eventsAfter, eventsBefore) {
		t.Fatalf("EventsList(after restart) changed: before=%#v after=%#v err=%v", eventsBefore, eventsAfter, err)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}
