-- Packet rows and bindings are immutable authority. Validate redundant SQL
-- columns against the canonical packet JSON at the boundary.
CREATE TRIGGER context_packet_validate_insert
BEFORE INSERT ON context_packets
BEGIN
    SELECT CASE WHEN length(NEW.id) <> 36
        OR substr(NEW.id, 1, 4) <> 'ctx_'
        OR substr(NEW.id, 5) GLOB '*[^0-9a-f]*'
        THEN RAISE(ABORT, 'invalid context packet id') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.id') IS NOT NEW.id
        OR json_extract(NEW.packet_json, '$.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.packet_json, '$.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.packet_json, '$.task_id') IS NOT NEW.task_id
        OR json_extract(NEW.packet_json, '$.agent_id') IS NOT NEW.agent_id
        OR json_extract(NEW.packet_json, '$.checkout_id') IS NOT NEW.checkout_id
        OR json_extract(NEW.packet_json, '$.content_hash') IS NOT NEW.content_hash
        OR json_extract(NEW.packet_json, '$.byte_size') IS NOT NEW.byte_size
        OR json_extract(NEW.packet_json, '$.created_at') IS NOT NEW.created_at
        OR json_extract(NEW.packet_json, '$.created_by') IS NOT NEW.created_by
        OR NEW.created_by IS NOT 'local-owner'
        OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
        OR length(CAST(NEW.packet_json AS BLOB)) IS NOT NEW.byte_size
        THEN RAISE(ABORT, 'context packet row and JSON differ') END;
    SELECT CASE WHEN json_type(NEW.packet_json, '$.schema') IS NOT 'text' OR json_extract(NEW.packet_json, '$.schema') NOT IN (
        'urn:crewfold:schema:domain:context-packet:v1',
        'urn:crewfold:schema:domain:context-packet:v2',
        'urn:crewfold:schema:domain:context-packet:v3',
        'urn:crewfold:schema:domain:context-packet:v4'
    ) THEN RAISE(ABORT, 'unsupported context packet schema') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v4' AND (
          json_type(NEW.packet_json, '$.as_of_event_sequence') IS NOT 'integer'
          OR json_extract(NEW.packet_json, '$.as_of_event_sequence') < 0
          OR json_extract(NEW.packet_json, '$.as_of_event_sequence') IS NOT
             (SELECT COALESCE(MAX(sequence), 0) FROM events)
          OR json_extract(NEW.packet_json, '$.live_context.schema') IS NOT
             'urn:crewfold:schema:domain:live-context-policy:v1'
          OR json_extract(NEW.packet_json, '$.live_context.delivery') IS NOT 'explicit_pull'
          OR json_extract(NEW.packet_json, '$.live_context.ack_authority') IS NOT 'bound_run'
          OR json_extract(NEW.packet_json, '$.live_context.max_pending') IS NOT 1
          OR json_extract(NEW.packet_json, '$.live_context.max_relevant_events') IS NOT 1000
          OR json_extract(NEW.packet_json, '$.live_context.per_delta_limit_bytes') IS NOT 16384
          OR json_extract(NEW.packet_json, '$.live_context.cumulative_delta_limit_bytes') IS NOT 65536
          OR json_extract(NEW.packet_json, '$.policy.allowed_tools') IS NOT json_array(
             'crewfold_acknowledge_context_delta','crewfold_acknowledge_message','crewfold_get_briefing',
             'crewfold_get_context_delta','crewfold_get_status','crewfold_list_inbox','crewfold_propose_knowledge',
             'crewfold_propose_completion','crewfold_publish_artifact','crewfold_read_message',
             'crewfold_report_blocked','crewfold_report_contradiction','crewfold_report_progress','crewfold_send_message')
          OR json_extract(NEW.packet_json, '$.policy.denied_operations') IS NOT json_array(
             'change another run or task','push or merge source','deploy','message a person or broadcast','read unscoped context')
          OR json_extract(NEW.packet_json, '$.policy.approval_required') IS NOT json_array(
             'shared repository mutation','external side effect','destructive operation')
        ) THEN RAISE(ABORT, 'invalid version-four live context policy') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v4' AND EXISTS (
        SELECT 1 FROM tasks task JOIN agents agent ON agent.id = NEW.agent_id
        JOIN checkouts checkout ON checkout.id = NEW.checkout_id JOIN projects project ON project.id = NEW.project_id
        JOIN repositories repository ON repository.id = checkout.repository_id
        WHERE task.id = NEW.task_id AND (
          task.workspace_id IS NOT NEW.workspace_id OR task.project_id IS NOT NEW.project_id
          OR json_extract(NEW.packet_json, '$.role.name') IS NOT agent.name
          OR json_extract(NEW.packet_json, '$.role.role') IS NOT agent.role
          OR json_extract(NEW.packet_json, '$.role.provider') IS NOT agent.provider
          OR json_extract(NEW.packet_json, '$.role.runtime') IS NOT agent.runtime
          OR json_extract(NEW.packet_json, '$.role.revision') IS NOT agent.revision
          OR json_extract(NEW.packet_json, '$.task.assignment_id') IS NOT COALESCE((SELECT id FROM task_assignments WHERE task_id = task.id AND status = 'active'), '')
          OR json_extract(NEW.packet_json, '$.task.title') IS NOT task.title
          OR COALESCE(json_extract(NEW.packet_json, '$.task.description'), '') IS NOT COALESCE(task.description, '')
          OR json_extract(NEW.packet_json, '$.task.priority') IS NOT task.priority
          OR json_extract(NEW.packet_json, '$.task.revision') IS NOT task.revision
          OR json_extract(NEW.packet_json, '$.checkout.project_name') IS NOT project.name
          OR json_extract(NEW.packet_json, '$.checkout.repository_id') IS NOT repository.id
          OR json_extract(NEW.packet_json, '$.checkout.repository_fingerprint') IS NOT repository.fingerprint
          OR json_extract(NEW.packet_json, '$.checkout.path') IS NOT checkout.path
          OR json_extract(NEW.packet_json, '$.checkout.write_mode') IS NOT checkout.write_mode
          OR json_extract(NEW.packet_json, '$.checkout.checkout_kind') IS NOT checkout.checkout_kind
          OR checkout.availability <> 'available'
        )) THEN RAISE(ABORT, 'version-four context packet differs from canonical base authority') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v4' AND NEW.content_hash IS NOT
        'sha256:' || lower(hex(sha256(CAST(json_set(
            NEW.packet_json,
            '$.id', '',
            '$.content_hash', '',
            '$.created_at', '',
            '$.created_by', '',
            '$.byte_size', 0,
            '$.budget.total.used_bytes', 0,
            '$.budget.total.remaining_bytes', 32768
        ) AS BLOB))))
        THEN RAISE(ABORT, 'context packet semantic hash is invalid') END;
END;

CREATE TRIGGER run_context_binding_validate_insert
BEFORE INSERT ON run_context_bindings
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM runs run JOIN context_packets packet ON packet.id = NEW.context_packet_id
        WHERE run.id = NEW.run_id AND run.workspace_id = packet.workspace_id
          AND run.project_id = packet.project_id AND run.task_id = packet.task_id
          AND run.agent_id = packet.agent_id AND run.checkout_id = packet.checkout_id
    ) THEN RAISE(ABORT, 'run context binding authority differs') END;
    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM runs run JOIN context_packets packet ON packet.id = NEW.context_packet_id
        JOIN tasks task ON task.id = run.task_id JOIN agents agent ON agent.id = run.agent_id
        JOIN checkouts checkout ON checkout.id = run.checkout_id JOIN projects project ON project.id = run.project_id
        JOIN repositories repository ON repository.id = checkout.repository_id
        WHERE run.id = NEW.run_id AND json_extract(packet.packet_json, '$.schema') =
              'urn:crewfold:schema:domain:context-packet:v4' AND (
          json_extract(packet.packet_json, '$.role.agent_id') IS NOT agent.id
          OR json_extract(packet.packet_json, '$.role.name') IS NOT agent.name
          OR json_extract(packet.packet_json, '$.role.role') IS NOT agent.role
          OR json_extract(packet.packet_json, '$.role.provider') IS NOT agent.provider
          OR json_extract(packet.packet_json, '$.role.runtime') IS NOT agent.runtime
          OR json_extract(packet.packet_json, '$.role.revision') IS NOT agent.revision
          OR agent.enabled <> 1
          OR json_extract(packet.packet_json, '$.task.task_id') IS NOT task.id
          OR json_extract(packet.packet_json, '$.task.assignment_id') IS NOT COALESCE((
              SELECT id FROM task_assignments WHERE task_id = task.id AND status = 'active'), '')
          OR COALESCE(json_extract(packet.packet_json, '$.task.objective_id'), '') IS NOT COALESCE(task.objective_id, '')
          OR json_extract(packet.packet_json, '$.task.title') IS NOT task.title
          OR COALESCE(json_extract(packet.packet_json, '$.task.description'), '') IS NOT COALESCE(task.description, '')
          OR json_extract(packet.packet_json, '$.task.priority') IS NOT task.priority
          OR json_extract(packet.packet_json, '$.task.budget.token_limit') IS NOT task.budget_tokens
          OR json_extract(packet.packet_json, '$.task.budget.cost_cents') IS NOT task.budget_cost_cents
          OR json_extract(packet.packet_json, '$.task.budget.time_seconds') IS NOT task.budget_time_seconds
          OR json_extract(packet.packet_json, '$.task.revision') IS NOT task.revision
          OR json_extract(packet.packet_json, '$.checkout.checkout_id') IS NOT checkout.id
          OR json_extract(packet.packet_json, '$.checkout.project_id') IS NOT project.id
          OR json_extract(packet.packet_json, '$.checkout.project_name') IS NOT project.name
          OR json_extract(packet.packet_json, '$.checkout.repository_id') IS NOT repository.id
          OR json_extract(packet.packet_json, '$.checkout.repository_fingerprint') IS NOT repository.fingerprint
          OR json_extract(packet.packet_json, '$.checkout.path') IS NOT checkout.path
          OR json_extract(packet.packet_json, '$.checkout.write_mode') IS NOT checkout.write_mode
          OR json_extract(packet.packet_json, '$.checkout.checkout_kind') IS NOT checkout.checkout_kind
          OR checkout.availability <> 'available'
          OR json_extract(packet.packet_json, '$.dependencies') IS NOT (
              SELECT json_group_array(json_object('task_id', dependency_task.id, 'title', dependency_task.title,
                  'status', dependency_task.status, 'revision', dependency_task.revision))
              FROM (SELECT depends_on_task_id FROM task_dependencies WHERE task_id = task.id ORDER BY depends_on_task_id) edge
              JOIN tasks dependency_task ON dependency_task.id = edge.depends_on_task_id)
        )
    ) THEN RAISE(ABORT, 'run context packet base differs from canonical authority') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM events event JOIN runs run ON run.id = NEW.run_id
        JOIN context_packets packet ON packet.id = NEW.context_packet_id
        WHERE event.workspace_id = run.workspace_id
          AND event.entity_type = 'context_packet' AND event.entity_id = packet.id
          AND event.entity_revision = 1 AND event.type = 'context.packet_built'
          AND event.actor_id = 'local-owner' AND event.actor_type = 'human'
          AND json_extract(event.data_json, '$.task_id') = run.task_id
          AND json_extract(event.data_json, '$.agent_id') = run.agent_id
          AND json_extract(event.data_json, '$.checkout_id') = run.checkout_id
          AND (json_extract(packet.packet_json, '$.schema') IS NOT
               'urn:crewfold:schema:domain:context-packet:v4' OR (
              json_extract(event.data_json, '$.packet_schema') = json_extract(packet.packet_json, '$.schema')
              AND json_extract(event.data_json, '$.as_of_event_sequence') = json_extract(packet.packet_json, '$.as_of_event_sequence')))
          AND json_extract(event.data_json, '$.content_hash') = packet.content_hash
          AND json_extract(event.data_json, '$.byte_size') = packet.byte_size
    ) THEN RAISE(ABORT, 'run context packet has no exact built event') END;
END;

CREATE TRIGGER run_context_binding_reject_update
BEFORE UPDATE ON run_context_bindings
BEGIN
    SELECT RAISE(ABORT, 'run context bindings are immutable');
END;

CREATE TRIGGER run_context_binding_reject_delete
BEFORE DELETE ON run_context_bindings
BEGIN
    SELECT RAISE(ABORT, 'run context bindings are immutable');
END;

CREATE TABLE run_context_delta_state (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    context_packet_id TEXT NOT NULL UNIQUE REFERENCES context_packets(id),
    status TEXT NOT NULL CHECK (status IN ('ready', 'pending_ack', 'rebase_required')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    scan_event_sequence INTEGER NOT NULL CHECK (scan_event_sequence >= 0),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 0),
    last_delta_id TEXT REFERENCES context_deltas(id),
    pending_delta_id TEXT REFERENCES context_deltas(id),
    last_acknowledged_delta_id TEXT REFERENCES context_deltas(id),
    delta_count INTEGER NOT NULL CHECK (delta_count >= 0),
    cumulative_byte_size INTEGER NOT NULL CHECK (cumulative_byte_size BETWEEN 0 AND 65536),
    rebase_reason TEXT CHECK (rebase_reason IS NULL OR rebase_reason IN (
        'unsupported_packet','base_contract_changed','dependency_set_changed','event_window_exceeded',
        'delta_limit_exceeded','cumulative_limit_exceeded','unsupported_event_type')),
    rebase_event_sequence INTEGER REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (delta_count = last_sequence),
    CHECK ((last_sequence = 0 AND last_delta_id IS NULL) OR
           (last_sequence > 0 AND last_delta_id IS NOT NULL)),
    CHECK ((status = 'pending_ack' AND pending_delta_id IS NOT NULL) OR
           (status <> 'pending_ack' AND pending_delta_id IS NULL)),
    CHECK ((status = 'rebase_required' AND rebase_reason IS NOT NULL) OR
           (status <> 'rebase_required' AND rebase_reason IS NULL)),
    CHECK ((status = 'rebase_required' AND rebase_event_sequence IS NOT NULL) OR
           (status <> 'rebase_required' AND rebase_event_sequence IS NULL))
) STRICT;

CREATE TABLE context_deltas (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 39 AND substr(id, 1, 7) = 'cdelta_'
        AND substr(id, 8) NOT GLOB '*[^0-9a-f]*'
    ),
    run_id TEXT NOT NULL REFERENCES runs(id),
    context_packet_id TEXT NOT NULL REFERENCES context_packets(id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    parent_delta_id TEXT REFERENCES context_deltas(id),
    from_event_sequence INTEGER NOT NULL CHECK (from_event_sequence >= 0),
    through_event_sequence INTEGER NOT NULL CHECK (through_event_sequence >= from_event_sequence),
    delta_json TEXT NOT NULL CHECK (json_valid(delta_json)),
    content_hash TEXT NOT NULL CHECK (
        length(content_hash) = 71 AND substr(content_hash, 1, 7) = 'sha256:'
        AND substr(content_hash, 8) NOT GLOB '*[^0-9a-f]*'
    ),
    byte_size INTEGER NOT NULL CHECK (byte_size BETWEEN 1 AND 16384),
    built_event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    UNIQUE (run_id, sequence),
    UNIQUE (id, run_id)
) STRICT;

CREATE INDEX context_deltas_run_sequence_idx
    ON context_deltas(run_id, sequence);

CREATE TABLE context_delta_acknowledgements (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'cdack_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    run_id TEXT NOT NULL REFERENCES runs(id),
    context_packet_id TEXT NOT NULL REFERENCES context_packets(id),
    delta_id TEXT NOT NULL UNIQUE REFERENCES context_deltas(id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    acknowledged_at TEXT NOT NULL,
    acknowledged_by TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK (
        length(request_hash) = 64 AND request_hash NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    UNIQUE (run_id, idempotency_key)
) STRICT;

CREATE INDEX context_delta_ack_run_sequence_idx
    ON context_delta_acknowledgements(run_id, sequence);

CREATE TRIGGER context_delta_validate_insert
BEFORE INSERT ON context_deltas
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM run_context_bindings binding
        JOIN run_context_delta_state state ON state.run_id = binding.run_id
        WHERE binding.run_id = NEW.run_id
          AND binding.context_packet_id = NEW.context_packet_id
          AND state.context_packet_id = NEW.context_packet_id
          AND state.status = 'ready'
          AND state.pending_delta_id IS NULL
          AND NEW.sequence = state.last_sequence + 1
          AND NEW.parent_delta_id IS state.last_delta_id
          AND NEW.from_event_sequence = state.scan_event_sequence
          AND state.cumulative_byte_size + NEW.byte_size <= 65536
    ) THEN RAISE(ABORT, 'context delta does not extend the ready run chain') END;
    SELECT CASE WHEN json_extract(NEW.delta_json, '$.schema') IS NOT
            'urn:crewfold:schema:domain:context-delta:v1'
        OR json_extract(NEW.delta_json, '$.id') IS NOT NEW.id
        OR json_extract(NEW.delta_json, '$.run_id') IS NOT NEW.run_id
        OR json_extract(NEW.delta_json, '$.context_packet_id') IS NOT NEW.context_packet_id
        OR json_extract(NEW.delta_json, '$.sequence') IS NOT NEW.sequence
        OR COALESCE(json_extract(NEW.delta_json, '$.parent_delta_id'), '') IS NOT
           COALESCE(NEW.parent_delta_id, '')
        OR json_extract(NEW.delta_json, '$.from_event_sequence') IS NOT NEW.from_event_sequence
        OR json_extract(NEW.delta_json, '$.through_event_sequence') IS NOT NEW.through_event_sequence
        OR json_extract(NEW.delta_json, '$.created_at') IS NOT NEW.created_at
        OR json_extract(NEW.delta_json, '$.created_by') IS NOT NEW.created_by
        OR json_extract(NEW.delta_json, '$.evaluated_at') IS NOT NEW.created_at
        OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
        OR json_extract(NEW.delta_json, '$.content_hash') IS NOT NEW.content_hash
        OR json_extract(NEW.delta_json, '$.byte_size') IS NOT NEW.byte_size
        OR json_extract(NEW.delta_json, '$.budget.total.limit_bytes') IS NOT 16384
        OR json_extract(NEW.delta_json, '$.budget.total.used_bytes') IS NOT NEW.byte_size
        OR json_extract(NEW.delta_json, '$.budget.total.remaining_bytes') IS NOT 16384 - NEW.byte_size
        OR json_extract(NEW.delta_json, '$.budget.chain.limit_bytes') IS NOT 65536
        OR json_extract(NEW.delta_json, '$.budget.chain.used_bytes') IS NOT
           ((SELECT cumulative_byte_size FROM run_context_delta_state WHERE run_id = NEW.run_id) + NEW.byte_size)
        OR json_extract(NEW.delta_json, '$.budget.chain.remaining_bytes') IS NOT
           (65536 - (SELECT cumulative_byte_size FROM run_context_delta_state WHERE run_id = NEW.run_id) - NEW.byte_size)
        OR length(CAST(NEW.delta_json AS BLOB)) IS NOT NEW.byte_size
        THEN RAISE(ABORT, 'context delta row and JSON differ') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM runs run
        JOIN run_context_bindings binding ON binding.run_id = run.id
        JOIN context_packets packet ON packet.id = binding.context_packet_id
        WHERE run.id = NEW.run_id AND binding.context_packet_id = NEW.context_packet_id
          AND json_extract(NEW.delta_json, '$.workspace_id') = run.workspace_id
          AND json_extract(NEW.delta_json, '$.project_id') = run.project_id
          AND json_extract(NEW.delta_json, '$.task_id') = run.task_id
          AND json_extract(NEW.delta_json, '$.agent_id') = run.agent_id
          AND json_extract(NEW.delta_json, '$.base_packet_schema') =
              json_extract(packet.packet_json, '$.schema')
    ) THEN RAISE(ABORT, 'context delta scope differs from its bound run') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM events event
        WHERE event.sequence = NEW.built_event_sequence
          AND event.workspace_id = json_extract(NEW.delta_json, '$.workspace_id')
          AND event.entity_type = 'context_delta' AND event.entity_id = NEW.id
          AND event.entity_revision = NEW.sequence
          AND event.type = 'context_delta.built'
          AND event.actor_id = 'local-owner' AND event.actor_type = 'human'
          AND event.occurred_at = NEW.created_at AND event.recorded_at = NEW.created_at
          AND json_extract(event.data_json, '$.run_id') = NEW.run_id
          AND json_extract(event.data_json, '$.context_packet_id') = NEW.context_packet_id
          AND json_extract(event.data_json, '$.sequence') = NEW.sequence
          AND json_extract(event.data_json, '$.state_revision') =
              (SELECT revision + 1 FROM run_context_delta_state WHERE run_id = NEW.run_id)
          AND COALESCE(json_extract(event.data_json, '$.parent_delta_id'), '') = COALESCE(NEW.parent_delta_id, '')
          AND json_extract(event.data_json, '$.from_event_sequence') = NEW.from_event_sequence
          AND json_extract(event.data_json, '$.through_event_sequence') = NEW.through_event_sequence
          AND json_extract(event.data_json, '$.content_hash') = NEW.content_hash
          AND json_extract(event.data_json, '$.byte_size') = NEW.byte_size
          AND json_extract(event.data_json, '$.change_count') = json_array_length(NEW.delta_json, '$.changes')
          AND json_extract(event.data_json, '$.change_kinds') = (SELECT json_group_array(json_extract(value, '$.kind')) FROM json_each(NEW.delta_json, '$.changes'))
    ) THEN RAISE(ABORT, 'context delta built event is missing or inconsistent') END;
    SELECT CASE WHEN NEW.created_by IS NOT 'local-owner'
        OR NEW.from_event_sequence > (SELECT COALESCE(MAX(sequence), 0) FROM events)
        OR NEW.through_event_sequence > (SELECT COALESCE(MAX(sequence), 0) FROM events)
        OR NEW.through_event_sequence >= NEW.built_event_sequence
        THEN RAISE(ABORT, 'context delta actor or cursor is invalid') END;
    SELECT CASE WHEN NEW.content_hash IS NOT 'sha256:' || lower(hex(sha256(CAST(json_set(
        NEW.delta_json, '$.id', '', '$.content_hash', '', '$.created_at', '', '$.created_by', '',
        '$.byte_size', 0, '$.budget.total.used_bytes', 0,
        '$.budget.total.remaining_bytes', 16384, '$.budget.chain.used_bytes', 0,
        '$.budget.chain.remaining_bytes', 65536
    ) AS BLOB)))) THEN RAISE(ABORT, 'context delta semantic hash is invalid') END;
END;

CREATE TRIGGER context_delta_reject_update
BEFORE UPDATE ON context_deltas
BEGIN
    SELECT RAISE(ABORT, 'context deltas are immutable');
END;

CREATE TRIGGER context_delta_reject_delete
BEFORE DELETE ON context_deltas
BEGIN
    SELECT RAISE(ABORT, 'context deltas are immutable');
END;

CREATE TRIGGER context_delta_ack_validate_insert
BEFORE INSERT ON context_delta_acknowledgements
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.acknowledged_at) <> 1
        THEN RAISE(ABORT, 'acknowledgement timestamp is invalid') END;
    SELECT CASE WHEN NEW.acknowledged_by <> NEW.run_id OR NOT EXISTS (
        SELECT 1
        FROM run_context_delta_state state
        JOIN context_deltas delta ON delta.id = state.pending_delta_id
        WHERE state.run_id = NEW.run_id
          AND state.context_packet_id = NEW.context_packet_id
          AND state.status = 'pending_ack'
          AND delta.id = NEW.delta_id
          AND delta.sequence = NEW.sequence
          AND delta.run_id = NEW.run_id
    ) THEN RAISE(ABORT, 'acknowledgement is not for the exact pending run delta') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM events event
        WHERE event.sequence = NEW.event_sequence
          AND event.entity_type = 'context_delta_acknowledgement'
          AND event.entity_id = NEW.id AND event.entity_revision = 1
          AND event.type = 'context_delta.acknowledged'
          AND event.workspace_id = (SELECT workspace_id FROM runs WHERE id = NEW.run_id)
          AND event.actor_id = NEW.run_id AND event.actor_type = 'agent_run'
          AND event.occurred_at = NEW.acknowledged_at AND event.recorded_at = NEW.acknowledged_at
          AND json_extract(event.data_json, '$.run_id') = NEW.run_id
          AND json_extract(event.data_json, '$.context_packet_id') = NEW.context_packet_id
          AND json_extract(event.data_json, '$.delta_id') = NEW.delta_id
          AND json_extract(event.data_json, '$.acknowledgement_id') = NEW.id
          AND json_extract(event.data_json, '$.state_revision') =
              (SELECT revision + 1 FROM run_context_delta_state WHERE run_id = NEW.run_id)
          AND json_extract(event.data_json, '$.sequence') = NEW.sequence
          AND json_extract(event.data_json, '$.through_event_sequence') =
              (SELECT through_event_sequence FROM context_deltas WHERE id = NEW.delta_id)
    ) THEN RAISE(ABORT, 'context delta acknowledgement event is missing or inconsistent') END;
END;

CREATE TRIGGER run_context_delta_state_validate_insert
BEFORE INSERT ON run_context_delta_state
BEGIN
    SELECT CASE WHEN NEW.status <> 'ready' OR NEW.revision <> 1
        OR NEW.last_sequence <> 0 OR NEW.last_delta_id IS NOT NULL
        OR NEW.pending_delta_id IS NOT NULL OR NEW.last_acknowledged_delta_id IS NOT NULL
        OR NEW.delta_count <> 0 OR NEW.cumulative_byte_size <> 0
        OR NEW.rebase_reason IS NOT NULL OR NEW.rebase_event_sequence IS NOT NULL
        OR NEW.created_at <> NEW.updated_at
        OR crewfold_timestamp_canonical(NEW.created_at) <> 1
        OR NEW.scan_event_sequence > (SELECT COALESCE(MAX(sequence), 0) FROM events)
        OR NOT EXISTS (
            SELECT 1 FROM run_context_bindings binding
            JOIN context_packets packet ON packet.id = binding.context_packet_id
            WHERE binding.run_id = NEW.run_id
              AND binding.context_packet_id = NEW.context_packet_id
              AND json_extract(packet.packet_json, '$.schema') =
                  'urn:crewfold:schema:domain:context-packet:v4'
              AND json_extract(packet.packet_json, '$.as_of_event_sequence') =
                  NEW.scan_event_sequence
        ) THEN RAISE(ABORT, 'invalid initial run context delta state') END;
END;

CREATE TRIGGER context_delta_ack_reject_update
BEFORE UPDATE ON context_delta_acknowledgements
BEGIN
    SELECT RAISE(ABORT, 'context delta acknowledgements are immutable');
END;

CREATE TRIGGER context_delta_ack_reject_delete
BEFORE DELETE ON context_delta_acknowledgements
BEGIN
    SELECT RAISE(ABORT, 'context delta acknowledgements are immutable');
END;

CREATE TRIGGER run_context_delta_state_reject_delete
BEFORE DELETE ON run_context_delta_state
BEGIN
    SELECT RAISE(ABORT, 'run context delta state cannot be removed');
END;

CREATE TRIGGER run_context_delta_state_validate_update
BEFORE UPDATE ON run_context_delta_state
WHEN NEW.run_id <> OLD.run_id
  OR NEW.context_packet_id <> OLD.context_packet_id
  OR NEW.created_at <> OLD.created_at
  OR crewfold_timestamp_canonical(NEW.updated_at) <> 1
  OR NEW.revision <> OLD.revision + 1
  OR NEW.scan_event_sequence < OLD.scan_event_sequence
  OR NEW.scan_event_sequence > (SELECT COALESCE(MAX(sequence), 0) FROM events)
  OR NOT (
      -- An event-free scan cursor advance.
      (OLD.status = 'ready' AND NEW.status = 'ready'
       AND NEW.scan_event_sequence > OLD.scan_event_sequence
       AND NEW.last_sequence = OLD.last_sequence
       AND NEW.last_delta_id IS OLD.last_delta_id
       AND NEW.pending_delta_id IS NULL
       AND NEW.last_acknowledged_delta_id IS OLD.last_acknowledged_delta_id
       AND NEW.delta_count = OLD.delta_count
       AND NEW.cumulative_byte_size = OLD.cumulative_byte_size
       AND NEW.rebase_reason IS NULL AND NEW.rebase_event_sequence IS NULL)
      OR
      -- One immutable delta becomes the sole pending head.
      (OLD.status = 'ready' AND NEW.status = 'pending_ack'
       AND NEW.last_sequence = OLD.last_sequence + 1
       AND NEW.pending_delta_id IS NEW.last_delta_id
       AND NEW.last_acknowledged_delta_id IS OLD.last_acknowledged_delta_id
       AND NEW.delta_count = OLD.delta_count + 1
       AND NEW.rebase_reason IS NULL AND NEW.rebase_event_sequence IS NULL
       AND EXISTS (
          SELECT 1 FROM context_deltas delta
          WHERE delta.id = NEW.last_delta_id AND delta.run_id = NEW.run_id
            AND delta.context_packet_id = NEW.context_packet_id
            AND delta.sequence = NEW.last_sequence
            AND delta.parent_delta_id IS OLD.last_delta_id
            AND delta.from_event_sequence = OLD.scan_event_sequence
            AND delta.through_event_sequence = NEW.scan_event_sequence
            AND NEW.cumulative_byte_size = OLD.cumulative_byte_size + delta.byte_size
       ))
      OR
      -- The bound run acknowledges exactly the pending head.
      (OLD.status = 'pending_ack' AND NEW.status = 'ready'
       AND NEW.scan_event_sequence = OLD.scan_event_sequence
       AND NEW.last_sequence = OLD.last_sequence
       AND NEW.last_delta_id IS OLD.last_delta_id
       AND NEW.pending_delta_id IS NULL
       AND NEW.last_acknowledged_delta_id IS OLD.pending_delta_id
       AND NEW.delta_count = OLD.delta_count
       AND NEW.cumulative_byte_size = OLD.cumulative_byte_size
       AND NEW.rebase_reason IS NULL AND NEW.rebase_event_sequence IS NULL
       AND EXISTS (
          SELECT 1 FROM context_delta_acknowledgements ack
          WHERE ack.delta_id = OLD.pending_delta_id AND ack.run_id = NEW.run_id
       ))
      OR
      -- A safe ready chain becomes terminal and requires a fresh base packet.
      (OLD.status = 'ready' AND NEW.status = 'rebase_required'
       AND NEW.last_sequence = OLD.last_sequence
       AND NEW.last_delta_id IS OLD.last_delta_id
       AND NEW.pending_delta_id IS NULL
       AND NEW.last_acknowledged_delta_id IS OLD.last_acknowledged_delta_id
       AND NEW.delta_count = OLD.delta_count
       AND NEW.cumulative_byte_size = OLD.cumulative_byte_size
       AND NEW.rebase_reason IS NOT NULL AND NEW.rebase_event_sequence IS NOT NULL
       AND EXISTS (
          SELECT 1 FROM events event
          WHERE event.sequence = NEW.rebase_event_sequence
            AND event.entity_type = 'run_context_delta_state'
            AND event.entity_id = NEW.run_id
            AND event.entity_revision = NEW.revision
            AND event.type = 'context_delta.rebase_required'
            AND event.workspace_id = (SELECT workspace_id FROM runs WHERE id = NEW.run_id)
            AND event.actor_id = 'local-owner' AND event.actor_type = 'human'
            AND event.occurred_at = NEW.updated_at AND event.recorded_at = NEW.updated_at
            AND json_extract(event.data_json, '$.run_id') = NEW.run_id
            AND json_extract(event.data_json, '$.context_packet_id') = NEW.context_packet_id
            AND json_extract(event.data_json, '$.reason') = NEW.rebase_reason
            AND json_extract(event.data_json, '$.scan_from') = OLD.scan_event_sequence
            AND json_extract(event.data_json, '$.through_event_sequence') = NEW.scan_event_sequence
            AND json_extract(event.data_json, '$.delta_count') = OLD.delta_count
            AND json_extract(event.data_json, '$.cumulative_byte_size') = OLD.cumulative_byte_size
       ))
  )
BEGIN
    SELECT RAISE(ABORT, 'illegal run context delta state transition');
END;
