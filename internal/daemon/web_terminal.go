package daemon

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"crewfold/internal/execution"
	"crewfold/internal/store"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	workbenchTerminalGrantTTL = 30 * time.Second
	maximumTerminalInputBytes = 4096
	maximumTerminalFrameBytes = 8192
)

type workbenchTerminalGrantRequest struct {
	Workspace string `json:"workspace"`
	Run       string `json:"run"`
}

type workbenchTerminalClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func (w *workbenchServer) handleWorkbenchTerminalGrant(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "workbench terminal grant") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebRequestBytes))
	if err != nil || rejectDuplicateJSONFields(body) != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "terminal grant request is not exact bounded JSON")
		return
	}
	var params workbenchTerminalGrantRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Run, "run_") {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "terminal grant requires exact workspace and run")
		return
	}
	detail, driver, err := w.daemon.interactiveRun(params.Workspace, params.Run)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	attacher, supported := driver.(execution.RuntimeAttacher)
	if !supported {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime does not support an interactive terminal"})
		return
	}
	probeContext, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	_, err = attacher.Attach(probeContext, detail.Run.ID, detail.Run.RuntimeHandle)
	cancel()
	if err != nil {
		w.writeStoreError(response, &store.Error{Code: store.CodeRuntimeFailed, Message: "prepare browser terminal", Cause: err})
		return
	}
	token, digest, err := randomSecret()
	if err != nil {
		w.writeError(response, http.StatusInternalServerError, "terminal_unavailable", "could not create a terminal grant")
		return
	}
	expiresAt := w.now().UTC().Add(workbenchTerminalGrantTTL)
	w.mu.Lock()
	w.removeExpiredLocked(w.now())
	if len(w.terminalGrants) >= maxWorkbenchTerminals*2 {
		w.mu.Unlock()
		w.writeError(response, http.StatusServiceUnavailable, "terminal_capacity_exhausted", "too many terminal grants are pending")
		return
	}
	w.terminalGrants[digest] = workbenchTerminalGrant{expiresAt: expiresAt, routeHash: session.routeHash, workspace: detail.Run.WorkspaceID, run: detail.Run.ID}
	w.mu.Unlock()
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:terminal-grant:v1", "type": "terminal_grant",
		"run_id": detail.Run.ID, "runtime": detail.Run.Runtime,
		"websocket_path": "terminal", "protocol": "crewfold-terminal." + token,
		"expires_at": expiresAt.Format(time.RFC3339Nano),
	})
}

func (w *workbenchServer) handleWorkbenchTerminal(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if request.Method != http.MethodGet || request.Header.Get("Origin") != w.origin {
		w.writeError(response, http.StatusForbidden, "origin_mismatch", "terminal origin does not match the owner-local workbench")
		return
	}
	presented := ""
	for _, protocol := range websocket.Subprotocols(request) {
		if strings.HasPrefix(protocol, "crewfold-terminal.") {
			presented = protocol
			break
		}
	}
	token := strings.TrimPrefix(presented, "crewfold-terminal.")
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 || len(token) != 64 {
		w.writeError(response, http.StatusUnauthorized, "invalid_terminal_grant", "terminal grant is invalid or expired")
		return
	}
	digest := sha256Digest(raw)
	w.mu.Lock()
	grant, exists := w.terminalGrants[digest]
	delete(w.terminalGrants, digest)
	w.removeExpiredLocked(w.now())
	w.mu.Unlock()
	if !exists || !w.now().Before(grant.expiresAt) || grant.routeHash != session.routeHash {
		w.writeError(response, http.StatusUnauthorized, "invalid_terminal_grant", "terminal grant is invalid or expired")
		return
	}
	select {
	case w.terminals <- struct{}{}:
		defer func() { <-w.terminals }()
	default:
		w.writeError(response, http.StatusServiceUnavailable, "terminal_capacity_exhausted", "too many browser terminals are active")
		return
	}
	detail, driver, err := w.daemon.interactiveRun(grant.workspace, grant.run)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	attacher, supported := driver.(execution.RuntimeAttacher)
	if !supported {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "run runtime does not support an interactive terminal"})
		return
	}
	attachContext, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	spec, err := attacher.Attach(attachContext, detail.Run.ID, detail.Run.RuntimeHandle)
	cancel()
	if err != nil {
		w.writeStoreError(response, &store.Error{Code: store.CodeRuntimeFailed, Message: "prepare browser terminal", Cause: err})
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		Subprotocols:     []string{presented},
		CheckOrigin: func(request *http.Request) bool {
			return request.Header.Get("Origin") == w.origin && request.Host == w.host
		},
	}
	connection, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(maximumTerminalFrameBytes)

	terminalContext, terminalCancel := context.WithCancel(request.Context())
	defer terminalCancel()
	command := exec.CommandContext(terminalContext, spec.Executable, spec.Arguments...)
	command.Env = exactAttachEnvironment(spec.Environment)
	pseudoterminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "terminal process unavailable"), time.Now().Add(time.Second))
		return
	}
	defer func() {
		_ = pseudoterminal.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	var writer sync.Mutex
	write := func(kind int, payload []byte) error {
		writer.Lock()
		defer writer.Unlock()
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return connection.WriteMessage(kind, payload)
	}
	readFinished := make(chan struct{})
	go func() {
		defer close(readFinished)
		buffer := make([]byte, 16*1024)
		for {
			count, readErr := pseudoterminal.Read(buffer)
			if count > 0 && write(websocket.BinaryMessage, append([]byte(nil), buffer[:count]...)) != nil {
				return
			}
			if readErr != nil {
				return
			}
		}
	}()
	type clientFrame struct {
		kind    int
		payload []byte
		err     error
	}
	clientFrames := make(chan clientFrame, 1)
	go func() {
		for {
			kind, payload, readErr := connection.ReadMessage()
			select {
			case clientFrames <- clientFrame{kind: kind, payload: payload, err: readErr}:
			case <-terminalContext.Done():
				return
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		var frame clientFrame
		select {
		case <-readFinished:
			_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "terminal attach ended"))
			return
		case <-terminalContext.Done():
			return
		case <-w.daemon.stopCh:
			return
		case frame = <-clientFrames:
		}
		if frame.err != nil {
			return
		}
		if frame.kind != websocket.TextMessage || len(frame.payload) > maximumTerminalFrameBytes {
			_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "terminal control frames must be bounded JSON"))
			return
		}
		var message workbenchTerminalClientMessage
		decoder := json.NewDecoder(bytes.NewReader(frame.payload))
		decoder.DisallowUnknownFields()
		if rejectDuplicateJSONFields(frame.payload) != nil || decoder.Decode(&message) != nil || decodeHasTrailingValue(decoder) {
			_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid terminal control frame"))
			return
		}
		switch message.Type {
		case "input":
			if message.Data == "" || len(message.Data) > maximumTerminalInputBytes || !utf8.ValidString(message.Data) {
				_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid terminal input"))
				return
			}
			if _, err := io.WriteString(pseudoterminal, message.Data); err != nil {
				return
			}
		case "resize":
			if message.Cols < 20 || message.Cols > 400 || message.Rows < 5 || message.Rows > 200 {
				_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid terminal size"))
				return
			}
			if err := pty.Setsize(pseudoterminal, &pty.Winsize{Rows: message.Rows, Cols: message.Cols}); err != nil {
				return
			}
		default:
			_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "unknown terminal control type"))
			return
		}
	}
}

func exactAttachEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, pair := range os.Environ() {
		key, value, found := strings.Cut(pair, "=")
		if found {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}
