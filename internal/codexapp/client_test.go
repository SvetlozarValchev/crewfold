package codexapp

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientInspectsAndDeliversToExistingThread(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "app-server.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var mutex sync.Mutex
	var deliveredText string
	serverErrors := make(chan error, 2)
	go func() {
		for connectionIndex := 0; connectionIndex < 2; connectionIndex++ {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverErrors <- acceptErr
				return
			}
			go func(connection net.Conn) {
				defer connection.Close()
				method, params, serveErr := serveAppServerConnection(connection)
				if serveErr == nil && method == "turn/start" {
					input, _ := params["input"].([]any)
					if len(input) > 0 {
						item, _ := input[0].(map[string]any)
						mutex.Lock()
						deliveredText, _ = item["text"].(string)
						mutex.Unlock()
					}
				}
				serverErrors <- serveErr
			}(connection)
		}
	}()

	client := Client{SocketPath: socket, Timeout: 2 * time.Second}
	if err := client.Deliver(context.Background(), "thread-test", "room message #7", "message-7"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if deliveredText != "room message #7" {
		t.Fatalf("delivered text = %q", deliveredText)
	}
}

func serveAppServerConnection(connection net.Conn) (string, map[string]any, error) {
	reader := bufio.NewReader(connection)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return "", nil, err
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	accept := sha1.Sum([]byte(key + websocketGUID))
	if _, err := fmt.Fprintf(connection, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:])); err != nil {
		return "", nil, err
	}
	if _, _, err := readRPCRequest(reader, "initialize"); err != nil {
		return "", nil, err
	}
	if err := writeServerJSON(connection, map[string]any{"id": 1, "result": map[string]any{}}); err != nil {
		return "", nil, err
	}
	if _, _, err := readRPCRequest(reader, "initialized"); err != nil {
		return "", nil, err
	}
	method, params, err := readRPCRequest(reader, "")
	if err != nil {
		return "", nil, err
	}
	switch method {
	case "thread/read":
		err = writeServerJSON(connection, map[string]any{"id": 2, "result": map[string]any{"thread": map[string]any{"id": "thread-test", "status": map[string]any{"type": "idle"}, "canAcceptDirectInput": true}}})
	case "turn/start":
		err = writeServerJSON(connection, map[string]any{"id": 2, "result": map[string]any{"turn": map[string]any{"id": "turn-test"}}})
	default:
		err = fmt.Errorf("unexpected method %q", method)
	}
	return method, params, err
}

func readRPCRequest(reader *bufio.Reader, expectedMethod string) (string, map[string]any, error) {
	opcode, _, payload, err := readFrame(reader)
	if err != nil {
		return "", nil, err
	}
	if opcode != 0x1 {
		return "", nil, fmt.Errorf("unexpected opcode %d", opcode)
	}
	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return "", nil, err
	}
	if expectedMethod != "" && request.Method != expectedMethod {
		return "", nil, fmt.Errorf("method = %q, want %q", request.Method, expectedMethod)
	}
	return request.Method, request.Params, nil
}

func writeServerJSON(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := []byte{0x81}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127)
		length := make([]byte, 8)
		binary.BigEndian.PutUint64(length, uint64(len(payload)))
		header = append(header, length...)
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}

func TestRealCodexAppServerDelivery(t *testing.T) {
	if os.Getenv("CREWFOLD_REAL_CODEX_TEST") != "1" {
		t.Skip("set CREWFOLD_REAL_CODEX_TEST=1 to use a disposable real Codex thread")
	}
	socket, err := DefaultSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("Codex app-server control socket: %v", err)
	}
	client := Client{SocketPath: socket, Timeout: 8 * time.Second}
	connection, err := client.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Thread Thread `json:"thread"`
	}
	if err := connection.call("thread/start", map[string]any{"cwd": t.TempDir(), "ephemeral": true}, &started); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	_ = connection.Close()
	if started.Thread.ID == "" {
		t.Fatal("Codex app-server returned no disposable thread ID")
	}

	deliveryContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Deliver(deliveryContext, started.Thread.ID, "[CREWFOLD DELIVERY TEST]\nReply exactly: delivery accepted. Do not use tools.", "crewfold-real-delivery-test"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		thread, err := client.Inspect(context.Background(), started.Thread.ID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return
			}
			t.Fatal(err)
		}
		if thread.Status.Type == "idle" || thread.Status.Type == "systemError" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("disposable Codex turn did not settle")
}
