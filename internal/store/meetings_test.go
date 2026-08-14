package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestOwnerMeetingProposalRequiresAcceptanceBeforeSequenceMutatesWork(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")

	fixture := sequenceMeetingFixture(setup)
	proposed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: fixture,
		IdempotencyKey: "run-owner-meeting", CorrelationID: "request-run-owner-meeting",
	})
	if err != nil || proposed.Detail.Meeting.Status != domain.MeetingAwaitingApproval || proposed.Detail.Proposal == nil || proposed.Detail.Proposal.Status != domain.MeetingProposalProposed {
		t.Fatalf("RunMeeting() = %#v, %v", proposed, err)
	}
	beforeAcceptance, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.secondTask.Task.ID)
	if err != nil || len(beforeAcceptance.Dependencies) != 0 || beforeAcceptance.Task.Revision != setup.secondTask.Task.Revision {
		t.Fatalf("task before meeting acceptance = %#v, %v", beforeAcceptance, err)
	}
	claimPage, err := storage.ListClaims(context.Background(), ListClaimsQuery{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, Status: domain.ClaimActive})
	claims := claimPage.Claims
	if err != nil || len(claims) != 2 {
		t.Fatalf("active claims before acceptance = %#v, %v", claims, err)
	}

	accepted, err := storage.AcceptMeeting(context.Background(), AcceptMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: proposed.Detail.Meeting.Revision, DecisionNote: "owner approved dependency",
		IdempotencyKey: "accept-owner-meeting", CorrelationID: "request-accept-owner-meeting",
	})
	if err != nil || accepted.Detail.Meeting.Status != domain.MeetingConcluded || accepted.Detail.Proposal == nil || accepted.Detail.Proposal.Status != domain.MeetingProposalAccepted || len(accepted.Detail.Actions) != 1 || accepted.Detail.Actions[0].Status != domain.MeetingActionApplied {
		t.Fatalf("AcceptMeeting() = %#v, %v", accepted, err)
	}
	afterAcceptance, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.secondTask.Task.ID)
	if err != nil || len(afterAcceptance.Dependencies) != 1 || afterAcceptance.Dependencies[0].DependsOnTaskID != setup.firstTask.Task.ID || afterAcceptance.Task.Revision != setup.secondTask.Task.Revision+1 {
		t.Fatalf("task after meeting acceptance = %#v, %v", afterAcceptance, err)
	}
	activeClaimPage, err := storage.ListClaims(context.Background(), ListClaimsQuery{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, Status: domain.ClaimActive})
	activeClaims := activeClaimPage.Claims
	if err != nil || len(activeClaims) != 1 || activeClaims[0].TaskID != setup.firstTask.Task.ID {
		t.Fatalf("active claims after acceptance = %#v, %v", activeClaims, err)
	}
	overlap, err := storage.Overlap(context.Background(), setup.workspace.ID, setup.overlap.ID)
	if err != nil || overlap.Status != domain.OverlapResolved {
		t.Fatalf("overlap after acceptance = %#v, %v", overlap, err)
	}
	timeline, err := storage.EventTimeline(context.Background(), EventTimelineQuery{
		WorkspaceIdentifier: setup.workspace.ID,
		EntityType:          "task",
		EntityID:            setup.secondTask.Task.ID,
		Limit:               MaximumReadPageLimit,
	})
	if err != nil {
		t.Fatalf("EventTimeline(meeting-mutated task) error = %v", err)
	}
	foundAction := false
	for _, event := range timeline.Events {
		if event.Type == taskDependencyAdded {
			foundAction = true
			if event.Actor.ActorID != meetingActionActorID || event.Actor.ActorType != domain.EventActorSubsystem {
				t.Fatalf("meeting action actor = %#v, want %q/%q", event.Actor, meetingActionActorID, domain.EventActorSubsystem)
			}
		}
	}
	if !foundAction {
		t.Fatal("canonical task timeline omitted the meeting action event")
	}
}

func TestMeetingRestartReusesDurablePositionsWithoutCollectingTwice(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	storage, err := Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	pausedFixture := sequenceMeetingFixture(setup)
	pausedFixture.PauseAfterPositions = true
	pausedFixture.Proposal = nil
	paused, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: pausedFixture,
		IdempotencyKey: "pause-after-positions", CorrelationID: "request-pause-after-positions",
	})
	if err != nil || paused.Detail.Meeting.Status != domain.MeetingFacilitatorPending || len(paused.Detail.Contributions) != 2 {
		t.Fatalf("RunMeeting(pause) = %#v, %v", paused, err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	storage, err = Open(context.Background(), dataDir, Options{})
	if err != nil {
		t.Fatalf("Open(after restart) error = %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	resumed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: paused.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "resume-after-positions", CorrelationID: "request-resume-after-positions",
	})
	if err != nil || resumed.Detail.Meeting.Status != domain.MeetingAwaitingApproval || len(resumed.Detail.Contributions) != 2 || resumed.Detail.Proposal == nil {
		t.Fatalf("RunMeeting(after restart) = %#v, %v", resumed, err)
	}
}

func TestMeetingMissingParticipantStallsAndPreservesSubmittedPosition(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	partial := domain.MeetingRunFixture{Positions: []domain.MeetingPositionInput{{AgentID: setup.participants[0].ID, Summary: "complete the contract first", Evidence: []string{"owns contract"}}}}
	stalled, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: partial,
		IdempotencyKey: "stalled-meeting", CorrelationID: "request-stalled-meeting",
	})
	if err != nil || stalled.Detail.Meeting.Status != domain.MeetingStalled || len(stalled.Detail.Contributions) != 1 || stalled.Detail.Participants[1].Status != domain.MeetingParticipantMissing {
		t.Fatalf("RunMeeting(missing) = %#v, %v", stalled, err)
	}
	completedPositions := sequenceMeetingFixture(setup)
	completedPositions.Proposal = nil
	resumed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: stalled.Detail.Meeting.Revision, Fixture: completedPositions,
		IdempotencyKey: "resume-stalled-meeting", CorrelationID: "request-resume-stalled-meeting",
	})
	if err != nil || resumed.Detail.Meeting.Status != domain.MeetingFacilitatorPending || len(resumed.Detail.Contributions) != 2 {
		t.Fatalf("RunMeeting(recovered) = %#v, %v", resumed, err)
	}
}

func TestBoundedManagerAutoAppliesOnlyAllowedActions(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyManagerBounded, []string{domain.MeetingActionSequence}, "")
	result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "bounded-manager-run", CorrelationID: "request-bounded-manager-run",
	})
	if err != nil || result.Detail.Meeting.Status != domain.MeetingConcluded || result.Detail.Proposal == nil || result.Detail.Proposal.Status != domain.MeetingProposalAccepted {
		t.Fatalf("RunMeeting(bounded manager) = %#v, %v", result, err)
	}
}

func TestBoundedManagerDefersActionOutsideAuthorityWithoutMutation(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyManagerBounded, []string{domain.MeetingActionSequence}, "")
	fixture := sequenceMeetingFixture(setup)
	fixture.Proposal = &domain.MeetingProposalInput{Summary: "cancel conflicting downstream work", Actions: []domain.MeetingActionInput{{Type: domain.MeetingActionCancel, Payload: map[string]any{"task_id": setup.secondTask.Task.ID}}}}
	result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: fixture,
		IdempotencyKey: "bounded-manager-deferred", CorrelationID: "request-bounded-manager-deferred",
	})
	if err != nil || result.Detail.Meeting.Status != domain.MeetingAwaitingApproval || result.Detail.Actions[0].Status != domain.MeetingActionPending {
		t.Fatalf("RunMeeting(outside authority) = %#v, %v", result, err)
	}
	task, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.secondTask.Task.ID)
	if err != nil || task.Task.Status != domain.TaskReady || task.Task.Revision != setup.secondTask.Task.Revision {
		t.Fatalf("task after deferred manager action = %#v, %v", task, err)
	}
}

func TestMeetingDeadlineStallsWithFrozenInputIntact(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	setup := initializeMeetingTest(t, storage, 2)
	participants := []string{setup.participants[0].ID, setup.participants[1].ID}
	created, err := storage.CreateMeeting(context.Background(), CreateMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, OverlapID: setup.overlap.ID, ParticipantAgents: participants,
		FacilitatorAgent: setup.facilitator.ID, Policy: domain.MeetingPolicyOwnerDecision, Timeout: time.Second,
		IdempotencyKey: "deadline-meeting", CorrelationID: "request-deadline-meeting",
	})
	if err != nil {
		t.Fatalf("CreateMeeting() error = %v", err)
	}
	frozenHash := created.Detail.Meeting.FrozenInputHash
	now = now.Add(2 * time.Second)
	stalled, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: domain.MeetingRunFixture{},
		IdempotencyKey: "deadline-run", CorrelationID: "request-deadline-run",
	})
	if err != nil || stalled.Detail.Meeting.Status != domain.MeetingStalled || stalled.Detail.Meeting.FrozenInputHash != frozenHash || stalled.Detail.Meeting.StalledReason == "" {
		t.Fatalf("RunMeeting(after deadline) = %#v, %v", stalled, err)
	}
}

func TestMeetingRunIdempotencyIgnoresTransportCorrelation(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	command := RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "replayed-meeting-run", CorrelationID: "first-request",
	}
	first, err := storage.RunMeeting(context.Background(), command)
	if err != nil {
		t.Fatalf("RunMeeting(first) error = %v", err)
	}
	command.CorrelationID = "retried-request"
	replayed, err := storage.RunMeeting(context.Background(), command)
	if err != nil || replayed.Detail.Meeting.Revision != first.Detail.Meeting.Revision || len(replayed.Detail.Contributions) != len(first.Detail.Contributions) {
		t.Fatalf("RunMeeting(replay) = %#v, %v", replayed, err)
	}
}

func TestNamedReviewerMeetingDesignatesImplementerAndReviewer(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 3)
	reviewer := createMeetingAgent(t, storage, setup.workspace.ID, "decision-reviewer")
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyNamedReviewer, nil, reviewer.ID)
	approved := true
	fixture := domain.MeetingRunFixture{
		Positions: []domain.MeetingPositionInput{
			{AgentID: setup.participants[0].ID, Summary: "implement the shared contract"},
			{AgentID: setup.participants[1].ID, Summary: "yield implementation ownership"},
			{AgentID: setup.participants[2].ID, Summary: "review compatibility"},
		},
		Proposal: &domain.MeetingProposalInput{Summary: "one implementer and an independent reviewer", Actions: []domain.MeetingActionInput{
			{Type: domain.MeetingActionDesignateRole, Payload: map[string]any{"task_id": setup.firstTask.Task.ID, "agent_id": setup.participants[0].ID, "role": "implementer"}},
			{Type: domain.MeetingActionDesignateRole, Payload: map[string]any{"task_id": setup.firstTask.Task.ID, "agent_id": setup.participants[2].ID, "role": "reviewer"}},
		}},
		ReviewerApproved: &approved,
		ReviewerNote:     "roles preserve independent review",
	}
	result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: fixture,
		IdempotencyKey: "reviewed-role-meeting", CorrelationID: "request-reviewed-role-meeting",
	})
	if err != nil || result.Detail.Meeting.Status != domain.MeetingConcluded || len(result.Detail.Actions) != 2 || len(result.Detail.Contributions) != 4 {
		t.Fatalf("RunMeeting(named reviewer) = %#v, %v", result, err)
	}
	var roles int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM task_roles WHERE task_id = ? AND source_meeting_id = ?", setup.firstTask.Task.ID, created.Detail.Meeting.ID).Scan(&roles); err != nil || roles != 2 {
		t.Fatalf("designated roles = %d, %v", roles, err)
	}
}

func TestMeetingSplitReassignAndCancelActionsMutateDurableWork(t *testing.T) {
	t.Parallel()
	t.Run("split", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		setup := initializeMeetingTest(t, storage, 2)
		created := createMeetingTest(t, storage, setup, domain.MeetingPolicyManagerBounded, []string{domain.MeetingActionSplit}, "")
		fixture := sequenceMeetingFixture(setup)
		fixture.Proposal = &domain.MeetingProposalInput{Summary: "split compatibility work", Actions: []domain.MeetingActionInput{{Type: domain.MeetingActionSplit, Payload: map[string]any{"source_task_id": setup.firstTask.Task.ID, "title": "Verify compatibility", "description": "Run the compatibility review independently."}}}}
		result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID, ExpectedRevision: 1, Fixture: fixture, IdempotencyKey: "apply-split", CorrelationID: "request-apply-split"})
		if err != nil || result.Detail.Meeting.Status != domain.MeetingConcluded || len(result.Detail.Actions) != 1 || result.Detail.Actions[0].ResultEntityID == "" {
			t.Fatalf("RunMeeting(split) = %#v, %v", result, err)
		}
		taskPage, err := storage.ListTasks(context.Background(), ListTasksQuery{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID})
		tasks := taskPage.Tasks
		if err != nil || len(tasks) != 3 {
			t.Fatalf("tasks after split = %#v, %v", tasks, err)
		}
	})

	t.Run("reassign", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		setup := initializeMeetingTest(t, storage, 2)
		created := createMeetingTest(t, storage, setup, domain.MeetingPolicyManagerBounded, []string{domain.MeetingActionReassign}, "")
		fixture := sequenceMeetingFixture(setup)
		fixture.Proposal = &domain.MeetingProposalInput{Summary: "assign one owner", Actions: []domain.MeetingActionInput{{Type: domain.MeetingActionReassign, Payload: map[string]any{"task_id": setup.firstTask.Task.ID, "agent_id": setup.participants[0].ID, "lease_seconds": 600}}}}
		result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID, ExpectedRevision: 1, Fixture: fixture, IdempotencyKey: "apply-reassign", CorrelationID: "request-apply-reassign"})
		if err != nil || result.Detail.Meeting.Status != domain.MeetingConcluded {
			t.Fatalf("RunMeeting(reassign) = %#v, %v", result, err)
		}
		task, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.firstTask.Task.ID)
		if err != nil || task.Task.Status != domain.TaskAssigned || task.Task.AssignedAgentID != setup.participants[0].ID {
			t.Fatalf("task after reassign = %#v, %v", task, err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		setup := initializeMeetingTest(t, storage, 2)
		created := createMeetingTest(t, storage, setup, domain.MeetingPolicyManagerBounded, []string{domain.MeetingActionCancel}, "")
		fixture := sequenceMeetingFixture(setup)
		fixture.Proposal = &domain.MeetingProposalInput{Summary: "cancel duplicate work", Actions: []domain.MeetingActionInput{{Type: domain.MeetingActionCancel, Payload: map[string]any{"task_id": setup.secondTask.Task.ID}}}}
		result, err := storage.RunMeeting(context.Background(), RunMeetingCommand{WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID, ExpectedRevision: 1, Fixture: fixture, IdempotencyKey: "apply-cancel", CorrelationID: "request-apply-cancel"})
		if err != nil || result.Detail.Meeting.Status != domain.MeetingConcluded {
			t.Fatalf("RunMeeting(cancel) = %#v, %v", result, err)
		}
		task, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.secondTask.Task.ID)
		if err != nil || task.Task.Status != domain.TaskCancelled {
			t.Fatalf("task after cancel = %#v, %v", task, err)
		}
		overlap, err := storage.Overlap(context.Background(), setup.workspace.ID, setup.overlap.ID)
		if err != nil || overlap.Status != domain.OverlapResolved {
			t.Fatalf("overlap after cancel = %#v, %v", overlap, err)
		}
	})
}

func TestMeetingRejectsNonParticipantPositionAndUnknownActionFields(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	outsider := createMeetingAgent(t, storage, setup.workspace.ID, "meeting-outsider")
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	_, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID, ExpectedRevision: 1,
		Fixture:        domain.MeetingRunFixture{Positions: []domain.MeetingPositionInput{{AgentID: outsider.ID, Summary: "inject position"}}},
		IdempotencyKey: "outsider-position", CorrelationID: "request-outsider-position",
	})
	if ErrorCode(err) != CodeInvalidMeeting {
		t.Fatalf("RunMeeting(outsider) error = %v, code = %q", err, ErrorCode(err))
	}
	fixture := sequenceMeetingFixture(setup)
	fixture.Proposal.Actions[0].Payload["unexpected"] = true
	_, err = storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID, ExpectedRevision: 1,
		Fixture: fixture, IdempotencyKey: "unknown-action-field", CorrelationID: "request-unknown-action-field",
	})
	if ErrorCode(err) != CodeInvalidMeeting {
		t.Fatalf("RunMeeting(unknown action field) error = %v, code = %q", err, ErrorCode(err))
	}
	detail, err := storage.Meeting(context.Background(), setup.workspace.ID, created.Detail.Meeting.ID)
	if err != nil || detail.Meeting.Revision != 1 || len(detail.Contributions) != 0 || detail.Proposal != nil {
		t.Fatalf("meeting after rejected fixtures = %#v, %v", detail, err)
	}
}

func TestMeetingAcceptanceRejectsStaleFrozenTaskWithoutPartialMutation(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	proposed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "stale-proposal", CorrelationID: "request-stale-proposal",
	})
	if err != nil {
		t.Fatalf("RunMeeting() error = %v", err)
	}
	newTitle := "changed outside the meeting"
	if _, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{WorkspaceIdentifier: setup.workspace.ID, TaskID: setup.firstTask.Task.ID, Title: &newTitle, ExpectedRevision: setup.firstTask.Task.Revision, IdempotencyKey: "external-task-change", CorrelationID: "request-external-task-change"}); err != nil {
		t.Fatalf("UpdateTask() error = %v", err)
	}
	_, err = storage.AcceptMeeting(context.Background(), AcceptMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: proposed.Detail.Meeting.Revision, IdempotencyKey: "accept-stale", CorrelationID: "request-accept-stale",
	})
	if ErrorCode(err) != CodeMeetingStale {
		t.Fatalf("AcceptMeeting(stale) error = %v, code = %q", err, ErrorCode(err))
	}
	second, err := storage.TaskDetail(context.Background(), setup.workspace.ID, setup.secondTask.Task.ID)
	if err != nil || len(second.Dependencies) != 0 {
		t.Fatalf("stale acceptance partially mutated task = %#v, %v", second, err)
	}
}

func TestMeetingMutationFailureRollsBackProposalActionsAndPositions(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected meeting persistence failure")
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterProjection {
			return injected
		}
		return nil
	}
	_, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: "rollback-meeting", CorrelationID: "request-rollback-meeting",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("RunMeeting() error = %v, want injected", err)
	}
	storage.mutationHook = nil
	detail, err := storage.Meeting(context.Background(), setup.workspace.ID, created.Detail.Meeting.ID)
	if err != nil || detail.Meeting.Revision != 1 || detail.Proposal != nil || len(detail.Contributions) != 0 {
		t.Fatalf("meeting after rollback = %#v, %v", detail, err)
	}
}

type meetingTestSetup struct {
	workspace    Workspace
	project      domain.Project
	firstTask    domain.TaskDetail
	secondTask   domain.TaskDetail
	overlap      domain.WorkOverlap
	participants []domain.AgentDefinition
	facilitator  domain.AgentDefinition
}

func initializeMeetingTest(t *testing.T, storage *Store, participantCount int) meetingTestSetup {
	t.Helper()
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "shared contract owner", "meeting-first-task")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "shared contract consumer", "meeting-second-task")
	addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: firstTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "internal/shared/**", Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyPauseScheduling,
		LeaseDuration: time.Hour, IdempotencyKey: "meeting-first-claim", CorrelationID: "request-meeting-first-claim",
	})
	secondClaim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: secondTask.Task.ID, CheckoutIdentifier: checkout.ID,
		Kind: domain.ClaimKindPath, Target: "internal/shared/contract.go", Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyRequestResolution,
		LeaseDuration: time.Hour, IdempotencyKey: "meeting-second-claim", CorrelationID: "request-meeting-second-claim",
	})
	participants := make([]domain.AgentDefinition, 0, participantCount)
	for index := 0; index < participantCount; index++ {
		participants = append(participants, createMeetingAgent(t, storage, workspace.ID, "meeting-agent-"+string(rune('a'+index))))
	}
	return meetingTestSetup{workspace: workspace, project: project, firstTask: firstTask, secondTask: secondTask, overlap: secondClaim.Overlaps[0], participants: participants, facilitator: createMeetingAgent(t, storage, workspace.ID, "meeting-manager")}
}

func createMeetingAgent(t *testing.T, storage *Store, workspaceID, name string) domain.AgentDefinition {
	t.Helper()
	result, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspaceID, Name: name, Role: "meeting participant", Provider: "fake", Runtime: "fake", IdempotencyKey: "create-" + name, CorrelationID: "request-create-" + name})
	if err != nil {
		t.Fatalf("CreateAgent(%s) error = %v", name, err)
	}
	return result.Value
}

func createMeetingTest(t *testing.T, storage *Store, setup meetingTestSetup, policy string, allowed []string, reviewer string) MeetingMutationResult {
	t.Helper()
	participants := make([]string, 0, len(setup.participants))
	for _, participant := range setup.participants {
		participants = append(participants, participant.ID)
	}
	result, err := storage.CreateMeeting(context.Background(), CreateMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, OverlapID: setup.overlap.ID, ParticipantAgents: participants,
		FacilitatorAgent: setup.facilitator.ID, Policy: policy, ReviewerAgent: reviewer, AllowedActions: allowed,
		Timeout: time.Hour, IdempotencyKey: "create-meeting-" + policy, CorrelationID: "request-create-meeting-" + policy,
	})
	if err != nil {
		t.Fatalf("CreateMeeting() error = %v", err)
	}
	return result
}

func sequenceMeetingFixture(setup meetingTestSetup) domain.MeetingRunFixture {
	return domain.MeetingRunFixture{
		Positions: []domain.MeetingPositionInput{
			{AgentID: setup.participants[0].ID, Summary: "complete the contract first", Evidence: []string{"owns contract"}},
			{AgentID: setup.participants[1].ID, Summary: "consume the contract second", Evidence: []string{"depends on contract"}},
		},
		Proposal: &domain.MeetingProposalInput{Summary: "sequence the overlapping work", Actions: []domain.MeetingActionInput{{Type: domain.MeetingActionSequence, Payload: map[string]any{"before_task_id": setup.firstTask.Task.ID, "after_task_id": setup.secondTask.Task.ID}}}},
	}
}
