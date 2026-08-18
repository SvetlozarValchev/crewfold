package daemon

import (
	"bytes"
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

const domainAgentSpecTimeout = 120 * time.Second

func (s *server) handleDomainAgentSpecDraft(request localapi.Request) localapi.Response {
	var params localapi.DomainAgentSpecDraftParams
	if err := decodeParams(request.Params, &params); err != nil || !validWorkbenchText(params.OwnerIntent, 4096) {
		return invalidParamsResponse(request, "domain.agent.spec.draft requires exact domain scope and bounded owner_intent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainAgentSpecTimeout)
	defer cancel()
	checkoutPath, domainName := "", ""
	if params.RepositoryPath != "" {
		var ok bool
		checkoutPath, ok = resolveWorkbenchRepositoryPath(params.RepositoryPath)
		if !ok || !workbenchNamePattern.MatchString(params.DomainName) {
			return invalidParamsResponse(request, "agent specification draft repository path or domain name is invalid")
		}
		if _, err := s.gitInspector.Inspect(ctx, checkoutPath); err != nil {
			return storeErrorResponse(request, &store.Error{Code: store.CodeInvalidDomainAgent, Message: "agent specification draft requires an inspectable Git checkout", Cause: err})
		}
		domainName = params.DomainName
	} else {
		inspection, err := s.store.InspectProject(ctx, params.Workspace, params.Project)
		if err != nil {
			return storeErrorResponse(request, err)
		}
		var checkout domain.Checkout
		for _, candidate := range inspection.Checkouts {
			if candidate.Availability != domain.CheckoutAvailable || params.Checkout != "" && candidate.ID != params.Checkout {
				continue
			}
			if checkout.ID != "" {
				return storeErrorResponse(request, &store.Error{Code: store.CodeInvalidDomainAgent, Message: "agent specification draft requires one exact available checkout"})
			}
			checkout = candidate
		}
		if checkout.ID == "" {
			return storeErrorResponse(request, &store.Error{Code: store.CodeInvalidDomainAgent, Message: "agent specification draft requires one exact available checkout"})
		}
		checkoutPath, domainName = checkout.Path, inspection.Project.Name
	}
	draft, err := s.ensureDomainSessionHost().draftAgentSpec(ctx, checkoutPath, domainName, params.OwnerIntent)
	if err != nil {
		return domainSessionHostErrorResponse(request, "draft agent specification", err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DomainAgentSpecDraftResult{
		Schema: localapi.DomainAgentSpecDraftSchema, Type: "domain_agent_spec_draft", Draft: draft,
	})
}

func (host *domainSessionHost) draftAgentSpec(ctx context.Context, cwd, projectName, ownerIntent string) (localapi.DomainAgentSpecDraft, error) {
	transport, err := host.factory()
	if err != nil {
		return localapi.DomainAgentSpecDraft{}, err
	}
	client, err := execution.NewCodexAppServerClient(transport)
	if err != nil {
		_ = transport.Close()
		return localapi.DomainAgentSpecDraft{}, err
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		return localapi.DomainAgentSpecDraft{}, fmt.Errorf("initialize ephemeral Codex drafter: %w", err)
	}
	thread, err := client.StartThread(ctx, execution.CodexThreadStartParams{
		CWD: cwd, Ephemeral: true, ApprovalPolicy: "never", Sandbox: "read-only",
		BaseInstructions:      "You draft one owner-reviewable Crewfold durable-agent specification. You have no authority to create agents, grant staffing, start work, or mutate Crewfold. You may inspect the checkout read-only when it materially improves the draft. Return exactly one JSON object and no prose.",
		DeveloperInstructions: "The JSON object must have exactly: name, role, operating_charter, delegation_policy, rationale. name is a lowercase hyphenated durable identity. role is descriptive, not authority. operating_charter is a concise durable behavioral contract explaining what the agent owns, how it communicates and reports, when it delegates, and what it must escalate. delegation_policy is exactly hands_on, adaptive, or delegation_first. Do not include grants, permissions, hidden prompts, credentials, or repository facts you did not verify.",
		RuntimeWorkspaceRoots: []string{cwd},
	})
	if err != nil {
		return localapi.DomainAgentSpecDraft{}, fmt.Errorf("start ephemeral Codex drafter: %w", err)
	}
	prompt := fmt.Sprintf("Domain: %s\nOwner intent:\n%s\n\nDraft the reviewed durable-agent specification now.", projectName, ownerIntent)
	turn, err := client.StartTurn(ctx, thread.ID, prompt)
	if err != nil {
		return localapi.DomainAgentSpecDraft{}, fmt.Errorf("start agent specification draft: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return localapi.DomainAgentSpecDraft{}, ctx.Err()
		case <-client.Done():
			return localapi.DomainAgentSpecDraft{}, errors.New("ephemeral Codex drafter closed before returning a specification")
		case request := <-client.Requests():
			_ = client.Respond(request.ID, nil, errors.New("ephemeral specification drafting has no effect authority"))
		case notification := <-client.Notifications():
			if notification.Method != "turn/completed" {
				continue
			}
			var completed struct {
				ThreadID string              `json:"threadId"`
				Turn     execution.CodexTurn `json:"turn"`
			}
			if json.Unmarshal(notification.Params, &completed) != nil || completed.ThreadID != thread.ID || completed.Turn.ID != turn.ID {
				continue
			}
			if completed.Turn.Status != "completed" {
				return localapi.DomainAgentSpecDraft{}, fmt.Errorf("ephemeral Codex drafter ended with status %s", completed.Turn.Status)
			}
			return decodeDomainAgentSpecDraft(completed.Turn)
		}
	}
}

func decodeDomainAgentSpecDraft(turn execution.CodexTurn) (localapi.DomainAgentSpecDraft, error) {
	readable := execution.ReadableCodexTurns(execution.CodexThread{Turns: []execution.CodexTurn{turn}})
	var text string
	if len(readable) == 1 {
		for _, item := range readable[0].Items {
			if item.Type == "agentMessage" && strings.TrimSpace(item.Text) != "" {
				text = strings.TrimSpace(item.Text)
			}
		}
	}
	if text == "" {
		return localapi.DomainAgentSpecDraft{}, errors.New("ephemeral Codex drafter did not return one JSON object")
	}
	var draft localapi.DomainAgentSpecDraft
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil || decodeHasTrailingValue(decoder) || !workbenchNamePattern.MatchString(draft.Name) ||
		!validWorkbenchText(draft.Role, 128) || !validWorkbenchCharter(draft.OperatingCharter) ||
		!validWorkbenchDelegationPolicy(draft.DelegationPolicy) || !validWorkbenchText(draft.Rationale, 1024) {
		return localapi.DomainAgentSpecDraft{}, errors.New("ephemeral Codex drafter returned an invalid agent specification")
	}
	return draft, nil
}
