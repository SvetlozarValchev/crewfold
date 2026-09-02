// Package codexapp delivers room notifications to existing Codex threads.
package codexapp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	websocketGUID     = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maximumFrameBytes = 8 * 1024 * 1024
)

var (
	ErrThreadNotLoaded        = errors.New("Codex thread is not currently loaded")
	ErrDirectInputUnavailable = errors.New("Codex thread cannot accept direct input")
)

type Thread struct {
	ID                   string       `json:"id"`
	Status               ThreadStatus `json:"status"`
	CanAcceptDirectInput *bool        `json:"canAcceptDirectInput"`
}

type ThreadStatus struct {
	Type string `json:"type"`
}

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

func DefaultSocketPath() (string, error) {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		ownerHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve Codex home: %w", err)
		}
		home = filepath.Join(ownerHome, ".codex")
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("CODEX_HOME must be an absolute path")
	}
	return filepath.Join(filepath.Clean(home), "app-server-control", "app-server-control.sock"), nil
}

func (c Client) Inspect(ctx context.Context, threadID string) (Thread, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return Thread{}, errors.New("Codex thread ID is required")
	}
	connection, err := c.connect(ctx)
	if err != nil {
		return Thread{}, err
	}
	defer connection.Close()
	var response struct {
		Thread Thread `json:"thread"`
	}
	if err := connection.call("thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &response); err != nil {
		return Thread{}, fmt.Errorf("read Codex thread: %w", err)
	}
	if response.Thread.ID != threadID {
		return Thread{}, errors.New("Codex app-server returned a different thread")
	}
	return response.Thread, nil
}

// Deliver adds one visible room notification to the same Codex thread. The
// app-server starts a turn when the thread is idle and steers a steerable active
// turn when one is already running.
func (c Client) Deliver(ctx context.Context, threadID, text, messageID string) error {
	thread, err := c.Inspect(ctx, threadID)
	if err != nil {
		return err
	}
	switch thread.Status.Type {
	case "notLoaded":
		return ErrThreadNotLoaded
	case "systemError":
		return errors.New("Codex thread is in a system-error state")
	case "idle", "active":
	default:
		return fmt.Errorf("Codex thread has unsupported status %q", thread.Status.Type)
	}
	if thread.CanAcceptDirectInput != nil && !*thread.CanAcceptDirectInput {
		return ErrDirectInputUnavailable
	}
	connection, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	params := map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": text}},
	}
	if strings.TrimSpace(messageID) != "" {
		params["clientUserMessageId"] = messageID
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := connection.call("turn/start", params, &response); err != nil {
		return fmt.Errorf("deliver to Codex thread: %w", err)
	}
	if response.Turn.ID == "" {
		return errors.New("Codex app-server accepted no turn")
	}
	return nil
}

func (c Client) connect(ctx context.Context) (*rpcConnection, error) {
	path := strings.TrimSpace(c.SocketPath)
	if path == "" {
		return nil, errors.New("Codex app-server socket is not configured")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect to Codex app-server control socket: %w", err)
	}
	deadline := time.Now().Add(timeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = raw.SetDeadline(deadline)
	reader := bufio.NewReader(raw)
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = raw.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := io.WriteString(raw, request); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("start Codex WebSocket handshake: %w", err)
	}
	httpRequest, _ := http.NewRequest(http.MethodGet, "http://localhost/", nil)
	response, err := http.ReadResponse(reader, httpRequest)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("read Codex WebSocket handshake: %w", err)
	}
	accept := sha1.Sum([]byte(key + websocketGUID))
	expected := base64.StdEncoding.EncodeToString(accept[:])
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || response.Header.Get("Sec-WebSocket-Accept") != expected {
		_ = response.Body.Close()
		_ = raw.Close()
		return nil, fmt.Errorf("Codex WebSocket handshake was rejected: %s", response.Status)
	}
	connection := &rpcConnection{raw: raw, reader: reader, nextID: 1}
	var initialized map[string]any
	if err := connection.call("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "crewfold", "title": "Crewfold", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &initialized); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := connection.notify("initialized", map[string]any{}); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("acknowledge Codex app-server initialization: %w", err)
	}
	return connection, nil
}

type rpcConnection struct {
	raw    net.Conn
	reader *bufio.Reader
	nextID int64
}

func (c *rpcConnection) Close() error { return c.raw.Close() }

func (c *rpcConnection) call(method string, params any, result any) error {
	id := c.nextID
	c.nextID++
	if err := c.writeJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		payload, err := c.readMessage()
		if err != nil {
			return err
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("decode Codex app-server message: %w", err)
		}
		if envelope.Method != "" || string(envelope.ID) != strconv.FormatInt(id, 10) {
			continue
		}
		if envelope.Error != nil {
			return fmt.Errorf("app-server error %d: %s", envelope.Error.Code, envelope.Error.Message)
		}
		if result == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return nil
		}
		return json.Unmarshal(envelope.Result, result)
	}
}

func (c *rpcConnection) notify(method string, params any) error {
	return c.writeJSON(map[string]any{"method": method, "params": params})
}

func (c *rpcConnection) writeJSON(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFrame(c.raw, 0x1, payload)
}

func (c *rpcConnection) readMessage() ([]byte, error) {
	var message []byte
	started := false
	for {
		opcode, final, payload, err := readFrame(c.reader)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := writeFrame(c.raw, 0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1:
			if started {
				return nil, errors.New("unexpected WebSocket text fragment")
			}
			started = true
			message = append(message, payload...)
		case 0x0:
			if !started {
				return nil, errors.New("unexpected WebSocket continuation frame")
			}
			message = append(message, payload...)
		default:
			continue
		}
		if len(message) > maximumFrameBytes {
			return nil, errors.New("Codex app-server message is too large")
		}
		if final && started {
			return message, nil
		}
	}
}

func writeFrame(writer io.Writer, opcode byte, payload []byte) error {
	if len(payload) > maximumFrameBytes {
		return errors.New("WebSocket payload is too large")
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 0x80|127)
		length := make([]byte, 8)
		binary.BigEndian.PutUint64(length, uint64(len(payload)))
		header = append(header, length...)
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header = append(header, mask...)
	masked := make([]byte, len(payload))
	for index := range payload {
		masked[index] = payload[index] ^ mask[index%4]
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(masked)
	return err
}

func readFrame(reader io.Reader) (opcode byte, final bool, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err = io.ReadFull(reader, header); err != nil {
		return 0, false, nil, err
	}
	final = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err = io.ReadFull(reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err = io.ReadFull(reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}
	if length > maximumFrameBytes {
		return 0, false, nil, errors.New("Codex app-server frame is too large")
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(reader, mask); err != nil {
			return 0, false, nil, err
		}
	}
	payload = make([]byte, int(length))
	if _, err = io.ReadFull(reader, payload); err != nil {
		return 0, false, nil, err
	}
	if masked {
		for index := range payload {
			payload[index] ^= mask[index%4]
		}
	}
	return opcode, final, payload, nil
}
