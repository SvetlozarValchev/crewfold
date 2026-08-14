package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"crewfold/internal/domain"
)

const unresolvedRunPredicate = "status IN ('requested','starting','active','blocked','stopping','lost')"
const startingRunPredicate = "status IN ('requested','starting')"

// ExecutionHealth reads one bounded SQLite snapshot. It reports mechanical
// capacity and durable queue facts only; names, roles, and purposes never grant
// execution authority.
func (s *Store) ExecutionHealth(ctx context.Context) (domain.ExecutionHealth, error) {
	observed := s.clock().UTC()
	health := domain.ExecutionHealth{
		ObservedAt: observed.Format(time.RFC3339Nano),
		Workspaces: []domain.WorkspaceExecutionHealth{}, Projects: []domain.ProjectExecutionHealth{},
		Providers: []domain.ProviderExecutionHealth{}, Queues: []domain.ExecutionQueueState{},
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ExecutionHealth{}, storageFailure("begin execution health snapshot", err)
	}
	defer tx.Rollback()

	if err := tx.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN `+unresolvedRunPredicate+` THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN `+startingRunPredicate+` THEN 1 ELSE 0 END),0)
FROM runs`).Scan(&health.Node.Unresolved, &health.Node.Starting); err != nil {
		return domain.ExecutionHealth{}, storageFailure("read node execution usage", err)
	}

	workspaceRows, err := tx.QueryContext(ctx, `SELECT workspace.id,
COALESCE(SUM(CASE WHEN `+"run."+unresolvedRunPredicate+` THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN `+"run."+startingRunPredicate+` THEN 1 ELSE 0 END),0)
FROM workspaces workspace LEFT JOIN runs run ON run.workspace_id=workspace.id
GROUP BY workspace.id ORDER BY workspace.id`)
	if err != nil {
		return domain.ExecutionHealth{}, storageFailure("read workspace execution usage", err)
	}
	for workspaceRows.Next() {
		var item domain.WorkspaceExecutionHealth
		if err := workspaceRows.Scan(&item.WorkspaceID, &item.Unresolved, &item.Starting); err != nil {
			workspaceRows.Close()
			return domain.ExecutionHealth{}, storageFailure("scan workspace execution usage", err)
		}
		health.Workspaces = append(health.Workspaces, item)
	}
	if err := workspaceRows.Close(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("close workspace execution usage", err)
	}
	if err := workspaceRows.Err(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("iterate workspace execution usage", err)
	}

	projectRows, err := tx.QueryContext(ctx, `SELECT project.workspace_id,project.id,
COALESCE(SUM(CASE WHEN `+"run."+unresolvedRunPredicate+` THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN `+"run."+startingRunPredicate+` THEN 1 ELSE 0 END),0)
FROM projects project LEFT JOIN runs run ON run.project_id=project.id
GROUP BY project.workspace_id,project.id ORDER BY project.workspace_id,project.id`)
	if err != nil {
		return domain.ExecutionHealth{}, storageFailure("read project execution usage", err)
	}
	for projectRows.Next() {
		var item domain.ProjectExecutionHealth
		if err := projectRows.Scan(&item.WorkspaceID, &item.ProjectID, &item.Unresolved, &item.Starting); err != nil {
			projectRows.Close()
			return domain.ExecutionHealth{}, storageFailure("scan project execution usage", err)
		}
		health.Projects = append(health.Projects, item)
	}
	if err := projectRows.Close(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("close project execution usage", err)
	}
	if err := projectRows.Err(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("iterate project execution usage", err)
	}

	providerRows, err := tx.QueryContext(ctx, `WITH provider AS (
  SELECT workspace_id,provider FROM agents GROUP BY workspace_id,provider
)
SELECT provider.workspace_id,provider.provider,
COALESCE(SUM(CASE WHEN `+"run."+unresolvedRunPredicate+` THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN `+"run."+startingRunPredicate+` THEN 1 ELSE 0 END),0)
FROM provider LEFT JOIN runs run ON run.workspace_id=provider.workspace_id AND run.provider=provider.provider
GROUP BY provider.workspace_id,provider.provider ORDER BY provider.workspace_id,provider.provider`)
	if err != nil {
		return domain.ExecutionHealth{}, storageFailure("read provider execution usage", err)
	}
	for providerRows.Next() {
		var item domain.ProviderExecutionHealth
		if err := providerRows.Scan(&item.WorkspaceID, &item.Provider, &item.Unresolved, &item.Starting); err != nil {
			providerRows.Close()
			return domain.ExecutionHealth{}, storageFailure("scan provider execution usage", err)
		}
		health.Providers = append(health.Providers, item)
	}
	if err := providerRows.Close(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("close provider execution usage", err)
	}
	if err := providerRows.Err(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("iterate provider execution usage", err)
	}

	for _, queue := range []struct {
		name     string
		table    string
		statuses []string
	}{
		{name: "run", table: "run_jobs", statuses: []string{"pending", "leased", "complete"}},
		{name: "check", table: "check_jobs", statuses: []string{"pending", "leased", "complete"}},
		{name: "message_wake", table: "message_wake_jobs", statuses: []string{"pending", "leased", "succeeded", "failed", "failed_unknown"}},
	} {
		for _, status := range queue.statuses {
			item := domain.ExecutionQueueState{Queue: queue.name, Status: status}
			var oldest sql.NullString
			query := fmt.Sprintf("SELECT COUNT(*),MIN(updated_at) FROM %s WHERE status=?", queue.table)
			if err := tx.QueryRowContext(ctx, query, status).Scan(&item.Count, &oldest); err != nil {
				return domain.ExecutionHealth{}, storageFailure("read "+queue.name+" queue state", err)
			}
			if oldest.Valid {
				item.OldestUpdatedAt = oldest.String
				when, err := time.Parse(time.RFC3339Nano, oldest.String)
				if err != nil || when.After(observed) {
					return domain.ExecutionHealth{}, storageFailure("validate "+queue.name+" queue age", fmt.Errorf("noncanonical queue timestamp %q", oldest.String))
				}
				item.OldestAgeMillis = observed.Sub(when).Milliseconds()
			}
			health.Queues = append(health.Queues, item)
		}
	}

	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM run_log_artifacts artifact WHERE artifact.run_id=run.id AND artifact.kind='stdout') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM run_log_artifacts artifact WHERE artifact.run_id=run.id AND artifact.kind='stderr') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM run_log_artifacts artifact WHERE artifact.run_id=run.id AND artifact.kind='stdout')
  AND EXISTS(SELECT 1 FROM run_log_artifacts artifact WHERE artifact.run_id=run.id AND artifact.kind='stderr') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN NOT EXISTS(SELECT 1 FROM run_log_artifacts artifact WHERE artifact.run_id=run.id) THEN 1 ELSE 0 END),0)
FROM runs run WHERE run.status IN ('stopped','review','completed','start_failed','failed')`).Scan(
		&health.TerminalLog.TerminalRuns, &health.TerminalLog.StdoutReferences, &health.TerminalLog.StderrReferences,
		&health.TerminalLog.CompleteStreamPairs, &health.TerminalLog.RunsWithoutReferences,
	); err != nil {
		return domain.ExecutionHealth{}, storageFailure("read terminal run log references", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.ExecutionHealth{}, storageFailure("commit execution health snapshot", err)
	}
	return health, nil
}
