package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"crewfold/internal/execution"
)

type domainSessionHost struct {
	mu          sync.Mutex
	resumeMu    sync.Mutex
	operationMu sync.Mutex
	factory     func() (execution.CodexAppServerTransport, error)
	toolHandler func(context.Context, execution.CodexAppServerRequest) (any, error)
	client      *execution.CodexAppServerClient
	threads     map[string]execution.CodexThread
	closed      bool
}

const durableDomainCodexBaseInstructions = `You are Codex, a coding agent collaborating with the owner inside one Crewfold durable-agent conversation. Work directly in the selected checkout, explain material progress clearly, and keep going until the owner's request is genuinely handled or an exact blocker is reported. Use ordinary command and file tools for repository work. Crewfold's client-owned tools are in the direct crewfold namespace. Crewfold conversation and hierarchy are not authority; only its exact grants, assignments, claims, budgets, capabilities, accepted operations, and tool receipts authorize effects.`

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
	return &domainSessionHost{factory: factory, toolHandler: toolHandler, threads: make(map[string]execution.CodexThread)}
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
	go host.drain(client)
	return client, nil
}

func (host *domainSessionHost) drain(client *execution.CodexAppServerClient) {
	for {
		select {
		case _, ok := <-client.Notifications():
			if !ok {
				return
			}
		case request, ok := <-client.Requests():
			if !ok {
				return
			}
			if request.Method != "item/tool/call" || host.toolHandler == nil {
				_ = client.Respond(request.ID, nil, errors.New("Crewfold has not granted this provider request"))
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			result, err := host.toolHandler(ctx, request)
			cancel()
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
	}
}

func (host *domainSessionHost) startThread(ctx context.Context, cwd, threadName, instructions, sandbox string, networkAccess bool) (execution.CodexThread, error) {
	client, err := host.clientFor(ctx)
	if err != nil {
		return execution.CodexThread{}, err
	}
	if strings.TrimSpace(sandbox) == "" {
		sandbox = execution.CodexSandboxWorkspaceWrite
	}
	config := map[string]any{}
	// GPT-5.6 uses programmatic tool calling for ordinary repository work. Keep
	// that current model behavior, but make Crewfold's audited namespace direct
	// so each call crosses app-server's item/tool/call boundary and receives an
	// exact daemon-owned receipt.
	config["code_mode.direct_only_tool_namespaces"] = []string{"crewfold"}
	if sandbox == execution.CodexSandboxWorkspaceWrite {
		config["sandbox_workspace_write.network_access"] = networkAccess
	}
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

func (host *domainSessionHost) readThread(ctx context.Context, threadID string) (execution.CodexThread, error) {
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
			thread, resumeErr := client.ResumeThread(ctx, threadID)
			if resumeErr != nil {
				select {
				case <-client.Done():
					host.invalidate(client)
				default:
				}
				return execution.CodexThread{}, resumeErr
			}
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
	if _, err := host.readThread(ctx, threadID); err != nil {
		return err
	}
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
	return err
}
