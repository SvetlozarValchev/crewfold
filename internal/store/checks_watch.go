package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	maximumCheckWatchCandidates    = 100
	maximumCheckWatchJournalEvents = 1000
	maximumCheckWatchCursorBytes   = 4096
	checkWatchCompletedEvent       = "check.watch_completed"
	checkFreshnessObservedEvent    = "check.freshness_observed"
	checkFreshnessStaleEvent       = "check.freshness_stale"
)

type checkWatchCursor struct {
	Version       int    `json:"version"`
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id"`
	EventSequence int64  `json:"event_sequence"`
	ResultID      string `json:"result_id,omitempty"`
}

// ReplayCheckWatch checks the immutable owner request before preparation reads
// mutable cursor or Git source state. Callers use it only for public passes; a
// missing key proceeds through PrepareCheckWatch and ApplyCheckWatch normally.
func (s *Store) ReplayCheckWatch(ctx context.Context, command PrepareCheckWatchCommand, idempotencyKey string) (MutationResult[domain.CheckWatchReceipt], bool, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.After = strings.TrimSpace(command.After)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	limit := command.Limit
	if limit <= 0 {
		limit = maximumCheckWatchCandidates
	}
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || idempotencyKey == "" || limit > maximumCheckWatchCandidates {
		return MutationResult[domain.CheckWatchReceipt]{}, false, checkError(CodeInvalidRun, "check-watch replay request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, false, storageFailure("begin check-watch replay", err)
	}
	defer tx.Rollback()
	scope, err := dbgen.New(tx).GetCheckWatchScope(ctx, dbgen.GetCheckWatchScopeParams{WorkspaceIdentifier: command.WorkspaceIdentifier, ProjectIdentifier: command.ProjectIdentifier})
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult[domain.CheckWatchReceipt]{}, false, checkError(CodeInvalidRun, "check-watch project was not found in the workspace")
	}
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, false, storageFailure("resolve check-watch replay scope", err)
	}
	requestHash, err := checkWatchRequestHash(scope.WorkspaceID, scope.ProjectID, command.After, limit)
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, false, storageFailure("hash check-watch replay request", err)
	}
	var replay MutationResult[domain.CheckWatchReceipt]
	found, err := lookupIdempotency(ctx, tx, ownerCheckIdempotencyKey(idempotencyKey), "check.watch", requestHash, &replay)
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, false, storageFailure("finish check-watch replay", err)
	}
	return replay, found, nil
}

func (s *Store) ListCheckWatchScopes(ctx context.Context, query ListCheckWatchScopesQuery) (CheckWatchScopePage, error) {
	query.After = strings.TrimSpace(query.After)
	limit := query.Limit
	if limit <= 0 {
		limit = maximumCheckWatchCandidates
	}
	if limit > maximumCheckWatchCandidates || (query.After != "" && !strings.HasPrefix(query.After, "prj_")) {
		return CheckWatchScopePage{}, checkError(CodeInvalidRun, "check-watch scope page is invalid")
	}
	rows, err := dbgen.New(s.db).ListCheckWatchScopes(ctx, dbgen.ListCheckWatchScopesParams{AfterProjectID: query.After, ResultLimit: int64(limit + 1)})
	if err != nil {
		return CheckWatchScopePage{}, storageFailure("list check-watch scopes", err)
	}
	page := CheckWatchScopePage{Items: []CheckWatchScope{}}
	for index, row := range rows {
		if index == limit {
			page.NextCursor = page.Items[len(page.Items)-1].ProjectID
			break
		}
		page.Items = append(page.Items, CheckWatchScope{WorkspaceID: row.WorkspaceID, WorkspaceName: row.WorkspaceName, ProjectID: row.ProjectID, ProjectName: row.ProjectName})
	}
	return page, nil
}

func (s *Store) PrepareCheckWatch(ctx context.Context, command PrepareCheckWatchCommand) (PreparedCheckWatch, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.After = strings.TrimSpace(command.After)
	limit := command.Limit
	if limit <= 0 {
		limit = maximumCheckWatchCandidates
	}
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || limit > maximumCheckWatchCandidates {
		return PreparedCheckWatch{}, checkError(CodeInvalidRun, "check watch requires an exact project and a limit from 1 to 100")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("begin check-watch preparation", err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	scope, err := queries.GetCheckWatchScope(ctx, dbgen.GetCheckWatchScopeParams{WorkspaceIdentifier: command.WorkspaceIdentifier, ProjectIdentifier: command.ProjectIdentifier})
	if errors.Is(err, sql.ErrNoRows) {
		return PreparedCheckWatch{}, checkError(CodeInvalidRun, "check-watch project was not found in the workspace")
	}
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("resolve check-watch scope", err)
	}
	state, err := queries.GetCheckWatchState(ctx, dbgen.GetCheckWatchStateParams{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID})
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("read check-watch state", err)
	}
	fromEventSequence, fromResultID := state.LastEventSequence, state.LastResultID
	replayOnly := false
	if command.After != "" {
		cursor, err := decodeCheckWatchCursor(command.After, scope.WorkspaceID, scope.ProjectID)
		if err != nil {
			return PreparedCheckWatch{}, checkError(CodeCheckRunConflict, "check-watch cursor is outside the requested project")
		}
		if cursor.EventSequence != state.LastEventSequence || cursor.ResultID != state.LastResultID {
			// Preserve enough of a stale exact request for Apply's first operation
			// to return its frozen idempotent response. A new key cannot apply this
			// preparation because its from-state deliberately remains the cursor's
			// old state and fails transactional revalidation.
			fromEventSequence, fromResultID = cursor.EventSequence, cursor.ResultID
			replayOnly = true
		}
	}
	cutoff, err := queries.GetCheckWatchEventCutoff(ctx, scope.WorkspaceID)
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("read check-watch event cutoff", err)
	}
	through := fromEventSequence
	journal := []dbgen.ListCheckWatchJournalEventsRow{}
	if !replayOnly {
		journal, err = queries.ListCheckWatchJournalEvents(ctx, dbgen.ListCheckWatchJournalEventsParams{WorkspaceID: scope.WorkspaceID, AfterSequence: fromEventSequence, CutoffSequence: cutoff, ResultLimit: maximumCheckWatchJournalEvents})
	}
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("inspect check-watch journal", err)
	}
	for _, event := range journal {
		if !knownCheckWatchJournalEvent(event.Type) {
			return PreparedCheckWatch{}, checkError(CodeUnsupportedCheckEvent, fmt.Sprintf("check watch stopped before unsupported workspace event %q at sequence %d", event.Type, event.Sequence))
		}
		through = event.Sequence
	}
	prepared := PreparedCheckWatch{
		WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID,
		RequestedAfter: command.After, RequestedLimit: limit,
		FromEventSequence: fromEventSequence, ThroughEventSequence: through, CutoffEventSequence: cutoff,
		FromResultID: fromResultID, CaughtUp: !replayOnly && through == cutoff, Candidates: []CheckWatchCandidate{},
	}
	prepared.RequestSHA256, err = checkWatchRequestHash(scope.WorkspaceID, scope.ProjectID, command.After, limit)
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("hash check-watch request", err)
	}
	if prepared.CaughtUp {
		rows, err := queries.ListCheckWatchCandidates(ctx, dbgen.ListCheckWatchCandidatesParams{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, AfterResultID: state.LastResultID, ResultLimit: int64(limit + 1)})
		if err != nil {
			return PreparedCheckWatch{}, storageFailure("select check-watch candidates", err)
		}
		// Completing a result page resets the keyset so periodic background passes
		// observe real Git again even when no new Crewfold event exists.
		prepared.ThroughResultID = ""
		if len(rows) > limit {
			rows = rows[:limit]
			prepared.ThroughResultID = rows[len(rows)-1].CheckResultID
		}
		for _, row := range rows {
			prepared.Candidates = append(prepared.Candidates, CheckWatchCandidate{
				CheckResultID: row.CheckResultID, FreshnessRevision: row.FreshnessRevision,
				RepositoryID: row.RepositoryID, RepositoryRevision: row.RepositoryRevision, RepositoryFingerprint: row.RepositoryFingerprint, ObjectFormat: row.ObjectFormat,
				CheckoutID: row.CheckoutID, CheckoutRevision: row.CheckoutRevision, CheckoutPath: row.CheckoutPath,
			})
		}
	} else {
		prepared.ThroughResultID = fromResultID
	}
	prepared.PreparationSHA256, err = hashCheckWatchPreparation(prepared)
	if err != nil {
		return PreparedCheckWatch{}, storageFailure("hash check-watch preparation", err)
	}
	if err := tx.Commit(); err != nil {
		return PreparedCheckWatch{}, storageFailure("finish check-watch preparation", err)
	}
	return prepared, nil
}

func (s *Store) ApplyCheckWatch(ctx context.Context, command ApplyCheckWatchCommand) (MutationResult[domain.CheckWatchReceipt], error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.IdempotencyKey == "" || command.CorrelationID == "" || !validLowerSHA256(command.Preparation.RequestSHA256) || !validLowerSHA256(command.Preparation.PreparationSHA256) {
		return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeInvalidRun, "check-watch apply metadata is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, storageFailure("begin check-watch apply", err)
	}
	defer tx.Rollback()
	if command.PersistNoop {
		var replay MutationResult[domain.CheckWatchReceipt]
		if found, err := lookupIdempotency(ctx, tx, ownerCheckIdempotencyKey(command.IdempotencyKey), "check.watch", command.Preparation.RequestSHA256, &replay); err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		} else if found {
			return replay, nil
		}
	}
	wantPreparationHash, err := hashCheckWatchPreparation(command.Preparation)
	if err != nil || wantPreparationHash != command.Preparation.PreparationSHA256 {
		return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeCheckRunConflict, "check-watch preparation seal differs")
	}
	if len(command.Observations) != len(command.Preparation.Candidates) || len(command.Observations) > maximumCheckWatchCandidates {
		return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeCheckRunConflict, "check-watch observations do not cover the exact prepared candidates")
	}
	queries := dbgen.New(tx)
	state, err := queries.GetCheckWatchState(ctx, dbgen.GetCheckWatchStateParams{WorkspaceID: command.Preparation.WorkspaceID, ProjectID: command.Preparation.ProjectID})
	if err != nil || state.LastEventSequence != command.Preparation.FromEventSequence || state.LastResultID != command.Preparation.FromResultID {
		return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeCheckRunConflict, "check-watch state changed after preparation")
	}
	now := s.nowText()
	if parsed, parseErr := time.Parse(time.RFC3339Nano, state.UpdatedAt); parseErr == nil {
		candidate := s.clock().UTC()
		if !candidate.After(parsed) {
			now = parsed.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano)
		}
	}
	examined := make([]string, 0, len(command.Observations))
	freshnessAppended := 0
	notificationsCreated := 0
	routeFailuresCreated := 0
	repairsMarkedStale := 0
	lastEventSequence := int64(0)
	for index, observed := range command.Observations {
		candidate := command.Preparation.Candidates[index]
		if observed.CheckResultID != candidate.CheckResultID || observed.FreshnessRevision != candidate.FreshnessRevision || !validCheckObservation(observed.Observation) ||
			observed.Observation.RepositoryID != candidate.RepositoryID || observed.Observation.ObjectFormat != candidate.ObjectFormat || observed.Observation.CheckoutID != candidate.CheckoutID {
			return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeCheckRunConflict, "check-watch observation differs from its prepared source")
		}
		current, err := queries.GetCheckWatchCandidateState(ctx, dbgen.GetCheckWatchCandidateStateParams{CheckResultID: candidate.CheckResultID, WorkspaceID: command.Preparation.WorkspaceID, ProjectID: command.Preparation.ProjectID})
		if err != nil || current.FreshnessRevision != candidate.FreshnessRevision || current.RepositoryID != candidate.RepositoryID || current.RepositoryRevision != candidate.RepositoryRevision || current.RepositoryFingerprint != candidate.RepositoryFingerprint || current.ObjectFormat != candidate.ObjectFormat || current.CheckoutID != candidate.CheckoutID || current.CheckoutRevision != candidate.CheckoutRevision || current.CheckoutPath != candidate.CheckoutPath {
			return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeCheckRunConflict, "check-watch candidate changed after preparation")
		}
		status, reason, everStale := observedCheckFreshness(current, observed.Observation)
		examined = append(examined, current.CheckResultID)
		// An observation which leaves the closed freshness state unchanged adds no
		// new mechanical truth. In particular this makes periodic clean-HEAD and
		// already-stale background passes exact no-ops instead of unbounded history.
		if status == current.FreshnessStatus {
			continue
		}
		dirtyJSON, err := json.Marshal(observed.Observation.DirtyPaths)
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, checkError(CodeInvalidRun, "check-watch dirty paths are invalid")
		}
		freshnessID, _ := randomID("checkfresh_")
		evidenceID, _ := randomID("checkevidence_")
		effect := domain.CheckEvidenceInconclusive
		if current.Outcome == domain.CheckOutcomePassed && status == domain.CheckFreshnessFresh {
			effect = domain.CheckEvidenceSupports
		} else if current.Outcome == domain.CheckOutcomeFailed && status == domain.CheckFreshnessFresh {
			effect = domain.CheckEvidenceContradicts
		}
		err = s.withCheckMutationSeal(func() error {
			if err := queries.InsertObservedCheckFreshness(ctx, dbgen.InsertObservedCheckFreshnessParams{
				ID: freshnessID, CheckResultID: current.CheckResultID, Revision: current.FreshnessRevision + 1, Status: status, Reason: reason,
				InitiallyEligible: current.InitiallyEligible, EverStale: boolInteger(everStale), ObservationAvailable: boolInteger(observed.Observation.Available),
				RepositoryID: observed.Observation.RepositoryID, ObjectFormat: observed.Observation.ObjectFormat, CheckoutID: observed.Observation.CheckoutID,
				Branch: observed.Observation.Branch, HeadCommit: observed.Observation.HeadCommit, Dirty: boolInteger(observed.Observation.Dirty), DirtyPathsJson: string(dirtyJSON),
				ObservedAt: observed.Observation.ObservedAt, DiagnosticCode: observed.Observation.DiagnosticCode, Diagnostic: observed.Observation.Diagnostic, CreatedAt: now,
			}); err != nil {
				return err
			}
			return queries.InsertObservedCheckEvidence(ctx, dbgen.InsertObservedCheckEvidenceParams{ID: evidenceID, RequirementID: current.RequirementID, RequirementRevision: current.RequirementRevision, CheckResultID: current.CheckResultID, FreshnessRevision: current.FreshnessRevision + 1, Effect: effect, CreatedAt: now})
		})
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, checkConstraint("append observed check freshness", CodeCheckRunConflict, err)
		}
		if err := s.runMutationHook(MutationAfterCheckFreshness); err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
		if err := s.runMutationHook(MutationAfterCheckEvidence); err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
		eventType := checkFreshnessObservedEvent
		if status == domain.CheckFreshnessStale {
			eventType = checkFreshnessStaleEvent
		}
		lastEventSequence, err = appendEventForActor(ctx, tx, command.Preparation.WorkspaceID, "check_result", current.CheckResultID, current.FreshnessRevision+1, eventType, command.CorrelationID, now, "crewfold-check-worker", "subsystem", map[string]any{"freshness": status, "reason": reason, "observed_at": observed.Observation.ObservedAt})
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
		freshnessAppended++
		if status == domain.CheckFreshnessStale {
			work, err := checkWorkInTransaction(ctx, tx, current.CheckRunID)
			if err != nil {
				return MutationResult[domain.CheckWatchReceipt]{}, err
			}
			created, failures, err := s.materializeStaleCheckNotifications(ctx, tx, work, current.CheckResultID, current.Outcome, current.FreshnessRevision+1, now, command.CorrelationID)
			if err != nil {
				return MutationResult[domain.CheckWatchReceipt]{}, err
			}
			notificationsCreated += created
			routeFailuresCreated += failures
			stale, err := s.markCheckRepairsStale(ctx, tx, command.Preparation.WorkspaceID, current.CheckResultID, current.RequirementID, current.RequirementRevision, true, now, command.CorrelationID)
			if err != nil {
				return MutationResult[domain.CheckWatchReceipt]{}, err
			}
			repairsMarkedStale += stale
		} else if status == domain.CheckFreshnessFresh {
			stale, err := s.markCheckRepairsStale(ctx, tx, command.Preparation.WorkspaceID, current.CheckResultID, current.RequirementID, current.RequirementRevision, false, now, command.CorrelationID)
			if err != nil {
				return MutationResult[domain.CheckWatchReceipt]{}, err
			}
			repairsMarkedStale += stale
		}
	}
	stateChanged := command.Preparation.ThroughEventSequence != command.Preparation.FromEventSequence || command.Preparation.ThroughResultID != command.Preparation.FromResultID
	if stateChanged {
		err = s.withCheckMutationSeal(func() error {
			affected, err := queries.AdvanceCheckWatchState(ctx, dbgen.AdvanceCheckWatchStateParams{
				ThroughEventSequence: command.Preparation.ThroughEventSequence, ThroughResultID: command.Preparation.ThroughResultID, UpdatedAt: now,
				WorkspaceID: command.Preparation.WorkspaceID, ProjectID: command.Preparation.ProjectID,
				FromEventSequence: command.Preparation.FromEventSequence, FromResultID: command.Preparation.FromResultID,
			})
			if err == nil && affected != 1 {
				return errors.New("check-watch state lost prepared revision")
			}
			return err
		})
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, checkConstraint("advance check-watch state", CodeCheckRunConflict, err)
		}
	}
	nextCursor, err := encodeCheckWatchCursor(checkWatchCursor{Version: 1, WorkspaceID: command.Preparation.WorkspaceID, ProjectID: command.Preparation.ProjectID, EventSequence: command.Preparation.ThroughEventSequence, ResultID: command.Preparation.ThroughResultID})
	if err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, storageFailure("encode check-watch cursor", err)
	}
	receiptID, _ := randomID("checkwatch_")
	createdBy := "crewfold-check-worker"
	if command.PersistNoop {
		createdBy = localOwnerActorID
	}
	receipt := domain.CheckWatchReceipt{
		ID: receiptID, WorkspaceID: command.Preparation.WorkspaceID, ProjectID: command.Preparation.ProjectID,
		FromEventSequence: command.Preparation.FromEventSequence, ThroughEventSequence: command.Preparation.ThroughEventSequence, CutoffEventSequence: command.Preparation.CutoffEventSequence,
		CaughtUp: command.Preparation.CaughtUp, Degraded: false, ExaminedResultIDs: examined, FreshnessAppended: freshnessAppended,
		NotificationsCreated: notificationsCreated, RouteFailuresCreated: routeFailuresCreated, RepairsMarkedStale: repairsMarkedStale, NextCursor: nextCursor,
		CreatedAt: now, CreatedBy: createdBy,
	}
	material := freshnessAppended > 0 || notificationsCreated > 0 || routeFailuresCreated > 0 || repairsMarkedStale > 0
	if command.PersistNoop || material {
		content, err := json.Marshal(receipt)
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, storageFailure("encode check-watch receipt", err)
		}
		receipt.ContentSHA256, err = hashBytes(content)
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
		examinedJSON, _ := json.Marshal(examined)
		err = s.withCheckMutationSeal(func() error {
			return queries.InsertCheckWatchReceipt(ctx, dbgen.InsertCheckWatchReceiptParams{
				ID: receipt.ID, WorkspaceID: receipt.WorkspaceID, ProjectID: receipt.ProjectID, FromEventSequence: receipt.FromEventSequence,
				ThroughEventSequence: receipt.ThroughEventSequence, CutoffEventSequence: receipt.CutoffEventSequence, CaughtUp: boolInteger(receipt.CaughtUp),
				ExaminedResultIdsJson: string(examinedJSON), FreshnessAppended: int64(receipt.FreshnessAppended), NotificationsCreated: int64(receipt.NotificationsCreated), RouteFailuresCreated: int64(receipt.RouteFailuresCreated), RepairsMarkedStale: int64(receipt.RepairsMarkedStale),
				NextCursor: receipt.NextCursor, ContentJson: string(content), ContentSha256: receipt.ContentSHA256, CreatedAt: receipt.CreatedAt, CreatedBy: receipt.CreatedBy,
			})
		})
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, checkConstraint("record check-watch receipt", CodeCheckRunConflict, err)
		}
	}
	if material || command.PersistNoop {
		lastEventSequence, err = appendEventForActor(ctx, tx, receipt.WorkspaceID, "check_watch", receipt.ID, 1, checkWatchCompletedEvent, command.CorrelationID, now, createdBy, map[bool]string{true: "human", false: "subsystem"}[command.PersistNoop], map[string]any{"project_id": receipt.ProjectID, "freshness_appended": freshnessAppended, "notifications_created": notificationsCreated, "route_failures_created": routeFailuresCreated, "repairs_marked_stale": repairsMarkedStale})
		if err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
	}
	result := MutationResult[domain.CheckWatchReceipt]{Value: receipt, EventSequence: lastEventSequence}
	if command.PersistNoop {
		if err := recordIdempotency(ctx, tx, ownerCheckIdempotencyKey(command.IdempotencyKey), "check.watch", command.Preparation.RequestSHA256, result, now); err != nil {
			return MutationResult[domain.CheckWatchReceipt]{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.CheckWatchReceipt]{}, storageFailure("commit check-watch pass", err)
	}
	return result, nil
}

func observedCheckFreshness(current dbgen.GetCheckWatchCandidateStateRow, observation domain.CheckGitObservation) (string, string, bool) {
	if current.EverStale != 0 {
		return domain.CheckFreshnessStale, "a prior source observation permanently marked this result stale", true
	}
	if !observation.Available {
		return domain.CheckFreshnessUnknown, "the current source could not be observed", false
	}
	if observation.Dirty {
		return domain.CheckFreshnessStale, "the current checkout has uncommitted source changes", true
	}
	if current.BaselineHeadCommit == nil || observation.HeadCommit != *current.BaselineHeadCommit {
		return domain.CheckFreshnessStale, "the current checkout HEAD differs from the checked source", true
	}
	if current.InitiallyEligible != 0 {
		return domain.CheckFreshnessFresh, "the current clean checkout remains at the checked HEAD", false
	}
	return domain.CheckFreshnessUnknown, "the result was not initially eligible for source verification", false
}

func hashCheckWatchPreparation(prepared PreparedCheckWatch) (string, error) {
	prepared.PreparationSHA256 = ""
	return hashCommand("check.watch.preparation", prepared)
}

func checkWatchRequestHash(workspaceID, projectID, after string, limit int) (string, error) {
	request := struct {
		WorkspaceID string `json:"workspace_id"`
		ProjectID   string `json:"project_id"`
		After       string `json:"after,omitempty"`
		Limit       int    `json:"limit"`
	}{workspaceID, projectID, after, limit}
	return hashCommand("check.watch", request)
}

func hashBytes(value []byte) (string, error) {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func encodeCheckWatchCursor(cursor checkWatchCursor) (string, error) {
	value, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeCheckWatchCursor(value, workspaceID, projectID string) (checkWatchCursor, error) {
	if value == "" || len(value) > maximumCheckWatchCursorBytes {
		return checkWatchCursor{}, errors.New("invalid check-watch cursor")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return checkWatchCursor{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var cursor checkWatchCursor
	if err := decoder.Decode(&cursor); err != nil {
		return checkWatchCursor{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return checkWatchCursor{}, errors.New("invalid trailing cursor content")
	}
	if cursor.Version != 1 || cursor.WorkspaceID != workspaceID || cursor.ProjectID != projectID || cursor.EventSequence < 0 || (cursor.ResultID != "" && !strings.HasPrefix(cursor.ResultID, "checkresult_")) {
		return checkWatchCursor{}, errors.New("check-watch cursor scope differs")
	}
	return cursor, nil
}
