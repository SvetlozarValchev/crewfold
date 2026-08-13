package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func (s *Store) CreateCheckWatchGrant(ctx context.Context, command CreateCheckWatchGrantCommand) (MutationResult[domain.CheckWatchGrant], error) {
	command.WorkspaceIdentifier, command.ProjectIdentifier, command.AgentIdentifier, command.ExpiresAt, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.ProjectIdentifier), strings.TrimSpace(command.AgentIdentifier), strings.TrimSpace(command.ExpiresAt), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	operations, err := canonicalCheckOperations(command.Operations)
	if err != nil || command.ExpectedAgentRevision < 1 || command.MaxPending < 1 || command.MaxPending > 100 || command.MaxInFlight < 1 || command.MaxInFlight > command.MaxPending || len(command.Definitions) < 1 || len(command.Definitions) > 64 {
		return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "check-watch grant requires exact bounded operations, definitions, agent revision, and concurrency")
	}
	if command.ExpiresAt != "" && !canonicalTimestamp(command.ExpiresAt) {
		return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "grant expiry must be canonical UTC")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckWatchGrant); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	command.Operations = operations
	hash, err := checkSemanticHash("check.grant.create", command)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, storageFailure("hash check grant", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, storageFailure("begin check grant", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckWatchGrant]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.grant.create", hash, &replay); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, command.AgentIdentifier)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	if !agent.Enabled || agent.Revision != command.ExpectedAgentRevision {
		return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "grant agent revision is not exact and enabled")
	}
	if command.ExpiresAt != "" {
		expiry, _ := time.Parse(time.RFC3339Nano, command.ExpiresAt)
		if !expiry.After(s.clock().UTC()) {
			return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "grant expiry must be in the future")
		}
	}
	queries := dbgen.New(tx)
	definitions := make([]domain.CheckWatchGrantDefinition, 0, len(command.Definitions))
	seen := map[string]bool{}
	for _, allowed := range command.Definitions {
		definition, err := queryCheckDefinition(ctx, queries, workspace.ID, strings.TrimSpace(allowed.DefinitionID))
		if err != nil {
			return MutationResult[domain.CheckWatchGrant]{}, err
		}
		if definition.ProjectID != project.ID || definition.Status != domain.CheckDefinitionActive || definition.ContentRevision != allowed.ContentRevision || seen[definition.ID] {
			return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "grant definitions must be unique exact active project definitions")
		}
		seen[definition.ID] = true
		definitions = append(definitions, domain.CheckWatchGrantDefinition{DefinitionID: definition.ID, ContentRevision: definition.ContentRevision, DefinitionSHA256: definition.ContentSHA256})
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].DefinitionID < definitions[j].DefinitionID })
	id, _ := randomID("checkgrant_")
	content := checkWatchGrantContent{WorkspaceID: workspace.ID, ProjectID: project.ID, AgentID: agent.ID, AgentRevision: agent.Revision, Operations: operations, Definitions: definitions, MaxPending: command.MaxPending, MaxInFlight: command.MaxInFlight, ExpiresAt: command.ExpiresAt}
	contentJSON, contentHash, err := canonicalContent(content)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, storageFailure("seal check grant", err)
	}
	operationsJSON, _ := json.Marshal(operations)
	definitionsJSON, _ := json.Marshal(definitions)
	now := s.nowText()
	grant := domain.CheckWatchGrant{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, AgentID: agent.ID, AgentRevision: agent.Revision, Operations: operations, Definitions: definitions, MaxPending: command.MaxPending, MaxInFlight: command.MaxInFlight, ExpiresAt: command.ExpiresAt, ContentSHA256: contentHash, Status: domain.CheckWatchGrantActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	err = s.withCheckMutationSeal(func() error {
		for ordinal, operation := range operations {
			if err := queries.InsertCheckWatchGrantOperation(ctx, dbgen.InsertCheckWatchGrantOperationParams{GrantID: id, Ordinal: int64(ordinal), Operation: operation}); err != nil {
				return err
			}
		}
		for ordinal, definition := range definitions {
			if err := queries.InsertCheckWatchGrantDefinition(ctx, dbgen.InsertCheckWatchGrantDefinitionParams{GrantID: id, Ordinal: int64(ordinal), DefinitionID: definition.DefinitionID, ContentRevision: definition.ContentRevision, DefinitionSha256: definition.DefinitionSHA256}); err != nil {
				return err
			}
		}
		return queries.InsertCheckWatchGrant(ctx, dbgen.InsertCheckWatchGrantParams{ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, AgentID: agent.ID, AgentRevision: agent.Revision, OperationsJson: string(operationsJSON), DefinitionsJson: string(definitionsJSON), MaxPending: int64(command.MaxPending), MaxInFlight: int64(command.MaxInFlight), ExpiresAt: command.ExpiresAt, ContentJson: string(contentJSON), ContentSha256: contentHash, CreatedAt: now})
	})
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, checkConstraint("insert check-watch grant", CodeInvalidCheckWatchGrant, err)
	}
	seq, err := appendEvent(ctx, tx, workspace.ID, "check_watch_grant", id, 1, checkGrantCreatedEvent, command.CorrelationID, now, map[string]any{"project_id": project.ID, "agent_id": agent.ID, "content_sha256": contentHash})
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	result := MutationResult[domain.CheckWatchGrant]{Value: grant, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.grant.create", hash, result, now); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, storageFailure("commit check grant", err)
	}
	return result, nil
}

func (s *Store) RevokeCheckWatchGrant(ctx context.Context, command RevokeCheckWatchGrantCommand) (MutationResult[domain.CheckWatchGrant], error) {
	command.WorkspaceIdentifier, command.CheckWatchGrantID, command.Reason, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.CheckWatchGrantID), strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || !validCheckText(command.Reason, 2048) {
		return MutationResult[domain.CheckWatchGrant]{}, checkError(CodeInvalidCheckWatchGrant, "grant revocation requires revision and reason")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckWatchGrant); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	hash, _ := checkSemanticHash("check.grant.revoke", command)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckWatchGrant]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.grant.revoke", hash, &replay); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	queries := dbgen.New(tx)
	grant, err := queryCheckWatchGrant(ctx, queries, workspace.ID, command.CheckWatchGrantID)
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	if grant.Revision != command.ExpectedRevision {
		return MutationResult[domain.CheckWatchGrant]{}, revisionConflict("check-watch grant", grant.ID, command.ExpectedRevision, grant.Revision)
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.RevokeCheckWatchGrant(ctx, dbgen.RevokeCheckWatchGrantParams{UpdatedAt: now, ID: grant.ID, WorkspaceID: workspace.ID, ExpectedRevision: command.ExpectedRevision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("grant lost active revision")
		}
		return nil
	})
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, checkConstraint("revoke check grant", CodeCheckWatchGrantDenied, err)
	}
	grant.Status, grant.Revision, grant.UpdatedAt = domain.CheckWatchGrantRevoked, grant.Revision+1, now
	seq, err := appendEvent(ctx, tx, workspace.ID, "check_watch_grant", grant.ID, grant.Revision, checkGrantRevokedEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	result := MutationResult[domain.CheckWatchGrant]{Value: grant, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.grant.revoke", hash, result, now); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckWatchGrant]{}, err
	}
	return result, nil
}

func (s *Store) CheckWatchGrant(ctx context.Context, workspaceIdentifier, id string) (domain.CheckWatchGrant, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.CheckWatchGrant{}, err
	}
	return queryCheckWatchGrant(ctx, dbgen.New(s.db), workspace.ID, strings.TrimSpace(id))
}
func (s *Store) CheckWatchGrants(ctx context.Context, query ListCheckWatchGrantsQuery) ([]domain.CheckWatchGrant, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(query.ProjectIdentifier)
	if projectID != "" {
		project, err := queryProject(ctx, s.db, workspace.ID, projectID)
		if err != nil {
			return nil, err
		}
		projectID = project.ID
	}
	agentID := strings.TrimSpace(query.AgentIdentifier)
	if agentID != "" {
		agent, err := queryAgent(ctx, s.db, workspace.ID, agentID)
		if err != nil {
			return nil, err
		}
		agentID = agent.ID
	}
	queries := dbgen.New(s.db)
	ids, err := queries.ListCheckWatchGrantIDs(ctx, dbgen.ListCheckWatchGrantIDsParams{WorkspaceID: workspace.ID, ProjectID: projectID, AgentID: agentID, Status: strings.TrimSpace(query.Status), ResultLimit: int64(boundedCheckLimit(query.Limit))})
	if err != nil {
		return nil, storageFailure("list check grants", err)
	}
	result := make([]domain.CheckWatchGrant, 0, len(ids))
	for _, id := range ids {
		grant, err := queryCheckWatchGrant(ctx, queries, workspace.ID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, nil
}

func queryCheckWatchGrant(ctx context.Context, queries *dbgen.Queries, workspaceID, id string) (domain.CheckWatchGrant, error) {
	row, err := queries.GetCheckWatchGrant(ctx, dbgen.GetCheckWatchGrantParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CheckWatchGrant{}, checkError(CodeCheckWatchGrantNotFound, "check-watch grant was not found")
	}
	if err != nil {
		return domain.CheckWatchGrant{}, storageFailure("read check grant", err)
	}
	operations, err := queries.ListCheckWatchGrantOperations(ctx, id)
	if err != nil {
		return domain.CheckWatchGrant{}, storageFailure("read check grant operations", err)
	}
	definitionRows, err := queries.ListCheckWatchGrantDefinitions(ctx, id)
	if err != nil {
		return domain.CheckWatchGrant{}, storageFailure("read check grant definitions", err)
	}
	definitions := make([]domain.CheckWatchGrantDefinition, 0, len(definitionRows))
	for _, definition := range definitionRows {
		definitions = append(definitions, domain.CheckWatchGrantDefinition{DefinitionID: definition.DefinitionID, ContentRevision: definition.DefinitionContentRevision, DefinitionSHA256: definition.DefinitionSha256})
	}
	grant := domain.CheckWatchGrant{ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, AgentID: row.AgentID, AgentRevision: row.AgentRevision, Operations: operations, Definitions: definitions, MaxPending: int(row.MaxPending), MaxInFlight: int(row.MaxInFlight), ExpiresAt: stringValue(row.ExpiresAt), ContentSHA256: row.ContentSha256, Status: row.Status, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}
	_, hash, hashErr := canonicalContent(checkWatchGrantContent{WorkspaceID: grant.WorkspaceID, ProjectID: grant.ProjectID, AgentID: grant.AgentID, AgentRevision: grant.AgentRevision, Operations: grant.Operations, Definitions: grant.Definitions, MaxPending: grant.MaxPending, MaxInFlight: grant.MaxInFlight, ExpiresAt: grant.ExpiresAt})
	if hashErr != nil || hash != grant.ContentSHA256 || grant.Revision < 1 || (grant.Status != domain.CheckWatchGrantActive && grant.Status != domain.CheckWatchGrantRevoked && grant.Status != domain.CheckWatchGrantExpired) || grant.MaxInFlight < 1 || grant.MaxInFlight > grant.MaxPending {
		return domain.CheckWatchGrant{}, storageFailure("validate canonical check-watch grant", errors.New("grant content, hash, or lifecycle diverged"))
	}
	return grant, nil
}

func canonicalCheckOperations(values []string) ([]string, error) {
	rank := map[string]int{domain.CheckWatchOperationRun: 0, domain.CheckWatchOperationInspect: 1, domain.CheckWatchOperationProposeRepair: 2}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := rank[value]; !ok || seen[value] {
			return nil, checkError(CodeInvalidCheckWatchGrant, "check-watch operations must be unique closed values")
		}
		seen[value] = true
		result = append(result, value)
	}
	if len(result) < 1 || len(result) > 3 {
		return nil, checkError(CodeInvalidCheckWatchGrant, "check-watch grant requires 1 to 3 operations")
	}
	sort.Slice(result, func(i, j int) bool { return rank[result[i]] < rank[result[j]] })
	return result, nil
}

func (s *Store) CheckPolicy(ctx context.Context, workspaceIdentifier, projectIdentifier string) (domain.CheckPolicy, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.CheckPolicy{}, err
	}
	project, err := queryProject(ctx, s.db, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return domain.CheckPolicy{}, err
	}
	return queryCheckPolicy(ctx, dbgen.New(s.db), workspace.ID, project.ID)
}
func queryCheckPolicy(ctx context.Context, queries *dbgen.Queries, workspaceID, projectID string) (domain.CheckPolicy, error) {
	row, err := queries.GetCheckPolicy(ctx, dbgen.GetCheckPolicyParams{WorkspaceID: workspaceID, ProjectID: projectID})
	if err != nil {
		return domain.CheckPolicy{}, storageFailure("read check policy", err)
	}
	return domain.CheckPolicy{WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, RepairProposalsEnabled: row.RepairProposalsEnabled != 0, RepairLaunchProfileID: stringValue(row.RepairLaunchProfileID), RepairLaunchProfileRevision: int64Value(row.RepairLaunchProfileRevision), MaxOpenRepairProposals: int(row.MaxOpenRepairProposals), Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy}, nil
}
func (s *Store) ConfigureCheckPolicy(ctx context.Context, command ConfigureCheckPolicyCommand) (MutationResult[domain.CheckPolicy], error) {
	command.WorkspaceIdentifier, command.ProjectIdentifier, command.RepairLaunchProfileID, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.ProjectIdentifier), strings.TrimSpace(command.RepairLaunchProfileID), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.ExpectedRevision < 1 || command.MaxOpenRepairProposals < 1 || command.MaxOpenRepairProposals > 32 || (command.RepairProposalsEnabled != (command.RepairLaunchProfileID != "" && command.RepairLaunchProfileRevision > 0)) {
		return MutationResult[domain.CheckPolicy]{}, checkError(CodeInvalidCheckPolicy, "check policy requires coherent repair profile and bounded limit")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidCheckPolicy); err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	hash, _ := checkSemanticHash("check.policy.configure", command)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	defer tx.Rollback()
	var replay MutationResult[domain.CheckPolicy]
	idempotencyKey := ownerCheckIdempotencyKey(command.IdempotencyKey)
	if found, err := lookupIdempotency(ctx, tx, idempotencyKey, "check.policy.configure", hash, &replay); err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	queries := dbgen.New(tx)
	policy, err := queryCheckPolicy(ctx, queries, workspace.ID, project.ID)
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	if policy.Revision != command.ExpectedRevision {
		return MutationResult[domain.CheckPolicy]{}, revisionConflict("check policy", project.ID, command.ExpectedRevision, policy.Revision)
	}
	if command.RepairProposalsEnabled {
		profile, err := queryLaunchProfile(ctx, tx, workspace.ID, command.RepairLaunchProfileID)
		if err != nil || profile.ProjectID != project.ID || profile.Revision != command.RepairLaunchProfileRevision || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" {
			return MutationResult[domain.CheckPolicy]{}, checkError(CodeInvalidCheckPolicy, "repair profile must be the exact active project profile and cannot be manager-grant-bound")
		}
	}
	now := s.nowText()
	err = s.withCheckMutationSeal(func() error {
		affected, err := queries.UpdateCheckPolicy(ctx, dbgen.UpdateCheckPolicyParams{Enabled: boolInteger(command.RepairProposalsEnabled), ProfileID: command.RepairLaunchProfileID, ProfileRevision: command.RepairLaunchProfileRevision, MaxOpen: int64(command.MaxOpenRepairProposals), UpdatedAt: now, WorkspaceID: workspace.ID, ProjectID: project.ID, ExpectedRevision: command.ExpectedRevision})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("policy revision changed")
		}
		return nil
	})
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, checkConstraint("configure check policy", CodeInvalidCheckPolicy, err)
	}
	policy.RepairProposalsEnabled = command.RepairProposalsEnabled
	policy.RepairLaunchProfileID = command.RepairLaunchProfileID
	policy.RepairLaunchProfileRevision = command.RepairLaunchProfileRevision
	policy.MaxOpenRepairProposals = command.MaxOpenRepairProposals
	policy.Revision++
	policy.UpdatedAt = now
	seq, err := appendEvent(ctx, tx, workspace.ID, "check_policy", project.ID, policy.Revision, checkPolicyConfiguredEvent, command.CorrelationID, now, map[string]any{"project_id": project.ID, "repair_proposals_enabled": policy.RepairProposalsEnabled})
	if err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	result := MutationResult[domain.CheckPolicy]{Value: policy, EventSequence: seq}
	if err := recordIdempotency(ctx, tx, idempotencyKey, "check.policy.configure", hash, result, now); err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckPolicy]{}, err
	}
	return result, nil
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
