package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func TestContextDeltaCanonicalProvenanceRejectsSelfHashedRawPayloads(t *testing.T) {
	t.Parallel()

	t.Run("fabricated knowledge body", func(t *testing.T) {
		dataDir := t.TempDir()
		storage := openTestStore(t, dataDir, Options{})
		workspace, _, _, run := startContextDeltaTestRun(t, storage, "forged knowledge delta")
		accepted := acceptContextDecision(t, storage, workspace.ID, run.TaskID,
			"forged-delta-knowledge", "Canonical decision", run.TaskID)
		forged := accepted.Revision
		forged.Body = "A raw writer fabricated this authority."
		forged.ContentHash = knowledgeContentHash(forged.Title, forged.Body)
		change := domain.ContextDeltaChange{
			Kind: domain.ContextDeltaKnowledgeAccepted, EntityType: "knowledge_revision",
			EntityID: forged.ID, Revision: forged.StateRevision,
			Cause:     domain.ContextDeltaCause{EventSequence: accepted.EventSequence, Reason: "accepted applicable decision"},
			Knowledge: &forged,
		}
		delta := insertRawContextDeltaForTest(t, storage, run, change,
			"cdelta_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true)

		assertCorruptContextDeltaChain(t, storage, workspace.ID, run.ID, delta.ID)
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(context.Background(), dataDir, Options{})
		if err != nil {
			t.Fatalf("Open(after forged delta) error = %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		if _, err := reopened.FetchRunContextDelta(context.Background(), run.ID); ErrorCode(err) != CodeStorageFailed {
			t.Fatalf("FetchRunContextDelta(after restart) error=%v code=%s", err, ErrorCode(err))
		}
	})

	t.Run("fabricated message preview", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, project, agent, run := startContextDeltaTestRun(t, storage, "forged message delta")
		sent, err := storage.SendMessage(context.Background(), SendMessageCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, RecipientAgent: agent.ID,
			Kind: domain.MessageInform, Body: "Canonical immutable message body.",
			IdempotencyKey: "send-forged-preview-source", CorrelationID: "request-send-forged-preview-source",
		})
		if err != nil {
			t.Fatal(err)
		}
		preview := domain.InboxSummaryItem{
			MessageID: sent.Value.Message.ID, ThreadID: sent.Value.Message.ThreadID, Kind: sent.Value.Message.Kind,
			SenderAgentID: sent.Value.Message.SenderAgentID, SenderAgentName: sent.Value.Message.SenderAgentName,
			BodyPreview: "Fabricated instructions from a raw writer.", Status: domain.DeliveryQueued,
			CreatedAt: sent.Value.Message.CreatedAt,
		}
		change := domain.ContextDeltaChange{
			Kind: domain.ContextDeltaMessageReceived, EntityType: "message", EntityID: preview.MessageID, Revision: 1,
			Cause:   domain.ContextDeltaCause{EventSequence: sent.EventSequence, Reason: "authorized unseen message sent to this run"},
			Message: &preview,
		}
		delta := insertRawContextDeltaForTest(t, storage, run, change,
			"cdelta_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", true)
		assertCorruptContextDeltaChain(t, storage, workspace.ID, run.ID, delta.ID)
	})
}

func TestContextDeltaOrphanRowMakesShowAndExplainFailClosed(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, run := startContextDeltaTestRun(t, storage, "orphan delta")
	accepted := acceptContextDecision(t, storage, workspace.ID, run.TaskID,
		"orphan-delta-knowledge", "Orphan decision", run.TaskID)
	canonical := accepted.Revision
	change := domain.ContextDeltaChange{
		Kind: domain.ContextDeltaKnowledgeAccepted, EntityType: "knowledge_revision",
		EntityID: canonical.ID, Revision: canonical.StateRevision,
		Cause:     domain.ContextDeltaCause{EventSequence: accepted.EventSequence, Reason: "accepted applicable decision"},
		Knowledge: &canonical,
	}
	delta := insertRawContextDeltaForTest(t, storage, run, change,
		"cdelta_cccccccccccccccccccccccccccccccc", false)

	if _, err := storage.ContextDelta(context.Background(), workspace.ID, delta.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("ContextDelta(orphan) error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := storage.ExplainContextDelta(context.Background(), workspace.ID, delta.ID); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("ExplainContextDelta(orphan) error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := storage.ListContextDeltas(context.Background(), ListContextDeltasQuery{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, Limit: 10,
	}); ErrorCode(err) != CodeStorageFailed {
		t.Fatalf("ListContextDeltas(orphan) error=%v code=%s", err, ErrorCode(err))
	}
}

func TestContextDeltaDependentHistorySurvivesLaterTaskUpdate(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, _, run := startContextDeltaTestRun(t, storage, "dependent history")
	dependent := createWorkTestTask(t, storage, workspace.ID, project.ID, "Original dependent title", "create-dependent-history")
	linked, err := storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: dependent.Task.ID, DependsOnTaskID: run.TaskID,
		ExpectedRevision: dependent.Task.Revision, IdempotencyKey: "link-dependent-history", CorrelationID: "request-link-dependent-history",
	})
	if err != nil {
		t.Fatal(err)
	}
	preRefreshTitle := "Title updated in the edge-add window"
	linked, err = storage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: dependent.Task.ID, ExpectedRevision: linked.Detail.Task.Revision,
		Title: &preRefreshTitle, IdempotencyKey: "update-dependent-before-first-refresh", CorrelationID: "request-update-dependent-before-first-refresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-dependent-history-added", CorrelationID: "request-refresh-dependent-history-added",
	})
	if err != nil || created.Delta == nil || len(created.Delta.Changes) != 1 ||
		created.Delta.Changes[0].Kind != domain.ContextDeltaDependentAdded || created.Delta.Changes[0].Dependency == nil ||
		created.Delta.Changes[0].Dependency.Title != preRefreshTitle || created.Delta.Changes[0].Dependency.Status != domain.TaskReady {
		t.Fatalf("RefreshContext(dependent added)=%#v,%v", created, err)
	}
	first := *created.Delta
	if _, err := storage.AcknowledgeRunContextDelta(context.Background(), AcknowledgeContextDeltaCommand{
		RunID: run.ID, DeltaID: first.ID, ExpectedSequence: first.Sequence, IdempotencyKey: "ack-dependent-history-added",
	}); err != nil {
		t.Fatal(err)
	}
	updatedTitle := "Updated dependent title after acknowledgement"
	updated, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: dependent.Task.ID, ExpectedRevision: linked.Detail.Task.Revision,
		Title: &updatedTitle, IdempotencyKey: "update-dependent-history", CorrelationID: "request-update-dependent-history",
	})
	if err != nil {
		t.Fatal(err)
	}
	shown, err := storage.ContextDelta(context.Background(), workspace.ID, first.ID)
	if err != nil || shown.ID != first.ID || shown.Changes[0].Dependency == nil || shown.Changes[0].Dependency.Title != preRefreshTitle {
		t.Fatalf("ContextDelta(old dependent snapshot)=%#v,%v", shown, err)
	}
	listed, err := storage.ListContextDeltas(context.Background(), ListContextDeltasQuery{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID, Limit: 10,
	})
	if err != nil || len(listed.Deltas) != 1 || listed.Deltas[0].ID != first.ID {
		t.Fatalf("ListContextDeltas(old dependent snapshot)=%#v,%v", listed, err)
	}
	next, err := storage.RefreshContext(context.Background(), RefreshContextCommand{
		WorkspaceIdentifier: workspace.ID, RunID: run.ID,
		IdempotencyKey: "refresh-dependent-history-updated", CorrelationID: "request-refresh-dependent-history-updated",
	})
	if err != nil || next.Delta == nil || len(next.Delta.Changes) != 1 ||
		next.Delta.Changes[0].Kind != domain.ContextDeltaDependentUpdated || next.Delta.Changes[0].Dependency == nil ||
		next.Delta.Changes[0].Dependency.Title != updated.Detail.Task.Title || next.Delta.Changes[0].Dependency.Revision != updated.Detail.Task.Revision {
		t.Fatalf("RefreshContext(dependent updated)=%#v,%v", next, err)
	}
}

func insertRawContextDeltaForTest(t *testing.T, storage *Store, run domain.Run, change domain.ContextDeltaChange, deltaID string, pending bool) domain.ContextDelta {
	t.Helper()
	ctx := context.Background()
	state, err := dbgen.New(storage.db).GetRunContextDeltaState(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cutoff int64
	if err := storage.db.QueryRow("SELECT COALESCE(MAX(sequence), 0) FROM events").Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	now := storage.nowText()
	delta := domain.ContextDelta{
		Schema: domain.ContextDeltaSchema, ID: deltaID, RunID: run.ID, ContextPacketID: run.ContextPacketID,
		WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, TaskID: run.TaskID, AgentID: run.AgentID,
		BasePacketSchema: domain.ContextPacketSchema, Sequence: state.LastSequence + 1,
		ParentDeltaID: pointerValue(state.LastDeltaID), FromEventSequence: state.ScanEventSequence,
		ThroughEventSequence: cutoff, EvaluatedAt: now, Changes: []domain.ContextDeltaChange{change},
		Included: contextDeltaSelections([]domain.ContextDeltaChange{change}), Excluded: []domain.ContextExclusion{},
		CreatedAt: now, CreatedBy: localOwnerActorID,
	}
	deltaJSON, err := finalizeContextDelta(&delta, int(state.CumulativeByteSize))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	builtSequence, err := appendEvent(ctx, tx, run.WorkspaceID, "context_delta", delta.ID, delta.Sequence,
		contextDeltaBuiltEvent, "raw-"+delta.ID, now, map[string]any{
			"run_id": run.ID, "context_packet_id": run.ContextPacketID, "sequence": delta.Sequence,
			"state_revision": state.Revision + 1, "parent_delta_id": delta.ParentDeltaID,
			"from_event_sequence": delta.FromEventSequence, "through_event_sequence": delta.ThroughEventSequence,
			"content_hash": delta.ContentHash, "byte_size": delta.ByteSize, "change_count": 1,
			"change_kinds": []string{change.Kind},
		})
	if err != nil {
		t.Fatal(err)
	}
	queries := dbgen.New(tx)
	if err := queries.InsertContextDelta(ctx, dbgen.InsertContextDeltaParams{
		ID: delta.ID, RunID: run.ID, ContextPacketID: run.ContextPacketID, Sequence: delta.Sequence,
		ParentDeltaID: optionalStringPointer(delta.ParentDeltaID), FromEventSequence: delta.FromEventSequence,
		ThroughEventSequence: delta.ThroughEventSequence, DeltaJson: string(deltaJSON), ContentHash: delta.ContentHash,
		ByteSize: int64(delta.ByteSize), BuiltEventSequence: builtSequence, CreatedAt: now, CreatedBy: localOwnerActorID,
	}); err != nil {
		t.Fatalf("insert self-hashed raw delta: %v", err)
	}
	if pending {
		rows, err := queries.MarkRunContextDeltaPending(ctx, dbgen.MarkRunContextDeltaPendingParams{
			ScanEventSequence: cutoff, LastSequence: delta.Sequence, LastDeltaID: &delta.ID, PendingDeltaID: &delta.ID,
			CumulativeByteSize: int64(delta.ByteSize), UpdatedAt: now, RunID: run.ID, ContextPacketID: run.ContextPacketID,
			Revision: state.Revision, ScanEventSequence_2: state.ScanEventSequence, LastSequence_2: state.LastSequence,
		})
		if err != nil || rows != 1 {
			t.Fatalf("advance raw delta state rows=%d error=%v", rows, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return delta
}

func assertCorruptContextDeltaChain(t *testing.T, storage *Store, workspaceID, runID, deltaID string) {
	t.Helper()
	ctx := context.Background()
	checks := []struct {
		name string
		call func() error
	}{
		{"fetch", func() error { _, err := storage.FetchRunContextDelta(ctx, runID); return err }},
		{"list", func() error {
			_, err := storage.ListContextDeltas(ctx, ListContextDeltasQuery{WorkspaceIdentifier: workspaceID, RunID: runID, Limit: 10})
			return err
		}},
		{"show", func() error { _, err := storage.ContextDelta(ctx, workspaceID, deltaID); return err }},
		{"explain", func() error { _, err := storage.ExplainContextDelta(ctx, workspaceID, deltaID); return err }},
		{"refresh", func() error {
			_, err := storage.RefreshContext(ctx, RefreshContextCommand{WorkspaceIdentifier: workspaceID, RunID: runID,
				IdempotencyKey: "refresh-corrupt-" + deltaID, CorrelationID: "request-refresh-corrupt-" + deltaID})
			return err
		}},
		{"acknowledge", func() error {
			_, err := storage.AcknowledgeRunContextDelta(ctx, AcknowledgeContextDeltaCommand{
				RunID: runID, DeltaID: deltaID, ExpectedSequence: 1, IdempotencyKey: "ack-corrupt-" + deltaID})
			return err
		}},
	}
	for _, check := range checks {
		if err := check.call(); ErrorCode(err) != CodeStorageFailed {
			t.Fatalf("%s corrupt delta error=%v code=%s", check.name, err, ErrorCode(err))
		}
	}
}
