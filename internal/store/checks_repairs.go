package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	checkRepairProposedEvent = "check.repair_proposed"
	checkRepairAcceptedEvent = "check.repair_accepted"
	checkRepairRejectedEvent = "check.repair_rejected"
	checkRepairStaleEvent    = "check.repair_stale"

	maximumRepairTaskTokens      int64 = 200000
	maximumRepairTaskCostCents   int64 = 10000
	maximumRepairTaskTimeSeconds int64 = 86400
)

// checkRepairRecipe is the canonical, deterministic recipe frozen by a
// proposal. The granted watcher supplies only ResultID and Rationale; it never
// chooses a task, budget, profile, agent, or command.
type checkRepairRecipe struct {
	ObjectiveID         string        `json:"objective_id"`
	ObjectiveRevision   int64         `json:"objective_revision"`
	SourceTaskID        string        `json:"source_task_id"`
	SourceTaskRevision  int64         `json:"source_task_revision"`
	RequirementID       string        `json:"requirement_id"`
	RequirementRevision int64         `json:"requirement_revision"`
	CheckResultID       string        `json:"check_result_id"`
	Rationale           string        `json:"rationale"`
	Title               string        `json:"title"`
	Description         string        `json:"description"`
	Priority            int           `json:"priority"`
	Budget              domain.Budget `json:"budget"`
}

func (s *Store) ProposeGrantedCheckRepair(ctx context.Context, command ProposeGrantedCheckRepairCommand) (MutationResult[domain.CheckRepairProposal], error) {
	command.SourceRunID = strings.TrimSpace(command.SourceRunID)
	command.CheckResultID = strings.TrimSpace(command.CheckResultID)
	command.Rationale = strings.TrimSpace(command.Rationale)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.SourceRunID == "" || command.CheckResultID == "" || !validCheckText(command.Rationale, 4096) {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair proposal requires an exact result and bounded rationale")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeCheckRepairDenied); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	hash, err := checkSemanticHash("check.repair.propose", command)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("hash check repair proposal", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("begin check repair proposal", err)
	}
	defer tx.Rollback()

	// Agent authorization is current and happens before idempotency replay.
	grant, err := s.authorizeRunCheckWatchGrant(ctx, tx, command.SourceRunID, domain.CheckWatchOperationProposeRepair)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	idempotencyKey := runCheckIdempotencyKey(command.SourceRunID, command.IdempotencyKey)
	var replay MutationResult[domain.CheckRepairProposal]
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.repair.propose", hash, &replay); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	} else if found {
		return replay, nil
	}

	queries := dbgen.New(tx)
	checkRunID, err := queries.GetCheckResultRunID(ctx, command.CheckResultID)
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairNotFound, "check result was not found")
	}
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("resolve repair source result", err)
	}
	detail, err := checkRunDetailInTransaction(ctx, tx, checkRunID)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if detail.Result == nil || detail.CurrentFreshness == nil || detail.Result.ID != command.CheckResultID ||
		detail.Result.Outcome != domain.CheckOutcomeFailed || detail.CurrentFreshness.Status != domain.CheckFreshnessFresh ||
		!detail.CurrentFreshness.Observation.Available || detail.CurrentFreshness.Observation.HeadCommit == "" ||
		detail.Run.ProjectID != grant.ProjectID || detail.Run.WorkspaceID != grant.WorkspaceID ||
		detail.Requirement.Status != domain.CheckRequirementActive || detail.Requirement.Revision != detail.Run.RequirementRevision {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair source must be the exact current failed and fresh result")
	}
	latest, err := queries.CheckResultIsLatestForRequirement(ctx, command.CheckResultID)
	if err != nil || latest != 1 {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair source result is no longer latest")
	}
	allowed := false
	for _, definition := range grant.Definitions {
		if definition.DefinitionID == detail.Run.DefinitionID && definition.ContentRevision == detail.Run.DefinitionContentRevision && definition.DefinitionSHA256 == detail.Run.DefinitionSHA256 {
			allowed = true
			break
		}
	}
	if !allowed {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckWatchGrantDenied, "repair source definition is outside current grant")
	}
	task, err := queryTask(ctx, tx, grant.WorkspaceID, detail.Run.TaskID)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if task.ProjectID != grant.ProjectID || task.ObjectiveID == "" || task.Revision != detail.Run.TaskRevision || task.Status == domain.TaskCompleted || task.Status == domain.TaskCancelled {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, fmt.Sprintf("repair source task is no longer current (task project %s/%s, revision %d/%d, status %s, objective present %t)", task.ProjectID, grant.ProjectID, task.Revision, detail.Run.TaskRevision, task.Status, task.ObjectiveID != ""))
	}
	objective, err := queryObjective(ctx, tx, grant.WorkspaceID, task.ObjectiveID)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if objective.ProjectID != grant.ProjectID || objective.Status != domain.ObjectiveActive {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair source objective is no longer active")
	}
	policy, err := queryCheckPolicy(ctx, queries, grant.WorkspaceID, grant.ProjectID)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if !policy.RepairProposalsEnabled {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "check repair proposals are disabled")
	}
	profile, err := queryLaunchProfile(ctx, tx, grant.WorkspaceID, policy.RepairLaunchProfileID)
	if err != nil || profile.ProjectID != grant.ProjectID || profile.Revision != policy.RepairLaunchProfileRevision || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair policy profile is no longer exact and active")
	}
	repairAgent, err := queryAgent(ctx, tx, grant.WorkspaceID, profile.AgentID)
	if err != nil || !repairAgent.Enabled || repairAgent.Revision != profile.AgentRevision {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "repair policy agent is no longer exact and enabled")
	}
	open, err := queries.CountOpenCheckRepairProposals(ctx, dbgen.CountOpenCheckRepairProposalsParams{WorkspaceID: grant.WorkspaceID, ProjectID: grant.ProjectID})
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("count open check repair proposals", err)
	}
	if open >= int64(policy.MaxOpenRepairProposals) {
		return MutationResult[domain.CheckRepairProposal]{}, checkError(CodeCheckRepairDenied, "check repair proposal limit is exhausted")
	}

	recipe := deriveCheckRepairRecipe(task, objective, detail.Requirement, *detail.Result, command.Rationale)
	_, recipeHash, err := canonicalContent(recipe)
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("hash check repair recipe", err)
	}
	id, err := randomID("checkrepair_")
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("generate check repair proposal id", err)
	}
	now := s.nowText()
	createdBy := "agent:" + grant.AgentID
	proposal := domain.CheckRepairProposal{
		ID: id, WorkspaceID: grant.WorkspaceID, ProjectID: grant.ProjectID,
		ObjectiveID: objective.ID, ObjectiveRevision: objective.Revision, TaskID: task.ID, TaskRevision: task.Revision,
		RequirementID: detail.Requirement.ID, RequirementRevision: detail.Requirement.Revision,
		CheckResultID: detail.Result.ID, FreshnessRevision: detail.CurrentFreshness.Revision,
		SourceRepositoryID: detail.Result.TerminalObservation.RepositoryID, SourceCheckoutID: detail.Result.TerminalObservation.CheckoutID,
		SourceHeadCommit: detail.CurrentFreshness.Observation.HeadCommit, PolicyRevision: policy.Revision,
		RepairLaunchProfileID: profile.ID, RepairLaunchProfileRevision: profile.Revision,
		SourceRunID: command.SourceRunID, SourceAgentID: grant.AgentID, SourceAgentRevision: grant.AgentRevision,
		SourceGrantID: grant.ID, SourceGrantRevision: grant.Revision, Rationale: command.Rationale,
		RepairTaskTitle: recipe.Title, RepairTaskDescription: recipe.Description, RepairTaskPriority: recipe.Priority, RepairTaskBudget: recipe.Budget,
		Status: domain.CheckRepairPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: createdBy, UpdatedBy: createdBy,
	}
	err = s.withCheckMutationSeal(func() error {
		return queries.InsertCheckRepairProposal(ctx, dbgen.InsertCheckRepairProposalParams{
			ID: proposal.ID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID,
			ObjectiveID: proposal.ObjectiveID, ObjectiveRevision: proposal.ObjectiveRevision, TaskID: proposal.TaskID, TaskRevision: proposal.TaskRevision,
			RequirementID: proposal.RequirementID, RequirementRevision: proposal.RequirementRevision, CheckResultID: proposal.CheckResultID, FreshnessRevision: proposal.FreshnessRevision,
			SourceRepositoryID: proposal.SourceRepositoryID, SourceCheckoutID: proposal.SourceCheckoutID, SourceHeadCommit: proposal.SourceHeadCommit,
			PolicyRevision: proposal.PolicyRevision, RepairLaunchProfileID: proposal.RepairLaunchProfileID, RepairLaunchProfileRevision: proposal.RepairLaunchProfileRevision,
			SourceRunID: proposal.SourceRunID, SourceAgentID: proposal.SourceAgentID, SourceAgentRevision: proposal.SourceAgentRevision,
			SourceGrantID: proposal.SourceGrantID, SourceGrantRevision: proposal.SourceGrantRevision,
			Rationale: proposal.Rationale, RepairTaskTitle: proposal.RepairTaskTitle, RepairTaskDescription: proposal.RepairTaskDescription,
			RepairTaskPriority: int64(proposal.RepairTaskPriority), RepairBudgetTokens: proposal.RepairTaskBudget.TokenLimit,
			RepairBudgetCostCents: proposal.RepairTaskBudget.CostCents, RepairBudgetTimeSeconds: proposal.RepairTaskBudget.TimeSeconds,
			RecipeSha256: recipeHash, CreatedAt: now, CreatedBy: createdBy,
		})
	})
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, checkConstraint("insert check repair proposal", CodeCheckRepairConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckRepairProposalProjection); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, grant.WorkspaceID, "check_repair_proposal", proposal.ID, 1, checkRepairProposedEvent, command.CorrelationID, now, command.SourceRunID, "agent_run", map[string]any{
		"check_result_id": proposal.CheckResultID, "source_run_id": proposal.SourceRunID, "source_agent_id": proposal.SourceAgentID,
		"source_agent_revision": proposal.SourceAgentRevision, "source_grant_id": proposal.SourceGrantID, "source_grant_revision": proposal.SourceGrantRevision,
		"repair_launch_profile_id": proposal.RepairLaunchProfileID, "repair_launch_profile_revision": proposal.RepairLaunchProfileRevision,
	})
	if err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRepairProposalEvent); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	result := MutationResult[domain.CheckRepairProposal]{Value: proposal, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.repair.propose", hash, result, now); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRepairProposalIdempotency); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckRepairProposal]{}, storageFailure("commit check repair proposal", err)
	}
	return result, nil
}

func (s *Store) CheckRepairProposals(ctx context.Context, query ListCheckRepairProposalsQuery) ([]domain.CheckRepairDetail, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.TaskID = strings.TrimSpace(query.TaskID)
	query.Status = strings.TrimSpace(query.Status)
	if query.Status != "" && !validCheckRepairStatus(query.Status) {
		return nil, checkError(CodeCheckRepairNotFound, "check repair status is unsupported")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageFailure("begin check repair list", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return nil, err
	}
	projectID := ""
	if query.ProjectIdentifier != "" {
		project, err := projectInTransaction(ctx, tx, workspace.ID, query.ProjectIdentifier)
		if err != nil {
			return nil, err
		}
		projectID = project.ID
	}
	queries := dbgen.New(tx)
	ids, err := queries.ListCheckRepairProposalIDs(ctx, dbgen.ListCheckRepairProposalIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: query.TaskID, Status: query.Status, ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check repair proposals", err)
	}
	result := make([]domain.CheckRepairDetail, 0, len(ids))
	for _, id := range ids {
		detail, err := loadCheckRepairDetail(ctx, tx, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, detail)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageFailure("finish check repair list", err)
	}
	return result, nil
}

func (s *Store) CheckRepairProposal(ctx context.Context, workspaceIdentifier, proposalID string) (domain.CheckRepairDetail, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.CheckRepairDetail{}, storageFailure("begin check repair read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.CheckRepairDetail{}, err
	}
	detail, err := loadCheckRepairDetail(ctx, tx, workspace.ID, strings.TrimSpace(proposalID))
	if err != nil {
		return domain.CheckRepairDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRepairDetail{}, storageFailure("finish check repair read", err)
	}
	return detail, nil
}

func (s *Store) AcceptCheckRepair(ctx context.Context, command DecideCheckRepairCommand) (MutationResult[domain.CheckRepairDetail], error) {
	return s.decideCheckRepair(ctx, command, domain.CheckRepairAccepted)
}

func (s *Store) RejectCheckRepair(ctx context.Context, command DecideCheckRepairCommand) (MutationResult[domain.CheckRepairDetail], error) {
	return s.decideCheckRepair(ctx, command, domain.CheckRepairRejected)
}

func (s *Store) decideCheckRepair(ctx context.Context, command DecideCheckRepairCommand, decision string) (MutationResult[domain.CheckRepairDetail], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.CheckRepairProposalID = strings.TrimSpace(command.CheckRepairProposalID)
	command.DecisionNote = strings.TrimSpace(command.DecisionNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || command.CheckRepairProposalID == "" || (command.DecisionNote != "" && !validCheckText(command.DecisionNote, 4096)) {
		return MutationResult[domain.CheckRepairDetail]{}, checkError(CodeCheckRepairConflict, "repair decision requires exact revision and an optional bounded note")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeCheckRepairConflict); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	operation := "check.repair." + decision
	hash, err := checkSemanticHash(operation, command)
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, storageFailure("hash check repair decision", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, storageFailure("begin check repair decision", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckRepairDetail]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, operation, hash, &replay); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	queries := dbgen.New(tx)
	row, err := queries.GetCheckRepairProposal(ctx, dbgen.GetCheckRepairProposalParams{WorkspaceID: workspace.ID, ID: command.CheckRepairProposalID})
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.CheckRepairDetail]{}, checkError(CodeCheckRepairNotFound, "check repair proposal was not found")
	}
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, storageFailure("read check repair proposal", err)
	}
	proposal, err := checkRepairProposalFromRow(row)
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	if proposal.Revision != command.ExpectedRevision {
		return MutationResult[domain.CheckRepairDetail]{}, revisionConflict("check repair proposal", proposal.ID, command.ExpectedRevision, proposal.Revision)
	}
	if proposal.Status != domain.CheckRepairPending {
		return MutationResult[domain.CheckRepairDetail]{}, checkError(CodeCheckRepairConflict, "only a pending repair proposal can be decided")
	}
	var profile domain.LaunchProfile
	if decision == domain.CheckRepairAccepted {
		profile, err = validateCheckRepairAcceptance(ctx, tx, queries, proposal)
		if err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
	}
	now := s.nowText()
	decisionID, err := randomID("checkdecision_")
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, storageFailure("generate check repair decision id", err)
	}
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.UpdateCheckRepairProposalStatus(ctx, dbgen.UpdateCheckRepairProposalStatusParams{Status: decision, UpdatedAt: now, UpdatedBy: localOwnerActorID, ID: proposal.ID, WorkspaceID: workspace.ID, ExpectedRevision: proposal.Revision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("repair proposal revision changed")
		}
		return queries.InsertCheckRepairDecision(ctx, dbgen.InsertCheckRepairDecisionParams{ID: decisionID, RepairProposalID: proposal.ID, Decision: decision, ProposalRevision: proposal.Revision, Note: nullableInterface(command.DecisionNote), CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, checkConstraint("record check repair decision", CodeCheckRepairConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckRepairDecision); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	proposal.Status = decision
	proposal.Revision++
	proposal.UpdatedAt = now
	proposal.UpdatedBy = localOwnerActorID

	var sequence int64
	if decision == domain.CheckRepairAccepted {
		taskID, _ := randomID("task_")
		intentID, _ := randomID("sintent_")
		effectID, _ := randomID("checkeffect_")
		objectiveID := proposal.ObjectiveID
		repairProposalID := proposal.ID
		err = s.withCheckMutationSeal(func() error {
			return queries.InsertCheckRepairTask(ctx, dbgen.InsertCheckRepairTaskParams{
				ID: taskID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: &objectiveID,
				Title: proposal.RepairTaskTitle, Description: proposal.RepairTaskDescription, Priority: int64(proposal.RepairTaskPriority),
				BudgetTokens: proposal.RepairTaskBudget.TokenLimit, BudgetCostCents: proposal.RepairTaskBudget.CostCents,
				BudgetTimeSeconds: proposal.RepairTaskBudget.TimeSeconds, CreatedAt: now,
			})
		})
		if err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, checkConstraint("create accepted check repair task", CodeCheckRepairConflict, err)
		}
		if err := s.runMutationHook(MutationAfterCheckRepairTask); err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
		if _, err := appendEvent(ctx, tx, workspace.ID, "task", taskID, 1, taskCreated, command.CorrelationID, now, map[string]any{
			"project_id": proposal.ProjectID, "objective_id": proposal.ObjectiveID, "title": proposal.RepairTaskTitle,
			"description": proposal.RepairTaskDescription, "priority": proposal.RepairTaskPriority, "budget": proposal.RepairTaskBudget,
			"source_check_repair_proposal_id": proposal.ID,
		}); err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
		err = s.withCheckMutationSeal(func() error {
			return queries.InsertCheckRepairSchedulingIntent(ctx, dbgen.InsertCheckRepairSchedulingIntentParams{
				ID: intentID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: proposal.ObjectiveID,
				TaskID: taskID, AgentID: profile.AgentID, LaunchProfileID: profile.ID, RepairProposalID: &repairProposalID, CreatedAt: now,
			})
		})
		if err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, checkConstraint("create accepted check repair intent", CodeCheckRepairConflict, err)
		}
		if err := s.runMutationHook(MutationAfterCheckRepairIntent); err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
		if _, err := appendEvent(ctx, tx, workspace.ID, "scheduling_intent", intentID, 1, schedulingIntentCreatedEvent, command.CorrelationID, now, map[string]any{
			"task_id": taskID, "launch_profile_id": profile.ID, "agent_id": profile.AgentID, "source_check_repair_proposal_id": proposal.ID,
		}); err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
		err = s.withCheckMutationSeal(func() error {
			return queries.InsertCheckRepairEffect(ctx, dbgen.InsertCheckRepairEffectParams{ID: effectID, RepairProposalID: proposal.ID, RepairTaskID: taskID, SchedulingIntentID: intentID, CreatedAt: now})
		})
		if err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, checkConstraint("record accepted check repair effect", CodeCheckRepairConflict, err)
		}
		if err := s.runMutationHook(MutationAfterCheckRepairEffect); err != nil {
			return MutationResult[domain.CheckRepairDetail]{}, err
		}
		sequence, err = appendEvent(ctx, tx, workspace.ID, "check_repair_proposal", proposal.ID, proposal.Revision, checkRepairAcceptedEvent, command.CorrelationID, now, map[string]any{
			"repair_task_id": taskID, "scheduling_intent_id": intentID, "repair_launch_profile_id": profile.ID,
		})
	} else {
		sequence, err = appendEvent(ctx, tx, workspace.ID, "check_repair_proposal", proposal.ID, proposal.Revision, checkRepairRejectedEvent, command.CorrelationID, now, map[string]any{"note": command.DecisionNote})
	}
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRepairEvent); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	detail, err := loadCheckRepairDetail(ctx, tx, workspace.ID, proposal.ID)
	if err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	result := MutationResult[domain.CheckRepairDetail]{Value: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, idempotencyKey, operation, hash, result, now); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckRepairIdempotency); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckRepairDetail]{}, storageFailure("commit check repair decision", err)
	}
	return result, nil
}

func validateCheckRepairAcceptance(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries, proposal domain.CheckRepairProposal) (domain.LaunchProfile, error) {
	runID, err := queries.GetCheckResultRunID(ctx, proposal.CheckResultID)
	if err != nil {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair source result is unavailable")
	}
	detail, err := checkRunDetailInTransaction(ctx, tx, runID)
	if err != nil {
		return domain.LaunchProfile{}, err
	}
	latest, err := queries.CheckResultIsLatestForRequirement(ctx, proposal.CheckResultID)
	if err != nil || latest != 1 || detail.Result == nil || detail.CurrentFreshness == nil ||
		detail.Result.ID != proposal.CheckResultID || detail.Result.Outcome != domain.CheckOutcomeFailed ||
		detail.CurrentFreshness.Revision != proposal.FreshnessRevision || detail.CurrentFreshness.Status != domain.CheckFreshnessFresh ||
		detail.CurrentFreshness.Observation.HeadCommit != proposal.SourceHeadCommit || detail.Result.RequirementID != proposal.RequirementID ||
		detail.Result.RequirementRevision != proposal.RequirementRevision || detail.Requirement.Status != domain.CheckRequirementActive ||
		detail.Requirement.Revision != proposal.RequirementRevision {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair source result is no longer the exact current failed and fresh result")
	}
	task, err := queryTask(ctx, tx, proposal.WorkspaceID, proposal.TaskID)
	if err != nil || task.ProjectID != proposal.ProjectID || task.ObjectiveID != proposal.ObjectiveID || task.Revision != proposal.TaskRevision || task.Status == domain.TaskCompleted || task.Status == domain.TaskCancelled {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair source task is no longer current")
	}
	objective, err := queryObjective(ctx, tx, proposal.WorkspaceID, proposal.ObjectiveID)
	if err != nil || objective.ProjectID != proposal.ProjectID || objective.Revision != proposal.ObjectiveRevision || objective.Status != domain.ObjectiveActive {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair source objective is no longer current and active")
	}
	if err := validateCheckRepairObjectiveBudget(ctx, queries, objective, proposal.RepairTaskBudget); err != nil {
		return domain.LaunchProfile{}, err
	}
	checkout, err := queryCheckoutByID(ctx, tx, proposal.SourceCheckoutID)
	if err != nil || checkout.ProjectID != proposal.ProjectID || checkout.RepositoryID != proposal.SourceRepositoryID || checkout.Availability != domain.CheckoutAvailable ||
		checkout.Revision != detail.Run.CheckoutRevision || checkout.Path != detail.Run.CheckoutPath || checkout.WriteMode != detail.Run.CheckoutWriteMode ||
		(checkout.HeadCommit != "" && checkout.HeadCommit != proposal.SourceHeadCommit) {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair source checkout is no longer current and available")
	}
	policy, err := queryCheckPolicy(ctx, queries, proposal.WorkspaceID, proposal.ProjectID)
	if err != nil || !policy.RepairProposalsEnabled || policy.Revision != proposal.PolicyRevision || policy.RepairLaunchProfileID != proposal.RepairLaunchProfileID || policy.RepairLaunchProfileRevision != proposal.RepairLaunchProfileRevision {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair policy is no longer exact and enabled")
	}
	profile, err := queryLaunchProfile(ctx, tx, proposal.WorkspaceID, proposal.RepairLaunchProfileID)
	if err != nil || profile.ProjectID != proposal.ProjectID || profile.Revision != proposal.RepairLaunchProfileRevision || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair launch profile is no longer exact and active")
	}
	agent, err := queryAgent(ctx, tx, proposal.WorkspaceID, profile.AgentID)
	if err != nil || !agent.Enabled || agent.Revision != profile.AgentRevision {
		return domain.LaunchProfile{}, checkError(CodeCheckRepairConflict, "repair launch profile agent is no longer exact and enabled")
	}
	return profile, nil
}

func validateCheckRepairObjectiveBudget(ctx context.Context, queries *dbgen.Queries, objective domain.Objective, proposed domain.Budget) error {
	objectiveID := objective.ID
	rows, err := queries.ListCheckRepairObjectiveTaskBudgets(ctx, dbgen.ListCheckRepairObjectiveTaskBudgetsParams{WorkspaceID: objective.WorkspaceID, ObjectiveID: &objectiveID})
	if err != nil {
		return storageFailure("read check repair objective allocations", err)
	}
	type dimension struct {
		limit     int64
		proposed  int64
		allocated int64
	}
	dimensions := []dimension{
		{limit: objective.Budget.TokenLimit, proposed: proposed.TokenLimit},
		{limit: objective.Budget.CostCents, proposed: proposed.CostCents},
		{limit: objective.Budget.TimeSeconds, proposed: proposed.TimeSeconds},
	}
	for _, row := range rows {
		allocations := []int64{row.BudgetTokens, row.BudgetCostCents, row.BudgetTimeSeconds}
		for index, allocation := range allocations {
			if dimensions[index].limit == 0 {
				continue
			}
			if allocation == 0 || allocation > dimensions[index].limit-dimensions[index].allocated {
				return checkError(CodeCheckRepairConflict, "repair task would exceed the current objective budget")
			}
			dimensions[index].allocated += allocation
		}
	}
	for _, item := range dimensions {
		if item.limit > 0 && (item.proposed <= 0 || item.proposed > item.limit-item.allocated) {
			return checkError(CodeCheckRepairConflict, "repair task would exceed the current objective budget")
		}
	}
	return nil
}

func loadCheckRepairDetail(ctx context.Context, tx *sql.Tx, workspaceID, proposalID string) (domain.CheckRepairDetail, error) {
	queries := dbgen.New(tx)
	row, err := queries.GetCheckRepairProposal(ctx, dbgen.GetCheckRepairProposalParams{WorkspaceID: workspaceID, ID: proposalID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckRepairDetail{}, checkError(CodeCheckRepairNotFound, "check repair proposal was not found")
	}
	if err != nil {
		return domain.CheckRepairDetail{}, storageFailure("read check repair proposal", err)
	}
	proposal, err := checkRepairProposalFromRow(row)
	if err != nil {
		return domain.CheckRepairDetail{}, err
	}
	runID, err := queries.GetCheckResultRunID(ctx, proposal.CheckResultID)
	if err != nil {
		return domain.CheckRepairDetail{}, storageFailure("read check repair source run", err)
	}
	result, _, err := queryCheckResultAndFreshness(ctx, tx, runID)
	if err != nil || result == nil || result.ID != proposal.CheckResultID {
		return domain.CheckRepairDetail{}, storageFailure("read check repair source result", err)
	}
	detail := domain.CheckRepairDetail{Proposal: proposal, Result: *result}
	decisionRow, err := queries.GetCheckRepairDecision(ctx, proposal.ID)
	if err == nil {
		decision := domain.CheckRepairDecision{ID: decisionRow.ID, RepairProposalID: decisionRow.RepairProposalID, Decision: decisionRow.Decision, ProposalRevision: decisionRow.ProposalRevision, Note: stringValue(decisionRow.Note), CreatedAt: decisionRow.CreatedAt, CreatedBy: decisionRow.CreatedBy}
		if decision.RepairProposalID != proposal.ID || decision.ProposalRevision+1 != proposal.Revision || (decision.Decision != domain.CheckRepairAccepted && decision.Decision != domain.CheckRepairRejected) || decision.Decision != proposal.Status || decision.CreatedBy != localOwnerActorID || !canonicalTimestamp(decision.CreatedAt) || decision.CreatedAt != proposal.UpdatedAt || (decision.Note != "" && !validCheckText(decision.Note, 4096)) {
			return domain.CheckRepairDetail{}, storageFailure("validate check repair decision", errors.New("decision differs from exact owner transition"))
		}
		detail.Decision = &decision
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.CheckRepairDetail{}, storageFailure("read check repair decision", err)
	}
	effectRow, err := queries.GetCheckRepairEffect(ctx, proposal.ID)
	if err == nil {
		effect := domain.CheckRepairEffect{ID: effectRow.ID, RepairProposalID: effectRow.RepairProposalID, RepairTaskID: effectRow.RepairTaskID, SchedulingIntentID: effectRow.SchedulingIntentID, CreatedAt: effectRow.CreatedAt}
		if proposal.Status != domain.CheckRepairAccepted || detail.Decision == nil || effect.RepairProposalID != proposal.ID || !canonicalTimestamp(effect.CreatedAt) || effect.CreatedAt != detail.Decision.CreatedAt {
			return domain.CheckRepairDetail{}, storageFailure("validate check repair effect", errors.New("effect differs from accepted decision"))
		}
		detail.Effect = &effect
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.CheckRepairDetail{}, storageFailure("read check repair effect", err)
	}
	if proposal.Status == domain.CheckRepairAccepted && (detail.Decision == nil || detail.Effect == nil) || proposal.Status == domain.CheckRepairRejected && (detail.Decision == nil || detail.Effect != nil) || (proposal.Status == domain.CheckRepairPending || proposal.Status == domain.CheckRepairStale) && (detail.Decision != nil || detail.Effect != nil) {
		return domain.CheckRepairDetail{}, storageFailure("validate check repair lifecycle", errors.New("decision/effect cardinality differs from proposal status"))
	}
	return detail, nil
}

func checkRepairProposalFromRow(row dbgen.CheckRepairProposal) (domain.CheckRepairProposal, error) {
	proposal := domain.CheckRepairProposal{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, ObjectiveID: row.ObjectiveID, ObjectiveRevision: row.ObjectiveRevision,
		TaskID: row.TaskID, TaskRevision: row.TaskRevision, RequirementID: row.RequirementID, RequirementRevision: row.RequirementRevision,
		CheckResultID: row.CheckResultID, FreshnessRevision: row.FreshnessRevision, SourceRepositoryID: row.SourceRepositoryID,
		SourceCheckoutID: row.SourceCheckoutID, SourceHeadCommit: row.SourceHeadCommit, PolicyRevision: row.PolicyRevision,
		RepairLaunchProfileID: row.RepairLaunchProfileID, RepairLaunchProfileRevision: row.RepairLaunchProfileRevision,
		SourceRunID: row.SourceRunID, SourceAgentID: row.SourceAgentID, SourceAgentRevision: row.SourceAgentRevision,
		SourceGrantID: row.SourceGrantID, SourceGrantRevision: row.SourceGrantRevision, Rationale: row.Rationale,
		RepairTaskTitle: row.RepairTaskTitle, RepairTaskDescription: row.RepairTaskDescription, RepairTaskPriority: int(row.RepairTaskPriority),
		RepairTaskBudget: domain.Budget{TokenLimit: row.RepairBudgetTokens, CostCents: row.RepairBudgetCostCents, TimeSeconds: row.RepairBudgetTimeSeconds},
		Status:           row.Status, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
	}
	recipe := checkRepairRecipe{
		ObjectiveID: proposal.ObjectiveID, ObjectiveRevision: proposal.ObjectiveRevision, SourceTaskID: proposal.TaskID, SourceTaskRevision: proposal.TaskRevision,
		RequirementID: proposal.RequirementID, RequirementRevision: proposal.RequirementRevision, CheckResultID: proposal.CheckResultID,
		Rationale: proposal.Rationale, Title: proposal.RepairTaskTitle, Description: proposal.RepairTaskDescription, Priority: proposal.RepairTaskPriority, Budget: proposal.RepairTaskBudget,
	}
	_, recipeHash, err := canonicalContent(recipe)
	if err != nil || recipeHash != row.RecipeSha256 || !validCheckRepairStatus(proposal.Status) || proposal.Revision < 1 ||
		!canonicalTimestamp(proposal.CreatedAt) || !canonicalTimestamp(proposal.UpdatedAt) ||
		(proposal.Status == domain.CheckRepairPending && (proposal.Revision != 1 || proposal.UpdatedAt != proposal.CreatedAt)) ||
		(proposal.Status != domain.CheckRepairPending && (proposal.Revision != 2 || !timestampAfter(proposal.UpdatedAt, proposal.CreatedAt))) ||
		!validCheckText(proposal.Rationale, 4096) || !validCheckText(proposal.RepairTaskTitle, 256) || !validCheckText(proposal.RepairTaskDescription, 4096) ||
		proposal.RepairTaskPriority < 0 || proposal.RepairTaskPriority > 1000 || proposal.RepairTaskBudget.TokenLimit <= 0 || proposal.RepairTaskBudget.TokenLimit > maximumRepairTaskTokens ||
		proposal.RepairTaskBudget.CostCents <= 0 || proposal.RepairTaskBudget.CostCents > maximumRepairTaskCostCents || proposal.RepairTaskBudget.TimeSeconds <= 0 || proposal.RepairTaskBudget.TimeSeconds > maximumRepairTaskTimeSeconds ||
		proposal.SourceRunID == "" || proposal.SourceAgentID == "" || proposal.SourceAgentRevision < 1 || proposal.SourceGrantID == "" || proposal.SourceGrantRevision < 1 ||
		proposal.CreatedBy != "agent:"+proposal.SourceAgentID ||
		(proposal.Status == domain.CheckRepairPending && proposal.UpdatedBy != proposal.CreatedBy) ||
		((proposal.Status == domain.CheckRepairAccepted || proposal.Status == domain.CheckRepairRejected) && proposal.UpdatedBy != localOwnerActorID) ||
		(proposal.Status == domain.CheckRepairStale && proposal.UpdatedBy != "crewfold-check-worker") {
		return domain.CheckRepairProposal{}, storageFailure("validate check repair proposal", errors.New("proposal recipe, provenance, or lifecycle diverged"))
	}
	return proposal, nil
}

func deriveCheckRepairRecipe(task domain.Task, objective domain.Objective, requirement domain.TaskCheckRequirement, result domain.CheckResult, rationale string) checkRepairRecipe {
	title := truncateCheckUTF8("Repair check: "+task.Title, 256)
	description := fmt.Sprintf("Repair task %s for check requirement %s (%s) after result %s. Rationale: %s", task.ID, requirement.CriterionKey, requirement.Statement, result.ID, rationale)
	description = truncateCheckUTF8(description, 4096)
	budget := domain.Budget{
		TokenLimit:  boundedFiniteRepairBudget(task.Budget.TokenLimit, maximumRepairTaskTokens),
		CostCents:   boundedFiniteRepairBudget(task.Budget.CostCents, maximumRepairTaskCostCents),
		TimeSeconds: boundedFiniteRepairBudget(task.Budget.TimeSeconds, maximumRepairTaskTimeSeconds),
	}
	return checkRepairRecipe{
		ObjectiveID: objective.ID, ObjectiveRevision: objective.Revision, SourceTaskID: task.ID, SourceTaskRevision: task.Revision,
		RequirementID: requirement.ID, RequirementRevision: requirement.Revision, CheckResultID: result.ID, Rationale: rationale,
		Title: title, Description: description, Priority: task.Priority, Budget: budget,
	}
}

func boundedFiniteRepairBudget(source, maximum int64) int64 {
	if source <= 0 || source > maximum {
		return maximum
	}
	return source
}

func truncateCheckUTF8(value string, maximumBytes int) string {
	if len(value) <= maximumBytes {
		return value
	}
	value = value[:maximumBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func validCheckRepairStatus(value string) bool {
	return value == domain.CheckRepairPending || value == domain.CheckRepairAccepted || value == domain.CheckRepairRejected || value == domain.CheckRepairStale
}

func (s *Store) markCheckRepairsStale(ctx context.Context, tx *sql.Tx, workspaceID, resultID, requirementID string, requirementRevision int64, includeCurrent bool, now, correlationID string) (int, error) {
	queries := dbgen.New(tx)
	var rows []dbgen.MarkPendingCheckRepairsStaleRow
	var err error
	err = s.withCheckMutationSeal(func() error {
		if includeCurrent {
			rows, err = queries.MarkPendingCheckRepairsStale(ctx, dbgen.MarkPendingCheckRepairsStaleParams{UpdatedAt: now, CheckResultID: resultID})
			return err
		}
		var superseded []dbgen.MarkSupersededCheckRepairsStaleRow
		superseded, err = queries.MarkSupersededCheckRepairsStale(ctx, dbgen.MarkSupersededCheckRepairsStaleParams{UpdatedAt: now, RequirementID: requirementID, RequirementRevision: requirementRevision, CurrentCheckResultID: resultID})
		if err != nil {
			return err
		}
		rows = make([]dbgen.MarkPendingCheckRepairsStaleRow, 0, len(superseded))
		for _, row := range superseded {
			rows = append(rows, dbgen.MarkPendingCheckRepairsStaleRow{ID: row.ID, Revision: row.Revision})
		}
		return nil
	})
	if err != nil {
		return 0, checkConstraint("mark obsolete check repairs stale", CodeCheckRepairConflict, err)
	}
	for _, row := range rows {
		if _, err := appendEventForActor(ctx, tx, workspaceID, "check_repair_proposal", row.ID, row.Revision, checkRepairStaleEvent, correlationID, now, "crewfold-check-worker", "subsystem", map[string]any{"check_result_id": resultID, "reason": "source check result is no longer current and fresh"}); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}
