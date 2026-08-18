package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	checkNotificationQueuedEvent     = "check.notification_queued"
	checkNotificationUnroutableEvent = "check.notification_unroutable"
)

type pendingCheckNotification struct {
	receipt  domain.CheckNotificationReceipt
	threadID string
	body     string
}

type pendingCheckRouteFailure struct {
	failure domain.CheckRouteFailure
}

func (s *Store) materializeCheckNotifications(ctx context.Context, tx *sql.Tx, work CheckWork, resultID, outcome, freshness string, freshnessRevision int64, now, correlationID string) (int, int, error) {
	triggers := []string{domain.CheckRouteNonpass}
	if outcome == domain.CheckOutcomePassed {
		triggers = []string{domain.CheckRoutePass}
	}
	if freshness == domain.CheckFreshnessStale {
		triggers = append(triggers, domain.CheckRouteStale)
	}
	return s.materializeCheckNotificationsForTriggers(ctx, tx, work, resultID, outcome, freshness, freshnessRevision, now, correlationID, outcome != domain.CheckOutcomePassed, triggers)
}

// A freshness transition must not repeat the terminal task-owner or nonpass
// delivery. Only routes explicitly authored for the stale trigger participate.
func (s *Store) materializeStaleCheckNotifications(ctx context.Context, tx *sql.Tx, work CheckWork, resultID, outcome string, freshnessRevision int64, now, correlationID string) (int, int, error) {
	return s.materializeCheckNotificationsForTriggers(ctx, tx, work, resultID, outcome, domain.CheckFreshnessStale, freshnessRevision, now, correlationID, false, []string{domain.CheckRouteStale})
}

func (s *Store) materializeCheckNotificationsForTriggers(ctx context.Context, tx *sql.Tx, work CheckWork, resultID, outcome, freshness string, freshnessRevision int64, now, correlationID string, includeTaskOwner bool, triggers []string) (int, int, error) {
	queries := dbgen.New(tx)
	plans := []pendingCheckNotification{}
	failures := []pendingCheckRouteFailure{}
	if includeTaskOwner {
		owner, err := queries.GetCurrentCheckTaskOwner(ctx, dbgen.GetCurrentCheckTaskOwnerParams{TaskID: work.Run.TaskID, ObservedAt: now})
		if errors.Is(err, sql.ErrNoRows) {
			id, _ := randomID("checkroutefail_")
			failures = append(failures, pendingCheckRouteFailure{failure: domain.CheckRouteFailure{
				ID: id, CheckResultID: resultID, FreshnessRevision: freshnessRevision, Duty: domain.CheckDutyTaskOwner,
				Code: "unroutable", Diagnostic: "the failed check task has no current active owner assignment", CreatedAt: now,
			}})
		} else if err != nil {
			return 0, 0, storageFailure("resolve current check task owner", err)
		} else {
			plans = append(plans, newPendingCheckNotification(resultID, freshnessRevision, "", domain.CheckDutyTaskOwner, owner.AgentID, owner.AgentRevision, owner.AssignmentID, owner.AssignmentRevision, outcome, freshness, now))
		}
	}
	seenRoute := map[string]bool{}
	definitionID := work.Run.DefinitionID
	definitionRevision := work.Run.DefinitionContentRevision
	for _, trigger := range triggers {
		routes, err := queries.ListApplicableCheckRoutes(ctx, dbgen.ListApplicableCheckRoutesParams{
			WorkspaceID: work.Run.WorkspaceID, ProjectID: work.Run.ProjectID,
			DefinitionID: &definitionID, DefinitionContentRevision: &definitionRevision, Trigger: trigger,
		})
		if err != nil {
			return 0, 0, storageFailure("list applicable check routes", err)
		}
		for _, route := range routes {
			if seenRoute[route.ID] {
				continue
			}
			seenRoute[route.ID] = true
			if route.RecipientAvailable == 0 {
				id, _ := randomID("checkroutefail_")
				failures = append(failures, pendingCheckRouteFailure{failure: domain.CheckRouteFailure{
					ID: id, CheckResultID: resultID, FreshnessRevision: freshnessRevision, RouteID: route.ID, Duty: route.Duty,
					RecipientAgentID: route.AgentID, RecipientAgentRevision: route.AgentRevision,
					Code: "recipient_unavailable", Diagnostic: "the route's exact recipient agent revision is not currently enabled", CreatedAt: now,
				}})
				continue
			}
			plans = append(plans, newPendingCheckNotification(resultID, freshnessRevision, route.ID, route.Duty, route.AgentID, route.AgentRevision, "", 0, outcome, freshness, now))
		}
	}
	for _, plan := range plans {
		plan := plan
		err := s.withCheckMutationSeal(func() error {
			return queries.InsertCheckNotificationReceipt(ctx, dbgen.InsertCheckNotificationReceiptParams{
				ID: plan.receipt.ID, WorkspaceID: work.Run.WorkspaceID, ProjectID: work.Run.ProjectID, TaskID: work.Run.TaskID,
				CheckResultID: resultID, FreshnessRevision: freshnessRevision, RouteID: nullableInterface(plan.receipt.RouteID), Duty: plan.receipt.Duty,
				RecipientAgentID: plan.receipt.RecipientAgentID, RecipientAgentRevision: plan.receipt.RecipientAgentRevision,
				AssignmentID: nullableInterface(plan.receipt.AssignmentID), AssignmentRevision: nullablePositiveInt(plan.receipt.AssignmentRevision),
				MessageID: plan.receipt.MessageID, CreatedAt: now,
			})
		})
		if err != nil {
			return 0, 0, checkConstraint("record check notification", CodeCheckRunConflict, err)
		}
	}
	for _, plan := range failures {
		failure := plan.failure
		err := s.withCheckMutationSeal(func() error {
			return queries.InsertCheckRouteFailure(ctx, dbgen.InsertCheckRouteFailureParams{
				ID: failure.ID, WorkspaceID: work.Run.WorkspaceID, ProjectID: work.Run.ProjectID, TaskID: work.Run.TaskID,
				CheckResultID: resultID, FreshnessRevision: freshnessRevision, RouteID: nullableInterface(failure.RouteID), Duty: failure.Duty,
				RecipientAgentID: nullableInterface(failure.RecipientAgentID), RecipientAgentRevision: nullablePositiveInt(failure.RecipientAgentRevision),
				AssignmentID: nullableInterface(failure.AssignmentID), AssignmentRevision: nullablePositiveInt(failure.AssignmentRevision),
				Code: failure.Code, Diagnostic: failure.Diagnostic, CreatedAt: now,
			})
		})
		if err != nil {
			return 0, 0, checkConstraint("record check route failure", CodeCheckRunConflict, err)
		}
	}
	if err := s.runMutationHook(MutationAfterCheckNotification); err != nil {
		return 0, 0, err
	}
	projectID, taskID := work.Run.ProjectID, work.Run.TaskID
	for _, plan := range plans {
		plan := plan
		err := s.withCheckMutationSeal(func() error {
			if err := queries.InsertCheckNotificationThread(ctx, dbgen.InsertCheckNotificationThreadParams{ID: plan.threadID, WorkspaceID: work.Run.WorkspaceID, ProjectID: &projectID, TaskID: &taskID, Subject: "Crewfold check result", CreatedAt: now}); err != nil {
				return err
			}
			if err := queries.InsertCheckNotificationMessage(ctx, dbgen.InsertCheckNotificationMessageParams{ID: plan.receipt.MessageID, WorkspaceID: work.Run.WorkspaceID, ThreadID: plan.threadID, ProjectID: &projectID, TaskID: &taskID, Body: plan.body, CreatedAt: now}); err != nil {
				return err
			}
			if err := queries.InsertCheckNotificationRecipient(ctx, dbgen.InsertCheckNotificationRecipientParams{MessageID: plan.receipt.MessageID, RecipientAgentID: plan.receipt.RecipientAgentID, QueuedAt: now}); err != nil {
				return err
			}
			if targetRunID, err := queries.GetCheckNotificationWakeTarget(ctx, dbgen.GetCheckNotificationWakeTargetParams{WorkspaceID: work.Run.WorkspaceID, AgentID: plan.receipt.RecipientAgentID, ProjectID: work.Run.ProjectID}); err == nil {
				wakeID, _ := randomID("wake_")
				if err := queries.InsertCheckNotificationWake(ctx, dbgen.InsertCheckNotificationWakeParams{ID: wakeID, MessageID: plan.receipt.MessageID, RecipientAgentID: plan.receipt.RecipientAgentID, TargetRunID: &targetRunID, CreatedAt: now}); err != nil {
					return err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			changed, err := queries.AdvanceCheckNotificationThread(ctx, dbgen.AdvanceCheckNotificationThreadParams{UpdatedAt: now, ID: plan.threadID})
			if err == nil && changed != 1 {
				return errors.New("check notification thread revision was not advanced")
			}
			return err
		})
		if err != nil {
			return 0, 0, checkConstraint("materialize check notification message", CodeCheckRunConflict, err)
		}
	}
	if err := s.runMutationHook(MutationAfterCheckMessage); err != nil {
		return 0, 0, err
	}
	for _, plan := range plans {
		if _, err := appendEventForActor(ctx, tx, work.Run.WorkspaceID, "check_notification", plan.receipt.ID, 1, checkNotificationQueuedEvent, correlationID, now, "crewfold-check-worker", "subsystem", map[string]any{
			"check_result_id": resultID, "freshness_revision": freshnessRevision, "route_id": plan.receipt.RouteID, "duty": plan.receipt.Duty,
			"recipient_agent_id": plan.receipt.RecipientAgentID, "recipient_agent_revision": plan.receipt.RecipientAgentRevision,
			"assignment_id": plan.receipt.AssignmentID, "assignment_revision": plan.receipt.AssignmentRevision, "message_id": plan.receipt.MessageID,
		}); err != nil {
			return 0, 0, err
		}
	}
	for _, plan := range failures {
		failure := plan.failure
		if _, err := appendEventForActor(ctx, tx, work.Run.WorkspaceID, "check_route_failure", failure.ID, 1, checkNotificationUnroutableEvent, correlationID, now, "crewfold-check-worker", "subsystem", map[string]any{
			"check_result_id": resultID, "freshness_revision": freshnessRevision, "route_id": failure.RouteID, "duty": failure.Duty,
			"recipient_agent_id": failure.RecipientAgentID, "recipient_agent_revision": failure.RecipientAgentRevision, "code": failure.Code,
		}); err != nil {
			return 0, 0, err
		}
	}
	return len(plans), len(failures), nil
}

func newPendingCheckNotification(resultID string, freshnessRevision int64, routeID, duty, agentID string, agentRevision int64, assignmentID string, assignmentRevision int64, outcome, freshness, now string) pendingCheckNotification {
	receiptID, _ := randomID("checknotice_")
	messageID, _ := randomID("msg_")
	threadID, _ := randomID("thread_")
	return pendingCheckNotification{
		receipt:  domain.CheckNotificationReceipt{ID: receiptID, CheckResultID: resultID, FreshnessRevision: freshnessRevision, RouteID: routeID, Duty: duty, RecipientAgentID: agentID, RecipientAgentRevision: agentRevision, AssignmentID: assignmentID, AssignmentRevision: assignmentRevision, MessageID: messageID, CreatedAt: now},
		threadID: threadID,
		body:     fmt.Sprintf("Check result %s is %s with %s source freshness. Assigned duty: %s.", resultID, outcome, freshness, duty),
	}
}

func nullableInterface(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func loadCheckNotifications(ctx context.Context, queries *dbgen.Queries, resultID string) ([]domain.CheckNotificationReceipt, []domain.CheckRouteFailure, error) {
	notificationRows, err := queries.ListCheckNotificationReceipts(ctx, resultID)
	if err != nil {
		return nil, nil, err
	}
	notifications := make([]domain.CheckNotificationReceipt, 0, len(notificationRows))
	for _, row := range notificationRows {
		notifications = append(notifications, domain.CheckNotificationReceipt{ID: row.ID, CheckResultID: row.CheckResultID, FreshnessRevision: row.FreshnessRevision, RouteID: stringValue(row.RouteID), Duty: row.Duty, RecipientAgentID: row.RecipientAgentID, RecipientAgentRevision: row.RecipientAgentRevision, AssignmentID: stringValue(row.AssignmentID), AssignmentRevision: int64Value(row.AssignmentRevision), MessageID: row.MessageID, CreatedAt: row.CreatedAt})
	}
	failureRows, err := queries.ListCheckRouteFailures(ctx, resultID)
	if err != nil {
		return nil, nil, err
	}
	failures := make([]domain.CheckRouteFailure, 0, len(failureRows))
	for _, row := range failureRows {
		failures = append(failures, domain.CheckRouteFailure{ID: row.ID, CheckResultID: row.CheckResultID, FreshnessRevision: row.FreshnessRevision, RouteID: stringValue(row.RouteID), Duty: row.Duty, RecipientAgentID: stringValue(row.RecipientAgentID), RecipientAgentRevision: int64Value(row.RecipientAgentRevision), AssignmentID: stringValue(row.AssignmentID), AssignmentRevision: int64Value(row.AssignmentRevision), Code: row.Code, Diagnostic: row.Diagnostic, CreatedAt: row.CreatedAt})
	}
	return notifications, failures, nil
}
