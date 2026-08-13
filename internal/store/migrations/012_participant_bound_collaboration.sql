ALTER TABLE message_threads
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'direct'
    CHECK (kind IN ('direct', 'participant_bound'));

ALTER TABLE message_threads
    ADD COLUMN participant_revision INTEGER NOT NULL DEFAULT 0
    CHECK (participant_revision >= 0);

ALTER TABLE message_threads
    ADD COLUMN initial_participant_count INTEGER NOT NULL DEFAULT 0
    CHECK (initial_participant_count BETWEEN 0 AND 8);

CREATE TABLE thread_participants (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 44
        AND substr(id, 1, 12) = 'participant_'
        AND length(substr(id, 13)) = 32
        AND substr(id, 13) NOT GLOB '*[^0-9a-f]*'
    ),
    thread_id TEXT NOT NULL REFERENCES message_threads(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    agent_name TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    task_title TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    project_name TEXT NOT NULL,
    assignment_id TEXT NOT NULL REFERENCES task_assignments(id),
    assignment_revision INTEGER NOT NULL CHECK (assignment_revision > 0),
    agent_revision INTEGER NOT NULL CHECK (agent_revision > 0),
    task_revision INTEGER NOT NULL CHECK (task_revision > 0),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 8),
    status TEXT NOT NULL CHECK (status = 'active'),
    invited_at TEXT NOT NULL,
    invited_by TEXT NOT NULL,
    UNIQUE (thread_id, agent_id),
    UNIQUE (thread_id, task_id),
    UNIQUE (thread_id, ordinal),
    UNIQUE (id, thread_id)
) STRICT;

ALTER TABLE message_recipients
    ADD COLUMN recipient_participant_id TEXT REFERENCES thread_participants(id);

CREATE INDEX thread_participants_agent_scope_idx
    ON thread_participants(workspace_id, agent_id, project_id, task_id, thread_id);

CREATE TRIGGER message_thread_kind_reject_update
BEFORE UPDATE OF workspace_id, kind, project_id, task_id, initial_participant_count ON message_threads
WHEN NEW.workspace_id <> OLD.workspace_id
  OR NEW.kind <> OLD.kind
  OR NEW.project_id IS NOT OLD.project_id
  OR NEW.task_id IS NOT OLD.task_id
  OR NEW.initial_participant_count <> OLD.initial_participant_count
BEGIN
    SELECT RAISE(ABORT, 'message thread kind, scope, and initial roster are immutable');
END;

CREATE TRIGGER message_thread_kind_validate_insert
BEFORE INSERT ON message_threads
WHEN (NEW.kind = 'direct' AND (
      NEW.participant_revision <> 0 OR NEW.initial_participant_count <> 0
  ))
  OR (NEW.kind = 'participant_bound' AND (
      NEW.project_id IS NOT NULL OR NEW.task_id IS NOT NULL OR NEW.participant_revision <> 1
      OR NEW.initial_participant_count NOT BETWEEN 2 AND 8
      OR NEW.created_by <> 'local-owner' OR NEW.updated_by <> 'local-owner'
  ))
BEGIN
    SELECT RAISE(ABORT, 'message thread kind state is invalid');
END;

CREATE TRIGGER message_thread_participant_revision_validate
BEFORE UPDATE OF participant_revision ON message_threads
WHEN NEW.participant_revision <> OLD.participant_revision
 AND (
    OLD.kind <> 'participant_bound'
    OR NEW.participant_revision <> OLD.participant_revision + 1
    OR NEW.participant_revision <> (
        SELECT COUNT(*) - OLD.initial_participant_count + 1
        FROM thread_participants WHERE thread_id = OLD.id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal participant revision transition');
END;

CREATE TRIGGER thread_participant_validate_insert
BEFORE INSERT ON thread_participants
BEGIN
    SELECT CASE WHEN NEW.invited_by <> 'local-owner'
        THEN RAISE(ABORT, 'participant invitations must be owned by local-owner') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM message_threads th
        WHERE th.id = NEW.thread_id
          AND th.workspace_id = NEW.workspace_id
          AND th.kind = 'participant_bound'
          AND th.status = 'open'
    ) THEN RAISE(ABORT, 'participant thread is not an open participant-bound thread') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM agents a
        JOIN tasks t ON t.workspace_id = a.workspace_id
        JOIN task_assignments ta ON ta.task_id = t.id AND ta.agent_id = a.id
        JOIN projects pr ON pr.id = t.project_id AND pr.workspace_id = a.workspace_id
        WHERE a.id = NEW.agent_id
          AND a.workspace_id = NEW.workspace_id
          AND a.enabled = 1
          AND a.name = NEW.agent_name
          AND a.revision = NEW.agent_revision
          AND t.id = NEW.task_id
          AND t.title = NEW.task_title
          AND t.project_id = NEW.project_id
          AND t.revision = NEW.task_revision
          AND pr.name = NEW.project_name
          AND ta.id = NEW.assignment_id
          AND ta.status = 'active'
          AND ta.revision = NEW.assignment_revision
          AND crewfold_timestamp_key(ta.lease_expires_at) > crewfold_timestamp_key(NEW.invited_at)
    ) THEN RAISE(ABORT, 'participant binding is not currently eligible') END;
    SELECT CASE WHEN NEW.ordinal <> (
        SELECT COUNT(*) + 1 FROM thread_participants WHERE thread_id = NEW.thread_id
    ) THEN RAISE(ABORT, 'participant ordinal must be contiguous') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM message_threads th
        WHERE th.id = NEW.thread_id
          AND (
            ((SELECT COUNT(*) FROM thread_participants WHERE thread_id = NEW.thread_id) < th.initial_participant_count
              AND th.participant_revision = 1)
            OR
            ((SELECT COUNT(*) FROM thread_participants WHERE thread_id = NEW.thread_id) >= th.initial_participant_count
              AND th.participant_revision = (
                  SELECT COUNT(*) - th.initial_participant_count + 1
                  FROM thread_participants WHERE thread_id = NEW.thread_id
              ))
          )
    ) THEN RAISE(ABORT, 'participant roster revision is inconsistent') END;
    SELECT CASE WHEN (
        SELECT COUNT(*) FROM thread_participants WHERE thread_id = NEW.thread_id
    ) >= 8 THEN RAISE(ABORT, 'participant thread is full') END;
END;

CREATE TRIGGER thread_participant_advance_revision
AFTER INSERT ON thread_participants
WHEN (SELECT COUNT(*) FROM thread_participants WHERE thread_id = NEW.thread_id) >
     (SELECT initial_participant_count FROM message_threads WHERE id = NEW.thread_id)
BEGIN
    UPDATE message_threads
    SET participant_revision = participant_revision + 1,
        revision = revision + 1,
        updated_at = NEW.invited_at,
        updated_by = NEW.invited_by
    WHERE id = NEW.thread_id;
END;

CREATE TRIGGER thread_participant_reject_update
BEFORE UPDATE ON thread_participants
BEGIN
    SELECT RAISE(ABORT, 'thread participants are immutable');
END;

CREATE TRIGGER thread_participant_reject_delete
BEFORE DELETE ON thread_participants
BEGIN
    SELECT RAISE(ABORT, 'thread participants cannot be removed');
END;

CREATE TRIGGER participant_message_validate_insert
BEFORE INSERT ON messages
WHEN (SELECT kind FROM message_threads WHERE id = NEW.thread_id) = 'participant_bound'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM message_threads th
        WHERE th.id = NEW.thread_id
          AND th.workspace_id = NEW.workspace_id
          AND th.status = 'open'
          AND th.participant_revision = (
              SELECT COUNT(*) - th.initial_participant_count + 1
              FROM thread_participants WHERE thread_id = th.id
          )
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id = th.id) >= th.initial_participant_count
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id = th.id) BETWEEN 2 AND 8
          AND (SELECT COUNT(DISTINCT project_id) FROM thread_participants WHERE thread_id = th.id) >= 2
    ) THEN RAISE(ABORT, 'participant thread roster is incomplete or inconsistent') END;
    SELECT CASE WHEN NEW.reply_to_message_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM messages reply
        WHERE reply.id = NEW.reply_to_message_id
          AND reply.thread_id = NEW.thread_id
          AND reply.workspace_id = NEW.workspace_id
    ) THEN RAISE(ABORT, 'participant reply target must belong to the same thread') END;
    SELECT CASE WHEN json_type(NEW.artifact_ids_json) <> 'array'
                     OR json_array_length(NEW.artifact_ids_json) <> 0
        THEN RAISE(ABORT, 'participant-bound messages cannot attach artifacts') END;
    SELECT CASE WHEN NEW.sender_type = 'agent_run' AND NOT EXISTS (
        SELECT 1
        FROM thread_participants p
        JOIN runs r ON r.id = NEW.sender_run_id
        WHERE p.thread_id = NEW.thread_id
          AND p.status = 'active'
          AND p.agent_id = NEW.sender_agent_id
          AND p.project_id = NEW.project_id
          AND p.task_id = NEW.task_id
          AND r.workspace_id = NEW.workspace_id
          AND r.agent_id = p.agent_id
          AND r.project_id = p.project_id
          AND r.task_id = p.task_id
          AND r.status IN ('starting', 'active', 'blocked')
          AND NEW.sender_id = r.id
    ) THEN RAISE(ABORT, 'participant message sender is outside its bound scope') END;
    SELECT CASE WHEN NEW.sender_type = 'owner' AND (
                     NEW.sender_id <> 'local-owner'
                     OR NEW.sender_agent_id IS NOT NULL
                     OR NEW.sender_run_id IS NOT NULL
                     OR NEW.project_id IS NOT NULL
                     OR NEW.task_id IS NOT NULL
                     )
        THEN RAISE(ABORT, 'owner participant messages cannot impersonate an agent') END;
END;

CREATE TRIGGER message_reject_update
BEFORE UPDATE ON messages
BEGIN
    SELECT RAISE(ABORT, 'messages are immutable');
END;

CREATE TRIGGER message_reject_delete
BEFORE DELETE ON messages
BEGIN
    SELECT RAISE(ABORT, 'messages are immutable');
END;

CREATE TRIGGER participant_recipient_validate_insert
BEFORE INSERT ON message_recipients
BEGIN
    SELECT CASE WHEN (
        SELECT th.kind FROM messages m JOIN message_threads th ON th.id = m.thread_id
        WHERE m.id = NEW.message_id
    ) = 'participant_bound' AND (
        NEW.recipient_participant_id IS NULL OR NOT EXISTS (
            SELECT 1
            FROM messages m
            JOIN thread_participants p ON p.id = NEW.recipient_participant_id
            JOIN agents recipient_agent ON recipient_agent.id = p.agent_id
            WHERE m.id = NEW.message_id
              AND p.thread_id = m.thread_id
              AND p.agent_id = NEW.recipient_agent_id
              AND p.status = 'active'
              AND recipient_agent.workspace_id = m.workspace_id
              AND recipient_agent.enabled = 1
        )
        OR EXISTS (
            SELECT 1 FROM message_recipients existing
            WHERE existing.message_id = NEW.message_id
        )
        OR EXISTS (
            SELECT 1
            FROM messages m
            JOIN thread_participants sender
              ON sender.thread_id = m.thread_id
             AND sender.agent_id = m.sender_agent_id
             AND sender.project_id = m.project_id
             AND sender.task_id = m.task_id
            WHERE m.id = NEW.message_id
              AND m.sender_type = 'agent_run'
              AND sender.id = NEW.recipient_participant_id
        )
    ) THEN RAISE(ABORT, 'participant-bound recipient must name an active bound participant') END;
    SELECT CASE WHEN (
        SELECT th.kind FROM messages m JOIN message_threads th ON th.id = m.thread_id
        WHERE m.id = NEW.message_id
    ) = 'direct' AND NEW.recipient_participant_id IS NOT NULL
        THEN RAISE(ABORT, 'direct message recipient cannot name a bound participant') END;
END;

CREATE TRIGGER message_recipient_reject_delete
BEFORE DELETE ON message_recipients
BEGIN
    SELECT RAISE(ABORT, 'message recipients cannot be removed');
END;

CREATE TRIGGER message_recipient_binding_reject_update
BEFORE UPDATE OF message_id, recipient_agent_id, recipient_participant_id ON message_recipients
WHEN NEW.message_id <> OLD.message_id
  OR NEW.recipient_agent_id <> OLD.recipient_agent_id
  OR NEW.recipient_participant_id IS NOT OLD.recipient_participant_id
BEGIN
    SELECT RAISE(ABORT, 'message recipient binding is immutable');
END;

CREATE TRIGGER participant_wake_validate_insert
BEFORE INSERT ON message_wake_jobs
WHEN (
    SELECT th.kind
    FROM messages m JOIN message_threads th ON th.id = m.thread_id
    WHERE m.id = NEW.message_id
) = 'participant_bound'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM message_recipients mr
        JOIN messages m ON m.id = mr.message_id
        JOIN thread_participants p ON p.id = mr.recipient_participant_id
        JOIN runs r ON r.id = NEW.target_run_id
        WHERE mr.message_id = NEW.message_id
          AND mr.recipient_agent_id = NEW.recipient_agent_id
          AND m.workspace_id = r.workspace_id
          AND p.thread_id = m.thread_id
          AND p.agent_id = NEW.recipient_agent_id
          AND r.agent_id = p.agent_id
          AND r.project_id = p.project_id
          AND r.task_id = p.task_id
          AND r.status IN ('starting', 'active', 'blocked')
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id = m.thread_id) BETWEEN 2 AND 8
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id = m.thread_id) >=
              (SELECT initial_participant_count FROM message_threads WHERE id = m.thread_id)
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id = m.thread_id) =
              (SELECT initial_participant_count + participant_revision - 1 FROM message_threads WHERE id = m.thread_id)
          AND (SELECT COUNT(DISTINCT project_id) FROM thread_participants WHERE thread_id = m.thread_id) >= 2
    ) THEN RAISE(ABORT, 'participant wake target is outside its bound scope') END;
END;

CREATE TRIGGER message_wake_binding_reject_update
BEFORE UPDATE OF message_id, recipient_agent_id, target_run_id ON message_wake_jobs
WHEN NEW.message_id <> OLD.message_id
  OR NEW.recipient_agent_id <> OLD.recipient_agent_id
  OR NEW.target_run_id <> OLD.target_run_id
BEGIN
    SELECT RAISE(ABORT, 'message wake binding is immutable');
END;
