package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestPortableKnowledgeExportIsExactAndRoundTripsTaskScope(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "portable source", "portable-source")
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, task.Task.ID, "portable-proposal", "Portable contract", "Task-scoped canonical bytes survive a round trip", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationSupported)
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposed.Revision.ID, ExpectedStateRevision: 1,
		DecisionNote: "ship", Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-accept", CorrelationID: "portable-accept-request",
	})
	if err != nil {
		t.Fatalf("AcceptKnowledge() error = %v", err)
	}
	first, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatalf("ExportKnowledgeBundle() error = %v", err)
	}
	second, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || !bytes.Equal(first.ManifestJSON, second.ManifestJSON) || !bytes.Equal(first.Markdown, second.Markdown) || first.AsOfEventSequence != second.AsOfEventSequence {
		t.Fatalf("repeated export differs: %#v, %v", second, err)
	}
	if first.Manifest.BundleID != "kbun_"+first.Manifest.ContentSHA256[:32] || first.Manifest.Snapshot.Items[0].Item.TaskScopeID != task.Task.ID {
		t.Fatalf("manifest = %#v", first.Manifest)
	}

	destination := openTestStore(t, t.TempDir(), Options{})
	result, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: first.ManifestJSON, Markdown: first.Markdown,
		ExpectedContentSHA256: first.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-import", CorrelationID: "portable-import-request",
	})
	if err != nil {
		t.Fatalf("ImportKnowledgeBundle() error = %v", err)
	}
	if !result.Created.Workspace || !result.Created.Project || result.Created.TaskScopeAnchors != 1 || result.Replayed {
		t.Fatalf("import result = %#v", result)
	}
	imported, err := destination.KnowledgeRevision(context.Background(), workspace.ID, accepted.Revision.ID)
	if err != nil || !reflect.DeepEqual(imported, accepted.Revision) {
		t.Fatalf("imported revision = %#v, %v; want %#v", imported, err, accepted.Revision)
	}
	if _, err := destination.db.Exec(`INSERT INTO knowledge_sources(revision_id,ordinal,source_type,source_id,source_revision,role) VALUES(?,1,'task',?,1,'supporting')`, imported.ID, "task_ffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("imported knowledge sources were not sealed")
	}
	roundTrip, err := destination.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || !bytes.Equal(roundTrip.ManifestJSON, first.ManifestJSON) || !bytes.Equal(roundTrip.Markdown, first.Markdown) {
		t.Fatalf("round-trip export differs: %v\n%s\n%s", err, roundTrip.ManifestJSON, first.ManifestJSON)
	}
	replay, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.Name, ProjectIdentifier: project.Name, ManifestJSON: first.ManifestJSON, Markdown: first.Markdown,
		ExpectedContentSHA256: first.Manifest.ContentSHA256, CreateScope: false, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-import-new-key", CorrelationID: "portable-import-replay-request",
	})
	if err != nil || !replay.Replayed || replay.Receipt.ID != result.Receipt.ID || replay.Created != (KnowledgeBundleImportCreated{}) {
		t.Fatalf("exact receipt replay = %#v, %v", replay, err)
	}
	aliasReplay, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.Name, ProjectIdentifier: project.Name, ManifestJSON: first.ManifestJSON, Markdown: first.Markdown,
		ExpectedContentSHA256: first.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-import", CorrelationID: "portable-import-alias-replay-request",
	})
	if err != nil || !aliasReplay.Replayed || aliasReplay.Receipt.ID != result.Receipt.ID {
		t.Fatalf("same-key ID/name alias replay = %#v, %v", aliasReplay, err)
	}
	_, err = destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: first.ManifestJSON, Markdown: first.Markdown,
		ExpectedContentSHA256: first.Manifest.ContentSHA256, CreateScope: false, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-import", CorrelationID: "portable-import-changed-request",
	})
	if ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("same import key changed request error = %v, code %s", err, ErrorCode(err))
	}

	listed, err := destination.ListKnowledge(context.Background(), ListKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskScopeID: "task_ffffffffffffffffffffffffffffffff"})
	if err != nil || len(listed) != 0 {
		t.Fatalf("task-scope non-leak list = %#v, %v", listed, err)
	}
}

func TestPortableKnowledgeImportRejectsTamperingWithoutWrites(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "portable tamper", "portable-tamper-source")
	proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "portable-tamper-proposal", "Tamper contract", "Canonical manifest bytes are strict", "")
	exported, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}

	destination := openTestStore(t, t.TempDir(), Options{})
	before := portableWriteCounts(t, destination)
	badMarkdown := append([]byte(nil), exported.Markdown...)
	badMarkdown = append(badMarkdown, 'x')
	_, err = destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: badMarkdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(), IdempotencyKey: "tampered-import", CorrelationID: "tampered-import-request",
	})
	if ErrorCode(err) != CodeKnowledgeBundleDigestMismatch || portableWriteCounts(t, destination) != before {
		t.Fatalf("tampered import error/counts = %v/%v, want digest mismatch/%v", err, portableWriteCounts(t, destination), before)
	}

	var manifest domain.PortableKnowledgeBundleManifest
	if err := json.Unmarshal(exported.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Snapshot.Items[0].Revisions[0].StateRevision = 99
	malformed, _ := canonicalJSONLine(manifest)
	_, err = destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: malformed, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(), IdempotencyKey: "malformed-import", CorrelationID: "malformed-import-request",
	})
	if ErrorCode(err) != CodeInvalidKnowledgeBundle || portableWriteCounts(t, destination) != before {
		t.Fatalf("malformed import error/counts = %v/%v, want invalid/%v", err, portableWriteCounts(t, destination), before)
	}
}

func TestPortableKnowledgeImportRollbackResetsRestoreGate(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable rollback", "portable-rollback-source")
	proposeTaskKnowledge(t, source, workspace.ID, task.Task.ID, "portable-rollback-proposal", "Rollback contract", "All import writes roll back together", "")
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupt import")
	destination := openTestStore(t, t.TempDir(), Options{MutationHook: func(stage string) error {
		if stage == MutationAfterProjection {
			return injected
		}
		return nil
	}})
	_, err = destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(), IdempotencyKey: "rollback-import", CorrelationID: "rollback-import-request",
	})
	if !errors.Is(err, injected) || destination.restoreActive.Load() || portableWriteCounts(t, destination) != (portableCounts{}) {
		t.Fatalf("rollback/gate = %v/%v/%#v", err, destination.restoreActive.Load(), portableWriteCounts(t, destination))
	}
}

func TestPortableKnowledgeRestoreGateDoesNotLeakToConcurrentConnections(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable concurrent restore", "portable-concurrent-task")
	proposeSearchKnowledge(t, source, workspace.ID, task.Task.ID, task.Task.ID, "portable-concurrent-proposal", "Concurrent restore", "The restore gate is transaction bounded", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationSupported)
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	projectionReady, releaseImport := make(chan struct{}), make(chan struct{})
	destination := openTestStore(t, t.TempDir(), Options{MutationHook: func(stage string) error {
		if stage == MutationAfterProjection {
			close(projectionReady)
			<-releaseImport
		}
		return nil
	}})
	importDone := make(chan error, 1)
	go func() {
		_, importErr := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
			ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: "portable-concurrent-import", CorrelationID: "portable-concurrent-import-request",
		})
		importDone <- importErr
	}()
	<-projectionReady
	const attempts = 16
	forged := make(chan error, attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			_, execErr := destination.db.Exec(`INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?, 'task_scope_anchor', ?, NULL, ?)`,
				"kimp_"+fmt.Sprintf("%032x", index+1), task.Task.ID, destination.nowText())
			forged <- execErr
		}(index)
	}
	close(releaseImport)
	if err := <-importDone; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < attempts; index++ {
		if err := <-forged; err == nil {
			t.Fatal("concurrent forged import audit acquired a released connection while the restore gate was active")
		}
	}
	if destination.restoreActive.Load() {
		t.Fatal("restore gate remained active after successful commit")
	}
}

func TestPortableKnowledgeDatabaseSealsImportAuditAndApplicability(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "portable sealing", "portable-sealing-task")
	proposed := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "portable-sealing-proposal", "Sealed", "Import audit and applicability are append-only", "")
	if _, err := storage.db.Exec(`INSERT INTO knowledge_item_task_scopes(item_id,task_id) VALUES(?,?)`, proposed.Revision.ItemID, task.Task.ID); err == nil {
		t.Fatal("late task-scope binding unexpectedly persisted")
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES('kimp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','knowledge_item',?,NULL,?)`, proposed.Revision.ItemID, storage.nowText()); err == nil {
		t.Fatal("import audit row outside restore gate unexpectedly persisted")
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_imports(id,bundle_id,workspace_id,project_id,content_sha256,rendering_sha256,manifest_json,markdown,idempotency_key,request_hash,imported_at,imported_by,imported_by_type,created_workspace,created_project,created_task_scope_anchors,completed_event_sequence)
VALUES('kimp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','kbun_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?,?, ?,X'7B7D',X'', 'forged',?,?,'local-owner','human',0,0,0,(SELECT MAX(sequence) FROM events))`,
		workspace.ID, project.ID, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), storage.nowText()); err == nil {
		t.Fatal("import receipt outside restore gate unexpectedly persisted")
	}
}

type portableCounts struct{ workspaces, projects, items, revisions, imports, events int }

func portableWriteCounts(t *testing.T, storage *Store) portableCounts {
	t.Helper()
	var counts portableCounts
	if err := storage.db.QueryRow(`SELECT
(SELECT COUNT(*) FROM workspaces),(SELECT COUNT(*) FROM projects),(SELECT COUNT(*) FROM knowledge_items),
(SELECT COUNT(*) FROM knowledge_revisions),(SELECT COUNT(*) FROM knowledge_imports),(SELECT COUNT(*) FROM events)`).Scan(
		&counts.workspaces, &counts.projects, &counts.items, &counts.revisions, &counts.imports, &counts.events); err != nil {
		t.Fatal(err)
	}
	return counts
}
