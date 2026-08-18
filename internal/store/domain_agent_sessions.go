package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"crewfold/internal/domain"
)

const (
	maximumDomainAgentProviderBytes   = 64
	maximumDomainAgentThreadIDBytes   = 512
	maximumDomainAgentSessionCWDBytes = 4096
)

type BindDomainAgentSessionCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	AgentIdentifier     string
	Provider            string
	ThreadID            string
	CWD                 string
}

type ReplaceDomainAgentSessionCommand struct {
	WorkspaceIdentifier string
	ProjectIdentifier   string
	AgentIdentifier     string
	ExpectedThreadID    string
	Provider            string
	ThreadID            string
	CWD                 string
}

// BindDomainAgentSession records the private current-node provider thread for
// an already-created durable domain agent. It is deliberately not a domain
// mutation: the opaque provider identity never becomes agent identity or an
// event payload. Repeating the exact binding is safe; replacement is explicit.
func (s *Store) BindDomainAgentSession(ctx context.Context, command BindDomainAgentSessionCommand) (domain.DomainAgentSession, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	provider := strings.TrimSpace(command.Provider)
	threadID := strings.TrimSpace(command.ThreadID)
	cwd := command.CWD
	if workspaceIdentifier == "" || projectIdentifier == "" || agentIdentifier == "" ||
		!validDomainAgentProvider(provider) || !validPrivateSessionText(threadID, maximumDomainAgentThreadIDBytes) || !validSessionCWD(cwd) {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "domain agent session binding is invalid")
	}
	if err := s.validateRuntimeNodeIdentity(); err != nil {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "domain agent session requires the daemon's canonical node identity")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainAgentSession{}, storageFailure("begin domain agent session binding", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if membership.Status != domain.DomainAgentActive {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "retired domain agent cannot bind a provider session")
	}
	existing, found, err := queryDomainAgentSessionBinding(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if found {
		if existing.NodeID == s.runtimeNodeID && existing.NodeFingerprint == s.runtimeNodeFingerprint &&
			existing.Provider == provider && existing.ThreadID == threadID && existing.CWD == cwd {
			if err := tx.Commit(); err != nil {
				return domain.DomainAgentSession{}, storageFailure("replay domain agent session binding", err)
			}
			return publicDomainAgentSession(existing, true), nil
		}
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeDomainAgentSessionConflict, "domain agent already has a different durable provider session")
	}
	now := s.nowText()
	binding := domain.DomainAgentSession{
		ProjectID: project.ID, AgentID: agent.ID, Provider: provider, State: domain.DomainAgentSessionReady,
		CWD: cwd, HasConversation: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
		ThreadID: threadID, NodeID: s.runtimeNodeID, NodeFingerprint: s.runtimeNodeFingerprint,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_session_bindings(
project_id,agent_id,provider,node_id,node_fingerprint,thread_id,cwd,revision,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)`, binding.ProjectID, binding.AgentID, binding.Provider, binding.NodeID,
		binding.NodeFingerprint, binding.ThreadID, binding.CWD, binding.Revision, binding.CreatedAt, binding.UpdatedAt); err != nil {
		return domain.DomainAgentSession{}, storageFailure("insert domain agent session binding", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.DomainAgentSession{}, storageFailure("commit domain agent session binding", err)
	}
	return publicDomainAgentSession(binding, true), nil
}

// ReplaceDomainAgentSession changes only a missing provider-local continuity
// binding. The durable agent, hierarchy, assignments, messages, knowledge, and
// authority remain unchanged. Callers must first prove through the provider's
// resume operation that the expected rollout is unavailable.
func (s *Store) ReplaceDomainAgentSession(ctx context.Context, command ReplaceDomainAgentSessionCommand) (domain.DomainAgentSession, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	projectIdentifier := strings.TrimSpace(command.ProjectIdentifier)
	agentIdentifier := strings.TrimSpace(command.AgentIdentifier)
	expectedThreadID := strings.TrimSpace(command.ExpectedThreadID)
	provider := strings.TrimSpace(command.Provider)
	threadID := strings.TrimSpace(command.ThreadID)
	if workspaceIdentifier == "" || projectIdentifier == "" || agentIdentifier == "" ||
		!validPrivateSessionText(expectedThreadID, maximumDomainAgentThreadIDBytes) ||
		!validDomainAgentProvider(provider) || !validPrivateSessionText(threadID, maximumDomainAgentThreadIDBytes) ||
		expectedThreadID == threadID || !validSessionCWD(command.CWD) {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "replacement domain agent session binding is invalid")
	}
	if err := s.validateRuntimeNodeIdentity(); err != nil {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "domain agent session replacement requires the daemon's canonical node identity")
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainAgentSession{}, storageFailure("begin domain agent session replacement", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if membership.Status != domain.DomainAgentActive || !agent.Enabled {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "inactive domain agent cannot replace a provider session")
	}
	existing, found, err := queryDomainAgentSessionBinding(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if !found || existing.ThreadID != expectedThreadID || existing.NodeID != s.runtimeNodeID ||
		existing.NodeFingerprint != s.runtimeNodeFingerprint || existing.Provider != provider || existing.CWD != command.CWD {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeDomainAgentSessionConflict, "domain agent session changed before its unavailable provider binding could be replaced")
	}
	now := s.nowText()
	result, err := tx.ExecContext(ctx, `UPDATE domain_agent_session_bindings
SET thread_id=?,revision=revision+1,updated_at=?
WHERE project_id=? AND agent_id=? AND thread_id=? AND revision=?`, threadID, now, project.ID, agent.ID, expectedThreadID, existing.Revision)
	if err != nil {
		return domain.DomainAgentSession{}, storageFailure("replace domain agent session binding", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeDomainAgentSessionConflict, "domain agent session changed before replacement committed")
	}
	existing.ThreadID = threadID
	existing.Revision++
	existing.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return domain.DomainAgentSession{}, storageFailure("commit domain agent session replacement", err)
	}
	return publicDomainAgentSession(existing, true), nil
}

// DomainAgentSession returns a safe owner-facing session projection. The
// private thread and node values remain populated only for in-process daemon
// callers and are excluded from JSON.
func (s *Store) DomainAgentSession(ctx context.Context, workspaceIdentifier, projectIdentifier, agentIdentifier string) (domain.DomainAgentSession, error) {
	workspaceIdentifier, projectIdentifier, agentIdentifier = strings.TrimSpace(workspaceIdentifier), strings.TrimSpace(projectIdentifier), strings.TrimSpace(agentIdentifier)
	if workspaceIdentifier == "" || projectIdentifier == "" || agentIdentifier == "" {
		return domain.DomainAgentSession{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "domain agent session scope is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.DomainAgentSession{}, storageFailure("begin domain agent session read", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, projectIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, agentIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if _, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID); err != nil {
		return domain.DomainAgentSession{}, err
	}
	binding, found, err := queryDomainAgentSessionBinding(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if !found {
		return domain.DomainAgentSession{ProjectID: project.ID, AgentID: agent.ID, State: domain.DomainAgentSessionUnbound}, nil
	}
	current := binding.NodeID == s.runtimeNodeID && binding.NodeFingerprint == s.runtimeNodeFingerprint && validRuntimeNodeIdentity(s.runtimeNodeID, s.runtimeNodeFingerprint)
	return publicDomainAgentSession(binding, current), nil
}

// DomainAgentSessionScopeByThread resolves one private provider request back to
// its exact current-node durable agent and domain. A foreign or stale node
// binding never gains tool authority merely by knowing a provider thread ID.
func (s *Store) DomainAgentSessionScopeByThread(ctx context.Context, threadID string) (domain.DomainAgentSessionScope, error) {
	threadID = strings.TrimSpace(threadID)
	if !validPrivateSessionText(threadID, maximumDomainAgentThreadIDBytes) {
		return domain.DomainAgentSessionScope{}, domainAgentSessionError(CodeInvalidDomainAgentSession, "provider thread identifier is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.DomainAgentSessionScope{}, storageFailure("begin provider thread scope read", err)
	}
	defer tx.Rollback()
	return s.domainAgentSessionScopeInTransaction(ctx, tx, threadID)
}

func (s *Store) domainAgentSessionScopeInTransaction(ctx context.Context, tx *sql.Tx, threadID string) (domain.DomainAgentSessionScope, error) {
	binding, found, err := queryDomainAgentSessionBindingByThread(ctx, tx, threadID)
	if err != nil {
		return domain.DomainAgentSessionScope{}, err
	}
	if !found {
		return domain.DomainAgentSessionScope{}, domainAgentSessionError(CodeDomainAgentSessionNotFound, "provider thread is not bound to a durable Crewfold agent")
	}
	if binding.NodeID != s.runtimeNodeID || binding.NodeFingerprint != s.runtimeNodeFingerprint || !validRuntimeNodeIdentity(s.runtimeNodeID, s.runtimeNodeFingerprint) {
		return domain.DomainAgentSessionScope{}, domainAgentSessionError(CodeDomainAgentSessionDetached, "provider thread is detached from this Crewfold node")
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", binding.ProjectID).Scan(&workspaceID); err != nil {
		return domain.DomainAgentSessionScope{}, storageFailure("resolve provider thread workspace", err)
	}
	workspace, err := workspaceInTransaction(ctx, tx, workspaceID)
	if err != nil {
		return domain.DomainAgentSessionScope{}, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, binding.ProjectID)
	if err != nil {
		return domain.DomainAgentSessionScope{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, binding.AgentID)
	if err != nil {
		return domain.DomainAgentSessionScope{}, err
	}
	membership, err := queryDomainAgentMembership(ctx, tx, project.ID, agent.ID)
	if err != nil {
		return domain.DomainAgentSessionScope{}, err
	}
	if membership.Status != domain.DomainAgentActive || !agent.Enabled {
		return domain.DomainAgentSessionScope{}, domainAgentSessionError(CodeDomainAgentSessionDetached, "provider thread's durable agent is not active")
	}
	return domain.DomainAgentSessionScope{Workspace: workspace, Project: project, Agent: agent, Membership: membership, Session: publicDomainAgentSession(binding, true)}, nil
}

func queryDomainAgentSessionBinding(ctx context.Context, query queryRower, projectID, agentID string) (domain.DomainAgentSession, bool, error) {
	var binding domain.DomainAgentSession
	err := query.QueryRowContext(ctx, `SELECT project_id,agent_id,provider,node_id,node_fingerprint,thread_id,cwd,revision,created_at,updated_at
FROM domain_agent_session_bindings WHERE project_id=? AND agent_id=?`, projectID, agentID).Scan(
		&binding.ProjectID, &binding.AgentID, &binding.Provider, &binding.NodeID, &binding.NodeFingerprint,
		&binding.ThreadID, &binding.CWD, &binding.Revision, &binding.CreatedAt, &binding.UpdatedAt)
	if err == nil {
		return binding, true, nil
	}
	if err == sql.ErrNoRows {
		return domain.DomainAgentSession{}, false, nil
	}
	return domain.DomainAgentSession{}, false, storageFailure("query domain agent session binding", err)
}

func queryDomainAgentSessionBindingByThread(ctx context.Context, query queryRower, threadID string) (domain.DomainAgentSession, bool, error) {
	var binding domain.DomainAgentSession
	err := query.QueryRowContext(ctx, `SELECT project_id,agent_id,provider,node_id,node_fingerprint,thread_id,cwd,revision,created_at,updated_at
FROM domain_agent_session_bindings WHERE thread_id=?`, threadID).Scan(
		&binding.ProjectID, &binding.AgentID, &binding.Provider, &binding.NodeID, &binding.NodeFingerprint,
		&binding.ThreadID, &binding.CWD, &binding.Revision, &binding.CreatedAt, &binding.UpdatedAt)
	if err == nil {
		return binding, true, nil
	}
	if err == sql.ErrNoRows {
		return domain.DomainAgentSession{}, false, nil
	}
	return domain.DomainAgentSession{}, false, storageFailure("query provider thread session binding", err)
}

func publicDomainAgentSession(binding domain.DomainAgentSession, current bool) domain.DomainAgentSession {
	binding.HasConversation = true
	if current {
		binding.State = domain.DomainAgentSessionReady
	} else {
		binding.State = domain.DomainAgentSessionDetached
	}
	return binding
}

func validDomainAgentProvider(value string) bool {
	if !validPrivateSessionText(value, maximumDomainAgentProviderBytes) || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validPrivateSessionText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && len([]byte(value)) <= maximum && !strings.ContainsRune(value, '\x00')
}

func validSessionCWD(value string) bool {
	return validPrivateSessionText(value, maximumDomainAgentSessionCWDBytes) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func domainAgentSessionError(code, message string) error {
	return &Error{Code: code, Message: message}
}
