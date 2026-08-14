package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestExecutionHealthReportsBoundedMechanicalUsageAndQueues(t *testing.T) {
	observed := time.Date(2036, 7, 8, 9, 10, 11, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
	workspace, project, agent, _, assigned := initializeRunTest(t, storage, "execution health")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "execution-health", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-execution-health")

	health, err := storage.ExecutionHealth(context.Background())
	if err != nil {
		t.Fatalf("ExecutionHealth() error = %v", err)
	}
	if health.ObservedAt != observed.Format(time.RFC3339Nano) || health.Node.Unresolved != 1 || health.Node.Starting != 1 {
		t.Fatalf("node health = %#v", health)
	}
	if len(health.Workspaces) != 1 || health.Workspaces[0].WorkspaceID != workspace.ID || health.Workspaces[0].Unresolved != 1 || health.Workspaces[0].Starting != 1 {
		t.Fatalf("workspace health = %#v", health.Workspaces)
	}
	if len(health.Projects) != 1 || health.Projects[0].ProjectID != project.ID || health.Projects[0].Unresolved != 1 || health.Projects[0].Starting != 1 {
		t.Fatalf("project health = %#v", health.Projects)
	}
	if len(health.Providers) != 1 || health.Providers[0].Provider != agent.Provider || health.Providers[0].Unresolved != 1 || health.Providers[0].Starting != 1 {
		t.Fatalf("provider health = %#v", health.Providers)
	}
	expectedQueueStates := 0
	for _, definition := range durableQueueRegistry {
		expectedQueueStates += len(definition.statuses)
	}
	if len(health.Queues) != expectedQueueStates {
		t.Fatalf("queue health length = %d, want registry-exact %d: %#v", len(health.Queues), expectedQueueStates, health.Queues)
	}
	foundPendingRun := false
	for _, queue := range health.Queues {
		if queue.Queue == "run" && queue.Status == "pending" {
			foundPendingRun = queue.Count == 1 && queue.OldestUpdatedAt == created.Run.UpdatedAt && queue.OldestAgeMillis == 0
		}
	}
	if !foundPendingRun {
		t.Fatalf("pending run queue state missing: %#v", health.Queues)
	}
	if health.TerminalLog != (domain.TerminalRunLogReferenceHealth{}) {
		t.Fatalf("terminal log reference health = %#v", health.TerminalLog)
	}
}
