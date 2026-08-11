package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/localapi"
)

func TestServerStatusStopAndSocketPermissions(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Status != "ok" {
		t.Fatalf("status.Status = %q, want ok", status.Status)
	}
	if status.Protocol != localapi.MaxProtocol {
		t.Fatalf("status.Protocol = %d, want %d", status.Protocol, localapi.MaxProtocol)
	}
	if status.PID != os.Getpid() {
		t.Fatalf("status.PID = %d, want %d", status.PID, os.Getpid())
	}
	if err := status.ServerVersion.Validate(); err != nil {
		t.Fatalf("status.ServerVersion.Validate() error = %v", err)
	}

	info, err := os.Stat(running.config.SocketPath)
	if err != nil {
		t.Fatalf("os.Stat(socket) error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("socket permissions = %04o, want 0600", permissions)
	}
	dataInfo, err := os.Stat(running.config.DataDir)
	if err != nil {
		t.Fatalf("os.Stat(data dir) error = %v", err)
	}
	if permissions := dataInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("new data-directory permissions = %04o, want 0700", permissions)
	}
	lockInfo, err := os.Stat(filepath.Join(running.config.DataDir, "daemon.lock"))
	if err != nil {
		t.Fatalf("os.Stat(lock) error = %v", err)
	}
	if permissions := lockInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("lock permissions = %04o, want 0600", permissions)
	}

	stopResult, err := client.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopResult.Status != "stopping" {
		t.Fatalf("stopResult.Status = %q, want stopping", stopResult.Status)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Lstat(running.config.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after shutdown error = %v, want not exist", err)
	}
}

func TestSecondDaemonCannotClaimLiveDataDirOrSocket(t *testing.T) {
	t.Parallel()

	first := startTestServer(t, testConfig(t))

	sameData := first.config
	sameData.SocketPath = filepath.Join(t.TempDir(), "other.sock")
	err := Run(context.Background(), sameData)
	if ErrorCode(err) != CodeDataDirInUse {
		t.Fatalf("same data directory error = %v, code = %q, want %q", err, ErrorCode(err), CodeDataDirInUse)
	}

	sameSocket := first.config
	sameSocket.DataDir = filepath.Join(t.TempDir(), "other-data")
	err = Run(context.Background(), sameSocket)
	if ErrorCode(err) != CodeSocketInUse {
		t.Fatalf("same socket error = %v, code = %q, want %q", err, ErrorCode(err), CodeSocketInUse)
	}

	if _, err := localapi.NewClient(first.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
}

func TestStaleSocketIsRecovered(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("net.ListenUnix() error = %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	if _, err := os.Lstat(config.SocketPath); err != nil {
		t.Fatalf("stale socket should exist: %v", err)
	}

	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() after stale socket recovery error = %v", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNonSocketPathIsRefusedAndPreserved(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	const contents = "belongs to the user\n"
	if err := os.WriteFile(config.SocketPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile(socket path) error = %v", err)
	}

	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeSocketPathOccupied {
		t.Fatalf("Run() error = %v, code = %q, want %q", err, ErrorCode(err), CodeSocketPathOccupied)
	}
	data, readErr := os.ReadFile(config.SocketPath)
	if readErr != nil {
		t.Fatalf("os.ReadFile(socket path) error = %v", readErr)
	}
	if string(data) != contents {
		t.Fatalf("socket-path file = %q, want preserved %q", data, contents)
	}
}

func TestDataDirectoryLockSymlinkIsRefusedAndTargetPreserved(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll(data dir) error = %v", err)
	}
	targetPath := filepath.Join(t.TempDir(), "user-file")
	const contents = "must remain unchanged\n"
	if err := os.WriteFile(targetPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(targetPath, filepath.Join(config.DataDir, "daemon.lock")); err != nil {
		t.Fatalf("os.Symlink(lock) error = %v", err)
	}

	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeInvalidConfiguration {
		t.Fatalf("Run() error = %v, code = %q, want %q", err, ErrorCode(err), CodeInvalidConfiguration)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("os.ReadFile(target) error = %v", readErr)
	}
	if string(data) != contents {
		t.Fatalf("target contents = %q, want preserved %q", data, contents)
	}
}

func TestShutdownClosesInflightPartialRequest(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	partial, err := net.Dial("unix", running.config.SocketPath)
	if err != nil {
		t.Fatalf("net.Dial(partial) error = %v", err)
	}
	defer partial.Close()
	if _, err := partial.Write([]byte(`{"id":"partial"`)); err != nil {
		t.Fatalf("partial.Write() error = %v", err)
	}

	if _, err := localapi.NewClient(running.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if err := partial.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("partial.SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := partial.Read(buffer); err == nil {
		t.Fatal("partial.Read() error = nil, want connection closed during shutdown")
	}
}

func TestHelloRejectsUnsupportedProtocolRange(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	connection, err := net.Dial("unix", running.config.SocketPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer connection.Close()

	params, err := json.Marshal(localapi.HelloParams{MinProtocol: 2, MaxProtocol: 3})
	if err != nil {
		t.Fatalf("json.Marshal(params) error = %v", err)
	}
	request := localapi.Request{ID: "unsupported", Method: localapi.MethodHello, Params: params}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatalf("Encode(request) error = %v", err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatalf("ReadBytes(response) error = %v", err)
	}
	var response localapi.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	if response.Error == nil || response.Error.Code != "protocol_mismatch" {
		t.Fatalf("response.Error = %#v, want protocol_mismatch", response.Error)
	}

	if _, err := localapi.NewClient(running.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRequestLogsCarryCorrelationFields(t *testing.T) {
	t.Parallel()

	var logBuffer lockedBuffer
	config := testConfig(t)
	config.Logger = slog.New(slog.NewJSONHandler(&logBuffer, nil))
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logs := logBuffer.String()
	for _, expected := range []string{
		`"msg":"daemon started"`,
		`"msg":"request completed"`,
		`"request_id":"req-`,
		`"method":"system.status"`,
		`"msg":"daemon stopped"`,
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("logs do not contain %q:\n%s", expected, logs)
		}
	}
}

type runningServer struct {
	config Config
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startTestServer(t *testing.T, config Config) *runningServer {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	running := &runningServer{
		config: config,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		running.err = Run(ctx, config)
		close(running.done)
	}()
	t.Cleanup(func() {
		running.cancel()
		if err := running.wait(); err != nil {
			t.Errorf("Run() cleanup error = %v", err)
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	client := localapi.NewClient(config.SocketPath)
	for {
		if _, err := client.Status(context.Background()); err == nil {
			break
		}
		select {
		case <-running.done:
			t.Fatalf("server exited before readiness: %v", running.wait())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for server readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}

	return running
}

func (running *runningServer) wait() error {
	<-running.done
	return running.err
}

func testConfig(t *testing.T) Config {
	t.Helper()

	root := t.TempDir()
	return Config{
		DataDir:    filepath.Join(root, "data"),
		SocketPath: filepath.Join(root, "crewfold.sock"),
		Version:    buildinfo.Current(),
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestErrorCodeFallsBackForUnknownErrors(t *testing.T) {
	t.Parallel()

	if code := ErrorCode(syscall.EIO); code != "daemon_failed" {
		t.Fatalf("ErrorCode(EIO) = %q, want daemon_failed", code)
	}
}
