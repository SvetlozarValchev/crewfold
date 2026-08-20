package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crewfold/internal/herdr"
	"github.com/gorilla/websocket"
)

type HerdrCodexAppServerOptions struct {
	CodexExecutable string
	CodexHome       string
	HerdrExecutable string
	HerdrSession    string
	StateRoot       string
	Label           string
}

// StartCodexAppServerInHerdr gives the current durable-agent epoch one real
// Herdr-owned process surface while retaining app-server's structured protocol
// for exact turns and tool receipts. Closing the transport closes the Herdr
// workspace; the persistent Codex thread remains provider-owned and resumable.
func StartCodexAppServerInHerdr(ctx context.Context, options HerdrCodexAppServerOptions) (CodexAppServerTransport, error) {
	stateRoot := filepath.Clean(strings.TrimSpace(options.StateRoot))
	if stateRoot == "." || !filepath.IsAbs(stateRoot) {
		return nil, errors.New("Herdr Codex app-server state root must be absolute")
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create Herdr Codex host root: %w", err)
	}
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("protect Herdr Codex host root: %w", err)
	}
	hostRoot, err := os.MkdirTemp(stateRoot, ".e-")
	if err != nil {
		return nil, fmt.Errorf("create Herdr Codex host staging root: %w", err)
	}
	cleanupRoot := true
	defer func() {
		if cleanupRoot {
			_ = os.RemoveAll(hostRoot)
		}
	}()
	if err := os.Chmod(hostRoot, 0o700); err != nil {
		return nil, fmt.Errorf("protect Herdr Codex host staging root: %w", err)
	}

	client := herdr.NewClient(options.HerdrExecutable, options.HerdrSession, nil)
	label := strings.TrimSpace(options.Label)
	if label == "" {
		label = "crewfold-agent-" + filepath.Base(hostRoot)
	}
	surface, err := client.CreateWorkspace(ctx, hostRoot, label, nil)
	if err != nil {
		return nil, fmt.Errorf("create Herdr durable-agent workspace: %w", err)
	}
	cleanupWorkspace := true
	defer func() {
		if cleanupWorkspace {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = client.CloseWorkspace(cleanupCtx, surface.Workspace.WorkspaceID)
			cancel()
		}
	}()

	// Codex canonicalizes this path before bind(2). Fail immediately instead of
	// waiting for a child that can never create an overlong sockaddr_un.
	socketPath := filepath.Join(hostRoot, "a.sock")
	if len(socketPath) >= 108 {
		return nil, fmt.Errorf("Herdr Codex app-server socket path is too long (%d bytes); configure a shorter Crewfold socket directory", len(socketPath))
	}
	codexExecutable := strings.TrimSpace(options.CodexExecutable)
	if codexExecutable == "" {
		codexExecutable = "codex"
	}
	command := "exec "
	if codexHome := strings.TrimSpace(options.CodexHome); codexHome != "" {
		command += "env CODEX_HOME=" + shellQuote(codexHome) + " "
	}
	command += shellQuote(codexExecutable) + " app-server --listen " + shellQuote("unix://"+socketPath)
	if err := client.RunPane(ctx, surface.Pane.PaneID, command); err != nil {
		return nil, fmt.Errorf("start Codex app-server in Herdr: %w", err)
	}

	var connection CodexAppServerTransport
	for {
		connection, err = dialCodexAppServerUnixWebSocket(ctx, socketPath)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			diagnosticCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			output, _ := client.ReadPane(diagnosticCtx, surface.Pane.PaneID, 40)
			cancel()
			return nil, fmt.Errorf("connect to Herdr Codex app-server: %w: %s", ctx.Err(), strings.TrimSpace(output))
		case <-time.After(25 * time.Millisecond):
		}
	}
	cleanupRoot = false
	cleanupWorkspace = false
	return &herdrCodexAppServerTransport{
		transport: connection, client: client, workspaceID: surface.Workspace.WorkspaceID, paneID: surface.Pane.PaneID,
		hostRoot: hostRoot,
	}, nil
}

func dialCodexAppServerUnixWebSocket(ctx context.Context, socketPath string) (CodexAppServerTransport, error) {
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	connection, response, err := dialer.DialContext(ctx, "ws://crewfold.local/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return &webSocketJSONTransport{connection: connection}, nil
}

type webSocketJSONTransport struct {
	connection *websocket.Conn
	readMu     sync.Mutex
	writeMu    sync.Mutex
	reader     io.Reader
	closed     bool
}

func (transport *webSocketJSONTransport) Read(value []byte) (int, error) {
	transport.readMu.Lock()
	defer transport.readMu.Unlock()
	for {
		for transport.reader == nil {
			messageType, reader, err := transport.connection.NextReader()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return 0, io.EOF
				}
				return 0, err
			}
			if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
				continue
			}
			transport.reader = io.MultiReader(reader, strings.NewReader("\n"))
		}
		count, err := transport.reader.Read(value)
		if err != io.EOF {
			return count, err
		}
		transport.reader = nil
		if count != 0 {
			return count, nil
		}
		// EOF here only means one WebSocket message was consumed. Continue to
		// the next frame; the connection itself owns stream termination.
	}
}

func (transport *webSocketJSONTransport) Write(value []byte) (int, error) {
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	payload := bytes.TrimSuffix(value, []byte("\n"))
	if bytes.Contains(payload, []byte("\n")) {
		return 0, errors.New("Codex app-server WebSocket write contains multiple JSONL records")
	}
	if err := transport.connection.WriteMessage(websocket.TextMessage, payload); err != nil {
		return 0, err
	}
	return len(value), nil
}

func (transport *webSocketJSONTransport) Close() error {
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	if transport.closed {
		return nil
	}
	transport.closed = true
	return transport.connection.Close()
}

type herdrCodexAppServerTransport struct {
	transport   CodexAppServerTransport
	client      *herdr.Client
	workspaceID string
	paneID      string
	hostRoot    string
	closeOnce   sync.Once
	closeErr    error
}

func (transport *herdrCodexAppServerTransport) diagnostic(ctx context.Context) string {
	if transport.client == nil || transport.paneID == "" {
		return ""
	}
	text, _ := transport.client.ReadPane(ctx, transport.paneID, 80)
	return strings.TrimSpace(text)
}

func (transport *herdrCodexAppServerTransport) Read(value []byte) (int, error) {
	return transport.transport.Read(value)
}

func (transport *herdrCodexAppServerTransport) Write(value []byte) (int, error) {
	return transport.transport.Write(value)
}

func (transport *herdrCodexAppServerTransport) Close() error {
	transport.closeOnce.Do(func() {
		if transport.transport != nil {
			transport.closeErr = transport.transport.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := transport.client.CloseWorkspace(ctx, transport.workspaceID); err != nil && transport.closeErr == nil {
			transport.closeErr = err
		}
		cancel()
		if err := os.RemoveAll(transport.hostRoot); err != nil && transport.closeErr == nil {
			transport.closeErr = err
		}
	})
	return transport.closeErr
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
