package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// NodeExecutionCapacityLimit is the fixed current-contract ceiling shared by
// transactional admission and read-only health reporting.
const NodeExecutionCapacityLimit = 20

const unresolvedRunStatuses = "('requested','starting','active','blocked','stopping','lost')"

// ExecutionCapacityDetails is the stable, authority-neutral refusal payload
// exposed to the daemon. Role and launch-profile purpose never participate.
type ExecutionCapacityDetails struct {
	Dimension string
	Scope     string
	Actual    int
	Limit     int
}

type ExecutionCapacityError struct {
	Details ExecutionCapacityDetails
}

func (e *ExecutionCapacityError) Error() string {
	return fmt.Sprintf("execution capacity exhausted for %s at %s: actual=%d limit=%d", e.Details.Dimension, e.Details.Scope, e.Details.Actual, e.Details.Limit)
}

func ExecutionCapacityErrorDetails(err error) (ExecutionCapacityDetails, bool) {
	var capacity *ExecutionCapacityError
	if !errors.As(err, &capacity) {
		return ExecutionCapacityDetails{}, false
	}
	return capacity.Details, true
}

type executionCapacityDimension struct {
	dimension string
	scope     string
	actual    int
	limit     int
}

func (s *Store) enforceExecutionCapacity(ctx context.Context, tx *sql.Tx, workspaceID, projectID, provider string) error {
	dimensions, err := s.executionCapacityDimensions(ctx, tx, workspaceID, projectID, provider)
	if err != nil {
		return err
	}
	for _, dimension := range dimensions {
		if dimension.actual >= dimension.limit {
			return &ExecutionCapacityError{Details: ExecutionCapacityDetails{
				Dimension: dimension.dimension, Scope: dimension.scope,
				Actual: dimension.actual, Limit: dimension.limit,
			}}
		}
	}
	return nil
}

func (s *Store) executionCapacityDimensions(ctx context.Context, tx *sql.Tx, workspaceID, projectID, provider string) ([]executionCapacityDimension, error) {
	policy, err := querySupervisorPolicy(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	projectLimit := policy.Limits.DefaultProjectConcurrency
	if value, exists := policy.Limits.ProjectConcurrency[projectID]; exists {
		projectLimit = value
	}
	providerLimit := policy.Limits.DefaultProviderConcurrency
	if value, exists := policy.Limits.ProviderConcurrency[provider]; exists {
		providerLimit = value
	}
	dimensions := []executionCapacityDimension{
		{dimension: "node_unresolved", scope: "node", limit: NodeExecutionCapacityLimit},
		{dimension: "workspace_starting", scope: workspaceID, limit: policy.Limits.MaxStartingRuns},
		{dimension: "workspace_unresolved", scope: workspaceID, limit: policy.Limits.MaxActiveRuns},
		{dimension: "project_unresolved", scope: projectID, limit: projectLimit},
		{dimension: "provider_unresolved", scope: provider, limit: providerLimit},
	}
	queries := []struct {
		statement string
		arguments []any
	}{
		{statement: `SELECT COUNT(*) FROM runs WHERE status IN ` + unresolvedRunStatuses},
		{statement: `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ('requested','starting')`, arguments: []any{workspaceID}},
		{statement: `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ` + unresolvedRunStatuses, arguments: []any{workspaceID}},
		{statement: `SELECT COUNT(*) FROM runs WHERE project_id=? AND status IN ` + unresolvedRunStatuses, arguments: []any{projectID}},
		{statement: `SELECT COUNT(*) FROM runs WHERE workspace_id=? AND provider=? AND status IN ` + unresolvedRunStatuses, arguments: []any{workspaceID, provider}},
	}
	for index, query := range queries {
		if err := tx.QueryRowContext(ctx, query.statement, query.arguments...).Scan(&dimensions[index].actual); err != nil {
			return nil, storageFailure("count execution capacity", err)
		}
	}
	return dimensions, nil
}
