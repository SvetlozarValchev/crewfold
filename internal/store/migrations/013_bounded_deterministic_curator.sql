CREATE TABLE curator_rules (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'crule_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL CHECK (name = 'accepted_meeting_resolution_copy/v1'),
    revision INTEGER NOT NULL CHECK (revision > 0),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    event_sequence INTEGER NOT NULL CHECK (event_sequence >= 0),
    UNIQUE (workspace_id, name, revision)
) STRICT;

CREATE INDEX curator_rules_effective_idx
    ON curator_rules(workspace_id, name, revision DESC);

CREATE TRIGGER curator_rule_validate_insert
BEFORE INSERT ON curator_rules
WHEN NOT (
    (
        NEW.revision = 1 AND NEW.enabled = 0
        AND NEW.created_by = 'subsystem:curator' AND NEW.event_sequence = 0
        AND NOT EXISTS (
            SELECT 1 FROM curator_rules prior
            WHERE prior.workspace_id = NEW.workspace_id AND prior.name = NEW.name
        )
    ) OR (
        NEW.revision > 1 AND NEW.created_by = 'local-owner' AND NEW.event_sequence > 0
        AND NEW.revision = 1 + COALESCE((
            SELECT MAX(prior.revision) FROM curator_rules prior
            WHERE prior.workspace_id = NEW.workspace_id AND prior.name = NEW.name
        ), 0)
        AND EXISTS (
            SELECT 1 FROM events e
            WHERE e.sequence = NEW.event_sequence
              AND e.type = 'curator.rule_configured'
              AND e.actor_id = 'local-owner' AND e.actor_type = 'human'
              AND e.workspace_id = NEW.workspace_id
              AND e.entity_type = 'curator_rule'
              AND e.entity_id = NEW.id
              AND e.entity_revision = NEW.revision
        )
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid curator rule revision');
END;

CREATE TRIGGER curator_rule_reject_update
BEFORE UPDATE ON curator_rules
BEGIN
    SELECT RAISE(ABORT, 'curator rules are immutable revisions');
END;

CREATE TRIGGER curator_rule_reject_delete
BEFORE DELETE ON curator_rules
BEGIN
    SELECT RAISE(ABORT, 'curator rules are immutable revisions');
END;

-- Existing and future workspaces both begin at the same explicit, disabled
-- revision. Default rows have no command event, represented by cursor zero.
INSERT INTO curator_rules(
    id, workspace_id, name, revision, enabled, created_at, created_by, event_sequence
)
SELECT 'crule_' || lower(hex(randomblob(16))), id,
       'accepted_meeting_resolution_copy/v1', 1, 0, created_at,
       'subsystem:curator', 0
FROM workspaces;

CREATE TRIGGER workspace_seed_default_curator_rule
AFTER INSERT ON workspaces
BEGIN
    INSERT INTO curator_rules(
        id, workspace_id, name, revision, enabled, created_at, created_by, event_sequence
    ) VALUES (
        'crule_' || lower(hex(randomblob(16))), NEW.id,
        'accepted_meeting_resolution_copy/v1', 1, 0, NEW.created_at,
        'subsystem:curator', 0
    );
END;

CREATE TABLE curator_derivations (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'cder_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    rule_id TEXT NOT NULL REFERENCES curator_rules(id),
    rule_name TEXT NOT NULL CHECK (rule_name = 'accepted_meeting_resolution_copy/v1'),
    rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
    source_type TEXT NOT NULL CHECK (source_type = 'meeting_proposal'),
    source_id TEXT NOT NULL,
    source_revision INTEGER NOT NULL CHECK (source_revision > 0),
    source_content_hash TEXT NOT NULL CHECK (
        length(source_content_hash) = 64
        AND source_content_hash NOT GLOB '*[^0-9a-f]*'
    ),
    knowledge_revision_id TEXT NOT NULL UNIQUE REFERENCES knowledge_revisions(id),
    output_content_hash TEXT NOT NULL CHECK (
        length(output_content_hash) = 64
        AND output_content_hash NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'subsystem:curator'),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    UNIQUE (workspace_id, rule_name, source_type, source_id, source_revision)
) STRICT;

CREATE INDEX curator_derivations_project_idx
    ON curator_derivations(workspace_id, project_id, created_at, id);

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
        WHERE mp.id = NEW.source_id
          AND mp.revision = NEW.source_revision
          AND mp.status = 'accepted'
          AND m.status = 'concluded'
          AND m.workspace_id = NEW.workspace_id
          AND m.project_id = NEW.project_id
          AND kr.id = NEW.knowledge_revision_id
          AND ki.workspace_id = NEW.workspace_id
          AND ki.project_id = NEW.project_id
          AND ki.task_scope_id IS NULL
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

CREATE TRIGGER curator_derivation_reject_update
BEFORE UPDATE ON curator_derivations
BEGIN
    SELECT RAISE(ABORT, 'curator derivations are immutable');
END;

CREATE TRIGGER curator_derivation_reject_delete
BEFORE DELETE ON curator_derivations
BEGIN
    SELECT RAISE(ABORT, 'curator derivations are immutable');
END;

CREATE TABLE curator_auto_acceptances (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'cauto_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    rule_id TEXT NOT NULL REFERENCES curator_rules(id),
    rule_name TEXT NOT NULL CHECK (rule_name = 'accepted_meeting_resolution_copy/v1'),
    rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
    derivation_id TEXT NOT NULL UNIQUE REFERENCES curator_derivations(id),
    knowledge_revision_id TEXT NOT NULL UNIQUE REFERENCES knowledge_revisions(id),
    authority_check_id TEXT NOT NULL UNIQUE REFERENCES knowledge_authority_checks(id),
    knowledge_event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    actor_id TEXT NOT NULL CHECK (actor_id = 'subsystem:curator'),
    actor_type TEXT NOT NULL CHECK (actor_type = 'subsystem')
) STRICT;

CREATE INDEX curator_auto_acceptances_project_idx
    ON curator_auto_acceptances(workspace_id, project_id, created_at, id);

CREATE TRIGGER curator_auto_acceptance_validate_insert
BEFORE INSERT ON curator_auto_acceptances
WHEN NOT (
    EXISTS (
        SELECT 1
        FROM curator_rules r
        WHERE r.id = NEW.rule_id
          AND r.workspace_id = NEW.workspace_id
          AND r.name = NEW.rule_name
          AND r.revision = NEW.rule_revision
          AND r.enabled = 1
          AND r.revision = (
              SELECT MAX(latest.revision)
              FROM curator_rules latest
              WHERE latest.workspace_id = NEW.workspace_id
                AND latest.name = NEW.rule_name
          )
    )
    AND EXISTS (
        SELECT 1
        FROM curator_derivations d
        WHERE d.id = NEW.derivation_id
          AND d.workspace_id = NEW.workspace_id
          AND d.project_id = NEW.project_id
          AND d.rule_name = NEW.rule_name
          AND d.knowledge_revision_id = NEW.knowledge_revision_id
    )
    AND EXISTS (
        SELECT 1
        FROM knowledge_authority_checks a
        WHERE a.id = NEW.authority_check_id
          AND a.workspace_id = NEW.workspace_id
          AND a.revision_id = NEW.knowledge_revision_id
          AND a.action = 'accept'
          AND a.actor_id = NEW.actor_id
          AND a.actor_type = NEW.actor_type
          AND a.outcome = 'allowed'
          AND a.reason = 'state_policy'
          AND a.event_sequence = NEW.knowledge_event_sequence
    )
    AND EXISTS (
        SELECT 1
        FROM knowledge_revisions kr
        JOIN knowledge_items ki ON ki.id = kr.item_id
        WHERE kr.id = NEW.knowledge_revision_id
          AND ki.workspace_id = NEW.workspace_id
          AND ki.project_id = NEW.project_id
          AND kr.review_status = 'accepted'
          AND kr.currency_status = 'current'
          AND kr.accepted_by = NEW.actor_id
          AND kr.accepted_by_type = NEW.actor_type
    )
    AND EXISTS (
        SELECT 1 FROM events e
        WHERE e.sequence = NEW.knowledge_event_sequence
          AND e.type = 'knowledge.accepted'
          AND e.actor_id = NEW.actor_id AND e.actor_type = NEW.actor_type
          AND e.workspace_id = NEW.workspace_id
          AND e.entity_type = 'knowledge_revision'
          AND e.entity_id = NEW.knowledge_revision_id
          AND e.entity_revision = 2
    )
    AND EXISTS (
        SELECT 1 FROM events e
        WHERE e.sequence = NEW.event_sequence
          AND e.type = 'curator.auto_accepted'
          AND e.actor_id = NEW.actor_id AND e.actor_type = NEW.actor_type
          AND e.workspace_id = NEW.workspace_id
          AND e.entity_type = 'curator_auto_acceptance'
          AND e.entity_id = NEW.id
          AND e.entity_revision = 1
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid curator auto acceptance');
END;

CREATE TRIGGER curator_auto_acceptance_reject_update
BEFORE UPDATE ON curator_auto_acceptances
BEGIN
    SELECT RAISE(ABORT, 'curator auto acceptances are immutable');
END;

CREATE TRIGGER curator_auto_acceptance_reject_delete
BEFORE DELETE ON curator_auto_acceptances
BEGIN
    SELECT RAISE(ABORT, 'curator auto acceptances are immutable');
END;
