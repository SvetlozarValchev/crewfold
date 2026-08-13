package daemon

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

type countingCheckWatchInspector struct {
	calls       int
	observation domain.CheckoutObservation
}

func (inspector *countingCheckWatchInspector) Inspect(context.Context, string) (domain.CheckoutObservation, error) {
	inspector.calls++
	return inspector.observation, nil
}

func TestCheckWatcherConfigurationIsBoundedAndEnabledByDefault(t *testing.T) {
	t.Parallel()

	base := testConfig(t)
	base.RuntimeDrivers = map[string]execution.RuntimeDriver{}
	base.ProviderAdapters = map[string]execution.ProviderAdapter{}

	resolved, err := resolveConfig(base)
	if err != nil {
		t.Fatalf("resolveConfig(default check watcher) error = %v", err)
	}
	if resolved.DisableCheckWatcher || resolved.CheckWatchScanInterval != 2*time.Second {
		t.Fatalf("default check-watcher configuration = disabled %t, interval %s", resolved.DisableCheckWatcher, resolved.CheckWatchScanInterval)
	}

	tooFast := base
	tooFast.CheckWatchScanInterval = 99 * time.Millisecond
	if _, err := resolveConfig(tooFast); ErrorCode(err) != CodeInvalidConfiguration {
		t.Fatalf("resolveConfig(too-fast check watcher) error = %v, code = %q", err, ErrorCode(err))
	}

	disabled := tooFast
	disabled.DisableCheckWatcher = true
	if _, err := resolveConfig(disabled); err != nil {
		t.Fatalf("resolveConfig(disabled check watcher) error = %v", err)
	}
}

func TestCheckWatcherInspectsOneFrozenCheckoutSnapshotOncePerPass(t *testing.T) {
	t.Parallel()
	inspector := &countingCheckWatchInspector{observation: domain.CheckoutObservation{
		Repository: domain.RepositoryObservation{Fingerprint: "frozen", ObjectFormat: "sha1"},
		Branch:     "main", HeadCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DirtyPaths: []string{},
	}}
	server := &server{gitInspector: inspector}
	base := store.CheckWatchCandidate{
		CheckResultID: "checkresult_00000000000000000000000000000001", FreshnessRevision: 1,
		RepositoryID: "repo_00000000000000000000000000000000", RepositoryRevision: 2,
		RepositoryFingerprint: "frozen", ObjectFormat: "sha1",
		CheckoutID: "co_00000000000000000000000000000000", CheckoutRevision: 3, CheckoutPath: "/checkout",
	}
	second := base
	second.CheckResultID = "checkresult_00000000000000000000000000000002"
	observations := server.observeCheckWatchCandidates(context.Background(), []store.CheckWatchCandidate{base, second})
	if inspector.calls != 1 || len(observations) != 2 || !observations[0].Observation.Available ||
		observations[0].Observation.ObservedAt != observations[1].Observation.ObservedAt ||
		observations[0].CheckResultID == observations[1].CheckResultID {
		t.Fatalf("checkout inspections=%d observations=%#v", inspector.calls, observations)
	}
}

func TestBackgroundCheckWatcherUsesRealGitAndAppendsMonotonicStaleEvidence(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config, executable := checkWorkerTestConfig(t)
	config.DisableCheckWatcher = false
	config.CheckWatchScanInterval = 100 * time.Millisecond
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	marker := filepath.Join(t.TempDir(), "check-launched")
	started := createOwnerCheckRun(t, client, fixtureRoot, executable, ".", []string{
		"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", "mark-pass", marker,
	}, 4096)
	initial := waitForCheckResult(t, client, started.Run.ID)
	if initial.CurrentFreshness == nil || initial.CurrentFreshness.Status != domain.CheckFreshnessFresh || len(initial.Evidence.MechanicalCheck) != 1 {
		t.Fatalf("initial fresh evidence = %#v", initial)
	}
	// Several unchanged background ticks are an exact no-op: they must not
	// manufacture later fresh observations or receipts merely because time passed.
	time.Sleep(350 * time.Millisecond)
	idle, err := client.CheckInspect(context.Background(), "personal", started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if idle.Detail.CurrentFreshness == nil || idle.Detail.CurrentFreshness.Revision != 1 || len(idle.Detail.Evidence.MechanicalCheck) != 1 {
		t.Fatalf("unchanged background ticks created evidence churn: %#v", idle.Detail)
	}

	advanceCheckFixtureHead(t, fixtureRoot)

	var stale domain.CheckRunDetail
	waitForCondition(t, 8*time.Second, func() bool {
		result, err := client.CheckInspect(context.Background(), "personal", started.Run.ID)
		if err != nil {
			return false
		}
		stale = result.Detail
		return stale.CurrentFreshness != nil && stale.CurrentFreshness.Status == domain.CheckFreshnessStale
	}, "background check watcher to observe changed HEAD")
	if stale.CurrentFreshness.Revision != 2 || len(stale.Evidence.MechanicalCheck) != 2 ||
		stale.Evidence.MechanicalCheck[0].FreshnessRevision != 1 || stale.Evidence.MechanicalCheck[0].Effect != domain.CheckEvidenceSupports ||
		stale.Evidence.MechanicalCheck[1].FreshnessRevision != 2 || stale.Evidence.MechanicalCheck[1].Effect != domain.CheckEvidenceInconclusive {
		t.Fatalf("append-only stale evidence = %#v", stale.Evidence.MechanicalCheck)
	}

	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var receipts int
	if err := database.QueryRow("SELECT COUNT(*) FROM check_watch_receipts").Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("background effect receipts = %d, %v; want exactly one effectful pass", receipts, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPublicCheckWatchPersistsAndReplaysOneRealGitPass(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config, executable := checkWorkerTestConfig(t)
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	marker := filepath.Join(t.TempDir(), "check-launched")
	started := createOwnerCheckRun(t, client, fixtureRoot, executable, ".", []string{
		"-test.run=^TestCheckProcessHelper$", "--", "crewfold-check-process-helper", "mark-pass", marker,
	}, 4096)
	initial := waitForCheckResult(t, client, started.Run.ID)
	if initial.CurrentFreshness == nil || initial.CurrentFreshness.Revision != 1 {
		t.Fatalf("initial result = %#v", initial)
	}
	baselineParams := localapi.CheckWatchParams{
		Workspace: "personal", Project: started.Run.ProjectID, Limit: 100, IdempotencyKey: "public-real-git-watch-baseline",
	}
	baseline, err := client.CheckWatch(context.Background(), baselineParams)
	if err != nil || baseline.Receipt.NextCursor == "" || baseline.Receipt.FreshnessAppended != 0 || baseline.EventSequence == 0 {
		t.Fatalf("CheckWatch(baseline) = %#v, %v", baseline, err)
	}
	advanceCheckFixtureHead(t, fixtureRoot)

	params := localapi.CheckWatchParams{
		Workspace: "personal", Project: started.Run.ProjectID, Cursor: baseline.Receipt.NextCursor,
		Limit: 100, IdempotencyKey: "public-real-git-watch",
	}
	first, err := client.CheckWatch(context.Background(), params)
	if err != nil {
		t.Fatalf("CheckWatch(first) error = %v", err)
	}
	if first.Receipt.FreshnessAppended != 1 || first.Receipt.CreatedBy != "local-owner" || first.EventSequence == 0 ||
		!reflect.DeepEqual(first.Receipt.ExaminedResultIDs, []string{initial.Result.ID}) {
		t.Fatalf("CheckWatch(first) = %#v", first)
	}
	replayed, err := client.CheckWatch(context.Background(), params)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("CheckWatch(replay) = %#v, %v; want %#v", replayed, err, first)
	}
	noopParams := params
	noopParams.Cursor = first.Receipt.NextCursor
	noopParams.IdempotencyKey = "public-real-git-watch-noop"
	noop, err := client.CheckWatch(context.Background(), noopParams)
	if err != nil || noop.EventSequence == 0 || noop.Receipt.ID == "" || noop.Receipt.ContentSHA256 == "" || noop.Receipt.FreshnessAppended != 0 {
		t.Fatalf("CheckWatch(public no-op) = %#v, %v", noop, err)
	}
	changed := params
	changed.Limit = 99
	if _, err := client.CheckWatch(context.Background(), changed); err == nil {
		t.Fatal("CheckWatch(same key, changed request) error = nil")
	}
	detail, err := client.CheckInspect(context.Background(), "personal", started.Run.ID)
	if err != nil || detail.Detail.CurrentFreshness == nil || detail.Detail.CurrentFreshness.Status != domain.CheckFreshnessStale || detail.Detail.CurrentFreshness.Revision != 2 {
		t.Fatalf("public stale detail = %#v, %v", detail, err)
	}
	// Replay lookup precedes preparation: an immutable exact public response
	// remains available even when a later binary-incompatible fact would stop a
	// fresh check-watch pass before inspecting Git or applying effects.
	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO events(event_id,type,schema_version,occurred_at,recorded_at,actor_id,actor_type,workspace_id,entity_type,entity_id,entity_revision,correlation_id,causation_id,data_json)
		VALUES(?,'future.check.surface_fact',1,?,?,'local-owner','human',?,'project',?,1,'public-watch-replay-unknown',NULL,'{}')`,
		"evt_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", now, now, started.Run.WorkspaceID, started.Run.ProjectID); err != nil {
		database.Close()
		t.Fatalf("insert later unknown event: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	unknownReplay, err := client.CheckWatch(context.Background(), baselineParams)
	if err != nil || !reflect.DeepEqual(unknownReplay, baseline) {
		t.Fatalf("CheckWatch(replay before unknown-event preparation) = %#v, %v; want %#v", unknownReplay, err, baseline)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func advanceCheckFixtureHead(t *testing.T, fixtureRoot string) {
	t.Helper()
	checkout := filepath.Join(fixtureRoot, "world-engine")
	readme := filepath.Join(checkout, "README.md")
	contents, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readme, append(contents, []byte("\npost-check change\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "README.md"}, {"commit", "--quiet", "-m", "advance after check"}} {
		if output, err := exec.Command("git", append([]string{"-C", checkout}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
}
