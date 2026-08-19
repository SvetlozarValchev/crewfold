package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

const domainSessionOperationTimeout = 60 * time.Second

func (s *server) handleDomainAgentSessionOpen(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionOpenParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.open requires canonical scope, agent, and idempotency_key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.ensureDomainAgentSessionBound(ctx, params.Workspace, params.Project, params.Agent, params.Checkout)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume durable Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, view, nil)
}

// ensureDomainAgentSessionBound turns one durable definition into a reachable
// provider conversation without inventing a bootstrap message. Binding has a
// dedicated lock: a provider may create a child from inside an in-flight owner
// turn, so reusing the ordinary conversation-operation lock would deadlock the
// app-server request/response cycle. The store's unique membership binding is
// the final durable duplicate guard.
func (s *server) ensureDomainAgentSessionBound(ctx context.Context, workspace, project, agentIdentifier, checkoutIdentifier string) (domain.DomainAgentSession, error) {
	host := s.ensureDomainSessionHost()
	host.bindingMu.Lock()
	defer host.bindingMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, workspace, project, agentIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	if session.State == domain.DomainAgentSessionDetached {
		return domain.DomainAgentSession{}, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "durable agent session belongs to another Crewfold node and must be explicitly replaced"}
	}
	if session.State != domain.DomainAgentSessionUnbound {
		return session, nil
	}
	checkoutPath, agent, selectedProject, err := s.resolveDomainSessionStart(ctx, workspace, project, agentIdentifier, checkoutIdentifier)
	if err != nil {
		return domain.DomainAgentSession{}, err
	}
	thread, err := host.startThread(ctx, checkoutPath, domainSessionThreadName(agent.Definition.Name), durableDomainAgentInstructions(agent, selectedProject.Name))
	if err != nil {
		return domain.DomainAgentSession{}, &store.Error{Code: store.CodeAdapterUnavailable, Message: "start durable Codex session failed: " + safeDomainSessionDiagnostic(err), Cause: err}
	}
	session, err = s.store.BindDomainAgentSession(ctx, store.BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace, ProjectIdentifier: project, AgentIdentifier: agentIdentifier,
		Provider: "codex", ThreadID: thread.ID, CWD: thread.CWD,
	})
	if err != nil {
		_ = host.deleteThread(ctx, thread.ID)
		return domain.DomainAgentSession{}, err
	}
	return session, nil
}

func (s *server) handleDomainAgentSessionShow(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.show requires canonical workspace, project, and agent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if params.Epoch > 0 {
		session, err = s.store.DomainAgentSessionAtEpoch(ctx, params.Workspace, params.Project, params.Agent, params.Epoch)
	}
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State == domain.DomainAgentSessionArchived {
		view, err := s.readArchivedDomainAgentSessionView(ctx, params.Workspace, params.Project, params.Agent, session)
		if err != nil {
			return domainSessionHostErrorResponse(request, "read archived durable Codex epoch", err)
		}
		return marshalDomainAgentSessionResult(request, view, nil)
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read durable Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, view, nil)
}

func (s *server) readArchivedDomainAgentSessionView(ctx context.Context, workspace, project, agentIdentifier string, session domain.DomainAgentSession) (domain.DomainAgentSessionView, error) {
	current, err := s.store.DomainAgentSession(ctx, workspace, project, agentIdentifier)
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	if current.State != domain.DomainAgentSessionReady {
		return domain.DomainAgentSessionView{}, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "archived durable-agent history is unavailable on this Crewfold node"}
	}
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, current.ThreadID)
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	definition := domain.DomainAgent{Definition: scope.Agent, Membership: scope.Membership}
	host := s.ensureDomainSessionHost()
	thread, err := host.readThread(ctx, session.ThreadID, session.CWD, durableDomainAgentInstructions(definition, scope.Project.Name))
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	if thread.CWD != session.CWD {
		host.releaseThreadHost(session.ThreadID)
		return domain.DomainAgentSessionView{}, errors.New("archived Codex thread cwd does not match its immutable binding")
	}
	view := domainSessionView(session, thread, nil)
	host.releaseThreadHost(session.ThreadID)
	if err := s.attachDomainSessionEpochs(ctx, &view); err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	return view, nil
}

func (s *server) handleDomainAgentSessionSend(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionSendParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Text) == "" {
		return invalidParamsResponse(request, "domain.agent.session.send requires canonical scope, nonempty text, and idempotency_key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State == domain.DomainAgentSessionUnbound {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionNotFound, Message: "durable agent has no provider conversation; open it first"})
	}
	if session.State == domain.DomainAgentSessionDetached {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "durable agent session is detached from this Crewfold node"})
	}
	session, thread, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume durable Codex session", err)
	}
	clientMessageID := "crewfold:" + params.IdempotencyKey
	if replay, ok := codexTurnForClientMessage(thread, clientMessageID); ok {
		view := domainSessionView(session, thread, host.liveActivity(session.ThreadID))
		if err := s.attachDomainSessionEpochs(ctx, &view); err != nil {
			return storeErrorResponse(request, err)
		}
		accepted := readableDomainSessionTurn(replay)
		return marshalDomainAgentSessionResult(request, view, &accepted)
	}
	turn, err := host.startTurn(ctx, session.ThreadID, clientMessageID, params.Text)
	if err != nil {
		return domainSessionHostErrorResponse(request, "send durable Codex turn", err)
	}
	accepted := readableDomainSessionTurn(turn)
	thread.Turns = append(thread.Turns, turn)
	thread.Status.Type = "active"
	view := domainSessionView(session, thread, host.liveActivity(session.ThreadID))
	if err := s.attachDomainSessionEpochs(ctx, &view); err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalDomainAgentSessionResult(request, view, &accepted)
}

func (s *server) handleDomainAgentSessionInterrupt(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionInterruptParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.interrupt requires canonical scope, agent, and turn_id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State != domain.DomainAgentSessionReady {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "durable agent session is not controllable on this Crewfold node"})
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume durable Codex session", err)
	}
	session = view.Session
	if err := host.interruptTurn(ctx, session.ThreadID, params.TurnID); err != nil {
		return domainSessionHostErrorResponse(request, "interrupt durable Codex turn", err)
	}
	view, err = s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read interrupted Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, view, nil)
}

func (s *server) handleDomainAgentSessionCompact(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionCompactParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.compact requires canonical scope, agent, and expected_epoch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State != domain.DomainAgentSessionReady || session.Epoch != params.ExpectedEpoch {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: "durable agent session changed before compaction"})
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read durable Codex session before compaction", err)
	}
	if view.ThreadStatus == "active" || domainSessionViewHasActiveTurn(view) {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: "durable agent session can compact only between provider turns"})
	}
	blockerCount, blockerIDs, err := s.store.DomainAgentSessionRotationBlockers(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if blockerCount != 0 {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: fmt.Sprintf("durable agent session has %d unresolved execution run(s), including %s", blockerCount, strings.Join(blockerIDs, ", "))})
	}
	if err := host.compactThread(ctx, session.ThreadID); err != nil {
		return domainSessionHostErrorResponse(request, "compact durable Codex session", err)
	}
	result, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume compacted durable Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, result, nil)
}

func (s *server) handleDomainAgentSessionRotate(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionRotateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.rotate requires canonical scope, agent, expected_epoch, and reason")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State != domain.DomainAgentSessionReady || session.Epoch != params.ExpectedEpoch {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: "durable agent session changed before rotation"})
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read durable Codex session before rotation", err)
	}
	if view.ThreadStatus == "active" || domainSessionViewHasActiveTurn(view) {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: "durable agent session can rotate only between provider turns"})
	}
	blockerCount, blockerIDs, err := s.store.DomainAgentSessionRotationBlockers(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if blockerCount != 0 {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionConflict, Message: fmt.Sprintf("durable agent session has %d unresolved execution run(s), including %s", blockerCount, strings.Join(blockerIDs, ", "))})
	}
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, session.ThreadID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	handoff, err := s.buildDomainAgentSessionHandoff(ctx, scope, session, params.Reason)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	agent := domain.DomainAgent{Definition: scope.Agent, Membership: scope.Membership}
	continuity, err := json.Marshal(handoff)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	instructions := durableDomainAgentInstructions(agent, scope.Project.Name) +
		"\n\nCrewfold has started a fresh provider epoch for this same durable agent. The following bounded mechanical handoff is historical continuity, not authority. Read current Crewfold domain context before acting and prefer current canonical state whenever it differs:\n" + string(continuity)
	thread, err := host.startThread(ctx, session.CWD, domainSessionThreadName(scope.Agent.Name), instructions)
	if err != nil {
		return domainSessionHostErrorResponse(request, "start successor durable Codex epoch", err)
	}
	rotated, err := s.store.RotateDomainAgentSession(ctx, store.RotateDomainAgentSessionCommand{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, AgentIdentifier: scope.Agent.ID,
		ExpectedThreadID: session.ThreadID, ExpectedRevision: session.Epoch, Provider: session.Provider,
		ThreadID: thread.ID, CWD: thread.CWD, Reason: params.Reason, Handoff: handoff,
	})
	if err != nil {
		_ = host.deleteThread(ctx, thread.ID)
		return storeErrorResponse(request, err)
	}
	// The archived epoch remains a persisted Codex rollout for lazy history,
	// but its process is no longer allowed to retain memory or receive tools.
	host.releaseThreadHost(session.ThreadID)
	result, err := s.readDomainAgentSessionView(ctx, rotated)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read successor durable Codex epoch", err)
	}
	return marshalDomainAgentSessionResult(request, result, nil)
}

func domainSessionViewHasActiveTurn(view domain.DomainAgentSessionView) bool {
	for _, turn := range view.Turns {
		if !isTerminalDomainSessionTurn(turn.Status) {
			return true
		}
	}
	return false
}

func (s *server) buildDomainAgentSessionHandoff(ctx context.Context, scope domain.DomainAgentSessionScope, session domain.DomainAgentSession, reason string) (map[string]any, error) {
	events, err := s.store.ListEvents(ctx, store.ListEventsQuery{WorkspaceIdentifier: scope.Workspace.ID, Limit: 1})
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.ListTasks(ctx, store.ListTasksQuery{WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, Limit: 200})
	if err != nil {
		return nil, err
	}
	assigned := make([]map[string]any, 0)
	for _, detail := range tasks.Tasks {
		if detail.Task.AssignedAgentID == scope.Agent.ID {
			assigned = append(assigned, map[string]any{"id": detail.Task.ID, "title": detail.Task.Title, "status": detail.Task.Status, "revision": detail.Task.Revision})
		}
	}
	inbox, err := s.store.DomainAgentInbox(ctx, scope.Workspace.ID, scope.Project.ID, scope.Agent.ID, 20)
	if err != nil {
		return nil, err
	}
	inboxItems := make([]map[string]any, 0, len(inbox))
	for _, item := range inbox {
		inboxItems = append(inboxItems, map[string]any{"message_id": item.Message.ID, "kind": item.Message.Kind, "delivery_status": item.Delivery.Status})
	}
	return map[string]any{
		"schema":     "urn:crewfold:schema:domain:agent-session-handoff:v1",
		"project_id": scope.Project.ID, "agent_id": scope.Agent.ID, "from_epoch": session.Epoch,
		"reason": reason, "as_of_event_sequence": events.HighWater, "assigned_work": assigned,
		"inbox": inboxItems, "project_tasks_examined": len(tasks.Tasks), "project_tasks_truncated": tasks.HasMore,
		"continuity": "The successor must read current Crewfold domain context before acting; this seal is historical continuity, not authority.",
	}, nil
}

func (s *server) ensureDomainSessionHost() *domainSessionHost {
	if s.domainSessions == nil {
		s.domainSessions = newDomainSessionHost(s.config, s.handleDomainSessionToolRequest)
	}
	return s.domainSessions
}

func (s *server) resolveDomainSessionStart(ctx context.Context, workspace, project, agentIdentifier, checkoutIdentifier string) (string, domain.DomainAgent, domain.Project, error) {
	inspection, err := s.store.InspectProject(ctx, workspace, project)
	if err != nil {
		return "", domain.DomainAgent{}, domain.Project{}, err
	}
	tree, err := s.store.DomainAgentTree(ctx, workspace, project)
	if err != nil {
		return "", domain.DomainAgent{}, domain.Project{}, err
	}
	var agent domain.DomainAgent
	for _, candidate := range tree.Agents {
		if candidate.Definition.ID == agentIdentifier {
			agent = candidate
			break
		}
	}
	if agent.Definition.ID == "" {
		return "", domain.DomainAgent{}, domain.Project{}, &store.Error{Code: store.CodeDomainAgentNotFound, Message: "agent is not attached to the selected domain"}
	}
	if agent.Definition.Provider != "codex" && agent.Definition.Provider != "codex-subscription" {
		return "", domain.DomainAgent{}, domain.Project{}, &store.Error{Code: store.CodeInvalidDomainAgentSession, Message: "durable Codex sessions require a Codex subscription agent"}
	}
	available := make([]domain.Checkout, 0, len(inspection.Checkouts))
	for _, checkout := range inspection.Checkouts {
		if checkout.Availability != domain.CheckoutAvailable {
			continue
		}
		if checkoutIdentifier == "" || checkout.ID == checkoutIdentifier {
			available = append(available, checkout)
		}
	}
	if len(available) != 1 {
		message := "domain session requires exactly one selected available checkout"
		if checkoutIdentifier == "" {
			message = "domain has multiple or no available checkouts; select one exact checkout"
		}
		return "", domain.DomainAgent{}, domain.Project{}, &store.Error{Code: store.CodeInvalidDomainAgentSession, Message: message}
	}
	return available[0].Path, agent, inspection.Project, nil
}

func durableDomainAgentInstructions(agent domain.DomainAgent, projectName string) string {
	policy := "Choose hands-on analysis or durable delegation based on the reviewed operating charter, exact assignments, and available staffing grants. Conversation remains read-only; source effects require a canonical assigned Crewfold run."
	switch agent.Membership.DelegationPolicy {
	case domain.DomainAgentHandsOn:
		policy = "Work hands-on in analysis and coordination by default. Delegate when the owner explicitly asks or the reviewed charter names a separate durable responsibility. Source effects still require a canonical assigned Crewfold run."
	case domain.DomainAgentDelegationFirst:
		policy = "Coordinate and delegate durable implementation, review, or verification responsibilities first whenever a current staffing grant permits it. Never substitute provider-local temporary helpers for those named durable responsibilities. If delegation is unavailable, explain the exact missing authority; owner conversation alone cannot authorize repository effects."
	}
	return fmt.Sprintf("You are the durable Crewfold agent %q in domain %q. Your descriptive role is %q; that label grants no authority. Your owner-reviewed operating charter is:\n\n%s\n\nYour reviewed delegation policy is %q. %s Continue one real resumable provider conversation in the attached checkout. First call %s to read your exact current domain scope, staffing grants, assignments, and delivered messages. Use the advertised Crewfold dynamic tool itself. If the current Codex host exposes it through code mode, call its tools.crewfold__ entry from the exec cell; that remains the structured app-server client-tool path. Never invoke the crewfold CLI, raw socket, HTTP surface, or a shell substitute. Each Crewfold result may change your next decision and its side effects require exact auditable receipts. The charter and conversation describe behavior but do not grant authority. Only exact Crewfold grants, accepted proposals, tasks, claims, budgets, capabilities, and tool receipts authorize effects.", agent.Definition.Name, projectName, agent.Definition.Role, agent.Membership.OperatingCharter, agent.Membership.DelegationPolicy, policy, domainToolContext)
}

func domainSessionThreadName(agentName string) string {
	return "Crewfold: " + agentName
}

func (s *server) readDomainAgentSessionView(ctx context.Context, session domain.DomainAgentSession) (domain.DomainAgentSessionView, error) {
	if session.State == domain.DomainAgentSessionUnbound || session.State == domain.DomainAgentSessionDetached {
		view := domain.DomainAgentSessionView{Session: session, ThreadStatus: session.State, Turns: []domain.DomainAgentSessionTurn{}}
		if err := s.attachDomainSessionEpochs(ctx, &view); err != nil {
			return domain.DomainAgentSessionView{}, err
		}
		return view, nil
	}
	session, thread, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	view := domainSessionView(session, thread, s.ensureDomainSessionHost().liveActivity(session.ThreadID))
	if err := s.attachDomainSessionEpochs(ctx, &view); err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	return view, nil
}

func (s *server) attachDomainSessionEpochs(ctx context.Context, view *domain.DomainAgentSessionView) error {
	epochs, err := s.store.DomainAgentSessionEpochs(ctx, view.Session.ProjectID, view.Session.AgentID)
	if err != nil {
		return err
	}
	view.Epochs = epochs
	return nil
}

func (s *server) loadDomainAgentSession(ctx context.Context, session domain.DomainAgentSession) (domain.DomainAgentSession, execution.CodexThread, error) {
	scope, scopeErr := s.store.DomainAgentSessionScopeByThread(ctx, session.ThreadID)
	if scopeErr != nil {
		return domain.DomainAgentSession{}, execution.CodexThread{}, scopeErr
	}
	agent := domain.DomainAgent{Definition: scope.Agent, Membership: scope.Membership}
	thread, err := s.ensureDomainSessionHost().readThread(ctx, session.ThreadID, session.CWD, durableDomainAgentInstructions(agent, scope.Project.Name))
	if err == nil {
		if thread.CWD != session.CWD {
			return domain.DomainAgentSession{}, execution.CodexThread{}, errors.New("resumed Codex thread cwd does not match its durable binding")
		}
		return session, thread, nil
	}
	if !execution.IsCodexThreadRolloutNotFound(err) {
		return domain.DomainAgentSession{}, execution.CodexThread{}, err
	}
	return s.replaceUnavailableDomainAgentSession(ctx, session)
}

func (s *server) replaceUnavailableDomainAgentSession(ctx context.Context, session domain.DomainAgentSession) (domain.DomainAgentSession, execution.CodexThread, error) {
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, session.ThreadID)
	if err != nil {
		return domain.DomainAgentSession{}, execution.CodexThread{}, err
	}
	host := s.ensureDomainSessionHost()
	agent := domain.DomainAgent{Definition: scope.Agent, Membership: scope.Membership}
	thread, err := host.startThread(ctx, session.CWD, domainSessionThreadName(scope.Agent.Name), durableDomainAgentInstructions(agent, scope.Project.Name))
	if err != nil {
		return domain.DomainAgentSession{}, execution.CodexThread{}, err
	}
	replaced, err := s.store.ReplaceDomainAgentSession(ctx, store.ReplaceDomainAgentSessionCommand{
		WorkspaceIdentifier: scope.Workspace.ID, ProjectIdentifier: scope.Project.ID, AgentIdentifier: scope.Agent.ID,
		ExpectedThreadID: session.ThreadID, Provider: "codex", ThreadID: thread.ID, CWD: thread.CWD,
	})
	if err != nil {
		_ = host.deleteThread(ctx, thread.ID)
		return domain.DomainAgentSession{}, execution.CodexThread{}, err
	}
	return replaced, thread, nil
}

func domainSessionView(session domain.DomainAgentSession, thread execution.CodexThread, live []domain.DomainAgentSessionTurn) domain.DomainAgentSessionView {
	return domain.DomainAgentSessionView{
		Session: session, ThreadStatus: thread.Status.Type,
		Turns: mergeDomainSessionTurns(execution.ReadableCodexTurns(thread), live),
	}
}

func mergeDomainSessionTurns(persisted, live []domain.DomainAgentSessionTurn) []domain.DomainAgentSessionTurn {
	result := make([]domain.DomainAgentSessionTurn, 0, len(persisted)+len(live))
	byID := make(map[string]int, len(persisted)+len(live))
	for _, turn := range persisted {
		turn.Items = append([]domain.DomainAgentSessionItem{}, turn.Items...)
		byID[turn.ID] = len(result)
		result = append(result, turn)
	}
	for _, update := range live {
		index, ok := byID[update.ID]
		if !ok {
			update.Items = append([]domain.DomainAgentSessionItem{}, update.Items...)
			byID[update.ID] = len(result)
			result = append(result, update)
			continue
		}
		turn := &result[index]
		if update.Status != "" && !(isTerminalDomainSessionTurn(turn.Status) && !isTerminalDomainSessionTurn(update.Status)) {
			turn.Status = update.Status
		}
		turn.Items = mergeDomainSessionItems(turn.Items, update.Items)
	}
	if len(result) > 100 {
		result = result[len(result)-100:]
	}
	return result
}

func mergeDomainSessionItems(persisted, live []domain.DomainAgentSessionItem) []domain.DomainAgentSessionItem {
	matched := func(item domain.DomainAgentSessionItem) bool {
		for _, update := range live {
			if item.ID == update.ID || sameDomainSessionMessage(item, update) {
				return true
			}
		}
		return false
	}
	result := make([]domain.DomainAgentSessionItem, 0, len(persisted)+len(live))
	// An accepted owner message may already be persisted before app-server emits
	// the first live item. Keep that input at the start until its live provider
	// item arrives; once it does, the live sequence is authoritative.
	for _, item := range persisted {
		if item.Type == "userMessage" && !matched(item) {
			result = append(result, item)
		}
	}
	result = append(result, live...)
	for _, item := range persisted {
		if item.Type != "userMessage" && !matched(item) {
			result = append(result, item)
		}
	}
	return result
}

func sameDomainSessionMessage(left, right domain.DomainAgentSessionItem) bool {
	if left.Type != right.Type || (left.Type != "userMessage" && left.Type != "agentMessage") {
		return false
	}
	return left.Text == right.Text && left.Command == right.Command
}

func isTerminalDomainSessionTurn(status string) bool {
	return status == "completed" || status == "failed" || status == "interrupted" || status == "cancelled"
}

func readableDomainSessionTurn(turn execution.CodexTurn) domain.DomainAgentSessionTurn {
	thread := execution.CodexThread{Turns: []execution.CodexTurn{turn}}
	turns := execution.ReadableCodexTurns(thread)
	if len(turns) == 1 {
		return turns[0]
	}
	return domain.DomainAgentSessionTurn{ID: turn.ID, Status: turn.Status, Items: []domain.DomainAgentSessionItem{}}
}

func codexTurnForClientMessage(thread execution.CodexThread, clientMessageID string) (execution.CodexTurn, bool) {
	for _, turn := range thread.Turns {
		for _, raw := range turn.Items {
			var item struct {
				Type     string `json:"type"`
				ClientID string `json:"clientId"`
			}
			if json.Unmarshal(raw, &item) == nil && item.Type == "userMessage" && item.ClientID == clientMessageID {
				return turn, true
			}
		}
	}
	return execution.CodexTurn{}, false
}

func marshalDomainAgentSessionResult(request localapi.Request, view domain.DomainAgentSessionView, accepted *domain.DomainAgentSessionTurn) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentSessionResult{
		Schema: localapi.DomainAgentSessionSchema, Type: "domain_agent_session", View: view, AcceptedTurn: accepted,
	})
}

func domainSessionHostErrorResponse(request localapi.Request, operation string, err error) localapi.Response {
	return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: operation + " failed: " + safeDomainSessionDiagnostic(err), Cause: err})
}
