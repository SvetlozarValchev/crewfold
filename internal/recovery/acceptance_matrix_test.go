package recovery

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

func TestOnlineBackupCapturesOneWALCutAndExactArtifactClosure(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	seed, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
		Name: "cut-seed", IdempotencyKey: "cut-seed", CorrelationID: "cut-seed",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hold one real WAL writer across the online-backup boundary. The first
	// commit is known to precede the snapshot; afterSnapshot releases the second
	// commit and waits for it before verification continues. The prepared log
	// file deliberately has no terminal DB reference and therefore must not enter
	// the captured closure even though it is published immediately after the cut.
	preCutResult := make(chan error, 1)
	snapshotCaptured := make(chan struct{})
	postCutResult := make(chan error, 1)
	var postCut store.WorkspaceInitResult
	go func() {
		if _, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
			Name: "cut-writer-before", IdempotencyKey: "cut-writer-before", CorrelationID: "cut-writer-before",
		}); err != nil {
			preCutResult <- err
			return
		}
		preCutResult <- nil
		<-snapshotCaptured
		var err error
		postCut, err = storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
			Name: "cut-writer-after", IdempotencyKey: "cut-writer-after", CorrelationID: "cut-writer-after",
		})
		if err != nil {
			postCutResult <- err
			return
		}
		runID := "run_00000000000000000000000000000001"
		if _, err := storage.PrepareRunLogArchive(ctx, runID, domain.RunLogs{
			RunID: runID, State: "exited",
			Stdout: domain.CapturedLog{Text: "unreferenced concurrent artifact\n", CapturedBytes: 31},
		}); err != nil {
			postCutResult <- err
			return
		}
		postCutResult <- nil
	}()
	if err := <-preCutResult; err != nil {
		t.Fatalf("pre-cut WAL writer error = %v", err)
	}

	target := filepath.Join(t.TempDir(), "wal-cut")
	created, err := createBundleWithHooks(ctx, storage, dataDir, target, "wal-cut", createHooks{
		afterSnapshot: func() error {
			close(snapshotCaptured)
			return <-postCutResult
		},
	})
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	if created.Manifest.EventHighWater < seed.EventSequence || created.Manifest.EventHighWater >= postCut.EventSequence {
		t.Fatalf("captured high-water = %d, seed = %d, post-cut = %d", created.Manifest.EventHighWater, seed.EventSequence, postCut.EventSequence)
	}
	verified, err := VerifyBundle(ctx, target)
	if err != nil {
		t.Fatalf("VerifyBundle() error = %v", err)
	}
	if verified.Integrity.EventHighWater != created.Manifest.EventHighWater ||
		verified.Integrity.LogicalSHA256 != created.Manifest.LogicalSHA256 ||
		!equalArtifactEntries(ArtifactEntries(verified.Integrity.ArtifactReferences), verified.Manifest.Entries) {
		t.Fatalf("verified cut does not exactly match manifest: %#v", verified)
	}
	if len(verified.Manifest.Entries) != 0 {
		t.Fatalf("unreferenced concurrent artifact entered bundle: %#v", verified.Manifest.Entries)
	}
	if _, err := os.Stat(filepath.Join(target, "run-artifacts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle copied unreferenced runtime artifact namespace: %v", err)
	}
}

func TestCreateBundleRefusesActionableRunWithoutPublishingOrAppendingEvent(t *testing.T) {
	ctx := context.Background()
	storage, dataDir, workspaceID, assigned, _ := newAssignedRecoveryFixture(t)
	created, err := storage.CreateRun(ctx, store.CreateRunCommand{
		WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "quiescence-blocker", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "quiescence-run", CorrelationID: "quiescence-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := storage.ListEvents(ctx, store.ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "refused-bundle")
	_, err = CreateBundle(ctx, storage, dataDir, target, "quiescence-refusal")
	if ErrorCode(err) != CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle() error = %v, code = %q", err, ErrorCode(err))
	}
	var detail *BackupNotQuiescentDetails
	for current := err; current != nil; {
		if typed, ok := current.(*Error); ok {
			detail = typed.Quiescence
			break
		}
		unwrapper, ok := current.(interface{ Unwrap() error })
		if !ok {
			break
		}
		current = unwrapper.Unwrap()
	}
	if detail == nil || detail.Counts.NonterminalRuns != 1 || detail.Counts.UnsettledRunJobs != 1 || len(detail.Samples) == 0 || len(detail.Samples) > 20 {
		t.Fatalf("quiescence refusal details = %#v for run %s", detail, created.Detail.Run.ID)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused backup published a target: %v", err)
	}
	after, err := storage.ListEvents(ctx, store.ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: 1})
	if err != nil || after.HighWater != before.HighWater {
		t.Fatalf("backup refusal changed event high-water from %d to %d: %v", before.HighWater, after.HighWater, err)
	}
}

func TestCreateBundleRefusesUnfinishedCheckAndItsDurableJob(t *testing.T) {
	ctx := context.Background()
	storage, dataDir, workspaceID, assigned, checkout := newAssignedRecoveryFixture(t)
	definition, err := storage.CreateCheckDefinition(ctx, store.CreateCheckDefinitionCommand{
		WorkspaceIdentifier: workspaceID, ProjectIdentifier: assigned.Task.ProjectID, Name: "quiescence-check",
		Executable: "/bin/true", Arguments: []string{"--version"}, WorkingDirectory: ".", TimeoutMillis: 1_000, OutputByteLimit: 1_024,
		IdempotencyKey: "quiescence-check-definition", CorrelationID: "quiescence-check-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := storage.CreateTaskCheckRequirement(ctx, store.CreateTaskCheckRequirementCommand{
		WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, CriterionKey: "quiescence-check",
		Statement: "the exact check passes", CheckDefinitionID: definition.Value.ID, DefinitionContentRevision: definition.Value.ContentRevision,
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "quiescence-check-requirement", CorrelationID: "quiescence-check-requirement",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := storage.RequestCheckRun(ctx, store.RequestCheckRunCommand{
		WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, RequirementID: requirement.Value.ID,
		CheckDefinitionIdentifier: definition.Value.ID, CheckoutIdentifier: checkout.ID, ExpectedCheckoutRevision: checkout.Revision,
		ExpectedRequirementRevision:       requirement.Value.Revision,
		ExpectedDefinitionContentRevision: definition.Value.ContentRevision,
		IdempotencyKey:                    "quiescence-check-run", CorrelationID: "quiescence-check-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "refused-check-bundle")
	_, err = CreateBundle(ctx, storage, dataDir, target, "quiescence-check-refusal")
	if ErrorCode(err) != CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle() error = %v, code = %q", err, ErrorCode(err))
	}
	typed, ok := err.(*Error)
	if !ok || typed.Quiescence == nil || typed.Quiescence.Counts.UnfinishedCheckRuns != 1 || typed.Quiescence.Counts.UnsettledCheckJobs != 1 {
		t.Fatalf("check quiescence refusal = %#v for %s", typed, requested.Value.ID)
	}
	wantSamples := map[string]bool{"unfinished_check_run": false, "unsettled_check_job": false}
	for _, sample := range typed.Quiescence.Samples {
		if _, exists := wantSamples[sample.Kind]; exists {
			wantSamples[sample.Kind] = true
		}
	}
	for kind, found := range wantSamples {
		if !found {
			t.Fatalf("quiescence samples omit %s: %#v", kind, typed.Quiescence.Samples)
		}
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused check backup published target: %v", err)
	}

	if err := os.MkdirAll(checkout.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	work, found, err := storage.ClaimCheckJob(ctx, 30*time.Second)
	if err != nil || !found || work.Run.ID != requested.Value.ID {
		t.Fatalf("ClaimCheckJob() = %#v, %t, %v", work, found, err)
	}
	if _, err := storage.MarkCheckStarting(ctx, store.MarkCheckStartingCommand{
		CheckRunID: work.Run.ID, OperationID: work.Run.ID, EffectiveSpecSHA256: strings.Repeat("a", 64),
		EffectiveWorkingDirectory: checkout.Path, Launchable: true,
		Observation: domain.CheckGitObservation{
			Available: true, RepositoryID: work.Run.RepositoryID, ObjectFormat: work.Run.RepositoryObjectFormat,
			CheckoutID: work.Run.CheckoutID, Branch: checkout.Branch, HeadCommit: checkout.HeadCommit,
			DirtyPaths: []string{}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		CorrelationID: "quiescence-check-starting",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RecordCheckRuntimeBinding(ctx, work.Run.ID, "check-runtime-handle", "quiescence-check-binding"); err != nil {
		t.Fatal(err)
	}
	boundTarget := filepath.Join(t.TempDir(), "refused-bound-check-bundle")
	_, err = CreateBundle(ctx, storage, dataDir, boundTarget, "quiescence-bound-check-refusal")
	if ErrorCode(err) != CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle(bound check) error = %v, code = %q", err, ErrorCode(err))
	}
	typed, ok = err.(*Error)
	if !ok || typed.Quiescence == nil || typed.Quiescence.Counts.RuntimeBindings != 1 {
		t.Fatalf("bound check quiescence refusal = %#v", typed)
	}
	foundBinding := false
	for _, sample := range typed.Quiescence.Samples {
		if sample.Kind == "check_runtime_binding" && sample.EntityID == work.Run.ID {
			foundBinding = true
		}
	}
	if !foundBinding {
		t.Fatalf("quiescence samples omit exact check binding: %#v", typed.Quiescence.Samples)
	}
}

func TestCreateBundleRefusesNodeBoundLiveRuntime(t *testing.T) {
	ctx := context.Background()
	storage, dataDir, workspaceID, assigned, _ := newAssignedRecoveryFixture(t)
	created, err := storage.CreateRun(ctx, store.CreateRunCommand{
		WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario:             domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "bound-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "active"}}},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "bound-run", CorrelationID: "bound-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarting(ctx, created.Detail.Run.ID, "bound-run-starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(ctx, created.Detail.Run.ID, "runtime-handle", "provider-handle", "bound-run-started"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "refused-bound-bundle")
	_, err = CreateBundle(ctx, storage, dataDir, target, "bound-run-refusal")
	if ErrorCode(err) != CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle() error = %v, code = %q", err, ErrorCode(err))
	}
	typed, ok := err.(*Error)
	if !ok || typed.Quiescence == nil || typed.Quiescence.Counts.RuntimeBindings != 1 {
		t.Fatalf("runtime binding quiescence refusal = %#v", typed)
	}
	found := false
	for _, sample := range typed.Quiescence.Samples {
		if sample.Kind == "run_runtime_binding" && sample.EntityID == created.Detail.Run.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("quiescence samples omit exact run binding: %#v", typed.Quiescence.Samples)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused bound-run backup published target: %v", err)
	}
}

func TestBackupQuiescenceDiagnosticsKeepExactCountsAndTwentySamples(t *testing.T) {
	ctx := context.Background()
	storage, dataDir, workspaceID, assigned, checkout := newAssignedRecoveryFixture(t)
	for index := 0; index < 11; index++ {
		key := fmt.Sprintf("sample-cap-%02d", index)
		definition, err := storage.CreateCheckDefinition(ctx, store.CreateCheckDefinitionCommand{
			WorkspaceIdentifier: workspaceID, ProjectIdentifier: assigned.Task.ProjectID, Name: key,
			Executable: "/bin/true", Arguments: []string{"--version"}, WorkingDirectory: ".", TimeoutMillis: 1_000, OutputByteLimit: 1_024,
			IdempotencyKey: key + "-definition", CorrelationID: key + "-definition",
		})
		if err != nil {
			t.Fatal(err)
		}
		current, err := storage.TaskDetail(ctx, workspaceID, assigned.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		requirement, err := storage.CreateTaskCheckRequirement(ctx, store.CreateTaskCheckRequirementCommand{
			WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, CriterionKey: key,
			Statement: "bounded quiescence sample " + key, CheckDefinitionID: definition.Value.ID,
			DefinitionContentRevision: definition.Value.ContentRevision, ExpectedTaskRevision: current.Task.Revision,
			IdempotencyKey: key + "-requirement", CorrelationID: key + "-requirement",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storage.RequestCheckRun(ctx, store.RequestCheckRunCommand{
			WorkspaceIdentifier: workspaceID, TaskID: assigned.Task.ID, RequirementID: requirement.Value.ID,
			CheckDefinitionIdentifier: definition.Value.ID, CheckoutIdentifier: checkout.ID,
			ExpectedRequirementRevision: requirement.Value.Revision, ExpectedDefinitionContentRevision: definition.Value.ContentRevision,
			ExpectedCheckoutRevision: checkout.Revision, IdempotencyKey: key + "-run", CorrelationID: key + "-run",
		}); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "sample-cap-bundle")
	_, err := CreateBundle(ctx, storage, dataDir, target, "sample-cap-refusal")
	if ErrorCode(err) != CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle() error = %v, code = %q", err, ErrorCode(err))
	}
	typed, ok := err.(*Error)
	if !ok || typed.Quiescence == nil {
		t.Fatalf("CreateBundle() quiescence error = %#v", typed)
	}
	if typed.Quiescence.Counts.UnfinishedCheckRuns != 11 || typed.Quiescence.Counts.UnsettledCheckJobs != 11 || len(typed.Quiescence.Samples) != 20 {
		t.Fatalf("quiescence diagnostics = %#v, want exact 11/11 counts and 20 samples", typed.Quiescence)
	}
}

func TestManifestRejectsEveryFrozenQuiescenceCount(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	destination := filepath.Join(t.TempDir(), "crewfold.db")
	snapshot, err := storage.BackupSnapshot(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	integrity, err := store.VerifyDatabaseSnapshot(ctx, destination, store.CanonicalVerifyOptions{Full: true})
	if err != nil || !integrity.Quiescence.Quiescent {
		t.Fatalf("base integrity = %#v, %v", integrity, err)
	}
	tests := []struct {
		name   string
		mutate func(*store.QuiescenceCounts)
	}{
		{name: "nonterminal run", mutate: func(counts *store.QuiescenceCounts) { counts.NonterminalRuns = 1 }},
		{name: "run job", mutate: func(counts *store.QuiescenceCounts) { counts.UnsettledRunJobs = 1 }},
		{name: "runtime binding", mutate: func(counts *store.QuiescenceCounts) { counts.RuntimeBindings = 1 }},
		{name: "check run", mutate: func(counts *store.QuiescenceCounts) { counts.UnfinishedCheckRuns = 1 }},
		{name: "check job", mutate: func(counts *store.QuiescenceCounts) { counts.UnsettledCheckJobs = 1 }},
		{name: "wake job", mutate: func(counts *store.QuiescenceCounts) { counts.OpenWakeJobs = 1 }},
		{name: "scheduling intent", mutate: func(counts *store.QuiescenceCounts) { counts.OpenSchedulingIntents = 1 }},
		{name: "supervisor action", mutate: func(counts *store.QuiescenceCounts) { counts.OpenSupervisorActions = 1 }},
		{name: "approval", mutate: func(counts *store.QuiescenceCounts) { counts.OpenApprovals = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := integrity
			test.mutate(&candidate.Quiescence.Counts)
			candidate.Quiescence.Quiescent = false
			if _, err := NewManifest("backup_00000000000000000000000000000009", "2026-08-14T12:00:00Z", snapshot, candidate, nil); ErrorCode(err) != CodeCanonicalIntegrityFailed {
				t.Fatalf("NewManifest() error = %v, code = %q", err, ErrorCode(err))
			}
		})
	}
}

func TestBundleRemainsVerifiableAndRestorableAfterSourceRemoval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "source")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "portable", IdempotencyKey: "portable", CorrelationID: "portable"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "bundle")
	created, err := CreateBundle(ctx, storage, dataDir, bundle, "source-independent")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dataDir); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBundle(ctx, bundle)
	if err != nil || verified.ManifestSHA256 != created.ManifestSHA256 || verified.Manifest.BackupID != created.Manifest.BackupID {
		t.Fatalf("VerifyBundle(without source) = %#v, %v", verified, err)
	}
	target := filepath.Join(root, "restored")
	pending, err := RestorePending(ctx, bundle, target)
	if err != nil || pending.EventHighWater != workspace.EventSequence || pending.LogicalSHA256 != created.Manifest.LogicalSHA256 {
		t.Fatalf("RestorePending(without source) = %#v, %v", pending, err)
	}
}

func TestBackupExcludesOperationalAuthoritySecretsAndUnreferencedFiles(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	secret := []byte("M20_SECRET_NEVER_BUNDLED_91e90c2a")
	for _, relative := range []string{"node.key", "capabilities/token", "runtime/live-handle", "check-runtime/live-handle", "provider-home/credential", "daemon.log"} {
		path := filepath.Join(dataDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runID := "run_00000000000000000000000000000002"
	if _, err := storage.PrepareRunLogArchive(ctx, runID, domain.RunLogs{
		RunID: runID, State: "exited", Stdout: domain.CapturedLog{Text: string(secret), CapturedBytes: int64(len(secret))},
	}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "secure-bundle")
	if _, err := CreateBundle(ctx, storage, dataDir, bundle, "security-exclusions"); err != nil {
		t.Fatal(err)
	}
	directory, err := openExactPrivateDirectory(bundle)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := walkSecureTree(ctx, directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(tree.files))
	for path := range tree.files {
		files = append(files, path)
	}
	sort.Strings(files)
	if want := []string{"crewfold.db", "manifest.json", "manifest.sha256"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("bundle files = %v, want %v", files, want)
	}
	for _, path := range files {
		content, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, secret) {
			t.Fatalf("bundle file %s contains excluded operational secret", path)
		}
	}
}

func TestInspectOfflineGivesStableBaselineCanonicalAndDerivedGuidance(t *testing.T) {
	tests := []struct {
		name        string
		corrupt     func(*testing.T, *sql.DB)
		wantStatus  string
		wantCode    string
		remediation string
	}{
		{name: "baseline", wantStatus: "failed", wantCode: CodeCurrentBaselineMismatch, remediation: "restore_verified_backup", corrupt: func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`DROP TRIGGER schema_baseline_reject_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE schema_baseline SET source_sha256=?`, strings.Repeat("f", 64)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "canonical unknown event", wantStatus: "failed", wantCode: CodeCanonicalIntegrityFailed, remediation: "restore_verified_backup", corrupt: func(t *testing.T, database *sql.DB) {
			_, err := database.Exec(`INSERT INTO events(event_id,type,schema_version,occurred_at,recorded_at,actor_id,actor_type,workspace_id,entity_type,entity_id,entity_revision,correlation_id,causation_id,data_json)
SELECT 'event_m20_unknown','m20.unknown',schema_version,occurred_at,recorded_at,actor_id,actor_type,workspace_id,entity_type,entity_id,entity_revision,'m20-unknown',event_id,'{}' FROM events ORDER BY sequence LIMIT 1`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "derived index", wantStatus: "degraded", wantCode: "derived_knowledge_index", remediation: "rebuild_derived_index", corrupt: func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`UPDATE knowledge_search_metadata SET source_count=source_count+1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			dataDir := createRepairTargetWithWorkspace(t)
			database, err := sql.Open("sqlite3", filepath.Join(dataDir, "crewfold.db"))
			if err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			before := readPresentRepairFiles(t, dataDir)
			report, err := InspectOffline(context.Background(), dataDir)
			if err != nil {
				t.Fatalf("InspectOffline() error = %v", err)
			}
			if report.Status != test.wantStatus {
				t.Fatalf("InspectOffline() status = %q, want %q; report = %#v", report.Status, test.wantStatus, report)
			}
			found := false
			for _, finding := range report.Findings {
				if finding.Code == test.wantCode && finding.Remediation == test.remediation {
					found = true
				}
			}
			if !found {
				t.Fatalf("InspectOffline() findings = %#v, want %s/%s", report.Findings, test.wantCode, test.remediation)
			}
			after := readPresentRepairFiles(t, dataDir)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("offline repair mutated source bytes: before=%v after=%v", repairFileSizes(before), repairFileSizes(after))
			}
		})
	}
}

func createRepairTargetWithWorkspace(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(context.Background(), dataDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.InitWorkspace(context.Background(), store.InitWorkspaceCommand{Name: "repair-guidance", IdempotencyKey: "repair-guidance", CorrelationID: "repair-guidance"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func newAssignedRecoveryFixture(t *testing.T) (*store.Store, string, string, domain.TaskDetail, domain.Checkout) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{
		RuntimeNodeID: strings.Repeat("1", 32), RuntimeNodeFingerprint: strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{Name: "quiescence", IdempotencyKey: "quiescence-workspace", CorrelationID: "quiescence-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	checkoutPath := filepath.Join(t.TempDir(), "checkout")
	project, err := storage.RegisterProject(ctx, store.RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "quiescence", IdempotencyKey: "quiescence-project", CorrelationID: "quiescence-project",
		Observation: domain.CheckoutObservation{
			Path: checkoutPath, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
			Branch: "main", HeadCommit: "2222222222222222222222222222222222222222", GitDir: filepath.Join(checkoutPath, ".git"), GitCommonDir: filepath.Join(checkoutPath, ".git"),
			Repository: domain.RepositoryObservation{Fingerprint: "git_1111111111111111111111111111111111111111111111111111111111111111", ObjectFormat: "sha1", RootCommits: []string{"0000000000000000000000000000000000000000"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "runner", Role: "descriptive-only", Provider: "fake", Runtime: "fake", IdempotencyKey: "quiescence-agent", CorrelationID: "quiescence-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := storage.CreateTask(ctx, store.CreateTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID, Title: "quiescence task", Priority: 100, IdempotencyKey: "quiescence-task", CorrelationID: "quiescence-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := storage.AssignTask(ctx, store.AssignTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TaskID: created.Detail.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: created.Detail.Task.Revision, IdempotencyKey: "quiescence-assignment", CorrelationID: "quiescence-assignment",
	})
	if err != nil {
		t.Fatal(err)
	}
	return storage, dataDir, workspace.Workspace.ID, assigned.Detail, project.Checkout
}
