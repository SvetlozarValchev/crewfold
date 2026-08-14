package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"crewfold/internal/localapi"
	protocolschema "crewfold/protocol"
)

func TestM21WorkbenchBootstrapIsSingleUseAndStatusIsAuthenticated(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatalf("WebBootstrap() error = %v", err)
	}
	parsed, err := url.Parse(bootstrap.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	token := parsed.Fragment
	if !strings.HasPrefix(token, "bootstrap=") {
		t.Fatalf("fragment = %q", token)
	}
	token = strings.TrimPrefix(token, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	unauthenticated, err := client.Get(origin + "/api/v1/session/" + strings.Repeat("0", 64) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.StatusCode)
	}
	unauthenticatedRaw, err := io.ReadAll(unauthenticated.Body)
	_ = unauthenticated.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := protocolschema.ValidateJSON("web/v1/error.response.schema.json", unauthenticatedRaw); err != nil {
		t.Fatalf("unauthenticated error schema = %v; response = %s", err, unauthenticatedRaw)
	}

	sessionRaw := exchangeWorkbenchBootstrap(t, client, origin, token, origin)
	if err := protocolschema.ValidateJSON("web/v1/session.response.schema.json", sessionRaw); err != nil {
		t.Fatalf("session response schema error = %v; response = %s", err, sessionRaw)
	}
	var session struct {
		APIBase string `json:"api_base"`
	}
	if err := json.Unmarshal(sessionRaw, &session); err != nil || !strings.HasPrefix(session.APIBase, "/api/v1/session/") {
		t.Fatalf("session response = %s error = %v", sessionRaw, err)
	}
	originURL, _ := url.Parse(origin + "/")
	if cookies := jar.Cookies(originURL); len(cookies) != 0 {
		t.Fatalf("owner cookie leaked to loopback root path: %#v", cookies)
	}

	statusResponse, err := client.Get(origin + session.APIBase + "/status")
	if err != nil {
		t.Fatal(err)
	}
	statusRaw, err := io.ReadAll(statusResponse.Body)
	_ = statusResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d: %s", statusResponse.StatusCode, statusRaw)
	}
	if err := protocolschema.ValidateJSON("web/v1/status.response.schema.json", statusRaw); err != nil {
		t.Fatalf("status response schema error = %v; response = %s", err, statusRaw)
	}

	replayRequest, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	replayRequest.Header.Set("Origin", origin)
	replayRequest.Header.Set("Content-Type", "application/json")
	replayResponse, err := (&http.Client{}).Do(replayRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap replay status = %d, want 401", replayResponse.StatusCode)
	}
}

func TestM21WorkbenchRejectsOriginHostAndUnknownSessionFields(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)

	for name, mutate := range map[string]func(*http.Request){
		"wrong origin": func(request *http.Request) { request.Header.Set("Origin", "http://attacker.invalid") },
		"wrong host":   func(request *http.Request) { request.Host = "localhost:1" },
	} {
		t.Run(name, func(t *testing.T) {
			bootstrap, err := client.WebBootstrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(bootstrap.URL)
			token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
			parsed.Fragment = ""
			origin := strings.TrimSuffix(parsed.String(), "/")
			request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
			mutate(request)
			response, err := (&http.Client{}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusMisdirectedRequest {
				t.Fatalf("response status = %d", response.StatusCode)
			}
		})
	}

	bootstrap, err := client.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(`{"bootstrap":"`+token+`","extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, want 400", response.StatusCode)
	}
}

func TestM21WorkbenchShellIsEmbeddedAndSecurityHeadersAreExact(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	bootstrap, err := localapi.NewClient(running.config.SocketPath).WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	parsed.Fragment = ""
	response, err := http.Get(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Crewfold Workbench")) {
		t.Fatalf("root response = %d %q", response.StatusCode, body)
	}
	for name, expected := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
	} {
		if value := response.Header.Get(name); value != expected {
			t.Errorf("%s = %q, want %q", name, value, expected)
		}
	}
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "connect-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", policy)
	}
	if cors := response.Header.Get("Access-Control-Allow-Origin"); cors != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want absent", cors)
	}
}

func TestM21DisabledWorkbenchFailsClosedAtLocalAPI(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	config.DisableWeb = true
	startTestServer(t, config)
	_, err := localapi.NewClient(config.SocketPath).WebBootstrap(context.Background())
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != "web_unavailable" || apiError.Retryable {
		t.Fatalf("WebBootstrap() error = %#v", err)
	}
}

func TestM21WorkbenchRefusesNonExactLoopbackAddress(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"0.0.0.0:0", "localhost:0", "[::1]:0", "127.0.0.2:0"} {
		if server, err := newWorkbenchServer(address, &server{}); err == nil {
			server.close()
			t.Errorf("newWorkbenchServer(%q) error = nil, want refusal", address)
		}
	}
}

func TestM21WorkbenchSessionCapacityEvictsTheOldestGrant(t *testing.T) {
	t.Parallel()

	server := &workbenchServer{sessions: make(map[[32]byte]workbenchSession)}
	now := time.Now().UTC()
	var oldest [32]byte
	oldest[0] = 1
	server.sessions[oldest] = workbenchSession{expiresAt: now.Add(time.Minute)}
	for index := byte(2); len(server.sessions) < maxWorkbenchSessions; index++ {
		var digest [32]byte
		digest[0] = index
		server.sessions[digest] = workbenchSession{expiresAt: now.Add(time.Duration(index) * time.Minute)}
	}
	server.evictOldestSessionLocked()
	if _, exists := server.sessions[oldest]; exists {
		t.Fatal("oldest owner session was not evicted")
	}
	if len(server.sessions) != maxWorkbenchSessions-1 {
		t.Fatalf("sessions = %d, want %d", len(server.sessions), maxWorkbenchSessions-1)
	}
}

func exchangeWorkbenchBootstrap(t *testing.T, client *http.Client, origin, token, requestOrigin string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{"bootstrap": token})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", requestOrigin)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session response status = %d: %s", response.StatusCode, raw)
	}
	return raw
}
