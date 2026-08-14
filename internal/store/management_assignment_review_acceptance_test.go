package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestManagerAssignmentProposalRequiresReadyUnassignedTargetWithoutOpenIntent(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, assignmentReviewValidationFixture, domain.TaskDetail) domain.TaskDetail
	}{
		{
			name: "assigned target",
			setup: func(t *testing.T, fixture assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				assigned, err := fixture.storage.AssignTask(context.Background(), AssignTaskCommand{
					WorkspaceIdentifier: fixture.base.workspace.ID,
					TaskID:              target.Task.ID,
					AgentIdentifier:     fixture.base.target.AgentID,
					LeaseSeconds:        900,
					ExpectedRevision:    target.Task.Revision,
					IdempotencyKey:      "assignment-validation-assigned-target",
					CorrelationID:       "request-assignment-validation-assigned-target",
				})
				if err != nil {
					t.Fatalf("AssignTask(assignment validation target) = %v", err)
				}
				return assigned.Detail
			},
		},
		{
			name: "dependency blocked target",
			setup: func(t *testing.T, fixture assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				upstream := fixture.createTask(t, "assignment-non-ready-upstream")
				blocked, err := fixture.storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{
					WorkspaceIdentifier: fixture.base.workspace.ID,
					TaskID:              target.Task.ID,
					DependsOnTaskID:     upstream.Task.ID,
					ExpectedRevision:    target.Task.Revision,
					IdempotencyKey:      "assignment-validation-non-ready-dependency",
					CorrelationID:       "request-assignment-validation-non-ready-dependency",
				})
				if err != nil {
					t.Fatalf("AddTaskDependency(assignment validation target) = %v", err)
				}
				if blocked.Detail.Task.Status != domain.TaskReady || blocked.Detail.Readiness.Ready {
					t.Fatalf("dependency-blocked assignment target = %#v; want ready projection with false readiness", blocked.Detail)
				}
				return blocked.Detail
			},
		},
		{
			name: "open intent target",
			setup: func(t *testing.T, fixture assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				fixture.acceptAssignment(t, target.Task, fixture.base.target.ID, "assignment-validation-open-intent-seed")
				stored, err := fixture.storage.TaskDetail(context.Background(), fixture.base.workspace.ID, target.Task.ID)
				if err != nil {
					t.Fatalf("TaskDetail(open intent assignment target) = %v", err)
				}
				if stored.Task.Status != domain.TaskReady || stored.Task.AssignmentID != "" || !stored.Readiness.Ready {
					t.Fatalf("open-intent assignment target = %#v; want otherwise ready and unassigned", stored)
				}
				assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND status='pending'`, target.Task.ID)
				return stored
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			key := "assignment-invalid-" + proposalValidationKey(test.name)
			fixture := newAssignmentReviewValidationFixture(t, key)
			target := test.setup(t, fixture, fixture.createTask(t, key+"-target"))
			invalid := fixture.submitAssignment(t, target.Task, fixture.reviewerProfile.ID, key)
			assertInvalidManagementProposalHasNoEffects(t, fixture.storage, fixture.base.workspace.ID, invalid, "task_not_assignable", key)
		})
	}

	t.Run("valid ready unassigned control", func(t *testing.T) {
		t.Parallel()
		const key = "assignment-valid-ready-unassigned"
		fixture := newAssignmentReviewValidationFixture(t, key)
		target := fixture.createTask(t, key+"-target")
		if target.Task.Status != domain.TaskReady || target.Task.AssignmentID != "" || !target.Readiness.Ready {
			t.Fatalf("valid assignment control target = %#v; want ready and unassigned", target)
		}
		submitted := fixture.submitAssignment(t, target.Task, fixture.base.target.ID, key)
		if submitted.Proposal.Status != domain.ManagerProposalPending || len(submitted.Proposal.ValidationIssues) != 0 {
			t.Fatalf("valid assignment proposal = %#v; want pending without issues", submitted.Proposal)
		}
		accepted := fixture.acceptProposal(t, submitted, key)
		if accepted.Proposal.Status != domain.ManagerProposalAccepted || len(accepted.Effects) != 1 ||
			accepted.Effects[0].EntityType != "scheduling_intent" || accepted.Effects[0].ActionID != submitted.Proposal.Actions[0].ID {
			t.Fatalf("accepted assignment proposal = %#v; want one exact scheduling-intent effect", accepted)
		}
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM scheduling_intents
WHERE id=? AND task_id=? AND agent_id=? AND launch_profile_id=? AND source_proposal_id=? AND source_action_id=? AND status='pending'`,
			accepted.Effects[0].EntityID, target.Task.ID, fixture.base.target.AgentID, fixture.base.target.ID,
			submitted.Proposal.ID, submitted.Proposal.Actions[0].ID)
		assertManagementRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM task_assignments WHERE task_id=?`, target.Task.ID)
	})
}

func TestManagerReviewProposalRequiresIndependentExactReviewer(t *testing.T) {
	for _, test := range []struct {
		name      string
		issueCode string
		setup     func(*testing.T, assignmentReviewValidationFixture, domain.TaskDetail) domain.TaskDetail
	}{
		{
			name:      "target implementer is not yet known",
			issueCode: "review_implementer_unknown",
			setup: func(_ *testing.T, _ assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				return target
			},
		},
		{
			name:      "current implementer",
			issueCode: "reviewer_not_independent",
			setup: func(t *testing.T, fixture assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				assigned, err := fixture.storage.AssignTask(context.Background(), AssignTaskCommand{
					WorkspaceIdentifier: fixture.base.workspace.ID,
					TaskID:              target.Task.ID,
					AgentIdentifier:     fixture.base.target.AgentID,
					LeaseSeconds:        900,
					ExpectedRevision:    target.Task.Revision,
					IdempotencyKey:      "review-validation-current-implementer-assignment",
					CorrelationID:       "request-review-validation-current-implementer-assignment",
				})
				if err != nil {
					t.Fatalf("AssignTask(review current implementer target) = %v", err)
				}
				return assigned.Detail
			},
		},
		{
			name:      "open intent planned implementer",
			issueCode: "reviewer_not_independent",
			setup: func(t *testing.T, fixture assignmentReviewValidationFixture, target domain.TaskDetail) domain.TaskDetail {
				fixture.acceptAssignment(t, target.Task, fixture.base.target.ID, "review-validation-open-intent-seed")
				stored, err := fixture.storage.TaskDetail(context.Background(), fixture.base.workspace.ID, target.Task.ID)
				if err != nil {
					t.Fatalf("TaskDetail(review open-intent target) = %v", err)
				}
				if stored.Task.AssignedAgentID != "" {
					t.Fatalf("review open-intent target already has current implementer %#v", stored.Task)
				}
				assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND agent_id=? AND status='pending'`, target.Task.ID, fixture.base.target.AgentID)
				return stored
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			key := "review-invalid-" + proposalValidationKey(test.name)
			fixture := newAssignmentReviewValidationFixture(t, key)
			target := test.setup(t, fixture, fixture.createTask(t, key+"-target"))
			invalid := fixture.submitReview(t, target.Task, fixture.base.target.ID, key)
			assertInvalidManagementProposalHasNoEffects(t, fixture.storage, fixture.base.workspace.ID, invalid, test.issueCode, key)
		})
	}

	t.Run("valid independent reviewer control", func(t *testing.T) {
		t.Parallel()
		const key = "review-valid-independent"
		fixture := newAssignmentReviewValidationFixture(t, key)
		target := fixture.createTask(t, key+"-target")
		assigned, err := fixture.storage.AssignTask(context.Background(), AssignTaskCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			TaskID:              target.Task.ID,
			AgentIdentifier:     fixture.base.target.AgentID,
			LeaseSeconds:        900,
			ExpectedRevision:    target.Task.Revision,
			IdempotencyKey:      key + "-assignment",
			CorrelationID:       "request-" + key + "-assignment",
		})
		if err != nil {
			t.Fatalf("AssignTask(valid review target) = %v", err)
		}
		if assigned.Detail.Task.AssignedAgentID == fixture.reviewerProfile.AgentID || fixture.base.target.AgentID == fixture.reviewerProfile.AgentID {
			t.Fatalf("valid review control did not use independent agent IDs: implementer=%s reviewer=%s", fixture.base.target.AgentID, fixture.reviewerProfile.AgentID)
		}
		submitted := fixture.submitReview(t, assigned.Detail.Task, fixture.reviewerProfile.ID, key)
		if submitted.Proposal.Status != domain.ManagerProposalPending || len(submitted.Proposal.ValidationIssues) != 0 {
			t.Fatalf("valid independent review proposal = %#v; want pending without issues", submitted.Proposal)
		}
		accepted := fixture.acceptProposal(t, submitted, key)
		if accepted.Proposal.Status != domain.ManagerProposalAccepted || len(accepted.Effects) != 3 {
			t.Fatalf("accepted independent review proposal = %#v; want three atomic effects", accepted)
		}
		var reviewTaskID, intentID string
		for _, effect := range accepted.Effects {
			if effect.ActionID != submitted.Proposal.Actions[0].ID {
				t.Fatalf("review effect = %#v; want exact action %s", effect, submitted.Proposal.Actions[0].ID)
			}
			if effect.EntityType == "task" && effect.EffectType == "created" {
				reviewTaskID = effect.EntityID
			}
			if effect.EntityType == "scheduling_intent" {
				intentID = effect.EntityID
			}
		}
		if reviewTaskID == "" || intentID == "" {
			t.Fatalf("independent review effects = %#v; want created task and scheduling intent", accepted.Effects)
		}
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM task_dependencies WHERE task_id=? AND depends_on_task_id=?`, reviewTaskID, target.Task.ID)
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM scheduling_intents
WHERE id=? AND task_id=? AND agent_id=? AND launch_profile_id=? AND source_proposal_id=? AND source_action_id=? AND status='pending'`,
			intentID, reviewTaskID, fixture.reviewerProfile.AgentID, fixture.reviewerProfile.ID,
			submitted.Proposal.ID, submitted.Proposal.Actions[0].ID)
	})
}

type assignmentReviewValidationFixture struct {
	storage         *Store
	base            managerGrantAdversarialFixture
	reviewer        domain.AgentDefinition
	reviewerProfile domain.LaunchProfile
	managerRunID    string
	packetSequence  int64
}

func newAssignmentReviewValidationFixture(t *testing.T, key string) assignmentReviewValidationFixture {
	t.Helper()
	storage, base := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()
	reviewer, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: base.workspace.ID,
		Name:                "independent-reviewer",
		Role:                base.manager.Role,
		Provider:            "fake",
		Runtime:             "fake",
		MaxConcurrency:      2,
		IdempotencyKey:      "assignment-review-agent-" + key,
		CorrelationID:       "request-assignment-review-agent-" + key,
	})
	if err != nil {
		t.Fatalf("CreateAgent(independent reviewer %s) = %v", key, err)
	}
	reviewerProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier:   base.workspace.ID,
		ProjectIdentifier:     base.project.ID,
		AgentIdentifier:       reviewer.Value.ID,
		ExpectedAgentRevision: reviewer.Value.Revision,
		Purpose:               "arbitrary review workflow metadata without authority semantics",
		Runtime:               reviewer.Value.Runtime,
		Provider:              reviewer.Value.Provider,
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   "independent-reviewer",
			Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "inspect exact accepted work"}},
		},
		AssignmentLeaseSeconds: 900,
		CapabilityTTLSeconds:   900,
		IdempotencyKey:         "assignment-review-profile-" + key,
		CorrelationID:          "request-assignment-review-profile-" + key,
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(independent reviewer %s) = %v", key, err)
	}
	grant, err := storage.CreateManagerGrant(ctx, CreateManagerGrantCommand{
		WorkspaceIdentifier:   base.workspace.ID,
		ProjectIdentifier:     base.project.ID,
		ObjectiveID:           base.objective.ID,
		TaskID:                base.planning.Task.ID,
		AgentIdentifier:       base.manager.ID,
		ExpectedTaskRevision:  base.planning.Task.Revision,
		ExpectedAgentRevision: base.manager.Revision,
		ProposalKinds:         []string{domain.ManagerProposalAssignment, domain.ManagerProposalReview},
		LaunchProfileIDs:      []string{base.target.ID, reviewerProfile.Value.ID},
		AllowedClaimKinds:     []string{},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 8, MaxActions: 4, MaxTasks: 4, MaxDependencies: 4, MaxClaimRequirements: 4,
			Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		},
		IdempotencyKey: "assignment-review-grant-" + key,
		CorrelationID:  "request-assignment-review-grant-" + key,
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(assignment/review %s) = %v", key, err)
	}
	base.grant = grant.Value
	runID, packetSequence := invokeAdversarialManager(t, storage, base, "assignment-review-validation")
	if base.manager.Role != reviewer.Value.Role || base.manager.Role != "constellation cartographer" ||
		reviewerProfile.Value.Purpose == reviewer.Value.Role {
		t.Fatalf("assignment/review fixture accidentally derives authority from role/purpose: manager=%q reviewer=%q purpose=%q",
			base.manager.Role, reviewer.Value.Role, reviewerProfile.Value.Purpose)
	}
	return assignmentReviewValidationFixture{
		storage: storage, base: base, reviewer: reviewer.Value, reviewerProfile: reviewerProfile.Value,
		managerRunID: runID, packetSequence: packetSequence,
	}
}

func (fixture assignmentReviewValidationFixture) createTask(t *testing.T, key string) domain.TaskDetail {
	t.Helper()
	created, err := fixture.storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		ProjectIdentifier:   fixture.base.project.ID,
		ObjectiveID:         fixture.base.objective.ID,
		Title:               "Assignment/review validation " + key,
		Budget:              domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		IdempotencyKey:      key,
		CorrelationID:       "request-" + key,
	})
	if err != nil {
		t.Fatalf("CreateTask(%s) = %v", key, err)
	}
	return created.Detail
}

func (fixture assignmentReviewValidationFixture) submitAssignment(t *testing.T, task domain.Task, profileID, key string) ManagerProposalMutationResult {
	t.Helper()
	result, err := fixture.storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.base.grant.ID,
		ExpectedGrantRevision: fixture.base.grant.Revision,
		Kind:                  domain.ManagerProposalAssignment,
		Summary:               "Nominate one exact ready target profile.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions: []domain.ManagerProposalAction{{
			Type: domain.ProposalActionAssignTask,
			AssignTask: &domain.ProposalAssignTaskAction{
				Task: domain.ProposalTaskRef{TaskID: task.ID, ExpectedTaskRevision: task.Revision}, LaunchProfileID: profileID,
			},
		}},
		IdempotencyKey: key + "-submit-assignment",
		CorrelationID:  "request-" + key + "-submit-assignment",
	})
	if err != nil {
		t.Fatalf("SubmitManagerProposal(assignment %s) = %v", key, err)
	}
	if len(result.Effects) != 0 {
		t.Fatalf("assignment submission %s effects = %#v; want inert", key, result.Effects)
	}
	return result
}

func (fixture assignmentReviewValidationFixture) acceptAssignment(t *testing.T, task domain.Task, profileID, key string) ManagerProposalMutationResult {
	t.Helper()
	submitted := fixture.submitAssignment(t, task, profileID, key)
	if submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("seed assignment proposal %s = %#v; want pending", key, submitted.Proposal)
	}
	return fixture.acceptProposal(t, submitted, key+"-assignment")
}

func (fixture assignmentReviewValidationFixture) submitReview(t *testing.T, task domain.Task, profileID, key string) ManagerProposalMutationResult {
	t.Helper()
	result, err := fixture.storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.base.grant.ID,
		ExpectedGrantRevision: fixture.base.grant.Revision,
		Kind:                  domain.ManagerProposalReview,
		Summary:               "Request one exact independent review.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions: []domain.ManagerProposalAction{{
			Type: domain.ProposalActionRequestReview,
			RequestReview: &domain.ProposalRequestReviewAction{
				Task: domain.ProposalTaskRef{TaskID: task.ID, ExpectedTaskRevision: task.Revision}, LaunchProfileID: profileID,
				Title: "Independent review of exact work", Description: "Inspect the exact target without implementation authority.", Priority: 10,
				Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
			},
		}},
		IdempotencyKey: key + "-submit-review",
		CorrelationID:  "request-" + key + "-submit-review",
	})
	if err != nil {
		t.Fatalf("SubmitManagerProposal(review %s) = %v", key, err)
	}
	if len(result.Effects) != 0 {
		t.Fatalf("review submission %s effects = %#v; want inert", key, result.Effects)
	}
	return result
}

func (fixture assignmentReviewValidationFixture) acceptProposal(t *testing.T, submitted ManagerProposalMutationResult, key string) ManagerProposalMutationResult {
	t.Helper()
	accepted, err := fixture.storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		ManagerProposalID:   submitted.Proposal.ID,
		ExpectedRevision:    submitted.Proposal.Revision,
		DecisionNote:        "Accept this exact validated manager proposal.",
		IdempotencyKey:      key + "-accept",
		CorrelationID:       "request-" + key + "-accept",
	})
	if err != nil {
		t.Fatalf("AcceptManagerProposal(%s) = %v", key, err)
	}
	return accepted
}

func assertInvalidManagementProposalHasNoEffects(t *testing.T, storage *Store, workspaceID string, result ManagerProposalMutationResult, issueCode, key string) {
	t.Helper()
	if result.Proposal.Status != domain.ManagerProposalInvalid || len(result.Effects) != 0 {
		t.Fatalf("invalid proposal %s = %#v; want invalid and inert", key, result)
	}
	assertProposalIssue(t, result.Proposal.ValidationIssues, issueCode, domain.ProposalIssueError)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, result.Proposal.ID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, result.Proposal.ID)
	if _, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: workspaceID,
		ManagerProposalID:   result.Proposal.ID,
		ExpectedRevision:    result.Proposal.Revision,
		DecisionNote:        "Invalid exact target must remain inert.",
		IdempotencyKey:      key + "-invalid-accept",
		CorrelationID:       "request-" + key + "-invalid-accept",
	}); ErrorCode(err) != CodeManagerProposalConflict {
		t.Fatalf("AcceptManagerProposal(invalid %s) = %v, code %q; want %q", key, err, ErrorCode(err), CodeManagerProposalConflict)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, result.Proposal.ID)
}

func proposalValidationKey(value string) string {
	result := make([]byte, len(value))
	for index := range value {
		if value[index] == ' ' {
			result[index] = '-'
		} else {
			result[index] = value[index]
		}
	}
	return string(result)
}
