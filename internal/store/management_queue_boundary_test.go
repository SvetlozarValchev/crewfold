package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestSupervisorReadinessOrderUsesLaterOfIntentAndDependencyCompletion(t *testing.T) {
	observed := time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := func() time.Time {
		observed = observed.Add(time.Millisecond)
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
		Clock:                clock,
	})
	runID, cursor := invokeAdversarialManager(t, storage, fixture, "readiness-later-of")

	upstream, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		ObjectiveID:         fixture.objective.ID,
		Title:               "Already completed prerequisite",
		Priority:            100,
		Budget:              domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		IdempotencyKey:      "readiness-later-of-upstream",
		CorrelationID:       "request-readiness-later-of-upstream",
	})
	if err != nil {
		t.Fatalf("CreateTask(upstream) = %v", err)
	}
	queueBoundaryCompleteTask(t, storage, fixture.workspace.ID, upstream.Detail.Task.ID, "request-readiness-later-of-complete")

	earlier := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "readiness-earlier", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "earlier-independent", LaunchProfileID: fixture.target.ID,
			Title: "Earlier independent intent", Priority: 100,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	later := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "readiness-later", []domain.ManagerProposalAction{
		{
			Type: domain.ProposalActionCreateTask,
			CreateTask: &domain.ProposalCreateTaskAction{
				TaskKey: "later-dependent", LaunchProfileID: fixture.target.ID,
				Title: "Later intent with old dependency", Priority: 100,
				Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
			},
		},
		{
			Type: domain.ProposalActionAddDependency,
			AddDependency: &domain.ProposalAddDependencyAction{
				Task: domain.ProposalTaskRef{ProposalTaskKey: "later-dependent"},
				DependsOn: domain.ProposalTaskRef{
					TaskID: upstream.Detail.Task.ID, ExpectedTaskRevision: upstream.Detail.Task.Revision + 1,
				},
			},
		},
	})
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "readiness-later-of")

	earlierIntentID := queueBoundaryProposalIntentID(t, storage, earlier.ID)
	laterIntentID := queueBoundaryProposalIntentID(t, storage, later.ID)
	var earlierTaskID string
	if err := storage.db.QueryRow(`SELECT task_id FROM scheduling_intents WHERE id=?`, earlierIntentID).Scan(&earlierTaskID); err != nil {
		t.Fatalf("read earlier task id = %v", err)
	}
	metadataOnly := "metadata changed while the task remained ready"
	var earlierRevision int64
	if err := storage.db.QueryRow(`SELECT revision FROM tasks WHERE id=?`, earlierTaskID).Scan(&earlierRevision); err != nil {
		t.Fatalf("read earlier task revision = %v", err)
	}
	if _, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: earlierTaskID, ExpectedRevision: earlierRevision,
		Description: &metadataOnly, IdempotencyKey: "readiness-metadata-update", CorrelationID: "request-readiness-metadata-update",
	}); err != nil {
		t.Fatalf("UpdateTask(metadata-only) = %v", err)
	}
	var earlierCreated, laterCreated, dependencyCompleted string
	if err := storage.db.QueryRow(`SELECT created_at FROM scheduling_intents WHERE id=?`, earlierIntentID).Scan(&earlierCreated); err != nil {
		t.Fatalf("read earlier intent time = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT created_at FROM scheduling_intents WHERE id=?`, laterIntentID).Scan(&laterCreated); err != nil {
		t.Fatalf("read later intent time = %v", err)
	}
	if err := storage.db.QueryRow(`SELECT updated_at FROM tasks WHERE id=?`, upstream.Detail.Task.ID).Scan(&dependencyCompleted); err != nil {
		t.Fatalf("read dependency completion time = %v", err)
	}
	if !(dependencyCompleted < earlierCreated && earlierCreated < laterCreated) {
		t.Fatalf("fixture times dependency=%q earlier=%q later=%q; want dependency < earlier < later", dependencyCompleted, earlierCreated, laterCreated)
	}

	explanation, err := storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
	})
	if err != nil {
		t.Fatalf("ExplainSupervisor() = %v", err)
	}
	positions := map[string]int{}
	for index, candidate := range explanation.Candidates {
		positions[candidate.IntentID] = index
	}
	if _, exists := positions[earlierIntentID]; !exists {
		t.Fatalf("readiness explanation omitted earlier intent %s: %#v", earlierIntentID, explanation.Candidates)
	}
	if _, exists := positions[laterIntentID]; !exists {
		t.Fatalf("readiness explanation omitted later intent %s: %#v", laterIntentID, explanation.Candidates)
	}
	if positions[earlierIntentID] >= positions[laterIntentID] {
		t.Fatalf("readiness order = %#v; earlier no-dependency intent %s must precede later-created intent %s even though its dependency completed long ago",
			explanation.Candidates, earlierIntentID, laterIntentID)
	}
}

func TestSupervisorDeferredPageBackoffCannotStarveLaterEligibleIntent(t *testing.T) {
	observed := time.Date(2036, 2, 3, 4, 5, 6, 0, time.UTC)
	autoAdvance := true
	clock := func() time.Time {
		if autoAdvance {
			observed = observed.Add(time.Millisecond)
		}
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
		Clock:                clock,
	})
	activeProfile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.target.AgentID, ExpectedAgentRevision: fixture.target.AgentRevision,
		Purpose: "active tail profile", Runtime: fixture.target.Runtime, Provider: fixture.target.Provider,
		Scenario: fixture.target.Scenario, AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900,
		IdempotencyKey: "queue-boundary-active-profile", CorrelationID: "request-queue-boundary-active-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(active tail) = %v", err)
	}
	grant, err := storage.CreateManagerGrant(context.Background(), CreateManagerGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID, AgentIdentifier: fixture.manager.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds:     []string{domain.ManagerProposalTaskDecomposition},
		LaunchProfileIDs:  []string{fixture.target.ID, activeProfile.Value.ID},
		AllowedClaimKinds: []string{domain.ClaimKindComponent},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 4, MaxClaimRequirements: 2,
			Budget: domain.Budget{TokenLimit: 1000, CostCents: 1000, TimeSeconds: 1000},
		},
		IdempotencyKey: "queue-boundary-grant", CorrelationID: "request-queue-boundary-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(queue boundary) = %v", err)
	}
	fixture.grant = grant.Value
	runID, cursor := invokeAdversarialManager(t, storage, fixture, "queue-boundary")

	const staleIntentCount = 101
	created := 0
	for proposalIndex := 0; created < staleIntentCount; proposalIndex++ {
		actions := make([]domain.ManagerProposalAction, 0, 4)
		for len(actions) < 4 && created < staleIntentCount {
			taskIndex := created
			actions = append(actions, domain.ManagerProposalAction{
				Type: domain.ProposalActionCreateTask,
				CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: fmt.Sprintf("stale-%03d", taskIndex), LaunchProfileID: fixture.target.ID,
					Title: fmt.Sprintf("Stale queue head %03d", taskIndex), Priority: 100,
					Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
				},
			})
			created++
		}
		queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, fmt.Sprintf("queue-stale-%02d", proposalIndex), actions)
	}
	tailProposal := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "queue-active-tail", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "active-tail", LaunchProfileID: activeProfile.Value.ID,
			Title: "Eligible task behind stale queue pages", Priority: 1,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	tailIntentID := queueBoundaryProposalIntentID(t, storage, tailProposal.ID)
	if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, LaunchProfileID: fixture.target.ID,
		ExpectedRevision: fixture.target.Revision, Reason: "make the deterministic queue head unavailable",
		IdempotencyKey: "queue-boundary-retire", CorrelationID: "request-queue-boundary-retire",
	}); err != nil {
		t.Fatalf("RetireLaunchProfile(queue head) = %v", err)
	}
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "queue-boundary")
	autoAdvance = false
	firstPassAt := observed

	first, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "queue-boundary-first", CorrelationID: "request-queue-boundary-first",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(first page) = %v", err)
	}
	if len(first.Actions) != 100 || len(first.ScheduledRunIDs) != 0 {
		t.Fatalf("RunSupervisor(first page) = %#v; want exactly 100 stale-profile deferrals", first)
	}
	for _, action := range first.Actions {
		if action.Status != domain.SupervisorActionDeferred {
			t.Fatalf("first-page action = %#v; want only deferred actions", action)
		}
	}
	assertManagementRowCount(t, storage, 100, `SELECT COUNT(*) FROM scheduling_intents WHERE status='deferred'`)
	assertManagementRowCount(t, storage, 2, `SELECT COUNT(*) FROM scheduling_intents WHERE status='pending'`)
	// A broadly scoped but unrelated capacity fact must not wake stale-profile
	// deferrals. Otherwise a busy workspace could keep the first page hot and
	// starve the eligible tail forever.
	if _, err := storage.MarkRunStarting(context.Background(), runID, "request-queue-boundary-manager-starting"); err != nil {
		t.Fatalf("MarkRunStarting(unrelated manager run) = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), runID, "queue-boundary-manager-runtime", "queue-boundary-manager-provider", "request-queue-boundary-manager-started"); err != nil {
		t.Fatalf("MarkRunStarted(unrelated manager run) = %v", err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "unrelated manager work completed",
		Evidence: []string{"proposal accepted"}, Handoff: "continue deterministic scheduling", LogArchive: prepareTestRunLogArchive(t, storage, runID),
	}, true, nil, "request-queue-boundary-manager-completed"); err != nil {
		t.Fatalf("ApplyRunObservation(unrelated manager completion) = %v", err)
	}

	second, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "queue-boundary-second", CorrelationID: "request-queue-boundary-second",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(immediate next page) = %v", err)
	}
	if len(second.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(immediate next page) = %#v; want eligible tail scheduled past backed-off head", second)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, tailIntentID)
	assertManagementRowCount(t, storage, staleIntentCount, `SELECT COUNT(*) FROM scheduling_intents WHERE status='deferred'`)

	var witnessID, retryAt string
	var witnessRevision int64
	if err := storage.db.QueryRow(`SELECT id,revision,next_attempt_at FROM scheduling_intents
WHERE status='deferred' ORDER BY created_at,task_id,id LIMIT 1`).Scan(&witnessID, &witnessRevision, &retryAt); err != nil {
		t.Fatalf("read backed-off witness = %v", err)
	}
	wantRetryAt := firstPassAt.Add(supervisorSchedulingRetryDelay).UTC().Format(time.RFC3339Nano)
	if retryAt != wantRetryAt || witnessRevision != 2 {
		t.Fatalf("backed-off witness revision/time = %d/%q, want 2/%q", witnessRevision, retryAt, wantRetryAt)
	}
	var deferredActionCount int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM supervisor_action_receipts receipt
JOIN supervisor_actions action ON action.id=receipt.action_id WHERE action.status='deferred'`).Scan(&deferredActionCount); err != nil {
		t.Fatalf("count deferred actions = %v", err)
	}
	if deferredActionCount != staleIntentCount {
		t.Fatalf("deferred action count = %d, want %d", deferredActionCount, staleIntentCount)
	}
	observed = firstPassAt.Add(supervisorSchedulingRetryDelay - time.Nanosecond)
	beforeBoundary, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "queue-boundary-before", CorrelationID: "request-queue-boundary-before",
	})
	if err != nil || len(beforeBoundary.Actions) != 0 || len(beforeBoundary.ScheduledRunIDs) != 0 {
		t.Fatalf("RunSupervisor(before retry boundary) = %#v, %v; want no reconsideration", beforeBoundary, err)
	}
	queueBoundaryAssertIntentBackoff(t, storage, witnessID, 2, wantRetryAt)

	observed = firstPassAt.Add(supervisorSchedulingRetryDelay)
	atBoundary, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "queue-boundary-exact", CorrelationID: "request-queue-boundary-exact",
	})
	if err != nil || len(atBoundary.Actions) != 0 || len(atBoundary.ScheduledRunIDs) != 0 {
		t.Fatalf("RunSupervisor(at retry boundary) = %#v, %v; want reconsideration without duplicate action", atBoundary, err)
	}
	queueBoundaryAssertIntentBackoff(t, storage, witnessID, 3,
		firstPassAt.Add(2*supervisorSchedulingRetryDelay).UTC().Format(time.RFC3339Nano))
	assertManagementRowCount(t, storage, staleIntentCount, `SELECT COUNT(*) FROM supervisor_action_receipts receipt
JOIN supervisor_actions action ON action.id=receipt.action_id WHERE action.status='deferred'`)
}

func TestSupervisorCandidatePageSkipsNonReadyOpenIntentHeads(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
	})
	runID, cursor := invokeAdversarialManager(t, storage, fixture, "queue-nonready-head")

	const nonReadyHeads = 100
	headTaskIDs := make([]string, 0, nonReadyHeads)
	created := 0
	for proposalIndex := 0; created < nonReadyHeads; proposalIndex++ {
		actions := make([]domain.ManagerProposalAction, 0, 4)
		for len(actions) < 4 && created < nonReadyHeads {
			taskIndex := created
			actions = append(actions, domain.ManagerProposalAction{
				Type: domain.ProposalActionCreateTask,
				CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: fmt.Sprintf("nonready-%03d", taskIndex), LaunchProfileID: fixture.target.ID,
					Title: fmt.Sprintf("Non-ready queue head %03d", taskIndex), Priority: 100,
					Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
				},
			})
			created++
		}
		proposal := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor,
			fmt.Sprintf("queue-nonready-%02d", proposalIndex), actions)
		rows, err := storage.db.Query(`SELECT task_id FROM scheduling_intents WHERE source_proposal_id=? ORDER BY task_id`, proposal.ID)
		if err != nil {
			t.Fatalf("read non-ready proposal tasks = %v", err)
		}
		for rows.Next() {
			var taskID string
			if err := rows.Scan(&taskID); err != nil {
				rows.Close()
				t.Fatalf("scan non-ready proposal task = %v", err)
			}
			headTaskIDs = append(headTaskIDs, taskID)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close non-ready proposal tasks = %v", err)
		}
	}
	if len(headTaskIDs) != nonReadyHeads {
		t.Fatalf("non-ready fixture tasks = %d, want %d", len(headTaskIDs), nonReadyHeads)
	}
	for index, taskID := range headTaskIDs {
		if _, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: taskID, ExpectedRevision: 1,
			Action: "block", Reason: "remain outside the scheduling-ready set",
			IdempotencyKey: fmt.Sprintf("queue-nonready-block-%03d", index),
			CorrelationID:  fmt.Sprintf("request-queue-nonready-block-%03d", index),
		}); err != nil {
			t.Fatalf("TransitionTask(block queue head %d) = %v", index, err)
		}
	}
	tailProposal := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "queue-ready-tail", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "ready-tail", LaunchProfileID: fixture.target.ID,
			Title: "Ready task after non-ready queue heads", Priority: 1,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	tailIntentID := queueBoundaryProposalIntentID(t, storage, tailProposal.ID)
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "queue-nonready-head")

	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "queue-nonready-scan", CorrelationID: "request-queue-nonready-scan",
	})
	if err != nil || len(result.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(non-ready queue heads) = %#v, %v; want ready tail scheduled in same pass", result, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, tailIntentID)
	assertManagementRowCount(t, storage, nonReadyHeads, `SELECT COUNT(*) FROM scheduling_intents intent
JOIN tasks task ON task.id=intent.task_id WHERE intent.status='pending' AND task.status='blocked'`)
}

func TestSupervisorReadinessOrderUsesTaskBecameReadyTime(t *testing.T) {
	observed := time.Date(2037, 3, 4, 5, 6, 7, 0, time.UTC)
	clock := func() time.Time {
		observed = observed.Add(time.Millisecond)
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: true,
		Clock:                clock,
	})
	runID, cursor := invokeAdversarialManager(t, storage, fixture, "readiness-unblocked")
	earlier := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "readiness-unblocked-earlier", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "earlier-blocked", LaunchProfileID: fixture.target.ID,
			Title: "Earlier intent that becomes ready later", Priority: 100,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	earlierIntentID := queueBoundaryProposalIntentID(t, storage, earlier.ID)
	var earlierTaskID string
	if err := storage.db.QueryRow(`SELECT task_id FROM scheduling_intents WHERE id=?`, earlierIntentID).Scan(&earlierTaskID); err != nil {
		t.Fatalf("read earlier task id = %v", err)
	}
	blocked, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: earlierTaskID, ExpectedRevision: 1,
		Action: "block", Reason: "temporarily not eligible",
		IdempotencyKey: "readiness-unblocked-block", CorrelationID: "request-readiness-unblocked-block",
	})
	if err != nil {
		t.Fatalf("TransitionTask(block earlier) = %v", err)
	}
	later := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "readiness-unblocked-later", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "later-stable", LaunchProfileID: fixture.target.ID,
			Title: "Later intent already ready", Priority: 100,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	laterIntentID := queueBoundaryProposalIntentID(t, storage, later.ID)
	if _, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: earlierTaskID,
		ExpectedRevision: blocked.Detail.Task.Revision, Action: "unblock",
		IdempotencyKey: "readiness-unblocked-unblock", CorrelationID: "request-readiness-unblocked-unblock",
	}); err != nil {
		t.Fatalf("TransitionTask(unblock earlier) = %v", err)
	}
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "readiness-unblocked")

	explanation, err := storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
	})
	if err != nil {
		t.Fatalf("ExplainSupervisor(unblock readiness) = %v", err)
	}
	positions := map[string]int{}
	for index, candidate := range explanation.Candidates {
		positions[candidate.IntentID] = index
	}
	if _, exists := positions[earlierIntentID]; !exists {
		t.Fatalf("unblock readiness explanation omitted earlier intent %s: %#v", earlierIntentID, explanation.Candidates)
	}
	if _, exists := positions[laterIntentID]; !exists {
		t.Fatalf("unblock readiness explanation omitted later intent %s: %#v", laterIntentID, explanation.Candidates)
	}
	if positions[laterIntentID] >= positions[earlierIntentID] {
		t.Fatalf("readiness order = %#v; stable-ready intent %s must precede intent %s that became ready only on later unblock",
			explanation.Candidates, laterIntentID, earlierIntentID)
	}
}

func TestSupervisorEligibilityChangeWakesDeferredIntentBeforeBackoff(t *testing.T) {
	observed := time.Date(2038, 4, 5, 6, 7, 8, 0, time.UTC)
	clock := func() time.Time {
		observed = observed.Add(time.Millisecond)
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		SharedTargetCheckout: false,
		Clock:                clock,
	})
	acceptAdversarialSchedulingPair(t, storage, fixture, "deferred-wake-terminal", "")
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "deferred-wake-terminal")

	initial, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "deferred-wake-initial", CorrelationID: "request-deferred-wake-initial",
	})
	if err != nil || len(initial.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(initial exclusive contention) = %#v, %v; want one scheduled run", initial, err)
	}
	var deferredIntentID, nextAttemptAt string
	if err := storage.db.QueryRow(`SELECT id,next_attempt_at FROM scheduling_intents WHERE status='deferred'`).
		Scan(&deferredIntentID, &nextAttemptAt); err != nil {
		t.Fatalf("read deferred exclusive intent = %v", err)
	}
	nextAttempt, err := time.Parse(time.RFC3339Nano, nextAttemptAt)
	if err != nil || !nextAttempt.After(observed) {
		t.Fatalf("deferred retry time = %q, %v; want future backoff after %s", nextAttemptAt, err, observed)
	}
	if _, err := storage.FailRunStart(context.Background(), initial.ScheduledRunIDs[0],
		"definite terminal state releases the exact checkout capacity", "request-deferred-wake-failed"); err != nil {
		t.Fatalf("FailRunStart(capacity owner) = %v", err)
	}
	if !observed.Before(nextAttempt) {
		t.Fatalf("fixture reached retry deadline %s before wake scan at %s", nextAttempt, observed)
	}
	var storedNextAttemptAt string
	if err := storage.db.QueryRow(`SELECT COALESCE(next_attempt_at,'') FROM scheduling_intents WHERE id=?`, deferredIntentID).
		Scan(&storedNextAttemptAt); err != nil {
		t.Fatalf("read eligibility wake = %v", err)
	}
	if storedNextAttemptAt != nextAttemptAt {
		t.Fatalf("eligibility-changing event rewrote stable backoff %q to %q; event-aware scan should wake it without a bulk write", nextAttemptAt, storedNextAttemptAt)
	}

	woken, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "deferred-wake-rescan", CorrelationID: "request-deferred-wake-rescan",
	})
	if err != nil || len(woken.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(after terminal capacity release) = %#v, %v; want deferred intent scheduled before old deadline", woken, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, deferredIntentID)
}

func TestSupervisorCheckoutMetadataDoesNotWakeOccupiedPinnedCheckout(t *testing.T) {
	observed := time.Date(2041, 7, 8, 9, 10, 11, 0, time.UTC)
	autoAdvance := true
	clock := func() time.Time {
		if autoAdvance {
			observed = observed.Add(time.Millisecond)
		}
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8,
		Clock:                clock,
	})
	ctx := context.Background()
	inspection, err := storage.InspectProject(ctx, fixture.workspace.ID, fixture.project.ID)
	if err != nil || len(inspection.Checkouts) != 1 || len(inspection.Repositories) != 1 {
		t.Fatalf("InspectProject(checkout wake fixture) = %#v, %v; want one checkout and repository", inspection, err)
	}
	checkout := inspection.Checkouts[0]
	repository := inspection.Repositories[0]
	pinnedProfile := queueBoundaryCreateLaunchProfile(t, storage, fixture, domain.AgentDefinition{
		ID: fixture.target.AgentID,
		// The exact immutable values are copied from the launch profile below;
		// queueBoundaryCreateLaunchProfile only needs this agent projection.
		Revision:       fixture.target.AgentRevision,
		Runtime:        fixture.target.Runtime,
		Provider:       fixture.target.Provider,
		MaxConcurrency: 8,
	}, checkout.ID, "checkout-metadata-pinned")
	fixture.grant = queueBoundaryCreateManagerGrant(t, storage, fixture,
		[]string{pinnedProfile.ID}, "checkout-metadata")
	managerRunID, cursor := invokeAdversarialManager(t, storage, fixture, "checkout-metadata-manager")
	proposal := queueBoundaryAcceptProposal(t, storage, fixture, managerRunID, cursor, "checkout-metadata-intent", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "pinned-checkout", LaunchProfileID: pinnedProfile.ID,
			Title: "Pinned intent behind occupied checkout", Priority: 100,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	intentID := queueBoundaryProposalIntentID(t, storage, proposal.ID)
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "checkout-metadata")
	autoAdvance = false

	initial, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "checkout-metadata-initial", CorrelationID: "request-checkout-metadata-initial",
	})
	if err != nil || len(initial.Actions) != 1 || initial.Actions[0].Status != domain.SupervisorActionDeferred {
		t.Fatalf("RunSupervisor(occupied pinned checkout) = %#v, %v; want one deferral", initial, err)
	}
	if primary := queueBoundaryPrimaryFailure(t, initial.Actions[0].ConstraintSnapshot); primary != "checkout" {
		t.Fatalf("occupied pinned checkout primary failure = %q, want checkout", primary)
	}
	var revision int64
	var retryAt string
	if err := storage.db.QueryRow(`SELECT revision,next_attempt_at FROM scheduling_intents WHERE id=?`, intentID).
		Scan(&revision, &retryAt); err != nil {
		t.Fatalf("read occupied checkout backoff = %v", err)
	}
	retryDeadline, parseErr := time.Parse(time.RFC3339Nano, retryAt)
	if revision != 2 || parseErr != nil || !retryDeadline.After(observed) {
		t.Fatalf("occupied checkout backoff = revision %d at %q (%v), want future revision 2 after %s",
			revision, retryAt, parseErr, observed)
	}

	for index := 1; index <= 2; index++ {
		observation := domain.CheckoutObservation{
			Path: checkout.Path, Availability: domain.CheckoutAvailable, CheckoutKind: checkout.CheckoutKind,
			Branch:     fmt.Sprintf("metadata-refresh-%d", index),
			HeadCommit: strings.Repeat(fmt.Sprintf("%x", index+2), 40),
			GitDir:     checkout.GitDir, GitCommonDir: checkout.GitCommonDir,
			Repository: domain.RepositoryObservation{
				Fingerprint: repository.Fingerprint, ObjectFormat: repository.ObjectFormat, RootCommits: repository.RootCommits,
			},
		}
		if _, err := storage.ApplyCheckoutObservations(ctx, fixture.workspace.ID, fixture.project.ID,
			fmt.Sprintf("request-checkout-metadata-refresh-%d", index), map[string]domain.CheckoutObservation{checkout.ID: observation}); err != nil {
			t.Fatalf("ApplyCheckoutObservations(occupied refresh %d) = %v", index, err)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE checkout_id=?
AND status IN ('requested','starting','active','blocked','stopping','lost')`, checkout.ID)
		result, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: fmt.Sprintf("checkout-metadata-refresh-%d", index),
			CorrelationID:  fmt.Sprintf("request-checkout-metadata-refresh-scan-%d", index),
		})
		if err != nil || len(result.Actions) != 0 || len(result.ScheduledRunIDs) != 0 {
			t.Fatalf("RunSupervisor(occupied metadata refresh %d) = %#v, %v; want backoff preserved", index, result, err)
		}
		queueBoundaryAssertIntentBackoff(t, storage, intentID, 2, retryAt)
		queueBoundaryAssertExplainBackoff(t, storage, fixture.workspace.ID, intentID, retryAt,
			fmt.Sprintf("occupied metadata refresh %d", index))
	}

	if !observed.Before(retryDeadline) {
		t.Fatalf("checkout wake fixture reached retry deadline %s before terminal release at %s", retryDeadline, observed)
	}
	queueBoundaryCompleteManagerRun(t, storage, managerRunID, "checkout-metadata-manager")
	woken, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "checkout-metadata-terminal", CorrelationID: "request-checkout-metadata-terminal",
	})
	if err != nil || len(woken.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(after occupying run terminalized) = %#v, %v; want exact checkout wake before deadline", woken, err)
	}
	assertManagementRowCount(t, storage, 1,
		`SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, intentID)
}

func TestSupervisorUnrelatedScopedRunFactDoesNotWakeDeferredCapacityPage(t *testing.T) {
	observed := time.Date(2040, 6, 7, 8, 9, 10, 0, time.UTC)
	autoAdvance := true
	clock := func() time.Time {
		if autoAdvance {
			observed = observed.Add(time.Millisecond)
		}
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 100,
		SharedTargetCheckout: true,
		Clock:                clock,
	})
	ctx := context.Background()
	var sharedCheckoutID string
	if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? AND write_mode='shared' ORDER BY id LIMIT 1`, fixture.project.ID).
		Scan(&sharedCheckoutID); err != nil {
		t.Fatalf("read unrelated-scope shared checkout = %v", err)
	}

	// Give project-scoped heads their own project and manager authority. This
	// keeps the eventual run fact in the original project genuinely unrelated,
	// while all four capacity dimensions still share one bounded queue page.
	projectFixture, projectCheckoutID := queueBoundaryCreateSecondaryManagerFixture(t, storage, fixture.workspace, "selective-project")
	queueBoundaryCreateReservedRun(t, storage, projectFixture, projectFixture.target.AgentID,
		projectFixture.target.Runtime, projectFixture.target.Provider, projectCheckoutID, "selective-project-blocker")
	projectManagerRunID, projectCursor := invokeAdversarialManager(t, storage, projectFixture, "selective-project-manager")
	queueBoundaryAcceptProfileIntents(t, storage, projectFixture, projectManagerRunID, projectCursor,
		"selective-project-head", 4, []domain.LaunchProfile{projectFixture.target})
	queueBoundaryCompleteManagerRun(t, storage, projectManagerRunID, "selective-project-manager")

	exclusiveCheckout, err := storage.AddCheckout(ctx, AddCheckoutCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		WriteMode:           domain.WriteModeExclusive,
		IdempotencyKey:      "selective-checkout-add",
		CorrelationID:       "request-selective-checkout-add",
		Observation:         sourceTestObservation(filepath.Join(t.TempDir(), "selective-exclusive"), "selective-exclusive"),
	})
	if err != nil {
		t.Fatalf("AddCheckout(selective exclusive) = %v", err)
	}

	providerAgent := queueBoundaryCreateAgent(t, storage, fixture.workspace.ID,
		"selective-provider-agent", "selective-provider-a", 100, "selective-provider")
	agentAgent := queueBoundaryCreateAgent(t, storage, fixture.workspace.ID,
		"selective-agent-agent", "fake", 1, "selective-agent")
	checkoutAgent := queueBoundaryCreateAgent(t, storage, fixture.workspace.ID,
		"selective-checkout-agent", "fake", 100, "selective-checkout")
	providerProfile := queueBoundaryCreateLaunchProfile(t, storage, fixture, providerAgent, "", "selective-provider")
	agentProfile := queueBoundaryCreateLaunchProfile(t, storage, fixture, agentAgent, "", "selective-agent")
	checkoutProfile := queueBoundaryCreateLaunchProfile(t, storage, fixture, checkoutAgent,
		exclusiveCheckout.Checkout.ID, "selective-checkout")
	// Install the exact scoped limits before constructing the four durable
	// blockers. Admission itself now enforces the current two-starting default,
	// so configuring afterward would make this formerly valid queue fixture
	// unreachable rather than testing selective retry wakeups.
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 100, MaxStartingRuns: 100,
		DefaultProjectConcurrency:  100,
		ProjectConcurrency:         map[string]int{projectFixture.project.ID: 1},
		DefaultProviderConcurrency: 100,
		ProviderConcurrency:        map[string]int{providerAgent.Provider: 1},
	}, "selective-scopes")

	queueBoundaryCreateReservedRun(t, storage, fixture, providerAgent.ID, providerAgent.Runtime,
		providerAgent.Provider, sharedCheckoutID, "selective-provider-blocker")
	queueBoundaryCreateReservedRun(t, storage, fixture, agentAgent.ID, agentAgent.Runtime,
		agentAgent.Provider, sharedCheckoutID, "selective-agent-blocker")
	queueBoundaryCreateReservedRun(t, storage, fixture, checkoutAgent.ID, checkoutAgent.Runtime,
		checkoutAgent.Provider, exclusiveCheckout.Checkout.ID, "selective-checkout-blocker")

	fixture.grant = queueBoundaryCreateManagerGrant(t, storage, fixture, []string{
		providerProfile.ID, agentProfile.ID, checkoutProfile.ID, fixture.target.ID,
	}, "selective-scopes")
	unrelatedRunID, cursor := invokeAdversarialManager(t, storage, fixture, "selective-unrelated")
	queueBoundaryAcceptProfileIntents(t, storage, fixture, unrelatedRunID, cursor,
		"selective-scoped-head", 97, []domain.LaunchProfile{providerProfile, agentProfile, checkoutProfile})
	tailProposal := queueBoundaryAcceptProposal(t, storage, fixture, unrelatedRunID, cursor, "selective-eligible-tail", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "eligible-tail", LaunchProfileID: fixture.target.ID,
			Title: "Eligible tail behind unrelated scoped deferrals", Priority: 1,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	tailIntentID := queueBoundaryProposalIntentID(t, storage, tailProposal.ID)
	autoAdvance = false

	first, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      "selective-scopes-first",
		CorrelationID:       "request-selective-scopes-first",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(selective first page) = %v", err)
	}
	if len(first.Actions) != 100 || len(first.ScheduledRunIDs) != 0 {
		t.Fatalf("RunSupervisor(selective first page) = %#v; want 100 scoped deferrals", first)
	}
	witnesses := make(map[string]string)
	for _, action := range first.Actions {
		if action.Status != domain.SupervisorActionDeferred {
			t.Fatalf("selective first-page action = %#v; want only deferrals", action)
		}
		dimension := queueBoundaryPrimaryFailure(t, action.ConstraintSnapshot)
		switch dimension {
		case "project_unresolved", "provider_unresolved", "agent_active_runs", "checkout":
			witnesses[dimension] = action.IntentID
		default:
			t.Fatalf("selective first-page primary failure = %q in %#v", dimension, action.ConstraintSnapshot)
		}
	}
	for _, dimension := range []string{"project_unresolved", "provider_unresolved", "agent_active_runs", "checkout"} {
		if witnesses[dimension] == "" {
			t.Fatalf("selective first page omitted %s witness: %#v", dimension, first.Actions)
		}
	}
	deadlines := make(map[string]string, len(witnesses))
	for dimension, intentID := range witnesses {
		var revision int64
		var deadline string
		if err := storage.db.QueryRow(`SELECT revision,next_attempt_at FROM scheduling_intents WHERE id=?`, intentID).
			Scan(&revision, &deadline); err != nil {
			t.Fatalf("read %s selective backoff = %v", dimension, err)
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, deadline)
		if revision != 2 || parseErr != nil || !parsed.After(observed) {
			t.Fatalf("%s selective backoff = revision %d at %q (%v), want revision 2 in the future after %s",
				dimension, revision, deadline, parseErr, observed)
		}
		deadlines[dimension] = deadline
	}

	// The only newly classified capacity facts are for the manager run in the
	// tail's project/provider/agent/checkout. None intersects a deferred head's
	// sealed primary failure scope.
	queueBoundaryCompleteManagerRun(t, storage, unrelatedRunID, "selective-unrelated")
	second, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      "selective-scopes-second",
		CorrelationID:       "request-selective-scopes-second",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(selective unrelated fact) = %v", err)
	}
	if len(second.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(selective unrelated fact) = %#v; want eligible tail scheduled past backed-off page", second)
	}
	assertManagementRowCount(t, storage, 1,
		`SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='run_requested'`, tailIntentID)
	assertManagementRowCount(t, storage, 101, `SELECT COUNT(*) FROM scheduling_intents
WHERE status='deferred' AND launch_profile_id IN (?,?,?,?)`, projectFixture.target.ID, providerProfile.ID, agentProfile.ID, checkoutProfile.ID)

	for dimension, intentID := range witnesses {
		queueBoundaryAssertIntentBackoff(t, storage, intentID, 2, deadlines[dimension])
		explanation, err := storage.ExplainSupervisor(ctx, ExplainSupervisorQuery{
			WorkspaceIdentifier: fixture.workspace.ID,
			IntentID:            intentID,
			Limit:               1,
		})
		if err != nil {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) = %v", dimension, err)
		}
		if len(explanation.Candidates) != 1 {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) = %#v; want one witness", dimension, explanation.Candidates)
		}
		candidate := explanation.Candidates[0]
		if candidate.Eligible {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) marked backed-off witness eligible: %#v", dimension, candidate)
		}
		if _, exists := candidate.Constraints["retry_backoff_bypassed_by_relevant_event"]; exists {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) claimed a relevant wake: %#v", dimension, candidate)
		}
		if candidate.Constraints["next_attempt_at"] != deadlines[dimension] {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) next attempt = %#v, want %q",
				dimension, candidate.Constraints["next_attempt_at"], deadlines[dimension])
		}
		if !strings.Contains(strings.Join(candidate.Reasons, "\n"), "deferred retry time has not arrived") {
			t.Fatalf("ExplainSupervisor(%s unrelated scope) reasons = %#v; want stable retry backoff", dimension, candidate.Reasons)
		}
	}
}

func TestSupervisorBlockedQueuePagesPastReceiptedFirstPage(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 100,
		SharedTargetCheckout: true,
	})
	ctx := context.Background()
	var checkoutID string
	if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? AND write_mode='shared' ORDER BY id LIMIT 1`, fixture.project.ID).
		Scan(&checkoutID); err != nil {
		t.Fatalf("read shared checkout = %v", err)
	}
	// Twenty simultaneously unresolved runs are the exact current node ceiling.
	// Two ten-item pages retain the receipt-pagination proof without constructing
	// an impossible pre-admission state.
	const blockedRunCount = NodeExecutionCapacityLimit
	const pageLimit = blockedRunCount / 2
	if _, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: false,
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: blockedRunCount, MaxStartingRuns: 2,
			DefaultProjectConcurrency: blockedRunCount, DefaultProviderConcurrency: blockedRunCount,
		},
		AutoRetryLimit: 0, RetryCooldownSeconds: 0, ExpectedRevision: 1,
		IdempotencyKey: "blocked-page-policy", CorrelationID: "request-blocked-page-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(blocked page) = %v", err)
	}
	runIDs := make([]string, 0, blockedRunCount)
	for index := 0; index < blockedRunCount; index++ {
		task, err := storage.CreateTask(ctx, CreateTaskCommand{
			WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
			ObjectiveID: fixture.objective.ID, Title: fmt.Sprintf("Blocked queue run %03d", index),
			Priority: 100, Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 600},
			IdempotencyKey: fmt.Sprintf("blocked-page-task-%03d", index),
			CorrelationID:  fmt.Sprintf("request-blocked-page-task-%03d", index),
		})
		if err != nil {
			t.Fatalf("CreateTask(blocked queue %d) = %v", index, err)
		}
		agentID := fixture.target.AgentID
		if index == blockedRunCount-1 {
			agentID = fixture.manager.ID
		}
		assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Detail.Task.ID, AgentIdentifier: agentID,
			LeaseSeconds: 3600, ExpectedRevision: task.Detail.Task.Revision,
			IdempotencyKey: fmt.Sprintf("blocked-page-assign-%03d", index),
			CorrelationID:  fmt.Sprintf("request-blocked-page-assign-%03d", index),
		})
		if err != nil {
			t.Fatalf("AssignTask(blocked queue %d) = %v", index, err)
		}
		created, err := storage.CreateRun(ctx, CreateRunCommand{
			WorkspaceIdentifier: fixture.workspace.ID, TaskID: assigned.Detail.Task.ID,
			CheckoutIdentifier: checkoutID, Runtime: "fake", Provider: "fake",
			Scenario: domain.FakeScenario{
				Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: fmt.Sprintf("blocked-page-%03d", index),
				Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "owner input required"}},
			},
			ExpectedTaskRevision: assigned.Detail.Task.Revision,
			IdempotencyKey:       fmt.Sprintf("blocked-page-run-%03d", index),
			CorrelationID:        fmt.Sprintf("request-blocked-page-run-%03d", index),
		})
		if err != nil {
			t.Fatalf("CreateRun(blocked queue %d) = %v", index, err)
		}
		if _, err := storage.MarkRunStarting(ctx, created.Detail.Run.ID, fmt.Sprintf("request-blocked-page-starting-%03d", index)); err != nil {
			t.Fatalf("MarkRunStarting(blocked queue %d) = %v", index, err)
		}
		if _, err := storage.MarkRunStarted(ctx, created.Detail.Run.ID,
			fmt.Sprintf("runtime-blocked-%03d", index), fmt.Sprintf("provider-blocked-%03d", index),
			fmt.Sprintf("request-blocked-page-started-%03d", index)); err != nil {
			t.Fatalf("MarkRunStarted(blocked queue %d) = %v", index, err)
		}
		blocked, err := storage.ApplyRunObservation(ctx, created.Detail.Run.ID, domain.RunObservation{
			Kind: domain.ObservationBlocked, Message: "owner input required",
		}, true, nil, fmt.Sprintf("request-blocked-page-observed-%03d", index))
		if err != nil || blocked.Run.Status != domain.RunBlocked {
			t.Fatalf("ApplyRunObservation(blocked queue %d) = %#v, %v", index, blocked, err)
		}
		runIDs = append(runIDs, blocked.Run.ID)
	}
	first, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: pageLimit,
		IdempotencyKey: "blocked-page-first", CorrelationID: "request-blocked-page-first",
	})
	if err != nil || len(first.Actions) != pageLimit {
		t.Fatalf("RunSupervisor(blocked first page) = %#v, %v; want %d approval actions", first, err, pageLimit)
	}
	second, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: pageLimit,
		IdempotencyKey: "blocked-page-second", CorrelationID: "request-blocked-page-second",
	})
	if err != nil || len(second.Actions) != pageLimit || second.Actions[pageLimit-1].RunID != runIDs[blockedRunCount-1] {
		t.Fatalf("RunSupervisor(blocked second page) = %#v, %v; want remaining %d blocked runs after receipted page", second, err, pageLimit)
	}
	assertManagementRowCount(t, storage, blockedRunCount, `SELECT COUNT(*) FROM supervisor_action_receipts receipt
JOIN supervisor_actions action ON action.id=receipt.action_id WHERE action.condition='blocked'`)
}

func TestSupervisorNonAutomaticQueuePagesPastReceiptedFirstPage(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, _, firstAssigned := initializeRunTest(t, storage, "nonautomatic-page")
	ctx := context.Background()
	const failedRunCount = 101
	failedRunIDs := make([]string, 0, failedRunCount)
	for index := 0; index < failedRunCount; index++ {
		assigned := firstAssigned
		if index != 0 {
			task := createWorkTestTask(t, storage, workspace.ID, project.ID,
				fmt.Sprintf("Nonautomatic failed run %03d", index), fmt.Sprintf("nonautomatic-page-task-%03d", index))
			assignedResult, err := storage.AssignTask(ctx, AssignTaskCommand{
				WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agent.ID,
				LeaseSeconds: 3600, ExpectedRevision: task.Task.Revision,
				IdempotencyKey: fmt.Sprintf("nonautomatic-page-assign-%03d", index),
				CorrelationID:  fmt.Sprintf("request-nonautomatic-page-assign-%03d", index),
			})
			if err != nil {
				t.Fatalf("AssignTask(nonautomatic queue %d) = %v", index, err)
			}
			assigned = assignedResult.Detail
		}
		created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: fmt.Sprintf("nonautomatic-page-%03d", index),
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "will fail before start"}},
		}, fmt.Sprintf("nonautomatic-page-run-%03d", index))
		if _, err := storage.MarkRunStarting(ctx, created.Run.ID, fmt.Sprintf("request-nonautomatic-page-starting-%03d", index)); err != nil {
			t.Fatalf("MarkRunStarting(nonautomatic queue %d) = %v", index, err)
		}
		failed, err := storage.FailRunStart(ctx, created.Run.ID, "definite direct start failure",
			fmt.Sprintf("request-nonautomatic-page-failed-%03d", index))
		if err != nil || failed.Run.Status != domain.RunStartFailed {
			t.Fatalf("FailRunStart(nonautomatic queue %d) = %#v, %v", index, failed, err)
		}
		failedRunIDs = append(failedRunIDs, failed.Run.ID)
	}
	configureAdversarialSupervisor(t, storage, workspace.ID, "nonautomatic-page-policy")

	first, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100,
		IdempotencyKey: "nonautomatic-page-first", CorrelationID: "request-nonautomatic-page-first",
	})
	if err != nil || len(first.Actions) != 100 {
		t.Fatalf("RunSupervisor(nonautomatic first page) = %#v, %v; want 100 owner actions", first, err)
	}
	second, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100,
		IdempotencyKey: "nonautomatic-page-second", CorrelationID: "request-nonautomatic-page-second",
	})
	if err != nil || len(second.Actions) != 1 || second.Actions[0].RunID != failedRunIDs[failedRunCount-1] {
		t.Fatalf("RunSupervisor(nonautomatic second page) = %#v, %v; want final failed run after receipted page", second, err)
	}
	assertManagementRowCount(t, storage, failedRunCount, `SELECT COUNT(*) FROM supervisor_action_receipts receipt
JOIN supervisor_actions action ON action.id=receipt.action_id WHERE action.condition='failed'`)
}

func TestSupervisorRetryPageSkipsIneligibleAuthorityHeads(t *testing.T) {
	observed := time.Date(2039, 5, 6, 7, 8, 9, 0, time.UTC)
	clock := func() time.Time {
		observed = observed.Add(time.Nanosecond)
		return observed
	}
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 100,
		SharedTargetCheckout: true,
		Clock:                clock,
	})
	activeProfile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.target.AgentID, ExpectedAgentRevision: fixture.target.AgentRevision,
		Purpose: "active retry tail profile", Runtime: fixture.target.Runtime, Provider: fixture.target.Provider,
		Scenario: fixture.target.Scenario, AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900,
		IdempotencyKey: "retry-page-active-profile", CorrelationID: "request-retry-page-active-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(retry tail) = %v", err)
	}
	grant, err := storage.CreateManagerGrant(context.Background(), CreateManagerGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID, AgentIdentifier: fixture.manager.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds:     []string{domain.ManagerProposalTaskDecomposition},
		LaunchProfileIDs:  []string{fixture.target.ID, activeProfile.Value.ID},
		AllowedClaimKinds: []string{domain.ClaimKindComponent},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 4, MaxClaimRequirements: 2,
			Budget: domain.Budget{TokenLimit: 1000, CostCents: 1000, TimeSeconds: 1000},
		},
		IdempotencyKey: "retry-page-grant", CorrelationID: "request-retry-page-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(retry page) = %v", err)
	}
	fixture.grant = grant.Value
	runID, cursor := invokeAdversarialManager(t, storage, fixture, "retry-page")

	const ineligibleHeads = 100
	created := 0
	for proposalIndex := 0; created < ineligibleHeads; proposalIndex++ {
		actions := make([]domain.ManagerProposalAction, 0, 4)
		for len(actions) < 4 && created < ineligibleHeads {
			index := created
			actions = append(actions, domain.ManagerProposalAction{
				Type: domain.ProposalActionCreateTask,
				CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: fmt.Sprintf("retry-head-%03d", index), LaunchProfileID: fixture.target.ID,
					Title: fmt.Sprintf("Retry authority head %03d", index), Priority: 100,
					Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
				},
			})
			created++
		}
		queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor,
			fmt.Sprintf("retry-page-head-%02d", proposalIndex), actions)
	}
	tailProposal := queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor, "retry-page-tail", []domain.ManagerProposalAction{{
		Type: domain.ProposalActionCreateTask,
		CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "retry-tail", LaunchProfileID: activeProfile.Value.ID,
			Title: "Eligible retry after ineligible authority page", Priority: 1,
			Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
		},
	}})
	tailIntentID := queueBoundaryProposalIntentID(t, storage, tailProposal.ID)
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: true,
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: 100, MaxStartingRuns: 100, DefaultProjectConcurrency: 100, DefaultProviderConcurrency: 100,
		},
		AutoRetryLimit: 1, RetryCooldownSeconds: 60, ExpectedRevision: 1,
		IdempotencyKey: "retry-page-policy", CorrelationID: "request-retry-page-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(retry page) = %v", err)
	}

	for index := 0; index < ineligibleHeads+1; index++ {
		scheduled, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 1,
			IdempotencyKey: fmt.Sprintf("retry-page-schedule-%03d", index),
			CorrelationID:  fmt.Sprintf("request-retry-page-schedule-%03d", index),
		})
		if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(schedule retry fixture %d) = %#v, %v", index, scheduled, err)
		}
		if _, err := storage.FailRunStart(context.Background(), scheduled.ScheduledRunIDs[0],
			"definite retry page fixture failure", fmt.Sprintf("request-retry-page-failed-%03d", index)); err != nil {
			t.Fatalf("FailRunStart(retry fixture %d) = %v", index, err)
		}
	}
	var tailPriorRunID string
	if err := storage.db.QueryRow(`SELECT run_id FROM scheduling_intents WHERE id=?`, tailIntentID).Scan(&tailPriorRunID); err != nil {
		t.Fatalf("read retry tail prior run = %v", err)
	}
	if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, LaunchProfileID: fixture.target.ID,
		ExpectedRevision: fixture.target.Revision, Reason: "invalidate exactly the old retry authority page",
		IdempotencyKey: "retry-page-retire-head", CorrelationID: "request-retry-page-retire-head",
	}); err != nil {
		t.Fatalf("RetireLaunchProfile(retry heads) = %v", err)
	}
	observed = observed.Add(60 * time.Second)

	retried, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 1,
		IdempotencyKey: "retry-page-boundary", CorrelationID: "request-retry-page-boundary",
	})
	if err != nil || len(retried.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(retry authority boundary) = %#v, %v; want eligible tail retry past 100 retired-profile heads", retried, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE prior_run_id=? AND run_id=?`,
		tailPriorRunID, retried.ScheduledRunIDs[0])
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM run_retry_receipts receipt
JOIN runs prior ON prior.id=receipt.prior_run_id
JOIN run_scheduling_receipts source ON source.run_id=prior.id WHERE source.launch_profile_id=?`, fixture.target.ID)
}

func queueBoundaryAcceptProposal(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, runID string, cursor int64, key string, actions []domain.ManagerProposalAction) domain.ManagerProposal {
	t.Helper()
	submitted, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Queue boundary fixture " + key,
		AsOfEventSequence: cursor, Actions: actions,
		IdempotencyKey: key + "-submit", CorrelationID: "request-" + key + "-submit",
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(%s) = %#v, %v; want pending", key, submitted.Proposal, err)
	}
	accepted, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID,
		ExpectedRevision: submitted.Proposal.Revision, DecisionNote: "Accept exact queue boundary fixture.",
		IdempotencyKey: key + "-accept", CorrelationID: "request-" + key + "-accept",
	})
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(%s) = %#v, %v; want accepted", key, accepted.Proposal, err)
	}
	return accepted.Proposal
}

func queueBoundaryCompleteTask(t *testing.T, storage *Store, workspaceID, taskID, correlationID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin completing queue prerequisite = %v", err)
	}
	defer tx.Rollback()
	task, err := queryTask(ctx, tx, workspaceID, taskID)
	if err != nil {
		t.Fatalf("query queue prerequisite = %v", err)
	}
	now := storage.nowText()
	task.Revision++
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='completed',revision=?,updated_at=?,updated_by=? WHERE id=?`, task.Revision, now, localOwnerActorID, task.ID); err != nil {
		t.Fatalf("complete queue prerequisite = %v", err)
	}
	if _, err := appendEvent(ctx, tx, task.WorkspaceID, "task", task.ID, task.Revision, taskCompletedEvent, correlationID, now,
		map[string]any{"status": domain.TaskCompleted}); err != nil {
		t.Fatalf("append queue prerequisite completion = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit queue prerequisite completion = %v", err)
	}
}

func queueBoundaryProposalIntentID(t *testing.T, storage *Store, proposalID string) string {
	t.Helper()
	var intentID string
	if err := storage.db.QueryRow(`SELECT id FROM scheduling_intents WHERE source_proposal_id=? ORDER BY id LIMIT 1`, proposalID).Scan(&intentID); err != nil {
		t.Fatalf("read proposal %s scheduling intent = %v", proposalID, err)
	}
	return intentID
}

func queueBoundaryAssertIntentBackoff(t *testing.T, storage *Store, intentID string, revision int64, nextAttemptAt string) {
	t.Helper()
	var observedRevision int64
	var observedNextAttemptAt string
	if err := storage.db.QueryRow(`SELECT revision,next_attempt_at FROM scheduling_intents WHERE id=?`, intentID).
		Scan(&observedRevision, &observedNextAttemptAt); err != nil {
		t.Fatalf("read intent %s backoff = %v", intentID, err)
	}
	if observedRevision != revision || observedNextAttemptAt != nextAttemptAt {
		t.Fatalf("intent %s backoff = revision %d at %q, want revision %d at %q",
			intentID, observedRevision, observedNextAttemptAt, revision, nextAttemptAt)
	}
}

func queueBoundaryCreateSecondaryManagerFixture(t *testing.T, storage *Store, workspace domain.Workspace, key string) (managerGrantAdversarialFixture, string) {
	t.Helper()
	ctx := context.Background()
	registered, err := storage.RegisterProject(ctx, RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID,
		Name:                key,
		WriteMode:           domain.WriteModeShared,
		IdempotencyKey:      key + "-project",
		CorrelationID:       "request-" + key + "-project",
		Observation:         sourceTestObservation(filepath.Join(t.TempDir(), key), key),
	})
	if err != nil {
		t.Fatalf("RegisterProject(%s) = %v", key, err)
	}
	manager := queueBoundaryCreateAgent(t, storage, workspace.ID, key+"-manager", "fake", 1, key+"-manager")
	target := queueBoundaryCreateAgent(t, storage, workspace.ID, key+"-target", key+"-provider", 100, key+"-target")
	objective, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   registered.Project.ID,
		Title:               "Selective wake authority " + key,
		Budget:              domain.Budget{TokenLimit: 10000, CostCents: 1000, TimeSeconds: 3600},
		IdempotencyKey:      key + "-objective",
		CorrelationID:       "request-" + key + "-objective",
	})
	if err != nil {
		t.Fatalf("CreateObjective(%s) = %v", key, err)
	}
	planning, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   registered.Project.ID,
		ObjectiveID:         objective.Value.ID,
		Title:               "Plan selective wake fixture " + key,
		Budget:              domain.Budget{TokenLimit: 500, CostCents: 50, TimeSeconds: 300},
		IdempotencyKey:      key + "-planning",
		CorrelationID:       "request-" + key + "-planning",
	})
	if err != nil {
		t.Fatalf("CreateTask(%s planning) = %v", key, err)
	}
	assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID,
		TaskID:              planning.Detail.Task.ID,
		AgentIdentifier:     manager.ID,
		LeaseSeconds:        900,
		ExpectedRevision:    planning.Detail.Task.Revision,
		IdempotencyKey:      key + "-assignment",
		CorrelationID:       "request-" + key + "-assignment",
	})
	if err != nil {
		t.Fatalf("AssignTask(%s planning) = %v", key, err)
	}
	profile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier:   workspace.ID,
		ProjectIdentifier:     registered.Project.ID,
		AgentIdentifier:       target.ID,
		ExpectedAgentRevision: target.Revision,
		Purpose:               "selective project capacity head",
		Runtime:               target.Runtime,
		Provider:              target.Provider,
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: key + "-target",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "bounded selective project work"}},
		},
		AssignmentLeaseSeconds: 900,
		CapabilityTTLSeconds:   900,
		IdempotencyKey:         key + "-profile",
		CorrelationID:          "request-" + key + "-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(%s target) = %v", key, err)
	}
	fixture := managerGrantAdversarialFixture{
		workspace: workspace,
		project:   registered.Project,
		objective: objective.Value,
		manager:   manager,
		target:    profile.Value,
		planning:  assigned.Detail,
	}
	fixture.grant = queueBoundaryCreateManagerGrant(t, storage, fixture, []string{fixture.target.ID}, key)
	return fixture, registered.Checkout.ID
}

func queueBoundaryCreateAgent(t *testing.T, storage *Store, workspaceID, name, provider string, maxConcurrency int, key string) domain.AgentDefinition {
	t.Helper()
	created, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspaceID,
		Name:                name,
		Role:                "selective capacity fixture",
		Provider:            provider,
		Runtime:             "fake",
		MaxConcurrency:      maxConcurrency,
		IdempotencyKey:      key + "-agent",
		CorrelationID:       "request-" + key + "-agent",
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s) = %v", key, err)
	}
	return created.Value
}

func queueBoundaryCreateLaunchProfile(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, agent domain.AgentDefinition, checkoutID, key string) domain.LaunchProfile {
	t.Helper()
	created, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier:   fixture.workspace.ID,
		ProjectIdentifier:     fixture.project.ID,
		AgentIdentifier:       agent.ID,
		ExpectedAgentRevision: agent.Revision,
		Purpose:               "selective " + key + " capacity head",
		Runtime:               agent.Runtime,
		Provider:              agent.Provider,
		CheckoutIdentifier:    checkoutID,
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: key + "-profile",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "bounded selective work"}},
		},
		AssignmentLeaseSeconds: 900,
		CapabilityTTLSeconds:   900,
		IdempotencyKey:         key + "-profile",
		CorrelationID:          "request-" + key + "-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(%s) = %v", key, err)
	}
	return created.Value
}

func queueBoundaryCreateManagerGrant(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, profileIDs []string, key string) domain.ManagerGrant {
	t.Helper()
	created, err := storage.CreateManagerGrant(context.Background(), CreateManagerGrantCommand{
		WorkspaceIdentifier:   fixture.workspace.ID,
		ProjectIdentifier:     fixture.project.ID,
		ObjectiveID:           fixture.objective.ID,
		TaskID:                fixture.planning.Task.ID,
		AgentIdentifier:       fixture.manager.ID,
		ExpectedTaskRevision:  fixture.planning.Task.Revision,
		ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds:         []string{domain.ManagerProposalTaskDecomposition},
		LaunchProfileIDs:      profileIDs,
		AllowedClaimKinds:     []string{domain.ClaimKindComponent},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 4, MaxClaimRequirements: 2,
			Budget: domain.Budget{TokenLimit: 1000, CostCents: 1000, TimeSeconds: 1000},
		},
		IdempotencyKey: key + "-grant",
		CorrelationID:  "request-" + key + "-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(%s) = %v", key, err)
	}
	return created.Value
}

func queueBoundaryCreateReservedRun(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, agentID, runtime, provider, checkoutID, key string) string {
	t.Helper()
	ctx := context.Background()
	task, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		ObjectiveID:         fixture.objective.ID,
		Title:               "Reserved capacity fixture " + key,
		Priority:            100,
		Budget:              domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 600},
		IdempotencyKey:      key + "-task",
		CorrelationID:       "request-" + key + "-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(%s blocker) = %v", key, err)
	}
	assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              task.Detail.Task.ID,
		AgentIdentifier:     agentID,
		LeaseSeconds:        3600,
		ExpectedRevision:    task.Detail.Task.Revision,
		IdempotencyKey:      key + "-assign",
		CorrelationID:       "request-" + key + "-assign",
	})
	if err != nil {
		t.Fatalf("AssignTask(%s blocker) = %v", key, err)
	}
	created, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              assigned.Detail.Task.ID,
		CheckoutIdentifier:  checkoutID,
		Runtime:             runtime,
		Provider:            provider,
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: key,
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "hold exact capacity"}},
		},
		ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey:       key + "-run",
		CorrelationID:        "request-" + key + "-run",
	})
	if err != nil {
		t.Fatalf("CreateRun(%s blocker) = %v", key, err)
	}
	return created.Detail.Run.ID
}

func queueBoundaryAcceptProfileIntents(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, runID string, cursor int64, key string, count int, profiles []domain.LaunchProfile) {
	t.Helper()
	created := 0
	for proposalIndex := 0; created < count; proposalIndex++ {
		actions := make([]domain.ManagerProposalAction, 0, 4)
		for len(actions) < 4 && created < count {
			index := created
			profile := profiles[index%len(profiles)]
			actions = append(actions, domain.ManagerProposalAction{
				Type: domain.ProposalActionCreateTask,
				CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: fmt.Sprintf("head-%03d", index), LaunchProfileID: profile.ID,
					Title: fmt.Sprintf("Selective scoped queue head %03d", index), Priority: 100,
					Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
				},
			})
			created++
		}
		queueBoundaryAcceptProposal(t, storage, fixture, runID, cursor,
			fmt.Sprintf("%s-%02d", key, proposalIndex), actions)
	}
}

func queueBoundaryCompleteManagerRun(t *testing.T, storage *Store, runID, key string) {
	t.Helper()
	if _, err := storage.MarkRunStarted(context.Background(), runID, key+"-runtime-handle", key+"-provider-handle", "request-"+key+"-started"); err != nil {
		t.Fatalf("MarkRunStarted(%s) = %v", key, err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "completed unrelated selective-scope management",
		Evidence: []string{"scoped fixture accepted"}, Handoff: "continue bounded scheduling", LogArchive: prepareTestRunLogArchive(t, storage, runID),
	}, true, nil, "request-"+key+"-completed"); err != nil {
		t.Fatalf("ApplyRunObservation(%s completion) = %v", key, err)
	}
}

func queueBoundaryPrimaryFailure(t *testing.T, snapshot map[string]any) string {
	t.Helper()
	switch dimensions := snapshot["failing_dimensions"].(type) {
	case []string:
		if len(dimensions) > 0 {
			return dimensions[0]
		}
	case []any:
		if len(dimensions) > 0 {
			if value, ok := dimensions[0].(string); ok {
				return value
			}
		}
	}
	t.Fatalf("constraint snapshot has no primary failing dimension: %#v", snapshot)
	return ""
}

func queueBoundaryAssertExplainBackoff(t *testing.T, storage *Store, workspaceID, intentID, retryAt, label string) {
	t.Helper()
	explanation, err := storage.ExplainSupervisor(context.Background(), ExplainSupervisorQuery{
		WorkspaceIdentifier: workspaceID,
		IntentID:            intentID,
		Limit:               1,
	})
	if err != nil {
		t.Fatalf("ExplainSupervisor(%s) = %v", label, err)
	}
	if len(explanation.Candidates) != 1 {
		t.Fatalf("ExplainSupervisor(%s) = %#v; want one candidate", label, explanation.Candidates)
	}
	candidate := explanation.Candidates[0]
	if candidate.Eligible {
		t.Fatalf("ExplainSupervisor(%s) marked backed-off checkout eligible: %#v", label, candidate)
	}
	if _, exists := candidate.Constraints["retry_backoff_bypassed_by_relevant_event"]; exists {
		t.Fatalf("ExplainSupervisor(%s) claimed irrelevant checkout metadata bypassed backoff: %#v", label, candidate)
	}
	if candidate.Constraints["next_attempt_at"] != retryAt {
		t.Fatalf("ExplainSupervisor(%s) next attempt = %#v, want %q", label, candidate.Constraints["next_attempt_at"], retryAt)
	}
	if !strings.Contains(strings.Join(candidate.Reasons, "\n"), "deferred retry time has not arrived") {
		t.Fatalf("ExplainSupervisor(%s) reasons = %#v; want stable retry backoff", label, candidate.Reasons)
	}
}
