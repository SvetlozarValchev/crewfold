package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestM22ArbitraryDurableAgentOwnsOneStrictResumableCodexConversation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := newCodexDomainSessionFixture(t)
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()

	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "m22-session-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeExclusive, "m22-session-project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "orchid", Role: "owner-defined-whatever",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m22-session-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "fern", Role: "another arbitrary durable agent",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m22-session-recipient-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	unsupported, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "cedar", Role: "non-Codex durable agent",
		Provider: "claude", Runtime: "herdr", IdempotencyKey: "m22-session-unsupported-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-session-attach",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: recipient.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "m22-session-recipient-attach",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: unsupported.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "m22-session-unsupported-attach",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: unsupported.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "m22-open-unsupported",
	})
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != store.CodeInvalidDomainAgentSession {
		t.Fatalf("non-Codex durable session error = %#v", err)
	}
	grant, err := client.DomainStaffingGrantCreate(ctx, localapi.DomainStaffingGrantCreateParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, ManagerAgent: agent.Agent.Name,
		ExpectedMembershipRevision: attached.Membership.Revision,
		Profiles:                   []domain.DomainAgentStaffingProfile{{Provider: "codex-subscription", Runtime: "herdr", MaxConcurrency: 1}},
		TaskClasses:                []string{"reviewer"}, MaxDescendants: 1, MaxConcurrency: 1,
		Budget: domain.Budget{TokenLimit: 100, CostCents: 100, TimeSeconds: 300}, IdempotencyKey: "m22-session-staffing-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.setGrantID(grant.Grant.ID)
	fixture.setCheckoutID(project.Checkout.ID)

	unbound, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if unbound.View.Session.State != domain.DomainAgentSessionUnbound || len(unbound.View.Turns) != 0 {
		t.Fatalf("unbound session = %#v", unbound)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "m22-open-orchid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.View.Session.State != domain.DomainAgentSessionReady || opened.View.Session.Provider != "codex" || !opened.View.Session.HasConversation {
		t.Fatalf("opened session = %#v", opened)
	}
	if len(opened.View.Turns) != 0 {
		t.Fatalf("fresh unmaterialized thread exposed turns = %#v", opened.View.Turns)
	}

	sent, err := client.DomainAgentSessionSend(ctx, localapi.DomainAgentSessionSendParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Text: "continue the exact work", IdempotencyKey: "m22-turn-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent.AcceptedTurn == nil || sent.AcceptedTurn.ID != "turn-new" || sent.AcceptedTurn.Status != "inProgress" {
		t.Fatalf("accepted turn = %#v", sent.AcceptedTurn)
	}
	replayed, err := client.DomainAgentSessionSend(ctx, localapi.DomainAgentSessionSendParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Text: "continue the exact work", IdempotencyKey: "m22-turn-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.AcceptedTurn == nil || replayed.AcceptedTurn.ID != "turn-new" || fixture.turnStarts() != 1 {
		t.Fatalf("idempotent replay = %#v, turn starts = %d", replayed.AcceptedTurn, fixture.turnStarts())
	}
	interrupted, err := client.DomainAgentSessionInterrupt(ctx, localapi.DomainAgentSessionInterruptParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name, TurnID: "turn-new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.View.ThreadStatus != "idle" || fixture.interrupts() != 1 {
		t.Fatalf("interrupted session = %#v, interrupts = %d", interrupted, fixture.interrupts())
	}
	compacted, err := client.DomainAgentSessionCompact(ctx, localapi.DomainAgentSessionCompactParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		ExpectedEpoch: interrupted.View.Session.Epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if compacted.View.Session.Epoch != 1 || compacted.View.ThreadStatus != "idle" || fixture.compacts() != 1 || fixture.resumes() != 1 {
		t.Fatalf("compacted session = %#v, compacts = %d, resumes = %d", compacted.View, fixture.compacts(), fixture.resumes())
	}
	raw, err := json.Marshal(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"thread-private-019", "node_id", "node_fingerprint", "thread_id"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("public session leaked %q: %s", private, raw)
		}
	}
	if !fixture.sawArbitraryAgentInstructions() {
		fixture.mu.Lock()
		params := fixture.startedParams
		toolChecks := fixture.toolChecks
		fixture.mu.Unlock()
		t.Fatalf("thread/start did not bind the arbitrary orchid agent and its authority-neutral role: params=%#v tool_checks=%d", params, toolChecks)
	}
	inbox, err := client.InboxList(ctx, workspace.Workspace.Name, recipient.Agent.Name, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message.SenderType != "durable_agent" ||
		inbox.Items[0].Message.SenderAgentName != agent.Agent.Name || inbox.Items[0].Message.Body != "freeze the shared interface before changing it" {
		t.Fatalf("durable agent message inbox = %#v", inbox.Items)
	}
	tree, err := client.DomainAgentTree(ctx, workspace.Workspace.Name, project.Project.Name)
	if err != nil {
		t.Fatal(err)
	}
	var childCount int
	for _, candidate := range tree.Agents {
		if candidate.Definition.Name == "moss-reviewer" && candidate.Membership.ParentAgentID == agent.Agent.ID {
			childCount++
		}
	}
	if childCount != 1 {
		t.Fatalf("durable child tree = %#v", tree.Agents)
	}

	// A daemon restart creates a new app-server process. The durable binding
	// remains canonical, but Codex requires the exact persisted thread to be
	// resumed into that process before it can be read or controlled.
	running.cancel()
	if err := running.wait(); err != nil {
		t.Fatalf("stop first daemon: %v", err)
	}
	startTestServer(t, config)
	afterRestart, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatalf("DomainAgentSessionShow(after restart) = %v", err)
	}
	if afterRestart.View.Session.State != domain.DomainAgentSessionReady || len(afterRestart.View.Turns) == 0 {
		t.Fatalf("resumed session after restart = %#v", afterRestart.View)
	}
	if fixture.resumes() != 2 || fixture.unloadedReads() != 0 {
		t.Fatalf("app-server restart lifecycle: resumes = %d, reads before resume = %d", fixture.resumes(), fixture.unloadedReads())
	}
	if !fixture.sawCurrentReadOnlyResumeBoundary() {
		t.Fatalf("thread/resume did not reapply the current read-only Crewfold conversation boundary: %#v", fixture.resumeParameters())
	}
}

func TestM23WorkstreamDurableSessionUsesOnlyItsPrimaryCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := newCodexDomainSessionFixture(t)
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()

	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "m23-session-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeExclusive, "m23-session-project")
	if err != nil {
		t.Fatal(err)
	}
	adjacent, err := client.CheckoutAdd(ctx, workspace.Workspace.Name, project.Project.Name, filepath.Join(repositoryRoot, "world-engine-2"), domain.WriteModeExclusive, "m23-session-adjacent")
	if err != nil {
		t.Fatal(err)
	}
	objective, err := client.ObjectiveCreate(ctx, localapi.ObjectiveCreateParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, PrimaryCheckout: project.Checkout.ID,
		Title: "Bound workstream", Budget: domain.Budget{}, IdempotencyKey: "m23-session-objective",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "orchid", Role: "workstream lead",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m23-session-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Workstream: objective.Objective.ID, OperatingCharter: daemonTestDomainCharter,
		DelegationPolicy: domain.DomainAgentAdaptive, PreferredEntry: true, IdempotencyKey: "m23-session-attach",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: adjacent.Checkout.ID, IdempotencyKey: "m23-session-wrong-checkout",
	})
	var apiError *localapi.APIError
	if !errors.As(err, &apiError) || apiError.Code != store.CodeInvalidDomainAgentSession {
		t.Fatalf("DomainAgentSessionOpen(adjacent) error = %#v", err)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		IdempotencyKey: "m23-session-primary-checkout",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.View.Session.State != domain.DomainAgentSessionReady {
		t.Fatalf("opened session = %#v", opened.View)
	}
	fixture.mu.Lock()
	startedCWD := fmt.Sprint(fixture.startedParams["cwd"])
	fixture.mu.Unlock()
	if startedCWD != project.Checkout.Path {
		t.Fatalf("thread/start cwd = %q, want workstream primary checkout %q", startedCWD, project.Checkout.Path)
	}
}

func TestM22TerminalDurableTurnRetiresOnlyItsDisposableHostAndResumesTheSameEpoch(t *testing.T) {
	fixture := newCodexDomainSessionFixture(t)
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	host := newDomainSessionHost(config, nil)
	host.idleAfter = 5 * time.Millisecond
	t.Cleanup(func() { _ = host.Close() })
	ctx := context.Background()
	thread, err := host.startThread(ctx, "/work", "Crewfold: orchid", "You are orchid.")
	if err != nil {
		t.Fatal(err)
	}
	host.recordNotification(execution.CodexAppServerNotification{
		Method: "turn/completed",
		Params: json.RawMessage(`{"threadId":"thread-private-019","turn":{"id":"turn-finished","status":"completed","items":[]}}`),
	})
	deadline := time.Now().Add(time.Second)
	for {
		if _, loaded := host.clientForThread(thread.ID); !loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal durable turn did not retire its disposable app-server host")
		}
		time.Sleep(time.Millisecond)
	}
	resumed, err := host.readThread(ctx, thread.ID, thread.CWD, "You are orchid.")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != thread.ID || fixture.resumes() != 1 {
		t.Fatalf("resumed thread = %#v, resumes = %d", resumed, fixture.resumes())
	}
}

func TestM22UnavailableProviderRolloutRebindsTheSameDurableAgentAndThenResumes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := newMissingRolloutDomainSessionFixture(t)
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()
	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "m22-missing-rollout-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeExclusive, "m22-missing-rollout-project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "orchid", Role: "arbitrary durable coordinator",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m22-missing-rollout-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-missing-rollout-attach",
	}); err != nil {
		t.Fatal(err)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "m22-missing-rollout-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.View.Session.Revision != 1 || fixture.starts() != 1 || fixture.names() != 1 {
		t.Fatalf("opened session = %#v, starts = %d, names = %d", opened.View.Session, fixture.starts(), fixture.names())
	}
	fixture.removeFirstRollout()
	running.cancel()
	if err := running.wait(); err != nil {
		t.Fatalf("stop first daemon: %v", err)
	}
	second := startTestServer(t, config)
	replaced, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatalf("DomainAgentSessionShow(missing rollout) = %v", err)
	}
	if replaced.View.Session.Revision != 2 || replaced.View.ThreadStatus != "idle" || len(replaced.View.Turns) != 0 || fixture.starts() != 2 || fixture.names() != 2 {
		t.Fatalf("replacement = %#v, starts = %d, names = %d", replaced.View, fixture.starts(), fixture.names())
	}
	second.cancel()
	if err := second.wait(); err != nil {
		t.Fatalf("stop second daemon: %v", err)
	}
	startTestServer(t, config)
	resumed, err := client.DomainAgentSessionShow(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name)
	if err != nil {
		t.Fatalf("DomainAgentSessionShow(replacement restart) = %v", err)
	}
	if resumed.View.Session.Revision != 2 || fixture.starts() != 2 || !fixture.resumedSecondRollout() {
		t.Fatalf("resumed replacement = %#v, starts = %d, resume ids = %#v", resumed.View, fixture.starts(), fixture.resumeIDs())
	}
}

func TestM22OwnerRotationArchivesOneEpochAndStartsOneCanonicalHandoffSuccessor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixture := newMissingRolloutDomainSessionFixture(t)
	config := testConfig(t)
	config.CodexAppServerTransportFactory = fixture.transport
	startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()
	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	workspace, err := client.WorkspaceInit(ctx, "personal", "m22-rotate-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.Name, "garden", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeExclusive, "m22-rotate-project")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.AgentCreate(ctx, localapi.AgentCreateParams{
		Workspace: workspace.Workspace.Name, Name: "orchid", Role: "arbitrary durable coordinator",
		Provider: "codex-subscription", Runtime: "herdr", IdempotencyKey: "m22-rotate-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DomainAgentAttach(ctx, localapi.DomainAgentAttachParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		OperatingCharter: daemonTestDomainCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		PreferredEntry: true, IdempotencyKey: "m22-rotate-attach",
	}); err != nil {
		t.Fatal(err)
	}
	opened, err := client.DomainAgentSessionOpen(ctx, localapi.DomainAgentSessionOpenParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		Checkout: project.Checkout.ID, IdempotencyKey: "m22-rotate-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := client.DomainAgentSessionRotate(ctx, localapi.DomainAgentSessionRotateParams{
		Workspace: workspace.Workspace.Name, Project: project.Project.Name, Agent: agent.Agent.Name,
		ExpectedEpoch: opened.View.Session.Epoch, Reason: "milestone",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.View.Session.Epoch != 2 || rotated.View.Session.State != domain.DomainAgentSessionReady || rotated.View.ThreadStatus != "idle" {
		t.Fatalf("rotated current session = %#v", rotated.View)
	}
	if len(rotated.View.Epochs) != 2 || rotated.View.Epochs[0].Epoch != 2 || rotated.View.Epochs[0].Status != "current" ||
		rotated.View.Epochs[1].Epoch != 1 || rotated.View.Epochs[1].Status != "archived" || rotated.View.Epochs[1].RotationReason != "milestone" {
		t.Fatalf("public epoch lineage = %#v", rotated.View.Epochs)
	}
	if fixture.starts() != 2 || !fixture.successorHasCanonicalHandoff() {
		t.Fatalf("starts = %d, developer instructions = %#v", fixture.starts(), fixture.developerInstructions())
	}
	deadline := time.Now().Add(time.Second)
	for fixture.closed() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.closed() < 1 {
		t.Fatal("archived epoch retained its disposable app-server process")
	}
	history, err := client.DomainAgentSessionShowEpoch(ctx, workspace.Workspace.Name, project.Project.Name, agent.Agent.Name, 1)
	if err != nil {
		t.Fatal(err)
	}
	if history.View.Session.State != domain.DomainAgentSessionArchived || history.View.Session.Epoch != 1 || history.View.ThreadStatus != "idle" {
		t.Fatalf("archived epoch history = %#v", history.View)
	}
	raw, err := json.Marshal(rotated)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"missing-rollout-thread-1", "missing-rollout-thread-2", "thread_id", "node_fingerprint"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("rotated public session leaked %q: %s", private, raw)
		}
	}
}

type missingRolloutDomainSessionFixture struct {
	t                   *testing.T
	mu                  sync.Mutex
	startCount          int
	nameCount           int
	firstRolloutGone    bool
	resumedThreadIDs    []string
	threadCWDs          map[string]string
	startedInstructions []string
	closedConnections   int
}

func newMissingRolloutDomainSessionFixture(t *testing.T) *missingRolloutDomainSessionFixture {
	return &missingRolloutDomainSessionFixture{t: t, threadCWDs: make(map[string]string)}
}

func (fixture *missingRolloutDomainSessionFixture) transport() (execution.CodexAppServerTransport, error) {
	clientSide, serverSide := net.Pipe()
	go fixture.serve(serverSide)
	return clientSide, nil
}

func (fixture *missingRolloutDomainSessionFixture) serve(connection net.Conn) {
	defer func() {
		_ = connection.Close()
		fixture.mu.Lock()
		fixture.closedConnections++
		fixture.mu.Unlock()
	}()
	scanner := bufio.NewScanner(connection)
	encoder := json.NewEncoder(connection)
	loadedThreadID, loadedCWD := "", ""
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fixture.t.Errorf("decode missing-rollout fixture request: %v", err)
			return
		}
		method, _ := request["method"].(string)
		id, hasID := request["id"]
		if method == "initialized" {
			continue
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"codexHome": "/private/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture"}
		case "thread/start":
			params, _ := request["params"].(map[string]any)
			loadedCWD, _ = params["cwd"].(string)
			fixture.mu.Lock()
			fixture.startCount++
			fixture.startedInstructions = append(fixture.startedInstructions, fmt.Sprint(params["developerInstructions"]))
			loadedThreadID = fmt.Sprintf("missing-rollout-thread-%d", fixture.startCount)
			fixture.threadCWDs[loadedThreadID] = loadedCWD
			fixture.mu.Unlock()
			result = map[string]any{"thread": fixture.thread(loadedThreadID, loadedCWD)}
		case "thread/name/set":
			params, _ := request["params"].(map[string]any)
			if params["threadId"] != loadedThreadID || params["name"] != "Crewfold: orchid" {
				fixture.t.Errorf("missing-rollout thread/name/set params = %#v", params)
				return
			}
			fixture.mu.Lock()
			fixture.nameCount++
			fixture.mu.Unlock()
			result = map[string]any{}
		case "thread/resume":
			params, _ := request["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			fixture.mu.Lock()
			fixture.resumedThreadIDs = append(fixture.resumedThreadIDs, threadID)
			missing := fixture.firstRolloutGone && threadID == "missing-rollout-thread-1"
			fixture.mu.Unlock()
			if missing {
				_ = encoder.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "no rollout found for thread id " + threadID}})
				continue
			}
			loadedThreadID = threadID
			fixture.mu.Lock()
			loadedCWD = fixture.threadCWDs[threadID]
			fixture.mu.Unlock()
			result = map[string]any{"thread": fixture.thread(loadedThreadID, loadedCWD)}
		case "thread/read":
			if loadedThreadID == "" {
				fixture.t.Error("thread/read arrived before thread/resume")
				return
			}
			result = map[string]any{"thread": fixture.thread(loadedThreadID, loadedCWD)}
		case "thread/turns/list":
			result = map[string]any{"data": []any{}}
		case "thread/delete":
			result = map[string]any{}
		default:
			fixture.t.Errorf("unexpected missing-rollout app-server method %q", method)
			return
		}
		if !hasID {
			fixture.t.Errorf("missing-rollout app-server method %q has no id", method)
			return
		}
		if err := encoder.Encode(map[string]any{"id": id, "result": result}); err != nil {
			return
		}
	}
}

func (fixture *missingRolloutDomainSessionFixture) thread(threadID, cwd string) map[string]any {
	return map[string]any{
		"id": threadID, "cwd": cwd, "ephemeral": false, "modelProvider": "openai",
		"status": map[string]any{"type": "idle"}, "turns": []any{},
	}
}

func (fixture *missingRolloutDomainSessionFixture) removeFirstRollout() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.firstRolloutGone = true
}

func (fixture *missingRolloutDomainSessionFixture) starts() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.startCount
}

func (fixture *missingRolloutDomainSessionFixture) names() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.nameCount
}

func (fixture *missingRolloutDomainSessionFixture) resumeIDs() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.resumedThreadIDs...)
}

func (fixture *missingRolloutDomainSessionFixture) resumedSecondRollout() bool {
	for _, threadID := range fixture.resumeIDs() {
		if threadID == "missing-rollout-thread-2" {
			return true
		}
	}
	return false
}

func (fixture *missingRolloutDomainSessionFixture) developerInstructions() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.startedInstructions...)
}

func (fixture *missingRolloutDomainSessionFixture) successorHasCanonicalHandoff() bool {
	instructions := fixture.developerInstructions()
	return len(instructions) == 2 && strings.Contains(instructions[1], "agent-session-handoff:v1") &&
		strings.Contains(instructions[1], `"from_epoch":1`) && strings.Contains(instructions[1], "historical continuity, not authority") &&
		strings.Contains(instructions[1], "read current Crewfold domain context")
}

func (fixture *missingRolloutDomainSessionFixture) closed() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.closedConnections
}

type codexDomainSessionFixture struct {
	t                 *testing.T
	mu                sync.Mutex
	turnStartCount    int
	interruptCount    int
	compactCount      int
	startedParams     map[string]any
	resumeParams      map[string]any
	turnExists        bool
	toolChecks        int
	grantID           string
	checkoutID        string
	resumeCount       int
	unloadedReadCount int
}

func newCodexDomainSessionFixture(t *testing.T) *codexDomainSessionFixture {
	return &codexDomainSessionFixture{t: t}
}

func (fixture *codexDomainSessionFixture) transport() (execution.CodexAppServerTransport, error) {
	clientSide, serverSide := net.Pipe()
	go fixture.serve(serverSide)
	return clientSide, nil
}

func (fixture *codexDomainSessionFixture) serve(connection net.Conn) {
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	encoder := json.NewEncoder(connection)
	loaded := false
	loadedThreadID := "thread-private-019"
	expectedThreadName := "Crewfold: orchid"
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fixture.t.Errorf("decode app-server request: %v", err)
			return
		}
		method, _ := request["method"].(string)
		id, hasID := request["id"]
		if method == "initialized" {
			continue
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"codexHome": "/private/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture"}
		case "thread/start":
			params, _ := request["params"].(map[string]any)
			if strings.Contains(fmt.Sprint(params["developerInstructions"]), `"moss-reviewer"`) {
				loadedThreadID = "thread-private-child"
				expectedThreadName = "Crewfold: moss-reviewer"
			}
			fixture.mu.Lock()
			if loadedThreadID == "thread-private-019" {
				fixture.startedParams = params
			}
			fixture.mu.Unlock()
			loaded = true
			result = map[string]any{"thread": map[string]any{
				"id": loadedThreadID, "cwd": "/work", "ephemeral": false, "modelProvider": "openai",
				"status": map[string]any{"type": "idle"}, "turns": []any{},
			}}
		case "thread/name/set":
			if !loaded {
				fixture.t.Error("thread/name/set arrived before thread/start")
				return
			}
			params, _ := request["params"].(map[string]any)
			if params["threadId"] != loadedThreadID || params["name"] != expectedThreadName {
				fixture.t.Errorf("thread/name/set params = %#v", params)
				return
			}
			result = map[string]any{}
		case "thread/resume":
			fixture.mu.Lock()
			fixture.resumeCount++
			fixture.resumeParams, _ = request["params"].(map[string]any)
			turnExists := fixture.turnExists
			fixture.mu.Unlock()
			loaded = true
			status := "idle"
			if turnExists {
				status = "active"
			}
			result = map[string]any{"thread": fixture.thread(status)}
		case "thread/read":
			if !loaded {
				fixture.mu.Lock()
				fixture.unloadedReadCount++
				fixture.mu.Unlock()
				if err := encoder.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "thread not loaded: thread-private-019"}}); err != nil {
					return
				}
				continue
			}
			fixture.mu.Lock()
			turnExists := fixture.turnExists
			fixture.mu.Unlock()
			status := "idle"
			if turnExists {
				status = "active"
			}
			result = map[string]any{"thread": fixture.thread(status)}
		case "thread/turns/list":
			fixture.mu.Lock()
			turnExists := fixture.turnExists
			fixture.mu.Unlock()
			status := "idle"
			if turnExists {
				status = "active"
			}
			thread := fixture.thread(status)
			result = map[string]any{"data": thread["turns"]}
		case "turn/start":
			params, _ := request["params"].(map[string]any)
			if params["clientUserMessageId"] != "crewfold:m22-turn-one" {
				fixture.t.Errorf("clientUserMessageId = %#v", params["clientUserMessageId"])
				return
			}
			fixture.requireDomainToolResponse(scanner, encoder, 7001, "not_advertised", "thread-private-019", false)
			fixture.requireDomainToolResponse(scanner, encoder, 7002, domainToolContext, "foreign-thread", false)
			fixture.requireDomainToolResponse(scanner, encoder, 7003, domainToolContext, "thread-private-019", true)
			fixture.requireDomainMessageToolResponse(scanner, encoder, 7004)
			fixture.requireDomainMessageToolResponse(scanner, encoder, 7005)
			fixture.requireDomainChildToolResponse(scanner, encoder, 7006)
			fixture.requireDomainChildToolResponse(scanner, encoder, 7007)
			fixture.requireDomainToolResponse(scanner, encoder, 7008, domainToolContext, "thread-private-019", true)
			fixture.mu.Lock()
			fixture.turnStartCount++
			fixture.turnExists = true
			fixture.mu.Unlock()
			result = map[string]any{"turn": fixture.newTurn("inProgress")}
		case "turn/interrupt":
			fixture.mu.Lock()
			fixture.interruptCount++
			fixture.turnExists = false
			fixture.mu.Unlock()
			result = map[string]any{}
		case "thread/compact/start":
			fixture.mu.Lock()
			fixture.compactCount++
			fixture.mu.Unlock()
			if err := encoder.Encode(map[string]any{"method": "thread/compacted", "params": map[string]any{"threadId": loadedThreadID, "turnId": "turn-compact"}}); err != nil {
				return
			}
			result = map[string]any{}
		case "thread/delete":
			result = map[string]any{}
		default:
			fixture.t.Errorf("unexpected app-server method %q", method)
			return
		}
		if !hasID {
			fixture.t.Errorf("app-server method %q has no id", method)
			return
		}
		if err := encoder.Encode(map[string]any{"id": id, "result": result}); err != nil {
			return
		}
	}
}

func (fixture *codexDomainSessionFixture) setGrantID(value string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.grantID = value
}

func (fixture *codexDomainSessionFixture) setCheckoutID(value string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.checkoutID = value
}

func (fixture *codexDomainSessionFixture) requireDomainChildToolResponse(scanner *bufio.Scanner, encoder *json.Encoder, id int64) {
	fixture.t.Helper()
	fixture.mu.Lock()
	grantID := fixture.grantID
	checkoutID := fixture.checkoutID
	fixture.mu.Unlock()
	if err := encoder.Encode(map[string]any{
		"id": id, "method": "item/tool/call", "params": map[string]any{
			"arguments": map[string]any{
				"grant_id": grantID, "name": "moss-reviewer", "role": "independent reviewer",
				"provider": "codex-subscription", "runtime": "herdr", "max_concurrency": 1,
				"operating_charter": daemonTestDomainCharter, "delegation_policy": domain.DomainAgentHandsOn,
				"task_class": "reviewer", "budget": map[string]any{"token_limit": 50, "cost_cents": 50, "time_seconds": 120},
				"checkout": checkoutID,
			},
			"callId": "call-child-one", "threadId": "thread-private-019",
			"tool": domainToolCreateChild, "turnId": "turn-new",
		},
	}); err != nil {
		fixture.t.Errorf("emit app-server child tool request: %v", err)
		return
	}
	if !scanner.Scan() {
		fixture.t.Errorf("read app-server child tool response: %v", scanner.Err())
		return
	}
	var possibleRequest map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &possibleRequest); err == nil && possibleRequest["method"] == "thread/start" {
		params, _ := possibleRequest["params"].(map[string]any)
		if !strings.Contains(fmt.Sprint(params["developerInstructions"]), `"moss-reviewer"`) {
			fixture.t.Errorf("durable child thread/start params = %#v", params)
			return
		}
		if err := encoder.Encode(map[string]any{"id": possibleRequest["id"], "result": map[string]any{"thread": map[string]any{
			"id": "thread-private-child", "cwd": "/work", "ephemeral": false, "modelProvider": "openai",
			"status": map[string]any{"type": "idle"}, "turns": []any{},
		}}}); err != nil {
			return
		}
		if !scanner.Scan() {
			fixture.t.Errorf("read durable child thread name request: %v", scanner.Err())
			return
		}
		var nameRequest map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &nameRequest); err != nil || nameRequest["method"] != "thread/name/set" {
			fixture.t.Errorf("durable child thread name request = %#v, error = %v", nameRequest, err)
			return
		}
		nameParams, _ := nameRequest["params"].(map[string]any)
		if nameParams["threadId"] != "thread-private-child" || nameParams["name"] != "Crewfold: moss-reviewer" {
			fixture.t.Errorf("durable child thread/name/set params = %#v", nameParams)
			return
		}
		if err := encoder.Encode(map[string]any{"id": nameRequest["id"], "result": map[string]any{}}); err != nil {
			return
		}
		if !scanner.Scan() {
			fixture.t.Errorf("read app-server child tool response after activation: %v", scanner.Err())
			return
		}
	}
	var response struct {
		ID     int64 `json:"id"`
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		fixture.t.Errorf("decode app-server child tool response: %v", err)
		return
	}
	if response.ID != id || response.Error != nil || !response.Result.Success || len(response.Result.ContentItems) != 1 {
		fixture.t.Errorf("app-server child tool response = %#v", response)
		return
	}
	var result struct {
		Schema string `json:"schema"`
		Agent  struct {
			Name string `json:"name"`
		} `json:"agent"`
		Membership struct {
			ParentAgentID string `json:"parent_agent_id"`
		} `json:"membership"`
	}
	if err := json.Unmarshal([]byte(response.Result.ContentItems[0].Text), &result); err != nil {
		fixture.t.Errorf("decode durable child result: %v", err)
		return
	}
	if result.Schema != "urn:crewfold:schema:domain:durable-agent-child-result:v1" || result.Agent.Name != "moss-reviewer" || result.Membership.ParentAgentID == "" {
		fixture.t.Errorf("durable child result = %#v", result)
	}
}

func (fixture *codexDomainSessionFixture) requireDomainMessageToolResponse(scanner *bufio.Scanner, encoder *json.Encoder, id int64) {
	fixture.t.Helper()
	if err := encoder.Encode(map[string]any{
		"id": id, "method": "item/tool/call", "params": map[string]any{
			"arguments": map[string]any{
				"recipient_agent": "fern", "kind": "question", "new_topic": true, "subject": "Shared boundary",
				"body": "freeze the shared interface before changing it",
			},
			"callId": "call-message-one", "threadId": "thread-private-019",
			"tool": domainToolSendMessage, "turnId": "turn-new",
		},
	}); err != nil {
		fixture.t.Errorf("emit app-server message tool request: %v", err)
		return
	}
	if !scanner.Scan() {
		fixture.t.Errorf("read app-server message tool response: %v", scanner.Err())
		return
	}
	var response struct {
		ID     int64 `json:"id"`
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		fixture.t.Errorf("decode app-server message tool response: %v", err)
		return
	}
	if response.ID != id || response.Error != nil || !response.Result.Success || len(response.Result.ContentItems) != 1 {
		fixture.t.Errorf("app-server message tool response = %#v", response)
		return
	}
	var result struct {
		Schema  string `json:"schema"`
		Message struct {
			SenderType string `json:"sender_type"`
			Kind       string `json:"kind"`
			Body       string `json:"body"`
		} `json:"message"`
		Recipient struct {
			RecipientName string `json:"recipient_name"`
		} `json:"recipient"`
	}
	if err := json.Unmarshal([]byte(response.Result.ContentItems[0].Text), &result); err != nil {
		fixture.t.Errorf("decode durable-agent message result: %v", err)
		return
	}
	if result.Schema != "urn:crewfold:schema:domain:durable-agent-message-result:v1" ||
		result.Message.SenderType != "durable_agent" || result.Message.Kind != "question" ||
		result.Message.Body != "freeze the shared interface before changing it" || result.Recipient.RecipientName != "fern" {
		fixture.t.Errorf("durable-agent message result = %#v", result)
	}
}

func (fixture *codexDomainSessionFixture) requireDomainToolResponse(scanner *bufio.Scanner, encoder *json.Encoder, id int64, tool, threadID string, wantSuccess bool) {
	fixture.t.Helper()
	if err := encoder.Encode(map[string]any{
		"id": id, "method": "item/tool/call", "params": map[string]any{
			"arguments": map[string]any{}, "callId": fmt.Sprintf("call-context-%d", id), "threadId": threadID,
			"tool": tool, "turnId": "turn-new",
		},
	}); err != nil {
		fixture.t.Errorf("emit app-server tool request: %v", err)
		return
	}
	if !scanner.Scan() {
		fixture.t.Errorf("read app-server tool response: %v", scanner.Err())
		return
	}
	var response struct {
		ID     int64 `json:"id"`
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		fixture.t.Errorf("decode app-server tool response: %v", err)
		return
	}
	if response.ID != id || response.Error != nil || response.Result.Success != wantSuccess || len(response.Result.ContentItems) != 1 || response.Result.ContentItems[0].Type != "inputText" {
		fixture.t.Errorf("app-server tool response = %#v, want id %d success %t", response, id, wantSuccess)
		return
	}
	if wantSuccess {
		var contextResult struct {
			Schema string `json:"schema"`
			Domain struct {
				Name string `json:"name"`
			} `json:"domain"`
			Agent struct {
				Name string `json:"name"`
				Role string `json:"role"`
			} `json:"agent"`
			AuthorityNote       string                            `json:"authority_note"`
			StaffingGrants      []domain.DomainAgentStaffingGrant `json:"staffing_grants"`
			CoordinationThreads []domain.ThreadSummary            `json:"coordination_threads"`
			KnowledgeAuthoring  struct {
				Available bool   `json:"available"`
				Operation string `json:"operation"`
			} `json:"knowledge_authoring"`
		}
		if err := json.Unmarshal([]byte(response.Result.ContentItems[0].Text), &contextResult); err != nil {
			fixture.t.Errorf("decode exact durable-agent context: %v", err)
			return
		}
		if contextResult.Schema != "urn:crewfold:schema:domain:durable-agent-context:v1" || contextResult.Domain.Name != "garden" || contextResult.Agent.Name != "orchid" || contextResult.Agent.Role != "owner-defined-whatever" || len(contextResult.StaffingGrants) != 1 || !strings.Contains(contextResult.AuthorityNote, "do not grant authority") || !contextResult.KnowledgeAuthoring.Available || contextResult.KnowledgeAuthoring.Operation != domainToolProposeKnowledge {
			fixture.t.Errorf("durable-agent context = %#v", contextResult)
			return
		}
		if id == 7008 && (len(contextResult.CoordinationThreads) != 1 || len(contextResult.CoordinationThreads[0].AgentIDs) != 2) {
			fixture.t.Errorf("durable-agent coordination context = %#v", contextResult.CoordinationThreads)
			return
		}
	}
	fixture.mu.Lock()
	fixture.toolChecks++
	fixture.mu.Unlock()
}

func (fixture *codexDomainSessionFixture) thread(status string) map[string]any {
	turns := []any{map[string]any{
		"id": "turn-existing", "status": "completed", "items": []any{
			map[string]any{"id": "owner-existing", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "existing owner instruction"}}},
			map[string]any{"id": "agent-existing", "type": "agentMessage", "text": "existing provider response"},
		},
	}}
	fixture.mu.Lock()
	if fixture.turnExists {
		turns = append(turns, fixture.newTurn("inProgress"))
	}
	fixture.mu.Unlock()
	return map[string]any{
		"id": "thread-private-019", "cwd": "/work", "ephemeral": false, "modelProvider": "openai",
		"status": map[string]any{"type": status}, "turns": turns,
	}
}

func (fixture *codexDomainSessionFixture) newTurn(status string) map[string]any {
	return map[string]any{
		"id": "turn-new", "status": status, "items": []any{
			map[string]any{"id": "owner-new", "type": "userMessage", "clientId": "crewfold:m22-turn-one", "content": []any{map[string]any{"type": "text", "text": "continue the exact work"}}},
		},
	}
}

func (fixture *codexDomainSessionFixture) turnStarts() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.turnStartCount
}

func (fixture *codexDomainSessionFixture) compacts() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.compactCount
}

func (fixture *codexDomainSessionFixture) interrupts() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.interruptCount
}

func (fixture *codexDomainSessionFixture) resumes() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.resumeCount
}

func (fixture *codexDomainSessionFixture) unloadedReads() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.unloadedReadCount
}

func (fixture *codexDomainSessionFixture) sawArbitraryAgentInstructions() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.startedParams == nil || fixture.startedParams["ephemeral"] != false {
		return false
	}
	instructions, _ := fixture.startedParams["developerInstructions"].(string)
	baseInstructions, _ := fixture.startedParams["baseInstructions"].(string)
	dynamicTools, _ := fixture.startedParams["dynamicTools"].([]any)
	if len(dynamicTools) != 1 {
		return false
	}
	namespace, _ := dynamicTools[0].(map[string]any)
	tools, _ := namespace["tools"].([]any)
	if namespace["name"] != "crewfold" || namespace["type"] != "namespace" || len(tools) != 10 {
		return false
	}
	expectedTools := []string{domainToolContext, domainToolSendMessage, domainToolCreateChild, domainToolDelegateStaffing, domainToolProposeWork, domainToolProposeKnowledge, domainToolControlService, domainToolInspectService, domainToolRequestService, domainToolDelegateService}
	for index, expected := range expectedTools {
		tool, _ := tools[index].(map[string]any)
		if tool["name"] != expected || tool["type"] != "function" {
			return false
		}
	}
	config, _ := fixture.startedParams["config"].(map[string]any)
	directNamespaces, _ := config["code_mode.direct_only_tool_namespaces"].([]any)
	toolChecks := fixture.toolChecks
	return fixture.startedParams["sandbox"] == execution.CodexSandboxReadOnly && fixture.startedParams["approvalPolicy"] == "never" &&
		strings.Contains(baseInstructions, "real continuing coordination agent") && strings.Contains(baseInstructions, "complete inert team") && strings.Contains(baseInstructions, "Do not call crewfold_create_durable_child while planning a deliverable") && strings.Contains(baseInstructions, "code-mode tools object") && strings.Contains(baseInstructions, "never shell out to the crewfold binary") && strings.Contains(baseInstructions, "not canonical knowledge") && strings.Contains(baseInstructions, "Never edit repository files") && strings.Contains(instructions, `"orchid"`) && strings.Contains(instructions, `"owner-defined-whatever"`) && strings.Contains(instructions, daemonTestDomainCharter) && strings.Contains(instructions, domain.DomainAgentAdaptive) && strings.Contains(instructions, "grants no authority") && strings.Contains(instructions, "tools.crewfold__") && strings.Contains(instructions, "Never invoke the crewfold CLI") &&
		len(directNamespaces) == 1 && directNamespaces[0] == "crewfold" && toolChecks == 4
}

func (fixture *codexDomainSessionFixture) sawCurrentReadOnlyResumeBoundary() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	params := fixture.resumeParams
	if params == nil || params["threadId"] != "thread-private-019" || params["cwd"] != "/work" ||
		params["sandbox"] != execution.CodexSandboxReadOnly || params["approvalPolicy"] != "never" {
		return false
	}
	base, _ := params["baseInstructions"].(string)
	instructions, _ := params["developerInstructions"].(string)
	config, _ := params["config"].(map[string]any)
	directNamespaces, _ := config["code_mode.direct_only_tool_namespaces"].([]any)
	return strings.Contains(base, "not an implementation run") && strings.Contains(strings.ToLower(base), "never edit repository files") &&
		strings.Contains(instructions, `"orchid"`) && len(directNamespaces) == 1 && directNamespaces[0] == "crewfold"
}

func (fixture *codexDomainSessionFixture) resumeParameters() map[string]any {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.resumeParams
}
