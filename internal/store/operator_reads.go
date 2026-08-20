package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

// readCursor is the sole current keyset cursor representation. The query
// fingerprint binds it to an exact method, resolved scope, and filter set.
type readCursor struct {
	Query     string `json:"q"`
	Key       string `json:"k,omitempty"`
	ID        string `json:"i,omitempty"`
	Sequence  int64  `json:"s,omitempty"`
	HighWater int64  `json:"h,omitempty"`
}

func readPageLimit(value, maximum int) (int, error) {
	if value == 0 {
		return DefaultReadPageLimit, nil
	}
	if value < 1 || value > maximum {
		return 0, invalidCursor(fmt.Sprintf("limit must be between 1 and %d", maximum))
	}
	return value, nil
}

func readQueryFingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(strconv.Itoa(len(part))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(part))
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func encodeReadCursor(cursor readCursor) (string, error) {
	if cursor.Query == "" {
		return "", invalidCursor("cursor query fingerprint is missing")
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", storageFailure("encode read cursor", err)
	}
	value := base64.RawURLEncoding.EncodeToString(encoded)
	if len(value) > MaximumReadCursorBytes {
		return "", storageFailure("encode read cursor", errors.New("cursor exceeds 256 bytes"))
	}
	return value, nil
}

func decodeReadCursor(value, expectedQuery string) (readCursor, error) {
	if value == "" {
		return readCursor{Query: expectedQuery}, nil
	}
	if len(value) > MaximumReadCursorBytes || strings.TrimSpace(value) != value {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cursor readCursor
	if err := decoder.Decode(&cursor); err != nil {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(data, canonical) || base64.RawURLEncoding.EncodeToString(canonical) != value {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	if cursor.Query != expectedQuery {
		return readCursor{}, invalidCursor("cursor does not match the requested scope and filters")
	}
	return cursor, nil
}

func decodeRecordCursor(value, expectedQuery string) (readCursor, error) {
	cursor, err := decodeReadCursor(value, expectedQuery)
	if err != nil || value == "" {
		return cursor, err
	}
	if cursor.Key == "" || cursor.ID == "" || cursor.Sequence != 0 || cursor.HighWater != 0 {
		return readCursor{}, invalidCursor("cursor is malformed")
	}
	return cursor, nil
}

func invalidCursor(message string) *Error {
	return &Error{Code: CodeInvalidCursor, Message: message}
}

func (s *Store) ListWorkspaces(ctx context.Context, query ListWorkspacesQuery) (WorkspacePage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return WorkspacePage{}, err
	}
	fingerprint := readQueryFingerprint("workspace.list")
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return WorkspacePage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkspacePage{}, storageFailure("begin workspace page snapshot", err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorWorkspaces(ctx)
	if err != nil {
		return WorkspacePage{}, storageFailure("count workspaces", err)
	}
	rows, err := queries.ListOperatorWorkspaces(ctx, dbgen.ListOperatorWorkspacesParams{
		CursorName: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return WorkspacePage{}, storageFailure("list workspaces", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Workspace, len(rows))
	for index, row := range rows {
		values[index] = domain.Workspace{
			ID: row.ID, Name: row.Name, Revision: row.Revision, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		}
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.Workspace) (string, string) { return value.Name, value.ID })
	if err != nil {
		return WorkspacePage{}, err
	}
	return WorkspacePage{Workspaces: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListProjects(ctx context.Context, query ListProjectsQuery) (ProjectPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return ProjectPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectPage{}, storageFailure("begin project page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return ProjectPage{}, err
	}
	fingerprint := readQueryFingerprint("project.list", workspace.ID)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return ProjectPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorProjects(ctx, workspace.ID)
	if err != nil {
		return ProjectPage{}, storageFailure("count projects", err)
	}
	rows, err := queries.ListOperatorProjects(ctx, dbgen.ListOperatorProjectsParams{
		WorkspaceID: workspace.ID, CursorName: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return ProjectPage{}, storageFailure("list projects", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Project, len(rows))
	for index, row := range rows {
		values[index] = domain.Project{
			ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, Revision: row.Revision,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		}
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.Project) (string, string) { return value.Name, value.ID })
	if err != nil {
		return ProjectPage{}, err
	}
	return ProjectPage{Projects: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListAgents(ctx context.Context, query ListAgentsQuery) (AgentPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return AgentPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentPage{}, storageFailure("begin agent page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return AgentPage{}, err
	}
	fingerprint := readQueryFingerprint("agent.list", workspace.ID)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return AgentPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorAgents(ctx, workspace.ID)
	if err != nil {
		return AgentPage{}, storageFailure("count agents", err)
	}
	rows, err := queries.ListOperatorAgents(ctx, dbgen.ListOperatorAgentsParams{
		WorkspaceID: workspace.ID, CursorName: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return AgentPage{}, storageFailure("list agents", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.AgentDefinition, len(rows))
	for index, row := range rows {
		values[index] = domain.AgentDefinition{
			ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, Role: row.Role,
			Provider: row.Provider, Runtime: row.Runtime, Enabled: row.Enabled != 0,
			MaxConcurrency: int(row.MaxConcurrency), Revision: row.Revision, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		}
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.AgentDefinition) (string, string) { return value.Name, value.ID })
	if err != nil {
		return AgentPage{}, err
	}
	return AgentPage{Agents: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListObjectives(ctx context.Context, query ListObjectivesQuery) (ObjectivePage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return ObjectivePage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ObjectivePage{}, storageFailure("begin objective page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return ObjectivePage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return ObjectivePage{}, err
	}
	fingerprint := readQueryFingerprint("objective.list", workspace.ID, projectID)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return ObjectivePage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorObjectives(ctx, dbgen.CountOperatorObjectivesParams{WorkspaceID: workspace.ID, ProjectID: projectID})
	if err != nil {
		return ObjectivePage{}, storageFailure("count objectives", err)
	}
	rows, err := queries.ListOperatorObjectives(ctx, dbgen.ListOperatorObjectivesParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, CursorKey: cursor.Key,
		CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return ObjectivePage{}, storageFailure("list objectives", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Objective, len(rows))
	for index, row := range rows {
		values[index] = domain.Objective{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, PrimaryCheckoutID: row.PrimaryCheckoutID, Title: row.Title,
			Status: row.Status, Budget: domain.Budget{TokenLimit: row.BudgetTokens, CostCents: row.BudgetCostCents, TimeSeconds: row.BudgetTimeSeconds},
			Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		}
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.Objective) (string, string) { return value.CreatedAt, value.ID })
	if err != nil {
		return ObjectivePage{}, err
	}
	return ObjectivePage{Objectives: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListTasks(ctx context.Context, query ListTasksQuery) (TaskPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return TaskPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskPage{}, storageFailure("begin task page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return TaskPage{}, err
	}
	projectID := ""
	if strings.TrimSpace(query.ProjectIdentifier) != "" {
		project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(query.ProjectIdentifier))
		if err != nil {
			return TaskPage{}, err
		}
		projectID = project.ID
	}
	ready := int64(0)
	if query.ReadyOnly {
		ready = 1
	}
	fingerprint := readQueryFingerprint("task.list", workspace.ID, projectID, strconv.FormatBool(query.ReadyOnly))
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return TaskPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorTasks(ctx, dbgen.CountOperatorTasksParams{WorkspaceID: workspace.ID, ProjectID: projectID, ReadyOnly: ready})
	if err != nil {
		return TaskPage{}, storageFailure("count tasks", err)
	}
	ids, err := queries.ListOperatorTaskIDs(ctx, dbgen.ListOperatorTaskIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, ReadyOnly: ready,
		CursorKey: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return TaskPage{}, storageFailure("list task ids", err)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	values := make([]domain.TaskDetail, 0, len(ids))
	for _, id := range ids {
		task, err := queryTask(ctx, tx, workspace.ID, id)
		if err != nil {
			return TaskPage{}, err
		}
		detail, err := taskDetailInTransaction(ctx, tx, task)
		if err != nil {
			return TaskPage{}, err
		}
		values = append(values, detail)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.TaskDetail) (string, string) { return value.Task.CreatedAt, value.Task.ID })
	if err != nil {
		return TaskPage{}, err
	}
	return TaskPage{Tasks: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListRuns(ctx context.Context, query ListRunsQuery) (RunPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return RunPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	if status != "" && !validRunStatus(status) {
		return RunPage{}, &Error{Code: CodeInvalidRun, Message: fmt.Sprintf("unsupported run status %q", status)}
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RunPage{}, storageFailure("begin run page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return RunPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return RunPage{}, err
	}
	taskID := strings.TrimSpace(query.TaskID)
	fingerprint := readQueryFingerprint("run.list", workspace.ID, projectID, taskID, status)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return RunPage{}, err
	}
	queries := dbgen.New(tx)
	filter := dbgen.CountOperatorRunsParams{WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: taskID, Status: status}
	total, err := queries.CountOperatorRuns(ctx, filter)
	if err != nil {
		return RunPage{}, storageFailure("count runs", err)
	}
	rows, err := queries.ListOperatorRuns(ctx, dbgen.ListOperatorRunsParams{
		RuntimeNodeID: s.runtimeNodeID, RuntimeNodeFingerprint: s.runtimeNodeFingerprint,
		WorkspaceID: workspace.ID, ProjectID: projectID, TaskID: taskID, Status: status, CursorKey: cursor.Key,
		CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return RunPage{}, storageFailure("list runs", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.RunSummary, len(rows))
	runtimeHandleBoundIDs := make(map[string]bool, len(rows))
	for index, row := range rows {
		values[index] = domain.RunSummary{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, TaskID: row.TaskID,
			AgentID: row.AgentID, Runtime: row.Runtime, Provider: row.Provider, Status: row.Status,
			Assessment: row.Assessment, BlockedQuestion: row.BlockedQuestion, ResultSummary: row.ResultSummary, FailureCode: row.FailureCode,
			Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		}
		runtimeHandleBoundIDs[row.ID] = row.RuntimeBindingCurrent == 1
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.RunSummary) (string, string) { return value.CreatedAt, value.ID })
	if err != nil {
		return RunPage{}, err
	}
	return RunPage{Runs: values, RuntimeHandleBoundIDs: runtimeHandleBoundIDs, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListClaims(ctx context.Context, query ListClaimsQuery) (ClaimPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return ClaimPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClaimPage{}, storageFailure("begin claim page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return ClaimPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return ClaimPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	if status != "" && status != domain.ClaimActive && status != domain.ClaimExpired && status != domain.ClaimReleased {
		return ClaimPage{}, &Error{Code: CodeInvalidClaim, Message: "claim status must be active, expired, or released"}
	}
	fingerprint := readQueryFingerprint("claim.list", workspace.ID, projectID, status)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return ClaimPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorClaims(ctx, dbgen.CountOperatorClaimsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: status})
	if err != nil {
		return ClaimPage{}, storageFailure("count claims", err)
	}
	ids, err := queries.ListOperatorClaimIDs(ctx, dbgen.ListOperatorClaimIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: status,
		CursorKey: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return ClaimPage{}, storageFailure("list claim ids", err)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	values := make([]domain.WorkClaim, 0, len(ids))
	for _, id := range ids {
		value, err := queryClaim(ctx, tx, workspace.ID, id)
		if err != nil {
			return ClaimPage{}, err
		}
		values = append(values, value)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.WorkClaim) (string, string) { return value.CreatedAt, value.ID })
	if err != nil {
		return ClaimPage{}, err
	}
	return ClaimPage{Claims: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListOverlaps(ctx context.Context, query ListOverlapsQuery) (OverlapPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return OverlapPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OverlapPage{}, storageFailure("begin overlap page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return OverlapPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return OverlapPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	if status != "" && status != domain.OverlapOpen && status != domain.OverlapResolved {
		return OverlapPage{}, &Error{Code: CodeInvalidClaim, Message: "overlap status must be open or resolved"}
	}
	fingerprint := readQueryFingerprint("overlap.list", workspace.ID, projectID, status)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return OverlapPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorOverlaps(ctx, dbgen.CountOperatorOverlapsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: status})
	if err != nil {
		return OverlapPage{}, storageFailure("count overlaps", err)
	}
	ids, err := queries.ListOperatorOverlapIDs(ctx, dbgen.ListOperatorOverlapIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: status,
		CursorKey: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return OverlapPage{}, storageFailure("list overlap ids", err)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	values := make([]domain.WorkOverlap, 0, len(ids))
	for _, id := range ids {
		value, err := queryOverlap(ctx, tx, workspace.ID, id)
		if err != nil {
			return OverlapPage{}, err
		}
		values = append(values, value)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.WorkOverlap) (string, string) { return value.DetectedAt, value.ID })
	if err != nil {
		return OverlapPage{}, err
	}
	return OverlapPage{Overlaps: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListClaimDrifts(ctx context.Context, query ListClaimDriftsQuery) (DriftPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return DriftPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DriftPage{}, storageFailure("begin claim drift page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return DriftPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return DriftPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	if status != "" && status != domain.DriftOpen && status != domain.DriftResolved {
		return DriftPage{}, &Error{Code: CodeInvalidClaim, Message: "drift status must be open or resolved"}
	}
	fingerprint := readQueryFingerprint("drift.list", workspace.ID, projectID, status)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return DriftPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorClaimDrifts(ctx, dbgen.CountOperatorClaimDriftsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: status})
	if err != nil {
		return DriftPage{}, storageFailure("count claim drifts", err)
	}
	ids, err := queries.ListOperatorClaimDriftIDs(ctx, dbgen.ListOperatorClaimDriftIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: status, CursorKey: cursor.Key,
		CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return DriftPage{}, storageFailure("list claim drift ids", err)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	values := make([]domain.ClaimDrift, 0, len(ids))
	for _, id := range ids {
		value, err := queryDrift(ctx, tx, workspace.ID, id)
		if err != nil {
			return DriftPage{}, err
		}
		values = append(values, value)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.ClaimDrift) (string, string) { return value.FirstObservedAt, value.ID })
	if err != nil {
		return DriftPage{}, err
	}
	return DriftPage{Drifts: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListMeetings(ctx context.Context, query ListMeetingsQuery) (MeetingPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return MeetingPage{}, err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MeetingPage{}, storageFailure("begin meeting page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return MeetingPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return MeetingPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	if status != "" && !validMeetingStatus(status) {
		return MeetingPage{}, &Error{Code: CodeInvalidMeeting, Message: "meeting status is invalid"}
	}
	fingerprint := readQueryFingerprint("meeting.list", workspace.ID, projectID, status)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return MeetingPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorMeetings(ctx, dbgen.CountOperatorMeetingsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: status})
	if err != nil {
		return MeetingPage{}, storageFailure("count meetings", err)
	}
	rows, err := queries.ListOperatorMeetings(ctx, dbgen.ListOperatorMeetingsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: status,
		CursorKey: cursor.Key, CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return MeetingPage{}, storageFailure("list meetings", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Meeting, 0, len(rows))
	for _, row := range rows {
		var actions []string
		if err := json.Unmarshal([]byte(row.AllowedActionsJson), &actions); err != nil {
			return MeetingPage{}, storageFailure("decode meeting allowed actions", err)
		}
		if actions == nil {
			return MeetingPage{}, storageFailure("decode meeting allowed actions", errors.New("allowed actions must be an array"))
		}
		value := domain.Meeting{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, OverlapID: row.OverlapID,
			Agenda: row.Agenda, FacilitatorAgentID: row.FacilitatorAgentID, Policy: row.Policy,
			AllowedActions: actions, Status: row.Status, FrozenInputHash: row.FrozenInputHash,
			DeadlineAt: row.DeadlineAt, Revision: row.Revision, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		}
		if row.ReviewerAgentID != nil {
			value.ReviewerAgentID = *row.ReviewerAgentID
		}
		if row.StalledReason != nil {
			value.StalledReason = *row.StalledReason
		}
		values = append(values, value)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.Meeting) (string, string) { return value.CreatedAt, value.ID })
	if err != nil {
		return MeetingPage{}, err
	}
	return MeetingPage{Meetings: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) ListEvents(ctx context.Context, query ListEventsQuery) (EventPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumEventPageLimit)
	if err != nil {
		return EventPage{}, err
	}
	if query.After < 0 {
		return EventPage{}, invalidCursor("event after cursor must be zero or greater")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventPage{}, storageFailure("begin event page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return EventPage{}, err
	}
	queries := dbgen.New(tx)
	currentHighWater, err := queries.MaxEventSequence(ctx)
	if err != nil {
		return EventPage{}, storageFailure("capture event high-water", err)
	}
	fingerprint := readQueryFingerprint("events.list", workspace.ID, strconv.FormatInt(query.After, 10))
	cursor, err := decodeReadCursor(query.Cursor, fingerprint)
	if err != nil {
		return EventPage{}, err
	}
	highWater := currentHighWater
	pageAfter := query.After
	if query.Cursor != "" {
		if cursor.Key != "" || cursor.ID != "" || cursor.Sequence <= query.After || cursor.HighWater < cursor.Sequence || cursor.HighWater > currentHighWater {
			return EventPage{}, invalidCursor("event cursor is malformed or its journal was rewound")
		}
		highWater = cursor.HighWater
		pageAfter = cursor.Sequence
	}
	unknown, err := queries.FindFirstUnsupportedOperatorEvent(ctx, dbgen.FindFirstUnsupportedOperatorEventParams{
		WorkspaceID: workspace.ID, AfterSequence: query.After, HighWater: highWater,
	})
	if err == nil {
		return EventPage{}, unsupportedOperatorEvent(unknown.Type, unknown.Sequence)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EventPage{}, storageFailure("classify frozen event interval", err)
	}
	total, err := queries.CountOperatorEvents(ctx, dbgen.CountOperatorEventsParams{
		WorkspaceID: workspace.ID, AfterSequence: query.After, HighWater: highWater,
	})
	if err != nil {
		return EventPage{}, storageFailure("count event page", err)
	}
	rows, err := queries.ListOperatorEvents(ctx, dbgen.ListOperatorEventsParams{
		WorkspaceID: workspace.ID, PageAfter: pageAfter, HighWater: highWater, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return EventPage{}, storageFailure("list event page", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		value, err := eventFromOperatorRow(row.EventID, row.Sequence, row.Type, row.SchemaVersion,
			row.OccurredAt, row.RecordedAt, row.ActorID, row.ActorType, row.WorkspaceID,
			row.EntityType, row.EntityID, row.EntityRevision, row.CorrelationID, row.CausationID, row.DataJson)
		if err != nil {
			return EventPage{}, err
		}
		values = append(values, value)
	}
	next := ""
	if hasMore {
		next, err = encodeReadCursor(readCursor{Query: fingerprint, Sequence: values[len(values)-1].Sequence, HighWater: highWater})
		if err != nil {
			return EventPage{}, err
		}
	}
	return EventPage{WorkspaceID: workspace.ID, HighWater: highWater, Events: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) EventTimeline(ctx context.Context, query EventTimelineQuery) (EventPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return EventPage{}, err
	}
	entityType := strings.TrimSpace(query.EntityType)
	entityID := strings.TrimSpace(query.EntityID)
	if entityType == "" || len(entityType) > 64 || entityID == "" || len(entityID) > 128 {
		return EventPage{}, invalidCursor("entity timeline requires a bounded entity type and id")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventPage{}, storageFailure("begin entity timeline snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return EventPage{}, err
	}
	queries := dbgen.New(tx)
	currentHighWater, err := queries.MaxEventSequence(ctx)
	if err != nil {
		return EventPage{}, storageFailure("capture entity timeline high-water", err)
	}
	fingerprint := readQueryFingerprint("events.timeline", workspace.ID, entityType, entityID)
	cursor, err := decodeReadCursor(query.Cursor, fingerprint)
	if err != nil {
		return EventPage{}, err
	}
	highWater := currentHighWater
	pageBefore := int64(0)
	if query.Cursor != "" {
		if cursor.Key != "" || cursor.ID != "" || cursor.Sequence <= 0 || cursor.HighWater < cursor.Sequence || cursor.HighWater > currentHighWater {
			return EventPage{}, invalidCursor("entity timeline cursor is malformed or its journal was rewound")
		}
		highWater = cursor.HighWater
		pageBefore = cursor.Sequence
	}
	unknown, err := queries.FindFirstUnsupportedOperatorEntityEvent(ctx, dbgen.FindFirstUnsupportedOperatorEntityEventParams{
		WorkspaceID: workspace.ID, EntityType: entityType, EntityID: entityID, HighWater: highWater,
	})
	if err == nil {
		return EventPage{}, unsupportedOperatorEvent(unknown.Type, unknown.Sequence)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EventPage{}, storageFailure("classify frozen entity timeline", err)
	}
	total, err := queries.CountOperatorEntityEvents(ctx, dbgen.CountOperatorEntityEventsParams{
		WorkspaceID: workspace.ID, EntityType: entityType, EntityID: entityID, HighWater: highWater,
	})
	if err != nil {
		return EventPage{}, storageFailure("count entity timeline", err)
	}
	rows, err := queries.ListOperatorEntityEvents(ctx, dbgen.ListOperatorEntityEventsParams{
		WorkspaceID: workspace.ID, EntityType: entityType, EntityID: entityID,
		HighWater: highWater, CursorSequence: pageBefore, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return EventPage{}, storageFailure("list entity timeline", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	values := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		value, err := eventFromOperatorRow(row.EventID, row.Sequence, row.Type, row.SchemaVersion,
			row.OccurredAt, row.RecordedAt, row.ActorID, row.ActorType, row.WorkspaceID,
			row.EntityType, row.EntityID, row.EntityRevision, row.CorrelationID, row.CausationID, row.DataJson)
		if err != nil {
			return EventPage{}, err
		}
		values = append(values, value)
	}
	next := ""
	if hasMore {
		next, err = encodeReadCursor(readCursor{Query: fingerprint, Sequence: values[len(values)-1].Sequence, HighWater: highWater})
		if err != nil {
			return EventPage{}, err
		}
	}
	return EventPage{WorkspaceID: workspace.ID, HighWater: highWater, Events: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func nextRecordCursor[T any](fingerprint string, hasMore bool, values []T, key func(T) (string, string)) (string, error) {
	if !hasMore {
		return "", nil
	}
	lastKey, lastID := key(values[len(values)-1])
	return encodeReadCursor(readCursor{Query: fingerprint, Key: lastKey, ID: lastID})
}

func resolveOptionalProjectID(ctx context.Context, database queryRower, workspaceID, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "", nil
	}
	project, err := queryProject(ctx, database, workspaceID, identifier)
	if err != nil {
		return "", err
	}
	return project.ID, nil
}

func validMeetingStatus(value string) bool {
	return value == domain.MeetingGatheringPositions || value == domain.MeetingFacilitatorPending ||
		value == domain.MeetingAwaitingApproval || value == domain.MeetingAwaitingReviewer ||
		value == domain.MeetingConcluded || value == domain.MeetingStalled || value == domain.MeetingCancelled
}

func eventFromOperatorRow(eventID string, sequence int64, eventType string, schemaVersion int64,
	occurredAt, recordedAt, actorID, actorType, workspaceID, entityType, entityID string,
	entityRevision int64, correlationID string, causationID *string, data string,
) (domain.Event, error) {
	if !domain.KnownEventType(eventType) {
		return domain.Event{}, unsupportedOperatorEvent(eventType, sequence)
	}
	if schemaVersion < 1 || int64(int(schemaVersion)) != schemaVersion {
		return domain.Event{}, storageFailure("decode canonical event", errors.New("event schema version is invalid"))
	}
	if causationID != nil && *causationID == "" {
		return domain.Event{}, storageFailure("decode canonical event", errors.New("present event causation id is empty"))
	}
	value := domain.Event{
		EventID: eventID, Sequence: sequence, Type: eventType, SchemaVersion: int(schemaVersion),
		OccurredAt: occurredAt, RecordedAt: recordedAt,
		Actor: domain.EventActor{ActorID: actorID, ActorType: actorType}, WorkspaceID: workspaceID,
		Entity:        domain.EventEntity{Type: entityType, ID: entityID, Revision: entityRevision},
		CorrelationID: correlationID, Data: json.RawMessage(data),
	}
	if causationID != nil {
		value.CausationID = *causationID
	}
	if !domain.ValidEvent(value) {
		return domain.Event{}, storageFailure("decode canonical event", errors.New("event envelope is invalid"))
	}
	return value, nil
}

func unsupportedOperatorEvent(eventType string, sequence int64) *Error {
	return &Error{
		Code:    CodeUnsupportedOperatorEvent,
		Message: fmt.Sprintf("operator read stopped before unsupported workspace event %q at sequence %d", eventType, sequence),
	}
}
