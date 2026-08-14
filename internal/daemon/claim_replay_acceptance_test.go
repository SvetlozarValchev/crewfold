package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/gitstate"
	"crewfold/internal/localapi"
)

func TestM19ClaimAddReplaySkipsGitObservationAndReturnsExactPublicResult(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	repositoryPath := filepath.Join(fixtureRoot, "world-engine")
	inspector := &m19CountingGitInspector{delegate: gitstate.NewInspector()}
	config := testConfig(t)
	config.GitInspector = inspector
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.DisableLeaseReconciler = true

	firstServer := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "claim-replay-workspace"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "demo", repositoryPath, domain.WriteModeExclusive, "claim-replay-project")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	task, err := client.TaskCreate(context.Background(), localapi.TaskCreateParams{
		Workspace: "personal", Project: "demo", Title: "claim replay target", Priority: 100,
		IdempotencyKey: "claim-replay-task",
	})
	if err != nil {
		t.Fatalf("TaskCreate() error = %v", err)
	}
	params := localapi.ClaimAddParams{
		Workspace: "personal", Project: "demo", Task: task.Detail.Task.ID, Checkout: project.Checkout.ID,
		Kind: domain.ClaimKindPath, Target: "internal/replay/**", Mode: domain.ClaimModeExclusive,
		ConflictPolicy: domain.ClaimPolicyNotify, LeaseSeconds: 3600, IdempotencyKey: "claim-add-daemon-replay",
	}
	firstBytes := m19RawClaimAdd(t, config.SocketPath, "transport-claim-add-first", params)
	var first localapi.ClaimMutationResult
	if err := json.Unmarshal(firstBytes, &first); err != nil || first.Claim.ID == "" {
		t.Fatalf("decode first claim result = %#v, %v", first, err)
	}
	inspectCalls := inspector.calls.Load()
	if inspectCalls < 2 {
		t.Fatalf("first request inspected Git %d times, want project registration and claim baseline", inspectCalls)
	}
	m19StopTestServer(t, firstServer)
	before := m19ReadClaimReplayState(t, config.DataDir, project.Checkout.ID, first.Claim.ID)

	if err := os.WriteFile(filepath.Join(repositoryPath, "replay-must-not-observe.txt"), []byte("changed after commit\n"), 0o600); err != nil {
		t.Fatalf("change Git observation fixture: %v", err)
	}
	secondServer := startTestServer(t, config)
	replayedBytes := m19RawClaimAdd(t, config.SocketPath, "transport-claim-add-replay", params)
	if !bytes.Equal(replayedBytes, firstBytes) {
		t.Fatalf("claim.add replay result changed:\nfirst:  %s\nreplay: %s", firstBytes, replayedBytes)
	}
	if got := inspector.calls.Load(); got != inspectCalls {
		t.Fatalf("claim.add replay inspected changed Git state: before=%d after=%d", inspectCalls, got)
	}
	m19StopTestServer(t, secondServer)
	after := m19ReadClaimReplayState(t, config.DataDir, project.Checkout.ID, first.Claim.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("claim.add replay changed durable observation/scan/event state:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

type m19CountingGitInspector struct {
	delegate gitstate.Inspector
	calls    atomic.Int64
}

func (inspector *m19CountingGitInspector) Inspect(ctx context.Context, path string) (domain.CheckoutObservation, error) {
	inspector.calls.Add(1)
	return inspector.delegate.Inspect(ctx, path)
}

func m19RawClaimAdd(t *testing.T, socketPath, requestID string, params localapi.ClaimAddParams) []byte {
	t.Helper()
	connection, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("connect raw claim client: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	encoder, decoder := json.NewEncoder(connection), json.NewDecoder(connection)
	helloParams, err := json.Marshal(localapi.HelloParams{MinProtocol: localapi.MinProtocol, MaxProtocol: localapi.MaxProtocol})
	if err != nil {
		t.Fatal(err)
	}
	helloID := "hello-" + requestID
	if err := encoder.Encode(localapi.Request{ID: helloID, Method: localapi.MethodHello, Params: helloParams}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	var helloResponse localapi.Response
	if err := decoder.Decode(&helloResponse); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if helloResponse.ID != helloID || helloResponse.Error != nil {
		t.Fatalf("hello response = %#v", helloResponse)
	}
	var hello localapi.HelloResult
	if err := json.Unmarshal(helloResponse.Result, &hello); err != nil || hello.SelectedProtocol < localapi.MinProtocol || hello.SelectedProtocol > localapi.MaxProtocol {
		t.Fatalf("decode hello = %#v, %v", hello, err)
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(localapi.Request{
		ID: requestID, Protocol: hello.SelectedProtocol, Method: localapi.MethodClaimAdd, Params: encodedParams,
	}); err != nil {
		t.Fatalf("write claim.add: %v", err)
	}
	var response localapi.Response
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("read claim.add: %v", err)
	}
	if response.ID != requestID || response.Error != nil || len(response.Result) == 0 {
		t.Fatalf("claim.add response = %#v", response)
	}
	return append([]byte(nil), response.Result...)
}

func m19StopTestServer(t *testing.T, running *runningServer) {
	t.Helper()
	if _, err := localapi.NewClient(running.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

type m19ClaimReplayState struct {
	CheckoutRevision       int64
	CheckoutHeadCommit     sql.NullString
	CheckoutDirty          int
	CheckoutDirtyPathsJSON string
	CheckoutObservedAt     string
	EventCount             int
	CheckoutObservedEvents int
	ScanCount              int
	ScanWatcher            string
	ScanHeadCommit         sql.NullString
	ScanDirtyPathsJSON     string
	ScanObservedAt         string
	ClaimStatus            string
	ClaimRevision          int64
}

func m19ReadClaimReplayState(t *testing.T, dataDir, checkoutID, claimID string) m19ClaimReplayState {
	t.Helper()
	location := &url.URL{Scheme: "file", Path: filepath.Join(dataDir, "crewfold.db")}
	query := location.Query()
	query.Set("mode", "ro")
	location.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatalf("open read-only state: %v", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	ctx := context.Background()
	var state m19ClaimReplayState
	if err := database.QueryRowContext(ctx, `
SELECT revision, head_commit, dirty, dirty_paths_json, observed_at
FROM checkouts WHERE id = ?`, checkoutID).Scan(
		&state.CheckoutRevision, &state.CheckoutHeadCommit, &state.CheckoutDirty,
		&state.CheckoutDirtyPathsJSON, &state.CheckoutObservedAt,
	); err != nil {
		t.Fatalf("read checkout replay state: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&state.EventCount); err != nil {
		t.Fatalf("count replay events: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM events
WHERE entity_type = 'checkout' AND entity_id = ? AND type = 'checkout.observed'`, checkoutID).Scan(&state.CheckoutObservedEvents); err != nil {
		t.Fatalf("count checkout observation events: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM checkout_claim_scans WHERE checkout_id = ?", checkoutID).Scan(&state.ScanCount); err != nil {
		t.Fatalf("count claim scans: %v", err)
	}
	if state.ScanCount != 1 {
		t.Fatalf("initial claim scan count = %d, want 1", state.ScanCount)
	}
	if err := database.QueryRowContext(ctx, `
SELECT watcher_id, head_commit, dirty_paths_json, observed_at
FROM checkout_claim_scans WHERE checkout_id = ?`, checkoutID).Scan(
		&state.ScanWatcher, &state.ScanHeadCommit, &state.ScanDirtyPathsJSON, &state.ScanObservedAt,
	); err != nil {
		t.Fatalf("read claim scan state: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT status, revision FROM work_claims WHERE id = ?", claimID).Scan(&state.ClaimStatus, &state.ClaimRevision); err != nil {
		t.Fatalf("read claim replay state: %v", err)
	}
	return state
}
