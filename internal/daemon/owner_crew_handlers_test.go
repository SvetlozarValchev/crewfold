package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestM22OnboardingDoesNotCreateLegacyExecutiveAuthority(t *testing.T) {
	config := testConfig(t)
	config.GitInspector = m21WorkbenchInspector{}
	config.RuntimeDrivers = map[string]execution.RuntimeDriver{"fake": execution.NewFakeRuntime()}
	config.ProviderAdapters = map[string]execution.ProviderAdapter{"fake": execution.FakeProvider{}}
	running := startTestServer(t, config)
	api := localapi.NewClient(running.config.SocketPath)
	bootstrap, err := api.WebBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrap.URL)
	token := strings.TrimPrefix(parsed.Fragment, "bootstrap=")
	parsed.Fragment = ""
	origin := strings.TrimSuffix(parsed.String(), "/")
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}
	var session struct {
		APIBase string `json:"api_base"`
		CSRF    string `json:"csrf_token"`
	}
	if err := json.Unmarshal(exchangeWorkbenchBootstrap(t, httpClient, origin, token, origin), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"repository_path": filepath.Join(t.TempDir(), "crew-project"), "workspace": "personal", "project": "crew-project",
		"agent": "builder", "role": "arbitrary coordinator", "operating_charter": daemonTestDomainCharter, "delegation_policy": "adaptive", "provider": "fake", "runtime": "fake", "write_mode": "shared",
	})
	request, _ := http.NewRequest(http.MethodPost, origin+session.APIBase+"/onboarding", bytes.NewReader(body))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Crewfold-CSRF", session.CSRF)
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("onboarding = %d: %s", response.StatusCode, raw)
	}
	var onboarded struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Staffing  domain.DomainAgentStaffingGrant `json:"staffing_grant"`
		Objective struct {
			ID string `json:"id"`
		} `json:"executive_objective"`
		Binding struct {
			Revision int64 `json:"revision"`
		} `json:"executive_binding"`
	}
	if err := json.Unmarshal(raw, &onboarded); err != nil {
		t.Fatal(err)
	}
	if onboarded.Agent.ID == "" || onboarded.Staffing.ManagerAgentID != onboarded.Agent.ID || onboarded.Staffing.Status != domain.DomainStaffingGrantActive || onboarded.Objective.ID != "" || onboarded.Binding.Revision != 0 {
		t.Fatalf("M22 onboarding retained legacy executive state: %#v", onboarded)
	}
	tree, err := api.DomainAgentTree(context.Background(), onboarded.Workspace.ID, onboarded.Project.ID)
	if err != nil || len(tree.Agents) != 1 || tree.Agents[0].Definition.ID != onboarded.Agent.ID || !tree.Agents[0].Membership.PreferredEntry {
		t.Fatalf("M22 domain agent tree = %#v, %v", tree, err)
	}
}
