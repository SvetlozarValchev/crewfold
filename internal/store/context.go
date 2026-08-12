package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const (
	contextPacketBuiltEvent = "context.packet_built"
	runReportReceivedEvent  = "run.report_received"
	runArtifactPublished    = "run.artifact_published"
	runToolCalledEvent      = "run.tool_called"
	runToolDeniedEvent      = "run.tool_denied"
	maximumContextBytes     = 32 * 1024
	maximumArtifactBytes    = 32 * 1024
)

var runScopedTools = []string{
	"crewfold_get_briefing",
	"crewfold_get_status",
	"crewfold_propose_completion",
	"crewfold_publish_artifact",
	"crewfold_report_blocked",
	"crewfold_report_progress",
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
	requestHash, err := hashCommand("context.build", command)
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
	packet, sequence, err := s.buildContextPacketInTransaction(ctx, tx, workspace.ID, task, agent, checkout, correlationID, now)
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
	project, err := queryProject(ctx, tx, workspaceID, task.ProjectID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	var repositoryFingerprint string
	if err := tx.QueryRowContext(ctx, "SELECT fingerprint FROM repositories WHERE id = ? AND workspace_id = ?", checkout.RepositoryID, workspaceID).Scan(&repositoryFingerprint); err != nil {
		return domain.ContextPacket{}, 0, storageFailure("query context repository", err)
	}
	dependencies, err := contextDependencies(ctx, tx, task.ID)
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	packetID, err := randomID("ctx_")
	if err != nil {
		return domain.ContextPacket{}, 0, storageFailure("generate context packet id", err)
	}
	packet := domain.ContextPacket{
		Schema: domain.ContextPacketSchema, ID: packetID, WorkspaceID: workspaceID,
		ProjectID: task.ProjectID, TaskID: task.ID, AgentID: agent.ID, CheckoutID: checkout.ID,
		Role: domain.ContextRole{AgentID: agent.ID, Name: agent.Name, Role: agent.Role, Provider: agent.Provider, Runtime: agent.Runtime, Revision: agent.Revision},
		Task: domain.ContextTask{TaskID: task.ID, ObjectiveID: task.ObjectiveID, Title: task.Title, Description: task.Description, Priority: task.Priority, Budget: task.Budget, Revision: task.Revision},
		Checkout: domain.ContextCheckout{
			CheckoutID: checkout.ID, ProjectID: project.ID, ProjectName: project.Name,
			RepositoryID: checkout.RepositoryID, RepositoryFingerprint: repositoryFingerprint,
			Path: checkout.Path, WriteMode: checkout.WriteMode, CheckoutKind: checkout.CheckoutKind,
			Branch: checkout.Branch, HeadCommit: checkout.HeadCommit, Dirty: checkout.Dirty, Revision: checkout.Revision,
		},
		Dependencies: dependencies,
		Policy: domain.ContextPolicy{
			AllowedTools:     append([]string(nil), runScopedTools...),
			DeniedOperations: []string{"change another run or task", "push or merge source", "deploy", "message a person", "read unscoped context"},
			ApprovalRequired: []string{"shared repository mutation", "external side effect", "destructive operation"},
		},
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
			{Section: "accepted_knowledge", Reason: "no canonical knowledge capability is enabled"},
			{Section: "messages", Reason: "agent messaging is not enabled"},
			{Section: "claims", Reason: "scope claims and overlap detection are not enabled"},
			{Section: "transcripts", Reason: "raw provider transcripts are not context authority"},
		},
		CreatedAt: now, CreatedBy: localOwnerActorID,
	}
	for _, dependency := range dependencies {
		packet.Included = append(packet.Included, domain.ContextSelection{Section: "dependencies", EntityType: "task", EntityID: dependency.TaskID, Revision: dependency.Revision, Reason: "direct task dependency"})
	}
	semantic := packet
	semantic.ID, semantic.ContentHash, semantic.CreatedAt, semantic.CreatedBy = "", "", "", ""
	semantic.ByteSize = 0
	semanticJSON, err := json.Marshal(semantic)
	if err != nil {
		return domain.ContextPacket{}, 0, storageFailure("encode context packet semantic content", err)
	}
	digest := sha256.Sum256(semanticJSON)
	packet.ContentHash = "sha256:" + hex.EncodeToString(digest[:])
	for range 4 {
		packetJSON, marshalErr := json.Marshal(packet)
		if marshalErr != nil {
			return domain.ContextPacket{}, 0, storageFailure("encode context packet", marshalErr)
		}
		if len(packetJSON) == packet.ByteSize {
			break
		}
		packet.ByteSize = len(packetJSON)
	}
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		return domain.ContextPacket{}, 0, storageFailure("encode final context packet", err)
	}
	if len(packetJSON) != packet.ByteSize || packet.ByteSize <= 0 || packet.ByteSize > maximumContextBytes {
		return domain.ContextPacket{}, 0, &Error{Code: CodeInvalidContext, Message: "context packet exceeds its bounded size"}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_packets(
id, workspace_id, project_id, task_id, agent_id, checkout_id, packet_json,
content_hash, byte_size, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		packet.ID, packet.WorkspaceID, packet.ProjectID, packet.TaskID, packet.AgentID, packet.CheckoutID,
		string(packetJSON), packet.ContentHash, packet.ByteSize, packet.CreatedAt, packet.CreatedBy); err != nil {
		return domain.ContextPacket{}, 0, storageFailure("insert context packet", err)
	}
	sequence, err := appendEvent(ctx, tx, workspaceID, "context_packet", packet.ID, 1, contextPacketBuiltEvent, correlationID, now, map[string]any{
		"task_id": task.ID, "agent_id": agent.ID, "checkout_id": checkout.ID,
		"content_hash": packet.ContentHash, "byte_size": packet.ByteSize,
	})
	if err != nil {
		return domain.ContextPacket{}, 0, err
	}
	return packet, sequence, nil
}

func contextDependencies(ctx context.Context, tx *sql.Tx, taskID string) ([]domain.ContextDependency, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.title, t.status, t.revision
FROM task_dependencies d JOIN tasks t ON t.id = d.depends_on_task_id
WHERE d.task_id = ? ORDER BY t.id`, taskID)
	if err != nil {
		return nil, storageFailure("query context dependencies", err)
	}
	defer rows.Close()
	result := make([]domain.ContextDependency, 0)
	for rows.Next() {
		var dependency domain.ContextDependency
		if err := rows.Scan(&dependency.TaskID, &dependency.Title, &dependency.Status, &dependency.Revision); err != nil {
			return nil, storageFailure("scan context dependency", err)
		}
		result = append(result, dependency)
	}
	return result, rows.Err()
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
	return domain.ContextExplanation{PacketID: packet.ID, ContentHash: packet.ContentHash, ByteSize: packet.ByteSize, Included: packet.Included, Excluded: packet.Excluded}, nil
}

func queryContextPacket(ctx context.Context, database queryRower, workspaceID, packetID string) (domain.ContextPacket, error) {
	var packetJSON string
	err := database.QueryRowContext(ctx, "SELECT packet_json FROM context_packets WHERE id = ? AND workspace_id = ?", packetID, workspaceID).Scan(&packetJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContextPacket{}, &Error{Code: CodeContextNotFound, Message: fmt.Sprintf("context packet %q was not found", packetID)}
	}
	if err != nil {
		return domain.ContextPacket{}, storageFailure("query context packet", err)
	}
	var packet domain.ContextPacket
	if err := json.Unmarshal([]byte(packetJSON), &packet); err != nil {
		return domain.ContextPacket{}, storageFailure("decode context packet", err)
	}
	if packet.Schema != domain.ContextPacketSchema || packet.ID != packetID || packet.WorkspaceID != workspaceID || packet.ByteSize != len(packetJSON) {
		return domain.ContextPacket{}, storageFailure("validate context packet", errors.New("stored packet identity or size is invalid"))
	}
	return packet, nil
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
