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
	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/gitstate"
	"crewfold/internal/localapi"
	"crewfold/internal/mcp"
	"crewfold/internal/store"
)

const (
	maxRequestBytes = 64 * 1024
	socketProbeTime = 250 * time.Millisecond
)

type Config struct {
	DataDir          string
	SocketPath       string
	Version          buildinfo.Info
	Logger           *slog.Logger
	StoreOptions     store.Options
	GitInspector     gitstate.Inspector
	RuntimeDrivers   map[string]execution.RuntimeDriver
	ProviderAdapters map[string]execution.ProviderAdapter
	RunWorkerHook    func(string, domain.Run) error
	MessageWake      func(context.Context, domain.MessageWakeJob) error
	HerdrExecutable  string
	HerdrSession     string
	DisableRunWorker bool
	defaultProviders bool
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
	workers        sync.WaitGroup
	store          *store.Store
	gitInspector   gitstate.Inspector
	runtimes       map[string]execution.RuntimeDriver
	providers      map[string]execution.ProviderAdapter
	capabilities   *runCapabilityManager
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

	storage, err := store.Open(ctx, resolved.DataDir, resolved.StoreOptions)
	if err != nil {
		return &StartupError{Code: CodeDatabaseUnavailable, Message: "initialize Crewfold database", Cause: err}
	}
	defer storage.Close()
	capabilities, err := newRunCapabilityManager(resolved.DataDir, resolved.SocketPath)
	if err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "initialize scoped run capabilities", Cause: err}
	}
	if resolved.defaultProviders {
		resolved.ProviderAdapters["fixture-mcp"] = execution.NewFixtureMCPProvider(capabilities)
		resolved.ProviderAdapters["fixture-terminal"] = execution.NewFixtureTerminalProvider(capabilities)
	}

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
		config:       resolved,
		listener:     listener,
		socketInfo:   socketInfo,
		startedAt:    time.Now().UTC(),
		stopCh:       make(chan struct{}),
		connections:  make(map[net.Conn]struct{}),
		store:        storage,
		gitInspector: resolved.GitInspector,
		runtimes:     resolved.RuntimeDrivers,
		providers:    resolved.ProviderAdapters,
		capabilities: capabilities,
	}
	defer instance.cleanupSocket()
	instance.startRunWorker()
	instance.processMessageWakeJobs()

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
	if config.GitInspector == nil {
		config.GitInspector = gitstate.NewInspector()
	}
	if config.RuntimeDrivers == nil {
		fakeRuntime := execution.NewFakeRuntime()
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "resolve daemon executable for direct runtime", Cause: executableErr}
		}
		directRuntime := execution.NewDirectRuntime(execution.DirectRuntimeOptions{
			StateRoot: filepath.Join(dataDir, "runtime"), SupervisorExecutable: executable,
		})
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
		herdrRuntime := execution.NewHerdrRuntime(execution.HerdrRuntimeOptions{
			StateRoot: filepath.Join(dataDir, "runtime", "herdr"), HerdrExecutable: herdrExecutable,
			CrewfoldExecutable: executable, Session: herdrSession,
		})
		config.RuntimeDrivers = map[string]execution.RuntimeDriver{
			fakeRuntime.Name():   fakeRuntime,
			directRuntime.Name(): directRuntime,
			herdrRuntime.Name():  herdrRuntime,
		}
	}
	if config.ProviderAdapters == nil {
		config.defaultProviders = true
		fakeProvider := execution.FakeProvider{}
		fixtureProvider := execution.NewFixtureProvider()
		config.ProviderAdapters = map[string]execution.ProviderAdapter{
			fakeProvider.Name():    fakeProvider,
			fixtureProvider.Name(): fixtureProvider,
		}
	}
	for name, driver := range config.RuntimeDrivers {
		if driver == nil || strings.TrimSpace(name) == "" || name != driver.Name() {
			return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "runtime driver registry contains an invalid entry"}
		}
	}
	for name, adapter := range config.ProviderAdapters {
		if adapter == nil || strings.TrimSpace(name) == "" || name != adapter.Name() {
			return Config{}, &StartupError{Code: CodeInvalidConfiguration, Message: "provider adapter registry contains an invalid entry"}
		}
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
			s.workers.Wait()
			return fmt.Errorf("accept local API connection: %w", err)
		}

		s.registerConnection(connection)
		s.handlers.Add(1)
		go s.handleConnection(connection)
	}

	s.closeConnections()
	s.handlers.Wait()
	s.workers.Wait()
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
		var envelope struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if json.Unmarshal(line, &envelope) == nil && envelope.JSONRPC == mcp.JSONRPCVersion {
			var request mcp.Request
			if err := json.Unmarshal(line, &request); err != nil {
				_ = encoder.Encode(mcp.Failure(nil, -32600, "invalid JSON-RPC request", nil))
				continue
			}
			s.activeRequests.Add(1)
			response := s.handleMCPRequest(request)
			encodeErr := encoder.Encode(response)
			s.activeRequests.Add(-1)
			if encodeErr != nil {
				return
			}
			continue
		}
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
	case localapi.MethodDatabaseStatus:
		return s.handleDatabaseStatus(request), false
	case localapi.MethodWorkspaceInit:
		return s.handleWorkspaceInit(request), false
	case localapi.MethodWorkspaceShow:
		return s.handleWorkspaceShow(request), false
	case localapi.MethodProjectAdd:
		return s.handleProjectAdd(request), false
	case localapi.MethodProjectInspect:
		return s.handleProjectInspect(request), false
	case localapi.MethodCheckoutAdd:
		return s.handleCheckoutAdd(request), false
	case localapi.MethodCheckoutList:
		return s.handleCheckoutList(request), false
	case localapi.MethodAgentCreate:
		return s.handleAgentCreate(request), false
	case localapi.MethodAgentUpdate:
		return s.handleAgentUpdate(request), false
	case localapi.MethodAgentShow:
		return s.handleAgentShow(request), false
	case localapi.MethodAgentList:
		return s.handleAgentList(request), false
	case localapi.MethodObjectiveCreate:
		return s.handleObjectiveCreate(request), false
	case localapi.MethodObjectiveUpdate:
		return s.handleObjectiveUpdate(request), false
	case localapi.MethodObjectiveShow:
		return s.handleObjectiveShow(request), false
	case localapi.MethodObjectiveList:
		return s.handleObjectiveList(request), false
	case localapi.MethodTaskCreate:
		return s.handleTaskCreate(request), false
	case localapi.MethodTaskUpdate:
		return s.handleTaskUpdate(request), false
	case localapi.MethodTaskShow:
		return s.handleTaskShow(request), false
	case localapi.MethodTaskList:
		return s.handleTaskList(request), false
	case localapi.MethodTaskDepend:
		return s.handleTaskDepend(request), false
	case localapi.MethodTaskAssign:
		return s.handleTaskAssign(request), false
	case localapi.MethodTaskTransition:
		return s.handleTaskTransition(request), false
	case localapi.MethodTaskTimeline:
		return s.handleTaskTimeline(request), false
	case localapi.MethodContextBuild:
		return s.handleContextBuild(request), false
	case localapi.MethodContextShow:
		return s.handleContextShow(request), false
	case localapi.MethodContextExplain:
		return s.handleContextExplain(request), false
	case localapi.MethodMessageSend:
		return s.handleMessageSend(request), false
	case localapi.MethodInboxList:
		return s.handleInboxList(request), false
	case localapi.MethodThreadShow:
		return s.handleThreadShow(request), false
	case localapi.MethodRunStart:
		return s.handleRunStart(request), false
	case localapi.MethodRunShow:
		return s.handleRunShow(request), false
	case localapi.MethodRunList:
		return s.handleRunList(request), false
	case localapi.MethodRunResume:
		return s.handleRunResume(request), false
	case localapi.MethodRunStop:
		return s.handleRunStop(request), false
	case localapi.MethodRunLogs:
		return s.handleRunLogs(request), false
	case localapi.MethodRunPrompt:
		return s.handleRunPrompt(request), false
	case localapi.MethodRunInterrupt:
		return s.handleRunInterrupt(request), false
	case localapi.MethodRunAttach:
		return s.handleRunAttach(request), false
	case localapi.MethodCoordinationStatus:
		return s.handleCoordinationStatus(request), false
	case localapi.MethodEventsList:
		return s.handleEventsList(request), false
	default:
		return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
			Code:      "method_not_found",
			Message:   fmt.Sprintf("unknown local API method %q", request.Method),
			Retryable: false,
		}), false
	}
}

func (s *server) handleDatabaseStatus(request localapi.Request) localapi.Response {
	health, err := s.store.Health(context.Background())
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DatabaseStatusResult{
		Schema:              localapi.DatabaseStatusSchema,
		Type:                "database_status",
		Status:              health.Status,
		SchemaVersion:       health.SchemaVersion,
		LatestSchemaVersion: health.LatestSchemaVersion,
		JournalMode:         health.JournalMode,
		ForeignKeys:         health.ForeignKeys,
		IntegrityCheck:      health.IntegrityCheck,
	})
}

func (s *server) handleWorkspaceInit(request localapi.Request) localapi.Response {
	var params localapi.WorkspaceInitParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "workspace.init requires name and idempotency_key")
	}
	result, err := s.store.InitWorkspace(context.Background(), store.InitWorkspaceCommand{
		Name:           params.Name,
		IdempotencyKey: params.IdempotencyKey,
		CorrelationID:  request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.WorkspaceInitResult{
		Schema:        localapi.WorkspaceInitSchema,
		Type:          "workspace_initialized",
		Workspace:     result.Workspace,
		EventID:       result.EventID,
		EventSequence: result.EventSequence,
	})
}

func (s *server) handleWorkspaceShow(request localapi.Request) localapi.Response {
	var params localapi.WorkspaceShowParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "workspace.show requires an identifier")
	}
	workspace, err := s.store.Workspace(context.Background(), params.Identifier)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.WorkspaceShowResult{
		Schema:    localapi.WorkspaceShowSchema,
		Type:      "workspace",
		Workspace: workspace,
	})
}

func (s *server) handleProjectAdd(request localapi.Request) localapi.Response {
	var params localapi.ProjectAddParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "project.add requires workspace, name, repository_path, and idempotency_key")
	}
	observation, err := s.gitInspector.Inspect(context.Background(), params.RepositoryPath)
	if err != nil {
		return gitErrorResponse(request, err)
	}
	result, err := s.store.RegisterProject(context.Background(), store.RegisterProjectCommand{
		WorkspaceIdentifier: params.Workspace,
		Name:                params.Name,
		WriteMode:           params.WriteMode,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
		Observation:         observation,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ProjectAddResult{
		Schema: localapi.ProjectAddSchema, Type: "project_registered", Project: result.Project,
		Repository: result.Repository, Checkout: result.Checkout, EventSequence: result.EventSequence,
	})
}

func (s *server) handleCheckoutAdd(request localapi.Request) localapi.Response {
	var params localapi.CheckoutAddParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "checkout.add requires workspace, project, repository_path, and idempotency_key")
	}
	observation, err := s.gitInspector.Inspect(context.Background(), params.RepositoryPath)
	if err != nil {
		return gitErrorResponse(request, err)
	}
	result, err := s.store.AddCheckout(context.Background(), store.AddCheckoutCommand{
		WorkspaceIdentifier: params.Workspace,
		ProjectIdentifier:   params.Project,
		WriteMode:           params.WriteMode,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
		Observation:         observation,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckoutAddResult{
		Schema: localapi.CheckoutAddSchema, Type: "checkout_registered", Repository: result.Repository,
		Checkout: result.Checkout, RepositoryCreated: result.RepositoryCreated, EventSequence: result.EventSequence,
	})
}

func (s *server) handleCheckoutList(request localapi.Request) localapi.Response {
	var params localapi.CheckoutListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "checkout.list requires workspace and project")
	}
	inspection, err := s.store.InspectProject(context.Background(), params.Workspace, params.Project)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.CheckoutListResult{
		Schema: localapi.CheckoutListSchema, Type: "checkout_list", Project: inspection.Project, Checkouts: inspection.Checkouts,
	})
}

func (s *server) handleProjectInspect(request localapi.Request) localapi.Response {
	var params localapi.ProjectInspectParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "project.inspect requires workspace and project")
	}
	current, err := s.store.InspectProject(context.Background(), params.Workspace, params.Project)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	observations := make(map[string]domain.CheckoutObservation, len(current.Checkouts))
	for _, checkout := range current.Checkouts {
		observation, inspectErr := s.gitInspector.Inspect(context.Background(), checkout.Path)
		if inspectErr != nil {
			observation = domain.CheckoutObservation{
				Path: checkout.Path, Availability: domain.CheckoutUnavailable, CheckoutKind: domain.CheckoutKindUnknown,
				DiagnosticCode: gitstate.ErrorCode(inspectErr), Diagnostic: inspectErr.Error(),
			}
		}
		observations[checkout.ID] = observation
	}
	inspection, err := s.store.ApplyCheckoutObservations(context.Background(), params.Workspace, params.Project, request.ID, observations)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ProjectInspectResult{
		Schema: localapi.ProjectInspectSchema, Type: "project_inspection", Project: inspection.Project,
		Repositories: inspection.Repositories, Checkouts: inspection.Checkouts,
	})
}

func (s *server) handleEventsList(request localapi.Request) localapi.Response {
	var params localapi.EventsListParams
	if err := decodeParams(request.Params, &params); err != nil || params.After == nil || *params.After < 0 || (params.Limit != nil && (*params.Limit < 1 || *params.Limit > 1000)) {
		return invalidParamsResponse(request, "events.list requires after >= 0 and limit between 1 and 1000 when supplied")
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	events, err := s.store.Events(context.Background(), *params.After, limit+1)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	nextAfter := *params.After
	if len(events) > 0 {
		nextAfter = events[len(events)-1].Sequence
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.EventsListResult{
		Schema:    localapi.EventsListSchema,
		Type:      "event_list",
		After:     *params.After,
		NextAfter: nextAfter,
		HasMore:   hasMore,
		Events:    events,
	})
}

func decodeParams(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return errors.New("params are required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("params contain more than one value")
		}
		return err
	}
	return nil
}

func invalidParamsResponse(request localapi.Request, message string) localapi.Response {
	return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
		Code:      "invalid_request",
		Message:   message,
		Retryable: false,
	})
}

func storeErrorResponse(request localapi.Request, err error) localapi.Response {
	code := store.ErrorCode(err)
	return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
		Code:      code,
		Message:   err.Error(),
		Retryable: code == store.CodeStorageFailed,
	})
}

func gitErrorResponse(request localapi.Request, err error) localapi.Response {
	code := gitstate.ErrorCode(err)
	return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
		Code: code, Message: err.Error(), Retryable: code == gitstate.CodeGitUnavailable || code == gitstate.CodeGitCommandFailed,
	})
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
