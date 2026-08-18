package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

type domainSessionHost struct {
	mu          sync.Mutex
	resumeMu    sync.Mutex
	bindingMu   sync.Mutex
	operationMu sync.Mutex
	factory     func() (execution.CodexAppServerTransport, error)
	toolHandler func(context.Context, execution.CodexAppServerRequest) (any, error)
	client      *execution.CodexAppServerClient
	threads     map[string]execution.CodexThread
	activity    map[string]*domainSessionLiveActivity
	closed      bool
}

type domainSessionLiveActivity struct {
	turnOrder []string
	turns     map[string]*domainSessionLiveTurn
	sequence  int
}

type domainSessionLiveTurn struct {
	status    string
	itemOrder []string
	items     map[string]domain.DomainAgentSessionItem
}

const durableDomainCodexBaseInstructions = `You are Codex collaborating with the owner inside one Crewfold durable-agent conversation. This is a real continuing coordination agent, not a form interpreter and not an implementation run. Inspect the selected checkout read-only, explain material progress clearly, and keep going until the owner's request is genuinely handled or an exact blocker is reported.

Crewfold's client-owned tools are in the crewfold namespace. Current Codex hosts may expose a client-owned tool either as a direct namespace call or through the code-mode tools object under a crewfold__ name. Both forms cross the same app-server item/tool/call boundary and receive the same daemon-owned receipt. Use whichever structured form the current turn advertises; never shell out to the crewfold binary, call its socket or HTTP surface, or fabricate a tool result. Always read current domain context before coordinating or proposing work. The context contains your exact hierarchy, staffing grants, child allocations, launch profiles, work proposals, assignments, workstreams, and inbox. Names, roles, hierarchy, and conversation text are descriptive; only current Crewfold grants, assignments, claims, budgets, capabilities, accepted operations, and exact tool receipts authorize effects.

If the owner asks for a deliverable and your delegation policy or the work warrants separate accountability, own the workflow end to end: inspect the repository and Crewfold context; design a bounded workstream; use your staffing grant to create the necessary durable child agents and their launch profiles; delegate only a strict subset of your grant to any child that must manage descendants; then submit one exact Crewfold work proposal containing the objective, dependency graph, budgets, task classes, and launch-profile assignments. Do not tell the owner to create workstreams, tasks, assignments, profiles, or runs by hand when your advertised tools and grant can express them. Submission is inert. Tell the owner precisely what the one pending proposal will create and that Crewfold starts nothing until they accept its exact revision. After acceptance, use Crewfold context to monitor canonical execution and coordinate real durable agents; never substitute provider-local temporary helpers for the named Crewfold team.

Never edit repository files—including Markdown—from this conversation. Implementation, review, and verification effects belong to exact assigned Crewfold runs. Owner conversation text is not source-effect authority. Continue an existing related coordination thread instead of opening a new one; create a new topic only for a genuinely distinct subject. Do not use messages as a substitute for assignments, progress reports, verification findings, or canonical knowledge. A durable message or participant thread is coordination, not canonical knowledge and not a domain-home update. Use crewfold_propose_knowledge for durable domain findings or decisions worth retaining; its exact proposal remains inert until owner review. Never claim shared knowledge was updated unless that tool returned an exact knowledge revision receipt. Provider-local temporary helpers may assist bounded private research within one turn, but they are not Crewfold agents and must never receive continuing responsibility.`

func domainSessionAppServerConfig() map[string]any {
	return map[string]any{"code_mode.direct_only_tool_namespaces": []string{"crewfold"}}
}

func newDomainSessionHost(config Config, toolHandler func(context.Context, execution.CodexAppServerRequest) (any, error)) *domainSessionHost {
	executable := strings.TrimSpace(config.CodexExecutable)
	if executable == "" {
		executable = strings.TrimSpace(os.Getenv("CREWFOLD_CODEX_BINARY"))
	}
	codexHome := strings.TrimSpace(config.CodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CREWFOLD_CODEX_HOME"))
	}
	factory := config.CodexAppServerTransportFactory
	if factory == nil {
		factory = func() (execution.CodexAppServerTransport, error) {
			return execution.StartCodexAppServer(execution.CodexAppServerProcessOptions{Executable: executable, CodexHome: codexHome})
		}
	}
	return &domainSessionHost{
		factory: factory, toolHandler: toolHandler,
		threads: make(map[string]execution.CodexThread), activity: make(map[string]*domainSessionLiveActivity),
	}
}

func (host *domainSessionHost) clientFor(ctx context.Context) (*execution.CodexAppServerClient, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return nil, errors.New("domain session host is closed")
	}
	if host.client != nil {
		select {
		case <-host.client.Done():
			_ = host.client.Close()
			host.client = nil
			host.threads = make(map[string]execution.CodexThread)
			host.activity = make(map[string]*domainSessionLiveActivity)
		default:
			return host.client, nil
		}
	}
	transport, err := host.factory()
	if err != nil {
		return nil, err
	}
	client, err := execution.NewCodexAppServerClient(transport)
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	if err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	host.client = client
	host.threads = make(map[string]execution.CodexThread)
	host.activity = make(map[string]*domainSessionLiveActivity)
	go host.drain(client)
	return client, nil
}

func (host *domainSessionHost) drain(client *execution.CodexAppServerClient) {
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				return
			}
			host.recordNotification(notification)
		case request, ok := <-client.Requests():
			if !ok {
				return
			}
			host.recordToolRequest(request, "inProgress", "")
			if request.Method != "item/tool/call" || host.toolHandler == nil {
				err := errors.New("Crewfold has not granted this provider request")
				host.recordToolRequest(request, "failed", err.Error())
				_ = client.Respond(request.ID, nil, err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), domainSessionOperationTimeout)
			result, err := host.toolHandler(ctx, request)
			cancel()
			status, diagnostic := "completed", ""
			if err != nil {
				status, diagnostic = "failed", err.Error()
			} else if succeeded, message, ok := domainSessionToolActivityResult(result); ok && !succeeded {
				status, diagnostic = "failed", message
			}
			host.recordToolRequest(request, status, diagnostic)
			_ = client.Respond(request.ID, result, err)
		case <-client.Done():
			return
		}
	}
}

func (host *domainSessionHost) invalidate(client *execution.CodexAppServerClient) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.client == client {
		_ = client.Close()
		host.client = nil
		host.threads = make(map[string]execution.CodexThread)
		host.activity = make(map[string]*domainSessionLiveActivity)
	}
}

func (host *domainSessionHost) startThread(ctx context.Context, cwd, threadName, instructions string) (execution.CodexThread, error) {
	client, err := host.clientFor(ctx)
	if err != nil {
		return execution.CodexThread{}, err
	}
	// Durable conversation is deliberately read-only. The daemon's provider
	// execution path, not owner prose in this thread, owns write authority.
	sandbox := execution.CodexSandboxReadOnly
	config := domainSessionAppServerConfig()
	// GPT-5.6 uses programmatic tool calling for ordinary repository work. Keep
	// that current model behavior, but make Crewfold's audited namespace direct
	// so each call crosses app-server's item/tool/call boundary and receives an
	// exact daemon-owned receipt.
	config["code_mode.direct_only_tool_namespaces"] = []string{"crewfold"}
	thread, err := client.StartThread(ctx, execution.CodexThreadStartParams{
		CWD: cwd, Ephemeral: false, ApprovalPolicy: "never", BaseInstructions: durableDomainCodexBaseInstructions, DeveloperInstructions: instructions,
		RuntimeWorkspaceRoots: []string{cwd}, Sandbox: sandbox, Config: config, DynamicTools: domainAgentDynamicToolSpecs(),
	})
	if err == nil {
		err = client.SetThreadName(ctx, thread.ID, threadName)
		if err != nil {
			_ = client.DeleteThread(ctx, thread.ID)
		}
	}
	if err != nil {
		select {
		case <-client.Done():
			host.invalidate(client)
		default:
		}
	}
	if err == nil {
		host.mu.Lock()
		host.threads[thread.ID] = thread
		host.mu.Unlock()
	}
	return thread, err
}

func (host *domainSessionHost) readThread(ctx context.Context, threadID, cwd, instructions string) (execution.CodexThread, error) {
	client, err := host.clientFor(ctx)
	if err != nil {
		return execution.CodexThread{}, err
	}
	// Codex does not materialize a non-ephemeral rollout until its first user
	// message. Preserve the exact thread/start result in memory so opening a
	// conversation never requires a synthetic bootstrap turn merely to make it
	// observable.
	host.mu.Lock()
	cached, cachedOK := host.threads[threadID]
	host.mu.Unlock()
	if cachedOK && len(cached.Turns) == 0 {
		return cached, nil
	}
	if !cachedOK {
		// A persisted Codex thread is not loaded into a newly started app-server
		// process. Resume the exact thread before attempting thread/read. This is
		// the normal path after a Crewfold service restart or an app-server host
		// replacement; it must not create a replacement conversation.
		host.resumeMu.Lock()
		defer host.resumeMu.Unlock()
		host.mu.Lock()
		cached, cachedOK = host.threads[threadID]
		host.mu.Unlock()
		if !cachedOK {
			thread, resumeErr := client.ResumeThreadWithParams(ctx, execution.CodexThreadResumeParams{
				ThreadID: threadID, CWD: cwd, ApprovalPolicy: "never",
				BaseInstructions: durableDomainCodexBaseInstructions, DeveloperInstructions: instructions,
				RuntimeWorkspaceRoots: []string{cwd}, Sandbox: execution.CodexSandboxReadOnly,
				Config: domainSessionAppServerConfig(),
			})
			if resumeErr != nil {
				select {
				case <-client.Done():
					host.invalidate(client)
				default:
				}
				return execution.CodexThread{}, resumeErr
			}
			turns, turnsErr := client.ListThreadTurns(ctx, thread.ID, 100)
			if turnsErr != nil {
				return execution.CodexThread{}, turnsErr
			}
			thread.Turns = turns
			host.mu.Lock()
			host.threads[thread.ID] = thread
			host.mu.Unlock()
			return thread, nil
		}
		if len(cached.Turns) == 0 {
			return cached, nil
		}
	}
	// thread/read observes a materialized thread without changing its lifecycle.
	thread, err := client.ReadThread(ctx, threadID)
	if err != nil {
		select {
		case <-client.Done():
			host.invalidate(client)
		default:
		}
		return execution.CodexThread{}, err
	}
	turns, err := client.ListThreadTurns(ctx, thread.ID, 100)
	if err != nil {
		return execution.CodexThread{}, err
	}
	thread.Turns = turns
	host.mu.Lock()
	host.threads[thread.ID] = thread
	host.mu.Unlock()
	return thread, nil
}

func (host *domainSessionHost) startTurn(ctx context.Context, threadID, clientMessageID, text string) (execution.CodexTurn, error) {
	client, err := host.clientFor(ctx)
	if err != nil {
		return execution.CodexTurn{}, err
	}
	turn, err := client.StartTurnWithClientID(ctx, threadID, clientMessageID, text)
	if err == nil {
		host.mu.Lock()
		thread := host.threads[threadID]
		thread.ID = threadID
		thread.Status.Type = "active"
		thread.Turns = append(thread.Turns, turn)
		host.threads[threadID] = thread
		host.mu.Unlock()
	}
	return turn, err
}

func (host *domainSessionHost) interruptTurn(ctx context.Context, threadID, turnID string) error {
	client, err := host.clientFor(ctx)
	if err != nil {
		return err
	}
	return client.InterruptTurn(ctx, threadID, turnID)
}

func (host *domainSessionHost) deleteThread(ctx context.Context, threadID string) error {
	client, err := host.clientFor(ctx)
	if err != nil {
		return err
	}
	err = client.DeleteThread(ctx, threadID)
	if err == nil {
		host.mu.Lock()
		delete(host.threads, threadID)
		delete(host.activity, threadID)
		host.mu.Unlock()
	}
	return err
}

func (host *domainSessionHost) Close() error {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.closed = true
	if host.client == nil {
		return nil
	}
	err := host.client.Close()
	host.client = nil
	host.threads = make(map[string]execution.CodexThread)
	host.activity = make(map[string]*domainSessionLiveActivity)
	return err
}

// recordNotification retains a bounded, owner-readable projection of the live
// app-server stream. thread/read is intentionally lossy for commands and tool
// lifecycles, so dropping these notifications makes an active Codex turn look
// like an unexplained spinner even while observable work is happening.
func (host *domainSessionHost) recordNotification(notification execution.CodexAppServerNotification) {
	var scope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(notification.Params, &scope) != nil || scope.ThreadID == "" {
		return
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	stream := host.activity[scope.ThreadID]
	if stream == nil {
		stream = &domainSessionLiveActivity{turns: make(map[string]*domainSessionLiveTurn)}
		host.activity[scope.ThreadID] = stream
	}
	switch notification.Method {
	case "turn/started", "turn/completed":
		var params struct {
			Turn execution.CodexTurn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) != nil || params.Turn.ID == "" {
			return
		}
		turn := ensureDomainSessionLiveTurn(stream, params.Turn.ID)
		turn.status = params.Turn.Status
		for _, raw := range params.Turn.Items {
			if item, ok := execution.ReadableCodexItem(raw); ok {
				upsertDomainSessionLiveItem(turn, item)
			}
		}
	case "item/started", "item/completed":
		var params struct {
			Item json.RawMessage `json:"item"`
		}
		if scope.TurnID == "" || json.Unmarshal(notification.Params, &params) != nil {
			return
		}
		item, ok := execution.ReadableCodexItem(params.Item)
		if !ok {
			return
		}
		if item.Status == "" {
			if notification.Method == "item/started" {
				item.Status = "inProgress"
			} else {
				item.Status = "completed"
			}
		}
		upsertDomainSessionLiveItem(ensureDomainSessionLiveTurn(stream, scope.TurnID), item)
	case "item/agentMessage/delta", "item/plan/delta", "item/commandExecution/outputDelta", "item/mcpToolCall/progress":
		var params struct {
			ItemID  string `json:"itemId"`
			Delta   string `json:"delta"`
			Message string `json:"message"`
		}
		if scope.TurnID == "" || json.Unmarshal(notification.Params, &params) != nil || params.ItemID == "" {
			return
		}
		itemType := "agentMessage"
		if notification.Method == "item/plan/delta" {
			itemType = "plan"
		} else if notification.Method == "item/commandExecution/outputDelta" {
			itemType = "commandExecution"
		} else if notification.Method == "item/mcpToolCall/progress" {
			itemType = "mcpToolCall"
			params.Delta = params.Message
		}
		turn := ensureDomainSessionLiveTurn(stream, scope.TurnID)
		item := turn.items[params.ItemID]
		if item.ID == "" {
			item = domain.DomainAgentSessionItem{ID: params.ItemID, Type: itemType}
		}
		item.Status = "inProgress"
		item.Text = boundedDomainSessionActivityText(item.Text + params.Delta)
		upsertDomainSessionLiveItem(turn, item)
	case "item/fileChange/patchUpdated":
		var params struct {
			ItemID  string          `json:"itemId"`
			Changes json.RawMessage `json:"changes"`
		}
		if scope.TurnID == "" || json.Unmarshal(notification.Params, &params) != nil || params.ItemID == "" || len(params.Changes) == 0 {
			return
		}
		raw, err := json.Marshal(struct {
			ID      string          `json:"id"`
			Type    string          `json:"type"`
			Status  string          `json:"status"`
			Changes json.RawMessage `json:"changes"`
		}{ID: params.ItemID, Type: "fileChange", Status: "inProgress", Changes: params.Changes})
		if err != nil {
			return
		}
		item, ok := execution.ReadableCodexItem(raw)
		if !ok {
			return
		}
		upsertDomainSessionLiveItem(ensureDomainSessionLiveTurn(stream, scope.TurnID), item)
	case "turn/plan/updated":
		var params struct {
			Explanation string `json:"explanation"`
			Plan        []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if scope.TurnID == "" || json.Unmarshal(notification.Params, &params) != nil {
			return
		}
		lines := make([]string, 0, len(params.Plan)+1)
		if strings.TrimSpace(params.Explanation) != "" {
			lines = append(lines, params.Explanation)
		}
		for _, step := range params.Plan {
			lines = append(lines, fmt.Sprintf("[%s] %s", step.Status, step.Step))
		}
		upsertDomainSessionLiveItem(ensureDomainSessionLiveTurn(stream, scope.TurnID), domain.DomainAgentSessionItem{
			ID: "turn-plan:" + scope.TurnID, Type: "plan", Text: boundedDomainSessionActivityText(strings.Join(lines, "\n")), Status: "inProgress",
		})
	case "error":
		if scope.TurnID == "" {
			return
		}
		var params struct {
			WillRetry bool `json:"willRetry"`
			Error     struct {
				Message           string `json:"message"`
				AdditionalDetails string `json:"additionalDetails"`
			} `json:"error"`
		}
		if json.Unmarshal(notification.Params, &params) != nil {
			return
		}
		message := strings.TrimSpace(strings.Join([]string{params.Error.Message, params.Error.AdditionalDetails}, "\n"))
		if message == "" {
			message = "Codex reported an unspecified provider error"
		}
		stream.sequence++
		status := "failed"
		if params.WillRetry {
			status = "retrying"
		}
		upsertDomainSessionLiveItem(ensureDomainSessionLiveTurn(stream, scope.TurnID), domain.DomainAgentSessionItem{
			ID: fmt.Sprintf("turn-error:%s:%d", scope.TurnID, stream.sequence), Type: "error", Text: boundedDomainSessionActivityText(message), Status: status,
		})
	}
}

func (host *domainSessionHost) recordToolRequest(request execution.CodexAppServerRequest, status, diagnostic string) {
	var params struct {
		CallID   string `json:"callId"`
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Tool     string `json:"tool"`
	}
	if request.Method != "item/tool/call" || json.Unmarshal(request.Params, &params) != nil ||
		params.CallID == "" || params.ThreadID == "" || params.TurnID == "" || params.Tool == "" {
		return
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	stream := host.activity[params.ThreadID]
	if stream == nil {
		stream = &domainSessionLiveActivity{turns: make(map[string]*domainSessionLiveTurn)}
		host.activity[params.ThreadID] = stream
	}
	upsertDomainSessionLiveItem(ensureDomainSessionLiveTurn(stream, params.TurnID), domain.DomainAgentSessionItem{
		ID: params.CallID, Type: "dynamicToolCall", Command: params.Tool,
		Text: boundedDomainSessionActivityText(diagnostic), Status: status,
	})
}

func ensureDomainSessionLiveTurn(stream *domainSessionLiveActivity, turnID string) *domainSessionLiveTurn {
	turn := stream.turns[turnID]
	if turn != nil {
		return turn
	}
	turn = &domainSessionLiveTurn{status: "inProgress", items: make(map[string]domain.DomainAgentSessionItem)}
	stream.turns[turnID] = turn
	stream.turnOrder = append(stream.turnOrder, turnID)
	if len(stream.turnOrder) > 100 {
		delete(stream.turns, stream.turnOrder[0])
		stream.turnOrder = stream.turnOrder[1:]
	}
	return turn
}

func upsertDomainSessionLiveItem(turn *domainSessionLiveTurn, item domain.DomainAgentSessionItem) {
	if item.ID == "" || item.Type == "" {
		return
	}
	if _, ok := turn.items[item.ID]; !ok {
		turn.itemOrder = append(turn.itemOrder, item.ID)
		if len(turn.itemOrder) > 512 {
			delete(turn.items, turn.itemOrder[0])
			turn.itemOrder = turn.itemOrder[1:]
		}
	}
	// app-server's later item/completed notification does not include the
	// daemon-owned tool response. Preserve the exact bounded failure diagnosis
	// we recorded at the tool-response boundary instead of replacing it with a
	// generic completed marker.
	if previous, ok := turn.items[item.ID]; ok && item.Type == "dynamicToolCall" {
		if item.Text == "" && previous.Text != "" {
			item.Text = previous.Text
		}
		if previous.Status == "failed" && item.Status == "completed" {
			item.Status = previous.Status
		}
	}
	turn.items[item.ID] = item
}

func domainSessionToolActivityResult(result any) (bool, string, bool) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return false, "", false
	}
	var response struct {
		Success      bool `json:"success"`
		ContentItems []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"contentItems"`
	}
	if json.Unmarshal(encoded, &response) != nil {
		return false, "", false
	}
	parts := make([]string, 0, len(response.ContentItems))
	for _, item := range response.ContentItems {
		if item.Type == "inputText" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return response.Success, boundedDomainSessionActivityText(strings.Join(parts, "\n")), true
}

func (host *domainSessionHost) liveActivity(threadID string) []domain.DomainAgentSessionTurn {
	host.mu.Lock()
	defer host.mu.Unlock()
	stream := host.activity[threadID]
	if stream == nil {
		return []domain.DomainAgentSessionTurn{}
	}
	result := make([]domain.DomainAgentSessionTurn, 0, len(stream.turnOrder))
	for _, turnID := range stream.turnOrder {
		live := stream.turns[turnID]
		if live == nil {
			continue
		}
		turn := domain.DomainAgentSessionTurn{ID: turnID, Status: live.status, Items: make([]domain.DomainAgentSessionItem, 0, len(live.itemOrder))}
		for _, itemID := range live.itemOrder {
			if item, ok := live.items[itemID]; ok {
				turn.Items = append(turn.Items, item)
			}
		}
		result = append(result, turn)
	}
	return result
}

func boundedDomainSessionActivityText(value string) string {
	const maximum = 64 * 1024
	if len(value) <= maximum {
		return value
	}
	return value[len(value)-maximum:]
}
