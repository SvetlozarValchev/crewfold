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
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State == domain.DomainAgentSessionDetached {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "durable agent session belongs to another Crewfold node and must be explicitly replaced"})
	}
	if session.State == domain.DomainAgentSessionUnbound {
		checkoutPath, agent, project, resolveErr := s.resolveDomainSessionStart(ctx, params.Workspace, params.Project, params.Agent, params.Checkout)
		if resolveErr != nil {
			return storeErrorResponse(request, resolveErr)
		}
		instructions := durableDomainAgentInstructions(agent, project.Name)
		thread, startErr := host.startThread(ctx, checkoutPath, domainSessionThreadName(agent.Definition.Name), instructions)
		if startErr != nil {
			return domainSessionHostErrorResponse(request, "start durable Codex session", startErr)
		}
		session, err = s.store.BindDomainAgentSession(ctx, store.BindDomainAgentSessionCommand{
			WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: params.Agent,
			Provider: "codex", ThreadID: thread.ID, CWD: thread.CWD,
		})
		if err != nil {
			_ = host.deleteThread(ctx, thread.ID)
			return storeErrorResponse(request, err)
		}
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume durable Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, view, nil)
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
	if err != nil {
		return storeErrorResponse(request, err)
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
	if err != nil {
		return domainSessionHostErrorResponse(request, "read durable Codex session", err)
	}
	return marshalDomainAgentSessionResult(request, view, nil)
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
	return fmt.Sprintf("You are the durable Crewfold agent %q in domain %q. Your descriptive role is %q; that label grants no authority. Your owner-reviewed operating charter is:\n\n%s\n\nYour reviewed delegation policy is %q. %s Continue one real resumable provider conversation in the attached checkout. First call %s to read your exact current domain scope, staffing grants, assignments, and delivered messages. Call Crewfold dynamic tools directly; never invoke them from exec, JavaScript, programmatic tool calling, or another tool wrapper. Each Crewfold result may change your next decision and its side effects require direct auditable receipts. The charter and conversation describe behavior but do not grant authority. Only exact Crewfold grants, accepted proposals, tasks, claims, budgets, capabilities, and tool receipts authorize effects.", agent.Definition.Name, projectName, agent.Definition.Role, agent.Membership.OperatingCharter, agent.Membership.DelegationPolicy, policy, domainToolContext)
}

func domainSessionThreadName(agentName string) string {
	return "Crewfold: " + agentName
}

func (s *server) readDomainAgentSessionView(ctx context.Context, session domain.DomainAgentSession) (domain.DomainAgentSessionView, error) {
	if session.State == domain.DomainAgentSessionUnbound || session.State == domain.DomainAgentSessionDetached {
		return domain.DomainAgentSessionView{Session: session, ThreadStatus: session.State, Turns: []domain.DomainAgentSessionTurn{}}, nil
	}
	session, thread, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	return domainSessionView(session, thread, s.ensureDomainSessionHost().liveActivity(session.ThreadID)), nil
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
