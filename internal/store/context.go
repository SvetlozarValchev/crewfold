package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	contextPacketBuiltEvent      = "context.packet_built"
	runReportReceivedEvent       = "run.report_received"
	runArtifactPublished         = "run.artifact_published"
	runToolCalledEvent           = "run.tool_called"
	runToolDeniedEvent           = "run.tool_denied"
	maximumContextBytes          = 32 * 1024
	maximumContextKnowledgeBytes = 12 * 1024
	maximumContextKnowledgeItems = 16
	maximumContextDependents     = 32
	maximumContextThreads        = 8
	maximumContextThreadBytes    = 8 * 1024
	maximumContextDeltaBytes     = 16 * 1024
	maximumContextDeltaTotal     = 64 * 1024
	maximumContextDeltaEvents    = 1000
	maximumArtifactBytes         = 32 * 1024
)

var runScopedTools = []string{
	"crewfold_acknowledge_context_delta",
	"crewfold_acknowledge_message",
	"crewfold_get_briefing",
	"crewfold_get_context_delta",
	"crewfold_get_status",
	"crewfold_list_inbox",
	"crewfold_propose_knowledge",
	"crewfold_propose_completion",
	"crewfold_publish_artifact",
	"crewfold_read_message",
	"crewfold_report_blocked",
	"crewfold_report_contradiction",
	"crewfold_report_progress",
	"crewfold_send_message",
}

var managerProposalTools = []struct {
	kind string
	tool string
}{
	{kind: domain.ManagerProposalAssignment, tool: "crewfold_propose_assignment"},
	{kind: domain.ManagerProposalEscalation, tool: "crewfold_propose_escalation"},
	{kind: domain.ManagerProposalReview, tool: "crewfold_propose_review"},
	{kind: domain.ManagerProposalTaskDecomposition, tool: "crewfold_propose_tasks"},
}

func containsContextString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Store) BuildContextPacket(ctx context.Context, command BuildContextCommand) (MutationResult[domain.ContextPacket], error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	taskID := strings.TrimSpace(command.TaskID)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	checkoutIdentifier := strings.TrimSpace(command.CheckoutIdentifier)
	key, correlationID := strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || taskID == "" || agentIdentifier == "" || command.ExpectedTaskRevision < 1 {
		return MutationResult[domain.ContextPacket]{}, &Error{Code: CodeInvalidContext, Message: "context build requires workspace, task, agent, and expected task revision"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidContext); err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	knowledgeRevisionIDs, err := normalizeContextKnowledgeRevisionIDs(command.KnowledgeRevisionIDs)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	requestHash, err := hashCommand("context.build", map[string]any{
		"workspace": workspaceIdentifier, "task": taskID, "agent": agentIdentifier,
		"checkout": checkoutIdentifier, "knowledge_revision_ids": knowledgeRevisionIDs,
		"expected_task_revision": command.ExpectedTaskRevision,
	})
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, storageFailure("hash context build", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, storageFailure("begin context build", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ContextPacket]
	if found, err := lookupIdempotency(ctx, tx, key, "context.build", requestHash, &replay); err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, taskID)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	if task.Revision != command.ExpectedTaskRevision {
		return MutationResult[domain.ContextPacket]{}, revisionConflict("task", task.ID, command.ExpectedTaskRevision, task.Revision)
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	if task.Status != domain.TaskAssigned || task.AssignmentID == "" || task.AssignedAgentID != agent.ID {
		return MutationResult[domain.ContextPacket]{}, &Error{Code: CodeInvalidContext, Message: "context packet requires the task's current assigned agent"}
	}
	if !agent.Enabled {
		return MutationResult[domain.ContextPacket]{}, &Error{Code: CodeInvalidContext, Message: "context packet cannot target a disabled agent"}
	}
	checkout, err := selectRunCheckout(ctx, tx, task.ProjectID, checkoutIdentifier)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	now := s.nowText()
	packet, sequence, err := s.buildContextPacketWithKnowledgeInTransaction(ctx, tx, workspace.ID, task, agent, checkout, knowledgeRevisionIDs, correlationID, now)
	if err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	result := MutationResult[domain.ContextPacket]{Value: packet, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, key, "context.build", requestHash, result, now); err != nil {
		return MutationResult[domain.ContextPacket]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ContextPacket]{}, storageFailure("commit context build", err)
	}
	return result, nil
}

func (s *Store) buildContextPacketInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, correlationID, now string) (domain.ContextPacket, int64, error) {
	return s.buildContextPacketWithKnowledgeInTransaction(ctx, tx, workspaceID, task, agent, checkout, nil, correlationID, now)
}

func (s *Store) buildManagerContextPacketInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, grant domain.ManagerGrant, profile domain.LaunchProfile, correlationID, now string) (domain.ContextPacket, int64, error) {
	if grant.Status != domain.ManagerGrantActive || profile.Status != domain.LaunchProfileActive ||
		grant.WorkspaceID != workspaceID || grant.ProjectID != task.ProjectID || grant.ObjectiveID != task.ObjectiveID ||
		grant.TaskID != task.ID || grant.TaskRevision != task.Revision || grant.AgentID != agent.ID || grant.AgentRevision != agent.Revision ||
		profile.WorkspaceID != workspaceID || profile.ProjectID != task.ProjectID || profile.AgentID != agent.ID ||
		profile.AgentRevision != agent.Revision || profile.ManagerGrantID != grant.ID {
		return domain.ContextPacket{}, 0, &Error{Code: CodeInvalidManagerGrant, Message: "manager packet requires the exact current planning task, grant, profile, and agent revisions"}
	}
	objective, err := queryObjective(ctx, tx, workspaceID, task.ObjectiveID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	if objective.Status != domain.ObjectiveActive || objective.Revision != grant.ObjectiveRevision {
		return domain.ContextPacket{}, 0, &Error{Code: CodeInvalidManagerGrant, Message: "manager packet objective differs from the exact frozen grant revision"}
	}
	launchProfiles := make([]domain.ContextManagerLaunchProfile, len(grant.LaunchProfiles))
	for index, target := range grant.LaunchProfiles {
		launchProfiles[index] = domain.ContextManagerLaunchProfile{
			LaunchProfileID: target.LaunchProfileID, Revision: target.Revision,
			AgentID: target.AgentID, AgentRevision: target.AgentRevision,
		}
	}
	managementGrant := &domain.ContextManagerGrant{
		Schema: domain.ContextManagerGrantSchema, GrantID: grant.ID, GrantRevision: grant.Revision,
		WorkspaceID: workspaceID, ProjectID: grant.ProjectID, ObjectiveID: grant.ObjectiveID, ObjectiveRevision: grant.ObjectiveRevision,
		ManagerAgentID: grant.AgentID, ManagerAgentRevision: grant.AgentRevision,
		ManagerTaskID: grant.TaskID, ManagerTaskRevision: grant.TaskRevision,
		AllowedProposalKinds: append([]string(nil), grant.ProposalKinds...), LaunchProfiles: launchProfiles,
		// Preserve an authorized empty set as JSON [] rather than null. The packet-v5
		// contract requires an array, and SQL validates it against the grant's
		// canonical [] mirror.
		AllowedClaimKinds: append([]string{}, grant.AllowedClaimKinds...), MaxOpenProposals: grant.Limits.MaxOpenProposals,
		MaxActions: grant.Limits.MaxActions, MaxTasks: grant.Limits.MaxTasks, MaxDependencies: grant.Limits.MaxDependencies,
		MaxClaimRequirements: grant.Limits.MaxClaimRequirements, Budget: grant.Limits.Budget, ExpiresAt: grant.ExpiresAt,
	}
	return s.buildContextPacketWithAuthorityInTransaction(ctx, tx, workspaceID, task, agent, checkout, nil, managementGrant, correlationID, now)
}

func (s *Store) buildContextPacketWithKnowledgeInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, knowledgeRevisionIDs []string, correlationID, now string) (domain.ContextPacket, int64, error) {
	return s.buildContextPacketWithAuthorityInTransaction(ctx, tx, workspaceID, task, agent, checkout, knowledgeRevisionIDs, nil, correlationID, now)
}

func (s *Store) buildContextPacketWithAuthorityInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, knowledgeRevisionIDs []string, managementGrant *domain.ContextManagerGrant, correlationID, now string) (domain.ContextPacket, int64, error) {
	project, err := queryProject(ctx, tx, workspaceID, task.ProjectID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	var repositoryFingerprint string
	repositoryFingerprint, err = dbgen.New(tx).GetContextRepositoryFingerprint(ctx, dbgen.GetContextRepositoryFingerprintParams{ID: checkout.RepositoryID, WorkspaceID: workspaceID})
	if err != nil {
		return domain.ContextPacket{}, 0, storageFailure("query context repository", err)
	}
	dependencies, err := contextDependencies(ctx, tx, task.ID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	dependents, dependentCount, err := contextDependents(ctx, tx, task.ID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	inbox, err := inboxSummaryInTransaction(ctx, tx, agent.ID, task.ProjectID, task.ID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	participantThreads, collaborationSelections, collaborationExclusions, collaborationUsedBytes, err := contextParticipantThreads(ctx, tx, workspaceID, agent.ID, task.ProjectID, task.ID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	var asOfEventSequence int64
	if asOfEventSequence, err = dbgen.New(tx).GetContextEventCursor(ctx); err != nil {
		return domain.ContextPacket{}, 0, storageFailure("read context packet event cursor", err)
	}
	acceptedKnowledge, knowledgeSelections, knowledgeExclusions, knowledgeUsedBytes, err := s.selectContextKnowledgeInTransaction(ctx, tx, workspaceID, task, knowledgeRevisionIDs, now)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	packetID, err := randomID("ctx_")
	if err != nil {
		return domain.ContextPacket{}, 0, storageFailure("generate context packet id", err)
	}
	packetSchema := domain.ContextPacketSchemaV4
	allowedTools := append([]string(nil), runScopedTools...)
	if managementGrant != nil {
		packetSchema = domain.ContextPacketSchemaV5
		for _, candidate := range managerProposalTools {
			if containsContextString(managementGrant.AllowedProposalKinds, candidate.kind) {
				allowedTools = append(allowedTools, candidate.tool)
			}
		}
	}
	packet := domain.ContextPacket{
		Schema: packetSchema, ID: packetID, WorkspaceID: workspaceID,
		ProjectID: task.ProjectID, TaskID: task.ID, AgentID: agent.ID, CheckoutID: checkout.ID,
		Role: domain.ContextRole{AgentID: agent.ID, Name: agent.Name, Role: agent.Role, Provider: agent.Provider, Runtime: agent.Runtime, Revision: agent.Revision},
		Task: domain.ContextTask{TaskID: task.ID, AssignmentID: task.AssignmentID, ObjectiveID: task.ObjectiveID, Title: task.Title, Description: task.Description, Priority: task.Priority, Budget: task.Budget, Revision: task.Revision},
		Checkout: domain.ContextCheckout{
			CheckoutID: checkout.ID, ProjectID: project.ID, ProjectName: project.Name,
			RepositoryID: checkout.RepositoryID, RepositoryFingerprint: repositoryFingerprint,
			Path: checkout.Path, WriteMode: checkout.WriteMode, CheckoutKind: checkout.CheckoutKind,
			Branch: checkout.Branch, HeadCommit: checkout.HeadCommit, Dirty: checkout.Dirty, Revision: checkout.Revision,
		},
		Dependencies: dependencies, Dependents: dependents, DependentTaskCount: dependentCount,
		Inbox: inbox, ParticipantThreads: participantThreads,
		RequestedKnowledgeRevisionIDs: append(make([]string, 0, len(knowledgeRevisionIDs)), knowledgeRevisionIDs...),
		AcceptedKnowledge:             acceptedKnowledge,
		Policy: domain.ContextPolicy{
			AllowedTools:     allowedTools,
			DeniedOperations: []string{"change another run or task", "push or merge source", "deploy", "message a person or broadcast", "read unscoped context"},
			ApprovalRequired: []string{"shared repository mutation", "external side effect", "destructive operation"},
		},
		LiveContext: domain.ContextLivePolicy{
			Schema: domain.ContextLivePolicySchema, Delivery: domain.ContextLiveDeliveryExplicitPull,
			AckAuthority: domain.ContextLiveAckBoundRun, MaxPending: 1,
			MaxRelevantEvents: maximumContextDeltaEvents, PerDeltaLimitBytes: maximumContextDeltaBytes,
			CumulativeDeltaLimitBytes: maximumContextDeltaTotal,
		},
		ManagementGrant: managementGrant,
		Reporting: domain.ContextReporting{
			Progress:   "Report concise completed work, next work, risks, and evidence through crewfold_report_progress.",
			Blocked:    "Stop unsafe work and report the blocking reason and requested resolution through crewfold_report_blocked.",
			Artifact:   "Publish only bounded evidence needed by this run through crewfold_publish_artifact.",
			Completion: "Propose completion with a concise handoff and evidence; Crewfold decides acceptance.",
		},
		Included: []domain.ContextSelection{
			{Section: "role", EntityType: "agent", EntityID: agent.ID, Revision: agent.Revision, Reason: "the task's current assigned agent"},
			{Section: "task", EntityType: "task", EntityID: task.ID, Revision: task.Revision, Reason: "the run's exact task contract"},
			{Section: "checkout", EntityType: "checkout", EntityID: checkout.ID, Revision: checkout.Revision, Reason: "the writable checkout selected for execution"},
		},
		Excluded: []domain.ContextExclusion{
			{Section: "messages", Reason: "full message bodies are excluded; the bounded inbox summary contains only identifiers and previews"},
			{Section: "claims", Reason: "scope claims and overlap detection are not enabled"},
			{Section: "transcripts", Reason: "raw provider transcripts are not context authority"},
		},
		Budget: domain.ContextBudget{
			Total: domain.ContextBudgetUsage{LimitBytes: maximumContextBytes, RemainingBytes: maximumContextBytes},
			Knowledge: domain.ContextBudgetUsage{
				LimitBytes: maximumContextKnowledgeBytes, UsedBytes: knowledgeUsedBytes,
				RemainingBytes: maximumContextKnowledgeBytes - knowledgeUsedBytes,
			},
			Collaboration: domain.ContextBudgetUsage{
				LimitBytes: maximumContextThreadBytes, UsedBytes: collaborationUsedBytes,
				RemainingBytes: maximumContextThreadBytes - collaborationUsedBytes,
			},
		},
		AsOfEventSequence: asOfEventSequence,
		CreatedAt:         now, CreatedBy: localOwnerActorID,
	}
	if len(knowledgeRevisionIDs) == 0 {
		packet.Excluded = append([]domain.ContextExclusion{{Section: "accepted_knowledge", Reason: "no explicit knowledge revision links were requested"}}, packet.Excluded...)
	}
	if managementGrant != nil {
		packet.Included = append(packet.Included, domain.ContextSelection{
			Section: "management_grant", EntityType: "manager_grant", EntityID: managementGrant.GrantID,
			Revision: managementGrant.GrantRevision, Reason: "the exact current owner grant bound to this manager run",
		})
	}
	for _, dependency := range dependencies {
		packet.Included = append(packet.Included, domain.ContextSelection{Section: "dependencies", EntityType: "task", EntityID: dependency.TaskID, Revision: dependency.Revision, Reason: "direct task dependency"})
	}
	for _, dependent := range dependents {
		packet.Included = append(packet.Included, domain.ContextSelection{Section: "dependents", EntityType: "task", EntityID: dependent.TaskID, Revision: dependent.Revision, Reason: "task directly depends on this run's task"})
	}
	if dependentCount > len(dependents) {
		packet.Excluded = append(packet.Excluded, domain.ContextExclusion{Section: "dependents", EntityType: "task", ReasonCode: "over_item_limit",
			Reason: fmt.Sprintf("%d additional direct reverse dependents are omitted by the %d-item limit", dependentCount-len(dependents), maximumContextDependents)})
	}
	packet.Included = append(packet.Included, collaborationSelections...)
	packet.Excluded = append(packet.Excluded, collaborationExclusions...)
	for _, item := range inbox.Items {
		packet.Included = append(packet.Included, domain.ContextSelection{Section: "inbox", EntityType: "message", EntityID: item.MessageID, Revision: 1, Reason: "unseen message addressed to the assigned agent"})
	}
	packet.Included = append(packet.Included, knowledgeSelections...)
	baseExclusionCount := len(packet.Excluded)
	totalBudgetRosterExclusions := make([]domain.ContextExclusion, 0)
	rebuildBoundedExclusions := func() {
		packet.Excluded = append(packet.Excluded[:baseExclusionCount], totalBudgetRosterExclusions...)
		packet.Excluded = append(packet.Excluded, orderedContextKnowledgeExclusions(knowledgeRevisionIDs, knowledgeExclusions)...)
	}
	rebuildBoundedExclusions()

	var packetJSON []byte
	for {
		packetJSON, err = finalizeContextPacket(&packet)
		if err != nil {
			return domain.ContextPacket{}, 0, err
		}
		if packet.ByteSize <= maximumContextBytes {
			break
		}
		if len(packet.ParticipantThreads) > 0 {
			evicted := packet.ParticipantThreads[len(packet.ParticipantThreads)-1]
			packet.ParticipantThreads = packet.ParticipantThreads[:len(packet.ParticipantThreads)-1]
			for index := len(packet.Included) - 1; index >= 0; index-- {
				if packet.Included[index].Section == "participant_threads" && packet.Included[index].EntityID == evicted.Thread.ID {
					packet.Included = append(packet.Included[:index], packet.Included[index+1:]...)
					break
				}
			}
			encoded, marshalErr := json.Marshal(evicted)
			if marshalErr != nil {
				return domain.ContextPacket{}, 0, storageFailure("encode over-budget context participant roster", marshalErr)
			}
			totalBudgetRosterExclusions = append(totalBudgetRosterExclusions, domain.ContextExclusion{Section: "participant_threads", EntityType: "thread", EntityID: evicted.Thread.ID, Revision: evicted.ParticipantRevision, ReasonCode: "over_budget", Reason: "the complete participant roster would exceed the context packet byte budget", ByteSize: len(encoded)})
			rebuildBoundedExclusions()
			collaborationUsedBytes -= len(encoded)
			packet.Budget.Collaboration.UsedBytes = collaborationUsedBytes
			packet.Budget.Collaboration.RemainingBytes = maximumContextThreadBytes - collaborationUsedBytes
			continue
		}
		if len(packet.AcceptedKnowledge) == 0 {
			return domain.ContextPacket{}, 0, &Error{Code: CodeInvalidContext, Message: "context packet base content exceeds its bounded size"}
		}
		evicted := packet.AcceptedKnowledge[len(packet.AcceptedKnowledge)-1]
		packet.AcceptedKnowledge = packet.AcceptedKnowledge[:len(packet.AcceptedKnowledge)-1]
		for index := len(packet.Included) - 1; index >= 0; index-- {
			if packet.Included[index].Section == "accepted_knowledge" && packet.Included[index].EntityID == evicted.ID {
				packet.Included = append(packet.Included[:index], packet.Included[index+1:]...)
				break
			}
		}
		encoded, marshalErr := json.Marshal(evicted)
		if marshalErr != nil {
			return domain.ContextPacket{}, 0, storageFailure("encode over-budget context knowledge", marshalErr)
		}
		knowledgeExclusions[evicted.ID] = contextKnowledgeExclusion(evicted, "over_budget", "the complete revision would exceed the context packet byte budget", len(encoded))
		rebuildBoundedExclusions()
		knowledgeUsedBytes, err = contextKnowledgeEncodedBytes(packet.AcceptedKnowledge)
		if err != nil {
			return domain.ContextPacket{}, 0, err
		}
		packet.Budget.Knowledge.UsedBytes = knowledgeUsedBytes
		packet.Budget.Knowledge.RemainingBytes = maximumContextKnowledgeBytes - knowledgeUsedBytes
	}
	if err := dbgen.New(tx).InsertContextPacket(ctx, dbgen.InsertContextPacketParams{
		ID: packet.ID, WorkspaceID: packet.WorkspaceID, ProjectID: packet.ProjectID, TaskID: packet.TaskID,
		AgentID: packet.AgentID, CheckoutID: packet.CheckoutID, PacketJson: string(packetJSON),
		ContentHash: packet.ContentHash, ByteSize: int64(packet.ByteSize), CreatedAt: packet.CreatedAt, CreatedBy: packet.CreatedBy,
	}); err != nil {
		return domain.ContextPacket{}, 0, storageFailure("insert context packet", err)
	}
	sequence, err := appendEvent(ctx, tx, workspaceID, "context_packet", packet.ID, 1, contextPacketBuiltEvent, correlationID, now, map[string]any{
		"task_id": task.ID, "agent_id": agent.ID, "checkout_id": checkout.ID,
		"packet_schema": packet.Schema, "as_of_event_sequence": packet.AsOfEventSequence,
		"content_hash": packet.ContentHash, "byte_size": packet.ByteSize,
	})
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	return packet, sequence, nil
}

func normalizeContextKnowledgeRevisionIDs(values []string) ([]string, error) {
	if len(values) > maximumContextKnowledgeItems {
		return nil, &Error{Code: CodeInvalidContext, Message: fmt.Sprintf("context build accepts at most %d explicit knowledge revisions", maximumContextKnowledgeItems)}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validContextKnowledgeRevisionID(value) {
			return nil, &Error{Code: CodeInvalidContext, Message: "context knowledge revision IDs must use the krev_ identifier form"}
		}
		if _, exists := seen[value]; exists {
			return nil, &Error{Code: CodeInvalidContext, Message: fmt.Sprintf("context knowledge revision %s was requested more than once", value)}
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validContextKnowledgeRevisionID(value string) bool {
	if len(value) != len("krev_")+32 || !strings.HasPrefix(value, "krev_") {
		return false
	}
	for _, character := range value[len("krev_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) selectContextKnowledgeInTransaction(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, revisionIDs []string, nowText string) ([]domain.KnowledgeRevision, []domain.ContextSelection, map[string]domain.ContextExclusion, int, error) {
	eligible := make([]domain.KnowledgeRevision, 0, len(revisionIDs))
	exclusions := make(map[string]domain.ContextExclusion)
	for _, revisionID := range revisionIDs {
		revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, revisionID)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		encoded, err := json.Marshal(revision)
		if err != nil {
			return nil, nil, nil, 0, storageFailure("encode context knowledge revision", err)
		}
		if code, reason, err := contextKnowledgeIneligibility(revision, workspaceID, task, nowText); err != nil {
			return nil, nil, nil, 0, err
		} else if code != "" {
			exclusion := contextKnowledgeExclusion(revision, code, reason, len(encoded))
			if code == "superseded" {
				replacementID, err := s.KnowledgeCurrentSuccessorIDInTransaction(ctx, tx, workspaceID, revision.ID)
				if err != nil {
					return nil, nil, nil, 0, err
				}
				exclusion.ReplacementRevisionID = replacementID
			}
			exclusions[revision.ID] = exclusion
			continue
		}
		eligible = append(eligible, revision)
	}
	eligibleIDs := make([]string, 0, len(eligible))
	for _, revision := range eligible {
		eligibleIDs = append(eligibleIDs, revision.ID)
	}
	if err := s.AssertKnowledgeRevisionsUndisputedInTransaction(ctx, tx, workspaceID, eligibleIDs); err != nil {
		return nil, nil, nil, 0, err
	}
	accepted := make([]domain.KnowledgeRevision, 0, len(eligible))
	selections := make([]domain.ContextSelection, 0, len(eligible))
	for _, revision := range eligible {
		encoded, err := json.Marshal(revision)
		if err != nil {
			return nil, nil, nil, 0, storageFailure("encode context knowledge revision", err)
		}
		prospective := append(append([]domain.KnowledgeRevision(nil), accepted...), revision)
		usedBytes, err := contextKnowledgeEncodedBytes(prospective)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		if usedBytes > maximumContextKnowledgeBytes {
			exclusions[revision.ID] = contextKnowledgeExclusion(revision, "over_budget", "the complete revision would exceed the accepted knowledge byte budget", len(encoded))
			continue
		}
		accepted = prospective
		selections = append(selections, domain.ContextSelection{
			Section: "accepted_knowledge", EntityType: "knowledge_revision", EntityID: revision.ID,
			Revision: revision.RevisionNumber, Reason: "the exact accepted current knowledge revision was explicitly requested",
		})
	}
	usedBytes, err := contextKnowledgeEncodedBytes(accepted)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return accepted, selections, exclusions, usedBytes, nil
}

func contextKnowledgeIneligibility(revision domain.KnowledgeRevision, workspaceID string, task domain.Task, nowText string) (string, string, error) {
	if revision.WorkspaceID != workspaceID || revision.ProjectID != task.ProjectID || (revision.TaskScopeID != "" && revision.TaskScopeID != task.ID) {
		return "out_of_scope", "the requested revision does not apply to this task and project", nil
	}
	switch revision.ReviewStatus {
	case domain.KnowledgeReviewProposed:
		return "proposed", "the requested revision has not been accepted", nil
	case domain.KnowledgeReviewRejected:
		return "rejected", "the requested revision was rejected", nil
	case domain.KnowledgeReviewAccepted:
	default:
		return "", "", storageFailure("classify context knowledge revision", fmt.Errorf("unsupported review status %q", revision.ReviewStatus))
	}
	switch revision.CurrencyStatus {
	case domain.KnowledgeCurrencyStale:
		return "stale", "the requested revision is stale", nil
	case domain.KnowledgeCurrencySuperseded:
		return "superseded", "the requested revision was superseded; an exact successor must be requested explicitly", nil
	case domain.KnowledgeCurrencyCurrent:
	default:
		return "", "", storageFailure("classify context knowledge revision", fmt.Errorf("unsupported accepted currency status %q", revision.CurrencyStatus))
	}
	if revision.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
		freshUntil, err := time.Parse(time.RFC3339Nano, revision.FreshUntil)
		if err != nil {
			return "", "", storageFailure("parse context knowledge freshness", err)
		}
		builtAt, err := time.Parse(time.RFC3339Nano, nowText)
		if err != nil {
			return "", "", storageFailure("parse context build time", err)
		}
		if !builtAt.Before(freshUntil) {
			return "stale", "the requested revision's explicit freshness deadline has passed", nil
		}
	}
	return "", "", nil
}

func contextKnowledgeExclusion(revision domain.KnowledgeRevision, code, reason string, byteSize int) domain.ContextExclusion {
	return domain.ContextExclusion{
		Section: "accepted_knowledge", EntityType: "knowledge_revision", EntityID: revision.ID,
		Revision: revision.RevisionNumber, RequestedRevisionID: revision.ID,
		ReasonCode: code, Reason: reason, ByteSize: byteSize,
	}
}

func orderedContextKnowledgeExclusions(revisionIDs []string, exclusions map[string]domain.ContextExclusion) []domain.ContextExclusion {
	result := make([]domain.ContextExclusion, 0, len(exclusions))
	for _, revisionID := range revisionIDs {
		if exclusion, exists := exclusions[revisionID]; exists {
			result = append(result, exclusion)
		}
	}
	return result
}

func contextKnowledgeEncodedBytes(revisions []domain.KnowledgeRevision) (int, error) {
	if len(revisions) == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(revisions)
	if err != nil {
		return 0, storageFailure("encode accepted context knowledge", err)
	}
	return len(encoded) - 2, nil
}

func finalizeContextPacket(packet *domain.ContextPacket) ([]byte, error) {
	packet.ContentHash = "sha256:" + strings.Repeat("0", 64)
	packet.ByteSize = 0
	packet.Budget.Total = domain.ContextBudgetUsage{LimitBytes: maximumContextBytes, RemainingBytes: maximumContextBytes}
	stable := false
	for range 8 {
		encoded, err := json.Marshal(packet)
		if err != nil {
			return nil, storageFailure("encode context packet", err)
		}
		usedBytes := len(encoded)
		remainingBytes := maximumContextBytes - usedBytes
		if remainingBytes < 0 {
			remainingBytes = 0
		}
		if packet.ByteSize == usedBytes && packet.Budget.Total.UsedBytes == usedBytes && packet.Budget.Total.RemainingBytes == remainingBytes {
			stable = true
			break
		}
		packet.ByteSize = usedBytes
		packet.Budget.Total.UsedBytes = usedBytes
		packet.Budget.Total.RemainingBytes = remainingBytes
	}
	if !stable {
		return nil, storageFailure("encode context packet", errors.New("packet byte accounting did not converge"))
	}

	var err error
	packet.ContentHash, err = contextPacketSemanticHash(*packet)
	if err != nil {
		return nil, err
	}
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		return nil, storageFailure("encode final context packet", err)
	}
	if len(packetJSON) != packet.ByteSize || packet.ByteSize <= 0 {
		return nil, storageFailure("validate context packet byte accounting", errors.New("stored packet size is not stable"))
	}
	return packetJSON, nil
}

func contextDependencies(ctx context.Context, tx *sql.Tx, taskID string) ([]domain.ContextDependency, error) {
	queries := dbgen.New(tx)
	count, err := queries.CountContextDependencies(ctx, taskID)
	if err != nil {
		return nil, storageFailure("count context dependencies", err)
	}
	if count > int64(maximumContextDependents) {
		return nil, &Error{Code: CodeInvalidContext, Message: fmt.Sprintf("context task has %d direct dependencies; maximum is %d", count, maximumContextDependents)}
	}
	rows, err := queries.ListContextDependencies(ctx, taskID)
	if err != nil {
		return nil, storageFailure("query context dependencies", err)
	}
	result := make([]domain.ContextDependency, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.ContextDependency{TaskID: row.ID, Title: row.Title, Status: row.Status, Revision: row.Revision})
	}
	return result, nil
}

func contextDependents(ctx context.Context, tx *sql.Tx, taskID string) ([]domain.ContextDependency, int, error) {
	queries := dbgen.New(tx)
	count, err := queries.CountContextDependents(ctx, taskID)
	if err != nil {
		return nil, 0, storageFailure("count context dependent tasks", err)
	}
	rows, err := queries.ListContextDependents(ctx, dbgen.ListContextDependentsParams{DependsOnTaskID: taskID, Limit: maximumContextDependents})
	if err != nil {
		return nil, 0, storageFailure("query context dependent tasks", err)
	}
	result := make([]domain.ContextDependency, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.ContextDependency{TaskID: row.ID, Title: row.Title, Status: row.Status, Revision: row.Revision})
	}
	return result, int(count), nil
}

func contextParticipantThreads(ctx context.Context, tx *sql.Tx, workspaceID, agentID, projectID, taskID string) ([]domain.ParticipantThread, []domain.ContextSelection, []domain.ContextExclusion, int, error) {
	queries := dbgen.New(tx)
	totalCount64, err := queries.CountContextParticipantThreads(ctx, dbgen.CountContextParticipantThreadsParams{
		WorkspaceID: workspaceID, AgentID: agentID, ProjectID: projectID, TaskID: taskID,
	})
	if err != nil {
		return nil, nil, nil, 0, storageFailure("count context participant threads", err)
	}
	totalCount := int(totalCount64)
	const maximumContextThreadCandidates = 32
	threadIDs, err := queries.ListContextParticipantThreadIDs(ctx, dbgen.ListContextParticipantThreadIDsParams{
		WorkspaceID: workspaceID, AgentID: agentID, ProjectID: projectID, TaskID: taskID, Limit: maximumContextThreadCandidates,
	})
	if err != nil {
		return nil, nil, nil, 0, storageFailure("query context participant threads", err)
	}
	threads := make([]domain.ParticipantThread, 0)
	selections := make([]domain.ContextSelection, 0)
	exclusions := make([]domain.ContextExclusion, 0)
	usedBytes := 0
	for _, threadID := range threadIDs {
		thread, err := participantThreadInTransaction(ctx, queries, workspaceID, threadID)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		encoded, err := json.Marshal(thread)
		if err != nil {
			return nil, nil, nil, 0, storageFailure("encode context participant thread", err)
		}
		if len(threads) >= maximumContextThreads || usedBytes+len(encoded) > maximumContextThreadBytes {
			reasonCode, reason := "over_budget", "the complete participant roster exceeds the bounded collaboration context"
			if len(threads) >= maximumContextThreads {
				reasonCode, reason = "over_item_limit", "the participant thread count exceeds the bounded collaboration context"
			}
			exclusions = append(exclusions, domain.ContextExclusion{Section: "participant_threads", EntityType: "thread", EntityID: thread.Thread.ID, Revision: thread.ParticipantRevision, ReasonCode: reasonCode, Reason: reason, ByteSize: len(encoded)})
			continue
		}
		threads = append(threads, thread)
		usedBytes += len(encoded)
		selections = append(selections, domain.ContextSelection{Section: "participant_threads", EntityType: "thread", EntityID: thread.Thread.ID, Revision: thread.ParticipantRevision, Reason: "exact participant binding for this run's agent, project, and task"})
	}
	if totalCount > len(threadIDs) {
		exclusions = append(exclusions, domain.ContextExclusion{Section: "participant_threads", EntityType: "thread", ReasonCode: "over_item_limit",
			Reason: fmt.Sprintf("%d additional authorized participant threads were not inspected beyond the bounded %d-candidate lookahead", totalCount-len(threadIDs), maximumContextThreadCandidates)})
	}
	return threads, selections, exclusions, usedBytes, nil
}

func (s *Store) ContextPacket(ctx context.Context, workspaceIdentifier, packetID string) (domain.ContextPacket, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ContextPacket{}, err
	}
	return queryContextPacket(ctx, s.db, workspace.ID, strings.TrimSpace(packetID))
}

func (s *Store) ExplainContextPacket(ctx context.Context, workspaceIdentifier, packetID string) (domain.ContextExplanation, error) {
	packet, err := s.ContextPacket(ctx, workspaceIdentifier, packetID)
	if err != nil {
		return domain.ContextExplanation{}, err
	}
	budget := packet.Budget
	if budget.Total.LimitBytes == 0 || budget.Knowledge.LimitBytes == 0 {
		remainingBytes := maximumContextBytes - packet.ByteSize
		if remainingBytes < 0 {
			remainingBytes = 0
		}
		budget = domain.ContextBudget{
			Total:     domain.ContextBudgetUsage{LimitBytes: maximumContextBytes, UsedBytes: packet.ByteSize, RemainingBytes: remainingBytes},
			Knowledge: domain.ContextBudgetUsage{LimitBytes: maximumContextKnowledgeBytes, RemainingBytes: maximumContextKnowledgeBytes},
		}
	}
	return domain.ContextExplanation{PacketID: packet.ID, ContentHash: packet.ContentHash, ByteSize: packet.ByteSize, Included: packet.Included, Excluded: packet.Excluded, Budget: budget}, nil
}

func queryContextPacket(ctx context.Context, database dbgen.DBTX, workspaceID, packetID string) (domain.ContextPacket, error) {
	row, err := dbgen.New(database).GetWorkspaceContextPacket(ctx, dbgen.GetWorkspaceContextPacketParams{ID: packetID, WorkspaceID: workspaceID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContextPacket{}, &Error{Code: CodeContextNotFound, Message: fmt.Sprintf("context packet %q was not found", packetID)}
	}
	if err != nil {
		return domain.ContextPacket{}, storageFailure("query context packet", err)
	}
	var packet domain.ContextPacket
	if err := json.Unmarshal([]byte(row.PacketJson), &packet); err != nil {
		return domain.ContextPacket{}, storageFailure("decode context packet", err)
	}
	if domain.IsLiveContextPacketSchema(packet.Schema) {
		decoder := json.NewDecoder(bytes.NewBufferString(row.PacketJson))
		decoder.DisallowUnknownFields()
		var strict domain.ContextPacket
		if err := decoder.Decode(&strict); err != nil {
			return domain.ContextPacket{}, storageFailure("strictly decode context packet", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return domain.ContextPacket{}, storageFailure("strictly decode context packet", errors.New("packet has trailing JSON content"))
		}
		packet = strict
	}
	if (packet.Schema != domain.ContextPacketSchemaV1 && packet.Schema != domain.ContextPacketSchemaV2 && packet.Schema != domain.ContextPacketSchemaV3 && !domain.IsLiveContextPacketSchema(packet.Schema)) ||
		packet.ID != packetID || packet.WorkspaceID != workspaceID || packet.ProjectID != row.ProjectID || packet.TaskID != row.TaskID ||
		packet.AgentID != row.AgentID || packet.CheckoutID != row.CheckoutID || packet.ContentHash != row.ContentHash ||
		packet.ByteSize != int(row.ByteSize) || packet.ByteSize != len([]byte(row.PacketJson)) {
		return domain.ContextPacket{}, storageFailure("validate context packet", errors.New("stored packet identity or size is invalid"))
	}
	if domain.IsLiveContextPacketSchema(packet.Schema) {
		if packet.AsOfEventSequence < 0 || packet.LiveContext != (domain.ContextLivePolicy{
			Schema: domain.ContextLivePolicySchema, Delivery: domain.ContextLiveDeliveryExplicitPull,
			AckAuthority: domain.ContextLiveAckBoundRun, MaxPending: 1,
			MaxRelevantEvents: maximumContextDeltaEvents, PerDeltaLimitBytes: maximumContextDeltaBytes,
			CumulativeDeltaLimitBytes: maximumContextDeltaTotal,
		}) {
			return domain.ContextPacket{}, storageFailure("validate context packet live policy", errors.New("stored live-context policy is invalid"))
		}
		semanticHash, err := contextPacketSemanticHash(packet)
		if err != nil || semanticHash != packet.ContentHash {
			if err == nil {
				err = errors.New("stored packet semantic hash differs")
			}
			return domain.ContextPacket{}, storageFailure("validate context packet semantic hash", err)
		}
		if err := validateLiveContextPacket(packet); err != nil {
			return domain.ContextPacket{}, storageFailure("validate live context packet", err)
		}
		if packet.Schema == domain.ContextPacketSchemaV5 {
			if err := validateStoredManagerGrantAgainstCanonical(ctx, database, packet); err != nil {
				return domain.ContextPacket{}, storageFailure("validate stored manager grant authority", err)
			}
		}
	}
	return packet, nil
}

// Stored v5 reads prove the immutable grant authority against normalized rows
// without requiring the grant or target profiles to remain active. Current
// lifecycle/expiry checks happen again at invoke, bind, and proposal calls.
func validateStoredManagerGrantAgainstCanonical(ctx context.Context, database queryRower, packet domain.ContextPacket) error {
	snapshot := packet.ManagementGrant
	if snapshot == nil {
		return errors.New("stored manager grant snapshot is missing")
	}
	grant, err := queryManagerGrant(ctx, database, packet.WorkspaceID, snapshot.GrantID)
	if err != nil {
		return err
	}
	if snapshot.GrantRevision < 1 || snapshot.GrantRevision > grant.Revision ||
		grant.WorkspaceID != snapshot.WorkspaceID || grant.ProjectID != snapshot.ProjectID || grant.ObjectiveID != snapshot.ObjectiveID ||
		grant.TaskID != snapshot.ManagerTaskID || grant.TaskRevision != snapshot.ManagerTaskRevision ||
		grant.AgentID != snapshot.ManagerAgentID || grant.AgentRevision != snapshot.ManagerAgentRevision ||
		!reflect.DeepEqual(grant.ProposalKinds, snapshot.AllowedProposalKinds) ||
		!reflect.DeepEqual(grant.AllowedClaimKinds, snapshot.AllowedClaimKinds) ||
		grant.Limits.MaxOpenProposals != snapshot.MaxOpenProposals || grant.Limits.MaxActions != snapshot.MaxActions ||
		grant.Limits.MaxTasks != snapshot.MaxTasks || grant.Limits.MaxDependencies != snapshot.MaxDependencies ||
		grant.Limits.MaxClaimRequirements != snapshot.MaxClaimRequirements || grant.Limits.Budget != snapshot.Budget ||
		grant.ExpiresAt != snapshot.ExpiresAt || len(grant.LaunchProfiles) != len(snapshot.LaunchProfiles) {
		return errors.New("stored manager grant differs from normalized authority")
	}
	for index, frozen := range snapshot.LaunchProfiles {
		current := grant.LaunchProfiles[index]
		if current.LaunchProfileID != frozen.LaunchProfileID || current.Revision != frozen.Revision ||
			current.AgentID != frozen.AgentID || current.AgentRevision != frozen.AgentRevision {
			return errors.New("stored manager launch profile tuple differs from normalized authority")
		}
	}
	return nil
}

func validateVersionFourContextPacket(packet domain.ContextPacket) error {
	if packet.Schema != domain.ContextPacketSchemaV4 || packet.ManagementGrant != nil {
		return errors.New("version-four packet cannot carry manager authority")
	}
	return validateLiveContextPacketBase(packet, runScopedTools)
}

func validateLiveContextPacket(packet domain.ContextPacket) error {
	switch packet.Schema {
	case domain.ContextPacketSchemaV4:
		return validateVersionFourContextPacket(packet)
	case domain.ContextPacketSchemaV5:
		if err := validateContextManagerGrant(packet); err != nil {
			return err
		}
		return validateLiveContextPacketBase(packet, managerAllowedTools(packet.ManagementGrant.AllowedProposalKinds))
	default:
		return errors.New("packet does not use a live-context schema")
	}
}

func managerAllowedTools(proposalKinds []string) []string {
	result := append([]string(nil), runScopedTools...)
	for _, candidate := range managerProposalTools {
		if containsContextString(proposalKinds, candidate.kind) {
			result = append(result, candidate.tool)
		}
	}
	return result
}

func validateLiveContextPacketBase(packet domain.ContextPacket, expectedTools []string) error {
	if packet.Role.AgentID != packet.AgentID || packet.Task.TaskID != packet.TaskID || packet.Task.AssignmentID == "" ||
		packet.Checkout.CheckoutID != packet.CheckoutID || packet.Checkout.ProjectID != packet.ProjectID ||
		len(packet.Dependencies) > maximumContextDependents || len(packet.Dependents) > maximumContextDependents ||
		packet.DependentTaskCount < len(packet.Dependents) || len(packet.ParticipantThreads) > maximumContextThreads ||
		len(packet.Inbox.Items) > 10 || len(packet.RequestedKnowledgeRevisionIDs) > maximumContextKnowledgeItems ||
		len(packet.AcceptedKnowledge) > maximumContextKnowledgeItems || len(packet.Included) > 128 || len(packet.Excluded) > 128 ||
		packet.ByteSize <= 0 || packet.ByteSize > maximumContextBytes || !reflect.DeepEqual(packet.Policy.AllowedTools, expectedTools) ||
		!reflect.DeepEqual(packet.Policy.DeniedOperations, []string{"change another run or task", "push or merge source", "deploy", "message a person or broadcast", "read unscoped context"}) ||
		!reflect.DeepEqual(packet.Policy.ApprovalRequired, []string{"shared repository mutation", "external side effect", "destructive operation"}) ||
		packet.Reporting != (domain.ContextReporting{
			Progress:   "Report concise completed work, next work, risks, and evidence through crewfold_report_progress.",
			Blocked:    "Stop unsafe work and report the blocking reason and requested resolution through crewfold_report_blocked.",
			Artifact:   "Publish only bounded evidence needed by this run through crewfold_publish_artifact.",
			Completion: "Propose completion with a concise handoff and evidence; Crewfold decides acceptance.",
		}) {
		return errors.New("packet binding, relation bounds, or immutable tool policy is invalid")
	}
	for _, relations := range [][]domain.ContextDependency{packet.Dependencies, packet.Dependents} {
		for index, relation := range relations {
			if relation.TaskID == "" || relation.Revision < 1 || index > 0 && relations[index-1].TaskID >= relation.TaskID {
				return errors.New("packet task relations are not unique and deterministically sorted")
			}
		}
	}
	requested := make(map[string]bool, len(packet.RequestedKnowledgeRevisionIDs))
	for _, revisionID := range packet.RequestedKnowledgeRevisionIDs {
		if !validContextKnowledgeRevisionID(revisionID) || requested[revisionID] {
			return errors.New("packet requested knowledge IDs are invalid")
		}
		requested[revisionID] = true
	}
	accepted := make(map[string]bool, len(packet.AcceptedKnowledge))
	for _, revision := range packet.AcceptedKnowledge {
		if !requested[revision.ID] || accepted[revision.ID] || revision.WorkspaceID != packet.WorkspaceID || revision.ProjectID != packet.ProjectID ||
			(revision.TaskScopeID != "" && revision.TaskScopeID != packet.TaskID) || revision.ReviewStatus != domain.KnowledgeReviewAccepted ||
			revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent || !domain.ValidKnowledgeFreshnessPolicy(revision.FreshnessPolicy) {
			return errors.New("packet accepted knowledge scope is invalid")
		}
		if revision.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
			freshUntil, freshErr := time.Parse(time.RFC3339Nano, revision.FreshUntil)
			createdAt, createdErr := time.Parse(time.RFC3339Nano, packet.CreatedAt)
			if freshErr != nil || createdErr != nil || !createdAt.Before(freshUntil) {
				return errors.New("packet accepted knowledge is expired")
			}
		}
		accepted[revision.ID] = true
	}
	knowledgeBytes, err := contextKnowledgeEncodedBytes(packet.AcceptedKnowledge)
	if err != nil {
		return err
	}
	if packet.Budget.Knowledge != (domain.ContextBudgetUsage{LimitBytes: maximumContextKnowledgeBytes, UsedBytes: knowledgeBytes, RemainingBytes: maximumContextKnowledgeBytes - knowledgeBytes}) {
		return errors.New("packet knowledge budget is invalid")
	}
	selectionKeys := make(map[string]bool, len(packet.Included))
	for _, selection := range packet.Included {
		key := selection.Section + "\x00" + selection.EntityType + "\x00" + selection.EntityID
		if selection.Section == "" || selection.EntityID == "" || selection.Revision < 1 || selectionKeys[key] {
			return errors.New("packet inclusion evidence is invalid")
		}
		selectionKeys[key] = true
	}
	requireSelection := func(section, entityType, entityID string, revision int64) error {
		key := section + "\x00" + entityType + "\x00" + entityID
		if !selectionKeys[key] {
			return fmt.Errorf("packet is missing %s selection for %s", section, entityID)
		}
		for _, selection := range packet.Included {
			if selection.Section == section && selection.EntityType == entityType && selection.EntityID == entityID && selection.Revision != revision {
				return fmt.Errorf("packet selection revision differs for %s", entityID)
			}
		}
		return nil
	}
	for _, required := range []struct {
		section, entityType, id string
		revision                int64
	}{
		{"role", "agent", packet.AgentID, packet.Role.Revision}, {"task", "task", packet.TaskID, packet.Task.Revision}, {"checkout", "checkout", packet.CheckoutID, packet.Checkout.Revision},
	} {
		if err := requireSelection(required.section, required.entityType, required.id, required.revision); err != nil {
			return err
		}
	}
	for _, relation := range packet.Dependencies {
		if err := requireSelection("dependencies", "task", relation.TaskID, relation.Revision); err != nil {
			return err
		}
	}
	for _, relation := range packet.Dependents {
		if err := requireSelection("dependents", "task", relation.TaskID, relation.Revision); err != nil {
			return err
		}
	}
	for _, message := range packet.Inbox.Items {
		if err := requireSelection("inbox", "message", message.MessageID, 1); err != nil {
			return err
		}
	}
	for _, thread := range packet.ParticipantThreads {
		if err := requireSelection("participant_threads", "thread", thread.Thread.ID, thread.ParticipantRevision); err != nil {
			return err
		}
	}
	for _, revision := range packet.AcceptedKnowledge {
		if err := requireSelection("accepted_knowledge", "knowledge_revision", revision.ID, revision.RevisionNumber); err != nil {
			return err
		}
	}
	collaborationBytes := 0
	for _, thread := range packet.ParticipantThreads {
		if err := validateContextParticipantThread(thread, packet.WorkspaceID, packet.ProjectID, packet.TaskID, packet.AgentID); err != nil {
			return err
		}
		encoded, err := json.Marshal(thread)
		if err != nil {
			return err
		}
		collaborationBytes += len(encoded)
	}
	if collaborationBytes > maximumContextThreadBytes || packet.Budget.Collaboration != (domain.ContextBudgetUsage{
		LimitBytes: maximumContextThreadBytes, UsedBytes: collaborationBytes, RemainingBytes: maximumContextThreadBytes - collaborationBytes,
	}) || packet.Budget.Total.LimitBytes != maximumContextBytes || packet.Budget.Total.UsedBytes != packet.ByteSize ||
		packet.Budget.Total.RemainingBytes != maximumContextBytes-packet.ByteSize {
		return errors.New("packet collaboration or total budget is invalid")
	}
	return nil
}

func validateContextManagerGrant(packet domain.ContextPacket) error {
	grant := packet.ManagementGrant
	if grant == nil || grant.Schema != domain.ContextManagerGrantSchema || grant.GrantID == "" || grant.GrantRevision < 1 ||
		grant.WorkspaceID != packet.WorkspaceID || grant.ProjectID != packet.ProjectID || grant.ObjectiveID != packet.Task.ObjectiveID || grant.ObjectiveRevision < 1 ||
		grant.ManagerAgentID != packet.AgentID || grant.ManagerAgentRevision != packet.Role.Revision ||
		grant.ManagerTaskID != packet.TaskID || grant.ManagerTaskRevision != packet.Task.Revision ||
		len(grant.AllowedProposalKinds) < 1 || len(grant.AllowedProposalKinds) > len(managerProposalTools) ||
		grant.MaxOpenProposals < 1 || grant.MaxOpenProposals > 32 || grant.MaxActions < 1 || grant.MaxActions > 32 ||
		grant.MaxTasks < 1 || grant.MaxTasks > 16 || grant.MaxDependencies < 1 || grant.MaxDependencies > 32 ||
		grant.MaxClaimRequirements < 1 || grant.MaxClaimRequirements > 32 || len(grant.LaunchProfiles) < 1 || len(grant.LaunchProfiles) > 32 ||
		len(grant.AllowedClaimKinds) > 3 || grant.Budget.TokenLimit < 0 || grant.Budget.CostCents < 0 || grant.Budget.TimeSeconds < 0 {
		return errors.New("packet manager grant identity, scope, or limits are invalid")
	}
	kindOrder := make(map[string]int, len(managerProposalTools))
	for index, candidate := range managerProposalTools {
		kindOrder[candidate.kind] = index
	}
	lastKind := -1
	for _, kind := range grant.AllowedProposalKinds {
		order, exists := kindOrder[kind]
		if !exists || order <= lastKind {
			return errors.New("packet manager proposal kinds are invalid or not canonically ordered")
		}
		lastKind = order
	}
	for index, profile := range grant.LaunchProfiles {
		if profile.LaunchProfileID == "" || profile.Revision < 1 || profile.AgentID == "" || profile.AgentRevision < 1 ||
			index > 0 && grant.LaunchProfiles[index-1].LaunchProfileID >= profile.LaunchProfileID {
			return errors.New("packet manager launch profiles are invalid or not canonically ordered")
		}
	}
	for index, kind := range grant.AllowedClaimKinds {
		if kind != domain.ClaimKindPath && kind != domain.ClaimKindComponent && kind != domain.ClaimKindOperation ||
			index > 0 && grant.AllowedClaimKinds[index-1] >= kind {
			return errors.New("packet manager claim kinds are invalid or not canonically ordered")
		}
	}
	if grant.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt); err != nil {
			return errors.New("packet manager grant expiry is invalid")
		}
	}
	foundSelection := false
	for _, selection := range packet.Included {
		if selection.Section == "management_grant" && selection.EntityType == "manager_grant" &&
			selection.EntityID == grant.GrantID && selection.Revision == grant.GrantRevision {
			foundSelection = true
		}
	}
	if !foundSelection {
		return errors.New("packet manager grant inclusion evidence is missing")
	}
	return nil
}

func validateContextParticipantThread(thread domain.ParticipantThread, workspaceID, projectID, taskID, agentID string) error {
	if thread.Kind != domain.ThreadKindParticipantBound || thread.Thread.ID == "" || thread.Thread.WorkspaceID != workspaceID ||
		thread.Thread.ProjectID != "" || thread.Thread.TaskID != "" || thread.Thread.Status != domain.ThreadOpen ||
		thread.ParticipantRevision < 1 || len(thread.Participants) < 2 || len(thread.Participants) > 8 {
		return errors.New("context participant thread header is invalid")
	}
	participantIDs, agentIDs, projects, ordinals := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[int]bool{}
	exactBindings := 0
	for _, participant := range thread.Participants {
		if participant.ID == "" || participant.ThreadID != thread.Thread.ID || participant.WorkspaceID != workspaceID || participant.AgentID == "" ||
			participant.TaskID == "" || participant.ProjectID == "" || participant.AssignmentID == "" || participant.Ordinal < 1 ||
			participant.Status != domain.ThreadParticipantActive || participant.AssignmentRevision < 1 || participant.AgentRevision < 1 || participant.TaskRevision < 1 ||
			participantIDs[participant.ID] || agentIDs[participant.AgentID] || ordinals[participant.Ordinal] {
			return errors.New("context participant roster member is invalid or duplicated")
		}
		participantIDs[participant.ID], agentIDs[participant.AgentID], ordinals[participant.Ordinal], projects[participant.ProjectID] = true, true, true, true
		if participant.AgentID == agentID && participant.ProjectID == projectID && participant.TaskID == taskID {
			exactBindings++
		}
	}
	for ordinal := 1; ordinal <= len(thread.Participants); ordinal++ {
		if !ordinals[ordinal] {
			return errors.New("context participant roster ordinals are not contiguous")
		}
	}
	if len(projects) < 2 || exactBindings != 1 {
		return errors.New("context participant roster project diversity or exact run binding is invalid")
	}
	return nil
}

func (s *Store) validateLiveContextPacketAgainstCanonical(ctx context.Context, tx *sql.Tx, packet domain.ContextPacket) error {
	task, err := queryTask(ctx, tx, packet.WorkspaceID, packet.TaskID)
	if err != nil {
		return err
	}
	agent, err := queryAgent(ctx, tx, packet.WorkspaceID, packet.AgentID)
	if err != nil {
		return err
	}
	checkout, err := queryCheckoutByID(ctx, tx, packet.CheckoutID)
	if err != nil {
		return err
	}
	project, err := queryProject(ctx, tx, packet.WorkspaceID, packet.ProjectID)
	if err != nil {
		return err
	}
	fingerprint, err := dbgen.New(tx).GetContextRepositoryFingerprint(ctx, dbgen.GetContextRepositoryFingerprintParams{ID: checkout.RepositoryID, WorkspaceID: packet.WorkspaceID})
	if err != nil {
		return storageFailure("query canonical context repository", err)
	}
	expectedRole := domain.ContextRole{AgentID: agent.ID, Name: agent.Name, Role: agent.Role, Provider: agent.Provider, Runtime: agent.Runtime, Revision: agent.Revision}
	expectedTask := domain.ContextTask{TaskID: task.ID, AssignmentID: task.AssignmentID, ObjectiveID: task.ObjectiveID, Title: task.Title,
		Description: task.Description, Priority: task.Priority, Budget: task.Budget, Revision: task.Revision}
	expectedCheckout := domain.ContextCheckout{CheckoutID: checkout.ID, ProjectID: project.ID, ProjectName: project.Name,
		RepositoryID: checkout.RepositoryID, RepositoryFingerprint: fingerprint, Path: checkout.Path, WriteMode: checkout.WriteMode,
		CheckoutKind: checkout.CheckoutKind, Branch: checkout.Branch, HeadCommit: checkout.HeadCommit, Dirty: checkout.Dirty, Revision: checkout.Revision}
	packetCheckout, currentCheckout := packet.Checkout, expectedCheckout
	packetCheckout.Branch, packetCheckout.HeadCommit, packetCheckout.Dirty, packetCheckout.Revision = "", "", false, 0
	currentCheckout.Branch, currentCheckout.HeadCommit, currentCheckout.Dirty, currentCheckout.Revision = "", "", false, 0
	if packet.Role != expectedRole || packet.Task != expectedTask || packetCheckout != currentCheckout || !agent.Enabled || checkout.Availability != domain.CheckoutAvailable {
		return &Error{Code: CodeInvalidContext, Message: "live context packet snapshots differ from canonical run authority"}
	}
	dependencies, err := contextDependencies(ctx, tx, task.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(packet.Dependencies, dependencies) {
		return &Error{Code: CodeInvalidContext, Message: "live context packet upstream dependencies differ from canonical authority"}
	}
	for _, dependent := range packet.Dependents {
		var projectID, title, status string
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT task.project_id, task.title, task.status, task.revision
FROM task_dependencies dependency JOIN tasks task ON task.id = dependency.task_id
WHERE dependency.task_id = ? AND dependency.depends_on_task_id = ?`, dependent.TaskID, task.ID).Scan(&projectID, &title, &status, &revision); err != nil || projectID != packet.ProjectID || revision < dependent.Revision {
			return &Error{Code: CodeInvalidContext, Message: "live packet reverse dependent lacks canonical provenance", Cause: err}
		}
		if revision == dependent.Revision && (title != dependent.Title || status != dependent.Status) {
			return &Error{Code: CodeInvalidContext, Message: "live packet reverse dependent snapshot differs"}
		}
	}
	for _, item := range packet.Inbox.Items {
		canonical, err := queryInboxItem(ctx, tx, item.MessageID, packet.AgentID)
		if err != nil || canonical.Message.ThreadID != item.ThreadID || canonical.Message.Kind != item.Kind ||
			canonical.Message.SenderAgentID != item.SenderAgentID || canonical.Message.SenderAgentName != item.SenderAgentName ||
			messagePreview(canonical.Message.Body, 160) != item.BodyPreview || canonical.Message.CreatedAt != item.CreatedAt || canonical.Delivery.Status != item.Status {
			return &Error{Code: CodeInvalidContext, Message: "live packet inbox item lacks canonical provenance", Cause: err}
		}
		var authorized int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages message JOIN message_threads thread ON thread.id = message.thread_id
JOIN message_recipients recipient ON recipient.message_id = message.id
WHERE message.id = ? AND recipient.recipient_agent_id = ? AND recipient.status IN ('queued','delivered') AND (
 (thread.kind = 'direct' AND (message.project_id IS NULL OR message.project_id = ?)) OR
 (thread.kind = 'participant_bound' AND EXISTS (SELECT 1 FROM thread_participants participant
   WHERE participant.id = recipient.recipient_participant_id AND participant.status = 'active'
     AND participant.agent_id = ? AND participant.project_id = ? AND participant.task_id = ?))))`,
			item.MessageID, packet.AgentID, packet.ProjectID, packet.AgentID, packet.ProjectID, packet.TaskID).Scan(&authorized); err != nil || authorized != 1 {
			return &Error{Code: CodeInvalidContext, Message: "live packet inbox item is outside exact run authority", Cause: err}
		}
		var sentSequence int64
		if err := tx.QueryRowContext(ctx, "SELECT sequence FROM events WHERE type = 'message.sent' AND entity_id = ?", item.MessageID).Scan(&sentSequence); err != nil || sentSequence > packet.AsOfEventSequence {
			return &Error{Code: CodeInvalidContext, Message: "live packet inbox item postdates its cursor", Cause: err}
		}
	}
	for _, revision := range packet.AcceptedKnowledge {
		canonical, err := s.KnowledgeRevisionInTransaction(ctx, tx, packet.WorkspaceID, revision.ID)
		if err != nil {
			return &Error{Code: CodeInvalidContext, Message: "live packet knowledge is not canonical", Cause: err}
		}
		immutableRevision, immutableCanonical := revision, canonical
		immutableRevision.ReviewStatus, immutableRevision.CurrencyStatus, immutableRevision.StateRevision = "", "", 0
		immutableCanonical.ReviewStatus, immutableCanonical.CurrencyStatus, immutableCanonical.StateRevision = "", "", 0
		if !reflect.DeepEqual(immutableRevision, immutableCanonical) {
			return &Error{Code: CodeInvalidContext, Message: "live packet knowledge snapshot differs from canonical authority"}
		}
		if code, _, err := contextKnowledgeIneligibility(canonical, packet.WorkspaceID, task, s.nowText()); err != nil {
			return err
		} else if code != "" {
			return &Error{Code: CodeInvalidContext, Message: "live packet knowledge is no longer eligible at run binding"}
		}
		_, openCount, err := openKnowledgeContradictions(ctx, tx, packet.WorkspaceID, revision.ID)
		if err != nil {
			return err
		}
		if openCount != 0 {
			return &Error{Code: CodeInvalidContext, Message: "live packet knowledge is disputed at run binding"}
		}
	}
	for _, thread := range packet.ParticipantThreads {
		canonical, err := participantThreadInTransaction(ctx, dbgen.New(tx), packet.WorkspaceID, thread.Thread.ID)
		if err != nil {
			return err
		}
		frozenHeader, currentHeader := thread.Thread, canonical.Thread
		frozenHeader.Revision, frozenHeader.UpdatedAt, frozenHeader.UpdatedBy = 0, "", ""
		currentHeader.Revision, currentHeader.UpdatedAt, currentHeader.UpdatedBy = 0, "", ""
		if canonical.ParticipantRevision < thread.ParticipantRevision || len(canonical.Participants) < len(thread.Participants) || !reflect.DeepEqual(frozenHeader, currentHeader) {
			return &Error{Code: CodeInvalidContext, Message: "live packet participant roster differs from canonical authority"}
		}
		for index := range thread.Participants {
			if !reflect.DeepEqual(thread.Participants[index], canonical.Participants[index]) {
				return &Error{Code: CodeInvalidContext, Message: "live packet participant lacks canonical provenance"}
			}
		}
	}
	if packet.Schema == domain.ContextPacketSchemaV5 {
		if err := s.validateVersionFiveManagerGrantAgainstCanonical(ctx, tx, packet); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validateVersionFiveManagerGrantAgainstCanonical(ctx context.Context, tx *sql.Tx, packet domain.ContextPacket) error {
	if packet.ManagementGrant == nil {
		return &Error{Code: CodeInvalidContext, Message: "version-five context packet is missing manager authority"}
	}
	snapshot := packet.ManagementGrant
	grant, err := queryManagerGrant(ctx, tx, packet.WorkspaceID, snapshot.GrantID)
	if err != nil {
		return &Error{Code: CodeInvalidContext, Message: "version-five manager grant is not canonical", Cause: err}
	}
	objective, err := queryObjective(ctx, tx, packet.WorkspaceID, grant.ObjectiveID)
	if err != nil {
		return &Error{Code: CodeInvalidContext, Message: "version-five manager objective is not canonical", Cause: err}
	}
	if grant.Status != domain.ManagerGrantActive || grant.Revision != snapshot.GrantRevision ||
		grant.WorkspaceID != snapshot.WorkspaceID || grant.ProjectID != snapshot.ProjectID || grant.ObjectiveID != snapshot.ObjectiveID ||
		objective.Revision != snapshot.ObjectiveRevision || grant.TaskID != snapshot.ManagerTaskID || grant.TaskRevision != snapshot.ManagerTaskRevision ||
		grant.AgentID != snapshot.ManagerAgentID || grant.AgentRevision != snapshot.ManagerAgentRevision ||
		!reflect.DeepEqual(grant.ProposalKinds, snapshot.AllowedProposalKinds) ||
		!reflect.DeepEqual(grant.AllowedClaimKinds, snapshot.AllowedClaimKinds) ||
		grant.Limits.MaxOpenProposals != snapshot.MaxOpenProposals || grant.Limits.MaxActions != snapshot.MaxActions ||
		grant.Limits.MaxTasks != snapshot.MaxTasks || grant.Limits.MaxDependencies != snapshot.MaxDependencies ||
		grant.Limits.MaxClaimRequirements != snapshot.MaxClaimRequirements || grant.Limits.Budget != snapshot.Budget ||
		grant.ExpiresAt != snapshot.ExpiresAt || len(grant.LaunchProfiles) != len(snapshot.LaunchProfiles) {
		return &Error{Code: CodeInvalidContext, Message: "version-five manager grant differs from current exact authority"}
	}
	if grant.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		if err != nil || !s.clock().UTC().Before(expiresAt) {
			return &Error{Code: CodeInvalidContext, Message: "version-five manager grant has expired", Cause: err}
		}
	}
	for index, frozen := range snapshot.LaunchProfiles {
		current := grant.LaunchProfiles[index]
		if current.LaunchProfileID != frozen.LaunchProfileID || current.Revision != frozen.Revision ||
			current.AgentID != frozen.AgentID || current.AgentRevision != frozen.AgentRevision {
			return &Error{Code: CodeInvalidContext, Message: "version-five manager launch-profile snapshot differs from current grant authority"}
		}
		profile, err := queryLaunchProfile(ctx, tx, packet.WorkspaceID, frozen.LaunchProfileID)
		if err != nil || profile.Status != domain.LaunchProfileActive || profile.ProjectID != packet.ProjectID ||
			profile.Revision != frozen.Revision || profile.AgentID != frozen.AgentID || profile.AgentRevision != frozen.AgentRevision ||
			profile.ManagerGrantID != "" {
			return &Error{Code: CodeInvalidContext, Message: "version-five manager target profile is no longer exact and active", Cause: err}
		}
	}
	return nil
}

func contextPacketSemanticHash(packet domain.ContextPacket) (string, error) {
	semantic := packet
	semantic.ID, semantic.ContentHash, semantic.CreatedAt, semantic.CreatedBy = "", "", "", ""
	semantic.ByteSize = 0
	semantic.Budget.Total.UsedBytes = 0
	semantic.Budget.Total.RemainingBytes = maximumContextBytes
	semanticJSON, err := json.Marshal(semantic)
	if err != nil {
		return "", storageFailure("encode context packet semantic content", err)
	}
	digest := sha256.Sum256(semanticJSON)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (s *Store) AuthorizeRunCapability(ctx context.Context, runID string) (domain.RunBriefing, error) {
	run, err := queryRun(ctx, s.db, "", strings.TrimSpace(runID))
	if err != nil {
		return domain.RunBriefing{}, err
	}
	var packetID, expiresAt string
	if err := s.db.QueryRowContext(ctx, `SELECT b.context_packet_id, c.expires_at
FROM run_context_bindings b JOIN run_capabilities c ON c.run_id = b.run_id
WHERE b.run_id = ?`, run.ID).Scan(&packetID, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RunBriefing{}, &Error{Code: CodeCapabilityInactive, Message: "run has no scoped capability"}
		}
		return domain.RunBriefing{}, storageFailure("query run capability", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.RunBriefing{}, storageFailure("parse run capability expiry", err)
	}
	if !s.clock().UTC().Before(expires) {
		return domain.RunBriefing{}, &Error{Code: CodeCapabilityExpired, Message: "run capability has expired"}
	}
	switch run.Status {
	case domain.RunStarting, domain.RunActive, domain.RunBlocked:
	default:
		return domain.RunBriefing{}, &Error{Code: CodeCapabilityInactive, Message: fmt.Sprintf("run capability is inactive while run is %s", run.Status)}
	}
	packet, err := queryContextPacket(ctx, s.db, run.WorkspaceID, packetID)
	if err != nil {
		return domain.RunBriefing{}, err
	}
	task, err := queryTask(ctx, s.db, run.WorkspaceID, run.TaskID)
	if err != nil {
		return domain.RunBriefing{}, err
	}
	return domain.RunBriefing{Run: run, Task: task, Packet: packet, ExpiresAt: expiresAt, Resource: "crewfold://runs/" + run.ID + "/briefing"}, nil
}

func (s *Store) SubmitRunReport(ctx context.Context, command CreateRunReportCommand) (domain.RunReport, error) {
	runID, kind := strings.TrimSpace(command.RunID), strings.TrimSpace(command.Kind)
	message, handoff, key := strings.TrimSpace(command.Message), strings.TrimSpace(command.Handoff), strings.TrimSpace(command.IdempotencyKey)
	if runID == "" || key == "" || len(key) > 128 || !validRunReportText(message, 1024) {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report requires run, bounded message, and idempotency key"}
	}
	if kind != domain.ObservationProgress && kind != domain.ObservationBlocked && kind != domain.ObservationCompletion {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report kind is unsupported"}
	}
	if kind == domain.ObservationCompletion && !validRunReportText(handoff, 4096) {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "completion report requires a bounded handoff"}
	}
	if len(command.Evidence) > 32 {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report contains too many evidence identifiers"}
	}
	for _, evidence := range command.Evidence {
		if !validRunReportText(strings.TrimSpace(evidence), 128) {
			return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report evidence identifier is invalid"}
		}
	}
	payloadJSON, err := json.Marshal(command.Payload)
	if err != nil || len(payloadJSON) > 16*1024 {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report payload exceeds its bounded encoding"}
	}
	requestHash, err := hashCommand("run.report", map[string]any{"kind": kind, "message": message, "evidence": command.Evidence, "handoff": handoff, "payload": json.RawMessage(payloadJSON)})
	if err != nil {
		return domain.RunReport{}, storageFailure("hash run report", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunReport{}, storageFailure("begin run report", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunReport{}, err
	}
	var existingHash string
	err = tx.QueryRowContext(ctx, "SELECT request_hash FROM run_reports WHERE run_id = ? AND idempotency_key = ?", run.ID, key).Scan(&existingHash)
	if err == nil {
		if existingHash != requestHash {
			return domain.RunReport{}, &Error{Code: CodeIdempotencyConflict, Message: "run report idempotency key was used with different content"}
		}
		return queryRunReport(ctx, tx, run.ID, key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.RunReport{}, storageFailure("check run report idempotency", err)
	}
	if run.Status != domain.RunStarting && run.Status != domain.RunActive {
		return domain.RunReport{}, &Error{Code: CodeRunConflict, Message: "run reports require a starting or active run"}
	}
	reportID, err := randomID("report_")
	if err != nil {
		return domain.RunReport{}, storageFailure("generate run report id", err)
	}
	now := s.nowText()
	evidenceJSON, _ := json.Marshal(command.Evidence)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_reports(
id, run_id, kind, message, evidence_json, handoff, payload_json, idempotency_key,
request_hash, status, created_at) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, 'pending', ?)`,
		reportID, run.ID, kind, message, string(evidenceJSON), handoff, string(payloadJSON), key, requestHash, now); err != nil {
		return domain.RunReport{}, storageFailure("insert run report", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_jobs SET status = 'pending', available_at = ?, lease_expires_at = NULL, updated_at = ? WHERE run_id = ? AND status = 'complete'", now, now, run.ID); err != nil {
		return domain.RunReport{}, storageFailure("wake run for report", err)
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runReportReceivedEvent, "report-"+reportID, now, map[string]any{"report_id": reportID, "kind": kind}); err != nil {
		return domain.RunReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunReport{}, storageFailure("commit run report", err)
	}
	return domain.RunReport{ID: reportID, RunID: run.ID, Kind: kind, Message: message, Evidence: append([]string(nil), command.Evidence...), Handoff: handoff, IdempotencyKey: key, Status: "pending", CreatedAt: now}, nil
}

func (s *Store) NextPendingRunReport(ctx context.Context, runID string) (domain.RunReport, bool, error) {
	var key string
	err := s.db.QueryRowContext(ctx, "SELECT idempotency_key FROM run_reports WHERE run_id = ? AND status = 'pending' ORDER BY sequence LIMIT 1", strings.TrimSpace(runID)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunReport{}, false, nil
	}
	if err != nil {
		return domain.RunReport{}, false, storageFailure("query pending run report", err)
	}
	report, err := queryRunReport(ctx, s.db, strings.TrimSpace(runID), key)
	return report, err == nil, err
}

func queryRunReportByID(ctx context.Context, database queryRower, reportID string) (domain.RunReport, error) {
	var report domain.RunReport
	var evidenceJSON string
	err := database.QueryRowContext(ctx, `SELECT id, run_id, kind, message, evidence_json,
COALESCE(handoff, ''), idempotency_key, status, created_at, COALESCE(applied_at, '')
FROM run_reports WHERE id = ?`, reportID).Scan(
		&report.ID, &report.RunID, &report.Kind, &report.Message, &evidenceJSON,
		&report.Handoff, &report.IdempotencyKey, &report.Status, &report.CreatedAt, &report.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunReport{}, &Error{Code: CodeInvalidReport, Message: "run report was not found"}
	}
	if err != nil {
		return domain.RunReport{}, storageFailure("query run report by id", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &report.Evidence); err != nil {
		return domain.RunReport{}, storageFailure("decode run report evidence", err)
	}
	return report, nil
}

func queryRunReport(ctx context.Context, database queryRower, runID, key string) (domain.RunReport, error) {
	var report domain.RunReport
	var evidenceJSON string
	err := database.QueryRowContext(ctx, `SELECT id, run_id, kind, message, evidence_json,
COALESCE(handoff, ''), idempotency_key, status, created_at, COALESCE(applied_at, '')
FROM run_reports WHERE run_id = ? AND idempotency_key = ?`, runID, key).Scan(
		&report.ID, &report.RunID, &report.Kind, &report.Message, &evidenceJSON,
		&report.Handoff, &report.IdempotencyKey, &report.Status, &report.CreatedAt, &report.AppliedAt)
	if err != nil {
		return domain.RunReport{}, storageFailure("query run report", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &report.Evidence); err != nil {
		return domain.RunReport{}, storageFailure("decode run report evidence", err)
	}
	return report, nil
}

func (s *Store) PublishRunArtifact(ctx context.Context, command PublishRunArtifactCommand) (domain.RunArtifact, error) {
	runID, name := strings.TrimSpace(command.RunID), strings.TrimSpace(command.Name)
	mediaType, key := strings.TrimSpace(command.MediaType), strings.TrimSpace(command.IdempotencyKey)
	if runID == "" || !validRunReportText(name, 128) || !validRunReportText(mediaType, 128) || key == "" || len(key) > 128 || !utf8.ValidString(command.Content) || len(command.Content) > maximumArtifactBytes {
		return domain.RunArtifact{}, &Error{Code: CodeInvalidReport, Message: "artifact requires bounded name, media type, content, and idempotency key"}
	}
	requestHash, err := hashCommand("run.artifact.publish", map[string]any{"name": name, "media_type": mediaType, "content": command.Content})
	if err != nil {
		return domain.RunArtifact{}, storageFailure("hash run artifact", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunArtifact{}, storageFailure("begin run artifact", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunArtifact{}, err
	}
	var existingHash string
	err = tx.QueryRowContext(ctx, "SELECT request_hash FROM run_artifacts WHERE run_id = ? AND idempotency_key = ?", run.ID, key).Scan(&existingHash)
	if err == nil {
		if existingHash != requestHash {
			return domain.RunArtifact{}, &Error{Code: CodeIdempotencyConflict, Message: "artifact idempotency key was used with different content"}
		}
		return queryRunArtifact(ctx, tx, run.ID, key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.RunArtifact{}, storageFailure("check artifact idempotency", err)
	}
	if run.Status != domain.RunStarting && run.Status != domain.RunActive {
		return domain.RunArtifact{}, &Error{Code: CodeRunConflict, Message: "artifacts require a starting or active run"}
	}
	artifactID, err := randomID("artifact_")
	if err != nil {
		return domain.RunArtifact{}, storageFailure("generate artifact id", err)
	}
	digest := sha256.Sum256([]byte(command.Content))
	contentHash := "sha256:" + hex.EncodeToString(digest[:])
	now := s.nowText()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_artifacts(
id, run_id, name, media_type, content, content_hash, byte_size, idempotency_key,
request_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifactID, run.ID,
		name, mediaType, command.Content, contentHash, len(command.Content), key, requestHash, now); err != nil {
		return domain.RunArtifact{}, storageFailure("insert run artifact", err)
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runArtifactPublished, "artifact-"+artifactID, now, map[string]any{"artifact_id": artifactID, "name": name, "content_hash": contentHash, "byte_size": len(command.Content)}); err != nil {
		return domain.RunArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunArtifact{}, storageFailure("commit run artifact", err)
	}
	return domain.RunArtifact{ID: artifactID, RunID: run.ID, Name: name, MediaType: mediaType, ContentHash: contentHash, ByteSize: len(command.Content), IdempotencyKey: key, CreatedAt: now}, nil
}

func queryRunArtifact(ctx context.Context, database queryRower, runID, key string) (domain.RunArtifact, error) {
	var artifact domain.RunArtifact
	err := database.QueryRowContext(ctx, `SELECT id, run_id, name, media_type, content_hash,
byte_size, idempotency_key, created_at FROM run_artifacts WHERE run_id = ? AND idempotency_key = ?`, runID, key).Scan(
		&artifact.ID, &artifact.RunID, &artifact.Name, &artifact.MediaType, &artifact.ContentHash,
		&artifact.ByteSize, &artifact.IdempotencyKey, &artifact.CreatedAt)
	if err != nil {
		return domain.RunArtifact{}, storageFailure("query run artifact", err)
	}
	return artifact, nil
}

func (s *Store) RecordRunToolCall(ctx context.Context, runID, requestID, method, targetID, outcome, errorCode string) (domain.RunToolCall, error) {
	if runID == "" || requestID == "" || len(requestID) > 128 || method == "" || len(method) > 128 || (outcome != "allowed" && outcome != "denied" && outcome != "error") {
		return domain.RunToolCall{}, &Error{Code: CodeInvalidReport, Message: "tool audit fields are invalid"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunToolCall{}, storageFailure("begin tool audit", err)
	}
	defer tx.Rollback()
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.RunToolCall{}, err
	}
	id, err := randomID("toolcall_")
	if err != nil {
		return domain.RunToolCall{}, storageFailure("generate tool audit id", err)
	}
	now := s.nowText()
	call := domain.RunToolCall{ID: id, RunID: run.ID, RequestID: requestID, Method: method, TargetID: targetID, Outcome: outcome, ErrorCode: errorCode, RecordedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_tool_calls(id, run_id, request_id, method, target_id, outcome, error_code, recorded_at)
VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?)`, call.ID, call.RunID, call.RequestID, call.Method, call.TargetID, call.Outcome, call.ErrorCode, call.RecordedAt); err != nil {
		return domain.RunToolCall{}, storageFailure("insert tool audit", err)
	}
	eventType := runToolCalledEvent
	if outcome == "denied" {
		eventType = runToolDeniedEvent
	}
	if _, err := appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, eventType, requestID, now, map[string]any{"tool_call_id": call.ID, "method": method, "target_id": targetID, "outcome": outcome, "error_code": errorCode}); err != nil {
		return domain.RunToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunToolCall{}, storageFailure("commit tool audit", err)
	}
	return call, nil
}

func (s *Store) RunToolCalls(ctx context.Context, runID string) ([]domain.RunToolCall, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, run_id, request_id, method, COALESCE(target_id, ''), outcome, COALESCE(error_code, ''), recorded_at FROM run_tool_calls WHERE run_id = ? ORDER BY recorded_at, id", strings.TrimSpace(runID))
	if err != nil {
		return nil, storageFailure("list run tool calls", err)
	}
	defer rows.Close()
	result := make([]domain.RunToolCall, 0)
	for rows.Next() {
		var call domain.RunToolCall
		if err := rows.Scan(&call.ID, &call.RunID, &call.RequestID, &call.Method, &call.TargetID, &call.Outcome, &call.ErrorCode, &call.RecordedAt); err != nil {
			return nil, storageFailure("scan run tool call", err)
		}
		result = append(result, call)
	}
	return result, rows.Err()
}

func validRunReportText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
}
