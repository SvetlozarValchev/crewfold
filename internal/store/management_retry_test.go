package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestSupervisorRetriesDefiniteStartFailureWithExactProfileAndBound(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()
	observed := time.Now().UTC().Add(time.Second)
	storage.clock = func() time.Time { return observed }
	tick := func() { observed = observed.Add(time.Second) }

	tick()
	planningProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Runtime: "fake", Provider: "fake", Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema, Name: "bounded-retry-planning",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "propose retry work"}},
		},
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: "retry-planning-profile", CorrelationID: "request-retry-planning-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(planning) = %v", err)
	}
	tick()
	invoked, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.planning.Task.ID, ManagerGrantID: fixture.grant.ID, LaunchProfileID: planningProfile.Value.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
		ExpectedProfileRevision: planningProfile.Value.Revision,
		IdempotencyKey:          "retry-manager-invoke", CorrelationID: "request-retry-manager-invoke",
	})
	if err != nil {
		t.Fatalf("InvokeManager() = %v", err)
	}
	tick()
	if _, err := storage.MarkRunStarting(ctx, invoked.Detail.Run.ID, "request-retry-manager-starting"); err != nil {
		t.Fatalf("MarkRunStarting(manager) = %v", err)
	}
	var packetCursor int64
	if err := storage.db.QueryRow(`SELECT json_extract(packet.packet_json,'$.as_of_event_sequence') FROM run_context_bindings binding JOIN context_packets packet ON packet.id=binding.context_packet_id WHERE binding.run_id=?`, invoked.Detail.Run.ID).Scan(&packetCursor); err != nil {
		t.Fatalf("read manager packet cursor = %v", err)
	}
	tick()
	submitted, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: invoked.Detail.Run.ID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Create one exact retry target", AsOfEventSequence: packetCursor,
		Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "retry-target", LaunchProfileID: fixture.target.ID, Title: "Bounded retry target", Priority: 10,
			Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
		}}},
		IdempotencyKey: "retry-proposal-submit", CorrelationID: "request-retry-proposal-submit",
	})
	if err != nil {
		t.Fatalf("SubmitManagerProposal() = %v", err)
	}
	tick()
	if _, err := storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: 1,
		DecisionNote: "Accept one exact retry target.", IdempotencyKey: "retry-proposal-accept", CorrelationID: "request-retry-proposal-accept",
	}); err != nil {
		t.Fatalf("AcceptManagerProposal() = %v", err)
	}
	tick()
	if _, err := storage.MarkRunStarted(ctx, invoked.Detail.Run.ID, "manager-runtime", "manager-provider", "request-retry-manager-started"); err != nil {
		t.Fatalf("MarkRunStarted(manager) = %v", err)
	}
	tick()
	if _, err := storage.ApplyRunObservation(ctx, invoked.Detail.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "proposal submitted", Handoff: "owner accepted exact retry target",
	}, true, nil, "request-retry-manager-complete"); err != nil {
		t.Fatalf("ApplyRunObservation(manager completion) = %v", err)
	}
	tick()
	if _, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: true,
		Limits:         domain.SupervisorLimits{MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4},
		AutoRetryLimit: 1, RetryCooldownSeconds: 30, ExpectedRevision: 1,
		IdempotencyKey: "bounded-retry-policy", CorrelationID: "request-bounded-retry-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy() = %v", err)
	}
	tick()
	scheduled, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-retry-schedule", CorrelationID: "request-bounded-retry-schedule",
	})
	if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(schedule) = %#v, %v", scheduled, err)
	}
	runID := scheduled.ScheduledRunIDs[0]
	var assignmentID string
	if err := storage.db.QueryRow(`SELECT assignment_id FROM runs WHERE id=?`, runID).Scan(&assignmentID); err != nil {
		t.Fatalf("read scheduled assignment = %v", err)
	}
	tick()
	if _, err := storage.MarkRunStarting(ctx, runID, "request-bounded-retry-starting-one"); err != nil {
		t.Fatalf("MarkRunStarting(first) = %v", err)
	}
	tick()
	failed, err := storage.FailRunStart(ctx, runID, "definite fixture start failure", "request-bounded-retry-failed-one")
	if err != nil || failed.Run.Status != domain.RunStartFailed {
		t.Fatalf("FailRunStart(first) = %#v, %v", failed, err)
	}
	tick()
	idle, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-retry-cooldown", CorrelationID: "request-bounded-retry-cooldown",
	})
	if err != nil || len(idle.Actions) != 0 {
		t.Fatalf("RunSupervisor(cooldown) = %#v, %v; want no premature retry or approval", idle, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE run_id=? AND status='run_requested'`, runID)

	observed = observed.Add(31 * time.Second)
	retried, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-retry-apply", CorrelationID: "request-bounded-retry-apply",
	})
	if err != nil || len(retried.Actions) != 1 || retried.Actions[0].Response != domain.SupervisorResponseRetryTask ||
		retried.Actions[0].Status != domain.SupervisorActionApplied || retried.Actions[0].PriorRunID != runID ||
		len(retried.ScheduledRunIDs) != 1 || retried.Actions[0].RunID != retried.ScheduledRunIDs[0] {
		t.Fatalf("RunSupervisor(retry) = %#v, %v", retried, err)
	}
	retryRunID := retried.ScheduledRunIDs[0]
	prior, err := storage.RunDetail(ctx, fixture.workspace.ID, runID)
	if err != nil || prior.Run.Status != domain.RunStartFailed {
		t.Fatalf("prior run = %#v, %v; want immutable start_failed", prior.Run, err)
	}
	detail, err := storage.RunDetail(ctx, fixture.workspace.ID, retryRunID)
	if err != nil || detail.Run.Status != domain.RunRequested || detail.Run.AssignmentID != assignmentID || detail.Run.FailureCode != "" {
		t.Fatalf("fresh retry run = %#v, %v; want requested exact assignment", detail.Run, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts WHERE run_id=? AND assignment_id=?`, runID, assignmentID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE run_id=? AND prior_run_id=? AND assignment_id=?`, retryRunID, runID, assignmentID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=? AND prior_run_id=? AND response='retry_task' AND status='applied'`, retryRunID, runID)

	tick()
	if _, err := storage.MarkRunStarting(ctx, retryRunID, "request-bounded-retry-starting-two"); err != nil {
		t.Fatalf("MarkRunStarting(second) = %v", err)
	}
	tick()
	if _, err := storage.FailRunStart(ctx, retryRunID, "second definite fixture start failure", "request-bounded-retry-failed-two"); err != nil {
		t.Fatalf("FailRunStart(second) = %v", err)
	}
	observed = observed.Add(31 * time.Second)
	exhausted, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-retry-exhausted", CorrelationID: "request-bounded-retry-exhausted",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(exhausted) = %v", err)
	}
	conditions := map[string]bool{}
	for _, action := range exhausted.Actions {
		if action.Response == domain.SupervisorResponseRetryTask {
			t.Fatalf("retry limit widened: %#v", action)
		}
		if action.Status == domain.SupervisorActionAwaitingApproval {
			conditions[action.Condition] = true
		}
	}
	if conditions[domain.SupervisorConditionFailed] || !conditions[domain.SupervisorConditionRepeatedFailure] {
		t.Fatalf("exhausted retry conditions = %#v; want one repeated-failure approval", exhausted.Actions)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE intent_id IN (SELECT intent_id FROM run_scheduling_receipts WHERE run_id=?)`, runID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE run_id=? AND status='failed' AND revision>2`, runID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events event JOIN scheduling_intents intent ON intent.id=event.entity_id
WHERE intent.run_id=? AND event.type='supervisor.intent_failed' AND event.entity_revision=intent.revision`, runID)
}
