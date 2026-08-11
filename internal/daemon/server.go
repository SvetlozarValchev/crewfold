// Package daemon implements Crewfold's foreground local control-plane process.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/localapi"
)

const (
	maxRequestBytes = 64 * 1024
	socketProbeTime = 250 * time.Millisecond
)

type Config struct {
	DataDir    string
	SocketPath string
	Version    buildinfo.Info
	Logger     *slog.Logger
}

type server struct {
	config         Config
	listener       *net.UnixListener
	socketInfo     os.FileInfo
	startedAt      time.Time
	stopOnce       sync.Once
	stopCh         chan struct{}
	shutdown       atomic.Bool
	activeRequests atomic.Int64
	connectionsMu  sync.Mutex
	connections    map[net.Conn]struct{}
	handlers       sync.WaitGroup
}

// Run owns the daemon lifecycle until the context is cancelled or system.stop is
// accepted. It performs no backgrounding and returns only after handlers stop.
func Run(ctx context.Context, config Config) error {
	resolved, err := resolveConfig(config)
	if err != nil {
		return err
	}

	dataLock, err := acquireDataDirLock(resolved.DataDir)
	if err != nil {
		return err
	}
	defer dataLock.release()

	if err := prepareSocketPath(resolved.SocketPath); err != nil {
		return err
	}

	address := &net.UnixAddr{Name: resolved.SocketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return &StartupError{Code: CodeSocketUnavailable, Message: "listen on local API socket", Cause: err}
	}
	listener.SetUnlinkOnClose(false)

	if err := os.Chmod(resolved.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(resolved.SocketPath)
		return &StartupError{Code: CodeSocketUnavailable, Message: "set local API socket permissions", Cause: err}
	}
	socketInfo, err := os.Lstat(resolved.SocketPath)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(resolved.SocketPath)
		return &StartupError{Code: CodeSocketUnavailable, Message: "inspect local API socket", Cause: err}
	}

	instance := &server{
		config:      resolved,
		listener:    listener,
		socketInfo:  socketInfo,
		startedAt:   time.Now().UTC(),
		stopCh:      make(chan struct{}),
		connections: make(map[net.Conn]struct{}),
	}
	defer instance.cleanupSocket()

	resolved.Logger.Info("daemon started",
		"component", "daemon",
		"socket", resolved.SocketPath,
		"data_dir", resolved.DataDir,
		"pid", os.Getpid(),
		"protocol_min", localapi.MinProtocol,
		"protocol_max", localapi.MaxProtocol,
	)

	err = instance.serve(ctx)
	if err != nil {
		resolved.Logger.Error("daemon stopped with error", "component", "daemon", "error", err)
		return err
	}
	resolved.Logger.Info("daemon stopped", "component", "daemon")
	return nil
}

func resolveConfig(config Config) (Config, error) {
	if strings.TrimSpace(config.DataDir) == "" {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "--data-dir is required"}
	}
	if strings.TrimSpace(config.SocketPath) == "" {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "--socket is required"}
	}

	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "resolve data directory", Cause: err}
	}
	socketPath, err := filepath.Abs(config.SocketPath)
	if err != nil {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "resolve socket path", Cause: err}
	}
	if dataDir == socketPath {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "data directory and socket path must differ"}
	}
	if err := config.Version.Validate(); err != nil {
		return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "invalid daemon build metadata", Cause: err}
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	config.DataDir = dataDir
	config.SocketPath = socketPath
	return config, nil
}

func prepareSocketPath(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &StartupError{Code: CodeSocketUnavailable, Message: "inspect local API socket path", Cause: err}
	}
	if info.Mode()&os.ModeSocket == 0 {
		return &StartupError{
			Code:    CodeSocketPathOccupied,
			Message: fmt.Sprintf("socket path is occupied by a non-socket file: %s", socketPath),
		}
	}

	connection, dialErr := net.DialTimeout("unix", socketPath, socketProbeTime)
	if dialErr == nil {
		_ = connection.Close()
		return &StartupError{
			Code:    CodeSocketInUse,
			Message: fmt.Sprintf("socket is served by a live process: %s", socketPath),
		}
	}
	if errors.Is(dialErr, syscall.ENOENT) {
		return nil
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return &StartupError{
			Code:    CodeSocketPathOccupied,
			Message: fmt.Sprintf("socket path exists but its owner cannot be identified safely: %s", socketPath),
			Cause:   dialErr,
		}
	}

	current, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &StartupError{Code: CodeSocketUnavailable, Message: "reinspect stale local API socket", Cause: err}
	}
	if current.Mode()&os.ModeSocket == 0 || !os.SameFile(info, current) {
		return &StartupError{
			Code:    CodeSocketPathOccupied,
			Message: fmt.Sprintf("socket path changed during stale-socket inspection: %s", socketPath),
		}
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &StartupError{Code: CodeSocketUnavailable, Message: "remove stale local API socket", Cause: err}
	}
	return nil
}

func (s *server) serve(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			s.requestStop("context cancelled")
		case <-s.stopCh:
		}
		_ = s.listener.Close()
		s.closeConnections()
	}()

	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if s.shutdown.Load() || errors.Is(err, net.ErrClosed) {
				break
			}
			s.requestStop("accept failed")
			s.closeConnections()
			s.handlers.Wait()
			return fmt.Errorf("accept local API connection: %w", err)
		}

		s.registerConnection(connection)
		s.handlers.Add(1)
		go s.handleConnection(connection)
	}

	s.closeConnections()
	s.handlers.Wait()
	return nil
}

func (s *server) handleConnection(connection net.Conn) {
	defer s.handlers.Done()
	defer s.unregisterConnection(connection)
	defer connection.Close()

	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maxRequestBytes)
	encoder := json.NewEncoder(connection)

	for scanner.Scan() {
		line := scanner.Bytes()
		var request localapi.Request
		if err := json.Unmarshal(line, &request); err != nil {
			response := localapi.ErrorResponse("", 0, &localapi.APIError{
				Code:      "invalid_request",
				Message:   "request is not valid JSON",
				Retryable: false,
			})
			_ = encoder.Encode(response)
			s.config.Logger.Warn("request rejected",
				"component", "local_api",
				"request_id", "",
				"error_code", "invalid_request",
			)
			continue
		}

		started := time.Now()
		s.activeRequests.Add(1)
		response, stop := s.handleRequest(request)
		encodeErr := encoder.Encode(response)
		s.activeRequests.Add(-1)

		status := "ok"
		errorCode := ""
		if response.Error != nil {
			status = "error"
			errorCode = response.Error.Code
		}
		attributes := []any{
			"component", "local_api",
			"request_id", request.ID,
			"method", request.Method,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		if errorCode != "" {
			attributes = append(attributes, "error_code", errorCode)
		}
		s.config.Logger.Info("request completed", attributes...)

		if encodeErr != nil {
			return
		}
		if stop {
			s.requestStop("API stop requested")
			return
		}
	}

	if err := scanner.Err(); err != nil && !s.shutdown.Load() {
		s.config.Logger.Warn("connection read failed", "component", "local_api", "error", err)
	}
}

func (s *server) handleRequest(request localapi.Request) (localapi.Response, bool) {
	if strings.TrimSpace(request.ID) == "" || len(request.ID) > 128 {
		return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
			Code:      "invalid_request",
			Message:   "request id must contain 1 to 128 characters",
			Retryable: false,
		}), false
	}

	if request.Method == localapi.MethodHello {
		return s.handleHello(request), false
	}

	if request.Protocol < localapi.MinProtocol || request.Protocol > localapi.MaxProtocol {
		return localapi.ErrorResponse(request.ID, request.Protocol, protocolMismatch()), false
	}

	switch request.Method {
	case localapi.MethodStatus:
		return localapi.MarshalResult(request.ID, request.Protocol, localapi.StatusResult{
			Schema:          localapi.StatusSchema,
			Type:            "system_status",
			Status:          "ok",
			Protocol:        request.Protocol,
			PID:             os.Getpid(),
			StartedAt:       s.startedAt.Format(time.RFC3339Nano),
			UptimeMillis:    time.Since(s.startedAt).Milliseconds(),
			ServerVersion:   s.config.Version,
			ActiveRequests:  int(s.activeRequests.Load()),
			ShutdownPending: s.shutdown.Load(),
		}), false
	case localapi.MethodStop:
		return localapi.MarshalResult(request.ID, request.Protocol, localapi.StopResult{
			Schema: localapi.StopSchema,
			Type:   "stop_acknowledgement",
			Status: "stopping",
		}), true
	default:
		return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
			Code:      "method_not_found",
			Message:   fmt.Sprintf("unknown local API method %q", request.Method),
			Retryable: false,
		}), false
	}
}

func (s *server) handleHello(request localapi.Request) localapi.Response {
	var params localapi.HelloParams
	if err := json.Unmarshal(request.Params, &params); err != nil || params.MinProtocol <= 0 || params.MaxProtocol < params.MinProtocol {
		return localapi.ErrorResponse(request.ID, 0, &localapi.APIError{
			Code:      "invalid_request",
			Message:   "hello requires a valid min_protocol and max_protocol range",
			Retryable: false,
		})
	}

	selected := min(params.MaxProtocol, localapi.MaxProtocol)
	if selected < max(params.MinProtocol, localapi.MinProtocol) {
		return localapi.ErrorResponse(request.ID, 0, protocolMismatch())
	}

	return localapi.MarshalResult(request.ID, selected, localapi.HelloResult{
		Type:             "hello",
		SelectedProtocol: selected,
		ServerMin:        localapi.MinProtocol,
		ServerMax:        localapi.MaxProtocol,
		Version:          s.config.Version,
	})
}

func protocolMismatch() *localapi.APIError {
	return &localapi.APIError{
		Code:      "protocol_mismatch",
		Message:   "client and daemon do not share a supported protocol version",
		Retryable: false,
		Details: map[string]any{
			"server_min_protocol": localapi.MinProtocol,
			"server_max_protocol": localapi.MaxProtocol,
		},
	}
}

func (s *server) requestStop(reason string) {
	s.stopOnce.Do(func() {
		s.shutdown.Store(true)
		s.config.Logger.Info("daemon shutdown requested", "component", "daemon", "reason", reason)
		close(s.stopCh)
	})
}

func (s *server) registerConnection(connection net.Conn) {
	s.connectionsMu.Lock()
	s.connections[connection] = struct{}{}
	s.connectionsMu.Unlock()
}

func (s *server) unregisterConnection(connection net.Conn) {
	s.connectionsMu.Lock()
	delete(s.connections, connection)
	s.connectionsMu.Unlock()
}

func (s *server) closeConnections() {
	s.connectionsMu.Lock()
	connections := make([]net.Conn, 0, len(s.connections))
	for connection := range s.connections {
		connections = append(connections, connection)
	}
	s.connectionsMu.Unlock()

	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (s *server) cleanupSocket() {
	_ = s.listener.Close()
	current, err := os.Lstat(s.config.SocketPath)
	if err != nil {
		return
	}
	if os.SameFile(s.socketInfo, current) {
		_ = os.Remove(s.config.SocketPath)
	}
}
