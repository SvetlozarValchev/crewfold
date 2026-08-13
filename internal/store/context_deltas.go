package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	contextDeltaBuiltEvent          = "context_delta.built"
	contextDeltaAcknowledgedEvent   = "context_delta.acknowledged"
	contextDeltaRebaseRequiredEvent = "context_delta.rebase_required"
	maximumWithdrawalContradictions = 16
)

const knownContextTaskEventTypesSQL = `'task.created','task.updated','task.dependency_added','task.readied',
'task.assigned','task.assignment_expired','task.started','task.blocked','task.completion_proposed',
'task.changes_requested','task.completed','task.failed','task.cancelled','task.run_stopped',
'task.handoff_recorded','task.reassigned','task.role_designated'`

type effectiveContextState struct {
	messages       map[string]bool
	knowledge      map[string]domain.KnowledgeRevision
	delivered      map[string]domain.KnowledgeRevision
	withdrawn      map[string]string
	suppressed     map[string]bool
	contradictions map[string]domain.ContextContradictionSnapshot
	dependencies   map[string]domain.ContextDependency
	dependents     map[string]domain.ContextDependency
	threads        map[string]domain.ParticipantThread
}

func (s *Store) RefreshContext(ctx context.Context, command RefreshContextCommand) (domain.ContextRefreshResult, error) {
	workspaceIdentifier := strings.TrimSpace(command.WorkspaceIdentifier)
	runID := strings.TrimSpace(command.RunID)
	key := strings.TrimSpace(command.IdempotencyKey)
	correlationID := strings.TrimSpace(command.CorrelationID)
	if workspaceIdentifier == "" || runID == "" {
		return domain.ContextRefreshResult{}, &Error{Code: CodeInvalidContextDelta, Message: "context refresh requires workspace and run"}
	}
	if err := validateMutationMetadata(key, correlationID, CodeInvalidContextDelta); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ContextRefreshResult{}, storageFailure("begin context refresh", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, workspaceIdentifier)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	run, err := queryRun(ctx, tx, workspace.ID, runID)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	requestHash, err := hashCommand("context.refresh", map[string]string{"workspace_id": workspace.ID, "run_id": run.ID})
	if err != nil {
		return domain.ContextRefreshResult{}, storageFailure("hash context refresh", err)
	}
	packet, err := queryContextPacket(ctx, tx, workspace.ID, run.ContextPacketID)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	scopedKey := "context-refresh:" + localOwnerActorID + ":" + key
	var replay domain.ContextRefreshResult
	if found, err := lookupIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, &replay); err != nil {
		return domain.ContextRefreshResult{}, err
	} else if found {
		if packet.Schema == domain.ContextPacketSchema {
			state, stateErr := dbgen.New(tx).GetRunContextDeltaState(ctx, run.ID)
			if stateErr != nil {
				return domain.ContextRefreshResult{}, contextDeltaStateError(packet, stateErr)
			}
			if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
				return domain.ContextRefreshResult{}, err
			}
		}
		replay.Replayed = true
		return replay, nil
	}
	if !runCanUseMailbox(run.Status) {
		return domain.ContextRefreshResult{}, &Error{Code: CodeInvalidContextDelta, Message: "context can refresh only for a live run"}
	}
	if packet.Schema != domain.ContextPacketSchema {
		result := domain.ContextRefreshResult{Status: domain.ContextRefreshRebaseRequired, RunID: run.ID,
			ContextPacketID: packet.ID, RebaseReason: domain.ContextRebaseUnsupportedPacket,
			Chain: domain.ContextDeltaChain{RunID: run.ID, ContextPacketID: packet.ID, RebaseReason: domain.ContextRebaseUnsupportedPacket}}
		if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, s.nowText()); err != nil {
			return domain.ContextRefreshResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextRefreshResult{}, storageFailure("commit legacy context refresh", err)
		}
		return result, nil
	}
	queries := dbgen.New(tx)
	state, err := queries.GetRunContextDeltaState(ctx, run.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContextRefreshResult{}, storageFailure("query run context delta state", errors.New("version-four run has no delta state"))
	}
	if err != nil {
		return domain.ContextRefreshResult{}, storageFailure("query run context delta state", err)
	}
	if state.ContextPacketID != packet.ID {
		return domain.ContextRefreshResult{}, storageFailure("validate run context delta state", errors.New("delta state packet binding differs"))
	}
	if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if state.Status == "pending_ack" {
		delta, err := contextDeltaByID(ctx, queries, pointerValue(state.PendingDeltaID))
		if err != nil {
			return domain.ContextRefreshResult{}, err
		}
		if delta.Budget.Chain.UsedBytes != int(state.CumulativeByteSize) {
			return domain.ContextRefreshResult{}, storageFailure("validate pending context delta chain budget", errors.New("delta and state cumulative bytes differ"))
		}
		result := refreshResult(packet, state, domain.ContextRefreshPending, state.ScanEventSequence, &delta)
		if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, s.nowText()); err != nil {
			return domain.ContextRefreshResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextRefreshResult{}, storageFailure("commit pending context refresh", err)
		}
		return result, nil
	}
	if state.Status == "rebase_required" {
		result := refreshResult(packet, state, domain.ContextRefreshRebaseRequired, state.ScanEventSequence, nil)
		scannedFrom, err := contextRebaseScannedFrom(ctx, tx, state)
		if err != nil {
			return domain.ContextRefreshResult{}, err
		}
		result.ScannedFromEventSequence = scannedFrom
		result.EventSequence = pointerValue(state.RebaseEventSequence)
		if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, s.nowText()); err != nil {
			return domain.ContextRefreshResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextRefreshResult{}, storageFailure("commit rebase context refresh", err)
		}
		return result, nil
	}
	var cutoff int64
	if cutoff, err = dbgen.New(tx).GetContextEventCursor(ctx); err != nil {
		return domain.ContextRefreshResult{}, storageFailure("read context refresh event cursor", err)
	}
	evaluatedAt := s.nowText()
	priorDeltas, err := queries.ListAllRunContextDeltas(ctx, run.ID)
	if err != nil {
		return domain.ContextRefreshResult{}, storageFailure("list context delta chain", err)
	}
	effective, err := foldEffectiveContext(packet, priorDeltas)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if rebaseReason, err := s.validateContextBaseContract(ctx, tx, run, packet); err != nil {
		return domain.ContextRefreshResult{}, err
	} else if rebaseReason != "" {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, rebaseReason, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	events, overflow, err := relevantContextEvents(ctx, tx, run, effective, state.ScanEventSequence, cutoff, evaluatedAt)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if overflow {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, domain.ContextRebaseEventWindowExceeded, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	changes, rebaseReason, err := s.projectContextChanges(ctx, tx, run, packet, effective, events, cutoff, evaluatedAt)
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if rebaseReason != "" {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, rebaseReason, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	now := evaluatedAt
	if len(changes) == 0 {
		scannedFrom := state.ScanEventSequence
		if cutoff > state.ScanEventSequence {
			rows, err := queries.AdvanceRunContextDeltaScan(ctx, dbgen.AdvanceRunContextDeltaScanParams{
				ScanEventSequence: cutoff, UpdatedAt: now, RunID: run.ID, ContextPacketID: packet.ID,
				Revision: state.Revision, ScanEventSequence_2: state.ScanEventSequence,
			})
			if err != nil || rows != 1 {
				if err == nil {
					err = errors.New("context scan cursor was concurrently changed")
				}
				return domain.ContextRefreshResult{}, storageFailure("advance context refresh cursor", err)
			}
			state.Revision++
			state.ScanEventSequence = cutoff
		}
		result := refreshResult(packet, state, domain.ContextRefreshUpToDate, state.ScanEventSequence, nil)
		result.ScannedFromEventSequence = scannedFrom
		if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, now); err != nil {
			return domain.ContextRefreshResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextRefreshResult{}, storageFailure("commit up-to-date context refresh", err)
		}
		return result, nil
	}
	// One relevant fact can expand into more than one closed-union change (for
	// example a participant message plus its authorization roster, or a
	// contradiction plus quarantine markers). Classify that bounded expansion
	// as a durable size rebase before finalization rather than leaking an
	// internal validation failure.
	if len(changes) > maximumContextDeltaEvents {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, domain.ContextRebaseDeltaLimitExceeded, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	deltaID, err := randomID("cdelta_")
	if err != nil {
		return domain.ContextRefreshResult{}, storageFailure("generate context delta id", err)
	}
	delta := domain.ContextDelta{
		Schema: domain.ContextDeltaSchema, ID: deltaID, RunID: run.ID, ContextPacketID: packet.ID,
		WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, TaskID: run.TaskID, AgentID: run.AgentID,
		BasePacketSchema: packet.Schema, Sequence: state.LastSequence + 1,
		ParentDeltaID: pointerValue(state.LastDeltaID), FromEventSequence: state.ScanEventSequence,
		ThroughEventSequence: cutoff, EvaluatedAt: now, Changes: changes,
		Included: contextDeltaSelections(changes), Excluded: make([]domain.ContextExclusion, 0),
		CreatedAt: now, CreatedBy: localOwnerActorID,
	}
	deltaJSON, err := finalizeContextDelta(&delta, int(state.CumulativeByteSize))
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if delta.ByteSize > packet.LiveContext.PerDeltaLimitBytes {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, domain.ContextRebaseDeltaLimitExceeded, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	if int(state.CumulativeByteSize)+delta.ByteSize > packet.LiveContext.CumulativeDeltaLimitBytes {
		return s.commitContextRebase(ctx, tx, queries, packet, state, cutoff, domain.ContextRebaseCumulativeLimitExceeded, scopedKey, requestHash, correlationID, evaluatedAt)
	}
	changeKinds := make([]string, 0, len(delta.Changes))
	for _, change := range delta.Changes {
		changeKinds = append(changeKinds, change.Kind)
	}
	builtSequence, err := appendEvent(ctx, tx, run.WorkspaceID, "context_delta", delta.ID, delta.Sequence, contextDeltaBuiltEvent, correlationID, now, map[string]any{
		"run_id": run.ID, "context_packet_id": packet.ID, "sequence": delta.Sequence, "state_revision": state.Revision + 1,
		"parent_delta_id": delta.ParentDeltaID, "from_event_sequence": delta.FromEventSequence,
		"through_event_sequence": delta.ThroughEventSequence, "content_hash": delta.ContentHash,
		"byte_size": delta.ByteSize, "change_count": len(delta.Changes), "change_kinds": changeKinds,
	})
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if err := queries.InsertContextDelta(ctx, dbgen.InsertContextDeltaParams{
		ID: delta.ID, RunID: run.ID, ContextPacketID: packet.ID, Sequence: delta.Sequence,
		ParentDeltaID: optionalStringPointer(delta.ParentDeltaID), FromEventSequence: delta.FromEventSequence,
		ThroughEventSequence: delta.ThroughEventSequence, DeltaJson: string(deltaJSON), ContentHash: delta.ContentHash,
		ByteSize: int64(delta.ByteSize), BuiltEventSequence: builtSequence, CreatedAt: now, CreatedBy: localOwnerActorID,
	}); err != nil {
		return domain.ContextRefreshResult{}, storageFailure("insert context delta", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	rows, err := queries.MarkRunContextDeltaPending(ctx, dbgen.MarkRunContextDeltaPendingParams{
		ScanEventSequence: cutoff, LastSequence: delta.Sequence, LastDeltaID: &delta.ID, PendingDeltaID: &delta.ID,
		CumulativeByteSize: int64(delta.ByteSize), UpdatedAt: now, RunID: run.ID, ContextPacketID: packet.ID,
		Revision: state.Revision, ScanEventSequence_2: state.ScanEventSequence, LastSequence_2: state.LastSequence,
	})
	if err != nil || rows != 1 {
		if err == nil {
			err = errors.New("context delta state was concurrently changed")
		}
		return domain.ContextRefreshResult{}, storageFailure("mark context delta pending", err)
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	state.Status, state.Revision, state.ScanEventSequence = "pending_ack", state.Revision+1, cutoff
	state.LastSequence, state.LastDeltaID, state.PendingDeltaID = delta.Sequence, &delta.ID, &delta.ID
	state.DeltaCount++
	state.CumulativeByteSize += int64(delta.ByteSize)
	result := refreshResult(packet, state, domain.ContextRefreshCreated, state.ScanEventSequence, &delta)
	result.EventSequence = builtSequence
	if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, now); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextRefreshResult{}, storageFailure("commit context refresh", err)
	}
	return result, nil
}

func (s *Store) ListContextDeltas(ctx context.Context, query ListContextDeltasQuery) (domain.ContextDeltaList, error) {
	query.WorkspaceIdentifier, query.RunID = strings.TrimSpace(query.WorkspaceIdentifier), strings.TrimSpace(query.RunID)
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.WorkspaceIdentifier == "" || query.RunID == "" || query.AfterSequence < 0 || query.Limit < 1 || query.Limit > 100 {
		return domain.ContextDeltaList{}, &Error{Code: CodeInvalidContextDelta, Message: "context delta list requires workspace, run, non-negative sequence, and limit from 1 to 100"}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ContextDeltaList{}, storageFailure("begin context delta list", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return domain.ContextDeltaList{}, err
	}
	run, err := queryRun(ctx, tx, workspace.ID, query.RunID)
	if err != nil {
		return domain.ContextDeltaList{}, err
	}
	packet, err := queryContextPacket(ctx, tx, workspace.ID, run.ContextPacketID)
	if err != nil {
		return domain.ContextDeltaList{}, err
	}
	if packet.Schema != domain.ContextPacketSchema {
		return domain.ContextDeltaList{}, &Error{Code: CodeInvalidContextDelta, Message: "context packet has no bounded live delta chain"}
	}
	state, err := dbgen.New(tx).GetRunContextDeltaState(ctx, run.ID)
	if err != nil {
		return domain.ContextDeltaList{}, contextDeltaStateError(packet, err)
	}
	if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
		return domain.ContextDeltaList{}, err
	}
	rows, err := dbgen.New(tx).ListRunContextDeltas(ctx, dbgen.ListRunContextDeltasParams{RunID: run.ID, Sequence: query.AfterSequence, Limit: int64(query.Limit + 1)})
	if err != nil {
		return domain.ContextDeltaList{}, storageFailure("list context deltas", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	deltas := make([]domain.ContextDelta, 0, len(rows))
	for _, row := range rows {
		delta, err := decodeContextDelta(row)
		if err != nil {
			return domain.ContextDeltaList{}, err
		}
		deltas = append(deltas, delta)
	}
	next := query.AfterSequence
	if len(deltas) > 0 {
		next = deltas[len(deltas)-1].Sequence
	}
	result := domain.ContextDeltaList{Chain: contextDeltaChain(packet, state), AfterSequence: query.AfterSequence, NextSequence: next, HasMore: hasMore, Deltas: deltas}
	if err := tx.Commit(); err != nil {
		return domain.ContextDeltaList{}, storageFailure("commit context delta list", err)
	}
	return result, nil
}

func (s *Store) ContextDelta(ctx context.Context, workspaceIdentifier, deltaID string) (domain.ContextDelta, error) {
	if !validContextDeltaID(deltaID) {
		return domain.ContextDelta{}, &Error{Code: CodeInvalidContextDelta, Message: "context delta ID is invalid"}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ContextDelta{}, storageFailure("begin context delta query", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ContextDelta{}, err
	}
	queries := dbgen.New(tx)
	row, err := queries.GetWorkspaceContextDeltaByID(ctx, dbgen.GetWorkspaceContextDeltaByIDParams{ID: deltaID, WorkspaceID: workspace.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContextDelta{}, &Error{Code: CodeContextDeltaNotFound, Message: fmt.Sprintf("context delta %q was not found", deltaID)}
	}
	if err != nil {
		return domain.ContextDelta{}, storageFailure("query context delta", err)
	}
	packet, err := queryContextPacket(ctx, tx, workspace.ID, row.ContextPacketID)
	if err != nil {
		return domain.ContextDelta{}, err
	}
	state, err := queries.GetRunContextDeltaState(ctx, row.RunID)
	if err != nil {
		return domain.ContextDelta{}, contextDeltaStateError(packet, err)
	}
	if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
		return domain.ContextDelta{}, err
	}
	delta, err := decodeContextDelta(row)
	if err != nil {
		return domain.ContextDelta{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextDelta{}, storageFailure("commit context delta query", err)
	}
	return delta, nil
}

func (s *Store) ExplainContextDelta(ctx context.Context, workspaceIdentifier, deltaID string) (domain.ContextDeltaExplanation, error) {
	delta, err := s.ContextDelta(ctx, workspaceIdentifier, deltaID)
	if err != nil {
		return domain.ContextDeltaExplanation{}, err
	}
	kinds := make([]string, 0, len(delta.Changes))
	for _, change := range delta.Changes {
		kinds = append(kinds, change.Kind)
	}
	return domain.ContextDeltaExplanation{DeltaID: delta.ID, RunID: delta.RunID, ContextPacketID: delta.ContextPacketID,
		Sequence: delta.Sequence, ParentDeltaID: delta.ParentDeltaID, FromEventSequence: delta.FromEventSequence,
		ThroughEventSequence: delta.ThroughEventSequence, ChangeKinds: kinds, ContentHash: delta.ContentHash, ByteSize: delta.ByteSize,
		Included: delta.Included, Excluded: delta.Excluded, Budget: delta.Budget}, nil
}

func (s *Store) FetchRunContextDelta(ctx context.Context, runID string) (domain.ContextDeltaFetchResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ContextDeltaFetchResult{}, storageFailure("begin context delta fetch", err)
	}
	defer tx.Rollback()
	run, err := authorizeRunContextDeltaInTransaction(ctx, tx, s.clock, strings.TrimSpace(runID))
	if err != nil {
		return domain.ContextDeltaFetchResult{}, err
	}
	packet, err := queryContextPacket(ctx, tx, run.WorkspaceID, run.ContextPacketID)
	if err != nil {
		return domain.ContextDeltaFetchResult{}, err
	}
	if packet.Schema != domain.ContextPacketSchema {
		return domain.ContextDeltaFetchResult{Status: domain.ContextDeltaRebaseRequired, RunID: run.ID,
			ContextPacketID: packet.ID, RebaseReason: domain.ContextRebaseUnsupportedPacket,
			Chain: domain.ContextDeltaChain{RunID: run.ID, ContextPacketID: packet.ID, RebaseReason: domain.ContextRebaseUnsupportedPacket}}, nil
	}
	queries := dbgen.New(tx)
	state, err := queries.GetRunContextDeltaState(ctx, run.ID)
	if err != nil {
		return domain.ContextDeltaFetchResult{}, contextDeltaStateError(packet, err)
	}
	if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
		return domain.ContextDeltaFetchResult{}, err
	}
	result := domain.ContextDeltaFetchResult{Status: domain.ContextDeltaNonePending, RunID: run.ID,
		ContextPacketID: packet.ID, StateRevision: state.Revision,
		ScannedThroughEventSequence: state.ScanEventSequence, Chain: contextDeltaChain(packet, state)}
	if state.Status == "pending_ack" {
		delta, err := contextDeltaByID(ctx, queries, pointerValue(state.PendingDeltaID))
		if err != nil {
			return domain.ContextDeltaFetchResult{}, err
		}
		if delta.Budget.Chain.UsedBytes != int(state.CumulativeByteSize) {
			return domain.ContextDeltaFetchResult{}, storageFailure("validate pending context delta chain budget", errors.New("delta and state cumulative bytes differ"))
		}
		result.Status, result.Delta = domain.ContextDeltaPending, &delta
	} else if state.Status == "rebase_required" {
		result.Status, result.RebaseReason = domain.ContextDeltaRebaseRequired, pointerValue(state.RebaseReason)
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextDeltaFetchResult{}, storageFailure("commit context delta fetch", err)
	}
	return result, nil
}

func (s *Store) AcknowledgeRunContextDelta(ctx context.Context, command AcknowledgeContextDeltaCommand) (domain.ContextDeltaAcknowledgement, error) {
	runID, deltaID, key := strings.TrimSpace(command.RunID), command.DeltaID, strings.TrimSpace(command.IdempotencyKey)
	if runID == "" || !validContextDeltaID(deltaID) || command.ExpectedSequence < 1 || key == "" || len(key) > 128 {
		return domain.ContextDeltaAcknowledgement{}, &Error{Code: CodeInvalidContextDelta, Message: "context delta acknowledgement requires run, delta, positive expected sequence, and bounded idempotency key"}
	}
	requestHash, err := hashCommand("context_delta.ack", map[string]any{"run_id": runID, "delta_id": deltaID, "expected_sequence": command.ExpectedSequence})
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("hash context delta acknowledgement", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("begin context delta acknowledgement", err)
	}
	defer tx.Rollback()
	run, err := authorizeRunContextDeltaInTransaction(ctx, tx, s.clock, runID)
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	packet, err := queryContextPacket(ctx, tx, run.WorkspaceID, run.ContextPacketID)
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	if packet.Schema != domain.ContextPacketSchema {
		return domain.ContextDeltaAcknowledgement{}, &Error{Code: CodeContextRebaseRequired, Message: "context packet predates bounded live context and must be rebased"}
	}
	queries := dbgen.New(tx)
	state, err := queries.GetRunContextDeltaState(ctx, run.ID)
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, contextDeltaStateError(packet, err)
	}
	if err := s.validateContextDeltaStateChain(ctx, tx, packet, state); err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	scopedKey := "context-delta-ack:" + run.ID + ":" + key
	var replay domain.ContextDeltaAcknowledgement
	if found, err := lookupIdempotency(ctx, tx, scopedKey, "context_delta.ack", requestHash, &replay); err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	if existing, err := queries.GetContextDeltaAcknowledgement(ctx, dbgen.GetContextDeltaAcknowledgementParams{DeltaID: deltaID, RunID: run.ID}); err == nil {
		if existing.Sequence != command.ExpectedSequence {
			return domain.ContextDeltaAcknowledgement{}, &Error{Code: CodeInvalidContextDelta, Message: "expected sequence does not match the acknowledged delta"}
		}
		result := contextDeltaAcknowledgementFromDB(existing)
		if err := recordIdempotency(ctx, tx, scopedKey, "context_delta.ack", requestHash, result, s.nowText()); err != nil {
			return domain.ContextDeltaAcknowledgement{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ContextDeltaAcknowledgement{}, storageFailure("commit duplicate context delta acknowledgement", err)
		}
		return result, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("query context delta acknowledgement", err)
	}
	if state.Status != "pending_ack" || pointerValue(state.PendingDeltaID) != deltaID || state.LastSequence != command.ExpectedSequence {
		return domain.ContextDeltaAcknowledgement{}, &Error{Code: CodeInvalidContextDelta, Message: "acknowledgement must name the exact pending delta and sequence"}
	}
	delta, err := contextDeltaByID(ctx, queries, deltaID)
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	ackID, err := randomID("cdack_")
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("generate context delta acknowledgement id", err)
	}
	now := s.nowText()
	eventSequence, err := appendEventForActor(ctx, tx, run.WorkspaceID, "context_delta_acknowledgement", ackID, 1,
		contextDeltaAcknowledgedEvent, "context-delta-ack-"+delta.ID, now, run.ID, "agent_run", map[string]any{
			"run_id": run.ID, "context_packet_id": packet.ID, "delta_id": delta.ID, "acknowledgement_id": ackID, "state_revision": state.Revision + 1,
			"sequence": delta.Sequence, "through_event_sequence": delta.ThroughEventSequence,
		})
	if err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	if err := queries.InsertContextDeltaAcknowledgement(ctx, dbgen.InsertContextDeltaAcknowledgementParams{
		ID: ackID, RunID: run.ID, ContextPacketID: packet.ID, DeltaID: delta.ID, Sequence: delta.Sequence,
		AcknowledgedAt: now, AcknowledgedBy: run.ID, IdempotencyKey: key, RequestHash: requestHash, EventSequence: eventSequence,
	}); err != nil {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("insert context delta acknowledgement", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	rows, err := queries.AcknowledgeRunContextDelta(ctx, dbgen.AcknowledgeRunContextDeltaParams{
		LastAcknowledgedDeltaID: &delta.ID, UpdatedAt: now, RunID: run.ID, ContextPacketID: packet.ID,
		Revision: state.Revision, PendingDeltaID: &delta.ID, LastSequence: delta.Sequence,
	})
	if err != nil || rows != 1 {
		if err == nil {
			err = errors.New("pending context delta was concurrently changed")
		}
		return domain.ContextDeltaAcknowledgement{}, storageFailure("acknowledge pending context delta", err)
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	result := domain.ContextDeltaAcknowledgement{ID: ackID, RunID: run.ID, ContextPacketID: packet.ID,
		DeltaID: delta.ID, Sequence: delta.Sequence, AcknowledgedAt: now, AcknowledgedBy: run.ID, EventSequence: eventSequence}
	if err := recordIdempotency(ctx, tx, scopedKey, "context_delta.ack", requestHash, result, now); err != nil {
		return domain.ContextDeltaAcknowledgement{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextDeltaAcknowledgement{}, storageFailure("commit context delta acknowledgement", err)
	}
	return result, nil
}

func relevantContextEvents(ctx context.Context, tx *sql.Tx, run domain.Run, effective effectiveContextState, after, through int64, evaluatedAt string) ([]domain.Event, bool, error) {
	representedIDs := make([]string, 0, len(effective.delivered))
	for revisionID := range effective.delivered {
		representedIDs = append(representedIDs, revisionID)
	}
	for revisionID := range effective.withdrawn {
		if _, delivered := effective.delivered[revisionID]; !delivered {
			representedIDs = append(representedIDs, revisionID)
		}
	}
	sort.Strings(representedIDs)
	representedJSON, err := json.Marshal(representedIDs)
	if err != nil {
		return nil, false, storageFailure("encode represented context knowledge IDs", err)
	}
	representedMessages := make([]string, 0, len(effective.messages))
	for messageID := range effective.messages {
		representedMessages = append(representedMessages, messageID)
	}
	sort.Strings(representedMessages)
	representedMessagesJSON, err := json.Marshal(representedMessages)
	if err != nil {
		return nil, false, storageFailure("encode represented context message IDs", err)
	}
	priorOpenContradictions := make(map[string]bool, len(effective.contradictions))
	for contradictionID := range effective.contradictions {
		priorOpenContradictions[contradictionID] = true
	}
	priorOpenIDs := make([]string, 0, len(priorOpenContradictions))
	for contradictionID := range priorOpenContradictions {
		priorOpenIDs = append(priorOpenIDs, contradictionID)
	}
	sort.Strings(priorOpenIDs)
	priorOpenJSON, err := json.Marshal(priorOpenIDs)
	if err != nil {
		return nil, false, storageFailure("encode represented context contradiction IDs", err)
	}
	disputedIDs := make([]string, 0, len(effective.withdrawn))
	for revisionID, reason := range effective.withdrawn {
		if reason == "disputed" {
			disputedIDs = append(disputedIDs, revisionID)
		}
	}
	sort.Strings(disputedIDs)
	disputedJSON, err := json.Marshal(disputedIDs)
	if err != nil {
		return nil, false, storageFailure("encode disputed context knowledge IDs", err)
	}
	representedDependents := make([]string, 0, len(effective.dependents))
	for dependentID := range effective.dependents {
		representedDependents = append(representedDependents, dependentID)
	}
	sort.Strings(representedDependents)
	representedDependentsJSON, err := json.Marshal(representedDependents)
	if err != nil {
		return nil, false, storageFailure("encode represented context dependent IDs", err)
	}
	representedThreads := make([]string, 0, len(effective.threads))
	for threadID := range effective.threads {
		representedThreads = append(representedThreads, threadID)
	}
	sort.Strings(representedThreads)
	representedThreadsJSON, err := json.Marshal(representedThreads)
	if err != nil {
		return nil, false, storageFailure("encode represented context thread IDs", err)
	}
	query := `SELECT event_id, sequence, type, schema_version,
occurred_at, recorded_at, actor_id, actor_type, workspace_id, entity_type,
entity_id, entity_revision, correlation_id, COALESCE(causation_id, ''), data_json
FROM events
WHERE workspace_id = ? AND sequence > ? AND sequence <= ? AND (
 (entity_type = 'message' AND (
   (type = 'message.sent' AND EXISTS (
    SELECT 1 FROM message_recipients delivery
    JOIN messages message ON message.id = delivery.message_id
    JOIN message_threads thread ON thread.id = message.thread_id
    WHERE message.id = events.entity_id AND delivery.recipient_agent_id = ?
      AND delivery.status IN ('queued','delivered')
		AND ((thread.kind = 'direct' AND (message.project_id IS NULL OR message.project_id = ?)) OR (thread.kind = 'participant_bound' AND EXISTS (SELECT 1 FROM thread_participants participant
		   WHERE participant.id = delivery.recipient_participant_id AND participant.status = 'active'
			 AND participant.agent_id = ? AND participant.project_id = ? AND participant.task_id = ?)))))
   OR
   (type NOT IN ('message.sent','message.delivered','message.read','message.acknowledged','message.wake_succeeded','message.wake_failed')
    AND (entity_id IN (SELECT value FROM json_each(?)) OR EXISTS (
    SELECT 1 FROM message_recipients delivery
    JOIN messages message ON message.id = delivery.message_id
    JOIN message_threads thread ON thread.id = message.thread_id
    WHERE message.id = events.entity_id AND delivery.recipient_agent_id = ?
      AND delivery.status IN ('queued','delivered')
		AND ((thread.kind = 'direct' AND (message.project_id IS NULL OR message.project_id = ?)) OR (thread.kind = 'participant_bound' AND EXISTS (SELECT 1 FROM thread_participants participant
		   WHERE participant.id = delivery.recipient_participant_id AND participant.status = 'active'
			 AND participant.agent_id = ? AND participant.project_id = ? AND participant.task_id = ?))))))))
 OR (entity_type = 'knowledge_revision' AND (
      (type IN ('knowledge.accepted','knowledge.imported') AND EXISTS (
		 SELECT 1 FROM knowledge_revisions candidate JOIN knowledge_items candidate_item ON candidate_item.id = candidate.item_id
		 WHERE candidate.id = events.entity_id AND candidate_item.type = 'decision'
		   AND candidate.review_status = 'accepted' AND candidate.currency_status = 'current'
		   AND (candidate.freshness_policy <> 'expires_at' OR crewfold_timestamp_key(candidate.fresh_until) > crewfold_timestamp_key(?))))
      OR (type IN ('knowledge.marked_stale','knowledge.superseded') AND entity_id IN (SELECT value FROM json_each(?)))
      OR (type NOT IN ('knowledge.proposed','knowledge.accepted','knowledge.rejected','knowledge.marked_stale',
          'knowledge.superseded','knowledge.imported','knowledge.acceptance_denied','knowledge.rejection_denied','knowledge.stale_denied')
		  AND (entity_id IN (SELECT value FROM json_each(?)) OR EXISTS (
		     SELECT 1 FROM knowledge_revisions unknown_revision
		     JOIN knowledge_items unknown_item ON unknown_item.id = unknown_revision.item_id
		     WHERE unknown_revision.id = events.entity_id AND unknown_item.type = 'decision'
		       AND unknown_revision.review_status = 'accepted' AND unknown_revision.currency_status = 'current'
		       AND (unknown_revision.freshness_policy <> 'expires_at' OR crewfold_timestamp_key(unknown_revision.fresh_until) > crewfold_timestamp_key(?)))))) AND EXISTS (
    SELECT 1 FROM knowledge_revisions revision JOIN knowledge_items item ON item.id = revision.item_id
    WHERE revision.id = events.entity_id AND item.project_id = ?
      AND (item.task_scope_id IS NULL OR item.task_scope_id = ?)))
 OR (entity_type = 'knowledge_contradiction' AND (
    (type IN ('contradiction.confirmed','contradiction.imported') AND EXISTS (
      SELECT 1 FROM knowledge_contradictions current_contradiction
      WHERE current_contradiction.id = events.entity_id AND current_contradiction.status = 'open'))
    OR (type IN ('contradiction.dismissed','contradiction.resolved') AND
      (entity_id IN (SELECT value FROM json_each(?)) OR EXISTS (
      SELECT 1 FROM knowledge_contradictions current_contradiction
      WHERE current_contradiction.id = events.entity_id AND (
        current_contradiction.confirm_event_sequence > 0 AND current_contradiction.confirm_event_sequence < events.sequence
        AND (current_contradiction.left_revision_id IN (SELECT value FROM json_each(?))
          OR current_contradiction.right_revision_id IN (SELECT value FROM json_each(?)))))))
    OR type NOT IN ('contradiction.detected','contradiction.confirmed','contradiction.dismissed','contradiction.resolved',
                    'contradiction.imported','contradiction.confirm_denied','contradiction.dismiss_denied')) AND EXISTS (
    SELECT 1 FROM knowledge_contradictions contradiction
    JOIN knowledge_revisions left_revision ON left_revision.id = contradiction.left_revision_id
    JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
    JOIN knowledge_revisions right_revision ON right_revision.id = contradiction.right_revision_id
    JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
    WHERE contradiction.id = events.entity_id AND contradiction.project_id = ?
		AND (((left_item.task_scope_id IS NULL OR left_item.task_scope_id = ?)
		  AND (right_item.task_scope_id IS NULL OR right_item.task_scope_id = ?))
		 OR contradiction.left_revision_id IN (SELECT value FROM json_each(?))
		 OR contradiction.right_revision_id IN (SELECT value FROM json_each(?))
		 OR (left_item.type = 'decision' AND left_revision.review_status = 'accepted' AND left_revision.currency_status = 'current'
		    AND (left_revision.freshness_policy <> 'expires_at' OR crewfold_timestamp_key(left_revision.fresh_until) > crewfold_timestamp_key(?))
		    AND (left_item.task_scope_id IS NULL OR left_item.task_scope_id = ?) AND EXISTS (
		    SELECT 1 FROM events accepted WHERE accepted.workspace_id = events.workspace_id
		      AND accepted.entity_type = 'knowledge_revision' AND accepted.entity_id = contradiction.left_revision_id
		      AND accepted.type IN ('knowledge.accepted','knowledge.imported') AND accepted.sequence > ? AND accepted.sequence <= ?))
		 OR (right_item.type = 'decision' AND right_revision.review_status = 'accepted' AND right_revision.currency_status = 'current'
		    AND (right_revision.freshness_policy <> 'expires_at' OR crewfold_timestamp_key(right_revision.fresh_until) > crewfold_timestamp_key(?))
		    AND (right_item.task_scope_id IS NULL OR right_item.task_scope_id = ?) AND EXISTS (
		    SELECT 1 FROM events accepted WHERE accepted.workspace_id = events.workspace_id
		      AND accepted.entity_type = 'knowledge_revision' AND accepted.entity_id = contradiction.right_revision_id
		      AND accepted.type IN ('knowledge.accepted','knowledge.imported') AND accepted.sequence > ? AND accepted.sequence <= ?)))))
 OR (entity_type = 'thread' AND (
    ((type IN ('thread.created','thread.participant_added') OR entity_id NOT IN (SELECT value FROM json_each(?))) AND EXISTS (
      SELECT 1 FROM thread_participants participant WHERE participant.thread_id = events.entity_id
        AND participant.status = 'active' AND participant.agent_id = ?
        AND participant.project_id = ? AND participant.task_id = ?))
    OR (type NOT IN ('thread.created','thread.participant_added') AND entity_id IN (SELECT value FROM json_each(?)))))
 OR (entity_type = 'task' AND (
      (entity_id = ? AND type NOT IN (` + knownContextTaskEventTypesSQL + `))
      OR entity_id IN (SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ?)
      OR entity_id IN (SELECT value FROM json_each(?))
      OR EXISTS (SELECT 1 FROM events added_edge
        WHERE added_edge.workspace_id = events.workspace_id AND added_edge.entity_type = 'task'
          AND added_edge.entity_id = events.entity_id AND added_edge.type = 'task.dependency_added'
          AND added_edge.sequence > ? AND added_edge.sequence <= ?
          AND added_edge.sequence <= events.sequence
          AND json_extract(added_edge.data_json, '$.depends_on_task_id') = ?)))
 OR (entity_type = 'agent' AND entity_id = ? AND type <> 'agent.updated')
 OR (entity_type = 'checkout' AND entity_id = ? AND type <> 'checkout.git_observed')
 OR (entity_type = 'repository' AND entity_id = (SELECT repository_id FROM checkouts WHERE id = ?))
)
ORDER BY sequence LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, run.WorkspaceID, after, through,
		run.AgentID, run.ProjectID, run.AgentID, run.ProjectID, run.TaskID,
		string(representedMessagesJSON), run.AgentID, run.ProjectID, run.AgentID, run.ProjectID, run.TaskID,
		evaluatedAt, string(representedJSON), string(representedJSON), evaluatedAt, run.ProjectID, run.TaskID,
		string(priorOpenJSON), string(disputedJSON), string(disputedJSON), run.ProjectID, run.TaskID, run.TaskID, string(representedJSON), string(representedJSON),
		evaluatedAt, run.TaskID, after, through, evaluatedAt, run.TaskID, after, through,
		string(representedThreadsJSON), run.AgentID, run.ProjectID, run.TaskID, string(representedThreadsJSON),
		run.TaskID, run.TaskID, string(representedDependentsJSON), after, through, run.TaskID, run.AgentID, run.CheckoutID, run.CheckoutID,
		maximumContextDeltaEvents+1)
	if err != nil {
		return nil, false, storageFailure("query context refresh events", err)
	}
	defer rows.Close()
	events := make([]domain.Event, 0)
	for rows.Next() {
		var event domain.Event
		var data string
		if err := rows.Scan(&event.EventID, &event.Sequence, &event.Type, &event.SchemaVersion,
			&event.OccurredAt, &event.RecordedAt, &event.Actor.ActorID, &event.Actor.ActorType,
			&event.WorkspaceID, &event.Entity.Type, &event.Entity.ID, &event.Entity.Revision,
			&event.CorrelationID, &event.CausationID, &data); err != nil {
			return nil, false, storageFailure("scan context refresh event", err)
		}
		event.Data = json.RawMessage(data)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, storageFailure("iterate context refresh events", err)
	}
	if len(events) > maximumContextDeltaEvents {
		return events[:maximumContextDeltaEvents], true, nil
	}
	return events, false, nil
}

func (s *Store) projectContextChanges(ctx context.Context, tx *sql.Tx, run domain.Run, packet domain.ContextPacket, effective effectiveContextState, events []domain.Event, cutoff int64, evaluatedAt string) ([]domain.ContextDeltaChange, string, error) {
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return nil, "", err
	}
	agent, err := queryAgent(ctx, tx, run.WorkspaceID, run.AgentID)
	if err != nil {
		return nil, "", err
	}
	checkout, err := queryCheckoutByID(ctx, tx, run.CheckoutID)
	if err != nil {
		return nil, "", err
	}
	repositoryFingerprint, err := dbgen.New(tx).GetContextRepositoryFingerprint(ctx, dbgen.GetContextRepositoryFingerprintParams{ID: checkout.RepositoryID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return nil, "", storageFailure("query live context repository fingerprint", err)
	}
	if task.ID != packet.Task.TaskID || task.ObjectiveID != packet.Task.ObjectiveID || task.Title != packet.Task.Title ||
		task.AssignmentID != packet.Task.AssignmentID || task.AssignedAgentID != run.AgentID || task.Description != packet.Task.Description || task.Priority != packet.Task.Priority || task.Budget != packet.Task.Budget ||
		agent.ID != packet.Role.AgentID || agent.Name != packet.Role.Name || agent.Role != packet.Role.Role ||
		agent.Provider != packet.Role.Provider || agent.Runtime != packet.Role.Runtime || !agent.Enabled ||
		checkout.ID != packet.Checkout.CheckoutID || checkout.ProjectID != packet.Checkout.ProjectID ||
		checkout.RepositoryID != packet.Checkout.RepositoryID || checkout.Path != packet.Checkout.Path ||
		checkout.Availability != domain.CheckoutAvailable ||
		checkout.WriteMode != packet.Checkout.WriteMode || checkout.CheckoutKind != packet.Checkout.CheckoutKind ||
		repositoryFingerprint != packet.Checkout.RepositoryFingerprint {
		return nil, domain.ContextRebaseBaseContractChanged, nil
	}
	dependencyCount, err := dbgen.New(tx).CountContextDependencies(ctx, task.ID)
	if err != nil {
		return nil, "", storageFailure("count live context dependencies", err)
	}
	if dependencyCount > maximumContextDependents {
		return nil, domain.ContextRebaseDependencySetChanged, nil
	}
	currentDependencies, err := contextDependencies(ctx, tx, task.ID)
	if err != nil {
		return nil, "", err
	}
	if !reflect.DeepEqual(packet.Dependencies, currentDependencies) {
		return nil, domain.ContextRebaseDependencySetChanged, nil
	}

	latest := make(map[string]domain.Event)
	knownEvents := map[string]bool{
		"message.sent": true, "message.acknowledged": true,
		"knowledge.proposed": true, "knowledge.accepted": true, "knowledge.rejected": true,
		"knowledge.acceptance_denied": true, "knowledge.rejection_denied": true, "knowledge.stale_denied": true,
		"knowledge.marked_stale": true,
		"knowledge.superseded":   true, "knowledge.imported": true,
		"contradiction.confirmed": true, "contradiction.dismissed": true,
		"contradiction.resolved": true, "contradiction.imported": true,
		"thread.created": true, "thread.participant_added": true, "task.created": true,
		"task.updated": true, "task.dependency_added": true, "task.readied": true,
		"task.assigned": true, "task.assignment_expired": true, "task.started": true,
		"task.blocked": true, "task.completion_proposed": true, "task.changes_requested": true,
		"task.completed": true, "task.failed": true, "task.cancelled": true, "task.run_stopped": true,
		"task.handoff_recorded": true, "task.reassigned": true, "task.role_designated": true,
		"agent.updated":  true,
		checkoutObserved: true,
	}
	for _, event := range events {
		if !knownEvents[event.Type] {
			return nil, domain.ContextRebaseUnsupportedEventType, nil
		}
		latest[event.Type+"\x00"+event.Entity.ID] = event
	}
	changes := make([]domain.ContextDeltaChange, 0)
	threadCauses := make(map[string]int64)
	for _, event := range events {
		if event.Type != messageSentEvent || effective.messages[event.Entity.ID] {
			continue
		}
		authorized, err := runAuthorizedForMessage(ctx, tx, run, event.Entity.ID)
		if err != nil {
			return nil, "", err
		}
		if !authorized {
			continue
		}
		item, err := queryInboxItem(ctx, tx, event.Entity.ID, run.AgentID)
		if err != nil {
			if ErrorCode(err) == CodeStorageFailed && errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, "", err
		}
		if item.Delivery.Status != domain.DeliveryQueued && item.Delivery.Status != domain.DeliveryDelivered {
			continue
		}
		preview := domain.InboxSummaryItem{MessageID: item.Message.ID, ThreadID: item.Message.ThreadID,
			Kind: item.Message.Kind, SenderAgentID: item.Message.SenderAgentID, SenderAgentName: item.Message.SenderAgentName,
			BodyPreview: messagePreview(item.Message.Body, 160), Status: item.Delivery.Status, CreatedAt: item.Message.CreatedAt}
		changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaMessageReceived,
			EntityType: "message", EntityID: item.Message.ID, Revision: 1,
			Cause: domain.ContextDeltaCause{EventSequence: event.Sequence, Reason: "authorized unseen message sent to this run"}, Message: &preview})
		if _, represented := effective.threads[item.Message.ThreadID]; !represented {
			threadCauses[item.Message.ThreadID] = event.Sequence
		}
	}

	// Any represented revision can be withdrawn, including v3 findings. A
	// dispute tombstone remains represented authority too: a later terminal or
	// freshness transition must replace the weaker disputed reason and must
	// never be accidentally reoffered.
	representedKnowledge := make(map[string]bool, len(effective.knowledge)+len(effective.withdrawn))
	for revisionID := range effective.knowledge {
		representedKnowledge[revisionID] = true
	}
	for revisionID := range effective.withdrawn {
		representedKnowledge[revisionID] = true
	}
	for revisionID := range representedKnowledge {
		revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, run.WorkspaceID, revisionID)
		if err != nil {
			return nil, "", err
		}
		reason, replacement := "", ""
		if revision.CurrencyStatus == domain.KnowledgeCurrencyStale {
			reason = "stale"
		} else if revision.CurrencyStatus == domain.KnowledgeCurrencySuperseded {
			reason = "superseded"
			replacement, err = s.KnowledgeCurrentSuccessorIDInTransaction(ctx, tx, run.WorkspaceID, revision.ID)
			if err != nil {
				return nil, "", err
			}
		} else if revision.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
			freshUntil, parseErr := time.Parse(time.RFC3339Nano, revision.FreshUntil)
			if parseErr != nil {
				return nil, "", storageFailure("parse represented knowledge freshness", parseErr)
			}
			evaluated, parseErr := time.Parse(time.RFC3339Nano, evaluatedAt)
			if parseErr != nil {
				return nil, "", storageFailure("parse context delta evaluation time", parseErr)
			}
			if !evaluated.Before(freshUntil) {
				reason = "freshness_expired"
			}
		}
		contradictionIDs, contradictionCount, err := openKnowledgeContradictions(ctx, tx, run.WorkspaceID, revision.ID)
		if err != nil {
			return nil, "", err
		}
		if contradictionCount > 0 && reason == "" {
			reason = "disputed"
		}
		if reason == "" || effective.withdrawn[revisionID] == reason {
			continue
		}
		causeSequence := knowledgeWithdrawalCause(events, revision.ID, reason)
		if reason == "disputed" {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(confirm_event_sequence), 0) FROM knowledge_contradictions
WHERE workspace_id = ? AND status = 'open' AND (left_revision_id = ? OR right_revision_id = ?)`,
				run.WorkspaceID, revision.ID, revision.ID).Scan(&causeSequence); err != nil {
				return nil, "", storageFailure("query represented knowledge dispute cause", err)
			}
		}
		withdrawal := domain.ContextKnowledgeWithdrawal{RevisionID: revision.ID, StateRevision: revision.StateRevision,
			Reason: reason, ReplacementRevisionID: replacement, OpenContradictionIDs: contradictionIDs,
			OpenContradictionCount: contradictionCount}
		changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaKnowledgeWithdrawn,
			EntityType: "knowledge_revision", EntityID: revision.ID, Revision: revision.StateRevision,
			Cause: domain.ContextDeltaCause{EventSequence: causeSequence, Reason: reason}, Withdrawal: &withdrawal})
	}
	for _, event := range events {
		if event.Type != knowledgeAcceptedEvent && event.Type != knowledgeImportedEvent {
			continue
		}
		if _, exists := effective.knowledge[event.Entity.ID]; exists {
			continue
		}
		revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, run.WorkspaceID, event.Entity.ID)
		if err != nil {
			return nil, "", err
		}
		if revision.Type != domain.KnowledgeTypeDecision {
			continue
		}
		code, _, err := contextKnowledgeIneligibility(revision, run.WorkspaceID, task, evaluatedAt)
		if err != nil {
			return nil, "", err
		}
		if code != "" {
			continue
		}
		ids, count, err := openKnowledgeContradictions(ctx, tx, run.WorkspaceID, revision.ID)
		if err != nil {
			return nil, "", err
		}
		if count != 0 {
			var cause int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(confirm_event_sequence), 0) FROM knowledge_contradictions
WHERE workspace_id = ? AND status = 'open' AND (left_revision_id = ? OR right_revision_id = ?)`, run.WorkspaceID, revision.ID, revision.ID).Scan(&cause); err != nil {
				return nil, "", storageFailure("query suppressed knowledge contradiction cause", err)
			}
			withdrawal := domain.ContextKnowledgeWithdrawal{RevisionID: revision.ID, StateRevision: revision.StateRevision,
				Reason: "disputed", OpenContradictionIDs: ids, OpenContradictionCount: count}
			changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaKnowledgeWithdrawn,
				EntityType: "knowledge_revision", EntityID: revision.ID, Revision: revision.StateRevision,
				Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: "disputed"}, Withdrawal: &withdrawal})
			continue
		}
		copy := revision
		changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaKnowledgeAccepted,
			EntityType: "knowledge_revision", EntityID: revision.ID, Revision: revision.StateRevision,
			Cause: domain.ContextDeltaCause{EventSequence: event.Sequence, Reason: "accepted applicable decision"}, Knowledge: &copy})
	}

	contradictionCandidates := make(map[string]int64)
	closureAffected := make(map[string]int64)
	for _, event := range events {
		switch event.Type {
		case contradictionConfirmedEvent, contradictionDismissedEvent, contradictionResolvedEvent, contradictionImportedEvent:
			contradictionCandidates[event.Entity.ID] = event.Sequence
		}
	}
	for contradictionID := range effective.contradictions {
		if _, exists := contradictionCandidates[contradictionID]; !exists {
			contradictionCandidates[contradictionID] = 0
		}
	}
	for contradictionID, cause := range contradictionCandidates {
		detail, err := s.knowledgeContradictionDetailInTransaction(ctx, tx, run.WorkspaceID, contradictionID)
		if err != nil {
			return nil, "", err
		}
		if detail.Contradiction.ProjectID != run.ProjectID {
			continue
		}
		leftApplies := knowledgeScopeApplies(detail.LeftRevision, run.WorkspaceID, run.ProjectID, run.TaskID)
		rightApplies := knowledgeScopeApplies(detail.RightRevision, run.WorkspaceID, run.ProjectID, run.TaskID)
		_, represented := effective.contradictions[contradictionID]
		if leftApplies && rightApplies && detail.Contradiction.Status == domain.KnowledgeContradictionOpen && !represented {
			snapshot := domain.ContextContradictionSnapshot{Contradiction: detail.Contradiction,
				LeftRevision: detail.LeftRevision, RightRevision: detail.RightRevision}
			changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaContradictionOpened,
				EntityType: "knowledge_contradiction", EntityID: contradictionID, Revision: detail.Contradiction.StateRevision,
				Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: "applicable exact-revision contradiction is open"}, Contradiction: &snapshot})
		} else if leftApplies && rightApplies && detail.Contradiction.Status != domain.KnowledgeContradictionOpen && represented {
			snapshot := domain.ContextContradictionSnapshot{Contradiction: detail.Contradiction,
				LeftRevision: detail.LeftRevision, RightRevision: detail.RightRevision}
			changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaContradictionClosed,
				EntityType: "knowledge_contradiction", EntityID: contradictionID, Revision: detail.Contradiction.StateRevision,
				Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: "previously delivered contradiction is closed"}, Contradiction: &snapshot})
		}
		if detail.Contradiction.Status != domain.KnowledgeContradictionOpen && cause > 0 {
			provedOpen := detail.Contradiction.ConfirmEventSequence > 0 && detail.Contradiction.ConfirmEventSequence < cause
			leftContributor := provedOpen && effective.withdrawn[detail.LeftRevision.ID] == "disputed"
			rightContributor := provedOpen && effective.withdrawn[detail.RightRevision.ID] == "disputed"
			if leftApplies && leftContributor {
				closureAffected[detail.LeftRevision.ID] = max(closureAffected[detail.LeftRevision.ID], cause)
			}
			if rightApplies && rightContributor {
				closureAffected[detail.RightRevision.ID] = max(closureAffected[detail.RightRevision.ID], cause)
			}
		}
	}
	for revisionID, cause := range closureAffected {
		if effective.withdrawn[revisionID] != "disputed" || (!effective.suppressed[revisionID] && effective.delivered[revisionID].ID == "") {
			continue
		}
		if _, exists := effective.knowledge[revisionID]; exists {
			continue
		}
		revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, run.WorkspaceID, revisionID)
		if err != nil {
			return nil, "", err
		}
		code, _, err := contextKnowledgeIneligibility(revision, run.WorkspaceID, task, evaluatedAt)
		if err != nil {
			return nil, "", err
		}
		_, count, err := openKnowledgeContradictions(ctx, tx, run.WorkspaceID, revisionID)
		if err != nil {
			return nil, "", err
		}
		if revision.Type != domain.KnowledgeTypeDecision || code != "" || count != 0 {
			continue
		}
		copy := revision
		changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaKnowledgeAccepted,
			EntityType: "knowledge_revision", EntityID: revision.ID, Revision: revision.StateRevision,
			Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: "contradiction_closed_reoffer"}, Knowledge: &copy})
	}

	dependentCandidates := make(map[string]bool, len(effective.dependents))
	for dependentID := range effective.dependents {
		var stillRelated int
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?)", dependentID, run.TaskID).Scan(&stillRelated); err != nil {
			return nil, "", storageFailure("revalidate reverse dependent relation", err)
		}
		if stillRelated == 0 {
			return nil, domain.ContextRebaseDependencySetChanged, nil
		}
		dependentCandidates[dependentID] = true
	}
	for _, event := range events {
		if event.Entity.Type == "task" && event.Entity.ID != run.TaskID {
			var related int
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?)", event.Entity.ID, run.TaskID).Scan(&related); err != nil {
				return nil, "", storageFailure("query reverse dependent event relation", err)
			}
			if related != 0 {
				dependentCandidates[event.Entity.ID] = true
			}
		}
	}
	for dependentID := range dependentCandidates {
		taskSnapshot, err := queryTask(ctx, tx, run.WorkspaceID, dependentID)
		if err != nil {
			return nil, "", err
		}
		current := domain.ContextDependency{TaskID: taskSnapshot.ID, Title: taskSnapshot.Title, Status: taskSnapshot.Status, Revision: taskSnapshot.Revision}
		previous, represented := effective.dependents[current.TaskID]
		cause, relationAdded := latestDependentCause(events, current.TaskID, run.TaskID)
		if !represented && !relationAdded || represented && previous == current {
			continue
		}
		if cause == 0 {
			continue
		}
		kind, reason := domain.ContextDeltaDependentUpdated, "represented reverse dependent snapshot changed"
		if !represented {
			kind, reason = domain.ContextDeltaDependentAdded, "task became a direct reverse dependent"
		}
		copy := current
		changes = append(changes, domain.ContextDeltaChange{Kind: kind, EntityType: "task", EntityID: current.TaskID,
			Revision: current.Revision, Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: reason}, Dependency: &copy})
	}

	for _, event := range events {
		if event.Type == threadCreatedEvent || event.Type == threadParticipantAdded {
			threadCauses[event.Entity.ID] = event.Sequence
		}
	}
	for threadID, cause := range threadCauses {
		participant, err := findThreadParticipantBinding(ctx, tx, threadID, run.AgentID, run.ProjectID, run.TaskID)
		if err != nil {
			return nil, "", err
		}
		if participant.ID == "" {
			continue
		}
		thread, err := participantThreadInTransaction(ctx, dbgen.New(tx), run.WorkspaceID, threadID)
		if err != nil {
			return nil, "", err
		}
		if previous, exists := effective.threads[threadID]; exists && previous.ParticipantRevision == thread.ParticipantRevision {
			continue
		}
		copy := thread
		changes = append(changes, domain.ContextDeltaChange{Kind: domain.ContextDeltaParticipantRosterUpdated,
			EntityType: "thread", EntityID: threadID, Revision: thread.ParticipantRevision,
			Cause: domain.ContextDeltaCause{EventSequence: cause, Reason: "exact participant roster changed"}, ParticipantThread: &copy})
	}

	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		leftSequence, rightSequence := left.Cause.EventSequence, right.Cause.EventSequence
		if leftSequence == 0 {
			leftSequence = cutoff + 1
		}
		if rightSequence == 0 {
			rightSequence = cutoff + 1
		}
		if leftSequence != rightSequence {
			return leftSequence < rightSequence
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.EntityID < right.EntityID
	})
	return coalesceContextChanges(changes), "", nil
}

func (s *Store) validateContextBaseContract(ctx context.Context, tx *sql.Tx, run domain.Run, packet domain.ContextPacket) (string, error) {
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return "", err
	}
	agent, err := queryAgent(ctx, tx, run.WorkspaceID, run.AgentID)
	if err != nil {
		return "", err
	}
	checkout, err := queryCheckoutByID(ctx, tx, run.CheckoutID)
	if err != nil {
		return "", err
	}
	fingerprint, err := dbgen.New(tx).GetContextRepositoryFingerprint(ctx, dbgen.GetContextRepositoryFingerprintParams{ID: checkout.RepositoryID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return "", storageFailure("query live context repository fingerprint", err)
	}
	if task.ID != packet.Task.TaskID || task.ObjectiveID != packet.Task.ObjectiveID || task.Title != packet.Task.Title ||
		task.AssignmentID != packet.Task.AssignmentID || task.AssignedAgentID != run.AgentID || task.Description != packet.Task.Description ||
		task.Priority != packet.Task.Priority || task.Budget != packet.Task.Budget || agent.ID != packet.Role.AgentID ||
		agent.Name != packet.Role.Name || agent.Role != packet.Role.Role || agent.Provider != packet.Role.Provider ||
		agent.Runtime != packet.Role.Runtime || !agent.Enabled || checkout.ID != packet.Checkout.CheckoutID ||
		checkout.ProjectID != packet.Checkout.ProjectID || checkout.RepositoryID != packet.Checkout.RepositoryID ||
		checkout.Path != packet.Checkout.Path || checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode != packet.Checkout.WriteMode ||
		checkout.CheckoutKind != packet.Checkout.CheckoutKind || fingerprint != packet.Checkout.RepositoryFingerprint {
		return domain.ContextRebaseBaseContractChanged, nil
	}
	dependencyCount, err := dbgen.New(tx).CountContextDependencies(ctx, task.ID)
	if err != nil {
		return "", storageFailure("count live context dependencies", err)
	}
	if dependencyCount > maximumContextDependents {
		return domain.ContextRebaseDependencySetChanged, nil
	}
	dependencies, err := contextDependencies(ctx, tx, task.ID)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(packet.Dependencies, dependencies) {
		return domain.ContextRebaseDependencySetChanged, nil
	}
	return "", nil
}

func foldEffectiveContext(packet domain.ContextPacket, rows []dbgen.ContextDelta) (effectiveContextState, error) {
	state := effectiveContextState{
		messages: make(map[string]bool), knowledge: make(map[string]domain.KnowledgeRevision),
		delivered: make(map[string]domain.KnowledgeRevision), withdrawn: make(map[string]string),
		suppressed:     make(map[string]bool),
		contradictions: make(map[string]domain.ContextContradictionSnapshot),
		dependencies:   make(map[string]domain.ContextDependency), dependents: make(map[string]domain.ContextDependency),
		threads: make(map[string]domain.ParticipantThread),
	}
	for _, message := range packet.Inbox.Items {
		state.messages[message.MessageID] = true
	}
	for _, revision := range packet.AcceptedKnowledge {
		state.knowledge[revision.ID] = revision
		state.delivered[revision.ID] = revision
	}
	for _, dependency := range packet.Dependencies {
		state.dependencies[dependency.TaskID] = dependency
	}
	for _, dependent := range packet.Dependents {
		state.dependents[dependent.TaskID] = dependent
	}
	for _, thread := range packet.ParticipantThreads {
		state.threads[thread.Thread.ID] = thread
	}
	var priorID string
	var priorThrough int64 = packet.AsOfEventSequence
	cumulativeBytes := 0
	for index, row := range rows {
		delta, err := decodeContextDelta(row)
		if err != nil {
			return effectiveContextState{}, err
		}
		cumulativeBytes += delta.ByteSize
		if delta.ContextPacketID != packet.ID || delta.WorkspaceID != packet.WorkspaceID || delta.ProjectID != packet.ProjectID ||
			delta.TaskID != packet.TaskID || delta.AgentID != packet.AgentID || delta.Sequence != int64(index+1) ||
			delta.ParentDeltaID != priorID || delta.FromEventSequence < priorThrough || delta.Budget.Chain.UsedBytes != cumulativeBytes {
			return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("delta chain linkage or cumulative budget is invalid"))
		}
		priorID, priorThrough = delta.ID, delta.ThroughEventSequence
		for _, change := range delta.Changes {
			switch change.Kind {
			case domain.ContextDeltaMessageReceived:
				if state.messages[change.EntityID] {
					return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("message was delivered more than once"))
				}
				state.messages[change.EntityID] = true
			case domain.ContextDeltaKnowledgeAccepted:
				if change.Knowledge != nil {
					if _, active := state.knowledge[change.EntityID]; active {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("knowledge was accepted while already represented"))
					}
					if reason, delivered := state.withdrawn[change.EntityID]; delivered && reason != "disputed" {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("knowledge reoffer was not solely dispute-withdrawn"))
					}
					state.knowledge[change.EntityID] = *change.Knowledge
					state.delivered[change.EntityID] = *change.Knowledge
					delete(state.withdrawn, change.EntityID)
					delete(state.suppressed, change.EntityID)
				}
			case domain.ContextDeltaKnowledgeWithdrawn:
				_, active := state.knowledge[change.EntityID]
				if !active {
					priorReason, knownWithdrawal := state.withdrawn[change.EntityID]
					if !knownWithdrawal {
						if change.Withdrawal == nil || change.Withdrawal.Reason != "disputed" {
							return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("unrepresented knowledge was withdrawn"))
						}
						state.suppressed[change.EntityID] = true
					} else if priorReason != "disputed" || change.Withdrawal == nil || change.Withdrawal.Reason == "disputed" {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("inactive knowledge withdrawal is not a terminal dispute transition"))
					}
				}
				delete(state.knowledge, change.EntityID)
				if change.Withdrawal != nil {
					state.withdrawn[change.EntityID] = change.Withdrawal.Reason
				}
			case domain.ContextDeltaContradictionOpened:
				if change.Contradiction != nil {
					if _, open := state.contradictions[change.EntityID]; open {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("contradiction was opened twice"))
					}
					state.contradictions[change.EntityID] = *change.Contradiction
				}
			case domain.ContextDeltaContradictionClosed:
				if _, open := state.contradictions[change.EntityID]; !open {
					return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("unrepresented contradiction was closed"))
				}
				delete(state.contradictions, change.EntityID)
			case domain.ContextDeltaDependentAdded:
				if change.Dependency != nil {
					if _, exists := state.dependents[change.EntityID]; exists {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("represented dependent was added twice"))
					}
					state.dependents[change.EntityID] = *change.Dependency
				}
			case domain.ContextDeltaDependentUpdated:
				if change.Dependency != nil {
					if _, exists := state.dependents[change.EntityID]; !exists {
						return effectiveContextState{}, storageFailure("fold context delta chain", errors.New("unrepresented dependent was updated"))
					}
					state.dependents[change.EntityID] = *change.Dependency
				}
			case domain.ContextDeltaParticipantRosterUpdated:
				if change.ParticipantThread != nil {
					state.threads[change.EntityID] = *change.ParticipantThread
				}
			default:
				return effectiveContextState{}, storageFailure("fold context delta chain", fmt.Errorf("unsupported change kind %q", change.Kind))
			}
		}
	}
	return state, nil
}

func finalizeContextDelta(delta *domain.ContextDelta, cumulativeBytes int) ([]byte, error) {
	delta.ContentHash = "sha256:" + strings.Repeat("0", 64)
	delta.ByteSize = 0
	delta.Budget = domain.ContextDeltaBudget{
		Total: domain.ContextBudgetUsage{LimitBytes: maximumContextDeltaBytes, RemainingBytes: maximumContextDeltaBytes},
		Chain: domain.ContextBudgetUsage{LimitBytes: maximumContextDeltaTotal, UsedBytes: cumulativeBytes, RemainingBytes: maximumContextDeltaTotal - cumulativeBytes},
	}
	stable := false
	for range 8 {
		encoded, err := json.Marshal(delta)
		if err != nil {
			return nil, storageFailure("encode context delta", err)
		}
		used := len(encoded)
		remainingTotal, remainingChain := maximumContextDeltaBytes-used, maximumContextDeltaTotal-cumulativeBytes-used
		if remainingTotal < 0 {
			remainingTotal = 0
		}
		if remainingChain < 0 {
			remainingChain = 0
		}
		if delta.ByteSize == used && delta.Budget.Total.UsedBytes == used &&
			delta.Budget.Total.RemainingBytes == remainingTotal &&
			delta.Budget.Chain.UsedBytes == cumulativeBytes+used &&
			delta.Budget.Chain.RemainingBytes == remainingChain {
			stable = true
			break
		}
		delta.ByteSize = used
		delta.Budget.Total.UsedBytes, delta.Budget.Total.RemainingBytes = used, remainingTotal
		delta.Budget.Chain.UsedBytes, delta.Budget.Chain.RemainingBytes = cumulativeBytes+used, remainingChain
	}
	if !stable {
		return nil, storageFailure("encode context delta", errors.New("delta byte accounting did not converge"))
	}
	hash, err := contextDeltaSemanticHash(*delta)
	if err != nil {
		return nil, err
	}
	delta.ContentHash = hash
	encoded, err := json.Marshal(delta)
	if err != nil {
		return nil, storageFailure("encode final context delta", err)
	}
	if len(encoded) != delta.ByteSize {
		return nil, storageFailure("validate context delta byte accounting", errors.New("stored delta size is not stable"))
	}
	if err := validateContextDeltaForFinalize(*delta); err != nil {
		return nil, storageFailure("validate final context delta", err)
	}
	return encoded, nil
}

func contextDeltaSemanticHash(delta domain.ContextDelta) (string, error) {
	semantic := delta
	semantic.ID, semantic.ContentHash, semantic.CreatedAt, semantic.CreatedBy = "", "", "", ""
	semantic.ByteSize = 0
	semantic.Budget.Total.UsedBytes, semantic.Budget.Total.RemainingBytes = 0, semantic.Budget.Total.LimitBytes
	semantic.Budget.Chain.UsedBytes, semantic.Budget.Chain.RemainingBytes = 0, semantic.Budget.Chain.LimitBytes
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return "", storageFailure("encode context delta semantic content", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeContextDelta(row dbgen.ContextDelta) (domain.ContextDelta, error) {
	var delta domain.ContextDelta
	decoder := json.NewDecoder(bytes.NewBufferString(row.DeltaJson))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&delta); err != nil {
		return domain.ContextDelta{}, storageFailure("decode context delta", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.ContextDelta{}, storageFailure("decode context delta", errors.New("delta has trailing JSON content"))
	}
	if delta.Schema != domain.ContextDeltaSchema || delta.ID != row.ID || delta.RunID != row.RunID ||
		delta.ContextPacketID != row.ContextPacketID || delta.Sequence != row.Sequence ||
		delta.ParentDeltaID != pointerValue(row.ParentDeltaID) || delta.FromEventSequence != row.FromEventSequence ||
		delta.ThroughEventSequence != row.ThroughEventSequence || delta.ContentHash != row.ContentHash ||
		delta.ByteSize != int(row.ByteSize) || delta.ByteSize != len([]byte(row.DeltaJson)) {
		return domain.ContextDelta{}, storageFailure("validate context delta", errors.New("stored delta row and JSON differ"))
	}
	hash, err := contextDeltaSemanticHash(delta)
	if err != nil {
		return domain.ContextDelta{}, err
	}
	if hash != delta.ContentHash {
		return domain.ContextDelta{}, storageFailure("validate context delta semantic hash", errors.New("stored delta semantic hash differs"))
	}
	if err := validateContextDelta(delta); err != nil {
		return domain.ContextDelta{}, storageFailure("validate context delta content", err)
	}
	return delta, nil
}

func validateContextDelta(delta domain.ContextDelta) error {
	if err := validateContextDeltaForFinalize(delta); err != nil {
		return err
	}
	if delta.ByteSize <= 0 || delta.ByteSize > maximumContextDeltaBytes || delta.Budget.Chain.UsedBytes > maximumContextDeltaTotal {
		return errors.New("delta exceeds immutable byte bounds")
	}
	return nil
}

func validateContextDeltaForFinalize(delta domain.ContextDelta) error {
	if delta.Schema != domain.ContextDeltaSchema || !validContextDeltaID(delta.ID) || delta.RunID == "" || delta.ContextPacketID == "" ||
		delta.WorkspaceID == "" || delta.ProjectID == "" || delta.TaskID == "" || delta.AgentID == "" ||
		delta.BasePacketSchema != domain.ContextPacketSchema || delta.Sequence < 1 ||
		delta.FromEventSequence < 0 || delta.ThroughEventSequence < delta.FromEventSequence || len(delta.Changes) == 0 ||
		len(delta.Changes) > maximumContextDeltaEvents || delta.CreatedBy != localOwnerActorID || delta.CreatedAt != delta.EvaluatedAt {
		return errors.New("delta identity, cursor, creator, or change count is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, delta.EvaluatedAt); err != nil {
		return errors.New("delta evaluation timestamp is invalid")
	}
	if delta.Sequence == 1 && delta.ParentDeltaID != "" || delta.Sequence > 1 && !validContextDeltaID(delta.ParentDeltaID) {
		return errors.New("delta parent is invalid")
	}
	if len(delta.Excluded) != 0 || !reflect.DeepEqual(delta.Included, contextDeltaSelections(delta.Changes)) {
		return errors.New("delta inclusion and exclusion evidence is invalid")
	}
	if delta.Budget.Total.LimitBytes != maximumContextDeltaBytes || delta.Budget.Total.UsedBytes != delta.ByteSize ||
		delta.Budget.Total.RemainingBytes != max(0, maximumContextDeltaBytes-delta.ByteSize) ||
		delta.ByteSize <= 0 || delta.Budget.Chain.LimitBytes != maximumContextDeltaTotal ||
		delta.Budget.Chain.UsedBytes < delta.ByteSize ||
		delta.Budget.Chain.RemainingBytes != max(0, maximumContextDeltaTotal-delta.Budget.Chain.UsedBytes) {
		return errors.New("delta budget is invalid")
	}
	changeKeys := make(map[string]bool, len(delta.Changes))
	for index, change := range delta.Changes {
		if change.EntityID == "" || change.Revision < 1 || change.Cause.Reason == "" || change.Cause.EventSequence < 0 {
			return errors.New("delta change identity, revision, or cause is invalid")
		}
		if change.Cause.EventSequence != 0 && (change.Cause.EventSequence <= delta.FromEventSequence || change.Cause.EventSequence > delta.ThroughEventSequence) {
			return errors.New("delta change cause is outside its scan window")
		}
		if change.Cause.EventSequence == 0 && (change.Kind != domain.ContextDeltaKnowledgeWithdrawn || change.Withdrawal == nil || change.Withdrawal.Reason != "freshness_expired") {
			return errors.New("only time-driven freshness withdrawal may omit an event cause")
		}
		key := change.Kind + "\x00" + change.EntityType + "\x00" + change.EntityID
		if changeKeys[key] {
			return errors.New("delta changes are not coalesced")
		}
		changeKeys[key] = true
		if index > 0 {
			previous := delta.Changes[index-1]
			leftSequence, rightSequence := previous.Cause.EventSequence, change.Cause.EventSequence
			if leftSequence == 0 {
				leftSequence = delta.ThroughEventSequence + 1
			}
			if rightSequence == 0 {
				rightSequence = delta.ThroughEventSequence + 1
			}
			if leftSequence > rightSequence || leftSequence == rightSequence && (previous.Kind > change.Kind || previous.Kind == change.Kind && previous.EntityID >= change.EntityID) {
				return errors.New("delta changes are not deterministically ordered")
			}
		}
		payloads := 0
		for _, present := range []bool{change.Message != nil, change.Knowledge != nil, change.Withdrawal != nil, change.Contradiction != nil, change.Dependency != nil, change.ParticipantThread != nil} {
			if present {
				payloads++
			}
		}
		if payloads != 1 {
			return errors.New("delta change must carry exactly one typed payload")
		}
		switch change.Kind {
		case domain.ContextDeltaMessageReceived:
			if change.EntityType != "message" || change.Message == nil || change.Message.MessageID != change.EntityID {
				return errors.New("message change payload is invalid")
			}
		case domain.ContextDeltaKnowledgeAccepted:
			if change.EntityType != "knowledge_revision" || change.Knowledge == nil || change.Knowledge.ID != change.EntityID || change.Knowledge.StateRevision != change.Revision || change.Knowledge.Type != domain.KnowledgeTypeDecision {
				return errors.New("knowledge acceptance payload is invalid")
			}
			knowledge := change.Knowledge
			if knowledge.WorkspaceID != delta.WorkspaceID || knowledge.ProjectID != delta.ProjectID || (knowledge.TaskScopeID != "" && knowledge.TaskScopeID != delta.TaskID) ||
				knowledge.ReviewStatus != domain.KnowledgeReviewAccepted || knowledge.CurrencyStatus != domain.KnowledgeCurrencyCurrent {
				return errors.New("knowledge acceptance authority is invalid")
			}
		case domain.ContextDeltaKnowledgeWithdrawn:
			if change.EntityType != "knowledge_revision" || change.Withdrawal == nil || change.Withdrawal.RevisionID != change.EntityID || change.Withdrawal.StateRevision != change.Revision || len(change.Withdrawal.OpenContradictionIDs) > maximumWithdrawalContradictions || change.Withdrawal.OpenContradictionCount < len(change.Withdrawal.OpenContradictionIDs) || !sort.StringsAreSorted(change.Withdrawal.OpenContradictionIDs) {
				return errors.New("knowledge withdrawal payload is invalid")
			}
			if change.Withdrawal.Reason != "stale" && change.Withdrawal.Reason != "superseded" && change.Withdrawal.Reason != "freshness_expired" && change.Withdrawal.Reason != "disputed" ||
				(change.Withdrawal.Reason == "superseded") != (change.Withdrawal.ReplacementRevisionID != "") {
				return errors.New("knowledge withdrawal reason or replacement is invalid")
			}
			for index, id := range change.Withdrawal.OpenContradictionIDs {
				if id == "" || index > 0 && id == change.Withdrawal.OpenContradictionIDs[index-1] {
					return errors.New("knowledge withdrawal contradiction IDs are invalid")
				}
			}
		case domain.ContextDeltaContradictionOpened, domain.ContextDeltaContradictionClosed:
			if change.EntityType != "knowledge_contradiction" || change.Contradiction == nil || change.Contradiction.Contradiction.ID != change.EntityID || change.Contradiction.Contradiction.StateRevision != change.Revision || change.Contradiction.LeftRevision.ID == "" || change.Contradiction.RightRevision.ID == "" {
				return errors.New("contradiction payload is invalid")
			}
			contradiction := change.Contradiction.Contradiction
			if contradiction.LeftRevisionID != change.Contradiction.LeftRevision.ID || contradiction.RightRevisionID != change.Contradiction.RightRevision.ID ||
				(change.Kind == domain.ContextDeltaContradictionOpened) != (contradiction.Status == domain.KnowledgeContradictionOpen) {
				return errors.New("contradiction kind, status, or revision pair is invalid")
			}
		case domain.ContextDeltaDependentAdded, domain.ContextDeltaDependentUpdated:
			if change.EntityType != "task" || change.Dependency == nil || change.Dependency.TaskID != change.EntityID || change.Dependency.Revision != change.Revision {
				return errors.New("dependent payload is invalid")
			}
		case domain.ContextDeltaParticipantRosterUpdated:
			if change.EntityType != "thread" || change.ParticipantThread == nil || change.ParticipantThread.Thread.ID != change.EntityID || change.ParticipantThread.ParticipantRevision != change.Revision {
				return errors.New("participant roster payload is invalid")
			}
			if err := validateContextParticipantThread(*change.ParticipantThread, delta.WorkspaceID, delta.ProjectID, delta.TaskID, delta.AgentID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported context delta change kind %q", change.Kind)
		}
	}
	return nil
}

func contextDeltaByID(ctx context.Context, queries *dbgen.Queries, deltaID string) (domain.ContextDelta, error) {
	row, err := queries.GetContextDeltaByID(ctx, deltaID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContextDelta{}, &Error{Code: CodeContextDeltaNotFound, Message: fmt.Sprintf("context delta %q was not found", deltaID)}
	}
	if err != nil {
		return domain.ContextDelta{}, storageFailure("query context delta", err)
	}
	return decodeContextDelta(row)
}

func refreshResult(packet domain.ContextPacket, state dbgen.RunContextDeltaState, status string, through int64, delta *domain.ContextDelta) domain.ContextRefreshResult {
	from := state.ScanEventSequence
	if delta != nil {
		from = delta.FromEventSequence
	}
	return domain.ContextRefreshResult{Status: status, RunID: state.RunID, ContextPacketID: packet.ID,
		StateRevision: state.Revision, ScannedFromEventSequence: from, ScannedThroughEventSequence: through,
		Chain: contextDeltaChain(packet, state), Delta: delta, RebaseReason: pointerValue(state.RebaseReason)}
}

func contextDeltaChain(packet domain.ContextPacket, state dbgen.RunContextDeltaState) domain.ContextDeltaChain {
	chain := domain.ContextDeltaChain{RunID: state.RunID, ContextPacketID: state.ContextPacketID,
		BaseEventSequence: packet.AsOfEventSequence, ScannedThroughEventSequence: state.ScanEventSequence,
		LatestDeltaID: pointerValue(state.LastDeltaID), LatestSequence: state.LastSequence,
		PendingDeltaID: pointerValue(state.PendingDeltaID), DeltaCount: state.DeltaCount,
		CumulativeByteSize: int(state.CumulativeByteSize), RebaseReason: pointerValue(state.RebaseReason),
		RebaseEventSequence: pointerValue(state.RebaseEventSequence), Revision: state.Revision}
	if state.PendingDeltaID != nil {
		chain.PendingSequence = state.LastSequence
	}
	if state.LastAcknowledgedDeltaID != nil {
		chain.LastAcknowledgedDeltaID = *state.LastAcknowledgedDeltaID
		chain.LastAcknowledgedSequence = state.LastSequence
		if state.PendingDeltaID != nil {
			chain.LastAcknowledgedSequence--
		}
	}
	return chain
}

func contextRebaseScannedFrom(ctx context.Context, tx *sql.Tx, state dbgen.RunContextDeltaState) (int64, error) {
	if state.RebaseEventSequence == nil {
		return 0, storageFailure("read context rebase interval", errors.New("rebase state has no event"))
	}
	var scannedFrom int64
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(data_json, '$.scan_from')
FROM events WHERE sequence = ? AND type = ? AND entity_type = 'run_context_delta_state' AND entity_id = ?`,
		*state.RebaseEventSequence, contextDeltaRebaseRequiredEvent, state.RunID).Scan(&scannedFrom); err != nil {
		return 0, storageFailure("read context rebase interval", err)
	}
	if scannedFrom < 0 || scannedFrom > state.ScanEventSequence {
		return 0, storageFailure("validate context rebase interval", errors.New("rebase scan interval is invalid"))
	}
	return scannedFrom, nil
}

func (s *Store) validateContextDeltaStateChain(ctx context.Context, tx *sql.Tx, packet domain.ContextPacket, state dbgen.RunContextDeltaState) error {
	rows, err := dbgen.New(tx).ListAllRunContextDeltas(ctx, state.RunID)
	if err != nil {
		return storageFailure("validate context delta state chain", err)
	}
	if int64(len(rows)) != state.DeltaCount || state.DeltaCount != state.LastSequence {
		return storageFailure("validate context delta state chain", errors.New("delta row count differs from state"))
	}
	var cumulative int64
	var lastID string
	var lastThrough = packet.AsOfEventSequence
	activeKnowledge := make(map[string]bool, len(packet.AcceptedKnowledge))
	knownKnowledge := make(map[string]bool, len(packet.AcceptedKnowledge))
	for _, revision := range packet.AcceptedKnowledge {
		activeKnowledge[revision.ID] = true
		knownKnowledge[revision.ID] = true
	}
	for index, row := range rows {
		delta, err := decodeContextDelta(row)
		if err != nil {
			return err
		}
		if err := s.validateContextDeltaCanonicalProvenance(ctx, tx, packet, delta); err != nil {
			return err
		}
		if delta.Sequence != int64(index+1) || delta.ParentDeltaID != lastID || delta.FromEventSequence < lastThrough || delta.ThroughEventSequence < delta.FromEventSequence {
			return storageFailure("validate context delta state chain", errors.New("delta linkage differs from state chain"))
		}
		cumulative += int64(delta.ByteSize)
		if delta.Budget.Chain.UsedBytes != int(cumulative) {
			return storageFailure("validate context delta state chain", errors.New("delta cumulative budget differs"))
		}
		for _, change := range delta.Changes {
			switch change.Kind {
			case domain.ContextDeltaKnowledgeAccepted:
				activeKnowledge[change.EntityID] = true
				knownKnowledge[change.EntityID] = true
			case domain.ContextDeltaKnowledgeWithdrawn:
				if !activeKnowledge[change.EntityID] && !knownKnowledge[change.EntityID] {
					if change.Withdrawal == nil || change.Withdrawal.Reason != "disputed" {
						return storageFailure("validate context delta state chain", errors.New("unrepresented withdrawal is not a dispute tombstone"))
					}
					var acceptedWithinDelta int
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events
WHERE workspace_id = ? AND entity_type = 'knowledge_revision' AND entity_id = ?
  AND type IN ('knowledge.accepted','knowledge.imported') AND sequence > ? AND sequence <= ?)`,
						packet.WorkspaceID, change.EntityID, delta.FromEventSequence, delta.ThroughEventSequence).Scan(&acceptedWithinDelta); err != nil {
						return storageFailure("validate suppressed knowledge provenance", err)
					}
					if acceptedWithinDelta != 1 {
						return storageFailure("validate context delta state chain", errors.New("dispute tombstone lacks a post-base acceptance fact"))
					}
					knownKnowledge[change.EntityID] = true
				}
				delete(activeKnowledge, change.EntityID)
			}
		}
		lastID, lastThrough = delta.ID, delta.ThroughEventSequence
	}
	if cumulative != state.CumulativeByteSize || pointerValue(state.LastDeltaID) != lastID || state.ScanEventSequence < lastThrough {
		return storageFailure("validate context delta state chain", errors.New("delta chain head differs from state"))
	}
	if state.Status == "pending_ack" && pointerValue(state.PendingDeltaID) != lastID {
		return storageFailure("validate context delta state chain", errors.New("pending delta is not the chain head"))
	}
	var pendingAckCount int
	if state.PendingDeltaID != nil {
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM context_delta_acknowledgements WHERE delta_id = ?", *state.PendingDeltaID).Scan(&pendingAckCount); err != nil {
			return storageFailure("validate pending context acknowledgement", err)
		}
		if pendingAckCount != 0 {
			return storageFailure("validate context delta state chain", errors.New("pending delta already has a committed acknowledgement"))
		}
	}
	if state.LastAcknowledgedDeltaID != nil {
		var acknowledged int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM context_delta_acknowledgements WHERE run_id = ? AND delta_id = ?", state.RunID, *state.LastAcknowledgedDeltaID).Scan(&acknowledged); err != nil {
			return storageFailure("validate last context acknowledgement", err)
		}
		if acknowledged != 1 {
			return storageFailure("validate context delta state chain", errors.New("last acknowledged delta lacks exact acknowledgement"))
		}
	}
	if _, err := foldEffectiveContext(packet, rows); err != nil {
		return err
	}
	return nil
}

func (s *Store) commitContextRebase(ctx context.Context, tx *sql.Tx, queries *dbgen.Queries, packet domain.ContextPacket, state dbgen.RunContextDeltaState, cutoff int64, reason, scopedKey, requestHash, correlationID, evaluatedAt string) (domain.ContextRefreshResult, error) {
	now := evaluatedAt
	scannedFrom := state.ScanEventSequence
	newRevision := state.Revision + 1
	eventSequence, err := appendEvent(ctx, tx, packet.WorkspaceID, "run_context_delta_state", state.RunID, newRevision,
		contextDeltaRebaseRequiredEvent, correlationID, now, map[string]any{
			"run_id": state.RunID, "context_packet_id": packet.ID, "scan_from": state.ScanEventSequence,
			"through_event_sequence": cutoff, "reason": reason, "delta_count": state.DeltaCount,
			"cumulative_byte_size": state.CumulativeByteSize,
		})
	if err != nil {
		return domain.ContextRefreshResult{}, err
	}
	rows, err := queries.MarkRunContextRebaseRequired(ctx, dbgen.MarkRunContextRebaseRequiredParams{
		ScanEventSequence: cutoff, RebaseReason: &reason, RebaseEventSequence: &eventSequence, UpdatedAt: now,
		RunID: state.RunID, ContextPacketID: packet.ID, Revision: state.Revision, ScanEventSequence_2: state.ScanEventSequence,
	})
	if err != nil || rows != 1 {
		if err == nil {
			err = errors.New("context delta state was concurrently changed")
		}
		return domain.ContextRefreshResult{}, storageFailure("mark context rebase required", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	state.Status, state.Revision, state.ScanEventSequence = "rebase_required", newRevision, cutoff
	state.RebaseReason, state.RebaseEventSequence = &reason, &eventSequence
	result := refreshResult(packet, state, domain.ContextRefreshRebaseRequired, cutoff, nil)
	result.ScannedFromEventSequence = scannedFrom
	result.EventSequence = eventSequence
	if err := recordIdempotency(ctx, tx, scopedKey, "context.refresh", requestHash, result, now); err != nil {
		return domain.ContextRefreshResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ContextRefreshResult{}, storageFailure("commit context rebase required", err)
	}
	return result, nil
}

func authorizeRunContextDeltaInTransaction(ctx context.Context, tx *sql.Tx, clock func() time.Time, runID string) (domain.Run, error) {
	run, err := queryRun(ctx, tx, "", runID)
	if err != nil {
		return domain.Run{}, err
	}
	var expiresAt string
	if err := tx.QueryRowContext(ctx, `SELECT capability.expires_at
FROM run_context_bindings binding
JOIN run_capabilities capability ON capability.run_id = binding.run_id
WHERE binding.run_id = ? AND binding.context_packet_id = ?`, run.ID, run.ContextPacketID).Scan(&expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, &Error{Code: CodeCapabilityInactive, Message: "run has no scoped capability"}
		}
		return domain.Run{}, storageFailure("query run context delta capability", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.Run{}, storageFailure("parse run capability expiry", err)
	}
	if !clock().UTC().Before(expires) {
		return domain.Run{}, &Error{Code: CodeCapabilityExpired, Message: "run capability has expired"}
	}
	if !runCanUseMailbox(run.Status) {
		return domain.Run{}, &Error{Code: CodeCapabilityInactive, Message: "run is no longer active"}
	}
	return run, nil
}

func contextDeltaStateError(packet domain.ContextPacket, err error) error {
	if errors.Is(err, sql.ErrNoRows) && packet.Schema != domain.ContextPacketSchema {
		return &Error{Code: CodeContextRebaseRequired, Message: "context packet predates bounded live context and must be rebased"}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storageFailure("query run context delta state", errors.New("version-four run has no delta state"))
	}
	return storageFailure("query run context delta state", err)
}

func contextDeltaAcknowledgementFromDB(value dbgen.ContextDeltaAcknowledgement) domain.ContextDeltaAcknowledgement {
	return domain.ContextDeltaAcknowledgement{ID: value.ID, RunID: value.RunID, ContextPacketID: value.ContextPacketID,
		DeltaID: value.DeltaID, Sequence: value.Sequence, AcknowledgedAt: value.AcknowledgedAt,
		AcknowledgedBy: value.AcknowledgedBy, EventSequence: value.EventSequence}
}

func sameContextDependencyIDs(left, right []domain.ContextDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].TaskID != right[index].TaskID {
			return false
		}
	}
	return true
}

func knowledgeScopeApplies(revision domain.KnowledgeRevision, workspaceID, projectID, taskID string) bool {
	return revision.WorkspaceID == workspaceID && revision.ProjectID == projectID &&
		(revision.TaskScopeID == "" || revision.TaskScopeID == taskID)
}

func openKnowledgeContradictions(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string) ([]string, int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_contradictions
WHERE workspace_id = ? AND status = 'open' AND (left_revision_id = ? OR right_revision_id = ?)`,
		workspaceID, revisionID, revisionID).Scan(&count); err != nil {
		return nil, 0, storageFailure("count open knowledge contradictions", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM knowledge_contradictions
WHERE workspace_id = ? AND status = 'open' AND (left_revision_id = ? OR right_revision_id = ?)
ORDER BY id LIMIT ?`, workspaceID, revisionID, revisionID, maximumWithdrawalContradictions)
	if err != nil {
		return nil, 0, storageFailure("list open knowledge contradictions", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, storageFailure("scan open knowledge contradiction", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, storageFailure("iterate open knowledge contradictions", err)
	}
	return ids, count, nil
}

func knowledgeWithdrawalCause(events []domain.Event, revisionID, reason string) int64 {
	var result int64
	for _, event := range events {
		if event.Entity.ID == revisionID && ((reason == "stale" && event.Type == knowledgeStaleEvent) ||
			(reason == "superseded" && event.Type == knowledgeSupersededEvent)) && event.Sequence > result {
			result = event.Sequence
		}
		if reason == "disputed" && event.Entity.Type == "knowledge_contradiction" && event.Sequence > result {
			var data struct {
				Left  string `json:"left_revision_id"`
				Right string `json:"right_revision_id"`
			}
			if json.Unmarshal(event.Data, &data) == nil && (data.Left == revisionID || data.Right == revisionID) {
				result = event.Sequence
			}
		}
	}
	return result
}

func latestTaskCause(events []domain.Event, taskID string) int64 {
	var result int64
	for _, event := range events {
		if event.Entity.Type == "task" && event.Entity.ID == taskID && event.Sequence > result {
			result = event.Sequence
		}
	}
	return result
}

func latestDependentCause(events []domain.Event, dependentID, liveTaskID string) (int64, bool) {
	var result int64
	added := false
	for _, event := range events {
		if event.Entity.Type != "task" || event.Entity.ID != dependentID {
			continue
		}
		if event.Sequence > result {
			result = event.Sequence
		}
		if event.Type == taskDependencyAdded {
			var data struct {
				DependsOnTaskID string `json:"depends_on_task_id"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.DependsOnTaskID == liveTaskID {
				added = true
			}
		}
	}
	return result, added
}

func contextDeltaSelections(changes []domain.ContextDeltaChange) []domain.ContextSelection {
	result := make([]domain.ContextSelection, 0, len(changes))
	for _, change := range changes {
		result = append(result, domain.ContextSelection{Section: change.Kind, EntityType: change.EntityType,
			EntityID: change.EntityID, Revision: change.Revision, Reason: change.Cause.Reason})
	}
	return result
}

func coalesceContextChanges(changes []domain.ContextDeltaChange) []domain.ContextDeltaChange {
	if len(changes) < 2 {
		return changes
	}
	result := make([]domain.ContextDeltaChange, 0, len(changes))
	index := make(map[string]int)
	for _, change := range changes {
		key := change.Kind + "\x00" + change.EntityType + "\x00" + change.EntityID
		if existing, ok := index[key]; ok {
			result[existing] = change
			continue
		}
		index[key] = len(result)
		result = append(result, change)
	}
	return result
}

func pointerValue[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func validContextDeltaID(value string) bool {
	if len(value) != len("cdelta_")+32 || !strings.HasPrefix(value, "cdelta_") {
		return false
	}
	for _, character := range value[len("cdelta_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
