package room

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/codexapp"
	"crewfold/internal/localipc"
	webui "crewfold/web"
)

type ServerConfig struct {
	DataDir        string
	SocketPath     string
	WebAddress     string
	Version        buildinfo.Info
	StewardRuntime StewardRuntime
	CodexRuntime   CodexDeliveryRuntime
}

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     string `json:"id"`
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

type Server struct {
	config     ServerConfig
	store      *Store
	listener   net.Listener
	http       *http.Server
	httpListen net.Listener
	startedAt  time.Time
	origin     string
	mu         sync.Mutex
	bootstrap  map[[32]byte]time.Time
	sessions   map[[32]byte]time.Time
	stewards   *StewardManager
	deliveries *DeliveryManager
	closeOnce  sync.Once
}

func RunServer(ctx context.Context, config ServerConfig) error {
	if strings.TrimSpace(config.DataDir) == "" || strings.TrimSpace(config.SocketPath) == "" {
		return errors.New("daemon requires --data-dir and --socket")
	}
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return err
	}
	socketPath, err := localipc.Normalize(config.SocketPath)
	if err != nil {
		return err
	}
	config.SocketPath = socketPath
	store, err := Open(ctx, dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	listener, err := localipc.Listen(socketPath)
	if err != nil {
		return fmt.Errorf("listen on owner endpoint: %w", err)
	}
	runtime := config.StewardRuntime
	if runtime == nil {
		runtime, err = NewHerdrStewardRuntime(socketPath, dataDir)
		if err != nil {
			_ = listener.Close()
			_ = localipc.Remove(socketPath)
			return err
		}
	}
	server := &Server{config: config, store: store, listener: listener, startedAt: time.Now().UTC(), bootstrap: map[[32]byte]time.Time{}, sessions: map[[32]byte]time.Time{}}
	cliPath, err := os.Executable()
	if err != nil {
		server.stewards = NewStewardManager(ctx, store, runtime, "crewfold", socketPath)
	} else {
		server.stewards = NewStewardManager(ctx, store, runtime, cliPath, socketPath)
	}
	if err := server.stewards.Start(); err != nil {
		server.stewards.Close()
		_ = listener.Close()
		_ = localipc.Remove(socketPath)
		return err
	}
	codexRuntime := config.CodexRuntime
	if codexRuntime == nil {
		codexSocket, socketErr := codexapp.DefaultSocketPath()
		if socketErr != nil {
			server.stewards.Close()
			_ = listener.Close()
			_ = localipc.Remove(socketPath)
			return socketErr
		}
		codexRuntime = codexapp.Client{SocketPath: codexSocket}
	}
	server.deliveries = NewDeliveryManager(ctx, store, codexRuntime, cliPath, socketPath)
	server.deliveries.Start()
	if err := server.startWeb(); err != nil {
		server.deliveries.Close()
		_ = listener.Close()
		_ = localipc.Remove(socketPath)
		return err
	}
	defer server.close()
	go func() {
		<-ctx.Done()
		server.close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go server.serveConnection(ctx, connection)
	}
}

func (s *Server) close() {
	s.closeOnce.Do(func() {
		if s.deliveries != nil {
			s.deliveries.Close()
		}
		if s.stewards != nil {
			s.stewards.Close()
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.http != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.http.Shutdown(ctx)
			cancel()
		}
		if s.httpListen != nil {
			_ = s.httpListen.Close()
		}
		_ = localipc.Remove(s.config.SocketPath)
	})
}

func (s *Server) serveConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Minute))
	scanner := bufio.NewScanner(io.LimitReader(connection, 1024*1024))
	scanner.Buffer(make([]byte, 4096), 512*1024)
	encoder := json.NewEncoder(connection)
	for scanner.Scan() {
		var request Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			_ = encoder.Encode(Response{Error: "invalid request"})
			continue
		}
		response := s.dispatch(ctx, request)
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, request Request) Response {
	result, err := s.call(ctx, request.Method, request.Params)
	if err != nil {
		return Response{ID: request.ID, Error: err.Error()}
	}
	return Response{ID: request.ID, Result: result}
}

func (s *Server) call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	decode := func(target any) error {
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		return decoder.Decode(target)
	}
	switch method {
	case "status":
		rooms, err := s.store.ListRooms(ctx)
		return map[string]any{"status": "ok", "pid": os.Getpid(), "started_at": s.startedAt.Format(time.RFC3339Nano), "rooms": len(rooms), "version": s.config.Version}, err
	case "web.bootstrap":
		return s.mintBootstrap()
	case "room.create":
		var input CreateRoomInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.store.CreateRoom(ctx, input)
	case "room.list":
		return s.store.ListRooms(ctx)
	case "room.snapshot":
		var input ListMessagesInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.store.Snapshot(ctx, input.Room, input.After, input.Limit)
	case "room.archive":
		var input struct {
			Room string `json:"room"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		if _, err := s.stewards.Stop(ctx, input.Room); err != nil && !errors.Is(err, ErrHostedStewardNotConfigured) {
			return nil, err
		}
		return s.store.Archive(ctx, input.Room)
	case "steward.start":
		var input StartStewardInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.stewards.ConfigureAndStart(ctx, input)
	case "steward.status":
		var input struct {
			Room string `json:"room"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		console, err := s.stewards.Status(ctx, input.Room)
		if errors.Is(err, ErrHostedStewardNotConfigured) {
			return nil, nil
		}
		return console, err
	case "steward.prompt":
		var input PromptStewardInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true}, s.stewards.Prompt(ctx, input)
	case "steward.key":
		var input StewardKeyInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true}, s.stewards.SendKey(ctx, input)
	case "steward.stop":
		var input struct {
			Room string `json:"room"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.stewards.Stop(ctx, input.Room)
	case "steward.restart":
		var input struct {
			Room string `json:"room"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.stewards.Restart(ctx, input.Room)
	case "participant.join":
		var input JoinInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if input.Delivery == "codex" {
			validationCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			err := s.deliveries.Validate(validationCtx, input.ThreadID)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("bind Codex delivery: %w", err)
			}
		}
		participant, err := s.store.Join(ctx, input)
		if err == nil {
			s.deliveries.Wake()
		}
		return participant, err
	case "message.send":
		var input SendInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		message, err := s.store.Send(ctx, input)
		if err == nil {
			s.deliveries.Wake()
		}
		return message, err
	case "participant.ack":
		var input AckInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.store.Ack(ctx, input)
	case "document.upload":
		var input UploadInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		message, err := s.store.Upload(ctx, input)
		if err == nil {
			s.deliveries.Wake()
		}
		return message, err
	case "document.read":
		var input struct {
			Room     string `json:"room"`
			Document string `json:"document"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		document, content, err := s.store.ReadDocument(ctx, input.Room, input.Document)
		if err != nil {
			return nil, err
		}
		return map[string]any{"document": document, "content_base64": base64.StdEncoding.EncodeToString(content)}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func (s *Server) startWeb() error {
	address := strings.TrimSpace(s.config.WebAddress)
	if address == "" {
		address = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return errors.New("web address must use 127.0.0.1")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	s.httpListen = listener
	s.origin = "http://" + listener.Addr().String()
	dist, err := fs.Sub(webui.Assets, "dist")
	if err != nil {
		return err
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return err
	}
	files := http.FileServer(http.FS(dist))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		s.secureWeb(response)
		if request.Host != listener.Addr().String() {
			http.Error(response, "invalid host", http.StatusForbidden)
			return
		}
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(index)
	})
	mux.Handle("/assets/", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		s.secureWeb(response)
		if request.Host != listener.Addr().String() {
			http.Error(response, "invalid host", http.StatusForbidden)
			return
		}
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(response, request)
	}))
	mux.HandleFunc("/api/session", s.handleWebSession)
	mux.HandleFunc("/api/rpc", s.handleWebRPC)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	go func() { _ = s.http.Serve(listener) }()
	return nil
}

func (s *Server) mintBootstrap() (map[string]any, error) {
	token, digest, err := secret()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(time.Minute)
	s.mu.Lock()
	s.pruneSecretsLocked()
	s.bootstrap[digest] = expires
	s.mu.Unlock()
	return map[string]any{"url": s.origin + "/#bootstrap=" + token, "expires_at": expires.Format(time.RFC3339Nano)}, nil
}

func (s *Server) handleWebSession(response http.ResponseWriter, request *http.Request) {
	s.secureWeb(response)
	if request.Method != http.MethodPost || request.Header.Get("Origin") != s.origin {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Bootstrap string `json:"bootstrap"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096)).Decode(&body); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	bytes, err := hex.DecodeString(body.Bootstrap)
	if err != nil || len(bytes) != 32 {
		http.Error(response, "invalid bootstrap", http.StatusUnauthorized)
		return
	}
	digest := sha256.Sum256(bytes)
	now := time.Now().UTC()
	s.mu.Lock()
	expires, exists := s.bootstrap[digest]
	delete(s.bootstrap, digest)
	s.pruneSecretsLocked()
	s.mu.Unlock()
	if !exists || !now.Before(expires) {
		http.Error(response, "expired bootstrap", http.StatusUnauthorized)
		return
	}
	token, sessionDigest, err := secret()
	if err != nil {
		http.Error(response, "session unavailable", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions[sessionDigest] = now.Add(8 * time.Hour)
	s.mu.Unlock()
	s.writeWebJSON(response, map[string]any{"token": token, "expires_at": now.Add(8 * time.Hour).Format(time.RFC3339Nano)})
}

func (s *Server) handleWebRPC(response http.ResponseWriter, request *http.Request) {
	s.secureWeb(response)
	if request.Method != http.MethodPost || request.Header.Get("Origin") != s.origin || !s.authorizeWeb(request.Header.Get("Authorization")) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	var rpc Request
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 6*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rpc); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	s.writeWebJSON(response, s.dispatch(request.Context(), rpc))
}

func (s *Server) authorizeWeb(header string) bool {
	token := strings.TrimPrefix(header, "Bearer ")
	bytes, err := hex.DecodeString(token)
	if err != nil || len(bytes) != 32 {
		return false
	}
	digest := sha256.Sum256(bytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSecretsLocked()
	expires, exists := s.sessions[digest]
	return exists && time.Now().UTC().Before(expires)
}

func (s *Server) pruneSecretsLocked() {
	now := time.Now().UTC()
	for key, expiry := range s.bootstrap {
		if !now.Before(expiry) {
			delete(s.bootstrap, key)
		}
	}
	for key, expiry := range s.sessions {
		if !now.Before(expiry) {
			delete(s.sessions, key)
		}
	}
}

func (s *Server) secureWeb(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}

func (s *Server) writeWebJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(value)
}

func secret() (string, [32]byte, error) {
	var zero [32]byte
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", zero, err
	}
	return hex.EncodeToString(buffer), sha256.Sum256(buffer), nil
}
