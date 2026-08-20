package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const maximumWorkstreamDeliverySamples = 20

type workstreamDeliveryTask struct {
	ID                   string   `json:"id"`
	Class                string   `json:"class"`
	Status               string   `json:"status"`
	Revision             int64    `json:"revision"`
	BlockedReason        string   `json:"blocked_reason,omitempty"`
	ReportID             string   `json:"report_id,omitempty"`
	Assessment           string   `json:"assessment,omitempty"`
	ReportSummary        string   `json:"report_summary,omitempty"`
	RemediationConsumers int      `json:"remediation_consumers"`
	Evidence             []string `json:"evidence"`
}

type workstreamDeliveryContent struct {
	ObjectiveID     string                   `json:"objective_id"`
	PrimaryCheckout string                   `json:"primary_checkout"`
	Tasks           []workstreamDeliveryTask `json:"tasks"`
}

// WorkstreamDelivery derives the current owner-facing delivery state from exact
// canonical task, report, handoff, and evidence records. It performs no write.
func (s *Store) WorkstreamDelivery(ctx context.Context, workspaceIdentifier, objectiveID string) (domain.WorkstreamDelivery, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.WorkstreamDelivery{}, storageFailure("begin workstream delivery read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.WorkstreamDelivery{}, err
	}
	objective, err := queryObjective(ctx, tx, workspace.ID, strings.TrimSpace(objectiveID))
	if err != nil {
		return domain.WorkstreamDelivery{}, err
	}
	delivery, _, err := deriveWorkstreamDelivery(ctx, tx, objective)
	if err != nil {
		return domain.WorkstreamDelivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WorkstreamDelivery{}, storageFailure("commit workstream delivery read", err)
	}
	return delivery, nil
}

func deriveWorkstreamDelivery(ctx context.Context, tx *sql.Tx, objective domain.Objective) (domain.WorkstreamDelivery, bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT t.id,t.task_class,t.status,t.revision,COALESCE(t.blocked_reason,''),
 COALESCE((SELECT rr.id FROM run_reports rr JOIN runs r ON r.id=rr.run_id WHERE r.task_id=t.id AND rr.kind='completion' AND rr.status='applied' ORDER BY rr.sequence DESC LIMIT 1),''),
 COALESCE((SELECT rr.assessment FROM run_reports rr JOIN runs r ON r.id=rr.run_id WHERE r.task_id=t.id AND rr.kind='completion' AND rr.status='applied' ORDER BY rr.sequence DESC LIMIT 1),''),
 COALESCE((SELECT rr.message FROM run_reports rr JOIN runs r ON r.id=rr.run_id WHERE r.task_id=t.id AND rr.kind='completion' AND rr.status='applied' ORDER BY rr.sequence DESC LIMIT 1),''),
 (SELECT count(*) FROM task_dependencies downstream
  JOIN tasks remediation ON remediation.id=downstream.task_id
  WHERE downstream.depends_on_task_id=t.id
    AND remediation.task_class='implementation'
    AND downstream.delivery_requirement IN ('handoff','handoff_with_evidence')),
 COALESCE((
   SELECT json_group_array(artifact.id)
   FROM run_reports evidence_report
   JOIN runs evidence_run ON evidence_run.id=evidence_report.run_id
   JOIN json_each(evidence_report.evidence_json) evidence_reference
   JOIN run_artifacts artifact ON artifact.id=evidence_reference.value AND artifact.run_id=evidence_report.run_id
   WHERE evidence_report.id=(
     SELECT rr.id FROM run_reports rr JOIN runs r ON r.id=rr.run_id
     WHERE r.task_id=t.id AND rr.kind='completion' AND rr.status='applied'
     ORDER BY rr.sequence DESC LIMIT 1
   )
 ),'[]')
FROM tasks t WHERE t.objective_id=? ORDER BY t.id`, objective.ID)
	if err != nil {
		return domain.WorkstreamDelivery{}, false, storageFailure("query workstream delivery tasks", err)
	}
	defer rows.Close()
	content := workstreamDeliveryContent{ObjectiveID: objective.ID, PrimaryCheckout: objective.PrimaryCheckoutID, Tasks: []workstreamDeliveryTask{}}
	delivery := domain.WorkstreamDelivery{ObjectiveID: objective.ID, ObjectiveRevision: objective.Revision, Evidence: []string{}, Blockers: []string{}}
	allCompleted, eligible := true, true
	for rows.Next() {
		var item workstreamDeliveryTask
		var evidenceJSON string
		if err := rows.Scan(&item.ID, &item.Class, &item.Status, &item.Revision, &item.BlockedReason, &item.ReportID, &item.Assessment, &item.ReportSummary, &item.RemediationConsumers, &evidenceJSON); err != nil {
			return domain.WorkstreamDelivery{}, false, storageFailure("scan workstream delivery task", err)
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &item.Evidence); err != nil {
			return domain.WorkstreamDelivery{}, false, storageFailure("decode workstream delivery evidence", err)
		}
		sort.Strings(item.Evidence)
		content.Tasks = append(content.Tasks, item)
		delivery.TaskCount++
		if item.Status == domain.TaskCompleted {
			delivery.CompletedTasks++
		} else {
			allCompleted, eligible = false, false
			appendDeliveryBlocker(&delivery, fmt.Sprintf("%s: %s", item.ID, firstNonEmpty(item.BlockedReason, "task is "+item.Status)))
		}
		if item.Class == "review" || item.Class == "verification" {
			if item.Class == "verification" {
				delivery.VerificationTasks++
			}
			if item.Assessment == "pass" {
				if item.Class == "verification" {
					delivery.PassingVerifications++
				}
			} else if (item.Assessment != "changes_requested" && item.Assessment != "block") || item.Status != domain.TaskCompleted || item.RemediationConsumers == 0 {
				eligible = false
				appendDeliveryBlocker(&delivery, fmt.Sprintf("%s: %s assessment", item.ID, firstNonEmpty(item.Assessment, "missing structured")))
			}
			if len(item.Evidence) == 0 {
				eligible = false
				appendDeliveryBlocker(&delivery, fmt.Sprintf("%s: no readable review or verification evidence", item.ID))
			}
		}
		for _, evidence := range item.Evidence {
			if len(delivery.Evidence) < 128 {
				delivery.Evidence = append(delivery.Evidence, evidence)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.WorkstreamDelivery{}, false, storageFailure("iterate workstream delivery tasks", err)
	}
	if err := rows.Close(); err != nil {
		return domain.WorkstreamDelivery{}, false, storageFailure("close workstream delivery tasks", err)
	}
	if delivery.TaskCount == 0 {
		eligible = false
		allCompleted = false
		appendDeliveryBlocker(&delivery, "workstream has no accepted tasks")
	}
	if delivery.VerificationTasks == 0 || delivery.PassingVerifications == 0 {
		eligible = false
		appendDeliveryBlocker(&delivery, "final structured verification pass is missing")
	}
	sort.Strings(delivery.Evidence)
	delivery.Evidence = uniqueStrings(delivery.Evidence)
	digest, err := hashCommand("workstream.delivery.v1", content)
	if err != nil {
		return domain.WorkstreamDelivery{}, false, storageFailure("hash workstream delivery", err)
	}
	delivery.SHA256 = digest
	delivery.State = domain.WorkstreamDeliveryInProgress
	if !eligible && len(delivery.Blockers) > 0 && (allCompleted || hasConsequentialDeliveryBlocker(content.Tasks)) {
		delivery.State = domain.WorkstreamDeliveryBlocked
	}
	if eligible && allCompleted {
		delivery.State = domain.WorkstreamDeliveryVerifiedAwaitingAcceptance
	}
	var decision, reason, at string
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT sequence,recorded_at,
 COALESCE(json_extract(data_json,'$.delivery.decision'),''),
 COALESCE(json_extract(data_json,'$.delivery.reason'),'')
FROM events WHERE workspace_id=? AND entity_type='objective' AND entity_id=?
 AND type='objective.updated' AND json_extract(data_json,'$.delivery.sha256')=?
ORDER BY sequence DESC LIMIT 1`, objective.WorkspaceID, objective.ID, digest).Scan(&sequence, &at, &decision, &reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.WorkstreamDelivery{}, false, storageFailure("query workstream delivery decision", err)
	}
	if err == nil {
		delivery.DecisionReason, delivery.DecisionAt, delivery.DecisionEventSequence = reason, at, sequence
		if decision == "accepted" && objective.Status == domain.ObjectiveCompleted {
			delivery.State = domain.WorkstreamDeliveryAccepted
		} else if decision == "rejected" && objective.Status == domain.ObjectiveActive {
			delivery.State = domain.WorkstreamDeliveryRejected
		}
	}
	return delivery, eligible && allCompleted, nil
}

func appendDeliveryBlocker(delivery *domain.WorkstreamDelivery, blocker string) {
	if len(delivery.Blockers) < maximumWorkstreamDeliverySamples {
		delivery.Blockers = append(delivery.Blockers, blocker)
	}
}

func hasConsequentialDeliveryBlocker(tasks []workstreamDeliveryTask) bool {
	for _, task := range tasks {
		if task.Status == domain.TaskChangesRequested || task.Status == domain.TaskBlocked || task.Status == domain.TaskFailed || ((task.Assessment == "block" || task.Assessment == "changes_requested") && task.RemediationConsumers == 0) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func (s *Store) AcceptWorkstreamDelivery(ctx context.Context, command DecideWorkstreamDeliveryCommand) (WorkstreamDeliveryMutationResult, error) {
	return s.decideWorkstreamDelivery(ctx, command, true)
}

func (s *Store) RejectWorkstreamDelivery(ctx context.Context, command DecideWorkstreamDeliveryCommand) (WorkstreamDeliveryMutationResult, error) {
	return s.decideWorkstreamDelivery(ctx, command, false)
}

func (s *Store) decideWorkstreamDelivery(ctx context.Context, command DecideWorkstreamDeliveryCommand, accept bool) (WorkstreamDeliveryMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ObjectiveID = strings.TrimSpace(command.ObjectiveID)
	command.ExpectedSHA256 = strings.TrimSpace(command.ExpectedSHA256)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ObjectiveID == "" || command.ExpectedObjectiveRevision < 1 || len(command.ExpectedSHA256) != 64 || !validDeliveryReason(command.Reason, accept) {
		return WorkstreamDeliveryMutationResult{}, &Error{Code: CodeInvalidWorkstreamDelivery, Message: "delivery decision requires workspace, objective, exact objective revision, SHA-256, and a bounded rejection reason"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidWorkstreamDelivery); err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	operation := "workstream.delivery.reject"
	if accept {
		operation = "workstream.delivery.accept"
	}
	requestHash, err := hashCommand(operation, struct {
		WorkspaceIdentifier       string `json:"workspace_identifier"`
		ObjectiveID               string `json:"objective_id"`
		ExpectedObjectiveRevision int64  `json:"expected_objective_revision"`
		ExpectedSHA256            string `json:"expected_sha256"`
		Reason                    string `json:"reason"`
	}{
		WorkspaceIdentifier:       command.WorkspaceIdentifier,
		ObjectiveID:               command.ObjectiveID,
		ExpectedObjectiveRevision: command.ExpectedObjectiveRevision,
		ExpectedSHA256:            command.ExpectedSHA256,
		Reason:                    command.Reason,
	})
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, storageFailure("hash workstream delivery decision", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, storageFailure("begin workstream delivery decision", err)
	}
	defer tx.Rollback()
	var replay WorkstreamDeliveryMutationResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, &replay); err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	objective, err := queryObjective(ctx, tx, workspace.ID, command.ObjectiveID)
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	delivery, eligible, err := deriveWorkstreamDelivery(ctx, tx, objective)
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	if objective.Revision != command.ExpectedObjectiveRevision || delivery.SHA256 != command.ExpectedSHA256 {
		return WorkstreamDeliveryMutationResult{}, &Error{Code: CodeWorkstreamDeliveryStale, Message: "workstream delivery changed; inspect the new exact revision before deciding"}
	}
	if accept && (!eligible || objective.Status != domain.ObjectiveActive) {
		return WorkstreamDeliveryMutationResult{}, &Error{Code: CodeWorkstreamDeliveryNotReady, Message: "workstream delivery is not verified and eligible for owner acceptance"}
	}
	now := s.nowText()
	objective.Revision++
	if accept {
		objective.Status = domain.ObjectiveCompleted
	}
	objective.UpdatedAt, objective.UpdatedBy = now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE objectives SET status=?,revision=?,updated_at=?,updated_by=? WHERE id=?", objective.Status, objective.Revision, now, localOwnerActorID, objective.ID); err != nil {
		return WorkstreamDeliveryMutationResult{}, storageFailure("update workstream delivery objective", err)
	}
	decision := "rejected"
	if accept {
		decision = "accepted"
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "objective", objective.ID, objective.Revision, objectiveUpdated, command.CorrelationID, now, map[string]any{
		"title": objective.Title, "status": objective.Status, "budget": objective.Budget,
		"delivery": map[string]any{"decision": decision, "sha256": delivery.SHA256, "reason": command.Reason},
	})
	if err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	delivery.ObjectiveRevision = objective.Revision
	delivery.State = domain.WorkstreamDeliveryRejected
	if accept {
		delivery.State = domain.WorkstreamDeliveryAccepted
	}
	delivery.DecisionReason, delivery.DecisionAt, delivery.DecisionEventSequence = command.Reason, now, sequence
	result := WorkstreamDeliveryMutationResult{Delivery: delivery, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, result, now); err != nil {
		return WorkstreamDeliveryMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkstreamDeliveryMutationResult{}, storageFailure("commit workstream delivery decision", err)
	}
	return result, nil
}

func validDeliveryReason(value string, accept bool) bool {
	if accept && value == "" {
		return true
	}
	return value != "" && len(value) <= 2048 && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && value == strings.TrimSpace(value)
}
