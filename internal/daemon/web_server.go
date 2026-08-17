package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
	webui "crewfold/web"
)

const (
	workbenchBootstrapTTL = time.Minute
	workbenchSessionTTL   = 8 * time.Hour
	workbenchCookieName   = "crewfold_owner"
	maxWebRequestBytes    = 4 * 1024
	maxWorkbenchRPCBytes  = 64 * 1024
	maxWorkbenchGrants    = 32
	maxWorkbenchSessions  = 32
	maxWorkbenchStreams   = 16
	maxWorkbenchTerminals = 4
)

const (
	workbenchSessionSchema = "urn:crewfold:schema:web:workbench-session:v1"
	workbenchStatusSchema  = "urn:crewfold:schema:web:workbench-status:v1"
)

type workbenchSession struct {
	expiresAt time.Time
	csrfHash  [32]byte
	routeHash [32]byte
}

type workbenchTerminalGrant struct {
	expiresAt time.Time
	routeHash [32]byte
	workspace string
	run       string
}

type workbenchServer struct {
	daemon         *server
	listener       net.Listener
	http           *http.Server
	origin         string
	host           string
	now            func() time.Time
	mu             sync.Mutex
	bootstrap      map[[32]byte]time.Time
	sessions       map[[32]byte]workbenchSession
	streams        chan struct{}
	terminals      chan struct{}
	terminalGrants map[[32]byte]workbenchTerminalGrant
}

func newWorkbenchServer(address string, daemon *server) (*workbenchServer, error) {
	if strings.TrimSpace(address) == "" {
		address = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "web address must be an IP host and port", Cause: err}
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || ip.To4() == nil || host != "127.0.0.1" {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "web address must use exact IPv4 loopback 127.0.0.1"}
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, &StartupError{Code: CodeSocketUnavailable, Message: "listen on owner-local workbench address", Cause: err}
	}
	exactHost := listener.Addr().String()
	instance := &workbenchServer{
		daemon: daemon, listener: listener, origin: "http://" + exactHost, host: exactHost,
		now: time.Now, bootstrap: make(map[[32]byte]time.Time), sessions: make(map[[32]byte]workbenchSession),
		streams: make(chan struct{}, maxWorkbenchStreams), terminals: make(chan struct{}, maxWorkbenchTerminals),
		terminalGrants: make(map[[32]byte]workbenchTerminalGrant),
	}
	mux, err := instance.routes()
	if err != nil {
		_ = listener.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "load embedded workbench assets", Cause: err}
	}
	instance.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return instance, nil
}

func (w *workbenchServer) routes() (http.Handler, error) {
	dist, err := fs.Sub(webui.Assets, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(dist))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		w.secure(response)
		if !w.exactHost(request, response) {
			return
		}
		if request.URL.Path != "/" || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusOK)
		if request.Method == http.MethodGet {
			_, _ = response.Write(index)
		}
	})
	mux.HandleFunc("/assets/", func(response http.ResponseWriter, request *http.Request) {
		w.secure(response)
		if !w.exactHost(request, response) {
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "workbench assets are read-only")
			return
		}
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(response, request)
	})
	mux.HandleFunc("/api/v1/session", w.handleSession)
	mux.HandleFunc("/api/v1/session/", w.handleSessionAPI)
	return mux, nil
}

func (w *workbenchServer) serve() {
	err := w.http.Serve(w.listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		w.daemon.config.Logger.Error("workbench server failed", "component", "web", "error", err)
		w.daemon.requestStop("workbench server failed")
		_ = w.daemon.listener.Close()
	}
}

func (w *workbenchServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = w.http.Shutdown(ctx)
	_ = w.listener.Close()
}

func (w *workbenchServer) mintBootstrap() (localapi.WebBootstrapResult, error) {
	token, digest, err := randomSecret()
	if err != nil {
		return localapi.WebBootstrapResult{}, fmt.Errorf("generate owner web bootstrap: %w", err)
	}
	expiresAt := w.now().UTC().Add(workbenchBootstrapTTL)
	w.mu.Lock()
	w.removeExpiredLocked(w.now())
	if len(w.bootstrap) >= maxWorkbenchGrants {
		w.mu.Unlock()
		return localapi.WebBootstrapResult{}, errors.New("too many unconsumed owner web bootstrap grants")
	}
	w.bootstrap[digest] = expiresAt
	w.mu.Unlock()
	return localapi.WebBootstrapResult{
		Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
		URL: w.origin + "/#bootstrap=" + token, ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}, nil
}

func (w *workbenchServer) handleSession(response http.ResponseWriter, request *http.Request) {
	w.secure(response)
	if !w.exactHost(request, response) {
		return
	}
	if request.Method != http.MethodPost {
		w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "session exchange requires POST")
		return
	}
	if request.Header.Get("Origin") != w.origin {
		w.writeError(response, http.StatusForbidden, "origin_mismatch", "request origin does not match the owner-local workbench")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.writeError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "session exchange requires application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebRequestBytes))
	if err != nil {
		w.writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "session request exceeds the bounded body limit")
		return
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "session request is not exact JSON")
		return
	}
	var params struct {
		Bootstrap string `json:"bootstrap"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "session request does not match the current contract")
		return
	}
	bootstrapBytes, err := hex.DecodeString(params.Bootstrap)
	if err != nil || len(bootstrapBytes) != 32 || len(params.Bootstrap) != 64 {
		w.writeError(response, http.StatusUnauthorized, "invalid_bootstrap", "bootstrap grant is invalid or expired")
		return
	}
	digest := sha256.Sum256(bootstrapBytes)
	now := w.now().UTC()
	w.mu.Lock()
	expiresAt, exists := w.bootstrap[digest]
	delete(w.bootstrap, digest)
	w.removeExpiredLocked(now)
	w.mu.Unlock()
	if !exists || !now.Before(expiresAt) {
		w.writeError(response, http.StatusUnauthorized, "invalid_bootstrap", "bootstrap grant is invalid or expired")
		return
	}
	sessionToken, sessionDigest, err := randomSecret()
	if err != nil {
		w.writeError(response, http.StatusInternalServerError, "session_unavailable", "could not create owner session")
		return
	}
	csrfToken, csrfDigest, err := randomSecret()
	if err != nil {
		w.writeError(response, http.StatusInternalServerError, "session_unavailable", "could not create owner session")
		return
	}
	routeToken, routeDigest, err := randomSecret()
	if err != nil {
		w.writeError(response, http.StatusInternalServerError, "session_unavailable", "could not create owner session")
		return
	}
	apiBase := "/api/v1/session/" + routeToken
	sessionExpiry := now.Add(workbenchSessionTTL)
	w.mu.Lock()
	w.removeExpiredLocked(now)
	if len(w.sessions) >= maxWorkbenchSessions {
		w.evictOldestSessionLocked()
	}
	w.sessions[sessionDigest] = workbenchSession{expiresAt: sessionExpiry, csrfHash: csrfDigest, routeHash: routeDigest}
	w.mu.Unlock()
	http.SetCookie(response, &http.Cookie{
		Name: workbenchCookieName, Value: sessionToken, Path: apiBase + "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(workbenchSessionTTL.Seconds()), Expires: sessionExpiry,
	})
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": workbenchSessionSchema, "type": "workbench_session", "status": "authenticated",
		"api_base": apiBase, "csrf_token": csrfToken, "expires_at": sessionExpiry.Format(time.RFC3339Nano),
	})
}

func (w *workbenchServer) handleSessionAPI(response http.ResponseWriter, request *http.Request) {
	w.secure(response)
	if !w.exactHost(request, response) {
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/api/v1/session/")
	route, operation, exact := strings.Cut(remainder, "/")
	if !exact || len(route) != 64 || strings.Contains(operation, "/") {
		http.NotFound(response, request)
		return
	}
	session, authenticated := w.authenticate(request, route)
	if !authenticated {
		w.writeError(response, http.StatusUnauthorized, "unauthorized", "owner session is missing or expired")
		return
	}
	switch operation {
	case "status":
		if request.Method != http.MethodGet {
			w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "workbench status requires GET")
			return
		}
		w.writeJSON(response, http.StatusOK, map[string]any{
			"schema": workbenchStatusSchema, "type": "workbench_status", "status": "ok",
			"protocol": localapi.MaxProtocol, "pid": os.Getpid(),
			"started_at":     w.daemon.startedAt.Format(time.RFC3339Nano),
			"uptime_ms":      time.Since(w.daemon.startedAt).Milliseconds(),
			"server_version": w.daemon.config.Version,
		})
	case "rpc":
		w.handleRPC(response, request, session)
	case "events":
		w.handleEventStream(response, request)
	case "conversation":
		w.handleOwnerConversation(response, request, session)
	case "intent":
		w.handleOwnerIntent(response, request, session)
	case "onboarding":
		w.handleWorkbenchOnboarding(response, request, session)
	case "git":
		w.handleWorkbenchGitObservation(response, request)
	case "terminal-grant":
		w.handleWorkbenchTerminalGrant(response, request, session)
	case "terminal":
		w.handleWorkbenchTerminal(response, request, session)
	case "retry-run":
		w.handleWorkbenchRunRetry(response, request, session)
	default:
		http.NotFound(response, request)
	}
}

func (w *workbenchServer) authenticate(request *http.Request, route string) (workbenchSession, bool) {
	cookie, err := request.Cookie(workbenchCookieName)
	if err != nil || len(cookie.Value) != 64 {
		return workbenchSession{}, false
	}
	token, err := hex.DecodeString(cookie.Value)
	if err != nil || len(token) != 32 {
		return workbenchSession{}, false
	}
	digest := sha256.Sum256(token)
	routeBytes, err := hex.DecodeString(route)
	if err != nil || len(routeBytes) != 32 {
		return workbenchSession{}, false
	}
	routeDigest := sha256.Sum256(routeBytes)
	now := w.now().UTC()
	w.mu.Lock()
	defer w.mu.Unlock()
	session, exists := w.sessions[digest]
	if !exists || !now.Before(session.expiresAt) || session.routeHash != routeDigest {
		delete(w.sessions, digest)
		return workbenchSession{}, false
	}
	return session, true
}

func (w *workbenchServer) handleRPC(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "workbench RPC") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWorkbenchRPCBytes))
	if err != nil {
		w.writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "workbench RPC exceeds the bounded body limit")
		return
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "workbench RPC is not exact JSON")
		return
	}
	var rpc struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rpc); err != nil || decodeHasTrailingValue(decoder) || len(rpc.Params) == 0 {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "workbench RPC does not match the current contract")
		return
	}
	if !workbenchMethodAllowed(rpc.Method) {
		w.writeError(response, http.StatusForbidden, "method_not_allowed", "method is not exposed to the owner workbench")
		return
	}
	result, stop := w.daemon.handleRequest(localapi.Request{ID: rpc.ID, Protocol: localapi.MaxProtocol, Method: rpc.Method, Params: rpc.Params})
	if stop {
		w.writeError(response, http.StatusForbidden, "method_not_allowed", "the owner workbench cannot stop the daemon")
		return
	}
	w.writeJSON(response, http.StatusOK, result)
}

func (w *workbenchServer) authorizeMutation(response http.ResponseWriter, request *http.Request, session workbenchSession, label string) bool {
	if request.Method != http.MethodPost {
		w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", label+" requires POST")
		return false
	}
	if request.Header.Get("Origin") != w.origin {
		w.writeError(response, http.StatusForbidden, "origin_mismatch", "request origin does not match the owner-local workbench")
		return false
	}
	csrf, err := hex.DecodeString(request.Header.Get("X-Crewfold-CSRF"))
	csrfDigest := sha256Digest(csrf)
	if err != nil || len(csrf) != 32 || subtle.ConstantTimeCompare(session.csrfHash[:], csrfDigest[:]) != 1 {
		w.writeError(response, http.StatusForbidden, "csrf_mismatch", "request does not carry the owner session CSRF grant")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.writeError(response, http.StatusUnsupportedMediaType, "invalid_content_type", label+" requires application/json")
		return false
	}
	return true
}

func (w *workbenchServer) handleEventStream(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "workbench events require GET")
		return
	}
	workspace := strings.TrimSpace(request.URL.Query().Get("workspace"))
	if workspace == "" || len(workspace) > 128 {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "workspace is required for event invalidation")
		return
	}
	select {
	case w.streams <- struct{}{}:
		defer func() { <-w.streams }()
	default:
		w.writeError(response, http.StatusServiceUnavailable, "stream_capacity_exhausted", "too many owner event streams are active")
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		w.writeError(response, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	_ = http.NewResponseController(response).SetWriteDeadline(time.Time{})
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, ": crewfold owner event stream\n\n")
	flusher.Flush()

	last := int64(0)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-w.daemon.stopCh:
			return
		case <-keepalive.C:
			_, _ = io.WriteString(response, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			page, err := w.daemon.store.ListEvents(request.Context(), store.ListEventsQuery{WorkspaceIdentifier: workspace, After: last, Limit: 1})
			if err != nil {
				payload, _ := json.Marshal(map[string]any{"schema": "urn:crewfold:schema:web:invalidation:v1", "type": "invalidated", "workspace": workspace, "reason": "read_failed"})
				_, _ = fmt.Fprintf(response, "event: invalidated\ndata: %s\n\n", payload)
				flusher.Flush()
				return
			}
			if page.HighWater <= last {
				continue
			}
			last = page.HighWater
			if len(page.Events) == 0 && page.Total == 0 {
				continue
			}
			payload, _ := json.Marshal(map[string]any{"schema": "urn:crewfold:schema:web:invalidation:v1", "type": "invalidated", "workspace_id": page.WorkspaceID, "high_water": page.HighWater})
			_, _ = fmt.Fprintf(response, "event: invalidated\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func sha256Digest(value []byte) [32]byte { return sha256.Sum256(value) }

func workbenchMethodAllowed(method string) bool {
	_, allowed := workbenchMethods[method]
	return allowed
}

var workbenchMethods = map[string]struct{}{
	localapi.MethodWorkspaceInit: {}, localapi.MethodWorkspaceShow: {}, localapi.MethodWorkspaceList: {},
	localapi.MethodProjectAdd: {}, localapi.MethodProjectShow: {}, localapi.MethodProjectInspect: {}, localapi.MethodProjectList: {},
	localapi.MethodCheckoutAdd: {}, localapi.MethodCheckoutList: {},
	localapi.MethodAgentCreate: {}, localapi.MethodAgentUpdate: {}, localapi.MethodAgentShow: {}, localapi.MethodAgentList: {},
	localapi.MethodOwnerCrewConfigure: {},
	localapi.MethodObjectiveCreate:    {}, localapi.MethodObjectiveUpdate: {}, localapi.MethodObjectiveShow: {}, localapi.MethodObjectiveList: {},
	localapi.MethodTaskCreate: {}, localapi.MethodTaskUpdate: {}, localapi.MethodTaskShow: {}, localapi.MethodTaskList: {},
	localapi.MethodTaskDepend: {}, localapi.MethodTaskAssign: {}, localapi.MethodTaskTransition: {}, localapi.MethodTaskTimeline: {},
	localapi.MethodRunStart: {}, localapi.MethodRunShow: {}, localapi.MethodRunList: {}, localapi.MethodRunResume: {},
	localapi.MethodRunStop: {}, localapi.MethodRunLostResolve: {}, localapi.MethodRunLogs: {}, localapi.MethodRunPrompt: {}, localapi.MethodRunInterrupt: {},
	localapi.MethodMessageSend: {}, localapi.MethodInboxList: {},
	localapi.MethodCoordinationStatus: {}, localapi.MethodClaimList: {}, localapi.MethodOverlapList: {}, localapi.MethodDriftList: {},
	localapi.MethodEventsList: {}, localapi.MethodEventsTimeline: {},
	localapi.MethodApprovalList: {}, localapi.MethodApprovalInspect: {}, localapi.MethodApprovalAllow: {}, localapi.MethodApprovalDeny: {},
	localapi.MethodSupervisorPolicyShow: {}, localapi.MethodSupervisorPolicyConfigure: {},
	localapi.MethodSupervisorActionList: {}, localapi.MethodSupervisorActionShow: {},
	localapi.MethodLaunchProfileList: {}, localapi.MethodProposalList: {}, localapi.MethodProposalInspect: {},
	localapi.MethodProposalAccept: {}, localapi.MethodProposalReject: {},
	localapi.MethodBriefingShow: {}, localapi.MethodBriefingExplain: {},
	localapi.MethodCheckList: {}, localapi.MethodCheckInspect: {}, localapi.MethodCheckLogs: {},
	localapi.MethodSystemDoctorFull: {},
}

func (w *workbenchServer) removeExpiredLocked(now time.Time) {
	for digest, expiry := range w.bootstrap {
		if !now.Before(expiry) {
			delete(w.bootstrap, digest)
		}
	}
	for digest, session := range w.sessions {
		if !now.Before(session.expiresAt) {
			delete(w.sessions, digest)
		}
	}
	for digest, grant := range w.terminalGrants {
		if !now.Before(grant.expiresAt) {
			delete(w.terminalGrants, digest)
		}
	}
}

func (w *workbenchServer) evictOldestSessionLocked() {
	var oldestDigest [32]byte
	var oldestExpiry time.Time
	first := true
	for digest, session := range w.sessions {
		if first || session.expiresAt.Before(oldestExpiry) {
			oldestDigest = digest
			oldestExpiry = session.expiresAt
			first = false
		}
	}
	if !first {
		delete(w.sessions, oldestDigest)
	}
}

func (w *workbenchServer) exactHost(request *http.Request, response http.ResponseWriter) bool {
	if request.Host != w.host {
		w.writeError(response, http.StatusMisdirectedRequest, "host_mismatch", "request host does not match the owner-local workbench")
		return false
	}
	return true
}

func (w *workbenchServer) secure(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	response.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	response.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
}

func (w *workbenchServer) writeError(response http.ResponseWriter, status int, code, message string) {
	w.writeJSON(response, status, map[string]any{
		"schema": "urn:crewfold:schema:web:error:v1", "type": "error", "error": map[string]any{"code": code, "message": message},
	})
}

func (w *workbenchServer) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func randomSecret() (string, [32]byte, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", [32]byte{}, err
	}
	return hex.EncodeToString(buffer), sha256.Sum256(buffer), nil
}

func decodeHasTrailingValue(decoder *json.Decoder) bool {
	var trailing any
	return !errors.Is(decoder.Decode(&trailing), io.EOF)
}

func (s *server) handleWebBootstrap(request localapi.Request) localapi.Response {
	if s.web == nil {
		return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{Code: "web_unavailable", Message: "owner-local workbench is disabled", Retryable: false})
	}
	var params localapi.WebBootstrapParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, err.Error())
	}
	result, err := s.web.mintBootstrap()
	if err != nil {
		return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{Code: "web_unavailable", Message: "could not create owner-local workbench grant", Retryable: true})
	}
	return localapi.MarshalResult(request.ID, request.Protocol, result)
}
