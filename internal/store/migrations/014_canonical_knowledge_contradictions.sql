CREATE TABLE knowledge_contradictions (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'kcon_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    left_revision_id TEXT NOT NULL REFERENCES knowledge_revisions(id),
    right_revision_id TEXT NOT NULL REFERENCES knowledge_revisions(id),
    status TEXT NOT NULL CHECK (status IN ('proposed', 'open', 'resolved', 'dismissed')),
    state_revision INTEGER NOT NULL CHECK (state_revision > 0),
    report_note TEXT NOT NULL CHECK (
        length(CAST(report_note AS BLOB)) BETWEEN 1 AND 2048
        AND instr(report_note, char(0)) = 0
    ),
    reported_at TEXT NOT NULL,
    reported_by TEXT NOT NULL,
    reported_by_type TEXT NOT NULL CHECK (reported_by_type IN ('human', 'agent_run')),
    detected_event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    confirmed_at TEXT,
    confirmed_by TEXT,
    confirmed_by_type TEXT CHECK (confirmed_by_type = 'human'),
    confirm_note TEXT CHECK (
        confirm_note IS NULL OR (
            length(CAST(confirm_note AS BLOB)) BETWEEN 1 AND 2048
            AND instr(confirm_note, char(0)) = 0
        )
    ),
    confirm_event_sequence INTEGER UNIQUE REFERENCES events(sequence),
    dismissed_at TEXT,
    dismissed_by TEXT,
    dismissed_by_type TEXT CHECK (dismissed_by_type = 'human'),
    dismiss_note TEXT CHECK (
        dismiss_note IS NULL OR (
            length(CAST(dismiss_note AS BLOB)) BETWEEN 1 AND 2048
            AND instr(dismiss_note, char(0)) = 0
        )
    ),
    dismiss_event_sequence INTEGER UNIQUE REFERENCES events(sequence),
    resolution_reason TEXT CHECK (
        resolution_reason IS NULL OR resolution_reason IN ('participant_stale', 'participant_superseded')
    ),
    resolved_at TEXT,
    resolved_by TEXT,
    resolved_by_type TEXT CHECK (resolved_by_type = 'human'),
    resolution_note TEXT CHECK (
        resolution_note IS NULL OR (
            length(CAST(resolution_note AS BLOB)) BETWEEN 1 AND 2048
            AND instr(resolution_note, char(0)) = 0
        )
    ),
    resolution_event_sequence INTEGER UNIQUE REFERENCES events(sequence),
    resolution_cause_event_sequence INTEGER REFERENCES events(sequence),
    UNIQUE (workspace_id, left_revision_id, right_revision_id),
    CHECK (left_revision_id < right_revision_id),
    CHECK (
        (status = 'proposed'
            AND confirmed_at IS NULL AND confirmed_by IS NULL AND confirmed_by_type IS NULL AND confirm_note IS NULL AND confirm_event_sequence IS NULL
            AND dismissed_at IS NULL AND dismissed_by IS NULL AND dismissed_by_type IS NULL AND dismiss_note IS NULL AND dismiss_event_sequence IS NULL
            AND resolution_reason IS NULL AND resolved_at IS NULL AND resolved_by IS NULL
            AND resolved_by_type IS NULL AND resolution_note IS NULL AND resolution_event_sequence IS NULL
            AND resolution_cause_event_sequence IS NULL) OR
        (status = 'open'
            AND confirmed_at IS NOT NULL AND confirmed_by IS NOT NULL AND confirmed_by_type IS NOT NULL
            AND confirm_event_sequence IS NOT NULL
            AND dismissed_at IS NULL AND dismissed_by IS NULL AND dismissed_by_type IS NULL AND dismiss_note IS NULL AND dismiss_event_sequence IS NULL
            AND resolution_reason IS NULL AND resolved_at IS NULL AND resolved_by IS NULL
            AND resolved_by_type IS NULL AND resolution_note IS NULL AND resolution_event_sequence IS NULL
            AND resolution_cause_event_sequence IS NULL) OR
        (status = 'dismissed'
            AND dismissed_at IS NOT NULL AND dismissed_by IS NOT NULL AND dismissed_by_type IS NOT NULL
            AND dismiss_event_sequence IS NOT NULL
            AND resolution_reason IS NULL AND resolved_at IS NULL AND resolved_by IS NULL
            AND resolved_by_type IS NULL AND resolution_note IS NULL AND resolution_event_sequence IS NULL
            AND resolution_cause_event_sequence IS NULL
            AND ((confirmed_at IS NULL AND confirmed_by IS NULL AND confirmed_by_type IS NULL AND confirm_note IS NULL AND confirm_event_sequence IS NULL)
              OR (confirmed_at IS NOT NULL AND confirmed_by IS NOT NULL AND confirmed_by_type IS NOT NULL AND confirm_event_sequence IS NOT NULL))) OR
        (status = 'resolved'
            AND confirmed_at IS NOT NULL AND confirmed_by IS NOT NULL AND confirmed_by_type IS NOT NULL
            AND confirm_event_sequence IS NOT NULL
            AND dismissed_at IS NULL AND dismissed_by IS NULL AND dismissed_by_type IS NULL AND dismiss_note IS NULL AND dismiss_event_sequence IS NULL
            AND resolution_reason IS NOT NULL AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL
            AND resolved_by_type IS NOT NULL AND resolution_note IS NOT NULL AND resolution_event_sequence IS NOT NULL
            AND resolution_cause_event_sequence IS NOT NULL)
    )
) STRICT;

CREATE INDEX knowledge_contradictions_project_idx
    ON knowledge_contradictions(workspace_id, project_id, reported_at, id);

CREATE INDEX knowledge_contradictions_left_open_idx
    ON knowledge_contradictions(left_revision_id, id) WHERE status = 'open';

CREATE INDEX knowledge_contradictions_right_open_idx
    ON knowledge_contradictions(right_revision_id, id) WHERE status = 'open';

CREATE TRIGGER knowledge_contradiction_reject_invalid_utf8_insert
BEFORE INSERT ON knowledge_contradictions
WHEN crewfold_utf8_valid(NEW.report_note) != 1
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction report note must be valid UTF-8');
END;

CREATE TRIGGER knowledge_contradiction_reject_invalid_utf8_update
BEFORE UPDATE OF confirm_note, dismiss_note, resolution_note ON knowledge_contradictions
WHEN (NEW.confirm_note IS NOT NULL AND crewfold_utf8_valid(NEW.confirm_note) != 1)
  OR (NEW.dismiss_note IS NOT NULL AND crewfold_utf8_valid(NEW.dismiss_note) != 1)
  OR (NEW.resolution_note IS NOT NULL AND crewfold_utf8_valid(NEW.resolution_note) != 1)
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction lifecycle notes must be valid UTF-8');
END;

CREATE TRIGGER knowledge_contradiction_validate_insert
BEFORE INSERT ON knowledge_contradictions
WHEN NOT (
    NEW.status = 'proposed' AND NEW.state_revision = 1
    AND EXISTS (
        SELECT 1
        FROM knowledge_revisions left_revision
        JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
        JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
        JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
        JOIN projects p ON p.id = NEW.project_id
        WHERE left_revision.id = NEW.left_revision_id
          AND left_item.workspace_id = NEW.workspace_id
          AND right_item.workspace_id = NEW.workspace_id
          AND p.workspace_id = NEW.workspace_id
          AND left_item.project_id = NEW.project_id
          AND right_item.project_id = NEW.project_id
          AND left_item.id != right_item.id
          AND left_revision.review_status = 'accepted' AND left_revision.currency_status = 'current'
          AND right_revision.review_status = 'accepted' AND right_revision.currency_status = 'current'
          AND (left_item.task_scope_id IS NULL OR right_item.task_scope_id IS NULL
               OR left_item.task_scope_id = right_item.task_scope_id)
    )
    AND EXISTS (
        SELECT 1 FROM events e
        WHERE e.sequence = NEW.detected_event_sequence
          AND e.type = 'contradiction.detected'
          AND e.actor_id = NEW.reported_by AND e.actor_type = NEW.reported_by_type
          AND e.workspace_id = NEW.workspace_id
          AND e.entity_type = 'knowledge_contradiction'
          AND e.entity_id = NEW.id AND e.entity_revision = 1
          AND e.occurred_at = NEW.reported_at
    )
    AND (
        (NEW.reported_by = 'local-owner' AND NEW.reported_by_type = 'human') OR
        (NEW.reported_by_type = 'agent_run' AND EXISTS (
            SELECT 1
            FROM runs r
            JOIN knowledge_revisions left_revision ON left_revision.id = NEW.left_revision_id
            JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
            JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
            JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
            WHERE r.id = NEW.reported_by
              AND r.workspace_id = NEW.workspace_id AND r.project_id = NEW.project_id
              AND r.status IN ('starting', 'active', 'blocked')
              AND (left_item.task_scope_id IS NULL OR left_item.task_scope_id = r.task_id)
              AND (right_item.task_scope_id IS NULL OR right_item.task_scope_id = r.task_id)
        ))
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge contradiction report');
END;

CREATE TRIGGER knowledge_contradiction_reject_immutable_update
BEFORE UPDATE OF id, workspace_id, project_id, left_revision_id, right_revision_id,
    report_note, reported_at, reported_by, reported_by_type, detected_event_sequence
ON knowledge_contradictions
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction identity and report are immutable');
END;

CREATE TRIGGER knowledge_contradiction_reject_illegal_transition
BEFORE UPDATE OF status, state_revision, confirmed_at, confirmed_by, confirmed_by_type,
    confirm_note, confirm_event_sequence, dismissed_at, dismissed_by, dismissed_by_type, dismiss_note,
    dismiss_event_sequence, resolution_reason, resolved_at, resolved_by, resolved_by_type,
    resolution_note, resolution_event_sequence, resolution_cause_event_sequence
ON knowledge_contradictions
WHEN NOT (
    (
        OLD.status = 'proposed' AND NEW.status = 'open'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.confirmed_at IS NOT NULL AND NEW.confirmed_by IS NOT NULL AND NEW.confirmed_by_type IS NOT NULL
        AND NEW.confirm_event_sequence IS NOT NULL
        AND NEW.dismissed_at IS OLD.dismissed_at AND NEW.dismissed_by IS OLD.dismissed_by
        AND NEW.dismissed_by_type IS OLD.dismissed_by_type AND NEW.dismiss_note IS OLD.dismiss_note
        AND NEW.dismiss_event_sequence IS OLD.dismiss_event_sequence
        AND NEW.resolution_reason IS OLD.resolution_reason AND NEW.resolved_at IS OLD.resolved_at
        AND NEW.resolved_by IS OLD.resolved_by AND NEW.resolved_by_type IS OLD.resolved_by_type
        AND NEW.resolution_note IS OLD.resolution_note AND NEW.resolution_event_sequence IS OLD.resolution_event_sequence
        AND NEW.resolution_cause_event_sequence IS OLD.resolution_cause_event_sequence
        AND EXISTS (
            SELECT 1 FROM events e
            WHERE e.sequence = NEW.confirm_event_sequence
              AND e.type = 'contradiction.confirmed' AND e.workspace_id = NEW.workspace_id
              AND e.entity_type = 'knowledge_contradiction' AND e.entity_id = NEW.id
              AND e.entity_revision = NEW.state_revision AND e.actor_id = NEW.confirmed_by
              AND e.actor_type = NEW.confirmed_by_type AND e.occurred_at = NEW.confirmed_at
        )
        AND EXISTS (
            SELECT 1 FROM knowledge_contradiction_authority_checks a
            WHERE a.contradiction_id = NEW.id AND a.action = 'confirm'
              AND a.outcome = 'allowed' AND a.reason = 'workspace_owner'
              AND a.event_sequence = NEW.confirm_event_sequence
              AND a.actor_id = NEW.confirmed_by AND a.actor_type = NEW.confirmed_by_type
              AND a.note IS NEW.confirm_note
        )
        AND EXISTS (
            SELECT 1
            FROM knowledge_revisions left_revision
            JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
            JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
            JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
            WHERE left_revision.id = NEW.left_revision_id
              AND left_item.workspace_id = NEW.workspace_id
              AND right_item.workspace_id = NEW.workspace_id
              AND left_item.project_id = NEW.project_id AND right_item.project_id = NEW.project_id
              AND left_item.id != right_item.id
              AND left_revision.review_status = 'accepted' AND left_revision.currency_status = 'current'
              AND right_revision.review_status = 'accepted' AND right_revision.currency_status = 'current'
              AND (left_item.task_scope_id IS NULL OR right_item.task_scope_id IS NULL
                   OR left_item.task_scope_id = right_item.task_scope_id)
        )
    ) OR (
        OLD.status IN ('proposed', 'open') AND NEW.status = 'dismissed'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.confirmed_at IS OLD.confirmed_at AND NEW.confirmed_by IS OLD.confirmed_by
        AND NEW.confirmed_by_type IS OLD.confirmed_by_type AND NEW.confirm_note IS OLD.confirm_note
        AND NEW.confirm_event_sequence IS OLD.confirm_event_sequence
        AND NEW.dismissed_at IS NOT NULL AND NEW.dismissed_by IS NOT NULL AND NEW.dismissed_by_type IS NOT NULL
        AND NEW.dismiss_event_sequence IS NOT NULL
        AND NEW.resolution_reason IS OLD.resolution_reason AND NEW.resolved_at IS OLD.resolved_at
        AND NEW.resolved_by IS OLD.resolved_by AND NEW.resolved_by_type IS OLD.resolved_by_type
        AND NEW.resolution_note IS OLD.resolution_note AND NEW.resolution_event_sequence IS OLD.resolution_event_sequence
        AND NEW.resolution_cause_event_sequence IS OLD.resolution_cause_event_sequence
        AND EXISTS (
            SELECT 1 FROM events e
            WHERE e.sequence = NEW.dismiss_event_sequence
              AND e.type = 'contradiction.dismissed' AND e.workspace_id = NEW.workspace_id
              AND e.entity_type = 'knowledge_contradiction' AND e.entity_id = NEW.id
              AND e.entity_revision = NEW.state_revision AND e.actor_id = NEW.dismissed_by
              AND e.actor_type = NEW.dismissed_by_type AND e.occurred_at = NEW.dismissed_at
        )
        AND EXISTS (
            SELECT 1 FROM knowledge_contradiction_authority_checks a
            WHERE a.contradiction_id = NEW.id AND a.action = 'dismiss'
              AND a.outcome = 'allowed' AND a.reason = 'workspace_owner'
              AND a.event_sequence = NEW.dismiss_event_sequence
              AND a.actor_id = NEW.dismissed_by AND a.actor_type = NEW.dismissed_by_type
              AND a.note IS NEW.dismiss_note
        )
    ) OR (
        OLD.status = 'open' AND NEW.status = 'resolved'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.confirmed_at IS OLD.confirmed_at AND NEW.confirmed_by IS OLD.confirmed_by
        AND NEW.confirmed_by_type IS OLD.confirmed_by_type AND NEW.confirm_note IS OLD.confirm_note
        AND NEW.confirm_event_sequence IS OLD.confirm_event_sequence
        AND NEW.dismissed_at IS OLD.dismissed_at AND NEW.dismissed_by IS OLD.dismissed_by
        AND NEW.dismissed_by_type IS OLD.dismissed_by_type AND NEW.dismiss_note IS OLD.dismiss_note
        AND NEW.dismiss_event_sequence IS OLD.dismiss_event_sequence
        AND NEW.resolution_reason IN ('participant_stale', 'participant_superseded')
        AND NEW.resolved_at IS NOT NULL AND NEW.resolved_by IS NOT NULL AND NEW.resolved_by_type IS NOT NULL
        AND NEW.resolved_by = 'local-owner' AND NEW.resolved_by_type = 'human'
        AND NEW.resolution_note IS NOT NULL AND NEW.resolution_event_sequence IS NOT NULL
        AND NEW.resolution_cause_event_sequence IS NOT NULL
        AND EXISTS (
            SELECT 1 FROM events e
            WHERE e.sequence = NEW.resolution_event_sequence
              AND e.type = 'contradiction.resolved' AND e.workspace_id = NEW.workspace_id
              AND e.entity_type = 'knowledge_contradiction' AND e.entity_id = NEW.id
              AND e.entity_revision = NEW.state_revision AND e.actor_id = NEW.resolved_by
              AND e.actor_type = NEW.resolved_by_type AND e.occurred_at = NEW.resolved_at
        )
        AND EXISTS (
            SELECT 1
            FROM events cause
            JOIN knowledge_authority_checks authority ON authority.event_sequence = cause.sequence
            JOIN knowledge_revisions participant ON participant.id = cause.entity_id
            WHERE cause.sequence = NEW.resolution_cause_event_sequence
              AND cause.workspace_id = NEW.workspace_id
              AND cause.entity_type = 'knowledge_revision'
              AND cause.entity_id IN (NEW.left_revision_id, NEW.right_revision_id)
              AND cause.entity_revision = participant.state_revision
              AND cause.occurred_at = NEW.resolved_at
              AND cause.actor_id = NEW.resolved_by AND cause.actor_type = NEW.resolved_by_type
              AND authority.workspace_id = NEW.workspace_id
              AND authority.revision_id = cause.entity_id
              AND authority.actor_id = NEW.resolved_by AND authority.actor_type = NEW.resolved_by_type
              AND authority.outcome = 'allowed' AND authority.reason = 'workspace_owner'
              AND authority.created_at = cause.occurred_at
              AND ((NEW.resolution_reason = 'participant_stale'
                    AND cause.type = 'knowledge.marked_stale' AND authority.action = 'mark_stale'
                    AND participant.review_status = 'accepted' AND participant.currency_status = 'stale'
                    AND participant.stale_at = cause.occurred_at
                    AND participant.stale_by = cause.actor_id AND participant.stale_by_type = cause.actor_type
                    AND NEW.resolution_note = 'knowledge revision ' || cause.entity_id || ' became stale')
                OR (NEW.resolution_reason = 'participant_superseded'
                    AND cause.type = 'knowledge.superseded' AND authority.action = 'supersede'
                    AND participant.review_status = 'accepted' AND participant.currency_status = 'superseded'
                    AND NEW.resolution_note = 'knowledge revision ' || cause.entity_id || ' became superseded'))
        )
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge contradiction transition');
END;

CREATE TRIGGER knowledge_contradiction_reject_delete
BEFORE DELETE ON knowledge_contradictions
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradictions are immutable history');
END;

CREATE TABLE knowledge_contradiction_authority_checks (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 39 AND substr(id, 1, 7) = 'kcauth_'
        AND substr(id, 8) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    contradiction_id TEXT NOT NULL REFERENCES knowledge_contradictions(id),
    action TEXT NOT NULL CHECK (action IN ('confirm', 'dismiss')),
    actor_id TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('human', 'agent_run')),
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied')),
    reason TEXT NOT NULL CHECK (reason IN ('workspace_owner', 'actor_not_workspace_owner')),
    note TEXT CHECK (
        note IS NULL OR (
            length(CAST(note AS BLOB)) BETWEEN 1 AND 2048
            AND instr(note, char(0)) = 0
        )
    ),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK (
        length(request_hash) = 64 AND request_hash NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    UNIQUE (actor_type, actor_id, action, idempotency_key)
) STRICT;

CREATE INDEX knowledge_contradiction_authority_idx
    ON knowledge_contradiction_authority_checks(contradiction_id, created_at, id);

CREATE TRIGGER knowledge_contradiction_authority_reject_invalid_utf8
BEFORE INSERT ON knowledge_contradiction_authority_checks
WHEN NEW.note IS NOT NULL AND crewfold_utf8_valid(NEW.note) != 1
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction authority note must be valid UTF-8');
END;

CREATE TRIGGER knowledge_contradiction_authority_validate_insert
BEFORE INSERT ON knowledge_contradiction_authority_checks
WHEN NOT EXISTS (
    SELECT 1
    FROM knowledge_contradictions c
    JOIN events e ON e.sequence = NEW.event_sequence
    WHERE c.id = NEW.contradiction_id AND c.workspace_id = NEW.workspace_id
      AND e.workspace_id = NEW.workspace_id
      AND e.entity_type = 'knowledge_contradiction' AND e.entity_id = NEW.contradiction_id
      AND e.actor_id = NEW.actor_id AND e.actor_type = NEW.actor_type
      AND e.occurred_at = NEW.created_at
      AND (
          (NEW.outcome = 'allowed' AND NEW.reason = 'workspace_owner'
              AND NEW.actor_id = 'local-owner' AND NEW.actor_type = 'human'
              AND ((NEW.action = 'confirm' AND e.type = 'contradiction.confirmed' AND c.status = 'proposed')
                OR (NEW.action = 'dismiss' AND e.type = 'contradiction.dismissed' AND c.status IN ('proposed', 'open')))
              AND e.entity_revision = c.state_revision + 1) OR
          (NEW.outcome = 'denied' AND NEW.reason = 'actor_not_workspace_owner'
              AND NEW.actor_type = 'agent_run'
              AND EXISTS (
                  SELECT 1 FROM runs r
                  WHERE r.id = NEW.actor_id AND r.workspace_id = NEW.workspace_id
              )
              AND ((NEW.action = 'confirm' AND e.type = 'contradiction.confirm_denied')
                OR (NEW.action = 'dismiss' AND e.type = 'contradiction.dismiss_denied'))
              AND e.entity_revision = c.state_revision)
      )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge contradiction authority check');
END;

CREATE TRIGGER knowledge_contradiction_authority_reject_update
BEFORE UPDATE ON knowledge_contradiction_authority_checks
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction authority checks are append-only');
END;

CREATE TRIGGER knowledge_contradiction_authority_reject_delete
BEFORE DELETE ON knowledge_contradiction_authority_checks
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction authority checks are append-only');
END;
