package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	herdrHost   *execution.HerdrCodexAppServerOptions
	toolHandler func(context.Context, execution.CodexAppServerRequest) (any, error)
	clients     map[string]*execution.CodexAppServerClient
	threads     map[string]execution.CodexThread
	activity    map[string]*domainSessionLiveActivity
	compactions map[string]chan struct{}
	runTurns    map[string]domainSessionRunTurn
	closed      bool
}

type domainSessionRunTurn struct {
	runID  string
	turnID string
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

const durableDomainCodexBaseInstructions = `You are one real continuing Crewfold durable agent. Your owner conversation, accepted task work, mailbox activity, and Crewfold receipts all belong to this same Codex provider thread and identity. Do not create a second personality for task work. Ordinary owner turns are read-only; when Crewfold attaches an accepted task, that exact turn receives a checkout-scoped write sandbox and run tools. Keep going until the current owner request or attached task is genuinely handled or an exact blocker is reported. Domain-wide agents may compare every checkout listed by Crewfold context; a workstream-scoped agent treats its workstream's primary checkout as the execution home and other attached checkouts only as read-only references.

Crewfold's client-owned tools are in the crewfold namespace. Current Codex hosts may expose a client-owned tool either as a direct namespace call or through the code-mode tools object under a crewfold__ name. Both forms cross the same app-server item/tool/call boundary and receive the same daemon-owned receipt. Use whichever structured form the current turn advertises; never shell out to the crewfold binary, call its socket or HTTP surface, or fabricate a tool result. Always read current domain context before coordinating or proposing work. The context contains your exact hierarchy, staffing grants, child allocations, launch profiles, work proposals, assignments, workstreams, and inbox. Names, roles, hierarchy, and conversation text are descriptive; only current Crewfold grants, assignments, claims, budgets, capabilities, accepted operations, and exact tool receipts authorize effects.

If the owner asks for a deliverable and your delegation policy or the work warrants separate accountability, own the workflow end to end: inspect the repository and Crewfold context; choose the one existing writable checkout that should remain warm for that workstream; design one bounded workstream; then submit one exact Crewfold work proposal containing the complete inert team, hierarchy, execution profiles, checkout revision, objective, task graph, delivery requirements, budgets, task classes, and logical assignees. Do not call crewfold_create_durable_child while planning a deliverable. That immediate operation is reserved for an explicit owner request to add continuing domain-level staff outside a workstream. Work-proposal team entries do not exist before acceptance. Use completion only when downstream work needs no predecessor context, handoff when it needs a summary, and handoff_with_evidence when it needs changed paths, checks, risks, unknowns, or artifacts. Research, product-definition, architecture, or cross-workstream alignment that should survive this workstream must end with an explicit synthesis or knowledge-curation task. That task consumes the reviewed evidence and uses the run-scoped knowledge operation to propose sourced findings or decisions; ordinary artifacts and coordination messages never become shared domain memory by implication. Do not tell the owner to create workstreams, agents, tasks, assignments, profiles, or runs by hand when your proposal can express them. Tell the owner precisely what the one pending proposal will create and that Crewfold starts nothing until they accept its exact revision. Acceptance atomically creates the checkout-bound workstream, proposed durable team and profiles, tasks, placement, and scheduling intents. After acceptance, use Crewfold context to monitor canonical execution and coordinate real durable agents; never substitute provider-local temporary helpers for the named Crewfold team.

Managed local processes are generic reviewed commands attached to a checkout or workstream, not Vite-specific conveniences and not shell authority. Read the exact definitions, live instances, requests, and this agent's grants from Crewfold context. Use crewfold_inspect_managed_service with an exact inspect or logs grant to read current lifecycle, health, jobs, and bounded logs. When a current grant authorizes a lifecycle action, use crewfold_control_managed_service with its exact revisions. If the owner asks you to run, preview, serve, watch, or keep a repository command alive and no suitable definition exists, inspect the checkout's real scripts and executable, then use crewfold_propose_managed_service. Do not tell the owner to transcribe a command into a form. Your exact definition remains inert until one owner review accepts it; that acceptance grants you bounded inspect/log/start/stop/restart authority and starts the reviewed revision. Use crewfold_request_managed_service only for an existing owner-reviewed definition when you lack a start grant. A delegable service grant may be narrowed to a durable child. Never invent an executable, port, health state, grant, or process state, and never claim a request started anything.

Never edit repository files—including Markdown—during an ordinary owner turn. Repository effects are permitted only inside an exact accepted task turn and only within its supplied checkout sandbox. At the beginning of a task turn, call crewfold_get_briefing; use the run-scoped progress, artifact, blocked, and completion tools to record its outcome. For task-scoped messaging and knowledge, use crewfold_run_send_message and crewfold_run_propose_knowledge. Knowledge titles and bodies are owner-facing prose: never paste raw artifact, run, task, agent, or revision identifiers into them. Publish detailed evidence as a named artifact and include its exact artifact ID in the run report's evidence_ids; Crewfold resolves that canonical relationship into a readable attachment and producer/task attribution. Continue an existing related coordination thread instead of opening a new one; create a new topic only for a genuinely distinct subject. After processing an inbox message, either reply in its exact thread with reply_to_message_id or call crewfold_acknowledge_message when no reply is warranted. Merely reading context or receiving a wake is not acknowledgement. Do not use messages as a substitute for assignments, progress reports, verification findings, or canonical knowledge. A durable message or participant thread is coordination, not canonical knowledge and not a domain-home update. Use crewfold_propose_knowledge during owner coordination for durable domain findings or decisions worth retaining; its exact proposal remains inert until owner review. Never claim shared knowledge was updated unless that tool returned an exact knowledge revision receipt. Provider-local temporary helpers may assist bounded private research within one turn, but they are not Crewfold agents and must never receive continuing responsibility.`

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
	var herdrHost *execution.HerdrCodexAppServerOptions
	if factory == nil {
		herdrExecutable := strings.TrimSpace(config.HerdrExecutable)
		if herdrExecutable == "" {
			herdrExecutable = strings.TrimSpace(os.Getenv("CREWFOLD_HERDR_BINARY"))
		}
		if herdrExecutable == "" {
			herdrExecutable = "herdr"
		}
		herdrSession := strings.TrimSpace(config.HerdrSession)
		if herdrSession == "" {
			herdrSession = strings.TrimSpace(os.Getenv("CREWFOLD_HERDR_SESSION"))
		}
		if herdrSession == "" {
			herdrSession = strings.TrimSpace(os.Getenv("HERDR_SESSION"))
		}
		stateRoot := filepath.Join(config.DataDir, "runtime", "domain-agent-epochs")
		if socketPath := strings.TrimSpace(config.SocketPath); filepath.IsAbs(socketPath) {
			// Codex canonicalizes its Unix listener path, whose Linux kernel
			// limit is 108 bytes. Keep only the disposable Herdr host beside the
			// already-short daemon socket; durable conversation state remains in
			// the provider thread and Crewfold database.
			stateRoot = filepath.Join(filepath.Dir(socketPath), ".agents")
		}
		herdrHost = &execution.HerdrCodexAppServerOptions{
			CodexExecutable: executable, CodexHome: codexHome, HerdrExecutable: herdrExecutable,
			HerdrSession: herdrSession, StateRoot: stateRoot,
		}
	}
	return &domainSessionHost{
		factory: factory, herdrHost: herdrHost, toolHandler: toolHandler,
		clients: make(map[string]*execution.CodexAppServerClient), threads: make(map[string]execution.CodexThread),
		activity: make(map[string]*domainSessionLiveActivity), compactions: make(map[string]chan struct{}),
		runTurns: make(map[string]domainSessionRunTurn),
	}
}

func (host *domainSessionHost) newTransport(ctx context.Context, label string) (execution.CodexAppServerTransport, error) {
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return nil, errors.New("domain session host is closed")
	}
	host.mu.Unlock()
	if host.factory != nil {
		return host.factory()
	}
	if host.herdrHost != nil {
		options := *host.herdrHost
		options.Label = label
		return execution.StartCodexAppServerInHerdr(ctx, options)
	}
	return nil, errors.New("durable Codex host is not configured")
}

// newClient starts one private Codex app-server inside a Herdr workspace for
// one durable-agent epoch. Agents never share a process. The host remains
// observable across ordinary turns and is replaced only by explicit lifecycle
// work (compaction/rotation), daemon restart, or provider failure.
func (host *domainSessionHost) newClient(ctx context.Context, label string) (*execution.CodexAppServerClient, error) {
	transport, err := host.newTransport(ctx, label)
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
	go host.drain(client)
	return client, nil
}

func (host *domainSessionHost) clientForThread(threadID string) (*execution.CodexAppServerClient, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	client := host.clients[threadID]
	if client == nil {
		return nil, false
	}
	select {
	case <-client.Done():
		delete(host.clients, threadID)
		delete(host.threads, threadID)
		delete(host.activity, threadID)
		return nil, false
	default:
		return client, true
	}
}

func (host *domainSessionHost) bindClient(threadID string, client *execution.CodexAppServerClient, thread execution.CodexThread) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return errors.New("domain session host is closed")
	}
	if existing := host.clients[threadID]; existing != nil && existing != client {
		return errors.New("durable Codex thread is already hosted")
	}
	host.clients[threadID] = client
	host.threads[threadID] = thread
	return nil
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
	for threadID, candidate := range host.clients {
		if candidate == client {
			delete(host.clients, threadID)
			delete(host.threads, threadID)
			delete(host.activity, threadID)
		}
	}
	host.mu.Unlock()
	_ = client.Close()
}

func (host *domainSessionHost) startThread(ctx context.Context, cwd, threadName, instructions string) (execution.CodexThread, error) {
	client, err := host.newClient(ctx, threadName)
	if err != nil {
		return execution.CodexThread{}, err
	}
	keepClient := false
	defer func() {
		if !keepClient {
			_ = client.Close()
		}
	}()
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
	if err == nil {
		err = host.bindClient(thread.ID, client, thread)
		keepClient = err == nil
	}
	return thread, err
}

func (host *domainSessionHost) readThread(ctx context.Context, threadID, cwd, instructions string) (execution.CodexThread, error) {
	// Codex does not materialize a non-ephemeral rollout until its first user
	// message. Preserve the exact thread/start result in memory so opening a
	// conversation never requires a synthetic bootstrap turn merely to make it
	// observable.
	client, clientOK := host.clientForThread(threadID)
	host.mu.Lock()
	cached, cachedOK := host.threads[threadID]
	host.mu.Unlock()
	if cachedOK != clientOK {
		return execution.CodexThread{}, errors.New("durable Codex host state is inconsistent")
	}
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
		client = host.clients[threadID]
		host.mu.Unlock()
		if !cachedOK {
			var err error
			label := "Crewfold agent " + threadID
			if len(threadID) > 20 {
				label = "Crewfold agent " + threadID[len(threadID)-20:]
			}
			client, err = host.newClient(ctx, label)
			if err != nil {
				return execution.CodexThread{}, err
			}
			keepClient := false
			defer func() {
				if !keepClient {
					_ = client.Close()
				}
			}()
			thread, resumeErr := client.ResumeThreadWithParams(ctx, execution.CodexThreadResumeParams{
				ThreadID: threadID, CWD: cwd, ApprovalPolicy: "never",
				BaseInstructions: durableDomainCodexBaseInstructions, DeveloperInstructions: instructions,
				RuntimeWorkspaceRoots: []string{cwd}, Sandbox: execution.CodexSandboxReadOnly,
				Config: domainSessionAppServerConfig(),
			})
			if resumeErr != nil {
				return execution.CodexThread{}, resumeErr
			}
			turns, turnsErr := client.ListThreadTurns(ctx, thread.ID, 100)
			if turnsErr != nil {
				return execution.CodexThread{}, turnsErr
			}
			thread.Turns = turns
			if err := host.bindClient(thread.ID, client, thread); err != nil {
				return execution.CodexThread{}, err
			}
			keepClient = true
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
	client, ok := host.clientForThread(threadID)
	if !ok {
		return execution.CodexTurn{}, errors.New("durable Codex thread is not loaded")
	}
	turn, err := client.StartTurnWithOptions(ctx, threadID, text, execution.CodexTurnStartOptions{
		ClientMessageID: clientMessageID,
		ApprovalPolicy:  "never",
		SandboxPolicy:   map[string]any{"type": "readOnly", "networkAccess": false},
	})
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

func (host *domainSessionHost) bindRunTurn(threadID, runID, turnID string) {
	host.mu.Lock()
	host.runTurns[threadID] = domainSessionRunTurn{runID: runID, turnID: turnID}
	host.mu.Unlock()
}

func (host *domainSessionHost) clearRunTurn(threadID, runID string) {
	host.mu.Lock()
	if current, ok := host.runTurns[threadID]; ok && current.runID == runID {
		delete(host.runTurns, threadID)
	}
	host.mu.Unlock()
}

func (host *domainSessionHost) runForTool(threadID, turnID string) (string, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	current, ok := host.runTurns[threadID]
	if !ok || current.runID == "" || current.turnID != "" && current.turnID != turnID {
		return "", false
	}
	return current.runID, true
}

func (host *domainSessionHost) activeRunTurn(threadID string) (domainSessionRunTurn, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	current, ok := host.runTurns[threadID]
	return current, ok && current.runID != "" && current.turnID != ""
}

// cachedThread returns the host's last complete provider projection without
// issuing another app-server request. Accepted task turns update this cache
// and the live-activity stream before the run becomes active. The owner UI
// must be able to observe that in-flight turn even while worker reconciliation
// is reading the provider, rather than waiting behind the worker operation and
// showing the task only after it has finished.
func (host *domainSessionHost) cachedThread(threadID string) (execution.CodexThread, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	thread, ok := host.threads[threadID]
	if !ok {
		return execution.CodexThread{}, false
	}
	thread.Turns = append([]execution.CodexTurn{}, thread.Turns...)
	for index := range thread.Turns {
		thread.Turns[index].Items = append([]json.RawMessage{}, thread.Turns[index].Items...)
	}
	return thread, true
}

func (host *domainSessionHost) startRunTurn(ctx context.Context, threadID, runID, cwd, prompt string, networkAccess bool) (execution.CodexTurn, error) {
	client, ok := host.clientForThread(threadID)
	if !ok {
		return execution.CodexTurn{}, errors.New("durable Codex thread is not loaded")
	}
	host.bindRunTurn(threadID, runID, "")
	turn, err := client.StartTurnWithOptions(ctx, threadID, prompt, execution.CodexTurnStartOptions{
		ClientMessageID: "crewfold-run:" + runID,
		CWD:             cwd,
		ApprovalPolicy:  "never",
		SandboxPolicy: map[string]any{
			"type": "workspaceWrite", "networkAccess": networkAccess,
			"writableRoots": []string{},
		},
	})
	if err != nil {
		host.clearRunTurn(threadID, runID)
		return execution.CodexTurn{}, err
	}
	host.bindRunTurn(threadID, runID, turn.ID)
	host.mu.Lock()
	thread := host.threads[threadID]
	thread.Status.Type = "active"
	thread.Turns = append(thread.Turns, turn)
	host.threads[threadID] = thread
	host.mu.Unlock()
	return turn, nil
}

func (host *domainSessionHost) runTurn(ctx context.Context, threadID, runID, turnID string) (execution.CodexTurn, error) {
	client, ok := host.clientForThread(threadID)
	if !ok {
		return execution.CodexTurn{}, errors.New("durable Codex thread is not loaded")
	}
	host.bindRunTurn(threadID, runID, turnID)
	turns, err := client.ListThreadTurns(ctx, threadID, 100)
	if err != nil {
		return execution.CodexTurn{}, err
	}
	for _, turn := range turns {
		if turn.ID == turnID {
			return turn, nil
		}
	}
	return execution.CodexTurn{}, errors.New("durable Codex task turn is absent")
}

func (host *domainSessionHost) steerRunTurn(ctx context.Context, threadID, clientMessageID, text string) (execution.CodexTurn, error) {
	current, ok := host.activeRunTurn(threadID)
	if !ok {
		return execution.CodexTurn{}, errors.New("durable agent has no active task turn")
	}
	client, ok := host.clientForThread(threadID)
	if !ok {
		return execution.CodexTurn{}, errors.New("durable Codex thread is not loaded")
	}
	if _, err := client.SteerTurn(ctx, threadID, current.turnID, clientMessageID, text); err != nil {
		return execution.CodexTurn{}, err
	}
	return execution.CodexTurn{ID: current.turnID, Status: "inProgress"}, nil
}

func (host *domainSessionHost) interruptTurn(ctx context.Context, threadID, turnID string) error {
	client, ok := host.clientForThread(threadID)
	if !ok {
		return errors.New("durable Codex thread is not loaded")
	}
	if err := client.InterruptTurn(ctx, threadID, turnID); err != nil {
		return err
	}
	host.mu.Lock()
	if stream := host.activity[threadID]; stream != nil {
		if turn := stream.turns[turnID]; turn != nil {
			turn.status = "interrupted"
		}
	}
	thread := host.threads[threadID]
	thread.Status.Type = "idle"
	host.threads[threadID] = thread
	host.mu.Unlock()
	return nil
}

func (host *domainSessionHost) deleteThread(ctx context.Context, threadID string) error {
	client, ok := host.clientForThread(threadID)
	if !ok {
		return errors.New("durable Codex thread is not loaded")
	}
	err := client.DeleteThread(ctx, threadID)
	if err == nil {
		host.mu.Lock()
		delete(host.clients, threadID)
		delete(host.threads, threadID)
		delete(host.activity, threadID)
		host.mu.Unlock()
		_ = client.Close()
	}
	return err
}

// compactThread completes Codex's native persisted compaction and then closes
// only this agent's current Herdr-hosted provider process. The next read lazily
// resumes the same compacted epoch. Ordinary terminal turns do not recycle the
// host: one epoch remains one directly observable provider session until an
// explicit compaction, handoff, daemon shutdown, or provider failure.
func (host *domainSessionHost) compactThread(ctx context.Context, threadID string) error {
	client, ok := host.clientForThread(threadID)
	if !ok {
		return errors.New("durable Codex thread is not loaded")
	}
	waiter := make(chan struct{})
	host.mu.Lock()
	if host.compactions[threadID] != nil {
		host.mu.Unlock()
		return errors.New("durable Codex thread compaction is already running")
	}
	host.compactions[threadID] = waiter
	host.mu.Unlock()
	defer func() {
		host.mu.Lock()
		if host.compactions[threadID] == waiter {
			delete(host.compactions, threadID)
		}
		host.mu.Unlock()
	}()
	if err := client.CompactThread(ctx, threadID); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.Done():
		return errors.New("Codex app-server closed before thread compaction completed")
	case <-waiter:
	}
	host.releaseThreadHost(threadID)
	return nil
}

func (host *domainSessionHost) releaseThreadHost(threadID string) {
	host.mu.Lock()
	client := host.clients[threadID]
	delete(host.clients, threadID)
	delete(host.threads, threadID)
	delete(host.activity, threadID)
	delete(host.compactions, threadID)
	host.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (host *domainSessionHost) Close() error {
	host.mu.Lock()
	host.closed = true
	clients := make([]*execution.CodexAppServerClient, 0, len(host.clients))
	seen := make(map[*execution.CodexAppServerClient]struct{}, len(host.clients))
	for _, client := range host.clients {
		if _, ok := seen[client]; !ok {
			seen[client] = struct{}{}
			clients = append(clients, client)
		}
	}
	host.clients = make(map[string]*execution.CodexAppServerClient)
	host.threads = make(map[string]execution.CodexThread)
	host.activity = make(map[string]*domainSessionLiveActivity)
	host.compactions = make(map[string]chan struct{})
	host.mu.Unlock()
	var closeError error
	for _, client := range clients {
		if err := client.Close(); err != nil && closeError == nil {
			closeError = err
		}
	}
	return closeError
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
		if host.threads != nil {
			thread := host.threads[scope.ThreadID]
			thread.ID = scope.ThreadID
			if notification.Method == "turn/started" {
				thread.Status.Type = "active"
			} else {
				thread.Status.Type = "idle"
			}
			host.threads[scope.ThreadID] = thread
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
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		var envelope struct {
			Item json.RawMessage `json:"item"`
		}
		if scope.TurnID == "" || json.Unmarshal(notification.Params, &envelope) != nil || json.Unmarshal(envelope.Item, &params.Item) != nil {
			return
		}
		// Current Codex app-server versions expose native compaction as one
		// contextCompaction item. thread/compacted is deprecated and is not
		// emitted by the current server, so this exact terminal item is the
		// durable host-recycle boundary.
		if notification.Method == "item/completed" && params.Item.Type == "contextCompaction" {
			if waiter := host.compactions[scope.ThreadID]; waiter != nil {
				delete(host.compactions, scope.ThreadID)
				close(waiter)
			}
			return
		}
		item, ok := execution.ReadableCodexItem(envelope.Item)
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
