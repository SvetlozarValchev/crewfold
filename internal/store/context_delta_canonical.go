package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

// validateContextDeltaCanonicalProvenance treats a stored delta hash as an
// integrity checksum, not as proof that its nested payload was ever canonical.
// Every read of a run chain replays this check against immutable projections
// and their journaled lifecycle. This keeps a self-consistent raw SQL writer
// from inventing message previews, knowledge bodies, or other live authority.
func (s *Store) validateContextDeltaCanonicalProvenance(ctx context.Context, tx *sql.Tx, packet domain.ContextPacket, delta domain.ContextDelta) error {
	run, err := queryRun(ctx, tx, packet.WorkspaceID, delta.RunID)
	if err != nil {
		return storageFailure("validate context delta canonical run", err)
	}
	if run.ContextPacketID != packet.ID || run.WorkspaceID != delta.WorkspaceID || run.ProjectID != delta.ProjectID ||
		run.TaskID != delta.TaskID || run.AgentID != delta.AgentID {
		return storageFailure("validate context delta canonical scope", errors.New("delta scope differs from its bound run"))
	}
	for _, change := range delta.Changes {
		var cause domain.Event
		if change.Cause.EventSequence != 0 {
			cause, err = contextDeltaCauseEvent(ctx, tx, delta.WorkspaceID, change.Cause.EventSequence)
			if err != nil {
				return err
			}
		}
		switch change.Kind {
		case domain.ContextDeltaMessageReceived:
			err = s.validateCanonicalContextMessage(ctx, tx, run, delta, change, cause)
		case domain.ContextDeltaKnowledgeAccepted:
			err = s.validateCanonicalContextKnowledgeAcceptance(ctx, tx, delta, change, cause)
		case domain.ContextDeltaKnowledgeWithdrawn:
			err = s.validateCanonicalContextKnowledgeWithdrawal(ctx, tx, delta, change, cause)
		case domain.ContextDeltaContradictionOpened, domain.ContextDeltaContradictionClosed:
			err = s.validateCanonicalContextContradiction(ctx, tx, delta, change, cause)
		case domain.ContextDeltaDependentAdded, domain.ContextDeltaDependentUpdated:
			err = validateCanonicalContextDependent(ctx, tx, delta, change, cause)
		case domain.ContextDeltaParticipantRosterUpdated:
			err = validateCanonicalContextParticipantThread(ctx, tx, delta, change, cause)
		default:
			err = fmt.Errorf("unsupported canonical context change kind %q", change.Kind)
		}
		if err != nil {
			return storageFailure("validate context delta canonical payload", err)
		}
	}
	return nil
}

func contextDeltaCauseEvent(ctx context.Context, tx *sql.Tx, workspaceID string, sequence int64) (domain.Event, error) {
	var event domain.Event
	var data string
	err := tx.QueryRowContext(ctx, `SELECT event_id, sequence, type, schema_version, occurred_at, recorded_at,
actor_id, actor_type, workspace_id, entity_type, entity_id, entity_revision,
correlation_id, COALESCE(causation_id, ''), data_json
FROM events WHERE workspace_id = ? AND sequence = ?`, workspaceID, sequence).Scan(
		&event.EventID, &event.Sequence, &event.Type, &event.SchemaVersion,
		&event.OccurredAt, &event.RecordedAt, &event.Actor.ActorID, &event.Actor.ActorType,
		&event.WorkspaceID, &event.Entity.Type, &event.Entity.ID, &event.Entity.Revision,
		&event.CorrelationID, &event.CausationID, &data,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, errors.New("delta cause event is missing")
	}
	if err != nil {
		return domain.Event{}, storageFailure("query context delta cause event", err)
	}
	event.Data = json.RawMessage(data)
	return event, nil
}

func (s *Store) validateCanonicalContextMessage(ctx context.Context, tx *sql.Tx, run domain.Run, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	if change.Cause.Reason != "authorized unseen message sent to this run" || cause.Type != messageSentEvent ||
		cause.Entity.Type != "message" || cause.Entity.ID != change.EntityID || cause.Entity.Revision != 1 {
		return errors.New("message change lacks its exact sent event")
	}
	item, err := queryInboxItem(ctx, tx, change.EntityID, run.AgentID)
	if err != nil {
		return fmt.Errorf("query canonical message: %w", err)
	}
	authorized, err := runAuthorizedForMessage(ctx, tx, run, change.EntityID)
	if err != nil || !authorized {
		return errors.New("message is outside the exact bound run authority")
	}
	status := domain.DeliveryQueued
	var lifecycleType string
	err = tx.QueryRowContext(ctx, `SELECT type FROM events
WHERE workspace_id = ? AND entity_type = 'message' AND entity_id = ? AND sequence <= ?
  AND type IN ('message.delivered','message.read','message.acknowledged')
  AND json_extract(data_json, '$.recipient_agent_id') = ?
ORDER BY sequence DESC LIMIT 1`, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence, run.AgentID).Scan(&lifecycleType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("query historical message delivery: %w", err)
	}
	if lifecycleType == messageDeliveredEvent {
		status = domain.DeliveryDelivered
	} else if lifecycleType != "" {
		return errors.New("message was already read or acknowledged at the delta cursor")
	}
	expected := domain.InboxSummaryItem{
		MessageID: item.Message.ID, ThreadID: item.Message.ThreadID, Kind: item.Message.Kind,
		SenderAgentID: item.Message.SenderAgentID, SenderAgentName: item.Message.SenderAgentName,
		BodyPreview: messagePreview(item.Message.Body, 160), Status: status, CreatedAt: item.Message.CreatedAt,
	}
	if change.Message == nil || *change.Message != expected {
		return errors.New("message preview differs from canonical immutable content or historical delivery")
	}
	var eventData struct {
		ThreadID         string `json:"thread_id"`
		RecipientAgentID string `json:"recipient_agent_id"`
		Kind             string `json:"kind"`
		OriginProjectID  string `json:"origin_project_id"`
		OriginTaskID     string `json:"origin_task_id"`
	}
	if err := json.Unmarshal(cause.Data, &eventData); err != nil || eventData.ThreadID != item.Message.ThreadID ||
		eventData.RecipientAgentID != run.AgentID || eventData.Kind != item.Message.Kind ||
		eventData.OriginProjectID != item.Message.ProjectID || eventData.OriginTaskID != item.Message.TaskID {
		return errors.New("message sent event differs from canonical message authority")
	}
	return nil
}

func (s *Store) validateCanonicalContextKnowledgeAcceptance(ctx context.Context, tx *sql.Tx, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	if change.Knowledge == nil {
		return errors.New("knowledge acceptance has no snapshot")
	}
	if err := s.validateCanonicalKnowledgeSnapshotAt(ctx, tx, delta.WorkspaceID, *change.Knowledge, delta.ThroughEventSequence); err != nil {
		return err
	}
	if change.Knowledge.Type != domain.KnowledgeTypeDecision || change.Knowledge.ProjectID != delta.ProjectID ||
		(change.Knowledge.TaskScopeID != "" && change.Knowledge.TaskScopeID != delta.TaskID) {
		return errors.New("accepted knowledge is outside the exact delta scope")
	}
	if change.Knowledge.FreshnessPolicy == domain.KnowledgeFreshExpiresAt {
		evaluated, evaluatedErr := time.Parse(time.RFC3339Nano, delta.EvaluatedAt)
		freshUntil, freshErr := time.Parse(time.RFC3339Nano, change.Knowledge.FreshUntil)
		if evaluatedErr != nil || freshErr != nil || !evaluated.Before(freshUntil) {
			return errors.New("accepted knowledge was expired when the delta was evaluated")
		}
	}
	openIDs, _, err := canonicalOpenContradictionsAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence)
	if err != nil {
		return err
	}
	if len(openIDs) != 0 {
		return errors.New("accepted knowledge was disputed at the delta cursor")
	}
	switch change.Cause.Reason {
	case "accepted applicable decision":
		if cause.Entity.Type != "knowledge_revision" || cause.Entity.ID != change.EntityID ||
			cause.Entity.Revision != change.Revision || (cause.Type != knowledgeAcceptedEvent && cause.Type != knowledgeImportedEvent) {
			return errors.New("knowledge acceptance lacks its exact governance event")
		}
		var data struct {
			ItemID         string `json:"item_id"`
			ReviewStatus   string `json:"review_status"`
			CurrencyStatus string `json:"currency_status"`
		}
		if err := json.Unmarshal(cause.Data, &data); err != nil || data.ItemID != change.Knowledge.ItemID ||
			(cause.Type == knowledgeImportedEvent && (data.ReviewStatus != domain.KnowledgeReviewAccepted || data.CurrencyStatus != domain.KnowledgeCurrencyCurrent)) {
			return errors.New("knowledge governance event differs from its canonical snapshot")
		}
	case "contradiction_closed_reoffer":
		if cause.Entity.Type != "knowledge_contradiction" ||
			(cause.Type != contradictionDismissedEvent && cause.Type != contradictionResolvedEvent && cause.Type != contradictionImportedEvent) ||
			!contradictionEventNamesRevision(cause.Data, change.EntityID) {
			return errors.New("knowledge reoffer lacks a closing contradiction event")
		}
	default:
		return errors.New("knowledge acceptance reason is not canonical")
	}
	return nil
}

func (s *Store) validateCanonicalContextKnowledgeWithdrawal(ctx context.Context, tx *sql.Tx, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	withdrawal := change.Withdrawal
	if withdrawal == nil || change.Cause.Reason != withdrawal.Reason {
		return errors.New("knowledge withdrawal reason differs from its cause")
	}
	revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, delta.WorkspaceID, change.EntityID)
	if err != nil || !validatePortableKnowledgeRevision(revision) ||
		!knowledgeScopeApplies(revision, delta.WorkspaceID, delta.ProjectID, delta.TaskID) {
		return errors.New("withdrawn knowledge has no canonical revision")
	}
	review, currency, stateRevision, err := canonicalKnowledgeStateAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence)
	if err != nil || stateRevision != change.Revision {
		return errors.New("knowledge withdrawal revision differs at the delta cursor")
	}
	openIDs, openCause, err := canonicalOpenContradictionsAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence)
	if err != nil {
		return err
	}
	wantIDs := openIDs
	if len(wantIDs) > maximumWithdrawalContradictions {
		wantIDs = wantIDs[:maximumWithdrawalContradictions]
	}
	if withdrawal.OpenContradictionCount != len(openIDs) || !reflect.DeepEqual(withdrawal.OpenContradictionIDs, wantIDs) {
		return errors.New("knowledge withdrawal contradiction evidence differs at the delta cursor")
	}
	switch withdrawal.Reason {
	case "stale":
		if review != domain.KnowledgeReviewAccepted || currency != domain.KnowledgeCurrencyStale || cause.Type != knowledgeStaleEvent ||
			cause.Entity.Type != "knowledge_revision" || cause.Entity.ID != change.EntityID || cause.Entity.Revision != change.Revision || withdrawal.ReplacementRevisionID != "" {
			return errors.New("stale withdrawal lacks its exact governance transition")
		}
	case "superseded":
		if review != domain.KnowledgeReviewAccepted || currency != domain.KnowledgeCurrencySuperseded || cause.Type != knowledgeSupersededEvent ||
			cause.Entity.Type != "knowledge_revision" || cause.Entity.ID != change.EntityID || cause.Entity.Revision != change.Revision ||
			canonicalKnowledgeReplacementAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence) != withdrawal.ReplacementRevisionID {
			return errors.New("superseded withdrawal lacks its canonical successor chain")
		}
	case "freshness_expired":
		freshUntil, parseErr := time.Parse(time.RFC3339Nano, revision.FreshUntil)
		evaluated, evaluatedErr := time.Parse(time.RFC3339Nano, delta.EvaluatedAt)
		if review != domain.KnowledgeReviewAccepted || currency != domain.KnowledgeCurrencyCurrent || change.Cause.EventSequence != 0 ||
			revision.FreshnessPolicy != domain.KnowledgeFreshExpiresAt || parseErr != nil || evaluatedErr != nil || evaluated.Before(freshUntil) || withdrawal.ReplacementRevisionID != "" {
			return errors.New("freshness withdrawal was not time-driven at evaluation")
		}
	case "disputed":
		if review != domain.KnowledgeReviewAccepted || currency != domain.KnowledgeCurrencyCurrent || len(openIDs) == 0 ||
			change.Cause.EventSequence != openCause || cause.Entity.Type != "knowledge_contradiction" ||
			(cause.Type != contradictionConfirmedEvent && cause.Type != contradictionImportedEvent) ||
			!contradictionEventNamesRevision(cause.Data, change.EntityID) || withdrawal.ReplacementRevisionID != "" {
			return errors.New("disputed withdrawal lacks its exact open contradiction set")
		}
	default:
		return errors.New("knowledge withdrawal reason is not canonical")
	}
	return nil
}

func (s *Store) validateCanonicalKnowledgeSnapshotAt(ctx context.Context, tx *sql.Tx, workspaceID string, snapshot domain.KnowledgeRevision, through int64) error {
	if !validatePortableKnowledgeRevision(snapshot) {
		return errors.New("knowledge snapshot is structurally invalid")
	}
	canonical, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspaceID, snapshot.ID)
	if err != nil || !validatePortableKnowledgeRevision(canonical) {
		return errors.New("knowledge snapshot has no valid canonical revision")
	}
	review, currency, stateRevision, err := canonicalKnowledgeStateAt(ctx, tx, workspaceID, snapshot.ID, through)
	if err != nil || snapshot.ReviewStatus != review || snapshot.CurrencyStatus != currency || snapshot.StateRevision != stateRevision {
		return errors.New("knowledge snapshot governance differs at the delta cursor")
	}
	expected := canonical
	if currency == domain.KnowledgeCurrencyCurrent {
		expected.StateRevision = stateRevision
		expected.ReviewStatus, expected.CurrencyStatus = review, currency
		expected.StaleAt, expected.StaleBy, expected.StaleByType, expected.StaleReason = "", "", "", ""
	}
	if !reflect.DeepEqual(snapshot, expected) {
		return errors.New("knowledge snapshot immutable content or lifecycle differs from canonical authority")
	}
	return nil
}

func canonicalKnowledgeStateAt(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string, through int64) (string, string, int64, error) {
	var eventType, data string
	var stateRevision int64
	err := tx.QueryRowContext(ctx, `SELECT type, entity_revision, data_json FROM events
WHERE workspace_id = ? AND entity_type = 'knowledge_revision' AND entity_id = ? AND sequence <= ?
  AND type IN ('knowledge.proposed','knowledge.accepted','knowledge.rejected','knowledge.marked_stale','knowledge.superseded','knowledge.imported')
ORDER BY sequence DESC LIMIT 1`, workspaceID, revisionID, through).Scan(&eventType, &stateRevision, &data)
	if err != nil {
		return "", "", 0, fmt.Errorf("query historical knowledge state: %w", err)
	}
	switch eventType {
	case knowledgeProposedEvent:
		return domain.KnowledgeReviewProposed, domain.KnowledgeCurrencyPending, stateRevision, nil
	case knowledgeAcceptedEvent:
		return domain.KnowledgeReviewAccepted, domain.KnowledgeCurrencyCurrent, stateRevision, nil
	case knowledgeRejectedEvent:
		return domain.KnowledgeReviewRejected, domain.KnowledgeCurrencyPending, stateRevision, nil
	case knowledgeStaleEvent:
		return domain.KnowledgeReviewAccepted, domain.KnowledgeCurrencyStale, stateRevision, nil
	case knowledgeSupersededEvent:
		return domain.KnowledgeReviewAccepted, domain.KnowledgeCurrencySuperseded, stateRevision, nil
	case knowledgeImportedEvent:
		var imported struct {
			ReviewStatus   string `json:"review_status"`
			CurrencyStatus string `json:"currency_status"`
		}
		if err := json.Unmarshal([]byte(data), &imported); err != nil || !domain.ValidKnowledgeReviewStatus(imported.ReviewStatus) || !domain.ValidKnowledgeCurrencyStatus(imported.CurrencyStatus) {
			return "", "", 0, errors.New("imported knowledge event has invalid governance")
		}
		return imported.ReviewStatus, imported.CurrencyStatus, stateRevision, nil
	default:
		return "", "", 0, errors.New("knowledge has no canonical historical state")
	}
}

func canonicalOpenContradictionsAt(ctx context.Context, tx *sql.Tx, workspaceID, revisionID string, through int64) ([]string, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT contradiction.id,
  COALESCE((SELECT json_extract(event.data_json, '$.status') FROM events event
    WHERE event.workspace_id = contradiction.workspace_id
      AND event.entity_type = 'knowledge_contradiction' AND event.entity_id = contradiction.id
      AND event.sequence <= ? AND event.type IN ('contradiction.detected','contradiction.confirmed','contradiction.dismissed','contradiction.resolved','contradiction.imported')
    ORDER BY event.sequence DESC LIMIT 1), ''),
  COALESCE((SELECT event.sequence FROM events event
    WHERE event.workspace_id = contradiction.workspace_id
      AND event.entity_type = 'knowledge_contradiction' AND event.entity_id = contradiction.id
      AND event.sequence <= ? AND event.type IN ('contradiction.detected','contradiction.confirmed','contradiction.dismissed','contradiction.resolved','contradiction.imported')
    ORDER BY event.sequence DESC LIMIT 1), 0)
FROM knowledge_contradictions contradiction
WHERE contradiction.workspace_id = ? AND (contradiction.left_revision_id = ? OR contradiction.right_revision_id = ?)
ORDER BY contradiction.id`, through, through, workspaceID, revisionID, revisionID)
	if err != nil {
		return nil, 0, fmt.Errorf("query historical contradictions: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	var latest int64
	for rows.Next() {
		var id, status string
		var sequence int64
		if err := rows.Scan(&id, &status, &sequence); err != nil {
			return nil, 0, err
		}
		if status == domain.KnowledgeContradictionOpen {
			ids = append(ids, id)
			latest = max(latest, sequence)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return ids, latest, nil
}

func canonicalKnowledgeReplacementAt(ctx context.Context, tx *sql.Tx, workspaceID, withdrawnID string, through int64) string {
	current := withdrawnID
	for range 200 {
		var successorID string
		err := tx.QueryRowContext(ctx, `SELECT successor.id FROM knowledge_revisions successor
JOIN knowledge_items item ON item.id = successor.item_id
JOIN events accepted ON accepted.workspace_id = item.workspace_id
  AND accepted.entity_type = 'knowledge_revision' AND accepted.entity_id = successor.id
  AND accepted.type IN ('knowledge.accepted','knowledge.imported') AND accepted.sequence <= ?
WHERE item.workspace_id = ? AND successor.supersedes_revision_id = ?
ORDER BY accepted.sequence DESC, successor.id LIMIT 1`, through, workspaceID, current).Scan(&successorID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil || successorID == current {
			return ""
		}
		current = successorID
	}
	if current == withdrawnID {
		return ""
	}
	review, currency, _, err := canonicalKnowledgeStateAt(ctx, tx, workspaceID, current, through)
	if err != nil || review != domain.KnowledgeReviewAccepted || currency != domain.KnowledgeCurrencyCurrent {
		return ""
	}
	return current
}

func (s *Store) validateCanonicalContextContradiction(ctx context.Context, tx *sql.Tx, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	if change.Contradiction == nil || cause.Entity.Type != "knowledge_contradiction" || cause.Entity.ID != change.EntityID || cause.Entity.Revision != change.Revision {
		return errors.New("contradiction change lacks its exact lifecycle event")
	}
	row, err := dbgen.New(tx).GetKnowledgeContradiction(ctx, dbgen.GetKnowledgeContradictionParams{WorkspaceID: delta.WorkspaceID, ID: change.EntityID})
	if err != nil {
		return fmt.Errorf("query canonical contradiction: %w", err)
	}
	canonical := knowledgeContradictionFromRow(row)
	if canonical.ProjectID != delta.ProjectID ||
		!knowledgeScopeApplies(change.Contradiction.LeftRevision, delta.WorkspaceID, delta.ProjectID, delta.TaskID) ||
		!knowledgeScopeApplies(change.Contradiction.RightRevision, delta.WorkspaceID, delta.ProjectID, delta.TaskID) {
		return errors.New("contradiction is outside the exact delta scope")
	}
	status, stateRevision, err := canonicalContradictionStateAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence)
	if err != nil || change.Contradiction.Contradiction.Status != status || change.Contradiction.Contradiction.StateRevision != stateRevision {
		return errors.New("contradiction snapshot governance differs at the delta cursor")
	}
	expected := canonicalContradictionAtStatus(canonical, status, stateRevision)
	if !reflect.DeepEqual(change.Contradiction.Contradiction, expected) {
		return errors.New("contradiction snapshot differs from canonical identity or lifecycle")
	}
	if err := s.validateCanonicalKnowledgeSnapshotAt(ctx, tx, delta.WorkspaceID, change.Contradiction.LeftRevision, delta.ThroughEventSequence); err != nil {
		return err
	}
	if err := s.validateCanonicalKnowledgeSnapshotAt(ctx, tx, delta.WorkspaceID, change.Contradiction.RightRevision, delta.ThroughEventSequence); err != nil {
		return err
	}
	if change.Contradiction.LeftRevision.ID != canonical.LeftRevisionID || change.Contradiction.RightRevision.ID != canonical.RightRevisionID {
		return errors.New("contradiction participant pair differs from canonical identity")
	}
	if change.Kind == domain.ContextDeltaContradictionOpened {
		if change.Cause.Reason != "applicable exact-revision contradiction is open" || status != domain.KnowledgeContradictionOpen ||
			(cause.Type != contradictionConfirmedEvent && cause.Type != contradictionImportedEvent) {
			return errors.New("opened contradiction lacks an opening lifecycle event")
		}
	} else if change.Cause.Reason != "previously delivered contradiction is closed" ||
		(status != domain.KnowledgeContradictionDismissed && status != domain.KnowledgeContradictionResolved) ||
		(cause.Type != contradictionDismissedEvent && cause.Type != contradictionResolvedEvent && cause.Type != contradictionImportedEvent) {
		return errors.New("closed contradiction lacks a closing lifecycle event")
	}
	return nil
}

func canonicalContradictionStateAt(ctx context.Context, tx *sql.Tx, workspaceID, contradictionID string, through int64) (string, int64, error) {
	var data string
	var stateRevision int64
	err := tx.QueryRowContext(ctx, `SELECT data_json, entity_revision FROM events
WHERE workspace_id = ? AND entity_type = 'knowledge_contradiction' AND entity_id = ? AND sequence <= ?
  AND type IN ('contradiction.detected','contradiction.confirmed','contradiction.dismissed','contradiction.resolved','contradiction.imported')
ORDER BY sequence DESC LIMIT 1`, workspaceID, contradictionID, through).Scan(&data, &stateRevision)
	if err != nil {
		return "", 0, fmt.Errorf("query historical contradiction state: %w", err)
	}
	var value struct {
		Status        string `json:"status"`
		StateRevision int64  `json:"state_revision"`
	}
	if err := json.Unmarshal([]byte(data), &value); err != nil || !domain.ValidKnowledgeContradictionStatus(value.Status) || value.StateRevision != stateRevision {
		return "", 0, errors.New("contradiction lifecycle event has invalid state")
	}
	return value.Status, stateRevision, nil
}

func canonicalContradictionAtStatus(value domain.KnowledgeContradiction, status string, stateRevision int64) domain.KnowledgeContradiction {
	value.Status, value.StateRevision = status, stateRevision
	if status == domain.KnowledgeContradictionProposed || status == domain.KnowledgeContradictionOpen {
		value.DismissedAt, value.DismissedBy, value.DismissedByType, value.DismissNote = "", "", "", ""
		value.DismissEventSequence = 0
		value.ResolutionReason, value.ResolvedAt, value.ResolvedBy, value.ResolvedByType, value.ResolutionNote = "", "", "", "", ""
		value.ResolutionEventSequence, value.ResolutionCauseEventSequence = 0, 0
	}
	if status == domain.KnowledgeContradictionProposed {
		value.ConfirmedAt, value.ConfirmedBy, value.ConfirmedByType, value.ConfirmNote = "", "", "", ""
		value.ConfirmEventSequence = 0
	}
	return value
}

func validateCanonicalContextDependent(ctx context.Context, tx *sql.Tx, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	if change.Dependency == nil || cause.Entity.Type != "task" || cause.Entity.ID != change.EntityID || cause.Entity.Revision != change.Revision {
		return errors.New("dependent change lacks its task event")
	}
	expected, err := canonicalContextDependentAt(ctx, tx, delta.WorkspaceID, change.EntityID, delta.ThroughEventSequence)
	if err != nil || *change.Dependency != expected {
		return errors.New("dependent snapshot differs from canonical task history")
	}
	dependent, err := queryTask(ctx, tx, delta.WorkspaceID, change.EntityID)
	if err != nil || dependent.ProjectID != delta.ProjectID {
		return errors.New("dependent task is outside the exact delta project")
	}
	edgeAfter := int64(-1)
	if change.Kind == domain.ContextDeltaDependentAdded {
		edgeAfter = delta.FromEventSequence
	}
	var relationAdded int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events
WHERE workspace_id = ? AND entity_type = 'task' AND entity_id = ? AND type = 'task.dependency_added'
	  AND sequence > ? AND sequence <= ? AND json_extract(data_json, '$.depends_on_task_id') = ?)`,
		delta.WorkspaceID, change.EntityID, edgeAfter, delta.ThroughEventSequence, delta.TaskID).Scan(&relationAdded); err != nil || relationAdded != 1 {
		return errors.New("reverse dependent lacks its canonical dependency edge")
	}
	if change.Kind == domain.ContextDeltaDependentAdded {
		if change.Cause.Reason != "task became a direct reverse dependent" {
			return errors.New("dependent addition reason is not canonical")
		}
	} else if change.Cause.Reason != "represented reverse dependent snapshot changed" {
		return errors.New("dependent update reason is not canonical")
	}
	return nil
}

func canonicalContextDependentAt(ctx context.Context, tx *sql.Tx, workspaceID, taskID string, through int64) (domain.ContextDependency, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type, entity_revision, data_json FROM events
WHERE workspace_id = ? AND entity_type = 'task' AND entity_id = ? AND sequence <= ? ORDER BY sequence`, workspaceID, taskID, through)
	if err != nil {
		return domain.ContextDependency{}, err
	}
	defer rows.Close()
	result := domain.ContextDependency{TaskID: taskID}
	for rows.Next() {
		var eventType string
		var revision int64
		var data string
		if err := rows.Scan(&eventType, &revision, &data); err != nil {
			return domain.ContextDependency{}, err
		}
		var value struct {
			Title  *string `json:"title"`
			Status *string `json:"status"`
		}
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			return domain.ContextDependency{}, err
		}
		if value.Title != nil {
			result.Title = *value.Title
		}
		if value.Status != nil {
			result.Status = *value.Status
		} else {
			// Task creation has always meant ready, even though the event image
			// predates an explicit status field. A few lifecycle events likewise
			// encode their transition in the event type. Reconstruct those stable
			// semantics instead of consulting the mutable current projection.
			switch eventType {
			case taskCreated:
				result.Status = domain.TaskReady
			case taskAssigned, "task.reassigned":
				result.Status = domain.TaskAssigned
			case taskStarted:
				result.Status = domain.TaskActive
			case taskBlocked:
				result.Status = domain.TaskBlocked
			case taskCompletionProposed:
				result.Status = domain.TaskReview
			case taskChangesRequestedEvent:
				result.Status = domain.TaskChangesRequested
			case taskCompletedEvent:
				result.Status = domain.TaskCompleted
			case taskFailedEvent:
				result.Status = domain.TaskFailed
			case taskCancelled:
				result.Status = domain.TaskCancelled
			}
		}
		result.Revision = revision
	}
	if err := rows.Err(); err != nil {
		return domain.ContextDependency{}, err
	}
	if result.Title == "" || result.Status == "" || result.Revision < 1 {
		return domain.ContextDependency{}, errors.New("dependent task history is incomplete")
	}
	return result, nil
}

func validateCanonicalContextParticipantThread(ctx context.Context, tx *sql.Tx, delta domain.ContextDelta, change domain.ContextDeltaChange, cause domain.Event) error {
	if change.ParticipantThread == nil || change.Cause.Reason != "exact participant roster changed" {
		return errors.New("participant roster change is incomplete")
	}
	canonical, err := participantThreadInTransaction(ctx, dbgen.New(tx), delta.WorkspaceID, change.EntityID)
	if err != nil {
		return err
	}
	expected, err := canonicalContextParticipantThreadAt(ctx, tx, canonical, delta.ThroughEventSequence)
	if err != nil || !reflect.DeepEqual(*change.ParticipantThread, expected) {
		return errors.New("participant roster differs from canonical thread history")
	}
	if cause.Entity.Type == "thread" {
		if cause.Entity.ID != change.EntityID || (cause.Type != threadCreatedEvent && cause.Type != threadParticipantAdded) {
			return errors.New("participant roster cause is not a thread lifecycle event")
		}
	} else if cause.Entity.Type == "message" && cause.Type == messageSentEvent {
		var data struct {
			ThreadID         string `json:"thread_id"`
			RecipientAgentID string `json:"recipient_agent_id"`
		}
		if err := json.Unmarshal(cause.Data, &data); err != nil || data.ThreadID != change.EntityID || data.RecipientAgentID != delta.AgentID {
			return errors.New("participant roster message cause names another thread")
		}
	} else {
		return errors.New("participant roster has no canonical cause event")
	}
	return nil
}

func canonicalContextParticipantThreadAt(ctx context.Context, tx *sql.Tx, current domain.ParticipantThread, through int64) (domain.ParticipantThread, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type, occurred_at, actor_id FROM events
WHERE workspace_id = ? AND sequence <= ? AND (
  (entity_type = 'thread' AND entity_id = ? AND type IN ('thread.created','thread.participant_added')) OR
  (entity_type = 'message' AND type = 'message.sent' AND json_extract(data_json, '$.thread_id') = ?))
ORDER BY sequence`, current.Thread.WorkspaceID, through, current.Thread.ID, current.Thread.ID)
	if err != nil {
		return domain.ParticipantThread{}, err
	}
	defer rows.Close()
	result := current
	result.Participants = nil
	result.Thread.Revision = 0
	result.ParticipantRevision = 1
	for rows.Next() {
		var eventType, occurredAt, actorID string
		if err := rows.Scan(&eventType, &occurredAt, &actorID); err != nil {
			return domain.ParticipantThread{}, err
		}
		result.Thread.Revision++
		result.Thread.UpdatedAt, result.Thread.UpdatedBy = occurredAt, actorID
		if eventType == threadParticipantAdded {
			result.ParticipantRevision++
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ParticipantThread{}, err
	}
	var initial int
	if err := tx.QueryRowContext(ctx, "SELECT initial_participant_count FROM message_threads WHERE id = ?", current.Thread.ID).Scan(&initial); err != nil {
		return domain.ParticipantThread{}, err
	}
	count := initial + int(result.ParticipantRevision) - 1
	if result.Thread.Revision < 1 || count < 2 || count > len(current.Participants) {
		return domain.ParticipantThread{}, errors.New("participant thread history is incomplete")
	}
	result.Participants = append([]domain.ThreadParticipant(nil), current.Participants[:count]...)
	return result, nil
}

func contradictionEventNamesRevision(data json.RawMessage, revisionID string) bool {
	var value struct {
		LeftRevisionID  string `json:"left_revision_id"`
		RightRevisionID string `json:"right_revision_id"`
	}
	return json.Unmarshal(data, &value) == nil && (value.LeftRevisionID == revisionID || value.RightRevisionID == revisionID)
}
