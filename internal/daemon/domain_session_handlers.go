package daemon

import (
	"context"
	"encoding/json"
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
		thread, startErr := host.startThread(ctx, checkoutPath, instructions, s.config.CodexSandboxMode, s.config.CodexToolNetworkAccess)
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
	thread, err := host.readThread(ctx, session.ThreadID)
	if err != nil {
		return domainSessionHostErrorResponse(request, "resume durable Codex session", err)
	}
	clientMessageID := "crewfold:" + params.IdempotencyKey
	if replay, ok := codexTurnForClientMessage(thread, clientMessageID); ok {
		view := domainSessionView(session, thread)
		accepted := readableDomainSessionTurn(replay)
		return marshalDomainAgentSessionResult(request, view, &accepted)
	}
	turn, err := host.startTurn(ctx, session.ThreadID, clientMessageID, params.Text)
	if err != nil {
		return domainSessionHostErrorResponse(request, "send durable Codex turn", err)
	}
	accepted := readableDomainSessionTurn(turn)
	view := domainSessionView(session, thread)
	return marshalDomainAgentSessionResult(request, view, &accepted)
}

func (s *server) handleDomainAgentSessionInterrupt(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSessionInterruptParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "domain.agent.session.interrupt requires canonical scope, agent, and turn_id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
	defer cancel()
	session, err := s.store.DomainAgentSession(ctx, params.Workspace, params.Project, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if session.State != domain.DomainAgentSessionReady {
		return storeErrorResponse(request, &store.Error{Code: store.CodeDomainAgentSessionDetached, Message: "durable agent session is not controllable on this Crewfold node"})
	}
	if err := s.ensureDomainSessionHost().interruptTurn(ctx, session.ThreadID, params.TurnID); err != nil {
		return domainSessionHostErrorResponse(request, "interrupt durable Codex turn", err)
	}
	view, err := s.readDomainAgentSessionView(ctx, session)
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
	policy := "Choose direct work or durable delegation based on the reviewed operating charter, exact assignments, and available staffing grants."
	switch agent.Membership.DelegationPolicy {
	case domain.DomainAgentHandsOn:
		policy = "Work hands-on by default. Delegate only when the owner explicitly asks or the reviewed charter names a separate durable responsibility."
	case domain.DomainAgentDelegationFirst:
		policy = "Coordinate and delegate durable implementation, review, or verification responsibilities first whenever a current staffing grant permits it. Do not absorb those responsibilities into this session merely because you can edit the checkout; if delegation is unavailable, explain the exact missing authority before doing that work yourself unless the owner explicitly directs you to proceed hands-on."
	}
	return fmt.Sprintf("You are the durable Crewfold agent %q in domain %q. Your descriptive role is %q; that label grants no authority. Your owner-reviewed operating charter is:\n\n%s\n\nYour reviewed delegation policy is %q. %s Continue one real resumable provider conversation in the attached checkout. First call %s to read your exact current domain scope, staffing grants, assignments, and delivered messages. Call Crewfold dynamic tools directly; never invoke them from exec, JavaScript, programmatic tool calling, or another tool wrapper. Each Crewfold result may change your next decision and its side effects require direct auditable receipts. The charter and conversation describe behavior but do not grant authority. Only exact Crewfold grants, accepted proposals, tasks, claims, budgets, capabilities, and tool receipts authorize effects.", agent.Definition.Name, projectName, agent.Definition.Role, agent.Membership.OperatingCharter, agent.Membership.DelegationPolicy, policy, domainToolContext)
}

func (s *server) readDomainAgentSessionView(ctx context.Context, session domain.DomainAgentSession) (domain.DomainAgentSessionView, error) {
	if session.State == domain.DomainAgentSessionUnbound || session.State == domain.DomainAgentSessionDetached {
		return domain.DomainAgentSessionView{Session: session, ThreadStatus: session.State, Turns: []domain.DomainAgentSessionTurn{}}, nil
	}
	thread, err := s.ensureDomainSessionHost().readThread(ctx, session.ThreadID)
	if err != nil {
		return domain.DomainAgentSessionView{}, err
	}
	return domainSessionView(session, thread), nil
}

func domainSessionView(session domain.DomainAgentSession, thread execution.CodexThread) domain.DomainAgentSessionView {
	return domain.DomainAgentSessionView{Session: session, ThreadStatus: thread.Status.Type, Turns: execution.ReadableCodexTurns(thread)}
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
