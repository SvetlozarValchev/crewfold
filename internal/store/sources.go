package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
)

const (
	projectRegistered    = "project.registered"
	repositoryRegistered = "repository.registered"
	checkoutRegistered   = "checkout.registered"
	checkoutObserved     = "checkout.git_observed"
)

var repositoryFingerprintPattern = regexp.MustCompile(`^git_[0-9a-f]{64}$`)

func (s *Store) RegisterProject(ctx context.Context, command RegisterProjectCommand) (ProjectRegistrationResult, error) {
	name := strings.TrimSpace(command.Name)
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	writeMode := normalizedWriteMode(command.WriteMode)
	if !workspaceNamePattern.MatchString(name) {
		return ProjectRegistrationResult{}, &Error{Code: CodeInvalidProject, Message: "project name must start with a lowercase letter and contain at most 63 lowercase letters, digits, or hyphens"}
	}
	if workspaceIdentifier == "" || len(workspaceIdentifier) > 128 {
		return ProjectRegistrationResult{}, &Error{Code: CodeInvalidProject, Message: "workspace identifier must contain 1 to 128 characters"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidProject); err != nil {
		return ProjectRegistrationResult{}, err
	}
	if err := validateObservation(command.Observation); err != nil {
		return ProjectRegistrationResult{}, err
	}
	if !validWriteMode(writeMode) {
		return ProjectRegistrationResult{}, &Error{Code: CodeInvalidCheckout, Message: "write mode must be exclusive, claimed, shared, or read_only"}
	}

	requestHash, err := hashCommand("project.add", map[string]any{
		"workspace":   workspaceIdentifier,
		"name":        name,
		"write_mode":  writeMode,
		"observation": command.Observation,
	})
	if err != nil {
		return ProjectRegistrationResult{}, storageFailure("hash project registration", err)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRegistrationResult{}, storageFailure("begin project registration", err)
	}
	defer transaction.Rollback()

	var replay ProjectRegistrationResult
	if found, err := lookupIdempotency(ctx, transaction, key, "project.add", requestHash, &replay); err != nil {
		return ProjectRegistrationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, transaction, workspaceIdentifier)
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	var existingID string
	err = transaction.QueryRowContext(ctx, "SELECT id FROM projects WHERE workspace_id = ? AND name = ?", workspace.ID, name).Scan(&existingID)
	if err == nil {
		return ProjectRegistrationResult{}, &Error{Code: CodeProjectExists, Message: fmt.Sprintf("project %q already exists as %s", name, existingID)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectRegistrationResult{}, storageFailure("check project name", err)
	}
	if err := rejectDuplicateCheckout(ctx, transaction, command.Observation.Path); err != nil {
		return ProjectRegistrationResult{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	projectID, err := randomID("prj_")
	if err != nil {
		return ProjectRegistrationResult{}, storageFailure("generate project id", err)
	}
	project := domain.Project{ID: projectID, WorkspaceID: workspace.ID, Name: name, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO projects(id, workspace_id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, project.ID, project.WorkspaceID, project.Name, project.Revision, project.CreatedAt, project.UpdatedAt, project.CreatedBy, project.UpdatedBy); err != nil {
		return ProjectRegistrationResult{}, storageFailure("insert project projection", err)
	}

	repository, repositoryCreated, err := ensureRepository(ctx, transaction, workspace.ID, command.Observation.Repository, now)
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO project_repositories(project_id, repository_id, attached_at) VALUES (?, ?, ?)", project.ID, repository.ID, now); err != nil {
		return ProjectRegistrationResult{}, storageFailure("attach repository to project", err)
	}
	checkout, err := insertCheckout(ctx, transaction, project.ID, repository.ID, writeMode, command.Observation, now)
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ProjectRegistrationResult{}, err
	}

	sequence, err := appendEvent(ctx, transaction, workspace.ID, "project", project.ID, project.Revision, projectRegistered, correlationID, now, map[string]any{"name": project.Name})
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	if repositoryCreated {
		sequence, err = appendEvent(ctx, transaction, workspace.ID, "repository", repository.ID, repository.Revision, repositoryRegistered, correlationID, now, map[string]any{"fingerprint": repository.Fingerprint, "object_format": repository.ObjectFormat, "root_commits": repository.RootCommits})
		if err != nil {
			return ProjectRegistrationResult{}, err
		}
	}
	sequence, err = appendEvent(ctx, transaction, workspace.ID, "checkout", checkout.ID, checkout.Revision, checkoutRegistered, correlationID, now, checkoutEventData(checkout))
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	result := ProjectRegistrationResult{Project: project, Repository: repository, Checkout: checkout, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ProjectRegistrationResult{}, err
	}
	if err := recordIdempotency(ctx, transaction, key, "project.add", requestHash, result, now); err != nil {
		return ProjectRegistrationResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ProjectRegistrationResult{}, storageFailure("commit project registration", err)
	}
	return result, nil
}

func (s *Store) AddCheckout(ctx context.Context, command AddCheckoutCommand) (CheckoutRegistrationResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	writeMode := normalizedWriteMode(command.WriteMode)
	if workspaceIdentifier == "" || projectIdentifier == "" || len(workspaceIdentifier) > 128 || len(projectIdentifier) > 128 {
		return CheckoutRegistrationResult{}, &Error{Code: CodeInvalidCheckout, Message: "workspace and project identifiers must contain 1 to 128 characters"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidCheckout); err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if err := validateObservation(command.Observation); err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if !validWriteMode(writeMode) {
		return CheckoutRegistrationResult{}, &Error{Code: CodeInvalidCheckout, Message: "write mode must be exclusive, claimed, shared, or read_only"}
	}
	requestHash, err := hashCommand("checkout.add", map[string]any{
		"workspace":   workspaceIdentifier,
		"project":     projectIdentifier,
		"write_mode":  writeMode,
		"observation": command.Observation,
	})
	if err != nil {
		return CheckoutRegistrationResult{}, storageFailure("hash checkout registration", err)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckoutRegistrationResult{}, storageFailure("begin checkout registration", err)
	}
	defer transaction.Rollback()

	var replay CheckoutRegistrationResult
	if found, err := lookupIdempotency(ctx, transaction, key, "checkout.add", requestHash, &replay); err != nil {
		return CheckoutRegistrationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, transaction, workspaceIdentifier)
	if err != nil {
		return CheckoutRegistrationResult{}, err
	}
	project, err := projectInTransaction(ctx, transaction, workspace.ID, projectIdentifier)
	if err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if err := rejectDuplicateCheckout(ctx, transaction, command.Observation.Path); err != nil {
		return CheckoutRegistrationResult{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	repository, repositoryCreated, err := ensureRepository(ctx, transaction, workspace.ID, command.Observation.Repository, now)
	if err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if _, err := transaction.ExecContext(ctx, "INSERT OR IGNORE INTO project_repositories(project_id, repository_id, attached_at) VALUES (?, ?, ?)", project.ID, repository.ID, now); err != nil {
		return CheckoutRegistrationResult{}, storageFailure("attach repository to project", err)
	}
	checkout, err := insertCheckout(ctx, transaction, project.ID, repository.ID, writeMode, command.Observation, now)
	if err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return CheckoutRegistrationResult{}, err
	}

	var sequence int64
	if repositoryCreated {
		sequence, err = appendEvent(ctx, transaction, workspace.ID, "repository", repository.ID, repository.Revision, repositoryRegistered, correlationID, now, map[string]any{"fingerprint": repository.Fingerprint, "object_format": repository.ObjectFormat, "root_commits": repository.RootCommits})
		if err != nil {
			return CheckoutRegistrationResult{}, err
		}
	}
	sequence, err = appendEvent(ctx, transaction, workspace.ID, "checkout", checkout.ID, checkout.Revision, checkoutRegistered, correlationID, now, checkoutEventData(checkout))
	if err != nil {
		return CheckoutRegistrationResult{}, err
	}
	result := CheckoutRegistrationResult{Repository: repository, Checkout: checkout, RepositoryCreated: repositoryCreated, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if err := recordIdempotency(ctx, transaction, key, "checkout.add", requestHash, result, now); err != nil {
		return CheckoutRegistrationResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return CheckoutRegistrationResult{}, storageFailure("commit checkout registration", err)
	}
	return result, nil
}

func (s *Store) InspectProject(ctx context.Context, workspaceIdentifier, projectIdentifier string) (ProjectInspection, error) {
	workspace, err := s.Workspace(ctx, workspaceIdentifier)
	if err != nil {
		return ProjectInspection{}, err
	}
	project, err := queryProject(ctx, s.db, workspace.ID, projectIdentifier)
	if err != nil {
		return ProjectInspection{}, err
	}
	repositories, err := queryProjectRepositories(ctx, s.db, project.ID)
	if err != nil {
		return ProjectInspection{}, err
	}
	checkouts, err := queryProjectCheckouts(ctx, s.db, project.ID)
	if err != nil {
		return ProjectInspection{}, err
	}
	return ProjectInspection{Project: project, Repositories: repositories, Checkouts: checkouts}, nil
}

// Project resolves an exact project by ID or name without inspecting checkout
// state or emitting observation events.
func (s *Store) Project(ctx context.Context, workspaceIdentifier, projectIdentifier string) (domain.Project, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.Project{}, storageFailure("begin project lookup", err)
	}
	defer transaction.Rollback()
	workspace, err := workspaceInTransaction(ctx, transaction, workspaceIdentifier)
	if err != nil {
		return domain.Project{}, err
	}
	return queryProject(ctx, transaction, workspace.ID, projectIdentifier)
}

func (s *Store) ApplyCheckoutObservations(ctx context.Context, workspaceIdentifier, projectIdentifier, correlationID string, observations map[string]domain.CheckoutObservation) (ProjectInspection, error) {
	inspection, err := s.InspectProject(ctx, workspaceIdentifier, projectIdentifier)
	if err != nil {
		return ProjectInspection{}, err
	}
	if strings.TrimSpace(correlationID) == "" || len(correlationID) > 128 {
		return ProjectInspection{}, &Error{Code: CodeInvalidCheckout, Message: "correlation id must contain 1 to 128 characters"}
	}
	repositories := make(map[string]domain.Repository, len(inspection.Repositories))
	for _, repository := range inspection.Repositories {
		repositories[repository.ID] = repository
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectInspection{}, storageFailure("begin checkout observation update", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, current := range inspection.Checkouts {
		observation, ok := observations[current.ID]
		if !ok {
			continue
		}
		if observation.Availability == domain.CheckoutAvailable {
			repository := repositories[current.RepositoryID]
			if observation.Repository.Fingerprint != repository.Fingerprint {
				observation = unavailableObservation(current.Path, CodeRepositoryChanged, "the path now resolves to a different Git history")
			}
		}
		updated, changed := mergeCheckoutObservation(current, observation, now)
		if !changed {
			continue
		}
		_, dirtyPathsJSON, err := encodeDirtyPaths(updated.DirtyPaths)
		if err != nil {
			return ProjectInspection{}, &Error{Code: CodeInvalidCheckout, Message: err.Error()}
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE checkouts
SET revision = ?, availability = ?, checkout_kind = ?, branch = NULLIF(?, ''),
	    head_commit = NULLIF(?, ''), dirty = ?, dirty_paths_json = ?, git_dir = NULLIF(?, ''),
    git_common_dir = NULLIF(?, ''), observed_at = ?,
    diagnostic_code = NULLIF(?, ''), diagnostic = NULLIF(?, ''),
    updated_at = ?, updated_by = ?
WHERE id = ? AND project_id = ?`,
			updated.Revision, updated.Availability, updated.CheckoutKind, updated.Branch,
			updated.HeadCommit, updated.Dirty, dirtyPathsJSON, updated.GitDir, updated.GitCommonDir,
			updated.ObservedAt, updated.DiagnosticCode, updated.Diagnostic,
			updated.UpdatedAt, updated.UpdatedBy, updated.ID, inspection.Project.ID,
		); err != nil {
			return ProjectInspection{}, storageFailure("update checkout observation", err)
		}
		if _, err := appendEvent(ctx, transaction, inspection.Project.WorkspaceID, "checkout", updated.ID, updated.Revision, checkoutObserved, correlationID, now, checkoutEventData(updated)); err != nil {
			return ProjectInspection{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return ProjectInspection{}, storageFailure("commit checkout observations", err)
	}
	return s.InspectProject(ctx, workspaceIdentifier, projectIdentifier)
}

func unavailableObservation(path, code, diagnostic string) domain.CheckoutObservation {
	return domain.CheckoutObservation{Path: path, Availability: domain.CheckoutUnavailable, CheckoutKind: domain.CheckoutKindUnknown, DiagnosticCode: code, Diagnostic: diagnostic}
}

func mergeCheckoutObservation(current domain.Checkout, observation domain.CheckoutObservation, now string) (domain.Checkout, bool) {
	updated := current
	if observation.Availability == domain.CheckoutAvailable {
		updated.Availability = observation.Availability
		updated.CheckoutKind = observation.CheckoutKind
		updated.Branch = observation.Branch
		updated.HeadCommit = observation.HeadCommit
		updated.Dirty = observation.Dirty
		updated.DirtyPaths = append([]string(nil), observation.DirtyPaths...)
		updated.GitDir = observation.GitDir
		updated.GitCommonDir = observation.GitCommonDir
		updated.DiagnosticCode = ""
		updated.Diagnostic = ""
	} else {
		updated.Availability = domain.CheckoutUnavailable
		updated.DiagnosticCode = observation.DiagnosticCode
		updated.Diagnostic = observation.Diagnostic
	}
	changed := updated.Availability != current.Availability || updated.CheckoutKind != current.CheckoutKind || updated.Branch != current.Branch || updated.HeadCommit != current.HeadCommit || updated.Dirty != current.Dirty || !equalStrings(updated.DirtyPaths, current.DirtyPaths) || updated.GitDir != current.GitDir || updated.GitCommonDir != current.GitCommonDir || updated.DiagnosticCode != current.DiagnosticCode || updated.Diagnostic != current.Diagnostic
	if changed {
		updated.Revision++
		updated.ObservedAt = now
		updated.UpdatedAt = now
		updated.UpdatedBy = localOwnerActorID
	}
	return updated, changed
}

func validateObservation(observation domain.CheckoutObservation) error {
	if observation.Availability != domain.CheckoutAvailable || observation.CheckoutKind == domain.CheckoutKindUnknown {
		return &Error{Code: CodeInvalidCheckout, Message: "checkout registration requires a complete available Git observation"}
	}
	if !filepath.IsAbs(observation.Path) || !filepath.IsAbs(observation.GitDir) || !filepath.IsAbs(observation.GitCommonDir) {
		return &Error{Code: CodeInvalidCheckout, Message: "checkout and Git metadata paths must be absolute"}
	}
	if observation.CheckoutKind != domain.CheckoutStandalone && observation.CheckoutKind != domain.CheckoutLinkedWorktree {
		return &Error{Code: CodeInvalidCheckout, Message: "checkout kind must be standalone or linked_worktree"}
	}
	if !repositoryFingerprintPattern.MatchString(observation.Repository.Fingerprint) || (observation.Repository.ObjectFormat != "sha1" && observation.Repository.ObjectFormat != "sha256") || len(observation.Repository.RootCommits) == 0 || observation.HeadCommit == "" {
		return &Error{Code: CodeInvalidCheckout, Message: "Git repository observation is incomplete or malformed"}
	}
	if _, _, err := encodeDirtyPaths(observation.DirtyPaths); err != nil {
		return &Error{Code: CodeInvalidCheckout, Message: err.Error()}
	}
	return nil
}

func normalizedWriteMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return domain.WriteModeExclusive
	}
	return mode
}

func validWriteMode(mode string) bool {
	return mode == domain.WriteModeExclusive || mode == domain.WriteModeClaimed || mode == domain.WriteModeShared || mode == domain.WriteModeReadOnly
}

func validateMutationMetadata(key, correlationID, code string) error {
	if key == "" || len(key) > 128 {
		return &Error{Code: code, Message: "idempotency key must contain 1 to 128 characters"}
	}
	if correlationID == "" || len(correlationID) > 128 {
		return &Error{Code: code, Message: "correlation id must contain 1 to 128 characters"}
	}
	return nil
}

func workspaceInTransaction(ctx context.Context, transaction *sql.Tx, identifier string) (Workspace, error) {
	workspace, err := queryWorkspace(ctx, transaction, "SELECT id, name, revision, created_at, updated_at, created_by, updated_by FROM workspaces WHERE id = ?", identifier)
	if err == nil {
		return workspace, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, storageFailure("query workspace by id", err)
	}
	workspace, err = queryWorkspace(ctx, transaction, "SELECT id, name, revision, created_at, updated_at, created_by, updated_by FROM workspaces WHERE name = ?", identifier)
	if err == nil {
		return workspace, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, &Error{Code: CodeWorkspaceNotFound, Message: fmt.Sprintf("workspace %q was not found", identifier)}
	}
	return Workspace{}, storageFailure("query workspace by name", err)
}

func projectInTransaction(ctx context.Context, transaction *sql.Tx, workspaceID, identifier string) (domain.Project, error) {
	return queryProject(ctx, transaction, workspaceID, identifier)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryProject(ctx context.Context, database queryRower, workspaceID, identifier string) (domain.Project, error) {
	const columns = "id, workspace_id, name, revision, created_at, updated_at, created_by, updated_by"
	var project domain.Project
	err := scanProject(database.QueryRowContext(ctx, "SELECT "+columns+" FROM projects WHERE workspace_id = ? AND id = ?", workspaceID, identifier), &project)
	if err == nil {
		return project, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, storageFailure("query project by id", err)
	}
	err = scanProject(database.QueryRowContext(ctx, "SELECT "+columns+" FROM projects WHERE workspace_id = ? AND name = ?", workspaceID, identifier), &project)
	if err == nil {
		return project, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, &Error{Code: CodeProjectNotFound, Message: fmt.Sprintf("project %q was not found in workspace", identifier)}
	}
	return domain.Project{}, storageFailure("query project by name", err)
}

func scanProject(row rowScanner, project *domain.Project) error {
	return row.Scan(&project.ID, &project.WorkspaceID, &project.Name, &project.Revision, &project.CreatedAt, &project.UpdatedAt, &project.CreatedBy, &project.UpdatedBy)
}

func rejectDuplicateCheckout(ctx context.Context, transaction *sql.Tx, path string) error {
	var id string
	err := transaction.QueryRowContext(ctx, "SELECT id FROM checkouts WHERE path = ?", path).Scan(&id)
	if err == nil {
		return &Error{Code: CodeCheckoutExists, Message: fmt.Sprintf("checkout path %q is already registered as %s", path, id)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storageFailure("check checkout path", err)
	}
	return nil
}

func ensureRepository(ctx context.Context, transaction *sql.Tx, workspaceID string, observation domain.RepositoryObservation, now string) (domain.Repository, bool, error) {
	repository, err := queryRepositoryByFingerprint(ctx, transaction, workspaceID, observation.Fingerprint)
	if err == nil {
		roots := append([]string(nil), observation.RootCommits...)
		sort.Strings(roots)
		if repository.ObjectFormat != observation.ObjectFormat || !equalStrings(repository.RootCommits, roots) {
			return domain.Repository{}, false, &Error{Code: CodeRepositoryChanged, Message: "repository fingerprint conflicts with stored Git identity"}
		}
		return repository, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Repository{}, false, storageFailure("query repository identity", err)
	}
	repositoryID, err := randomID("repo_")
	if err != nil {
		return domain.Repository{}, false, storageFailure("generate repository id", err)
	}
	roots := append([]string(nil), observation.RootCommits...)
	sort.Strings(roots)
	repository = domain.Repository{ID: repositoryID, WorkspaceID: workspaceID, Fingerprint: observation.Fingerprint, ObjectFormat: observation.ObjectFormat, RootCommits: roots, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	rootsJSON, err := json.Marshal(repository.RootCommits)
	if err != nil {
		return domain.Repository{}, false, storageFailure("encode repository roots", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO repositories(id, workspace_id, fingerprint, object_format, root_commits_json, revision, created_at, updated_at, created_by, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, repository.ID, repository.WorkspaceID, repository.Fingerprint, repository.ObjectFormat, string(rootsJSON), repository.Revision, repository.CreatedAt, repository.UpdatedAt, repository.CreatedBy, repository.UpdatedBy); err != nil {
		return domain.Repository{}, false, storageFailure("insert repository projection", err)
	}
	return repository, true, nil
}

func queryRepositoryByFingerprint(ctx context.Context, database queryRower, workspaceID, fingerprint string) (domain.Repository, error) {
	var repository domain.Repository
	var rootsJSON string
	err := database.QueryRowContext(ctx, `
SELECT id, workspace_id, fingerprint, object_format, root_commits_json, revision, created_at, updated_at, created_by, updated_by
FROM repositories WHERE workspace_id = ? AND fingerprint = ?`, workspaceID, fingerprint).Scan(
		&repository.ID, &repository.WorkspaceID, &repository.Fingerprint, &repository.ObjectFormat, &rootsJSON,
		&repository.Revision, &repository.CreatedAt, &repository.UpdatedAt, &repository.CreatedBy, &repository.UpdatedBy,
	)
	if err != nil {
		return domain.Repository{}, err
	}
	if err := json.Unmarshal([]byte(rootsJSON), &repository.RootCommits); err != nil {
		return domain.Repository{}, storageFailure("decode repository roots", err)
	}
	return repository, nil
}

func insertCheckout(ctx context.Context, transaction *sql.Tx, projectID, repositoryID, writeMode string, observation domain.CheckoutObservation, now string) (domain.Checkout, error) {
	checkoutID, err := randomID("co_")
	if err != nil {
		return domain.Checkout{}, storageFailure("generate checkout id", err)
	}
	dirtyPaths, dirtyPathsJSON, err := encodeDirtyPaths(observation.DirtyPaths)
	if err != nil {
		return domain.Checkout{}, &Error{Code: CodeInvalidCheckout, Message: err.Error()}
	}
	checkout := domain.Checkout{
		ID: checkoutID, ProjectID: projectID, RepositoryID: repositoryID, Path: observation.Path,
		WriteMode: writeMode, Revision: 1, Availability: observation.Availability, CheckoutKind: observation.CheckoutKind,
		Branch: observation.Branch, HeadCommit: observation.HeadCommit, Dirty: observation.Dirty, DirtyPaths: dirtyPaths,
		GitDir: observation.GitDir, GitCommonDir: observation.GitCommonDir, ObservedAt: now,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO checkouts(
    id, project_id, repository_id, path, write_mode, revision, availability,
	    checkout_kind, branch, head_commit, dirty, dirty_paths_json, git_dir, git_common_dir,
	    observed_at, diagnostic_code, diagnostic, created_at, updated_at, created_by, updated_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULL, NULL, ?, ?, ?, ?)`,
		checkout.ID, checkout.ProjectID, checkout.RepositoryID, checkout.Path, checkout.WriteMode, checkout.Revision,
		checkout.Availability, checkout.CheckoutKind, checkout.Branch, checkout.HeadCommit, checkout.Dirty, dirtyPathsJSON,
		checkout.GitDir, checkout.GitCommonDir, checkout.ObservedAt, checkout.CreatedAt, checkout.UpdatedAt,
		checkout.CreatedBy, checkout.UpdatedBy,
	); err != nil {
		return domain.Checkout{}, storageFailure("insert checkout projection", err)
	}
	return checkout, nil
}

func queryProjectRepositories(ctx context.Context, database *sql.DB, projectID string) ([]domain.Repository, error) {
	rows, err := database.QueryContext(ctx, `
SELECT r.id, r.workspace_id, r.fingerprint, r.object_format, r.root_commits_json,
       r.revision, r.created_at, r.updated_at, r.created_by, r.updated_by
FROM repositories r
JOIN project_repositories pr ON pr.repository_id = r.id
WHERE pr.project_id = ? ORDER BY r.id`, projectID)
	if err != nil {
		return nil, storageFailure("list project repositories", err)
	}
	defer rows.Close()
	result := make([]domain.Repository, 0)
	for rows.Next() {
		var repository domain.Repository
		var rootsJSON string
		if err := rows.Scan(&repository.ID, &repository.WorkspaceID, &repository.Fingerprint, &repository.ObjectFormat, &rootsJSON, &repository.Revision, &repository.CreatedAt, &repository.UpdatedAt, &repository.CreatedBy, &repository.UpdatedBy); err != nil {
			return nil, storageFailure("scan project repository", err)
		}
		if err := json.Unmarshal([]byte(rootsJSON), &repository.RootCommits); err != nil {
			return nil, storageFailure("decode project repository roots", err)
		}
		result = append(result, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate project repositories", err)
	}
	return result, nil
}

func queryProjectCheckouts(ctx context.Context, database *sql.DB, projectID string) ([]domain.Checkout, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, project_id, repository_id, path, write_mode, revision, availability,
	       checkout_kind, COALESCE(branch, ''), COALESCE(head_commit, ''), dirty, dirty_paths_json,
       COALESCE(git_dir, ''), COALESCE(git_common_dir, ''), observed_at,
       COALESCE(diagnostic_code, ''), COALESCE(diagnostic, ''),
       created_at, updated_at, created_by, updated_by
FROM checkouts WHERE project_id = ? ORDER BY path`, projectID)
	if err != nil {
		return nil, storageFailure("list project checkouts", err)
	}
	defer rows.Close()
	result := make([]domain.Checkout, 0)
	for rows.Next() {
		var checkout domain.Checkout
		var dirtyPathsJSON string
		if err := rows.Scan(&checkout.ID, &checkout.ProjectID, &checkout.RepositoryID, &checkout.Path, &checkout.WriteMode, &checkout.Revision, &checkout.Availability, &checkout.CheckoutKind, &checkout.Branch, &checkout.HeadCommit, &checkout.Dirty, &dirtyPathsJSON, &checkout.GitDir, &checkout.GitCommonDir, &checkout.ObservedAt, &checkout.DiagnosticCode, &checkout.Diagnostic, &checkout.CreatedAt, &checkout.UpdatedAt, &checkout.CreatedBy, &checkout.UpdatedBy); err != nil {
			return nil, storageFailure("scan project checkout", err)
		}
		if err := json.Unmarshal([]byte(dirtyPathsJSON), &checkout.DirtyPaths); err != nil {
			return nil, storageFailure("decode checkout dirty paths", err)
		}
		result = append(result, checkout)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate project checkouts", err)
	}
	return result, nil
}

func appendEvent(ctx context.Context, transaction *sql.Tx, workspaceID, entityType, entityID string, revision int64, eventType, correlationID, now string, data any) (int64, error) {
	return appendEventForActor(ctx, transaction, workspaceID, entityType, entityID, revision, eventType, correlationID, now, localOwnerActorID, localActorType, data)
}

func appendEventForActor(ctx context.Context, transaction *sql.Tx, workspaceID, entityType, entityID string, revision int64, eventType, correlationID, now, actorID, actorType string, data any) (int64, error) {
	if strings.TrimSpace(actorID) == "" || !domain.ValidEventActorType(actorType) {
		return 0, storageFailure("append "+eventType+" event", errors.New("event actor is outside the canonical actor taxonomy"))
	}
	eventID, err := randomID("evt_")
	if err != nil {
		return 0, storageFailure("generate event id", err)
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return 0, storageFailure("encode event data", err)
	}
	if !domain.ValidEvent(domain.Event{
		EventID:       eventID,
		Sequence:      1,
		Type:          eventType,
		SchemaVersion: 1,
		OccurredAt:    now,
		RecordedAt:    now,
		Actor:         domain.EventActor{ActorID: actorID, ActorType: actorType},
		WorkspaceID:   workspaceID,
		Entity:        domain.EventEntity{Type: entityType, ID: entityID, Revision: revision},
		CorrelationID: correlationID,
		Data:          dataJSON,
	}) {
		return 0, storageFailure("append "+eventType+" event", errors.New("event envelope is outside the canonical event contract"))
	}
	inserted, err := transaction.ExecContext(ctx, `
INSERT INTO events(
    event_id, type, schema_version, occurred_at, recorded_at,
    actor_id, actor_type, workspace_id, entity_type, entity_id,
    entity_revision, correlation_id, causation_id, data_json
) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		eventID, eventType, now, now, actorID, actorType, workspaceID,
		entityType, entityID, revision, correlationID, string(dataJSON),
	)
	if err != nil {
		return 0, storageFailure("append "+eventType+" event", err)
	}
	sequence, err := inserted.LastInsertId()
	if err != nil {
		return 0, storageFailure("read event sequence", err)
	}
	return sequence, nil
}

func checkoutEventData(checkout domain.Checkout) map[string]any {
	return map[string]any{"project_id": checkout.ProjectID, "path": checkout.Path, "repository_id": checkout.RepositoryID, "write_mode": checkout.WriteMode, "availability": checkout.Availability, "checkout_kind": checkout.CheckoutKind, "branch": checkout.Branch, "head_commit": checkout.HeadCommit, "dirty": checkout.Dirty, "dirty_paths": checkout.DirtyPaths, "diagnostic_code": checkout.DiagnosticCode}
}

func encodeDirtyPaths(values []string) ([]string, string, error) {
	normalized := append([]string(nil), values...)
	if normalized == nil {
		normalized = make([]string, 0)
	}
	for index, value := range normalized {
		if value == "" || filepath.IsAbs(value) {
			return nil, "", errors.New("dirty paths must be non-empty repository-relative paths")
		}
		cleaned := filepath.ToSlash(filepath.Clean(value))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, "", errors.New("dirty paths cannot escape or name only the repository root")
		}
		normalized[index] = cleaned
	}
	sort.Strings(normalized)
	if len(normalized) != 0 {
		write := 1
		for read := 1; read < len(normalized); read++ {
			if normalized[read] != normalized[write-1] {
				normalized[write] = normalized[read]
				write++
			}
		}
		normalized = normalized[:write]
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	return normalized, string(encoded), nil
}

func recordIdempotency(ctx context.Context, transaction *sql.Tx, key, command, requestHash string, result any, now string) error {
	response, err := json.Marshal(result)
	if err != nil {
		return storageFailure("encode idempotent response", err)
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO idempotency_keys(key, command, request_hash, response_json, created_at) VALUES (?, ?, ?, ?, ?)", key, command, requestHash, string(response), now); err != nil {
		return storageFailure("record idempotency result", err)
	}
	return nil
}

func hashCommand(name string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(name+"\n"), data...))
	return hex.EncodeToString(digest[:]), nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
