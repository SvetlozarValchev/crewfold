-- Context packets have always been immutable run authority. Enforce that
-- invariant in SQLite now that exact accepted knowledge snapshots are embedded.
CREATE TRIGGER context_packet_reject_update
BEFORE UPDATE ON context_packets
BEGIN
    SELECT RAISE(ABORT, 'context packets are immutable');
END;

CREATE TRIGGER context_packet_reject_delete
BEFORE DELETE ON context_packets
BEGIN
    SELECT RAISE(ABORT, 'context packets are immutable');
END;

CREATE TABLE knowledge_items (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'know_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_scope_id TEXT REFERENCES tasks(id),
    type TEXT NOT NULL CHECK (type IN ('decision', 'finding')),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_by_type TEXT NOT NULL CHECK (created_by_type IN ('human', 'agent_run', 'subsystem'))
) STRICT;

CREATE INDEX knowledge_items_scope_idx
    ON knowledge_items(workspace_id, project_id, task_scope_id, type, created_at, id);

CREATE TABLE knowledge_revisions (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'krev_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    item_id TEXT NOT NULL REFERENCES knowledge_items(id),
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    state_revision INTEGER NOT NULL CHECK (state_revision > 0),
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 160),
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 16384),
    content_hash TEXT NOT NULL CHECK (length(content_hash) = 64),
    review_status TEXT NOT NULL CHECK (review_status IN ('proposed', 'accepted', 'rejected')),
    currency_status TEXT NOT NULL CHECK (currency_status IN ('pending', 'current', 'stale', 'superseded')),
    confidence TEXT NOT NULL CHECK (confidence IN ('low', 'medium', 'high')),
    verification_status TEXT NOT NULL CHECK (verification_status IN ('unverified', 'supported', 'verified')),
    freshness_policy TEXT NOT NULL CHECK (freshness_policy IN ('until_superseded', 'expires_at')),
    fresh_until TEXT,
    supersedes_revision_id TEXT REFERENCES knowledge_revisions(id),
    proposed_at TEXT NOT NULL,
    proposed_by TEXT NOT NULL,
    proposed_by_type TEXT NOT NULL CHECK (proposed_by_type IN ('human', 'agent_run', 'subsystem')),
    accepted_at TEXT,
    accepted_by TEXT,
    accepted_by_type TEXT CHECK (accepted_by_type IN ('human', 'agent_run', 'subsystem')),
    rejected_at TEXT,
    rejected_by TEXT,
    rejected_by_type TEXT CHECK (rejected_by_type IN ('human', 'agent_run', 'subsystem')),
    stale_at TEXT,
    stale_by TEXT,
    stale_by_type TEXT CHECK (stale_by_type IN ('human', 'agent_run', 'subsystem')),
    decision_note TEXT CHECK (decision_note IS NULL OR length(decision_note) <= 1024),
    stale_reason TEXT CHECK (stale_reason IS NULL OR length(stale_reason) BETWEEN 1 AND 1024),
    UNIQUE (item_id, revision_number),
    CHECK (
        (review_status = 'proposed' AND currency_status = 'pending') OR
        (review_status = 'rejected' AND currency_status = 'pending') OR
        (review_status = 'accepted' AND currency_status IN ('current', 'stale', 'superseded'))
    ),
    CHECK (
        (freshness_policy = 'until_superseded' AND fresh_until IS NULL) OR
        (freshness_policy = 'expires_at' AND fresh_until IS NOT NULL)
    ),
    CHECK (
        (review_status = 'accepted' AND accepted_at IS NOT NULL AND accepted_by IS NOT NULL AND accepted_by_type IS NOT NULL) OR
        (review_status != 'accepted' AND accepted_at IS NULL AND accepted_by IS NULL AND accepted_by_type IS NULL)
    ),
    CHECK (
        (review_status = 'rejected' AND rejected_at IS NOT NULL AND rejected_by IS NOT NULL AND rejected_by_type IS NOT NULL) OR
        (review_status != 'rejected' AND rejected_at IS NULL AND rejected_by IS NULL AND rejected_by_type IS NULL)
    ),
    CHECK (
        (currency_status = 'stale' AND stale_at IS NOT NULL AND stale_by IS NOT NULL AND stale_by_type IS NOT NULL AND stale_reason IS NOT NULL) OR
        (currency_status != 'stale' AND stale_at IS NULL AND stale_by IS NULL AND stale_by_type IS NULL AND stale_reason IS NULL)
    ),
    CHECK (supersedes_revision_id IS NULL OR supersedes_revision_id != id)
) STRICT;

CREATE UNIQUE INDEX knowledge_one_proposal_idx
    ON knowledge_revisions(item_id)
    WHERE review_status = 'proposed';

CREATE UNIQUE INDEX knowledge_one_current_idx
    ON knowledge_revisions(item_id)
    WHERE review_status = 'accepted' AND currency_status = 'current';

CREATE UNIQUE INDEX knowledge_one_live_successor_idx
    ON knowledge_revisions(supersedes_revision_id)
    WHERE supersedes_revision_id IS NOT NULL AND review_status IN ('proposed', 'accepted');

CREATE INDEX knowledge_revisions_state_idx
    ON knowledge_revisions(review_status, currency_status, proposed_at, id);

CREATE TRIGGER knowledge_revision_content_reject_update
BEFORE UPDATE OF item_id, revision_number, title, body, content_hash, confidence,
    verification_status, freshness_policy, fresh_until, supersedes_revision_id,
    proposed_at, proposed_by, proposed_by_type
ON knowledge_revisions
BEGIN
    SELECT RAISE(ABORT, 'knowledge revision content is immutable');
END;

CREATE TRIGGER knowledge_revision_reject_illegal_governance_update
BEFORE UPDATE OF state_revision, review_status, currency_status,
    accepted_at, accepted_by, accepted_by_type,
    rejected_at, rejected_by, rejected_by_type,
    stale_at, stale_by, stale_by_type, decision_note, stale_reason
ON knowledge_revisions
WHEN NOT (
    (
        OLD.review_status = 'proposed' AND OLD.currency_status = 'pending'
        AND NEW.review_status = 'accepted' AND NEW.currency_status = 'current'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.accepted_at IS NOT NULL AND NEW.accepted_by IS NOT NULL
        AND NEW.accepted_by_type IS NOT NULL
        AND NEW.rejected_at IS OLD.rejected_at
        AND NEW.rejected_by IS OLD.rejected_by
        AND NEW.rejected_by_type IS OLD.rejected_by_type
        AND NEW.stale_at IS OLD.stale_at
        AND NEW.stale_by IS OLD.stale_by
        AND NEW.stale_by_type IS OLD.stale_by_type
        AND NEW.stale_reason IS OLD.stale_reason
    ) OR (
        OLD.review_status = 'proposed' AND OLD.currency_status = 'pending'
        AND NEW.review_status = 'rejected' AND NEW.currency_status = 'pending'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.rejected_at IS NOT NULL AND NEW.rejected_by IS NOT NULL
        AND NEW.rejected_by_type IS NOT NULL
        AND NEW.accepted_at IS OLD.accepted_at
        AND NEW.accepted_by IS OLD.accepted_by
        AND NEW.accepted_by_type IS OLD.accepted_by_type
        AND NEW.stale_at IS OLD.stale_at
        AND NEW.stale_by IS OLD.stale_by
        AND NEW.stale_by_type IS OLD.stale_by_type
        AND NEW.stale_reason IS OLD.stale_reason
    ) OR (
        OLD.review_status = 'accepted' AND OLD.currency_status = 'current'
        AND NEW.review_status = 'accepted' AND NEW.currency_status = 'stale'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.accepted_at IS OLD.accepted_at
        AND NEW.accepted_by IS OLD.accepted_by
        AND NEW.accepted_by_type IS OLD.accepted_by_type
        AND NEW.rejected_at IS OLD.rejected_at
        AND NEW.rejected_by IS OLD.rejected_by
        AND NEW.rejected_by_type IS OLD.rejected_by_type
        AND NEW.decision_note IS OLD.decision_note
        AND NEW.stale_at IS NOT NULL AND NEW.stale_by IS NOT NULL
        AND NEW.stale_by_type IS NOT NULL AND NEW.stale_reason IS NOT NULL
    ) OR (
        OLD.review_status = 'accepted' AND OLD.currency_status = 'current'
        AND NEW.review_status = 'accepted' AND NEW.currency_status = 'superseded'
        AND NEW.state_revision = OLD.state_revision + 1
        AND NEW.accepted_at IS OLD.accepted_at
        AND NEW.accepted_by IS OLD.accepted_by
        AND NEW.accepted_by_type IS OLD.accepted_by_type
        AND NEW.rejected_at IS OLD.rejected_at
        AND NEW.rejected_by IS OLD.rejected_by
        AND NEW.rejected_by_type IS OLD.rejected_by_type
        AND NEW.stale_at IS OLD.stale_at
        AND NEW.stale_by IS OLD.stale_by
        AND NEW.stale_by_type IS OLD.stale_by_type
        AND NEW.decision_note IS OLD.decision_note
        AND NEW.stale_reason IS OLD.stale_reason
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge governance transition');
END;

CREATE TRIGGER knowledge_revision_reject_delete
BEFORE DELETE ON knowledge_revisions
BEGIN
    SELECT RAISE(ABORT, 'knowledge revisions are immutable history');
END;

CREATE TRIGGER knowledge_item_reject_update
BEFORE UPDATE ON knowledge_items
BEGIN
    SELECT RAISE(ABORT, 'knowledge items are immutable');
END;

CREATE TRIGGER knowledge_item_reject_delete
BEFORE DELETE ON knowledge_items
BEGIN
    SELECT RAISE(ABORT, 'knowledge items are immutable');
END;

CREATE TABLE knowledge_sources (
    revision_id TEXT NOT NULL REFERENCES knowledge_revisions(id),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 16),
    source_type TEXT NOT NULL CHECK (source_type IN ('task', 'meeting', 'meeting_proposal')),
    source_id TEXT NOT NULL,
    source_revision INTEGER NOT NULL CHECK (source_revision > 0),
    role TEXT NOT NULL CHECK (role IN ('primary', 'supporting')),
    PRIMARY KEY (revision_id, ordinal),
    UNIQUE (revision_id, source_type, source_id)
) STRICT;

CREATE INDEX knowledge_sources_entity_idx
    ON knowledge_sources(source_type, source_id, revision_id);

CREATE TRIGGER knowledge_source_reject_insert_after_proposal
BEFORE INSERT ON knowledge_sources
WHEN EXISTS (
    SELECT 1
    FROM events
    WHERE entity_type = 'knowledge_revision'
      AND entity_id = NEW.revision_id
      AND type = 'knowledge.proposed'
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge revision sources are sealed after proposal');
END;

CREATE TRIGGER knowledge_source_reject_update
BEFORE UPDATE ON knowledge_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge sources are immutable');
END;

CREATE TRIGGER knowledge_source_reject_delete
BEFORE DELETE ON knowledge_sources
BEGIN
    SELECT RAISE(ABORT, 'knowledge sources are immutable');
END;

CREATE TABLE knowledge_authority_checks (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'kauth_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    revision_id TEXT NOT NULL REFERENCES knowledge_revisions(id),
    action TEXT NOT NULL CHECK (action IN ('accept', 'reject', 'mark_stale', 'supersede')),
    actor_id TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('human', 'agent_run', 'subsystem')),
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied')),
    reason TEXT NOT NULL CHECK (reason IN ('workspace_owner', 'actor_not_workspace_owner', 'state_policy')),
    note TEXT CHECK (note IS NULL OR length(note) <= 1024),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    UNIQUE (actor_type, actor_id, action, idempotency_key)
) STRICT;

CREATE INDEX knowledge_authority_revision_idx
    ON knowledge_authority_checks(revision_id, created_at, id);

CREATE TRIGGER knowledge_authority_reject_update
BEFORE UPDATE ON knowledge_authority_checks
BEGIN
    SELECT RAISE(ABORT, 'knowledge authority checks are append-only');
END;

CREATE TRIGGER knowledge_authority_reject_delete
BEFORE DELETE ON knowledge_authority_checks
BEGIN
    SELECT RAISE(ABORT, 'knowledge authority checks are append-only');
END;
