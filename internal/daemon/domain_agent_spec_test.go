package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestM22EphemeralAgentSpecDraftIsReviewedAndCreatesNoDomainState(t *testing.T) {
	fixture := &agentSpecDraftFixture{t: t}
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()
	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)

	before, err := client.WorkspaceList(ctx, localapi.WorkspaceListParams{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DomainAgentSpecDraft(ctx, localapi.DomainAgentSpecDraftParams{
		RepositoryPath: filepath.Join(repositoryRoot, "world-engine"), DomainName: "world-engine",
		OwnerIntent: "Coordinate the domain, preserve shared knowledge, and delegate implementation to reviewed specialists.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Schema != localapi.DomainAgentSpecDraftSchema || result.Type != "domain_agent_spec_draft" ||
		result.Draft.Name != "domain-steward" || result.Draft.DelegationPolicy != domain.DomainAgentDelegationFirst ||
		!strings.Contains(result.Draft.OperatingCharter, "Delegate implementation") {
		t.Fatalf("draft = %#v", result)
	}
	after, err := client.WorkspaceList(ctx, localapi.WorkspaceListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 0 || after.Total != 0 || len(after.Workspaces) != 0 {
		t.Fatalf("ephemeral draft changed domain state: before=%#v after=%#v", before, after)
	}
	if !fixture.sawExactBoundary() {
		t.Fatal("ephemeral drafter did not use the exact read-only, effect-free boundary")
	}
}

func TestM22AgentSpecDraftParserRequiresOneExactClosedObject(t *testing.T) {
	valid := `{"name":"domain-steward","role":"domain coordinator","operating_charter":"Coordinate shared knowledge.\nDelegate implementation through reviewed grants.","delegation_policy":"delegation_first","rationale":"The domain needs a durable coordination point."}`
	for name, text := range map[string]string{
		"prose wrapper":        "Here is the draft: " + valid,
		"unknown field":        strings.TrimSuffix(valid, "}") + `,"authority":"all"}`,
		"unknown policy":       strings.Replace(valid, "delegation_first", "supreme_leader", 1),
		"control in charter":   strings.Replace(valid, "Coordinate shared knowledge.", "Coordinate\u0000shared knowledge.", 1),
		"multiple JSON values": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDomainAgentSpecDraft(specDraftTurn(text)); err == nil {
				t.Fatalf("accepted invalid draft %q", text)
			}
		})
	}
	draft, err := decodeDomainAgentSpecDraft(specDraftTurn(valid))
	if err != nil {
		t.Fatal(err)
	}
	if draft.Name != "domain-steward" || !strings.Contains(draft.OperatingCharter, "\n") {
		t.Fatalf("valid draft = %#v", draft)
	}
}

func specDraftTurn(text string) execution.CodexTurn {
	item, _ := json.Marshal(map[string]any{"id": "draft-message", "type": "agentMessage", "text": text})
	return execution.CodexTurn{ID: "draft-turn", Status: "completed", Items: []json.RawMessage{item}}
}

type agentSpecDraftFixture struct {
	t       *testing.T
	mu      sync.Mutex
	started map[string]any
	prompt  string
}

func (fixture *agentSpecDraftFixture) transport() (execution.CodexAppServerTransport, error) {
	clientSide, serverSide := net.Pipe()
	go fixture.serve(serverSide)
	return clientSide, nil
}

func (fixture *agentSpecDraftFixture) serve(connection net.Conn) {
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	encoder := json.NewEncoder(connection)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fixture.t.Errorf("decode drafter request: %v", err)
			return
		}
		method, _ := request["method"].(string)
		if method == "initialized" {
			continue
		}
		id := request["id"]
		var result any
		var notification any
		switch method {
		case "initialize":
			result = map[string]any{"codexHome": "/private/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture"}
		case "thread/start":
			params, _ := request["params"].(map[string]any)
			fixture.mu.Lock()
			fixture.started = params
			fixture.mu.Unlock()
			result = map[string]any{"thread": fixture.thread("idle", nil)}
		case "turn/start":
			params, _ := request["params"].(map[string]any)
			input, _ := params["input"].([]any)
			if len(input) == 1 {
				part, _ := input[0].(map[string]any)
				fixture.mu.Lock()
				fixture.prompt, _ = part["text"].(string)
				fixture.mu.Unlock()
			}
			result = map[string]any{"turn": map[string]any{"id": "draft-turn", "status": "inProgress", "items": []any{}}}
			notification = map[string]any{"method": "turn/completed", "params": map[string]any{
				"threadId": "ephemeral-draft-thread", "turn": map[string]any{
					"id": "draft-turn", "status": "completed", "items": []any{map[string]any{
						"id": "draft-message", "type": "agentMessage",
						"text": `{"name":"domain-steward","role":"domain coordinator","operating_charter":"Coordinate shared knowledge. Delegate implementation through reviewed grants and report cross-workstream conflicts to the owner.","delegation_policy":"delegation_first","rationale":"The owner asked for coordination rather than another hands-on implementer."}`,
					}},
				},
			}}
		default:
			fixture.t.Errorf("unexpected drafter method %q", method)
			return
		}
		if err := encoder.Encode(map[string]any{"id": id, "result": result}); err != nil {
			return
		}
		if notification != nil {
			if err := encoder.Encode(notification); err != nil {
				return
			}
		}
	}
}

func (fixture *agentSpecDraftFixture) thread(status string, turns []any) map[string]any {
	if turns == nil {
		turns = []any{}
	}
	return map[string]any{"id": "ephemeral-draft-thread", "cwd": "/work", "ephemeral": true, "modelProvider": "openai", "status": map[string]any{"type": status}, "turns": turns}
}

func (fixture *agentSpecDraftFixture) sawExactBoundary() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.started == nil || fixture.started["ephemeral"] != true || fixture.started["approvalPolicy"] != "never" || fixture.started["sandbox"] != "read-only" {
		return false
	}
	if tools, ok := fixture.started["dynamicTools"].([]any); ok && len(tools) != 0 {
		return false
	}
	base, _ := fixture.started["baseInstructions"].(string)
	return strings.Contains(base, "no authority") && strings.Contains(fixture.prompt, "world-engine") && strings.Contains(fixture.prompt, "delegate implementation")
}
