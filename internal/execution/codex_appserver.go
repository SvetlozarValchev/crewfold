package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maximumCodexAppServerMessageBytes = 8 * 1024 * 1024

// CodexAppServerTransport is the private JSONL transport to one current-node
// Codex app-server host. Production may back it with a Herdr-supervised stdio
// proxy or a private Unix socket; the protocol and lifecycle stay identical.
type CodexAppServerTransport interface {
	io.Reader
	io.Writer
	io.Closer
}

type CodexAppServerNotification struct {
	Method string
	Params json.RawMessage
}

// CodexAppServerRequest is an app-server initiated request that requires an
// explicit client response (for example, an approval). The transport exposes
// these separately from notifications so callers cannot accidentally treat a
// requested effect as an inert event.
type CodexAppServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type CodexThread struct {
	ID                   string            `json:"id"`
	CWD                  string            `json:"cwd"`
	Ephemeral            bool              `json:"ephemeral"`
	ModelProvider        string            `json:"modelProvider"`
	Status               CodexThreadStatus `json:"status"`
	CanAcceptDirectInput *bool             `json:"canAcceptDirectInput,omitempty"`
	Turns                []CodexTurn       `json:"turns"`
	Raw                  json.RawMessage   `json:"-"`
}

type CodexThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type CodexTurn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []json.RawMessage `json:"items"`
}

type CodexThreadStartParams struct {
	CWD                   string                 `json:"cwd"`
	Ephemeral             bool                   `json:"ephemeral"`
	ApprovalPolicy        string                 `json:"approvalPolicy,omitempty"`
	BaseInstructions      string                 `json:"baseInstructions,omitempty"`
	DeveloperInstructions string                 `json:"developerInstructions,omitempty"`
	RuntimeWorkspaceRoots []string               `json:"runtimeWorkspaceRoots,omitempty"`
	Sandbox               string                 `json:"sandbox,omitempty"`
	Config                map[string]any         `json:"config,omitempty"`
	DynamicTools          []CodexDynamicToolSpec `json:"dynamicTools,omitempty"`
}

// CodexDynamicToolSpec is one function implemented by Crewfold and exposed to
// a durable app-server thread. The input schema remains explicit and closed;
// app-server requests are still authorized by the daemon at call time.
type CodexDynamicToolSpec struct {
	Type         string                      `json:"type"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description"`
	InputSchema  map[string]any              `json:"inputSchema,omitempty"`
	DeferLoading bool                        `json:"deferLoading,omitempty"`
	Tools        []CodexDynamicNamespaceTool `json:"tools,omitempty"`
}

type CodexDynamicNamespaceTool struct {
	Type         string         `json:"type"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	DeferLoading bool           `json:"deferLoading,omitempty"`
}

type CodexThreadResponse struct {
	Thread CodexThread `json:"thread"`
}

type CodexTurnResponse struct {
	Turn CodexTurn `json:"turn"`
}

type codexAppServerEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type codexAppServerResponse struct {
	result json.RawMessage
	err    error
}

// CodexAppServerClient is a strict concurrent JSONL client for the supported
// v2 thread lifecycle. It never interprets provider output as Crewfold state.
type CodexAppServerClient struct {
	transport     CodexAppServerTransport
	writeMu       sync.Mutex
	mu            sync.Mutex
	nextID        int64
	pending       map[int64]chan codexAppServerResponse
	notifications chan CodexAppServerNotification
	requests      chan CodexAppServerRequest
	done          chan struct{}
	closeOnce     sync.Once
	readErr       error
}

func NewCodexAppServerClient(transport CodexAppServerTransport) (*CodexAppServerClient, error) {
	if transport == nil {
		return nil, errors.New("Codex app-server transport is required")
	}
	client := &CodexAppServerClient{
		transport: transport, pending: make(map[int64]chan codexAppServerResponse),
		notifications: make(chan CodexAppServerNotification, 256), done: make(chan struct{}),
		requests: make(chan CodexAppServerRequest, 32),
	}
	go client.readLoop()
	return client, nil
}

func (client *CodexAppServerClient) Notifications() <-chan CodexAppServerNotification {
	return client.notifications
}

func (client *CodexAppServerClient) Requests() <-chan CodexAppServerRequest {
	return client.requests
}

func (client *CodexAppServerClient) Done() <-chan struct{} {
	return client.done
}

// Respond sends the explicit result or error for one app-server initiated
// request. Callers must bind this to Crewfold's reviewed authority boundary;
// this client never approves an effect by itself.
func (client *CodexAppServerClient) Respond(id json.RawMessage, result any, responseError error) error {
	if len(id) == 0 || string(id) == "null" {
		return errors.New("Codex app-server response id is required")
	}
	if responseError != nil {
		return client.write(map[string]any{"id": json.RawMessage(id), "error": map[string]any{"code": -32000, "message": responseError.Error()}})
	}
	if result == nil {
		result = map[string]any{}
	}
	return client.write(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (client *CodexAppServerClient) Initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo":   map[string]string{"name": "crewfold", "title": "Crewfold", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}
	var response struct {
		CodexHome      string `json:"codexHome"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
		UserAgent      string `json:"userAgent"`
	}
	if err := client.call(ctx, "initialize", params, &response); err != nil {
		return err
	}
	if response.CodexHome == "" || response.PlatformFamily == "" || response.PlatformOS == "" || response.UserAgent == "" {
		return errors.New("Codex app-server initialize response is incomplete")
	}
	return client.notify("initialized", map[string]any{})
}

func (client *CodexAppServerClient) StartThread(ctx context.Context, params CodexThreadStartParams) (CodexThread, error) {
	if params.CWD == "" {
		return CodexThread{}, errors.New("Codex thread cwd is required")
	}
	var response CodexThreadResponse
	if err := client.call(ctx, "thread/start", params, &response); err != nil {
		return CodexThread{}, err
	}
	return validateCodexThread(response.Thread, false)
}

func (client *CodexAppServerClient) ResumeThread(ctx context.Context, threadID string) (CodexThread, error) {
	if threadID == "" {
		return CodexThread{}, errors.New("Codex thread id is required")
	}
	var response CodexThreadResponse
	if err := client.call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &response); err != nil {
		return CodexThread{}, err
	}
	return validateCodexThread(response.Thread, false)
}

func (client *CodexAppServerClient) ReadThread(ctx context.Context, threadID string) (CodexThread, error) {
	if threadID == "" {
		return CodexThread{}, errors.New("Codex thread id is required")
	}
	var response CodexThreadResponse
	if err := client.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &response); err != nil {
		return CodexThread{}, err
	}
	return validateCodexThread(response.Thread, false)
}

func (client *CodexAppServerClient) StartTurn(ctx context.Context, threadID, text string) (CodexTurn, error) {
	return client.StartTurnWithClientID(ctx, threadID, "", text)
}

func (client *CodexAppServerClient) StartTurnWithClientID(ctx context.Context, threadID, clientMessageID, text string) (CodexTurn, error) {
	if threadID == "" || text == "" {
		return CodexTurn{}, errors.New("Codex turn requires thread id and input")
	}
	var response CodexTurnResponse
	params := map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": text}},
	}
	if clientMessageID != "" {
		params["clientUserMessageId"] = clientMessageID
	}
	if err := client.call(ctx, "turn/start", params, &response); err != nil {
		return CodexTurn{}, err
	}
	if response.Turn.ID == "" || response.Turn.Status == "" {
		return CodexTurn{}, errors.New("Codex turn response is incomplete")
	}
	return response.Turn, nil
}

func (client *CodexAppServerClient) DeleteThread(ctx context.Context, threadID string) error {
	if threadID == "" {
		return errors.New("Codex thread id is required")
	}
	var response map[string]any
	return client.call(ctx, "thread/delete", map[string]string{"threadId": threadID}, &response)
}

func (client *CodexAppServerClient) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errors.New("Codex interrupt requires thread and turn ids")
	}
	var response map[string]any
	return client.call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, &response)
}

func (client *CodexAppServerClient) Close() error {
	client.closeOnce.Do(func() { _ = client.transport.Close() })
	<-client.done
	client.mu.Lock()
	err := client.readErr
	client.mu.Unlock()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (client *CodexAppServerClient) call(ctx context.Context, method string, params any, output any) error {
	client.mu.Lock()
	select {
	case <-client.done:
		err := client.readErr
		client.mu.Unlock()
		return fmt.Errorf("Codex app-server is closed: %w", err)
	default:
	}
	client.nextID++
	id := client.nextID
	waiter := make(chan codexAppServerResponse, 1)
	client.pending[id] = waiter
	client.mu.Unlock()
	if err := client.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		client.mu.Lock()
		delete(client.pending, id)
		client.mu.Unlock()
		return err
	}
	select {
	case response := <-waiter:
		if response.err != nil {
			return response.err
		}
		if output == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, output); err != nil {
			return fmt.Errorf("decode Codex app-server %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		client.mu.Lock()
		delete(client.pending, id)
		client.mu.Unlock()
		return ctx.Err()
	case <-client.done:
		// A peer may close immediately after writing its final response. The
		// reader delivers that response before closing done, so prefer it when
		// both channels become ready rather than losing an acknowledged call.
		select {
		case response := <-waiter:
			if response.err != nil {
				return response.err
			}
			if output == nil {
				return nil
			}
			if err := json.Unmarshal(response.result, output); err != nil {
				return fmt.Errorf("decode Codex app-server %s result: %w", method, err)
			}
			return nil
		default:
		}
		client.mu.Lock()
		err := client.readErr
		client.mu.Unlock()
		return fmt.Errorf("Codex app-server closed during %s: %w", method, err)
	}
}

func (client *CodexAppServerClient) notify(method string, params any) error {
	return client.write(map[string]any{"method": method, "params": params})
}

func (client *CodexAppServerClient) write(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	if _, err := client.transport.Write(encoded); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

func (client *CodexAppServerClient) readLoop() {
	defer close(client.done)
	defer close(client.notifications)
	defer close(client.requests)
	scanner := bufio.NewScanner(client.transport)
	scanner.Buffer(make([]byte, 64*1024), maximumCodexAppServerMessageBytes)
	for scanner.Scan() {
		var envelope codexAppServerEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			client.fail(fmt.Errorf("decode Codex app-server message: %w", err))
			return
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			select {
			case client.requests <- CodexAppServerRequest{ID: append(json.RawMessage(nil), envelope.ID...), Method: envelope.Method, Params: append(json.RawMessage(nil), envelope.Params...)}:
			default:
				client.fail(errors.New("Codex app-server request buffer is full"))
				return
			}
			continue
		}
		if len(envelope.ID) > 0 {
			var id int64
			if err := json.Unmarshal(envelope.ID, &id); err != nil {
				client.fail(fmt.Errorf("decode Codex app-server response id: %w", err))
				return
			}
			client.mu.Lock()
			waiter := client.pending[id]
			delete(client.pending, id)
			client.mu.Unlock()
			if waiter == nil {
				continue
			}
			if envelope.Error != nil {
				waiter <- codexAppServerResponse{err: fmt.Errorf("Codex app-server error %d: %s", envelope.Error.Code, envelope.Error.Message)}
			} else {
				waiter <- codexAppServerResponse{result: envelope.Result}
			}
			continue
		}
		if envelope.Method != "" {
			select {
			case client.notifications <- CodexAppServerNotification{Method: envelope.Method, Params: append(json.RawMessage(nil), envelope.Params...)}:
			default:
				client.fail(errors.New("Codex app-server notification buffer is full"))
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		client.fail(fmt.Errorf("read Codex app-server message: %w", err))
	} else {
		client.fail(io.EOF)
	}
}

func (client *CodexAppServerClient) fail(err error) {
	client.mu.Lock()
	client.readErr = err
	pending := client.pending
	client.pending = make(map[int64]chan codexAppServerResponse)
	client.mu.Unlock()
	for _, waiter := range pending {
		waiter <- codexAppServerResponse{err: err}
	}
}

func validateCodexThread(thread CodexThread, allowEphemeral bool) (CodexThread, error) {
	if thread.ID == "" || thread.CWD == "" || thread.ModelProvider == "" || thread.Status.Type == "" {
		return CodexThread{}, errors.New("Codex thread response is incomplete")
	}
	if thread.Ephemeral && !allowEphemeral {
		return CodexThread{}, errors.New("Codex durable agent thread is unexpectedly ephemeral")
	}
	return thread, nil
}
