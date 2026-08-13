-- Portable knowledge uses durable task-scope anchors so applicability survives
-- transport without importing operational task rows.
CREATE TABLE knowledge_task_scope_anchors (
    task_id TEXT PRIMARY KEY CHECK (
        length(task_id) = 37 AND substr(task_id, 1, 5) = 'task_'
        AND substr(task_id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (
        length(CAST(created_by AS BLOB)) BETWEEN 1 AND 128
        AND instr(created_by, char(0)) = 0
    )
) STRICT;

CREATE INDEX knowledge_task_scope_anchors_project_idx
    ON knowledge_task_scope_anchors(workspace_id, project_id, task_id);

INSERT INTO knowledge_task_scope_anchors(task_id, workspace_id, project_id, created_at, created_by)
SELECT DISTINCT t.id, t.workspace_id, t.project_id, t.created_at, t.created_by
FROM knowledge_items ki
JOIN tasks t ON t.id = ki.task_scope_id
WHERE ki.task_scope_id IS NOT NULL
ORDER BY t.id;

CREATE TABLE knowledge_item_task_scopes (
    item_id TEXT PRIMARY KEY REFERENCES knowledge_items(id),
    task_id TEXT NOT NULL REFERENCES knowledge_task_scope_anchors(task_id)
) STRICT;

INSERT INTO knowledge_item_task_scopes(item_id, task_id)
SELECT id, task_scope_id FROM knowledge_items
WHERE task_scope_id IS NOT NULL
ORDER BY id;

CREATE TRIGGER knowledge_task_scope_anchor_validate_insert
BEFORE INSERT ON knowledge_task_scope_anchors
WHEN crewfold_utf8_valid(NEW.created_by) != 1
 OR crewfold_timestamp_canonical(NEW.created_at) != 1
 OR NOT EXISTS (
    SELECT 1 FROM projects p
    WHERE p.id = NEW.project_id AND p.workspace_id = NEW.workspace_id
)
 OR EXISTS (
    SELECT 1 FROM tasks task
    WHERE task.id = NEW.task_id
      AND NOT (
        task.workspace_id = NEW.workspace_id AND task.project_id = NEW.project_id
        AND task.created_at = NEW.created_at AND task.created_by = NEW.created_by
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'knowledge task-scope anchor project is invalid');
END;

CREATE TRIGGER knowledge_task_scope_anchor_reject_update
BEFORE UPDATE ON knowledge_task_scope_anchors
BEGIN
    SELECT RAISE(ABORT, 'knowledge task-scope anchors are immutable');
END;

CREATE TRIGGER knowledge_task_scope_anchor_reject_delete
BEFORE DELETE ON knowledge_task_scope_anchors
BEGIN
    SELECT RAISE(ABORT, 'knowledge task-scope anchors are immutable');
END;

CREATE TRIGGER task_validate_knowledge_scope_anchor
BEFORE INSERT ON tasks
WHEN EXISTS (SELECT 1 FROM knowledge_task_scope_anchors WHERE task_id = NEW.id)
 AND NOT EXISTS (
    SELECT 1 FROM knowledge_task_scope_anchors anchor
    WHERE anchor.task_id = NEW.id
      AND anchor.workspace_id = NEW.workspace_id
      AND anchor.project_id = NEW.project_id
      AND anchor.created_at = NEW.created_at
      AND anchor.created_by = NEW.created_by
 )
BEGIN
    SELECT RAISE(ABORT, 'task identity conflicts with a portable knowledge scope anchor');
END;

CREATE TRIGGER task_preserve_knowledge_scope_anchor
BEFORE UPDATE OF id, workspace_id, project_id, created_at, created_by ON tasks
WHEN EXISTS (SELECT 1 FROM knowledge_task_scope_anchors WHERE task_id IN (OLD.id, NEW.id))
 AND NOT (
    NEW.id = OLD.id AND NEW.workspace_id = OLD.workspace_id AND NEW.project_id = OLD.project_id
    AND NEW.created_at = OLD.created_at AND NEW.created_by = OLD.created_by
 )
BEGIN
    SELECT RAISE(ABORT, 'anchored task identity is immutable');
END;

CREATE TRIGGER knowledge_item_task_scope_validate_insert
BEFORE INSERT ON knowledge_item_task_scopes
WHEN EXISTS (SELECT 1 FROM knowledge_revisions WHERE item_id = NEW.item_id)
 OR NOT EXISTS (
    SELECT 1
    FROM knowledge_items ki
    JOIN knowledge_task_scope_anchors a ON a.task_id = NEW.task_id
    WHERE ki.id = NEW.item_id
      AND ki.workspace_id = a.workspace_id AND ki.project_id = a.project_id
      AND (ki.task_scope_id IS NULL OR ki.task_scope_id = NEW.task_id)
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge item task scope is invalid');
END;

CREATE TRIGGER knowledge_item_task_scope_reject_update
BEFORE UPDATE ON knowledge_item_task_scopes
BEGIN
    SELECT RAISE(ABORT, 'knowledge item task scopes are immutable');
END;

CREATE TRIGGER knowledge_item_task_scope_reject_delete
BEFORE DELETE ON knowledge_item_task_scopes
BEGIN
    SELECT RAISE(ABORT, 'knowledge item task scopes are immutable');
END;

CREATE TRIGGER knowledge_source_reject_insert_after_import
BEFORE INSERT ON knowledge_sources
WHEN EXISTS (
    SELECT 1 FROM events
    WHERE entity_type = 'knowledge_revision'
      AND entity_id = NEW.revision_id
      AND type = 'knowledge.imported'
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge revision sources are sealed after import');
END;

CREATE VIEW knowledge_item_effective_scopes AS
SELECT ki.id, ki.workspace_id, ki.project_id,
       COALESCE(binding.task_id, ki.task_scope_id) AS task_scope_id
FROM knowledge_items ki
LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id;

-- The curator derives project-wide items only. Recreate its integrity trigger
-- so imported task bindings cannot masquerade as project-wide through the
-- intentionally NULL legacy column.
DROP TRIGGER curator_derivation_validate_insert;

CREATE TRIGGER curator_derivation_validate_insert
BEFORE INSERT ON curator_derivations
WHEN NOT (
    EXISTS (
        SELECT 1
        FROM curator_rules r
        WHERE r.id = NEW.rule_id
          AND r.workspace_id = NEW.workspace_id
          AND r.name = NEW.rule_name
          AND r.revision = NEW.rule_revision
    )
    AND EXISTS (
        SELECT 1 FROM events e
        WHERE e.sequence = NEW.event_sequence
          AND e.type = 'curator.derived'
          AND e.actor_id = 'subsystem:curator' AND e.actor_type = 'subsystem'
          AND e.workspace_id = NEW.workspace_id
          AND e.entity_type = 'curator_derivation'
          AND e.entity_id = NEW.id
          AND e.entity_revision = 1
    )
    AND EXISTS (
        SELECT 1
        FROM meeting_proposals mp
        JOIN meetings m ON m.id = mp.meeting_id
        JOIN knowledge_sources ks
          ON ks.source_type = 'meeting_proposal'
         AND ks.source_id = mp.id
         AND ks.source_revision = mp.revision
         AND ks.role = 'primary'
         AND ks.ordinal = 0
        JOIN knowledge_revisions kr ON kr.id = ks.revision_id
        JOIN knowledge_items ki ON ki.id = kr.item_id
        LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id
        WHERE mp.id = NEW.source_id
          AND mp.revision = NEW.source_revision
          AND mp.status = 'accepted'
          AND m.status = 'concluded'
          AND m.workspace_id = NEW.workspace_id
          AND m.project_id = NEW.project_id
          AND kr.id = NEW.knowledge_revision_id
          AND ki.workspace_id = NEW.workspace_id
          AND ki.project_id = NEW.project_id
          AND COALESCE(binding.task_id, ki.task_scope_id) IS NULL
          AND ki.type = 'decision'
          AND kr.review_status = 'proposed'
          AND kr.currency_status = 'pending'
          AND kr.title = m.agenda
          AND kr.body = mp.summary
          AND kr.confidence = 'medium'
          AND kr.verification_status = 'supported'
          AND kr.freshness_policy = 'until_superseded'
          AND kr.fresh_until IS NULL
          AND kr.supersedes_revision_id IS NULL
          AND kr.proposed_by = 'subsystem:curator'
          AND kr.proposed_by_type = 'subsystem'
          AND kr.content_hash = NEW.output_content_hash
          AND NEW.output_content_hash = lower(hex(sha256(
              m.agenda || char(10) || mp.summary
          )))
          AND NEW.source_content_hash = lower(hex(sha256(
              m.id || char(10) || mp.id || char(10) || CAST(mp.revision AS TEXT) || char(10) ||
              m.agenda || char(10) || mp.summary || char(10) || mp.status || char(10) || m.status
          )))
          AND length(CAST(m.agenda AS BLOB)) BETWEEN 1 AND 160
          AND instr(m.agenda, char(0)) = 0
          AND crewfold_utf8_valid(m.agenda) = 1
          AND length(CAST(mp.summary AS BLOB)) BETWEEN 1 AND 2048
          AND instr(mp.summary, char(0)) = 0
          AND crewfold_utf8_valid(mp.summary) = 1
          AND (SELECT COUNT(*) FROM knowledge_sources all_sources
               WHERE all_sources.revision_id = kr.id) = 1
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid curator derivation');
END;

-- Import receipts retain exact bytes so replay is byte-identical rather than
-- merely hash-identical. They are local audit state and are never exported.
CREATE TABLE knowledge_imports (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'kimp_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    bundle_id TEXT NOT NULL CHECK (
        length(bundle_id) = 37 AND substr(bundle_id, 1, 5) = 'kbun_'
        AND substr(bundle_id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    rendering_sha256 TEXT NOT NULL CHECK (
        length(rendering_sha256) = 64 AND rendering_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    manifest_json BLOB NOT NULL,
    markdown BLOB NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK (
        length(request_hash) = 64 AND request_hash NOT GLOB '*[^0-9a-f]*'
    ),
    imported_at TEXT NOT NULL,
    imported_by TEXT NOT NULL CHECK (imported_by = 'local-owner'),
    imported_by_type TEXT NOT NULL CHECK (imported_by_type = 'human'),
    created_workspace INTEGER NOT NULL CHECK (created_workspace IN (0, 1)),
    created_project INTEGER NOT NULL CHECK (created_project IN (0, 1)),
    created_task_scope_anchors INTEGER NOT NULL CHECK (created_task_scope_anchors >= 0),
    completed_event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    UNIQUE (workspace_id, project_id),
    UNIQUE (workspace_id, project_id, idempotency_key)
) STRICT;

CREATE TABLE knowledge_import_entities (
    import_id TEXT NOT NULL,
    entity_type TEXT NOT NULL CHECK (
        entity_type IN ('task_scope_anchor', 'knowledge_item', 'knowledge_revision', 'knowledge_contradiction')
    ),
    entity_id TEXT NOT NULL,
    event_sequence INTEGER REFERENCES events(sequence),
    imported_at TEXT NOT NULL,
    PRIMARY KEY (import_id, entity_type, entity_id)
) STRICT;

CREATE UNIQUE INDEX knowledge_import_entities_entity_idx
    ON knowledge_import_entities(entity_type, entity_id);

CREATE TRIGGER knowledge_import_reject_update
BEFORE UPDATE ON knowledge_imports
BEGIN
    SELECT RAISE(ABORT, 'knowledge import receipts are append-only');
END;

CREATE TRIGGER knowledge_import_validate_restore
BEFORE INSERT ON knowledge_imports
WHEN crewfold_restore_active() != 1 OR NOT EXISTS (
    SELECT 1 FROM events e
    WHERE e.sequence = NEW.completed_event_sequence
      AND e.workspace_id = NEW.workspace_id
      AND e.entity_type = 'knowledge_import' AND e.entity_id = NEW.id AND e.entity_revision = 1
      AND e.type = 'knowledge.import_completed'
      AND e.actor_id = NEW.imported_by AND e.actor_type = NEW.imported_by_type
      AND e.occurred_at = NEW.imported_at
      AND json_extract(e.data_json, '$.bundle_id') = NEW.bundle_id
      AND json_extract(e.data_json, '$.project_id') = NEW.project_id
      AND json_extract(e.data_json, '$.content_sha256') = NEW.content_sha256
      AND CAST(json_extract(e.data_json, '$.item_count') AS INTEGER) =
          CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.items') AS INTEGER)
      AND CAST(json_extract(e.data_json, '$.revision_count') AS INTEGER) =
          CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.revisions') AS INTEGER)
      AND CAST(json_extract(e.data_json, '$.contradiction_count') AS INTEGER) =
          CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.contradictions') AS INTEGER)
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge import receipts require the restore gate');
END;

CREATE TRIGGER knowledge_import_reject_delete
BEFORE DELETE ON knowledge_imports
BEGIN
    SELECT RAISE(ABORT, 'knowledge import receipts are append-only');
END;

CREATE TRIGGER knowledge_import_entity_reject_update
BEFORE UPDATE ON knowledge_import_entities
BEGIN
    SELECT RAISE(ABORT, 'knowledge import entity audit is append-only');
END;

CREATE TRIGGER knowledge_import_entity_reject_delete
BEFORE DELETE ON knowledge_import_entities
BEGIN
    SELECT RAISE(ABORT, 'knowledge import entity audit is append-only');
END;

CREATE TRIGGER knowledge_import_entity_validate_receipt
BEFORE INSERT ON knowledge_import_entities
WHEN crewfold_restore_active() != 1
 OR EXISTS (SELECT 1 FROM knowledge_imports WHERE id = NEW.import_id)
 OR (NEW.entity_type IN ('task_scope_anchor', 'knowledge_item') AND NEW.event_sequence IS NOT NULL)
 OR (NEW.entity_type = 'task_scope_anchor' AND NOT EXISTS (
      SELECT 1 FROM knowledge_task_scope_anchors WHERE task_id = NEW.entity_id
 ))
 OR (NEW.entity_type = 'knowledge_item' AND NOT EXISTS (
      SELECT 1 FROM knowledge_items WHERE id = NEW.entity_id
 ))
 OR (NEW.entity_type = 'knowledge_revision' AND NOT EXISTS (
      SELECT 1 FROM knowledge_revisions revision
      JOIN knowledge_items item ON item.id = revision.item_id
      JOIN events e ON e.sequence = NEW.event_sequence
      WHERE revision.id = NEW.entity_id
        AND e.type = 'knowledge.imported' AND e.workspace_id = item.workspace_id
        AND e.entity_type = 'knowledge_revision' AND e.entity_id = revision.id
        AND e.entity_revision = revision.state_revision
        AND e.occurred_at = NEW.imported_at
        AND e.actor_id = 'local-owner' AND e.actor_type = 'human'
        AND json_extract(e.data_json, '$.bundle_import_id') = NEW.import_id
        AND json_extract(e.data_json, '$.project_id') = item.project_id
        AND json_extract(e.data_json, '$.item_id') = revision.item_id
        AND CAST(json_extract(e.data_json, '$.revision_number') AS INTEGER) = revision.revision_number
        AND json_extract(e.data_json, '$.review_status') = revision.review_status
        AND json_extract(e.data_json, '$.currency_status') = revision.currency_status
 ))
 OR (NEW.entity_type = 'knowledge_contradiction' AND NOT EXISTS (
      SELECT 1 FROM knowledge_contradictions contradiction
      JOIN events e ON e.sequence = NEW.event_sequence
      WHERE contradiction.id = NEW.entity_id
        AND e.type = 'contradiction.imported' AND e.workspace_id = contradiction.workspace_id
        AND e.entity_type = 'knowledge_contradiction' AND e.entity_id = contradiction.id
        AND e.entity_revision = contradiction.state_revision
        AND e.occurred_at = NEW.imported_at
        AND e.actor_id = 'local-owner' AND e.actor_type = 'human'
        AND json_extract(e.data_json, '$.bundle_import_id') = NEW.import_id
        AND json_extract(e.data_json, '$.project_id') = contradiction.project_id
        AND json_extract(e.data_json, '$.status') = contradiction.status
 ))
BEGIN
    SELECT RAISE(ABORT, 'knowledge import entity audit requires an active unsealed receipt');
END;

CREATE TRIGGER knowledge_import_validate_entities
BEFORE INSERT ON knowledge_imports
WHEN EXISTS (
    SELECT 1 FROM knowledge_import_entities entity
    WHERE entity.import_id = NEW.id AND entity.imported_at != NEW.imported_at
)
 OR (SELECT COUNT(*) FROM knowledge_import_entities entity
     WHERE entity.import_id = NEW.id AND entity.entity_type = 'task_scope_anchor')
      != CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.task_scope_anchors') AS INTEGER)
 OR (SELECT COUNT(*) FROM knowledge_import_entities entity
     WHERE entity.import_id = NEW.id AND entity.entity_type = 'knowledge_item')
      != CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.items') AS INTEGER)
 OR (SELECT COUNT(*) FROM knowledge_import_entities entity
     WHERE entity.import_id = NEW.id AND entity.entity_type = 'knowledge_revision')
      != CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.revisions') AS INTEGER)
 OR (SELECT COUNT(*) FROM knowledge_import_entities entity
     WHERE entity.import_id = NEW.id AND entity.entity_type = 'knowledge_contradiction')
      != CAST(json_extract(CAST(NEW.manifest_json AS TEXT), '$.snapshot.counts.contradictions') AS INTEGER)
 OR EXISTS (
    SELECT 1 FROM knowledge_import_entities entity
    WHERE entity.import_id = NEW.id AND (
        (entity.entity_type = 'task_scope_anchor' AND NOT EXISTS (
            SELECT 1 FROM knowledge_task_scope_anchors anchor
            WHERE anchor.task_id = entity.entity_id
              AND anchor.workspace_id = NEW.workspace_id AND anchor.project_id = NEW.project_id
        )) OR
        (entity.entity_type = 'knowledge_item' AND NOT EXISTS (
            SELECT 1 FROM knowledge_items item
            WHERE item.id = entity.entity_id
              AND item.workspace_id = NEW.workspace_id AND item.project_id = NEW.project_id
        )) OR
        (entity.entity_type = 'knowledge_revision' AND NOT EXISTS (
            SELECT 1 FROM knowledge_revisions revision
            JOIN knowledge_items item ON item.id = revision.item_id
            WHERE revision.id = entity.entity_id
              AND item.workspace_id = NEW.workspace_id AND item.project_id = NEW.project_id
        )) OR
        (entity.entity_type = 'knowledge_contradiction' AND NOT EXISTS (
            SELECT 1 FROM knowledge_contradictions contradiction
            WHERE contradiction.id = entity.entity_id
              AND contradiction.workspace_id = NEW.workspace_id AND contradiction.project_id = NEW.project_id
        ))
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'knowledge import receipt requires exact entity audit');
END;

-- Recreate contradiction temporal validators against effective task scopes.
-- The restore gate bypasses only the INSERT-time event chronology validator;
-- all table checks, foreign keys, immutable-history triggers, and later
-- governance transition validators remain active.
DROP TRIGGER knowledge_contradiction_validate_insert;

CREATE TRIGGER knowledge_contradiction_validate_pair_insert
BEFORE INSERT ON knowledge_contradictions
WHEN NOT EXISTS (
    SELECT 1
    FROM knowledge_revisions left_revision
    JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
    JOIN knowledge_item_effective_scopes left_scope ON left_scope.id = left_item.id
    JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
    JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
    JOIN knowledge_item_effective_scopes right_scope ON right_scope.id = right_item.id
    JOIN projects p ON p.id = NEW.project_id
    WHERE left_revision.id = NEW.left_revision_id
      AND left_item.workspace_id = NEW.workspace_id
      AND right_item.workspace_id = NEW.workspace_id
      AND p.workspace_id = NEW.workspace_id
      AND left_item.project_id = NEW.project_id
      AND right_item.project_id = NEW.project_id
      AND left_item.id != right_item.id
      AND (left_scope.task_scope_id IS NULL OR right_scope.task_scope_id IS NULL
           OR left_scope.task_scope_id = right_scope.task_scope_id)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge contradiction pair');
END;

CREATE TRIGGER knowledge_contradiction_validate_insert
BEFORE INSERT ON knowledge_contradictions
WHEN crewfold_restore_active() = 0 AND NOT (
    NEW.status = 'proposed' AND NEW.state_revision = 1
    AND EXISTS (
        SELECT 1
        FROM knowledge_revisions left_revision
        JOIN knowledge_items left_item ON left_item.id = left_revision.item_id
        JOIN knowledge_item_effective_scopes left_scope ON left_scope.id = left_item.id
        JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
        JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
        JOIN knowledge_item_effective_scopes right_scope ON right_scope.id = right_item.id
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
          AND (left_scope.task_scope_id IS NULL OR right_scope.task_scope_id IS NULL
               OR left_scope.task_scope_id = right_scope.task_scope_id)
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
            JOIN knowledge_item_effective_scopes left_scope ON left_scope.id = left_revision.item_id
            JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
            JOIN knowledge_item_effective_scopes right_scope ON right_scope.id = right_revision.item_id
            WHERE r.id = NEW.reported_by
              AND r.workspace_id = NEW.workspace_id AND r.project_id = NEW.project_id
              AND r.status IN ('starting', 'active', 'blocked')
              AND (left_scope.task_scope_id IS NULL OR left_scope.task_scope_id = r.task_id)
              AND (right_scope.task_scope_id IS NULL OR right_scope.task_scope_id = r.task_id)
        ))
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge contradiction report');
END;

DROP TRIGGER knowledge_contradiction_reject_illegal_transition;

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
            JOIN knowledge_item_effective_scopes left_scope ON left_scope.id = left_item.id
            JOIN knowledge_revisions right_revision ON right_revision.id = NEW.right_revision_id
            JOIN knowledge_items right_item ON right_item.id = right_revision.item_id
            JOIN knowledge_item_effective_scopes right_scope ON right_scope.id = right_item.id
            WHERE left_revision.id = NEW.left_revision_id
              AND left_item.workspace_id = NEW.workspace_id
              AND right_item.workspace_id = NEW.workspace_id
              AND left_item.project_id = NEW.project_id AND right_item.project_id = NEW.project_id
              AND left_item.id != right_item.id
              AND left_revision.review_status = 'accepted' AND left_revision.currency_status = 'current'
              AND right_revision.review_status = 'accepted' AND right_revision.currency_status = 'current'
              AND (left_scope.task_scope_id IS NULL OR right_scope.task_scope_id IS NULL
                   OR left_scope.task_scope_id = right_scope.task_scope_id)
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
