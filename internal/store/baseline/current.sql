-- Crewfold's single greenfield schema. There are no historical upgrade paths.

CREATE TABLE schema_baseline (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    source_sha256 TEXT NOT NULL CHECK (
        length(source_sha256) = 64 AND source_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    catalog_sha256 TEXT NOT NULL CHECK (
        length(catalog_sha256) = 64 AND catalog_sha256 NOT GLOB '*[^0-9a-f]*'
    )
) STRICT;

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    occurred_at TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    entity_revision INTEGER NOT NULL CHECK (entity_revision > 0),
    correlation_id TEXT NOT NULL,
    causation_id TEXT,
    data_json TEXT NOT NULL CHECK (json_valid(data_json))
) STRICT;

CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, name)
) STRICT;

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    fingerprint TEXT NOT NULL,
    object_format TEXT NOT NULL CHECK (object_format IN ('sha1', 'sha256')),
    root_commits_json TEXT NOT NULL CHECK (json_valid(root_commits_json)),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, fingerprint)
) STRICT;

CREATE TABLE project_repositories (
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    attached_at TEXT NOT NULL,
    PRIMARY KEY (project_id, repository_id)
) STRICT;

CREATE TABLE checkouts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    path TEXT NOT NULL UNIQUE,
    write_mode TEXT NOT NULL CHECK (write_mode IN ('exclusive', 'claimed', 'shared', 'read_only')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    availability TEXT NOT NULL CHECK (availability IN ('available', 'unavailable')),
    checkout_kind TEXT NOT NULL CHECK (checkout_kind IN ('standalone', 'linked_worktree', 'unknown')),
    branch TEXT,
    head_commit TEXT,
    dirty INTEGER NOT NULL CHECK (dirty IN (0, 1)),
    git_dir TEXT,
    git_common_dir TEXT,
    observed_at TEXT NOT NULL,
    diagnostic_code TEXT,
    diagnostic TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
, dirty_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(dirty_paths_json))) STRICT;

CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    provider TEXT NOT NULL,
    runtime TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, name)
) STRICT;

CREATE TABLE objectives (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'completed', 'cancelled')),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE domain_agent_memberships (
    project_id TEXT NOT NULL REFERENCES projects(id),
    agent_id TEXT PRIMARY KEY REFERENCES agents(id),
    parent_agent_id TEXT REFERENCES agents(id),
    workstream_id TEXT REFERENCES objectives(id),
    preferred_entry INTEGER NOT NULL CHECK (preferred_entry IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by = 'local-owner'),
    UNIQUE (project_id, agent_id),
    CHECK (parent_agent_id IS NULL OR parent_agent_id <> agent_id),
    CHECK (status = 'active' OR preferred_entry = 0),
    FOREIGN KEY (project_id, parent_agent_id)
      REFERENCES domain_agent_memberships(project_id, agent_id)
      DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE domain_agent_session_bindings (
    project_id TEXT NOT NULL,
    agent_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (
        length(CAST(provider AS BLOB)) BETWEEN 1 AND 64
        AND provider = lower(provider)
        AND provider NOT GLOB '*[^a-z0-9._-]*'
    ),
    node_id TEXT NOT NULL CHECK (
        length(node_id) = 32 AND node_id NOT GLOB '*[^0-9a-f]*'
    ),
    node_fingerprint TEXT NOT NULL CHECK (
        length(node_fingerprint) = 64
        AND node_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    thread_id TEXT NOT NULL CHECK (
        length(CAST(thread_id AS BLOB)) BETWEEN 1 AND 512
        AND instr(thread_id, char(0)) = 0
    ),
    cwd TEXT NOT NULL CHECK (
        length(CAST(cwd AS BLOB)) BETWEEN 1 AND 4096
        AND substr(cwd, 1, 1) = '/'
        AND instr(cwd, char(0)) = 0
    ),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, agent_id),
    UNIQUE (node_fingerprint, provider, thread_id),
    FOREIGN KEY (project_id, agent_id)
      REFERENCES domain_agent_memberships(project_id, agent_id)
) STRICT;

CREATE TABLE domain_agent_tool_receipts (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 41 AND substr(id, 1, 9) = 'toolrcpt_'
        AND substr(id, 10) NOT GLOB '*[^0-9a-f]*'
    ),
    project_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_revision INTEGER NOT NULL CHECK (session_revision > 0),
    call_id TEXT NOT NULL CHECK (
        length(CAST(call_id AS BLOB)) BETWEEN 1 AND 512
        AND instr(call_id, char(0)) = 0
    ),
    turn_id TEXT NOT NULL CHECK (
        length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 512
        AND instr(turn_id, char(0)) = 0
    ),
    tool_name TEXT NOT NULL CHECK (
        length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 128
        AND tool_name = lower(tool_name)
        AND tool_name NOT GLOB '*[^a-z0-9._-]*'
    ),
    request_sha256 TEXT NOT NULL CHECK (
        length(request_sha256) = 64
        AND request_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed')),
    response_sha256 TEXT NOT NULL CHECK (
        length(response_sha256) = 64
        AND response_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    response_json TEXT NOT NULL CHECK (
        json_valid(response_json) AND json_type(response_json) = 'object'
        AND length(CAST(response_json AS BLOB)) BETWEEN 1 AND 262144
    ),
    created_at TEXT NOT NULL,
    UNIQUE (project_id, agent_id, call_id),
    FOREIGN KEY (project_id, agent_id)
      REFERENCES domain_agent_session_bindings(project_id, agent_id)
) STRICT;

CREATE TABLE domain_agent_staffing_grants (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 43 AND substr(id, 1, 11) = 'staffgrant_'
        AND substr(id, 12) NOT GLOB '*[^0-9a-f]*'
    ),
    project_id TEXT NOT NULL,
    manager_agent_id TEXT NOT NULL,
    manager_membership_revision INTEGER NOT NULL CHECK (manager_membership_revision > 0),
    max_descendants INTEGER NOT NULL CHECK (max_descendants BETWEEN 1 AND 1000),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    expires_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked', 'expired')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by = 'local-owner'),
    UNIQUE (id, project_id, manager_agent_id),
    FOREIGN KEY (project_id, manager_agent_id)
      REFERENCES domain_agent_memberships(project_id, agent_id)
) STRICT;

CREATE TABLE domain_agent_staffing_profiles (
    grant_id TEXT NOT NULL REFERENCES domain_agent_staffing_grants(id),
    provider TEXT NOT NULL CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
    runtime TEXT NOT NULL CHECK (length(CAST(runtime AS BLOB)) BETWEEN 1 AND 128),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    PRIMARY KEY (grant_id, provider, runtime)
) STRICT;

CREATE TABLE domain_agent_staffing_task_classes (
    grant_id TEXT NOT NULL REFERENCES domain_agent_staffing_grants(id),
    task_class TEXT NOT NULL CHECK (
        length(CAST(task_class AS BLOB)) BETWEEN 1 AND 63
        AND task_class = lower(task_class)
        AND task_class NOT GLOB '*[^a-z0-9._-]*'
    ),
    PRIMARY KEY (grant_id, task_class)
) STRICT;

CREATE TABLE domain_agent_staffing_allocations (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 43 AND substr(id, 1, 11) = 'staffalloc_'
        AND substr(id, 12) NOT GLOB '*[^0-9a-f]*'
    ),
    grant_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    parent_agent_id TEXT NOT NULL,
    child_agent_id TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    runtime TEXT NOT NULL,
    task_class TEXT NOT NULL,
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    request_sha256 TEXT NOT NULL CHECK (
        length(request_sha256) = 64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    UNIQUE (grant_id, child_agent_id),
    FOREIGN KEY (grant_id, project_id, parent_agent_id)
      REFERENCES domain_agent_staffing_grants(id, project_id, manager_agent_id),
    FOREIGN KEY (project_id, child_agent_id)
      REFERENCES domain_agent_memberships(project_id, agent_id),
    FOREIGN KEY (grant_id, provider, runtime)
      REFERENCES domain_agent_staffing_profiles(grant_id, provider, runtime),
    FOREIGN KEY (grant_id, task_class)
      REFERENCES domain_agent_staffing_task_classes(grant_id, task_class)
) STRICT;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT REFERENCES objectives(id),
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL CHECK (status IN ('ready', 'assigned', 'active', 'blocked', 'review', 'changes_requested', 'completed', 'failed', 'cancelled')),
    blocked_reason TEXT,
    priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    depends_on_task_id TEXT NOT NULL REFERENCES tasks(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
) STRICT;

CREATE TABLE task_assignments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'released')),
    lease_expires_at TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    runtime TEXT NOT NULL,
    provider TEXT NOT NULL,
    scenario_name TEXT NOT NULL,
    scenario_json TEXT NOT NULL CHECK (json_valid(scenario_json)),
    placement_reasons_json TEXT NOT NULL CHECK (json_valid(placement_reasons_json)),
    status TEXT NOT NULL CHECK (status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'stopped', 'lost', 'review', 'completed', 'start_failed', 'failed')),
    step_cursor INTEGER NOT NULL CHECK (step_cursor >= 0),
    blocked_question TEXT,
    result_summary TEXT,
    failure_code TEXT,
    failure_message TEXT,
    stop_grace_millis INTEGER NOT NULL DEFAULT 0 CHECK (stop_grace_millis >= 0 AND stop_grace_millis <= 30000),
    stop_forced INTEGER NOT NULL DEFAULT 0 CHECK (stop_forced IN (0, 1)),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
, assignment_id TEXT REFERENCES task_assignments(id)) STRICT;

CREATE TABLE run_runtime_bindings (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    node_id TEXT NOT NULL CHECK (
        length(node_id) = 32 AND node_id NOT GLOB '*[^0-9a-f]*'
    ),
    node_fingerprint TEXT NOT NULL CHECK (
        length(node_fingerprint) = 64
        AND node_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    operation_id TEXT NOT NULL UNIQUE CHECK (operation_id = run_id),
    runtime_handle TEXT NOT NULL CHECK (
        length(CAST(runtime_handle AS BLOB)) BETWEEN 1 AND 8192
        AND instr(runtime_handle, char(0)) = 0
    ),
    provider_handle TEXT CHECK (
        provider_handle IS NULL OR (
            length(CAST(provider_handle AS BLOB)) BETWEEN 1 AND 8192
            AND instr(provider_handle, char(0)) = 0
        )
    ),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE immutable_artifacts (
    content_sha256 TEXT PRIMARY KEY CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    byte_size INTEGER NOT NULL CHECK (byte_size BETWEEN 0 AND 1048576),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (
        created_by IN ('crewfold-check-worker', 'subsystem:run-worker')
    )
) STRICT;

CREATE TABLE run_log_artifacts (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 44 AND substr(id, 1, 12) = 'runartifact_'
        AND substr(id, 13) NOT GLOB '*[^0-9a-f]*'
    ),
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL CHECK (kind IN ('stdout', 'stderr')),
    content_sha256 TEXT NOT NULL REFERENCES immutable_artifacts(content_sha256),
    captured_bytes INTEGER NOT NULL CHECK (captured_bytes BETWEEN 0 AND 65536),
    omitted_bytes INTEGER NOT NULL CHECK (omitted_bytes >= 0),
    truncated INTEGER NOT NULL CHECK (
        truncated IN (0, 1) AND truncated = (omitted_bytes > 0)
    ),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'subsystem:run-worker'),
    UNIQUE (run_id, kind)
) STRICT;

CREATE TABLE run_loss_resolutions (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    lost_revision INTEGER NOT NULL CHECK (lost_revision > 0),
    resolution TEXT NOT NULL CHECK (
        resolution = 'owner_confirmed_effects_ended'
    ),
    note TEXT NOT NULL CHECK (
        length(CAST(note AS BLOB)) BETWEEN 1 AND 4096
        AND instr(note, char(0)) = 0
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    resolved_at TEXT NOT NULL,
    resolved_by TEXT NOT NULL CHECK (resolved_by = 'local-owner')
) STRICT;

CREATE TABLE run_jobs (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'complete')),
    available_at TEXT NOT NULL,
    lease_expires_at TEXT,
    attempts INTEGER NOT NULL CHECK (attempts >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
, origin TEXT NOT NULL DEFAULT 'owner'
    CHECK (origin IN ('owner','supervisor'))) STRICT;

CREATE TABLE run_timeline (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    message TEXT,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_handoffs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    summary TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

CREATE TABLE context_packets (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    packet_json TEXT NOT NULL CHECK (json_valid(packet_json)),
    content_hash TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size > 0 AND byte_size <= 32768),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

CREATE TABLE run_context_bindings (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    context_packet_id TEXT NOT NULL UNIQUE REFERENCES context_packets(id),
    bound_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_capabilities (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_reports (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL CHECK (kind IN ('progress', 'blocked', 'completion', 'executive_response')),
    message TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    handoff TEXT,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'applied')),
    created_at TEXT NOT NULL,
    applied_at TEXT,
    UNIQUE (run_id, idempotency_key)
) STRICT;

CREATE TABLE run_artifacts (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0 AND byte_size <= 32768),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, idempotency_key)
) STRICT;

CREATE TABLE run_tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    request_id TEXT NOT NULL,
    method TEXT NOT NULL,
    target_id TEXT,
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
    error_code TEXT,
    recorded_at TEXT NOT NULL
) STRICT;

CREATE TABLE message_threads (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT REFERENCES projects(id),
    task_id TEXT REFERENCES tasks(id),
    subject TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('open', 'closed')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
, kind TEXT NOT NULL DEFAULT 'direct'
    CHECK (kind IN ('direct', 'participant_bound')), participant_revision INTEGER NOT NULL DEFAULT 0
    CHECK (participant_revision >= 0), initial_participant_count INTEGER NOT NULL DEFAULT 0
    CHECK (initial_participant_count BETWEEN 0 AND 8)) STRICT;

CREATE TABLE work_claims (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    checkout_id TEXT REFERENCES checkouts(id),
    kind TEXT NOT NULL CHECK (kind IN ('path', 'component', 'operation')),
    target TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('exclusive', 'shared', 'advisory')),
    conflict_policy TEXT NOT NULL CHECK (conflict_policy IN ('notify', 'deny_new', 'pause_scheduling', 'request_resolution')),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'released')),
    baseline_paths_json TEXT NOT NULL CHECK (json_valid(baseline_paths_json)),
    lease_expires_at TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE work_overlaps (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    claim_low_id TEXT NOT NULL REFERENCES work_claims(id),
    claim_high_id TEXT NOT NULL REFERENCES work_claims(id),
    task_low_id TEXT NOT NULL REFERENCES tasks(id),
    task_high_id TEXT NOT NULL REFERENCES tasks(id),
    kind TEXT NOT NULL CHECK (kind IN ('path', 'component', 'operation')),
    witness TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    policy_response TEXT NOT NULL CHECK (policy_response IN ('notify', 'deny_new', 'pause_scheduling', 'request_resolution')),
    scheduling_paused INTEGER NOT NULL CHECK (scheduling_paused IN (0, 1)),
    resolution_required INTEGER NOT NULL CHECK (resolution_required IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    explanation_json TEXT NOT NULL CHECK (json_valid(explanation_json)),
    detected_at TEXT NOT NULL,
    resolved_at TEXT,
    resolution_reason TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    UNIQUE (claim_low_id, claim_high_id),
    CHECK (claim_low_id < claim_high_id),
    CHECK (task_low_id <> task_high_id)
) STRICT;

CREATE TABLE task_coordination_holds (
    overlap_id TEXT NOT NULL REFERENCES work_overlaps(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (overlap_id, task_id)
) STRICT;

CREATE TABLE claim_drifts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    claim_id TEXT NOT NULL REFERENCES work_claims(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    path TEXT NOT NULL,
    head_commit TEXT,
    observation_gap INTEGER NOT NULL CHECK (observation_gap IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    first_observed_at TEXT NOT NULL,
    last_observed_at TEXT NOT NULL,
    resolved_at TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    UNIQUE (task_id, checkout_id, path)
) STRICT;

CREATE TABLE checkout_claim_scans (
    checkout_id TEXT PRIMARY KEY REFERENCES checkouts(id),
    watcher_id TEXT NOT NULL,
    head_commit TEXT,
    dirty_paths_json TEXT NOT NULL CHECK (json_valid(dirty_paths_json)),
    observed_at TEXT NOT NULL
) STRICT;

CREATE TABLE meetings (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    overlap_id TEXT NOT NULL REFERENCES work_overlaps(id),
    agenda TEXT NOT NULL,
    facilitator_agent_id TEXT NOT NULL REFERENCES agents(id),
    policy TEXT NOT NULL CHECK (policy IN ('owner_decision', 'named_reviewer', 'manager_bounded')),
    reviewer_agent_id TEXT REFERENCES agents(id),
    allowed_actions_json TEXT NOT NULL CHECK (json_valid(allowed_actions_json)),
    status TEXT NOT NULL CHECK (status IN ('gathering_positions', 'facilitator_pending', 'awaiting_approval', 'awaiting_reviewer', 'concluded', 'stalled', 'cancelled')),
    frozen_input_json TEXT NOT NULL CHECK (json_valid(frozen_input_json)),
    frozen_input_hash TEXT NOT NULL,
    deadline_at TEXT NOT NULL,
    stalled_reason TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE meeting_participants (
    meeting_id TEXT NOT NULL REFERENCES meetings(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    task_id TEXT REFERENCES tasks(id),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    status TEXT NOT NULL CHECK (status IN ('pending', 'submitted', 'missing')),
    PRIMARY KEY (meeting_id, agent_id),
    UNIQUE (meeting_id, ordinal)
) STRICT;

CREATE TABLE meeting_contributions (
    id TEXT PRIMARY KEY,
    meeting_id TEXT NOT NULL REFERENCES meetings(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    round TEXT NOT NULL CHECK (round IN ('position', 'review')),
    summary TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    submitted_at TEXT NOT NULL,
    UNIQUE (meeting_id, agent_id, round)
) STRICT;

CREATE TABLE meeting_proposals (
    id TEXT PRIMARY KEY,
    meeting_id TEXT NOT NULL UNIQUE REFERENCES meetings(id),
    proposed_by TEXT NOT NULL REFERENCES agents(id),
    summary TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'rejected')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    proposed_at TEXT NOT NULL,
    decided_at TEXT,
    decision_note TEXT
) STRICT;

CREATE TABLE meeting_actions (
    id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES meeting_proposals(id),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    type TEXT NOT NULL CHECK (type IN ('sequence', 'split', 'reassign', 'designate_role', 'cancel')),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    status TEXT NOT NULL CHECK (status IN ('pending', 'applied', 'failed')),
    result_entity_id TEXT,
    diagnostic TEXT,
    applied_at TEXT,
    UNIQUE (proposal_id, ordinal)
) STRICT;

CREATE TABLE task_roles (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    role TEXT NOT NULL CHECK (role IN ('implementer', 'reviewer')),
    source_meeting_id TEXT NOT NULL REFERENCES meetings(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    PRIMARY KEY (task_id, agent_id, role)
) STRICT;

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

CREATE VIRTUAL TABLE knowledge_search USING fts5(
    revision_id UNINDEXED,
    workspace_id UNINDEXED,
    title,
    body,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE knowledge_search_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation INTEGER NOT NULL CHECK (generation > 0),
    built_at TEXT NOT NULL,
    source_event_sequence INTEGER NOT NULL CHECK (source_event_sequence >= 0),
    source_count INTEGER NOT NULL CHECK (source_count >= 0),
    source_digest TEXT NOT NULL CHECK (
        length(source_digest) = 64 AND source_digest NOT GLOB '*[^0-9a-f]*'
    )
) STRICT;

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

CREATE TABLE knowledge_item_task_scopes (
    item_id TEXT PRIMARY KEY REFERENCES knowledge_items(id),
    task_id TEXT NOT NULL REFERENCES knowledge_task_scope_anchors(task_id)
) STRICT;

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
        'base_contract_changed','dependency_set_changed','event_window_exceeded',
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

CREATE TABLE manager_grants (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 41 AND substr(id, 1, 9) = 'mgrgrant_'
        AND substr(id, 10) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id),
    objective_revision INTEGER NOT NULL CHECK (objective_revision > 0),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    task_revision INTEGER NOT NULL CHECK (task_revision > 0),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    agent_revision INTEGER NOT NULL CHECK (agent_revision > 0),
    proposal_kinds_json TEXT NOT NULL CHECK (
        json_valid(proposal_kinds_json)
        AND json_type(proposal_kinds_json) = 'array'
        AND json_array_length(proposal_kinds_json) BETWEEN 1 AND 4
    ),
    launch_profiles_json TEXT NOT NULL CHECK (
        json_valid(launch_profiles_json)
        AND json_type(launch_profiles_json) = 'array'
        AND json_array_length(launch_profiles_json) BETWEEN 1 AND 32
    ),
    allowed_claim_kinds_json TEXT NOT NULL CHECK (
        json_valid(allowed_claim_kinds_json)
        AND json_type(allowed_claim_kinds_json) = 'array'
        AND json_array_length(allowed_claim_kinds_json) BETWEEN 0 AND 3
    ),
    max_open_proposals INTEGER NOT NULL CHECK (max_open_proposals BETWEEN 1 AND 32),
    max_actions INTEGER NOT NULL CHECK (max_actions BETWEEN 1 AND 32),
    max_tasks INTEGER NOT NULL CHECK (max_tasks BETWEEN 1 AND 16),
    max_dependencies INTEGER NOT NULL CHECK (max_dependencies BETWEEN 1 AND 32),
    max_claim_requirements INTEGER NOT NULL CHECK (max_claim_requirements BETWEEN 1 AND 32),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    content_json TEXT NOT NULL CHECK (json_valid(content_json) AND json_type(content_json) = 'object'),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    expires_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked', 'expired')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by = 'local-owner')
) STRICT;

CREATE TABLE launch_profiles (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'lprof_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    agent_revision INTEGER NOT NULL CHECK (agent_revision > 0),
    purpose TEXT CHECK (purpose IS NULL OR length(CAST(purpose AS BLOB)) BETWEEN 1 AND 128),
    runtime TEXT NOT NULL CHECK (length(CAST(runtime AS BLOB)) BETWEEN 1 AND 128),
    provider TEXT NOT NULL CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
    checkout_id TEXT REFERENCES checkouts(id),
    scenario_json TEXT NOT NULL CHECK (json_valid(scenario_json)),
    scenario_sha256 TEXT NOT NULL CHECK (
        length(scenario_sha256) = 64
        AND scenario_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    content_json TEXT NOT NULL CHECK (json_valid(content_json) AND json_type(content_json) = 'object'),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    assignment_lease_seconds INTEGER NOT NULL CHECK (assignment_lease_seconds BETWEEN 30 AND 86400),
    capability_ttl_seconds INTEGER NOT NULL CHECK (capability_ttl_seconds BETWEEN 30 AND 86400),
    manager_grant_id TEXT REFERENCES manager_grants(id),
    status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by = 'local-owner')
) STRICT;

CREATE TABLE manager_grant_proposal_kinds (
    grant_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 3),
    kind TEXT NOT NULL CHECK (kind IN ('assignment','escalation','review','task_decomposition')),
    PRIMARY KEY (grant_id, ordinal),
    UNIQUE (grant_id, kind),
    FOREIGN KEY (grant_id) REFERENCES manager_grants(id)
      DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE manager_grant_launch_profiles (
    grant_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 31),
    launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id),
    launch_profile_revision INTEGER NOT NULL CHECK (launch_profile_revision > 0),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    agent_revision INTEGER NOT NULL CHECK (agent_revision > 0),
    PRIMARY KEY (grant_id, ordinal),
    UNIQUE (grant_id, launch_profile_id),
    FOREIGN KEY (grant_id) REFERENCES manager_grants(id)
      DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE manager_grant_claim_kinds (
    grant_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 2),
    kind TEXT NOT NULL CHECK (kind IN ('component','operation','path')),
    PRIMARY KEY (grant_id, ordinal),
    UNIQUE (grant_id, kind),
    FOREIGN KEY (grant_id) REFERENCES manager_grants(id)
      DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE manager_proposals (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'mprop_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id),
    objective_revision INTEGER NOT NULL CHECK (objective_revision > 0),
    source_run_id TEXT NOT NULL REFERENCES runs(id),
    source_agent_id TEXT NOT NULL REFERENCES agents(id),
    grant_id TEXT NOT NULL REFERENCES manager_grants(id),
    grant_revision INTEGER NOT NULL CHECK (grant_revision > 0),
    kind TEXT NOT NULL CHECK (kind IN ('task_decomposition','assignment','review','escalation')),
    summary TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','invalid','accepted','rejected','stale')),
    as_of_event_sequence INTEGER NOT NULL CHECK (as_of_event_sequence >= 0),
    actions_json TEXT NOT NULL CHECK (
        json_valid(actions_json) AND json_type(actions_json) = 'array'
        AND json_array_length(actions_json) BETWEEN 1 AND 32
        AND length(CAST(actions_json AS BLOB)) <= 49152
    ),
    validation_issues_json TEXT NOT NULL CHECK (
        json_valid(validation_issues_json) AND json_type(validation_issues_json) = 'array'
        AND json_array_length(validation_issues_json) <= 64
    ),
    content_json TEXT NOT NULL CHECK (
		json_valid(content_json) AND json_type(content_json) = 'object'
		AND length(CAST(content_json AS BLOB)) <= 49152
    ),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    decision_note TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    decided_at TEXT,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    decided_by TEXT
) STRICT;

CREATE TABLE manager_proposal_effects (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'mpeff_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id),
    proposal_id TEXT NOT NULL REFERENCES manager_proposals(id),
    action_id TEXT NOT NULL,
    effect_type TEXT NOT NULL CHECK (effect_type IN ('created','dependency_added')),
    entity_type TEXT NOT NULL CHECK (entity_type IN ('task','scheduling_intent','task_claim_requirement','supervisor_action','approval_request')),
    entity_id TEXT NOT NULL,
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    created_at TEXT NOT NULL,
    UNIQUE (proposal_id, action_id, effect_type, entity_type, entity_id),
    FOREIGN KEY (action_id, proposal_id)
      REFERENCES manager_proposal_actions(id, proposal_id)
) STRICT;

CREATE TABLE manager_proposal_actions (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 38 AND substr(id, 1, 6) = 'mpact_'
        AND substr(id, 7) NOT GLOB '*[^0-9a-f]*'
    ),
    proposal_id TEXT NOT NULL REFERENCES manager_proposals(id),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 31),
    type TEXT NOT NULL CHECK (type IN (
        'create_task','add_dependency','declare_claim_requirement',
        'assign_task','request_review','request_action'
    )),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    UNIQUE (proposal_id, ordinal),
    UNIQUE (id, proposal_id)
) STRICT;

CREATE TABLE manager_proposal_submissions (
    proposal_id TEXT PRIMARY KEY REFERENCES manager_proposals(id),
    action_count INTEGER NOT NULL CHECK (action_count BETWEEN 1 AND 32),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    submitted_at TEXT NOT NULL,
    submitted_by TEXT NOT NULL
) STRICT;

CREATE TABLE manager_proposal_decisions (
    proposal_id TEXT PRIMARY KEY REFERENCES manager_proposals(id),
    status TEXT NOT NULL CHECK (status IN ('accepted','rejected','stale')),
    proposal_revision INTEGER NOT NULL CHECK (proposal_revision > 1),
    effect_count INTEGER NOT NULL CHECK (effect_count BETWEEN 0 AND 128),
    effects_sha256 TEXT NOT NULL CHECK (
        length(effects_sha256) = 64 AND effects_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    decided_at TEXT NOT NULL,
    decided_by TEXT NOT NULL CHECK (decided_by = 'local-owner')
) STRICT;

CREATE TABLE task_claim_requirements (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 39 AND substr(id, 1, 7) = 'claimr_'
        AND substr(id, 8) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    source_proposal_id TEXT NOT NULL REFERENCES manager_proposals(id),
    source_action_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('path','component','operation')),
    target TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('exclusive','shared','advisory')),
    conflict_policy TEXT NOT NULL CHECK (conflict_policy IN ('notify','deny_new','pause_scheduling','request_resolution')),
    revision INTEGER NOT NULL CHECK (revision = 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by = 'local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by = 'local-owner'),
    UNIQUE (source_proposal_id, source_action_id),
    FOREIGN KEY (source_action_id, source_proposal_id)
      REFERENCES manager_proposal_actions(id, proposal_id)
) STRICT;

CREATE TABLE supervisor_policies (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
    max_active_runs INTEGER NOT NULL CHECK (max_active_runs BETWEEN 1 AND 100),
    max_starting_runs INTEGER NOT NULL CHECK (max_starting_runs BETWEEN 1 AND 100),
    default_project_concurrency INTEGER NOT NULL CHECK (default_project_concurrency BETWEEN 1 AND 100),
    default_provider_concurrency INTEGER NOT NULL CHECK (default_provider_concurrency BETWEEN 1 AND 100),
    project_concurrency_json TEXT NOT NULL CHECK (json_valid(project_concurrency_json) AND json_type(project_concurrency_json) = 'object'),
    provider_concurrency_json TEXT NOT NULL CHECK (json_valid(provider_concurrency_json) AND json_type(provider_concurrency_json) = 'object'),
    auto_schedule INTEGER NOT NULL CHECK (auto_schedule IN (0,1)),
    auto_retry_limit INTEGER NOT NULL CHECK (auto_retry_limit BETWEEN 0 AND 3),
    retry_cooldown_seconds INTEGER NOT NULL CHECK (retry_cooldown_seconds BETWEEN 0 AND 86400),
    event_sequence INTEGER NOT NULL CHECK (event_sequence >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (workspace_id, revision),
    CHECK (max_starting_runs <= max_active_runs)
) STRICT;

CREATE TABLE supervisor_policy_project_limits (
    workspace_id TEXT NOT NULL,
    policy_revision INTEGER NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    PRIMARY KEY (workspace_id, policy_revision, project_id),
    FOREIGN KEY (workspace_id, policy_revision)
      REFERENCES supervisor_policies(workspace_id, revision) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE supervisor_policy_provider_limits (
    workspace_id TEXT NOT NULL,
    policy_revision INTEGER NOT NULL,
    provider TEXT NOT NULL CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    PRIMARY KEY (workspace_id, policy_revision, provider),
    FOREIGN KEY (workspace_id, policy_revision)
      REFERENCES supervisor_policies(workspace_id, revision) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE supervisor_actions (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 40 AND substr(id, 1, 8) = 'saction_'
        AND substr(id, 9) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT REFERENCES projects(id),
    objective_id TEXT REFERENCES objectives(id),
    task_id TEXT REFERENCES tasks(id),
    run_id TEXT REFERENCES runs(id),
    prior_run_id TEXT REFERENCES runs(id),
    source_proposal_id TEXT REFERENCES manager_proposals(id),
    source_action_id TEXT,
    agent_id TEXT REFERENCES agents(id),
    intent_id TEXT REFERENCES scheduling_intents(id),
    condition TEXT NOT NULL CHECK (condition IN ('dependency_ready','blocked','stale','failed','repeated_failure','over_budget','manager_escalation')),
    condition_key TEXT NOT NULL CHECK (
        length(condition_key) = 64 AND condition_key NOT GLOB '*[^0-9a-f]*'
    ),
    response TEXT NOT NULL CHECK (response IN ('schedule','resume_run','stop_run','retry_task','reassign_task','request_owner')),
    status TEXT NOT NULL CHECK (status IN ('proposed','awaiting_approval','applied','deferred','dismissed','failed')),
    decision TEXT,
    entity_revision INTEGER NOT NULL CHECK (entity_revision > 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    as_of_event_sequence INTEGER NOT NULL CHECK (as_of_event_sequence >= 0),
    reasons_json TEXT NOT NULL CHECK (json_valid(reasons_json) AND json_type(reasons_json) = 'array' AND json_array_length(reasons_json) BETWEEN 1 AND 16),
    constraint_snapshot_json TEXT NOT NULL CHECK (json_valid(constraint_snapshot_json) AND json_type(constraint_snapshot_json) = 'object'),
    content_sha256 TEXT NOT NULL CHECK (
        length(content_sha256) = 64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    approval_id TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    applied_at TEXT,
    created_by TEXT NOT NULL CHECK (created_by = 'subsystem:supervisor'),
    updated_by TEXT NOT NULL
	, FOREIGN KEY (source_action_id,source_proposal_id) REFERENCES manager_proposal_actions(id,proposal_id)
) STRICT;

CREATE TABLE supervisor_action_receipts (
    action_id TEXT PRIMARY KEY REFERENCES supervisor_actions(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    condition_key TEXT NOT NULL UNIQUE CHECK (
      length(condition_key)=64 AND condition_key NOT GLOB '*[^0-9a-f]*'
    ),
    event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
    recorded_status TEXT NOT NULL CHECK (recorded_status IN ('applied','deferred','awaiting_approval','proposed')),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE TABLE approval_requests (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 37 AND substr(id, 1, 5) = 'appr_'
        AND substr(id, 6) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    action_id TEXT NOT NULL UNIQUE REFERENCES supervisor_actions(id),
    status TEXT NOT NULL CHECK (status IN ('pending','granted','denied','expired','consumed')),
    decision_note TEXT,
    decision_event_sequence INTEGER REFERENCES events(sequence),
    expected_action_revision INTEGER NOT NULL CHECK (expected_action_revision > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    decided_at TEXT,
    created_by TEXT NOT NULL CHECK (created_by = 'subsystem:supervisor'),
    updated_by TEXT NOT NULL,
    decided_by TEXT
) STRICT;

CREATE TABLE supervisor_state (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
    last_event_sequence INTEGER NOT NULL CHECK (last_event_sequence >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_scheduling_receipts (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    intent_id TEXT NOT NULL UNIQUE REFERENCES scheduling_intents(id),
    action_id TEXT NOT NULL UNIQUE REFERENCES supervisor_actions(id),
    launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id),
    launch_profile_revision INTEGER NOT NULL CHECK (launch_profile_revision > 0),
    assignment_id TEXT NOT NULL UNIQUE REFERENCES task_assignments(id),
    task_revision INTEGER NOT NULL CHECK (task_revision > 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_retry_receipts (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    prior_run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
    intent_id TEXT NOT NULL REFERENCES scheduling_intents(id),
    action_id TEXT NOT NULL UNIQUE REFERENCES supervisor_actions(id),
    launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id),
    launch_profile_revision INTEGER NOT NULL CHECK (launch_profile_revision > 0),
    assignment_id TEXT NOT NULL REFERENCES task_assignments(id),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    attempt INTEGER NOT NULL CHECK (attempt BETWEEN 1 AND 3),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE "messages" (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    thread_id TEXT NOT NULL REFERENCES message_threads(id),
    project_id TEXT REFERENCES projects(id),
    task_id TEXT REFERENCES tasks(id),
    sender_type TEXT NOT NULL CHECK (sender_type IN ('owner','agent_run','durable_agent','subsystem')),
    sender_id TEXT NOT NULL,
    sender_agent_id TEXT REFERENCES agents(id),
    sender_run_id TEXT REFERENCES runs(id),
    kind TEXT NOT NULL CHECK (kind IN ('inform','question','request','review_request','handoff','decision_notice','risk','conflict','approval_request')),
    body TEXT NOT NULL,
    artifact_ids_json TEXT NOT NULL CHECK (json_valid(artifact_ids_json)),
    reply_to_message_id TEXT REFERENCES "messages"(id) DEFERRABLE INITIALLY DEFERRED,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE "message_recipients" (
    message_id TEXT NOT NULL REFERENCES "messages"(id),
    recipient_agent_id TEXT NOT NULL REFERENCES agents(id),
    status TEXT NOT NULL CHECK(status IN ('queued','delivered','read','acknowledged')),
    queued_at TEXT NOT NULL,
    delivered_at TEXT,
    read_at TEXT,
    acknowledged_at TEXT,
    delivered_run_id TEXT REFERENCES runs(id),
    recipient_participant_id TEXT REFERENCES thread_participants(id),
    PRIMARY KEY(message_id,recipient_agent_id)
) STRICT;

CREATE TABLE "message_wake_jobs" (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    message_id TEXT NOT NULL REFERENCES "messages"(id),
    recipient_agent_id TEXT NOT NULL REFERENCES agents(id),
    target_run_id TEXT NOT NULL REFERENCES runs(id),
    status TEXT NOT NULL CHECK(status IN ('pending','leased','succeeded','failed','failed_unknown')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
    available_at TEXT NOT NULL,
    lease_expires_at TEXT,
    diagnostic TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(message_id,recipient_agent_id,target_run_id)
) STRICT;

CREATE TABLE check_definitions (
    id TEXT PRIMARY KEY CHECK (length(id)=41 AND substr(id,1,9)='checkdef_' AND substr(id,10) NOT GLOB '*[^0-9a-f]*'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128 AND instr(name,char(0))=0),
    executable TEXT NOT NULL CHECK (substr(executable,1,1)='/' AND length(CAST(executable AS BLOB)) BETWEEN 2 AND 4096 AND instr(executable,char(0))=0),
    working_directory TEXT NOT NULL CHECK (length(CAST(working_directory AS BLOB)) BETWEEN 1 AND 1024 AND substr(working_directory,1,1)<>'/' AND instr(working_directory,char(0))=0),
    timeout_millis INTEGER NOT NULL CHECK (timeout_millis BETWEEN 100 AND 3600000),
    output_byte_limit INTEGER NOT NULL CHECK (output_byte_limit BETWEEN 1024 AND 1048576),
    arguments_json TEXT NOT NULL CHECK (json_valid(arguments_json) AND json_type(arguments_json)='array' AND json_array_length(arguments_json) BETWEEN 0 AND 64),
    content_json TEXT NOT NULL CHECK (json_valid(content_json) AND json_type(content_json)='object'),
    content_revision INTEGER NOT NULL CHECK (content_revision=1),
    content_sha256 TEXT NOT NULL CHECK (length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK (status IN ('active','retired')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by='local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by='local-owner')
) STRICT;

CREATE TABLE check_definition_arguments (
    definition_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 63),
    argument TEXT NOT NULL CHECK (length(CAST(argument AS BLOB)) BETWEEN 0 AND 4096 AND instr(argument,char(0))=0),
    PRIMARY KEY(definition_id,ordinal),
    FOREIGN KEY(definition_id) REFERENCES check_definitions(id) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE task_check_requirements (
    id TEXT PRIMARY KEY CHECK (length(id)=41 AND substr(id,1,9)='checkreq_' AND substr(id,10) NOT GLOB '*[^0-9a-f]*'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    task_revision_at_creation INTEGER NOT NULL CHECK (task_revision_at_creation>0),
    criterion_key TEXT NOT NULL CHECK (length(CAST(criterion_key AS BLOB)) BETWEEN 1 AND 128 AND instr(criterion_key,char(0))=0),
    statement TEXT NOT NULL CHECK (length(CAST(statement AS BLOB)) BETWEEN 1 AND 2048 AND instr(statement,char(0))=0),
    definition_id TEXT NOT NULL REFERENCES check_definitions(id),
    definition_content_revision INTEGER NOT NULL CHECK (definition_content_revision>0),
    status TEXT NOT NULL CHECK (status IN ('active','retired')),
    revision INTEGER NOT NULL CHECK (revision>0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by='local-owner'),
    updated_by TEXT NOT NULL CHECK (updated_by='local-owner')
) STRICT;

CREATE TABLE check_watch_grants (
    id TEXT PRIMARY KEY CHECK (length(id)=43 AND substr(id,1,11)='checkgrant_' AND substr(id,12) NOT GLOB '*[^0-9a-f]*'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
    agent_id TEXT NOT NULL REFERENCES agents(id), agent_revision INTEGER NOT NULL CHECK(agent_revision>0),
    operations_json TEXT NOT NULL CHECK(json_valid(operations_json) AND json_type(operations_json)='array' AND json_array_length(operations_json) BETWEEN 1 AND 3),
    definitions_json TEXT NOT NULL CHECK(json_valid(definitions_json) AND json_type(definitions_json)='array' AND json_array_length(definitions_json) BETWEEN 1 AND 64),
    max_pending INTEGER NOT NULL CHECK(max_pending BETWEEN 1 AND 100),
    max_in_flight INTEGER NOT NULL CHECK(max_in_flight BETWEEN 1 AND max_pending),
    expires_at TEXT,
    content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json)='object'),
    content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK(status IN ('active','revoked','expired')), revision INTEGER NOT NULL CHECK(revision>0),
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK(created_by='local-owner'), updated_by TEXT NOT NULL CHECK(updated_by='local-owner')
) STRICT;

CREATE TABLE check_watch_grant_operations (
 grant_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 2), operation TEXT NOT NULL CHECK(operation IN ('run','inspect','propose_repair')),
 PRIMARY KEY(grant_id,ordinal), UNIQUE(grant_id,operation), FOREIGN KEY(grant_id) REFERENCES check_watch_grants(id) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE check_watch_grant_definitions (
 grant_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63), definition_id TEXT NOT NULL REFERENCES check_definitions(id),
 definition_content_revision INTEGER NOT NULL CHECK(definition_content_revision>0), definition_sha256 TEXT NOT NULL CHECK(length(definition_sha256)=64 AND definition_sha256 NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(grant_id,ordinal), UNIQUE(grant_id,definition_id), FOREIGN KEY(grant_id) REFERENCES check_watch_grants(id) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE check_policies (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT PRIMARY KEY REFERENCES projects(id),
 repair_proposals_enabled INTEGER NOT NULL CHECK(repair_proposals_enabled IN (0,1)),
 repair_launch_profile_id TEXT REFERENCES launch_profiles(id), repair_launch_profile_revision INTEGER CHECK(repair_launch_profile_revision IS NULL OR repair_launch_profile_revision>0),
 max_open_repair_proposals INTEGER NOT NULL CHECK(max_open_repair_proposals BETWEEN 1 AND 32), revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner'), updated_by TEXT NOT NULL CHECK(updated_by='local-owner'),
 CHECK((repair_proposals_enabled=0 AND repair_launch_profile_id IS NULL AND repair_launch_profile_revision IS NULL) OR (repair_proposals_enabled=1 AND repair_launch_profile_id IS NOT NULL AND repair_launch_profile_revision IS NOT NULL))
) STRICT;

CREATE TABLE check_routes (
 id TEXT PRIMARY KEY CHECK(length(id)=43 AND substr(id,1,11)='checkroute_' AND substr(id,12) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id), definition_id TEXT REFERENCES check_definitions(id), definition_content_revision INTEGER CHECK(definition_content_revision IS NULL OR definition_content_revision>0),
 trigger TEXT NOT NULL CHECK(trigger IN ('pass','nonpass','stale')), duty TEXT NOT NULL CHECK(duty IN ('evidence_review','coordination')),
 agent_id TEXT NOT NULL REFERENCES agents(id), agent_revision INTEGER NOT NULL CHECK(agent_revision>0), status TEXT NOT NULL CHECK(status IN ('active','retired')), revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner'), updated_by TEXT NOT NULL CHECK(updated_by='local-owner'),
 CHECK((definition_id IS NULL)=(definition_content_revision IS NULL))
) STRICT;

CREATE TABLE check_runs (
 id TEXT PRIMARY KEY CHECK(length(id)=41 AND substr(id,1,9)='checkrun_' AND substr(id,10) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL CHECK(task_revision>0),
 requirement_id TEXT NOT NULL REFERENCES task_check_requirements(id), requirement_revision INTEGER NOT NULL CHECK(requirement_revision>0),
 definition_id TEXT NOT NULL REFERENCES check_definitions(id), definition_content_revision INTEGER NOT NULL CHECK(definition_content_revision>0), definition_sha256 TEXT NOT NULL CHECK(length(definition_sha256)=64 AND definition_sha256 NOT GLOB '*[^0-9a-f]*'),
 checkout_id TEXT NOT NULL REFERENCES checkouts(id), checkout_revision INTEGER NOT NULL CHECK(checkout_revision>0), repository_id TEXT NOT NULL REFERENCES repositories(id), repository_object_format TEXT NOT NULL CHECK(repository_object_format IN ('sha1','sha256')), checkout_path TEXT NOT NULL, checkout_write_mode TEXT NOT NULL CHECK(checkout_write_mode IN ('exclusive','claimed','shared','read_only')),
 source_type TEXT NOT NULL CHECK(source_type IN ('owner','agent_run')), source_actor_id TEXT NOT NULL, source_agent_id TEXT REFERENCES agents(id), source_agent_revision INTEGER CHECK(source_agent_revision IS NULL OR source_agent_revision>0), source_run_id TEXT REFERENCES runs(id), source_grant_id TEXT REFERENCES check_watch_grants(id), source_grant_revision INTEGER CHECK(source_grant_revision IS NULL OR source_grant_revision>0), source_max_in_flight INTEGER NOT NULL CHECK(source_max_in_flight BETWEEN 0 AND 100),
 status TEXT NOT NULL CHECK(status IN ('requested','starting','running','finished')), revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, created_by TEXT NOT NULL, updated_by TEXT NOT NULL,
 CHECK((source_type='owner' AND source_actor_id='local-owner' AND source_agent_id IS NULL AND source_agent_revision IS NULL AND source_run_id IS NULL AND source_grant_id IS NULL AND source_grant_revision IS NULL AND source_max_in_flight=0) OR (source_type='agent_run' AND source_agent_id IS NOT NULL AND source_agent_revision IS NOT NULL AND source_run_id IS NOT NULL AND source_grant_id IS NOT NULL AND source_grant_revision IS NOT NULL AND source_max_in_flight>0))
) STRICT;

CREATE TABLE check_runtime_bindings (
 check_run_id TEXT PRIMARY KEY REFERENCES check_runs(id),
 node_id TEXT NOT NULL CHECK(length(node_id)=32 AND node_id NOT GLOB '*[^0-9a-f]*'),
 node_fingerprint TEXT NOT NULL CHECK(length(node_fingerprint)=64 AND node_fingerprint NOT GLOB '*[^0-9a-f]*'),
 operation_id TEXT NOT NULL UNIQUE CHECK(operation_id=check_run_id),
 runtime_handle TEXT NOT NULL CHECK(length(CAST(runtime_handle AS BLOB)) BETWEEN 1 AND 8192 AND instr(runtime_handle,char(0))=0),
 revision INTEGER NOT NULL CHECK(revision>0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE check_jobs (
 id TEXT PRIMARY KEY CHECK(length(id)=41 AND substr(id,1,9)='checkjob_' AND substr(id,10) NOT GLOB '*[^0-9a-f]*'), check_run_id TEXT NOT NULL UNIQUE REFERENCES check_runs(id),
 status TEXT NOT NULL CHECK(status IN ('pending','leased','complete')), available_at TEXT NOT NULL, lease_expires_at TEXT, attempts INTEGER NOT NULL CHECK(attempts>=0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE check_launch_receipts (
 id TEXT PRIMARY KEY CHECK(length(id)=44 AND substr(id,1,12)='checklaunch_' AND substr(id,13) NOT GLOB '*[^0-9a-f]*'), check_run_id TEXT NOT NULL UNIQUE REFERENCES check_runs(id), check_job_id TEXT NOT NULL UNIQUE REFERENCES check_jobs(id),
 operation_id TEXT NOT NULL UNIQUE, effective_spec_sha256 TEXT NOT NULL CHECK(length(effective_spec_sha256)=64 AND effective_spec_sha256 NOT GLOB '*[^0-9a-f]*'), effective_working_directory TEXT NOT NULL CHECK(substr(effective_working_directory,1,1)='/' AND length(CAST(effective_working_directory AS BLOB)) BETWEEN 2 AND 4096 AND instr(effective_working_directory,char(0))=0), launchable INTEGER NOT NULL CHECK(launchable IN (0,1)), preflight_failure_code TEXT, preflight_failure_diagnostic TEXT, definition_sha256 TEXT NOT NULL CHECK(length(definition_sha256)=64 AND definition_sha256 NOT GLOB '*[^0-9a-f]*'),
 source_type TEXT NOT NULL, source_actor_id TEXT NOT NULL, source_agent_id TEXT, source_agent_revision INTEGER, source_run_id TEXT, source_grant_id TEXT, source_grant_revision INTEGER,
 observation_available INTEGER NOT NULL CHECK(observation_available IN (0,1)), repository_id TEXT NOT NULL REFERENCES repositories(id), object_format TEXT NOT NULL CHECK(object_format IN ('sha1','sha256')), checkout_id TEXT NOT NULL REFERENCES checkouts(id), branch TEXT, head_commit TEXT, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)), dirty_paths_json TEXT NOT NULL CHECK(json_valid(dirty_paths_json) AND json_type(dirty_paths_json)='array'), observed_at TEXT NOT NULL, diagnostic_code TEXT, diagnostic TEXT,
 created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='crewfold-check-worker'),
 CHECK((launchable=1 AND preflight_failure_code IS NULL AND preflight_failure_diagnostic IS NULL) OR (launchable=0 AND preflight_failure_code IN ('working_directory_invalid','authority_revoked','definition_retired','requirement_retired','checkout_changed') AND preflight_failure_diagnostic IS NOT NULL AND length(CAST(preflight_failure_diagnostic AS BLOB)) BETWEEN 1 AND 4096))
) STRICT;

CREATE TABLE check_results (
 id TEXT PRIMARY KEY CHECK(length(id)=44 AND substr(id,1,12)='checkresult_' AND substr(id,13) NOT GLOB '*[^0-9a-f]*'), check_run_id TEXT NOT NULL UNIQUE REFERENCES check_runs(id),
 requirement_id TEXT NOT NULL REFERENCES task_check_requirements(id), requirement_revision INTEGER NOT NULL CHECK(requirement_revision>0), definition_id TEXT NOT NULL REFERENCES check_definitions(id), definition_content_revision INTEGER NOT NULL CHECK(definition_content_revision>0),
 outcome TEXT NOT NULL CHECK(outcome IN ('passed','failed','timed_out','start_failed','unknown')), exit_code INTEGER, forced INTEGER NOT NULL CHECK(forced IN (0,1)), diagnostic_code TEXT, diagnostic TEXT,
 observation_available INTEGER NOT NULL CHECK(observation_available IN (0,1)), repository_id TEXT NOT NULL REFERENCES repositories(id), object_format TEXT NOT NULL CHECK(object_format IN ('sha1','sha256')), checkout_id TEXT NOT NULL REFERENCES checkouts(id), branch TEXT, head_commit TEXT, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)), dirty_paths_json TEXT NOT NULL CHECK(json_valid(dirty_paths_json) AND json_type(dirty_paths_json)='array'), observed_at TEXT NOT NULL, observation_diagnostic_code TEXT, observation_diagnostic TEXT,
 created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='crewfold-check-worker'),
 CHECK((outcome='passed' AND exit_code=0 AND forced=0) OR (outcome='failed' AND exit_code IS NOT NULL AND exit_code<>0) OR outcome IN ('timed_out','start_failed','unknown'))
) STRICT;

CREATE TABLE check_artifacts (
 id TEXT PRIMARY KEY CHECK(length(id)=46 AND substr(id,1,14)='checkartifact_' AND substr(id,15) NOT GLOB '*[^0-9a-f]*'), check_result_id TEXT NOT NULL REFERENCES check_results(id), kind TEXT NOT NULL CHECK(kind IN ('stdout','stderr','diagnostic')),
 content_sha256 TEXT NOT NULL REFERENCES immutable_artifacts(content_sha256), captured_bytes INTEGER NOT NULL CHECK(captured_bytes>=0 AND captured_bytes<=CASE kind WHEN 'diagnostic' THEN 4096 ELSE 1048576 END), omitted_bytes INTEGER NOT NULL CHECK(omitted_bytes>=0), truncated INTEGER NOT NULL CHECK(truncated IN (0,1) AND truncated=(omitted_bytes>0)), created_at TEXT NOT NULL,
 UNIQUE(check_result_id,kind)
) STRICT;

CREATE TABLE check_result_freshness (
 id TEXT PRIMARY KEY CHECK(length(id)=43 AND substr(id,1,11)='checkfresh_' AND substr(id,12) NOT GLOB '*[^0-9a-f]*'), check_result_id TEXT NOT NULL REFERENCES check_results(id), revision INTEGER NOT NULL CHECK(revision>0), status TEXT NOT NULL CHECK(status IN ('fresh','stale','unknown')), reason TEXT NOT NULL,
 initially_eligible INTEGER NOT NULL CHECK(initially_eligible IN (0,1)), ever_stale INTEGER NOT NULL CHECK(ever_stale IN (0,1)), observation_available INTEGER NOT NULL CHECK(observation_available IN (0,1)), repository_id TEXT NOT NULL REFERENCES repositories(id), object_format TEXT NOT NULL CHECK(object_format IN ('sha1','sha256')), checkout_id TEXT NOT NULL REFERENCES checkouts(id), branch TEXT, head_commit TEXT, dirty INTEGER NOT NULL CHECK(dirty IN (0,1)), dirty_paths_json TEXT NOT NULL CHECK(json_valid(dirty_paths_json) AND json_type(dirty_paths_json)='array'), observed_at TEXT NOT NULL, diagnostic_code TEXT, diagnostic TEXT, created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='crewfold-check-worker'),
 UNIQUE(check_result_id,revision), CHECK(status<>'stale' OR ever_stale=1), CHECK(revision<>1 OR initially_eligible=(status='fresh'))
) STRICT;

CREATE TABLE check_requirement_evidence (
 id TEXT PRIMARY KEY CHECK(length(id)=46 AND substr(id,1,14)='checkevidence_' AND substr(id,15) NOT GLOB '*[^0-9a-f]*'), requirement_id TEXT NOT NULL REFERENCES task_check_requirements(id), requirement_revision INTEGER NOT NULL CHECK(requirement_revision>0), check_result_id TEXT NOT NULL REFERENCES check_results(id), freshness_revision INTEGER NOT NULL CHECK(freshness_revision>0), class TEXT NOT NULL CHECK(class='mechanical_check'), effect TEXT NOT NULL CHECK(effect IN ('supports','contradicts','inconclusive')), created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='crewfold-check-worker'), UNIQUE(check_result_id,freshness_revision)
) STRICT;

CREATE TABLE check_notification_receipts (
 id TEXT PRIMARY KEY CHECK(length(id)=44 AND substr(id,1,12)='checknotice_' AND substr(id,13) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 check_result_id TEXT NOT NULL REFERENCES check_results(id), freshness_revision INTEGER NOT NULL CHECK(freshness_revision>0),
 route_id TEXT REFERENCES check_routes(id), duty TEXT NOT NULL CHECK(duty IN ('task_owner','evidence_review','coordination')),
 recipient_agent_id TEXT NOT NULL REFERENCES agents(id), recipient_agent_revision INTEGER NOT NULL CHECK(recipient_agent_revision>0),
 assignment_id TEXT REFERENCES task_assignments(id), assignment_revision INTEGER CHECK(assignment_revision IS NULL OR assignment_revision>0),
 message_id TEXT NOT NULL UNIQUE REFERENCES messages(id) DEFERRABLE INITIALLY DEFERRED,
 created_at TEXT NOT NULL,
 CHECK((assignment_id IS NULL)=(assignment_revision IS NULL)),
 CHECK((duty='task_owner' AND route_id IS NULL AND assignment_id IS NOT NULL) OR (duty IN ('evidence_review','coordination') AND route_id IS NOT NULL AND assignment_id IS NULL))
) STRICT;

CREATE TABLE check_route_failures (
 id TEXT PRIMARY KEY CHECK(length(id)=47 AND substr(id,1,15)='checkroutefail_' AND substr(id,16) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 check_result_id TEXT NOT NULL REFERENCES check_results(id), freshness_revision INTEGER NOT NULL CHECK(freshness_revision>0),
 route_id TEXT REFERENCES check_routes(id), duty TEXT NOT NULL CHECK(duty IN ('task_owner','evidence_review','coordination')),
 recipient_agent_id TEXT REFERENCES agents(id), recipient_agent_revision INTEGER CHECK(recipient_agent_revision IS NULL OR recipient_agent_revision>0),
 assignment_id TEXT REFERENCES task_assignments(id), assignment_revision INTEGER CHECK(assignment_revision IS NULL OR assignment_revision>0),
 code TEXT NOT NULL CHECK(code IN ('unroutable','recipient_unavailable')),
 diagnostic TEXT NOT NULL CHECK(length(CAST(diagnostic AS BLOB)) BETWEEN 1 AND 4096 AND instr(diagnostic,char(0))=0), created_at TEXT NOT NULL,
 CHECK((recipient_agent_id IS NULL)=(recipient_agent_revision IS NULL)), CHECK((assignment_id IS NULL)=(assignment_revision IS NULL))
) STRICT;

CREATE TABLE check_repair_proposals (
 id TEXT PRIMARY KEY CHECK(length(id)=44 AND substr(id,1,12)='checkrepair_' AND substr(id,13) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
 objective_id TEXT NOT NULL REFERENCES objectives(id), objective_revision INTEGER NOT NULL CHECK(objective_revision>0),
 task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL CHECK(task_revision>0),
 requirement_id TEXT NOT NULL REFERENCES task_check_requirements(id), requirement_revision INTEGER NOT NULL CHECK(requirement_revision>0),
 check_result_id TEXT NOT NULL REFERENCES check_results(id), freshness_revision INTEGER NOT NULL CHECK(freshness_revision>0),
 source_repository_id TEXT NOT NULL REFERENCES repositories(id), source_checkout_id TEXT NOT NULL REFERENCES checkouts(id), source_head_commit TEXT NOT NULL,
 policy_revision INTEGER NOT NULL CHECK(policy_revision>0), repair_launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id), repair_launch_profile_revision INTEGER NOT NULL CHECK(repair_launch_profile_revision>0),
 source_run_id TEXT NOT NULL REFERENCES runs(id), source_agent_id TEXT NOT NULL REFERENCES agents(id), source_agent_revision INTEGER NOT NULL CHECK(source_agent_revision>0),
 source_grant_id TEXT NOT NULL REFERENCES check_watch_grants(id), source_grant_revision INTEGER NOT NULL CHECK(source_grant_revision>0),
 rationale TEXT NOT NULL CHECK(length(CAST(rationale AS BLOB)) BETWEEN 1 AND 4096 AND instr(rationale,char(0))=0),
 repair_task_title TEXT NOT NULL CHECK(length(CAST(repair_task_title AS BLOB)) BETWEEN 1 AND 256 AND instr(repair_task_title,char(0))=0),
 repair_task_description TEXT NOT NULL CHECK(length(CAST(repair_task_description AS BLOB)) BETWEEN 1 AND 4096 AND instr(repair_task_description,char(0))=0),
 repair_task_priority INTEGER NOT NULL CHECK(repair_task_priority BETWEEN 0 AND 1000),
 repair_budget_tokens INTEGER NOT NULL CHECK(repair_budget_tokens BETWEEN 1 AND 200000),
 repair_budget_cost_cents INTEGER NOT NULL CHECK(repair_budget_cost_cents BETWEEN 1 AND 10000),
 repair_budget_time_seconds INTEGER NOT NULL CHECK(repair_budget_time_seconds BETWEEN 1 AND 86400),
 recipe_sha256 TEXT NOT NULL CHECK(length(recipe_sha256)=64 AND recipe_sha256 NOT GLOB '*[^0-9a-f]*'),
 status TEXT NOT NULL CHECK(status IN ('pending','accepted','rejected','stale')), revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, created_by TEXT NOT NULL, updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE check_repair_decisions (
 id TEXT PRIMARY KEY CHECK(length(id)=46 AND substr(id,1,14)='checkdecision_' AND substr(id,15) NOT GLOB '*[^0-9a-f]*'),
 repair_proposal_id TEXT NOT NULL UNIQUE REFERENCES check_repair_proposals(id), decision TEXT NOT NULL CHECK(decision IN ('accepted','rejected')),
 proposal_revision INTEGER NOT NULL CHECK(proposal_revision>0), note TEXT CHECK(note IS NULL OR (length(CAST(note AS BLOB)) BETWEEN 1 AND 4096 AND instr(note,char(0))=0)), created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner')
) STRICT;

CREATE TABLE check_repair_effects (
 id TEXT PRIMARY KEY CHECK(length(id)=44 AND substr(id,1,12)='checkeffect_' AND substr(id,13) NOT GLOB '*[^0-9a-f]*'),
 repair_proposal_id TEXT NOT NULL UNIQUE REFERENCES check_repair_proposals(id), repair_task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id), scheduling_intent_id TEXT NOT NULL UNIQUE REFERENCES scheduling_intents(id),
 created_at TEXT NOT NULL
) STRICT;

CREATE TABLE "scheduling_intents" (
    id TEXT PRIMARY KEY CHECK(length(id)=40 AND substr(id,1,8)='sintent_' AND substr(id,9) NOT GLOB '*[^0-9a-f]*'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id), task_id TEXT NOT NULL REFERENCES tasks(id), agent_id TEXT NOT NULL REFERENCES agents(id),
    launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id),
    source_proposal_id TEXT REFERENCES manager_proposals(id), source_action_id TEXT,
    source_check_repair_proposal_id TEXT REFERENCES check_repair_proposals(id),
    source_owner_turn_id TEXT REFERENCES owner_turns(id), source_owner_operation_id TEXT UNIQUE REFERENCES owner_turn_operations(id),
    status TEXT NOT NULL CHECK(status IN ('pending','deferred','awaiting_approval','run_requested','satisfied','failed','cancelled')),
    reason TEXT, assignment_id TEXT REFERENCES task_assignments(id), run_id TEXT REFERENCES runs(id), supervisor_action_id TEXT,
    attempts INTEGER NOT NULL CHECK(attempts BETWEEN 0 AND 100), last_evaluated_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_evaluated_event_sequence>=0),
    revision INTEGER NOT NULL CHECK(revision>0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, next_attempt_at TEXT,
    created_by TEXT NOT NULL, updated_by TEXT NOT NULL,
    UNIQUE(source_proposal_id,source_action_id), UNIQUE(source_check_repair_proposal_id), UNIQUE(source_owner_turn_id,source_owner_operation_id),
    FOREIGN KEY(source_action_id,source_proposal_id) REFERENCES manager_proposal_actions(id,proposal_id),
    CHECK((source_proposal_id IS NOT NULL AND source_action_id IS NOT NULL AND source_check_repair_proposal_id IS NULL AND source_owner_turn_id IS NULL AND source_owner_operation_id IS NULL)
       OR (source_proposal_id IS NULL AND source_action_id IS NULL AND source_check_repair_proposal_id IS NOT NULL AND source_owner_turn_id IS NULL AND source_owner_operation_id IS NULL)
       OR (source_proposal_id IS NULL AND source_action_id IS NULL AND source_check_repair_proposal_id IS NULL AND source_owner_turn_id IS NOT NULL AND source_owner_operation_id IS NOT NULL))
) STRICT;

CREATE TABLE check_watch_state (
 project_id TEXT PRIMARY KEY REFERENCES projects(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), last_event_sequence INTEGER NOT NULL CHECK(last_event_sequence>=0), last_result_id TEXT NOT NULL DEFAULT '' CHECK(last_result_id='' OR (length(last_result_id)=44 AND substr(last_result_id,1,12)='checkresult_' AND substr(last_result_id,13) NOT GLOB '*[^0-9a-f]*')), revision INTEGER NOT NULL CHECK(revision>0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE check_watch_receipts (
 id TEXT PRIMARY KEY CHECK(length(id)=43 AND substr(id,1,11)='checkwatch_' AND substr(id,12) NOT GLOB '*[^0-9a-f]*'), workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
 from_event_sequence INTEGER NOT NULL CHECK(from_event_sequence>=0), through_event_sequence INTEGER NOT NULL CHECK(through_event_sequence>=from_event_sequence), cutoff_event_sequence INTEGER NOT NULL CHECK(cutoff_event_sequence>=through_event_sequence), caught_up INTEGER NOT NULL CHECK(caught_up IN (0,1) AND caught_up=(through_event_sequence=cutoff_event_sequence)), degraded INTEGER NOT NULL CHECK(degraded IN (0,1)), unknown_event_type TEXT, unknown_event_sequence INTEGER,
 examined_result_ids_json TEXT NOT NULL CHECK(json_valid(examined_result_ids_json) AND json_type(examined_result_ids_json)='array' AND json_array_length(examined_result_ids_json)<=100), freshness_appended INTEGER NOT NULL CHECK(freshness_appended BETWEEN 0 AND 100), notifications_created INTEGER NOT NULL CHECK(notifications_created BETWEEN 0 AND 300), route_failures_created INTEGER NOT NULL CHECK(route_failures_created BETWEEN 0 AND 300), repairs_marked_stale INTEGER NOT NULL CHECK(repairs_marked_stale BETWEEN 0 AND 100), next_cursor TEXT,
 content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json)='object'), content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'), created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by IN ('local-owner','crewfold-check-worker')),
 CHECK((degraded=0 AND unknown_event_type IS NULL AND unknown_event_sequence IS NULL) OR (degraded=1 AND unknown_event_type IS NOT NULL AND unknown_event_sequence IS NOT NULL AND unknown_event_sequence>through_event_sequence AND unknown_event_sequence<=cutoff_event_sequence))
) STRICT;

CREATE VIEW knowledge_item_effective_scopes AS
SELECT ki.id, ki.workspace_id, ki.project_id,
       COALESCE(binding.task_id, ki.task_scope_id) AS task_scope_id
FROM knowledge_items ki
LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id;

CREATE INDEX events_workspace_sequence_idx
    ON events(workspace_id, sequence);

CREATE INDEX events_workspace_entity_sequence_idx
    ON events(workspace_id, entity_type, entity_id, sequence DESC);

CREATE INDEX projects_workspace_idx ON projects(workspace_id, name);

CREATE INDEX repositories_workspace_idx ON repositories(workspace_id, fingerprint);

CREATE INDEX checkouts_project_idx ON checkouts(project_id, path);

CREATE INDEX checkouts_repository_idx ON checkouts(repository_id);

CREATE INDEX agents_workspace_idx ON agents(workspace_id, name);

CREATE INDEX objectives_project_idx ON objectives(project_id, status);

CREATE UNIQUE INDEX task_assignments_one_active_idx
    ON task_assignments(task_id) WHERE status = 'active';

CREATE INDEX tasks_project_status_idx ON tasks(project_id, status, priority DESC, created_at, id);

CREATE INDEX task_dependencies_depends_idx ON task_dependencies(depends_on_task_id, task_id);

CREATE INDEX task_assignments_agent_status_idx ON task_assignments(agent_id, status);

CREATE UNIQUE INDEX domain_agent_one_preferred_entry_idx
    ON domain_agent_memberships(project_id)
    WHERE status = 'active' AND preferred_entry = 1;

CREATE INDEX domain_agent_parent_idx
    ON domain_agent_memberships(project_id, parent_agent_id, status, agent_id);

CREATE INDEX domain_agent_workstream_idx
    ON domain_agent_memberships(project_id, workstream_id, status, agent_id);

CREATE INDEX domain_agent_tool_receipts_session_idx
    ON domain_agent_tool_receipts(project_id, agent_id, created_at, id);

CREATE INDEX domain_agent_staffing_grants_manager_idx
    ON domain_agent_staffing_grants(project_id, manager_agent_id, status, created_at, id);

CREATE INDEX domain_agent_staffing_allocations_grant_idx
    ON domain_agent_staffing_allocations(grant_id, created_at, id);

CREATE TRIGGER domain_agent_tool_receipt_reject_update
BEFORE UPDATE ON domain_agent_tool_receipts
BEGIN SELECT RAISE(ABORT, 'domain agent tool receipts are immutable'); END;

CREATE TRIGGER domain_agent_tool_receipt_reject_delete
BEFORE DELETE ON domain_agent_tool_receipts
BEGIN SELECT RAISE(ABORT, 'domain agent tool receipts are immutable'); END;

CREATE TRIGGER domain_agent_staffing_grant_validate_update
BEFORE UPDATE ON domain_agent_staffing_grants
BEGIN
    SELECT CASE WHEN NEW.id<>OLD.id OR NEW.project_id<>OLD.project_id
      OR NEW.manager_agent_id<>OLD.manager_agent_id
      OR NEW.manager_membership_revision<>OLD.manager_membership_revision
      OR NEW.max_descendants<>OLD.max_descendants OR NEW.max_concurrency<>OLD.max_concurrency
      OR NEW.budget_tokens<>OLD.budget_tokens OR NEW.budget_cost_cents<>OLD.budget_cost_cents
      OR NEW.budget_time_seconds<>OLD.budget_time_seconds OR NEW.expires_at IS NOT OLD.expires_at
      OR NEW.created_at<>OLD.created_at OR NEW.created_by<>OLD.created_by
      OR OLD.status<>'active' OR NEW.status NOT IN ('revoked','expired')
      OR NEW.revision<>OLD.revision+1 OR NEW.updated_by<>'local-owner'
      THEN RAISE(ABORT, 'domain staffing grants are immutable except terminal lifecycle') END;
END;

CREATE TRIGGER domain_agent_staffing_grant_validate_insert
BEFORE INSERT ON domain_agent_staffing_grants
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM domain_agent_memberships manager
        WHERE manager.project_id=NEW.project_id AND manager.agent_id=NEW.manager_agent_id
          AND manager.status='active' AND manager.revision=NEW.manager_membership_revision
    ) OR crewfold_timestamp_canonical(NEW.created_at)<>1
      OR NEW.updated_at<>NEW.created_at OR NEW.status<>'active' OR NEW.revision<>1
      OR (NEW.expires_at IS NOT NULL AND (
          crewfold_timestamp_canonical(NEW.expires_at)<>1
          OR crewfold_timestamp_key(NEW.expires_at)<=crewfold_timestamp_key(NEW.created_at)
      ))
      THEN RAISE(ABORT, 'domain staffing grant scope or lifecycle is invalid') END;
END;

CREATE TRIGGER domain_agent_staffing_grant_reject_delete
BEFORE DELETE ON domain_agent_staffing_grants
BEGIN SELECT RAISE(ABORT, 'domain staffing grants are durable authority'); END;

CREATE TRIGGER domain_agent_staffing_profile_reject_update
BEFORE UPDATE ON domain_agent_staffing_profiles
BEGIN SELECT RAISE(ABORT, 'domain staffing profiles are immutable authority'); END;

CREATE TRIGGER domain_agent_staffing_profile_reject_delete
BEFORE DELETE ON domain_agent_staffing_profiles
BEGIN SELECT RAISE(ABORT, 'domain staffing profiles are immutable authority'); END;

CREATE TRIGGER domain_agent_staffing_task_class_reject_update
BEFORE UPDATE ON domain_agent_staffing_task_classes
BEGIN SELECT RAISE(ABORT, 'domain staffing task classes are immutable authority'); END;

CREATE TRIGGER domain_agent_staffing_task_class_reject_delete
BEFORE DELETE ON domain_agent_staffing_task_classes
BEGIN SELECT RAISE(ABORT, 'domain staffing task classes are immutable authority'); END;

CREATE TRIGGER domain_agent_staffing_allocation_reject_update
BEFORE UPDATE ON domain_agent_staffing_allocations
BEGIN SELECT RAISE(ABORT, 'domain staffing allocations are immutable receipts'); END;

CREATE TRIGGER domain_agent_staffing_allocation_reject_delete
BEFORE DELETE ON domain_agent_staffing_allocations
BEGIN SELECT RAISE(ABORT, 'domain staffing allocations are immutable receipts'); END;

CREATE TRIGGER domain_agent_staffing_allocation_validate_insert
BEFORE INSERT ON domain_agent_staffing_allocations
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM domain_agent_staffing_grants grant
        JOIN domain_agent_memberships parent
          ON parent.project_id=grant.project_id AND parent.agent_id=grant.manager_agent_id
        JOIN domain_agent_memberships child
          ON child.project_id=grant.project_id AND child.agent_id=NEW.child_agent_id
        JOIN agents child_agent ON child_agent.id=child.agent_id
        JOIN events event ON event.sequence=NEW.event_sequence
        WHERE grant.id=NEW.grant_id AND grant.project_id=NEW.project_id
          AND grant.manager_agent_id=NEW.parent_agent_id AND grant.status='active'
          AND grant.manager_membership_revision=parent.revision AND parent.status='active'
          AND child.parent_agent_id=parent.agent_id AND child.status='active'
          AND child_agent.provider=NEW.provider AND child_agent.runtime=NEW.runtime
          AND event.type='domain.child_created' AND event.entity_type='domain_staffing_allocation'
          AND event.entity_id=NEW.id AND event.entity_revision=1
          AND event.actor_type='integration' AND event.actor_id=NEW.parent_agent_id
          AND event.occurred_at=NEW.created_at
    ) OR crewfold_timestamp_canonical(NEW.created_at)<>1
      THEN RAISE(ABORT, 'domain staffing allocation scope or receipt is invalid') END;
END;

CREATE TRIGGER domain_agent_membership_validate_insert
BEFORE INSERT ON domain_agent_memberships
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM projects project JOIN agents agent
          ON agent.id = NEW.agent_id AND agent.workspace_id = project.workspace_id
        WHERE project.id = NEW.project_id
    ) THEN RAISE(ABORT, 'domain agent must share the domain workspace') END;
    SELECT CASE WHEN NEW.workstream_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM objectives objective
        WHERE objective.id = NEW.workstream_id AND objective.project_id = NEW.project_id
    ) THEN RAISE(ABORT, 'domain agent workstream must belong to the domain') END;
    SELECT CASE WHEN NEW.parent_agent_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM domain_agent_memberships parent
        WHERE parent.project_id = NEW.project_id AND parent.agent_id = NEW.parent_agent_id
          AND parent.status = 'active'
    ) THEN RAISE(ABORT, 'domain agent parent must be active in the domain') END;
END;

CREATE TRIGGER domain_agent_membership_validate_update
BEFORE UPDATE ON domain_agent_memberships
BEGIN
    SELECT CASE WHEN NEW.project_id <> OLD.project_id OR NEW.agent_id <> OLD.agent_id
      OR NEW.created_at <> OLD.created_at OR NEW.created_by <> OLD.created_by
      THEN RAISE(ABORT, 'domain agent identity is immutable') END;
    SELECT CASE WHEN NEW.workstream_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM objectives objective
        WHERE objective.id = NEW.workstream_id AND objective.project_id = NEW.project_id
    ) THEN RAISE(ABORT, 'domain agent workstream must belong to the domain') END;
    SELECT CASE WHEN NEW.parent_agent_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM domain_agent_memberships parent
        WHERE parent.project_id = NEW.project_id AND parent.agent_id = NEW.parent_agent_id
          AND parent.status = 'active'
    ) THEN RAISE(ABORT, 'domain agent parent must be active in the domain') END;
    SELECT CASE WHEN NEW.parent_agent_id IS NOT NULL AND EXISTS (
        WITH RECURSIVE descendants(agent_id) AS (
            SELECT child.agent_id FROM domain_agent_memberships child
            WHERE child.project_id = NEW.project_id AND child.parent_agent_id = NEW.agent_id
              AND child.status = 'active'
            UNION ALL
            SELECT child.agent_id FROM domain_agent_memberships child
            JOIN descendants prior ON child.parent_agent_id = prior.agent_id
            WHERE child.project_id = NEW.project_id AND child.status = 'active'
        )
        SELECT 1 FROM descendants WHERE agent_id = NEW.parent_agent_id
    ) THEN RAISE(ABORT, 'domain agent ancestry cycle') END;
    SELECT CASE WHEN NEW.status = 'retired' AND EXISTS (
        SELECT 1 FROM domain_agent_memberships child
        WHERE child.project_id = NEW.project_id AND child.parent_agent_id = NEW.agent_id
          AND child.status = 'active'
    ) THEN RAISE(ABORT, 'domain agent with active children cannot retire') END;
END;

CREATE TRIGGER domain_agent_membership_reject_delete
BEFORE DELETE ON domain_agent_memberships
BEGIN
    SELECT RAISE(ABORT, 'domain agent memberships are durable');
END;

CREATE UNIQUE INDEX runs_one_live_task_idx
    ON runs(task_id) WHERE status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'lost');

CREATE INDEX runs_workspace_status_idx ON runs(workspace_id, status, created_at, id);

CREATE INDEX runs_agent_status_idx ON runs(agent_id, status);

CREATE INDEX runs_checkout_status_idx ON runs(checkout_id, status);

CREATE INDEX run_jobs_ready_idx ON run_jobs(status, available_at, run_id);

CREATE INDEX run_timeline_run_idx ON run_timeline(run_id, sequence);

CREATE INDEX context_packets_task_idx ON context_packets(task_id, created_at, id);

CREATE INDEX run_reports_pending_idx ON run_reports(run_id, status, sequence);

CREATE INDEX run_artifacts_run_idx ON run_artifacts(run_id, created_at, id);

CREATE INDEX run_tool_calls_run_idx ON run_tool_calls(run_id, recorded_at, id);

CREATE INDEX message_threads_workspace_idx ON message_threads(workspace_id, updated_at, id);

CREATE UNIQUE INDEX work_claims_active_scope_idx
    ON work_claims(task_id, kind, target, COALESCE(checkout_id, '')) WHERE status = 'active';

CREATE INDEX work_claims_project_status_idx
    ON work_claims(project_id, status, kind, lease_expires_at, id);

CREATE INDEX work_claims_checkout_status_idx
    ON work_claims(checkout_id, status, task_id, id);

CREATE INDEX work_overlaps_workspace_status_idx
    ON work_overlaps(workspace_id, status, severity, detected_at, id);

CREATE INDEX work_overlaps_project_status_idx
    ON work_overlaps(project_id, status, detected_at, id);

CREATE INDEX task_coordination_holds_task_idx ON task_coordination_holds(task_id, overlap_id);

CREATE INDEX claim_drifts_workspace_status_idx
    ON claim_drifts(workspace_id, status, last_observed_at, id);

CREATE INDEX claim_drifts_claim_status_idx
    ON claim_drifts(claim_id, status, path);

CREATE INDEX meetings_workspace_status_idx ON meetings(workspace_id, status, created_at, id);

CREATE INDEX meetings_overlap_idx ON meetings(overlap_id, created_at, id);

CREATE INDEX task_roles_task_idx ON task_roles(task_id, role, agent_id);

CREATE INDEX knowledge_items_scope_idx
    ON knowledge_items(workspace_id, project_id, task_scope_id, type, created_at, id);

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

CREATE INDEX knowledge_sources_entity_idx
    ON knowledge_sources(source_type, source_id, revision_id);

CREATE INDEX knowledge_authority_revision_idx
    ON knowledge_authority_checks(revision_id, created_at, id);

CREATE INDEX thread_participants_agent_scope_idx
    ON thread_participants(workspace_id, agent_id, project_id, task_id, thread_id);

CREATE INDEX curator_rules_effective_idx
    ON curator_rules(workspace_id, name, revision DESC);

CREATE INDEX curator_derivations_project_idx
    ON curator_derivations(workspace_id, project_id, created_at, id);

CREATE INDEX curator_auto_acceptances_project_idx
    ON curator_auto_acceptances(workspace_id, project_id, created_at, id);

CREATE INDEX knowledge_contradictions_project_idx
    ON knowledge_contradictions(workspace_id, project_id, reported_at, id);

CREATE INDEX knowledge_contradictions_left_open_idx
    ON knowledge_contradictions(left_revision_id, id) WHERE status = 'open';

CREATE INDEX knowledge_contradictions_right_open_idx
    ON knowledge_contradictions(right_revision_id, id) WHERE status = 'open';

CREATE INDEX knowledge_contradiction_authority_idx
    ON knowledge_contradiction_authority_checks(contradiction_id, created_at, id);

CREATE INDEX knowledge_task_scope_anchors_project_idx
    ON knowledge_task_scope_anchors(workspace_id, project_id, task_id);

CREATE UNIQUE INDEX knowledge_import_entities_entity_idx
    ON knowledge_import_entities(entity_type, entity_id);

CREATE INDEX context_deltas_run_sequence_idx
    ON context_deltas(run_id, sequence);

CREATE INDEX context_delta_ack_run_sequence_idx
    ON context_delta_acknowledgements(run_id, sequence);

CREATE INDEX manager_grants_scope_idx
    ON manager_grants(workspace_id, project_id, objective_id, task_id, agent_id, status, id);

CREATE INDEX launch_profiles_scope_idx
    ON launch_profiles(workspace_id, project_id, agent_id, status, id);

CREATE INDEX launch_profiles_grant_idx
    ON launch_profiles(manager_grant_id, status, id);

CREATE INDEX manager_proposals_queue_idx
    ON manager_proposals(workspace_id, status, created_at, id);

CREATE INDEX manager_proposals_scope_idx
    ON manager_proposals(project_id, objective_id, kind, status, id);

CREATE INDEX manager_proposals_run_idx ON manager_proposals(source_run_id, id);

CREATE INDEX supervisor_actions_queue_idx
    ON supervisor_actions(workspace_id, status, created_at, id);

CREATE INDEX supervisor_actions_entity_idx
    ON supervisor_actions(workspace_id, task_id, run_id, condition, entity_revision, id);

CREATE INDEX approval_requests_queue_idx
    ON approval_requests(workspace_id, status, created_at, id);

CREATE INDEX messages_thread_idx ON messages(thread_id,created_at,id);

CREATE INDEX message_recipients_inbox_idx ON message_recipients(recipient_agent_id,status,queued_at,message_id);

CREATE INDEX message_wake_jobs_queue_idx ON message_wake_jobs(status,available_at,sequence);

CREATE INDEX check_definitions_scope_idx ON check_definitions(workspace_id,project_id,status,name,id);

CREATE UNIQUE INDEX check_definitions_active_name_idx ON check_definitions(project_id,name) WHERE status='active';

CREATE UNIQUE INDEX task_check_requirements_active_key_idx ON task_check_requirements(task_id,criterion_key) WHERE status='active';

CREATE UNIQUE INDEX task_check_requirements_active_definition_idx ON task_check_requirements(task_id,definition_id) WHERE status='active';

CREATE INDEX task_check_requirements_scope_idx ON task_check_requirements(workspace_id,project_id,task_id,status,id);

CREATE INDEX check_watch_grants_scope_idx ON check_watch_grants(workspace_id,project_id,agent_id,status,id);

CREATE INDEX check_routes_scope_idx ON check_routes(workspace_id,project_id,status,trigger,duty,id);

CREATE UNIQUE INDEX check_routes_active_exact_idx ON check_routes(project_id,COALESCE(definition_id,''),trigger,duty,agent_id) WHERE status='active';

CREATE UNIQUE INDEX check_runs_one_live_idx ON check_runs(requirement_id,checkout_id) WHERE status IN ('requested','starting','running');

CREATE INDEX check_runs_scope_idx ON check_runs(workspace_id,project_id,task_id,status,created_at,id);

CREATE INDEX check_jobs_ready_idx ON check_jobs(status,available_at,check_run_id);

CREATE UNIQUE INDEX check_notification_exact_idx ON check_notification_receipts(check_result_id,freshness_revision,COALESCE(route_id,''),duty,recipient_agent_id);

CREATE INDEX check_notification_result_idx ON check_notification_receipts(check_result_id,freshness_revision,id);

CREATE UNIQUE INDEX check_route_failure_exact_idx ON check_route_failures(check_result_id,freshness_revision,COALESCE(route_id,''),duty,COALESCE(recipient_agent_id,''),code);

CREATE INDEX check_route_failure_result_idx ON check_route_failures(check_result_id,freshness_revision,id);

CREATE UNIQUE INDEX check_repair_result_policy_idx ON check_repair_proposals(check_result_id,policy_revision);

CREATE INDEX check_repair_scope_idx ON check_repair_proposals(workspace_id,project_id,task_id,status,created_at,id);

CREATE UNIQUE INDEX scheduling_intents_one_open_task_idx ON scheduling_intents(task_id) WHERE status IN ('pending','deferred','awaiting_approval','run_requested');

CREATE INDEX scheduling_intents_queue_idx ON scheduling_intents(workspace_id,status,next_attempt_at,task_id,id);

CREATE INDEX check_watch_receipts_scope_idx ON check_watch_receipts(workspace_id,project_id,created_at,id);

CREATE TRIGGER events_reject_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are immutable');
END;

CREATE TRIGGER events_reject_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are immutable');
END;

CREATE TRIGGER schema_baseline_reject_update
BEFORE UPDATE ON schema_baseline
BEGIN
    SELECT RAISE(ABORT, 'baseline identity is immutable');
END;

CREATE TRIGGER schema_baseline_reject_delete
BEFORE DELETE ON schema_baseline
BEGIN
    SELECT RAISE(ABORT, 'baseline identity is immutable');
END;

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

CREATE TRIGGER knowledge_contradiction_reject_immutable_update
BEFORE UPDATE OF id, workspace_id, project_id, left_revision_id, right_revision_id,
    report_note, reported_at, reported_by, reported_by_type, detected_event_sequence
ON knowledge_contradictions
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradiction identity and report are immutable');
END;

CREATE TRIGGER knowledge_contradiction_reject_delete
BEFORE DELETE ON knowledge_contradictions
BEGIN
    SELECT RAISE(ABORT, 'knowledge contradictions are immutable history');
END;

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

CREATE TRIGGER manager_grant_proposal_kind_validate_insert
BEFORE INSERT ON manager_grant_proposal_kinds
BEGIN
    SELECT CASE WHEN EXISTS (SELECT 1 FROM manager_grants WHERE id = NEW.grant_id)
      OR NEW.ordinal IS NOT (
      SELECT COUNT(*) FROM manager_grant_proposal_kinds WHERE grant_id = NEW.grant_id
    ) OR EXISTS (
      SELECT 1 FROM manager_grant_proposal_kinds prior WHERE prior.grant_id = NEW.grant_id
        AND CASE prior.kind WHEN 'assignment' THEN 0 WHEN 'escalation' THEN 1 WHEN 'review' THEN 2 ELSE 3 END
          >= CASE NEW.kind WHEN 'assignment' THEN 0 WHEN 'escalation' THEN 1 WHEN 'review' THEN 2 ELSE 3 END
    ) THEN RAISE(ABORT, 'manager grant proposal kinds are not canonical') END;
END;

CREATE TRIGGER manager_grant_claim_kind_validate_insert
BEFORE INSERT ON manager_grant_claim_kinds
BEGIN
    SELECT CASE WHEN EXISTS (SELECT 1 FROM manager_grants WHERE id = NEW.grant_id)
      OR NEW.ordinal IS NOT (
      SELECT COUNT(*) FROM manager_grant_claim_kinds WHERE grant_id = NEW.grant_id
    ) OR EXISTS (
      SELECT 1 FROM manager_grant_claim_kinds prior WHERE prior.grant_id = NEW.grant_id
        AND CASE prior.kind WHEN 'component' THEN 0 WHEN 'operation' THEN 1 ELSE 2 END
          >= CASE NEW.kind WHEN 'component' THEN 0 WHEN 'operation' THEN 1 ELSE 2 END
    ) THEN RAISE(ABORT, 'manager grant claim kinds are not canonical') END;
END;

CREATE TRIGGER manager_grant_proposal_kind_reject_update
BEFORE UPDATE ON manager_grant_proposal_kinds BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_proposal_kind_reject_delete
BEFORE DELETE ON manager_grant_proposal_kinds BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_launch_profile_validate_insert
BEFORE INSERT ON manager_grant_launch_profiles
BEGIN
    SELECT CASE WHEN EXISTS (SELECT 1 FROM manager_grants WHERE id = NEW.grant_id)
      OR NEW.ordinal IS NOT (
      SELECT COUNT(*) FROM manager_grant_launch_profiles WHERE grant_id = NEW.grant_id
    ) OR EXISTS (
      SELECT 1 FROM manager_grant_launch_profiles prior WHERE prior.grant_id = NEW.grant_id
        AND prior.launch_profile_id >= NEW.launch_profile_id
    ) OR NOT EXISTS (
      SELECT 1 FROM launch_profiles profile JOIN agents agent ON agent.id = profile.agent_id
      WHERE profile.id = NEW.launch_profile_id AND profile.revision = NEW.launch_profile_revision
        AND profile.agent_id = NEW.agent_id AND profile.agent_revision = NEW.agent_revision
        AND profile.status = 'active' AND profile.manager_grant_id IS NULL
        AND agent.enabled = 1 AND agent.revision = NEW.agent_revision
    ) THEN RAISE(ABORT, 'manager grant target profile is not exact') END;
END;

CREATE TRIGGER manager_grant_launch_profile_reject_update
BEFORE UPDATE ON manager_grant_launch_profiles BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_launch_profile_reject_delete
BEFORE DELETE ON manager_grant_launch_profiles BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_claim_kind_reject_update
BEFORE UPDATE ON manager_grant_claim_kinds BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_claim_kind_reject_delete
BEFORE DELETE ON manager_grant_claim_kinds BEGIN SELECT RAISE(ABORT, 'manager grant authority is immutable'); END;

CREATE TRIGGER manager_grant_validate_insert
BEFORE INSERT ON manager_grants
BEGIN
    SELECT CASE WHEN NEW.status <> 'active' OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
        OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
        OR NEW.created_at IS NOT NEW.updated_at OR NEW.revision IS NOT 1
        OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) <> NEW.content_sha256
        OR json_extract(NEW.content_json, '$.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.content_json, '$.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.content_json, '$.objective_id') IS NOT NEW.objective_id
        OR json_extract(NEW.content_json, '$.objective_revision') IS NOT NEW.objective_revision
        OR json_extract(NEW.content_json, '$.task_id') IS NOT NEW.task_id
        OR json_extract(NEW.content_json, '$.task_revision') IS NOT NEW.task_revision
        OR json_extract(NEW.content_json, '$.agent_id') IS NOT NEW.agent_id
        OR json_extract(NEW.content_json, '$.agent_revision') IS NOT NEW.agent_revision
        OR json(json_extract(NEW.content_json, '$.proposal_kinds')) <> json(NEW.proposal_kinds_json)
        OR json(json_extract(NEW.content_json, '$.launch_profiles')) <> json(NEW.launch_profiles_json)
        OR json(json_extract(NEW.content_json, '$.allowed_claim_kinds')) <> json(NEW.allowed_claim_kinds_json)
        OR json_extract(NEW.content_json, '$.limits.max_open_proposals') IS NOT NEW.max_open_proposals
        OR json_extract(NEW.content_json, '$.limits.max_actions') IS NOT NEW.max_actions
        OR json_extract(NEW.content_json, '$.limits.max_tasks') IS NOT NEW.max_tasks
        OR json_extract(NEW.content_json, '$.limits.max_dependencies') IS NOT NEW.max_dependencies
        OR json_extract(NEW.content_json, '$.limits.max_claim_requirements') IS NOT NEW.max_claim_requirements
        OR json_extract(NEW.content_json, '$.limits.budget.token_limit') IS NOT NEW.budget_tokens
        OR json_extract(NEW.content_json, '$.limits.budget.cost_cents') IS NOT NEW.budget_cost_cents
        OR json_extract(NEW.content_json, '$.limits.budget.time_seconds') IS NOT NEW.budget_time_seconds
        OR COALESCE(json_extract(NEW.content_json, '$.expires_at'),'') <> COALESCE(NEW.expires_at,'')
        OR (NEW.expires_at IS NOT NULL AND (
            crewfold_timestamp_canonical(NEW.expires_at) IS NOT 1
            OR crewfold_timestamp_key(NEW.expires_at) <= crewfold_timestamp_key(NEW.created_at)
        )) THEN RAISE(ABORT, 'invalid manager grant lifecycle') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM tasks task
        JOIN objectives objective ON objective.id = task.objective_id
        JOIN agents agent ON agent.id = NEW.agent_id
        JOIN task_assignments assignment ON assignment.id = (
            SELECT id FROM task_assignments WHERE task_id = task.id AND status = 'active'
        )
        WHERE task.id = NEW.task_id AND task.workspace_id = NEW.workspace_id
          AND task.project_id = NEW.project_id AND task.objective_id = NEW.objective_id
          AND objective.workspace_id = NEW.workspace_id AND objective.project_id = NEW.project_id
          AND objective.status = 'active' AND objective.revision = NEW.objective_revision AND task.revision = NEW.task_revision
          AND assignment.agent_id = NEW.agent_id AND agent.workspace_id = NEW.workspace_id
          AND crewfold_timestamp_key(assignment.lease_expires_at) > crewfold_timestamp_key(NEW.created_at)
          AND agent.revision = NEW.agent_revision AND agent.enabled = 1
    ) THEN RAISE(ABORT, 'manager grant scope is not current') END;
    SELECT CASE WHEN NEW.proposal_kinds_json IS NOT (
        SELECT json_group_array(kind) FROM (
          SELECT kind FROM manager_grant_proposal_kinds WHERE grant_id = NEW.id ORDER BY ordinal
        )
      ) OR NEW.launch_profiles_json IS NOT (
        SELECT json_group_array(json_object(
          'launch_profile_id',launch_profile_id,'revision',launch_profile_revision,
          'agent_id',agent_id,'agent_revision',agent_revision
        )) FROM (
          SELECT * FROM manager_grant_launch_profiles WHERE grant_id = NEW.id ORDER BY ordinal
        )
      ) OR NEW.allowed_claim_kinds_json IS NOT (
        SELECT json_group_array(kind) FROM (
          SELECT kind FROM manager_grant_claim_kinds WHERE grant_id = NEW.id ORDER BY ordinal
        )
      ) THEN RAISE(ABORT, 'manager grant authority mirror is incomplete') END;
    SELECT CASE WHEN EXISTS (
      SELECT 1 FROM manager_grant_launch_profiles allowed
      JOIN launch_profiles profile ON profile.id = allowed.launch_profile_id
      WHERE allowed.grant_id = NEW.id AND (
        profile.workspace_id <> NEW.workspace_id OR profile.project_id <> NEW.project_id
      )
    ) THEN RAISE(ABORT, 'manager target launch profile scope differs') END;
END;

CREATE TRIGGER manager_grant_validate_update
BEFORE UPDATE ON manager_grants
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
        OR NEW.project_id IS NOT OLD.project_id OR NEW.objective_id IS NOT OLD.objective_id
        OR NEW.objective_revision IS NOT OLD.objective_revision
        OR NEW.task_id IS NOT OLD.task_id OR NEW.task_revision IS NOT OLD.task_revision
        OR NEW.agent_id IS NOT OLD.agent_id OR NEW.agent_revision IS NOT OLD.agent_revision
        OR NEW.proposal_kinds_json IS NOT OLD.proposal_kinds_json
        OR NEW.launch_profiles_json IS NOT OLD.launch_profiles_json
        OR NEW.allowed_claim_kinds_json IS NOT OLD.allowed_claim_kinds_json
        OR NEW.max_open_proposals IS NOT OLD.max_open_proposals
        OR NEW.max_actions IS NOT OLD.max_actions OR NEW.max_tasks IS NOT OLD.max_tasks
        OR NEW.max_dependencies IS NOT OLD.max_dependencies
        OR NEW.max_claim_requirements IS NOT OLD.max_claim_requirements
        OR NEW.budget_tokens IS NOT OLD.budget_tokens OR NEW.budget_cost_cents IS NOT OLD.budget_cost_cents
        OR NEW.budget_time_seconds IS NOT OLD.budget_time_seconds OR NEW.expires_at IS NOT OLD.expires_at
        OR NEW.content_json IS NOT OLD.content_json OR NEW.content_sha256 IS NOT OLD.content_sha256
        OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
        OR OLD.status <> 'active' OR NEW.status NOT IN ('revoked','expired')
        OR NEW.revision <> OLD.revision + 1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
        OR crewfold_timestamp_key(NEW.updated_at) <= crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by <> 'local-owner'
        THEN RAISE(ABORT, 'manager grants are immutable except terminal lifecycle') END;
END;

CREATE TRIGGER launch_profile_validate_insert
BEFORE INSERT ON launch_profiles
BEGIN
    SELECT CASE WHEN NEW.revision <> 1 OR NEW.status <> 'active'
        OR NEW.created_at IS NOT NEW.updated_at
        OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
        OR lower(hex(sha256(CAST(NEW.scenario_json AS BLOB)))) IS NOT NEW.scenario_sha256
        OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) <> NEW.content_sha256
        OR json_extract(NEW.content_json, '$.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.content_json, '$.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.content_json, '$.agent_id') IS NOT NEW.agent_id
        OR json_extract(NEW.content_json, '$.agent_revision') IS NOT NEW.agent_revision
        OR COALESCE(json_extract(NEW.content_json, '$.purpose'),'') <> COALESCE(NEW.purpose,'')
        OR json_extract(NEW.content_json, '$.runtime') IS NOT NEW.runtime
        OR json_extract(NEW.content_json, '$.provider') IS NOT NEW.provider
        OR COALESCE(json_extract(NEW.content_json, '$.checkout_id'),'') <> COALESCE(NEW.checkout_id,'')
        OR json_extract(NEW.content_json, '$.scenario_sha256') IS NOT NEW.scenario_sha256
        OR json_extract(NEW.content_json, '$.assignment_lease_seconds') IS NOT NEW.assignment_lease_seconds
        OR json_extract(NEW.content_json, '$.capability_ttl_seconds') IS NOT NEW.capability_ttl_seconds
        OR COALESCE(json_extract(NEW.content_json, '$.manager_grant_id'),'') <> COALESCE(NEW.manager_grant_id,'')
        THEN RAISE(ABORT, 'invalid launch profile content') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM projects project JOIN agents agent ON agent.id = NEW.agent_id
        WHERE project.id = NEW.project_id AND project.workspace_id = NEW.workspace_id
          AND agent.workspace_id = NEW.workspace_id AND agent.enabled = 1
          AND agent.revision = NEW.agent_revision AND agent.runtime = NEW.runtime
          AND agent.provider = NEW.provider
    ) THEN RAISE(ABORT, 'launch profile agent or project is not current') END;
    SELECT CASE WHEN NEW.checkout_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM checkouts checkout
        WHERE checkout.id = NEW.checkout_id AND checkout.project_id = NEW.project_id
          AND checkout.availability = 'available'
    ) THEN RAISE(ABORT, 'launch profile checkout is unavailable') END;
    SELECT CASE WHEN NEW.manager_grant_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM manager_grants grant_row
        JOIN tasks task ON task.id = grant_row.task_id
        JOIN task_assignments assignment ON assignment.id = (
          SELECT id FROM task_assignments current_assignment
          WHERE current_assignment.task_id = task.id AND current_assignment.status = 'active'
        )
        WHERE grant_row.id = NEW.manager_grant_id AND grant_row.status = 'active'
          AND grant_row.workspace_id = NEW.workspace_id AND grant_row.project_id = NEW.project_id
          AND grant_row.agent_id = NEW.agent_id AND grant_row.agent_revision = NEW.agent_revision
          AND (grant_row.expires_at IS NULL OR crewfold_timestamp_key(grant_row.expires_at) > crewfold_timestamp_key(NEW.created_at))
          AND task.workspace_id = NEW.workspace_id AND task.project_id = grant_row.project_id
          AND task.objective_id = grant_row.objective_id AND task.revision = grant_row.task_revision
          AND task.status = 'assigned' AND assignment.agent_id = grant_row.agent_id
          AND crewfold_timestamp_key(assignment.lease_expires_at) > crewfold_timestamp_key(NEW.created_at)
    ) THEN RAISE(ABORT, 'management launch profile differs from grant') END;
END;

CREATE TRIGGER launch_profile_validate_update
BEFORE UPDATE ON launch_profiles
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
        OR NEW.project_id IS NOT OLD.project_id OR NEW.agent_id IS NOT OLD.agent_id
        OR NEW.agent_revision IS NOT OLD.agent_revision OR NEW.purpose IS NOT OLD.purpose
        OR NEW.runtime IS NOT OLD.runtime OR NEW.provider IS NOT OLD.provider
        OR NEW.checkout_id IS NOT OLD.checkout_id OR NEW.scenario_json IS NOT OLD.scenario_json
        OR NEW.scenario_sha256 IS NOT OLD.scenario_sha256 OR NEW.content_json IS NOT OLD.content_json
        OR NEW.content_sha256 IS NOT OLD.content_sha256
        OR NEW.assignment_lease_seconds IS NOT OLD.assignment_lease_seconds
        OR NEW.capability_ttl_seconds IS NOT OLD.capability_ttl_seconds
        OR NEW.manager_grant_id IS NOT OLD.manager_grant_id
        OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
        OR OLD.status <> 'active' OR NEW.status <> 'retired'
        OR NEW.revision <> OLD.revision + 1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
        OR crewfold_timestamp_key(NEW.updated_at) <= crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by <> 'local-owner'
        THEN RAISE(ABORT, 'launch profiles are immutable except retirement') END;
END;

CREATE TRIGGER manager_proposal_validate_insert
BEFORE INSERT ON manager_proposals
BEGIN
    SELECT CASE WHEN NEW.revision <> 1 OR NEW.created_at IS NOT NEW.updated_at
        OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
        OR NEW.created_by <> 'agent:' || NEW.source_agent_id OR NEW.updated_by <> NEW.created_by
        OR EXISTS (
          SELECT 1 FROM json_each(NEW.validation_issues_json) issue
          WHERE json_type(issue.value) <> 'object'
            OR json_type(issue.value, '$.code') <> 'text'
            OR length(json_extract(issue.value, '$.code')) NOT BETWEEN 1 AND 64
            OR json_type(issue.value, '$.path') <> 'text'
            OR length(json_extract(issue.value, '$.path')) NOT BETWEEN 1 AND 256
            OR json_type(issue.value, '$.message') <> 'text'
            OR length(json_extract(issue.value, '$.message')) NOT BETWEEN 1 AND 1024
            OR json_extract(issue.value, '$.severity') NOT IN ('warning','error')
        )
        OR (NEW.status = 'invalid' AND NOT EXISTS (
          SELECT 1 FROM json_each(NEW.validation_issues_json) issue
          WHERE json_extract(issue.value, '$.severity') = 'error'
        ))
        OR (NEW.status = 'pending' AND EXISTS (
          SELECT 1 FROM json_each(NEW.validation_issues_json) issue
          WHERE json_extract(issue.value, '$.severity') = 'error'
        ))
        OR NEW.status NOT IN ('pending','invalid') OR NEW.decision_note IS NOT NULL
        OR NEW.decided_at IS NOT NULL OR NEW.decided_by IS NOT NULL
        OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256
        OR json_extract(NEW.content_json, '$.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.content_json, '$.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.content_json, '$.objective_id') IS NOT NEW.objective_id
        OR json_extract(NEW.content_json, '$.objective_revision') IS NOT NEW.objective_revision
        OR json_extract(NEW.content_json, '$.source_run_id') IS NOT NEW.source_run_id
        OR json_extract(NEW.content_json, '$.source_agent_id') IS NOT NEW.source_agent_id
        OR json_extract(NEW.content_json, '$.grant_id') IS NOT NEW.grant_id
        OR json_extract(NEW.content_json, '$.grant_revision') IS NOT NEW.grant_revision
        OR json_extract(NEW.content_json, '$.kind') IS NOT NEW.kind
        OR json_extract(NEW.content_json, '$.summary') IS NOT NEW.summary
        OR json_extract(NEW.content_json, '$.as_of_event_sequence') IS NOT NEW.as_of_event_sequence
        OR json_extract(NEW.content_json, '$.actions') IS NOT json(NEW.actions_json)
        THEN RAISE(ABORT, 'invalid manager proposal insertion') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM runs run JOIN manager_grants grant_row ON grant_row.id = NEW.grant_id
        JOIN run_capabilities capability ON capability.run_id = run.id
        JOIN run_context_bindings binding ON binding.run_id = run.id
        JOIN context_packets packet ON packet.id = binding.context_packet_id
        WHERE run.id = NEW.source_run_id AND run.workspace_id = NEW.workspace_id
          AND run.project_id = NEW.project_id AND run.task_id = grant_row.task_id
          AND run.agent_id = NEW.source_agent_id AND run.agent_id = grant_row.agent_id
          AND grant_row.workspace_id = NEW.workspace_id AND grant_row.project_id = NEW.project_id
          AND grant_row.objective_id = NEW.objective_id AND grant_row.revision = NEW.grant_revision
          AND grant_row.status = 'active' AND (grant_row.expires_at IS NULL OR crewfold_timestamp_key(grant_row.expires_at) > crewfold_timestamp_key(NEW.created_at))
          AND run.status IN ('starting','active','blocked') AND crewfold_timestamp_key(capability.expires_at) > crewfold_timestamp_key(NEW.created_at)
          AND json_extract(packet.packet_json, '$.schema') = 'urn:crewfold:schema:domain:context-packet:v1'
          AND json_extract(packet.packet_json, '$.management_grant.grant_id') = grant_row.id
          AND json_extract(packet.packet_json, '$.management_grant.grant_revision') = grant_row.revision
          AND json_extract(packet.packet_json, '$.as_of_event_sequence') = NEW.as_of_event_sequence
          AND EXISTS (
            SELECT 1 FROM manager_grant_proposal_kinds allowed
            WHERE allowed.grant_id = grant_row.id AND allowed.kind = NEW.kind
          )
    ) THEN RAISE(ABORT, 'manager proposal authority linkage differs') END;
END;

CREATE TRIGGER manager_proposal_validate_update
BEFORE UPDATE ON manager_proposals
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
        OR NEW.project_id IS NOT OLD.project_id OR NEW.objective_id IS NOT OLD.objective_id
        OR NEW.objective_revision IS NOT OLD.objective_revision
        OR NEW.source_run_id IS NOT OLD.source_run_id OR NEW.source_agent_id IS NOT OLD.source_agent_id
        OR NEW.grant_id IS NOT OLD.grant_id OR NEW.grant_revision IS NOT OLD.grant_revision
        OR NEW.kind IS NOT OLD.kind OR NEW.summary IS NOT OLD.summary
        OR NEW.as_of_event_sequence IS NOT OLD.as_of_event_sequence
        OR NEW.actions_json IS NOT OLD.actions_json OR NEW.content_json IS NOT OLD.content_json
        OR NEW.content_sha256 IS NOT OLD.content_sha256
        OR (NEW.status <> 'stale' AND NEW.validation_issues_json IS NOT OLD.validation_issues_json)
        OR (NEW.status = 'stale' AND json_array_length(NEW.validation_issues_json) = 0)
        OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
        OR NOT (
          (OLD.status = 'pending' AND NEW.status IN ('accepted','rejected','stale'))
          OR (OLD.status = 'invalid' AND NEW.status = 'rejected')
        )
        OR NEW.revision <> OLD.revision + 1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
        OR crewfold_timestamp_key(NEW.updated_at) <= crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by <> 'local-owner'
        OR NEW.decided_at IS NOT NEW.updated_at OR NEW.decided_by <> 'local-owner'
        OR NEW.decision_note IS NULL
        THEN RAISE(ABORT, 'manager proposal decision is invalid') END;
    SELECT CASE WHEN NEW.status = 'accepted' AND (
        NOT EXISTS (SELECT 1 FROM manager_proposal_submissions receipt WHERE receipt.proposal_id = OLD.id)
        OR (SELECT COUNT(*) FROM manager_proposal_actions action WHERE action.proposal_id = OLD.id)
          <> json_array_length(OLD.actions_json)
        OR EXISTS (
          SELECT 1 FROM json_each(OLD.actions_json) frozen
          LEFT JOIN manager_proposal_actions action
            ON action.proposal_id = OLD.id AND action.ordinal = CAST(frozen.key AS INTEGER)
          WHERE action.id IS NULL OR action.id <> json_extract(frozen.value, '$.id')
        )
		OR NOT EXISTS (
		  SELECT 1 FROM manager_grants grant_row
		  JOIN objectives objective ON objective.id=NEW.objective_id
		  JOIN agents agent ON agent.id=NEW.source_agent_id
		  JOIN run_context_bindings binding ON binding.run_id=NEW.source_run_id
		  JOIN context_packets packet ON packet.id=binding.context_packet_id
		  WHERE grant_row.id=NEW.grant_id AND grant_row.status='active'
		    AND grant_row.revision=NEW.grant_revision
		    AND grant_row.objective_id=NEW.objective_id
		    AND grant_row.objective_revision=NEW.objective_revision
		    AND objective.project_id=NEW.project_id AND objective.status='active'
		    AND objective.revision=NEW.objective_revision
		    AND agent.id=grant_row.agent_id AND agent.enabled=1
		    AND agent.revision=grant_row.agent_revision
		    AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1'
		    AND json_extract(packet.packet_json,'$.management_grant.grant_id')=grant_row.id
		    AND json_extract(packet.packet_json,'$.management_grant.grant_revision')=grant_row.revision
		    AND json_extract(packet.packet_json,'$.management_grant.objective_revision')=NEW.objective_revision
		    AND (grant_row.expires_at IS NULL OR crewfold_timestamp_key(grant_row.expires_at)>crewfold_timestamp_key(NEW.updated_at))
		)
    ) THEN RAISE(ABORT, 'accepted proposal has incomplete action rows') END;
END;

CREATE TRIGGER manager_grant_reject_delete
BEFORE DELETE ON manager_grants BEGIN
    SELECT RAISE(ABORT, 'manager grants are durable audit authority');
END;

CREATE TRIGGER launch_profile_reject_delete
BEFORE DELETE ON launch_profiles BEGIN
    SELECT RAISE(ABORT, 'launch profiles are durable audit authority');
END;

CREATE TRIGGER manager_proposal_reject_delete
BEFORE DELETE ON manager_proposals BEGIN
    SELECT RAISE(ABORT, 'manager proposals are durable audit authority');
END;

CREATE TRIGGER manager_proposal_action_validate_insert
BEFORE INSERT ON manager_proposal_actions
BEGIN
    SELECT CASE WHEN lower(hex(sha256(CAST(NEW.payload_json AS BLOB)))) <> NEW.content_sha256
        OR NOT EXISTS (
          SELECT 1 FROM manager_proposals proposal
          WHERE proposal.id = NEW.proposal_id AND proposal.status IN ('pending','invalid')
            AND proposal.created_at = NEW.created_at AND proposal.created_by = NEW.created_by
            AND json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].id') = NEW.id
            AND json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].ordinal') = NEW.ordinal
            AND json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].type') = NEW.type
            AND NEW.payload_json = CASE NEW.type
              WHEN 'create_task' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].create_task')
              WHEN 'add_dependency' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].add_dependency')
              WHEN 'declare_claim_requirement' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].declare_claim_requirement')
              WHEN 'assign_task' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].assign_task')
              WHEN 'request_review' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].request_review')
              WHEN 'request_action' THEN json_extract(proposal.actions_json, '$[' || NEW.ordinal || '].request_action')
            END
        ) THEN RAISE(ABORT, 'manager proposal action differs from proposal') END;
END;

CREATE TRIGGER manager_proposal_action_reject_update
BEFORE UPDATE ON manager_proposal_actions BEGIN
    SELECT RAISE(ABORT, 'manager proposal actions are immutable');
END;

CREATE TRIGGER manager_proposal_action_reject_delete
BEFORE DELETE ON manager_proposal_actions BEGIN
    SELECT RAISE(ABORT, 'manager proposal actions are immutable');
END;

CREATE TRIGGER manager_proposal_submission_validate_insert
BEFORE INSERT ON manager_proposal_submissions
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.submitted_at) IS NOT 1 OR NOT EXISTS (
      SELECT 1 FROM manager_proposals proposal JOIN events event ON event.sequence = NEW.event_sequence
      WHERE proposal.id = NEW.proposal_id AND proposal.created_at = NEW.submitted_at
        AND proposal.created_by = NEW.submitted_by AND proposal.content_sha256 = NEW.content_sha256
        AND NEW.action_count = json_array_length(proposal.actions_json)
        AND NEW.action_count = (SELECT COUNT(*) FROM manager_proposal_actions action WHERE action.proposal_id = proposal.id)
        AND event.workspace_id = proposal.workspace_id AND event.entity_type = 'manager_proposal'
        AND event.entity_id = proposal.id AND event.entity_revision = 1
        AND event.type = 'manager.proposal_submitted' AND event.actor_id = proposal.created_by
        AND event.actor_type = 'agent_run'
    ) THEN RAISE(ABORT, 'manager proposal submission is incomplete') END;
END;

CREATE TRIGGER manager_proposal_submission_reject_update
BEFORE UPDATE ON manager_proposal_submissions BEGIN SELECT RAISE(ABORT, 'manager proposal submissions are immutable'); END;

CREATE TRIGGER manager_proposal_submission_reject_delete
BEFORE DELETE ON manager_proposal_submissions BEGIN SELECT RAISE(ABORT, 'manager proposal submissions are immutable'); END;

CREATE TRIGGER manager_proposal_effect_reject_update
BEFORE UPDATE ON manager_proposal_effects BEGIN
    SELECT RAISE(ABORT, 'manager proposal effects are immutable');
END;

CREATE TRIGGER manager_proposal_effect_reject_delete
BEFORE DELETE ON manager_proposal_effects BEGIN
    SELECT RAISE(ABORT, 'manager proposal effects are immutable');
END;

CREATE TRIGGER manager_proposal_decision_validate_insert
BEFORE INSERT ON manager_proposal_decisions
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.decided_at) IS NOT 1 OR NOT EXISTS (
      SELECT 1 FROM manager_proposals proposal JOIN events event ON event.sequence=NEW.event_sequence
      WHERE proposal.id=NEW.proposal_id AND proposal.status=NEW.status
        AND proposal.revision=NEW.proposal_revision AND proposal.decided_at=NEW.decided_at
        AND proposal.decided_by=NEW.decided_by AND NEW.effect_count=(
          SELECT COUNT(*) FROM manager_proposal_effects effect WHERE effect.proposal_id=proposal.id
        ) AND NEW.effects_sha256=lower(hex(sha256(CAST(COALESCE((
          SELECT json_group_array(json_object(
            'action_id',action_id,'effect_type',effect_type,'entity_type',entity_type,'entity_id',entity_id,'event_sequence',event_sequence
          )) FROM (SELECT * FROM manager_proposal_effects WHERE proposal_id=proposal.id ORDER BY action_id,effect_type,entity_type,entity_id)
        ),'[]') AS BLOB))))
        AND event.workspace_id=proposal.workspace_id AND event.entity_type='manager_proposal'
        AND event.entity_id=proposal.id AND event.entity_revision=proposal.revision
        AND event.type=CASE proposal.status WHEN 'accepted' THEN 'manager.proposal_accepted'
          WHEN 'rejected' THEN 'manager.proposal_rejected' ELSE 'manager.proposal_stale' END
        AND event.actor_id='local-owner' AND event.actor_type='human'
        AND event.occurred_at=proposal.decided_at AND event.recorded_at=proposal.decided_at
        AND json_extract(event.data_json,'$.status')=proposal.status
        AND json_extract(event.data_json,'$.decision_note')=proposal.decision_note
        AND json_extract(event.data_json,'$.effect_count')=NEW.effect_count
    ) THEN RAISE(ABORT, 'manager proposal decision receipt is incomplete') END;
END;

CREATE TRIGGER manager_proposal_decision_reject_update
BEFORE UPDATE ON manager_proposal_decisions BEGIN SELECT RAISE(ABORT, 'manager proposal decisions are immutable'); END;

CREATE TRIGGER manager_proposal_decision_reject_delete
BEFORE DELETE ON manager_proposal_decisions BEGIN SELECT RAISE(ABORT, 'manager proposal decisions are immutable'); END;

CREATE TRIGGER task_claim_requirement_validate_insert
BEFORE INSERT ON task_claim_requirements
BEGIN
    SELECT CASE WHEN NEW.revision <> 1 OR NEW.created_at IS NOT NEW.updated_at
      OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS (
        SELECT 1 FROM manager_proposals proposal
        JOIN manager_proposal_actions action ON action.proposal_id = proposal.id
        JOIN tasks task ON task.id = NEW.task_id
        WHERE proposal.id = NEW.source_proposal_id AND proposal.status = 'accepted'
          AND proposal.workspace_id = NEW.workspace_id AND proposal.project_id = NEW.project_id
          AND proposal.objective_id = NEW.objective_id AND action.id = NEW.source_action_id
          AND action.type = 'declare_claim_requirement'
          AND json_extract(action.payload_json, '$.kind') = NEW.kind
          AND json_extract(action.payload_json, '$.target') = NEW.target
          AND json_extract(action.payload_json, '$.mode') = NEW.mode
          AND json_extract(action.payload_json, '$.conflict_policy') = NEW.conflict_policy
          AND (
            json_extract(action.payload_json, '$.task.task_id') = NEW.task_id
            OR EXISTS (
              SELECT 1 FROM manager_proposal_actions creator
              JOIN manager_proposal_effects effect ON effect.action_id = creator.id
                AND effect.proposal_id = creator.proposal_id
              WHERE creator.proposal_id = proposal.id AND creator.type = 'create_task'
                AND json_extract(creator.payload_json, '$.task_key') = json_extract(action.payload_json, '$.task.proposal_task_key')
                AND effect.entity_type = 'task' AND effect.entity_id = NEW.task_id
            )
          )
          AND task.workspace_id = NEW.workspace_id AND task.project_id = NEW.project_id
          AND task.objective_id = NEW.objective_id
      ) THEN RAISE(ABORT, 'claim requirement lacks exact accepted action') END;
END;

CREATE TRIGGER task_claim_requirement_reject_update
BEFORE UPDATE ON task_claim_requirements BEGIN
    SELECT RAISE(ABORT, 'task claim requirements are immutable');
END;

CREATE TRIGGER task_claim_requirement_reject_delete
BEFORE DELETE ON task_claim_requirements BEGIN
    SELECT RAISE(ABORT, 'task claim requirements are immutable');
END;

CREATE TRIGGER supervisor_policy_project_limit_validate_insert
BEFORE INSERT ON supervisor_policy_project_limits
BEGIN
    SELECT CASE WHEN EXISTS (
      SELECT 1 FROM supervisor_policies WHERE workspace_id=NEW.workspace_id AND revision=NEW.policy_revision
    ) OR NOT EXISTS (
      SELECT 1 FROM projects WHERE id=NEW.project_id AND workspace_id=NEW.workspace_id
    ) THEN RAISE(ABORT, 'invalid supervisor project limit') END;
END;

CREATE TRIGGER supervisor_policy_provider_limit_validate_insert
BEFORE INSERT ON supervisor_policy_provider_limits
BEGIN
    SELECT CASE WHEN EXISTS (
      SELECT 1 FROM supervisor_policies WHERE workspace_id=NEW.workspace_id AND revision=NEW.policy_revision
    ) THEN RAISE(ABORT, 'invalid supervisor provider limit') END;
END;

CREATE TRIGGER supervisor_policy_project_limit_reject_update
BEFORE UPDATE ON supervisor_policy_project_limits BEGIN SELECT RAISE(ABORT, 'supervisor policy limits are immutable'); END;

CREATE TRIGGER supervisor_policy_project_limit_reject_delete
BEFORE DELETE ON supervisor_policy_project_limits BEGIN SELECT RAISE(ABORT, 'supervisor policy limits are immutable'); END;

CREATE TRIGGER supervisor_policy_provider_limit_reject_update
BEFORE UPDATE ON supervisor_policy_provider_limits BEGIN SELECT RAISE(ABORT, 'supervisor policy limits are immutable'); END;

CREATE TRIGGER supervisor_policy_provider_limit_reject_delete
BEFORE DELETE ON supervisor_policy_provider_limits BEGIN SELECT RAISE(ABORT, 'supervisor policy limits are immutable'); END;

CREATE TRIGGER supervisor_policy_reject_update
BEFORE UPDATE ON supervisor_policies BEGIN
    SELECT RAISE(ABORT, 'supervisor policies are immutable revisions');
END;

CREATE TRIGGER supervisor_policy_reject_delete
BEFORE DELETE ON supervisor_policies BEGIN
    SELECT RAISE(ABORT, 'supervisor policies are immutable revisions');
END;

CREATE TRIGGER supervisor_policy_validate_insert
BEFORE INSERT ON supervisor_policies
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR NEW.project_concurrency_json IS NOT COALESCE((
        SELECT json_group_object(project_id,max_concurrency) FROM (
          SELECT project_id,max_concurrency FROM supervisor_policy_project_limits
          WHERE workspace_id=NEW.workspace_id AND policy_revision=NEW.revision ORDER BY project_id
        )
      ),'{}')
      OR NEW.provider_concurrency_json IS NOT COALESCE((
        SELECT json_group_object(provider,max_concurrency) FROM (
          SELECT provider,max_concurrency FROM supervisor_policy_provider_limits
          WHERE workspace_id=NEW.workspace_id AND policy_revision=NEW.revision ORDER BY provider
        )
      ),'{}')
      OR EXISTS (
        SELECT 1 FROM json_each(NEW.project_concurrency_json)
        WHERE type <> 'integer' OR value NOT BETWEEN 1 AND 100
      ) OR EXISTS (
        SELECT 1 FROM json_each(NEW.provider_concurrency_json)
        WHERE type <> 'integer' OR value NOT BETWEEN 1 AND 100
      ) OR NOT (
        (NEW.revision = 1 AND NEW.event_sequence = 0
          AND NEW.created_by = 'subsystem:supervisor' AND NEW.updated_by = 'subsystem:supervisor'
          AND NOT EXISTS (SELECT 1 FROM supervisor_policies prior WHERE prior.workspace_id = NEW.workspace_id))
        OR
        (NEW.revision > 1 AND NEW.event_sequence > 0
          AND NEW.created_by = 'local-owner' AND NEW.updated_by = 'local-owner'
          AND NEW.revision = 1 + COALESCE((SELECT MAX(revision) FROM supervisor_policies prior WHERE prior.workspace_id = NEW.workspace_id),0)
          AND EXISTS (
            SELECT 1 FROM events event WHERE event.sequence = NEW.event_sequence
              AND event.workspace_id = NEW.workspace_id AND event.entity_type = 'supervisor_policy'
              AND event.entity_revision = NEW.revision AND event.type = 'supervisor.policy_configured'
              AND event.actor_id = 'local-owner' AND event.actor_type = 'human'
          ))
      ) THEN RAISE(ABORT, 'invalid supervisor policy revision') END;
END;

CREATE TRIGGER workspace_seed_default_supervisor_policy
AFTER INSERT ON workspaces
BEGIN
    INSERT INTO supervisor_policies(
        workspace_id, revision, enabled, max_active_runs, max_starting_runs,
        default_project_concurrency, default_provider_concurrency,
        project_concurrency_json, provider_concurrency_json, auto_schedule,
        auto_retry_limit, retry_cooldown_seconds, event_sequence,
        created_at, updated_at, created_by, updated_by
    ) VALUES (
        NEW.id, 1, 0, 8, 2, 4, 4, '{}', '{}', 0, 0, 0, 0,
        NEW.created_at, NEW.created_at, 'subsystem:supervisor', 'subsystem:supervisor'
    );
END;

CREATE TRIGGER supervisor_action_validate_update
BEFORE UPDATE ON supervisor_actions
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
      OR NEW.project_id IS NOT OLD.project_id OR NEW.objective_id IS NOT OLD.objective_id
      OR NEW.task_id IS NOT OLD.task_id OR NEW.run_id IS NOT OLD.run_id
      OR NEW.prior_run_id IS NOT OLD.prior_run_id
	  OR NEW.source_proposal_id IS NOT OLD.source_proposal_id OR NEW.source_action_id IS NOT OLD.source_action_id
      OR NEW.agent_id IS NOT OLD.agent_id OR NEW.intent_id IS NOT OLD.intent_id
      OR NEW.condition IS NOT OLD.condition OR NEW.condition_key IS NOT OLD.condition_key
      OR NEW.response IS NOT OLD.response OR NEW.entity_revision IS NOT OLD.entity_revision
      OR NEW.policy_revision IS NOT OLD.policy_revision OR NEW.as_of_event_sequence IS NOT OLD.as_of_event_sequence
      OR NEW.reasons_json IS NOT OLD.reasons_json OR NEW.constraint_snapshot_json IS NOT OLD.constraint_snapshot_json
      OR NEW.content_sha256 IS NOT OLD.content_sha256 OR NEW.created_at IS NOT OLD.created_at
      OR NEW.created_by IS NOT OLD.created_by OR NEW.revision <> OLD.revision + 1
      OR NEW.approval_id IS NOT OLD.approval_id
      OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR crewfold_timestamp_key(NEW.updated_at) < crewfold_timestamp_key(OLD.updated_at)
      OR NEW.updated_by <> 'local-owner' OR OLD.status NOT IN ('proposed','awaiting_approval')
      OR NEW.status NOT IN ('applied','dismissed','failed')
      OR (NEW.status = 'applied' AND NEW.applied_at IS NOT NEW.updated_at)
      OR (OLD.status = 'awaiting_approval' AND NEW.status = 'applied' AND NOT EXISTS (
        SELECT 1 FROM approval_requests approval WHERE approval.id=OLD.approval_id
          AND approval.action_id=OLD.id AND approval.status='granted'
      ))
      OR (OLD.status = 'awaiting_approval' AND NEW.status = 'dismissed' AND NOT EXISTS (
        SELECT 1 FROM approval_requests approval WHERE approval.id=OLD.approval_id
          AND approval.action_id=OLD.id AND approval.status='denied'
      ))
      THEN RAISE(ABORT, 'invalid supervisor action decision') END;
END;

CREATE TRIGGER supervisor_action_reject_delete
BEFORE DELETE ON supervisor_actions BEGIN
    SELECT RAISE(ABORT, 'supervisor actions are durable decision records');
END;

CREATE TRIGGER supervisor_action_receipt_validate_insert
BEFORE INSERT ON supervisor_action_receipts
BEGIN
  SELECT CASE WHEN crewfold_supervisor_action_seal_active() IS NOT 1
    OR crewfold_timestamp_canonical(NEW.recorded_at) IS NOT 1 OR NOT EXISTS (
    SELECT 1 FROM supervisor_actions action JOIN events event ON event.sequence=NEW.event_sequence
    WHERE action.id=NEW.action_id AND action.workspace_id=NEW.workspace_id
      AND action.condition_key=NEW.condition_key AND action.status=NEW.recorded_status
      AND action.revision=1 AND action.created_at=NEW.recorded_at
      AND event.workspace_id=NEW.workspace_id AND event.entity_type='supervisor_action'
      AND event.entity_id=action.id AND event.entity_revision=1
      AND event.type=CASE action.status WHEN 'applied' THEN 'supervisor.action_applied' ELSE 'supervisor.action_recorded' END
      AND event.occurred_at=NEW.recorded_at AND event.recorded_at=NEW.recorded_at
      AND event.actor_type IN ('subsystem','human')
      AND json_extract(event.data_json,'$.condition')=action.condition
      AND json_extract(event.data_json,'$.response')=action.response
  ) THEN RAISE(ABORT, 'supervisor action recording receipt is incomplete') END;
END;

CREATE TRIGGER supervisor_action_receipt_reject_update
BEFORE UPDATE ON supervisor_action_receipts BEGIN SELECT RAISE(ABORT, 'supervisor action receipts are immutable'); END;

CREATE TRIGGER supervisor_action_receipt_reject_delete
BEFORE DELETE ON supervisor_action_receipts BEGIN SELECT RAISE(ABORT, 'supervisor action receipts are immutable'); END;

CREATE TRIGGER approval_request_validate_insert
BEFORE INSERT ON approval_requests
BEGIN
    SELECT CASE WHEN NEW.status <> 'pending' OR NEW.decision_note IS NOT NULL
      OR NEW.decision_event_sequence IS NOT NULL OR NEW.revision <> 1
      OR NEW.created_at IS NOT NEW.updated_at OR NEW.decided_at IS NOT NULL OR NEW.decided_by IS NOT NULL
      OR NEW.updated_by <> 'subsystem:supervisor' OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR (NEW.expires_at IS NOT NULL AND (
        crewfold_timestamp_canonical(NEW.expires_at) IS NOT 1
        OR crewfold_timestamp_key(NEW.expires_at) <= crewfold_timestamp_key(NEW.created_at)
      )) OR NOT EXISTS (
        SELECT 1 FROM supervisor_actions action
        WHERE action.id = NEW.action_id AND action.workspace_id = NEW.workspace_id
          AND action.status = 'awaiting_approval' AND action.revision = NEW.expected_action_revision
          AND action.response <> 'schedule' AND action.approval_id = NEW.id
      ) THEN RAISE(ABORT, 'approval request lacks exact supervisor action') END;
END;

CREATE TRIGGER approval_request_validate_update
BEFORE UPDATE ON approval_requests
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
      OR NEW.action_id IS NOT OLD.action_id OR NEW.expected_action_revision IS NOT OLD.expected_action_revision
      OR NEW.expires_at IS NOT OLD.expires_at OR NEW.created_at IS NOT OLD.created_at
      OR NEW.created_by IS NOT OLD.created_by
      OR NOT (
        (OLD.status = 'pending' AND NEW.status IN ('granted','denied','expired'))
        OR (OLD.status = 'granted' AND NEW.status = 'consumed')
      )
      OR NEW.revision <> OLD.revision + 1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR crewfold_timestamp_key(NEW.updated_at) < crewfold_timestamp_key(OLD.updated_at)
      OR NEW.updated_by <> 'local-owner'
      OR (OLD.status = 'pending' AND (
        crewfold_timestamp_key(NEW.updated_at) <= crewfold_timestamp_key(OLD.updated_at)
        OR NEW.decided_at IS NOT NEW.updated_at OR NEW.decided_by <> 'local-owner'
        OR NEW.decision_note IS NULL OR NEW.decision_event_sequence IS NULL
        OR NOT EXISTS (
          SELECT 1 FROM events event WHERE event.sequence=NEW.decision_event_sequence
            AND event.workspace_id=NEW.workspace_id AND event.entity_type='approval_request'
            AND event.entity_id=NEW.id AND event.entity_revision=NEW.revision
            AND event.type=CASE NEW.status WHEN 'granted' THEN 'approval.granted'
              WHEN 'denied' THEN 'approval.denied' ELSE 'approval.expired' END
            AND event.occurred_at=NEW.decided_at AND event.recorded_at=NEW.decided_at
            AND event.actor_id='local-owner' AND event.actor_type='human'
            AND json_extract(event.data_json,'$.action_id')=NEW.action_id
            AND json_extract(event.data_json,'$.status')=NEW.status
            AND json_extract(event.data_json,'$.decision_note')=NEW.decision_note
        )
      ))
      OR (OLD.status = 'granted' AND (
        NEW.decision_note IS NOT OLD.decision_note
        OR NEW.decision_event_sequence IS NOT OLD.decision_event_sequence
        OR NEW.decided_at IS NOT OLD.decided_at OR NEW.decided_by IS NOT OLD.decided_by
        OR NOT EXISTS (
          SELECT 1 FROM supervisor_actions action WHERE action.id=NEW.action_id
            AND action.approval_id=NEW.id AND action.status='applied'
        )
      ))
      THEN RAISE(ABORT, 'invalid approval decision') END;
END;

CREATE TRIGGER approval_request_reject_delete
BEFORE DELETE ON approval_requests BEGIN
    SELECT RAISE(ABORT, 'approval requests are durable decision records');
END;

CREATE TRIGGER workspace_seed_default_supervisor_state
AFTER INSERT ON workspaces
BEGIN
    INSERT INTO supervisor_state(workspace_id, last_event_sequence, revision, updated_at)
    VALUES (NEW.id, 0, 1, NEW.created_at);
END;

CREATE TRIGGER supervisor_state_validate_update
BEFORE UPDATE ON supervisor_state
BEGIN
    SELECT CASE WHEN NEW.workspace_id IS NOT OLD.workspace_id
      OR NEW.last_event_sequence < OLD.last_event_sequence
	  OR NEW.last_event_sequence > COALESCE((
	    SELECT MAX(event.sequence) FROM events event WHERE event.workspace_id=OLD.workspace_id
	  ),0)
	  OR (NEW.last_event_sequence > OLD.last_event_sequence AND NOT EXISTS (
	    SELECT 1 FROM events event WHERE event.workspace_id=OLD.workspace_id
	      AND event.sequence=NEW.last_event_sequence
	  ))
	  OR EXISTS (
	    SELECT 1 FROM events event WHERE event.workspace_id=OLD.workspace_id
	      AND event.sequence>OLD.last_event_sequence AND event.sequence<=NEW.last_event_sequence
	      AND crewfold_event_type_known(event.type) IS NOT 1
	  )
      OR NEW.revision <> OLD.revision + 1
      OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR crewfold_timestamp_key(NEW.updated_at) <= crewfold_timestamp_key(OLD.updated_at)
      THEN RAISE(ABORT, 'invalid supervisor scan cursor') END;
END;

CREATE TRIGGER supervisor_state_reject_delete
BEFORE DELETE ON supervisor_state BEGIN
    SELECT RAISE(ABORT, 'supervisor state is durable');
END;

CREATE TRIGGER runs_require_backfilled_assignment
BEFORE UPDATE OF assignment_id ON runs
WHEN NEW.assignment_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM task_assignments assignment
    WHERE assignment.id = NEW.assignment_id AND assignment.task_id = NEW.task_id
      AND assignment.agent_id = NEW.agent_id
)
BEGIN
    SELECT RAISE(ABORT, 'run assignment backfill differs from packet');
END;

CREATE TRIGGER runs_require_exact_assignment_insert
BEFORE INSERT ON runs
BEGIN
    SELECT CASE WHEN NEW.assignment_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM task_assignments assignment
        WHERE assignment.id = NEW.assignment_id AND assignment.task_id = NEW.task_id
          AND assignment.agent_id = NEW.agent_id AND assignment.status = 'active'
    ) THEN RAISE(ABORT, 'run requires exact active assignment') END;
END;

CREATE TRIGGER runs_reject_assignment_update
BEFORE UPDATE OF assignment_id ON runs
BEGIN
    SELECT RAISE(ABORT, 'run assignment linkage is immutable');
END;

CREATE TRIGGER runs_reject_authority_update
BEFORE UPDATE OF workspace_id,project_id,task_id,agent_id,checkout_id,runtime,provider,
  scenario_name,scenario_json,placement_reasons_json,created_at,created_by ON runs
BEGIN
    SELECT RAISE(ABORT, 'run launch authority is immutable');
END;

CREATE TRIGGER runs_validate_lifecycle_update
BEFORE UPDATE ON runs
BEGIN
    SELECT CASE WHEN NEW.revision <= OLD.revision
      OR NOT (
        NEW.status = OLD.status
        OR (OLD.status='requested' AND NEW.status IN ('starting','start_failed'))
        OR (OLD.status='starting' AND NEW.status IN ('active','start_failed','lost'))
        OR (OLD.status='active' AND NEW.status IN ('blocked','review','completed','stopping','lost','failed'))
        OR (OLD.status='blocked' AND NEW.status IN ('active','stopping'))
        OR (OLD.status='stopping' AND NEW.status IN ('stopped','lost'))
        OR (OLD.status='lost' AND NEW.status='failed'
            AND crewfold_run_loss_resolution_active() IS 1
            AND NEW.revision=OLD.revision+1
            AND NEW.updated_by='local-owner'
            AND NEW.failure_code='runtime_retired_by_owner'
            AND NOT EXISTS(SELECT 1 FROM run_runtime_bindings binding WHERE binding.run_id=OLD.id)
            AND NEW.finished_at=NEW.updated_at)
      )
      OR (NEW.status IN ('stopped','review','completed','start_failed','failed')
          AND EXISTS(SELECT 1 FROM run_runtime_bindings binding WHERE binding.run_id=OLD.id))
      THEN RAISE(ABORT, 'invalid run lifecycle transition') END;
END;

CREATE TRIGGER run_runtime_binding_validate_insert
BEFORE INSERT ON run_runtime_bindings
BEGIN
    SELECT CASE WHEN NEW.operation_id<>NEW.run_id OR NEW.revision<>1
      OR NEW.created_at<>NEW.updated_at
      OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR crewfold_utf8_valid(NEW.runtime_handle) IS NOT 1
      OR (NEW.provider_handle IS NOT NULL AND crewfold_utf8_valid(NEW.provider_handle) IS NOT 1)
      OR NOT EXISTS(SELECT 1 FROM runs run WHERE run.id=NEW.run_id AND run.status='starting')
      THEN RAISE(ABORT, 'invalid run runtime binding') END;
END;

CREATE TRIGGER run_runtime_binding_validate_update
BEFORE UPDATE ON run_runtime_bindings
BEGIN
    SELECT CASE WHEN NEW.run_id<>OLD.run_id OR NEW.node_id<>OLD.node_id
      OR NEW.node_fingerprint<>OLD.node_fingerprint OR NEW.operation_id<>OLD.operation_id
      OR NEW.runtime_handle<>OLD.runtime_handle OR NEW.created_at<>OLD.created_at
      OR OLD.provider_handle IS NOT NULL OR NEW.provider_handle IS NULL
      OR NEW.revision<>OLD.revision+1
      OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR crewfold_utf8_valid(NEW.provider_handle) IS NOT 1
      OR NOT EXISTS(SELECT 1 FROM runs run WHERE run.id=NEW.run_id AND run.status='starting')
      THEN RAISE(ABORT, 'invalid run runtime binding update') END;
END;

CREATE TRIGGER task_assignment_reserved_run_reject_release
BEFORE UPDATE OF status ON task_assignments
WHEN OLD.status = 'active' AND NEW.status IN ('expired','released') AND EXISTS (
    SELECT 1 FROM runs run WHERE run.assignment_id = OLD.id
      AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)
BEGIN
    SELECT RAISE(ABORT, 'reserved run retains its exact assignment');
END;

CREATE TRIGGER work_claim_reserved_run_reject_release
BEFORE UPDATE OF status ON work_claims
WHEN OLD.status = 'active' AND NEW.status IN ('expired','released') AND EXISTS (
    SELECT 1 FROM runs run WHERE run.task_id = OLD.task_id
      AND run.status IN ('requested','starting','active','blocked','stopping','lost')
)
BEGIN
    SELECT RAISE(ABORT, 'reserved run retains task claims');
END;

CREATE TRIGGER run_job_origin_reject_update
BEFORE UPDATE OF origin ON run_jobs
BEGIN
    SELECT RAISE(ABORT, 'run job origin is immutable');
END;

CREATE TRIGGER run_job_validate_lifecycle_update
BEFORE UPDATE ON run_jobs
BEGIN
    SELECT CASE WHEN NEW.attempts < OLD.attempts
      OR (OLD.status='complete' AND NEW.status='pending' AND NOT EXISTS (
        SELECT 1 FROM runs run WHERE run.id=OLD.run_id AND run.status IN ('active','stopping')
      ))
      THEN RAISE(ABORT, 'invalid run job lifecycle transition') END;
END;

CREATE TRIGGER run_scheduling_receipt_reject_update
BEFORE UPDATE ON run_scheduling_receipts BEGIN
    SELECT RAISE(ABORT, 'run scheduling receipts are immutable');
END;

CREATE TRIGGER run_scheduling_receipt_reject_delete
BEFORE DELETE ON run_scheduling_receipts BEGIN
    SELECT RAISE(ABORT, 'run scheduling receipts are immutable');
END;

CREATE TRIGGER run_retry_receipt_reject_update
BEFORE UPDATE ON run_retry_receipts BEGIN SELECT RAISE(ABORT, 'run retry receipts are immutable'); END;

CREATE TRIGGER run_retry_receipt_reject_delete
BEFORE DELETE ON run_retry_receipts BEGIN SELECT RAISE(ABORT, 'run retry receipts are immutable'); END;

CREATE TRIGGER participant_message_validate_insert
BEFORE INSERT ON messages
WHEN (SELECT kind FROM message_threads WHERE id=NEW.thread_id)='participant_bound'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM message_threads th
        WHERE th.id=NEW.thread_id AND th.workspace_id=NEW.workspace_id AND th.status='open'
          AND th.participant_revision=(SELECT COUNT(*)-th.initial_participant_count+1 FROM thread_participants WHERE thread_id=th.id)
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id=th.id)>=th.initial_participant_count
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id=th.id) BETWEEN 2 AND 8
          AND (SELECT COUNT(DISTINCT project_id) FROM thread_participants WHERE thread_id=th.id)>=2
    ) THEN RAISE(ABORT,'participant thread roster is incomplete or inconsistent') END;
    SELECT CASE WHEN NEW.reply_to_message_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM messages reply WHERE reply.id=NEW.reply_to_message_id AND reply.thread_id=NEW.thread_id AND reply.workspace_id=NEW.workspace_id
    ) THEN RAISE(ABORT,'participant reply target must belong to the same thread') END;
    SELECT CASE WHEN json_type(NEW.artifact_ids_json)<>'array' OR json_array_length(NEW.artifact_ids_json)<>0
        THEN RAISE(ABORT,'participant-bound messages cannot attach artifacts') END;
    SELECT CASE WHEN NEW.sender_type='agent_run' AND NOT EXISTS (
        SELECT 1 FROM thread_participants p JOIN runs r ON r.id=NEW.sender_run_id
        WHERE p.thread_id=NEW.thread_id AND p.status='active' AND p.agent_id=NEW.sender_agent_id
          AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id AND r.workspace_id=NEW.workspace_id
          AND r.agent_id=p.agent_id AND r.project_id=p.project_id AND r.task_id=p.task_id
          AND r.status IN ('starting','active','blocked') AND NEW.sender_id=r.id
    ) THEN RAISE(ABORT,'participant message sender is outside its bound scope') END;
    SELECT CASE WHEN NEW.sender_type='owner' AND (NEW.sender_id<>'local-owner' OR NEW.sender_agent_id IS NOT NULL OR NEW.sender_run_id IS NOT NULL OR NEW.project_id IS NOT NULL OR NEW.task_id IS NOT NULL)
        THEN RAISE(ABORT,'owner participant messages cannot impersonate an agent') END;
    SELECT CASE WHEN NEW.sender_type='durable_agent'
        THEN RAISE(ABORT,'durable-agent sessions cannot impersonate task-bound thread participants') END;
    SELECT CASE WHEN NEW.sender_type='subsystem' THEN RAISE(ABORT,'check notifications require direct threads') END;
END;

CREATE TRIGGER durable_agent_message_validate_insert
BEFORE INSERT ON messages
WHEN NEW.sender_type='durable_agent'
BEGIN
    SELECT CASE WHEN NEW.sender_run_id IS NOT NULL OR NEW.sender_agent_id IS NULL
      OR NEW.sender_id<>NEW.sender_agent_id OR NEW.project_id IS NULL OR NEW.task_id IS NOT NULL
      OR NOT EXISTS (
        SELECT 1 FROM agents agent
        JOIN domain_agent_memberships membership
          ON membership.agent_id=agent.id AND membership.project_id=NEW.project_id
        JOIN domain_agent_session_bindings binding
          ON binding.project_id=membership.project_id AND binding.agent_id=membership.agent_id
        JOIN projects project ON project.id=membership.project_id
        WHERE agent.id=NEW.sender_agent_id AND agent.workspace_id=NEW.workspace_id
          AND project.workspace_id=NEW.workspace_id AND agent.enabled=1 AND membership.status='active'
      )
      THEN RAISE(ABORT,'durable-agent message lacks an active session-bound domain sender') END;
END;

CREATE TRIGGER message_reject_update BEFORE UPDATE ON messages BEGIN SELECT RAISE(ABORT,'messages are immutable'); END;

CREATE TRIGGER message_reject_delete BEFORE DELETE ON messages BEGIN SELECT RAISE(ABORT,'messages are immutable'); END;

CREATE TRIGGER participant_recipient_validate_insert
BEFORE INSERT ON message_recipients
BEGIN
    SELECT CASE WHEN (
        SELECT th.kind FROM messages m JOIN message_threads th ON th.id=m.thread_id WHERE m.id=NEW.message_id
    )='participant_bound' AND (
        NEW.recipient_participant_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM messages m JOIN thread_participants p ON p.id=NEW.recipient_participant_id
            JOIN agents recipient_agent ON recipient_agent.id=p.agent_id
            WHERE m.id=NEW.message_id AND p.thread_id=m.thread_id AND p.agent_id=NEW.recipient_agent_id
              AND p.status='active' AND recipient_agent.workspace_id=m.workspace_id AND recipient_agent.enabled=1
        ) OR EXISTS (SELECT 1 FROM message_recipients existing WHERE existing.message_id=NEW.message_id)
        OR EXISTS (
            SELECT 1 FROM messages m JOIN thread_participants sender ON sender.thread_id=m.thread_id
             AND sender.agent_id=m.sender_agent_id AND sender.project_id=m.project_id AND sender.task_id=m.task_id
            WHERE m.id=NEW.message_id AND m.sender_type='agent_run' AND sender.id=NEW.recipient_participant_id
        )
    ) THEN RAISE(ABORT,'participant-bound recipient must name an active bound participant') END;
    SELECT CASE WHEN (
        SELECT th.kind FROM messages m JOIN message_threads th ON th.id=m.thread_id WHERE m.id=NEW.message_id
    )='direct' AND NEW.recipient_participant_id IS NOT NULL
        THEN RAISE(ABORT,'direct message recipient cannot name a bound participant') END;
END;

CREATE TRIGGER message_recipient_reject_delete BEFORE DELETE ON message_recipients BEGIN SELECT RAISE(ABORT,'message recipients cannot be removed'); END;

CREATE TRIGGER message_recipient_binding_reject_update
BEFORE UPDATE OF message_id,recipient_agent_id,recipient_participant_id ON message_recipients
WHEN NEW.message_id<>OLD.message_id OR NEW.recipient_agent_id<>OLD.recipient_agent_id OR NEW.recipient_participant_id IS NOT OLD.recipient_participant_id
BEGIN SELECT RAISE(ABORT,'message recipient binding is immutable'); END;

CREATE TRIGGER participant_wake_validate_insert
BEFORE INSERT ON message_wake_jobs
WHEN (SELECT th.kind FROM messages m JOIN message_threads th ON th.id=m.thread_id WHERE m.id=NEW.message_id)='participant_bound'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM message_recipients mr JOIN messages m ON m.id=mr.message_id
        JOIN thread_participants p ON p.id=mr.recipient_participant_id JOIN runs r ON r.id=NEW.target_run_id
        WHERE mr.message_id=NEW.message_id AND mr.recipient_agent_id=NEW.recipient_agent_id
          AND m.workspace_id=r.workspace_id AND p.thread_id=m.thread_id AND p.agent_id=NEW.recipient_agent_id
          AND r.agent_id=p.agent_id AND r.project_id=p.project_id AND r.task_id=p.task_id
          AND r.status IN ('starting','active','blocked')
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id=m.thread_id) BETWEEN 2 AND 8
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id=m.thread_id)>=(SELECT initial_participant_count FROM message_threads WHERE id=m.thread_id)
          AND (SELECT COUNT(*) FROM thread_participants WHERE thread_id=m.thread_id)=(SELECT initial_participant_count+participant_revision-1 FROM message_threads WHERE id=m.thread_id)
          AND (SELECT COUNT(DISTINCT project_id) FROM thread_participants WHERE thread_id=m.thread_id)>=2
    ) THEN RAISE(ABORT,'participant wake target is outside its bound scope') END;
END;

CREATE TRIGGER message_wake_binding_reject_update
BEFORE UPDATE OF message_id,recipient_agent_id,target_run_id ON message_wake_jobs
WHEN NEW.message_id<>OLD.message_id OR NEW.recipient_agent_id<>OLD.recipient_agent_id OR NEW.target_run_id<>OLD.target_run_id
BEGIN SELECT RAISE(ABORT,'message wake binding is immutable'); END;

CREATE TRIGGER immutable_artifact_validate_insert
BEFORE INSERT ON immutable_artifacts
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      THEN RAISE(ABORT, 'invalid immutable artifact metadata') END;
END;

CREATE TRIGGER immutable_artifact_reject_update
BEFORE UPDATE ON immutable_artifacts
BEGIN SELECT RAISE(ABORT, 'immutable artifact catalog cannot be updated'); END;

CREATE TRIGGER immutable_artifact_reject_delete
BEFORE DELETE ON immutable_artifacts
BEGIN SELECT RAISE(ABORT, 'immutable artifact catalog cannot be deleted'); END;

CREATE TRIGGER run_log_artifact_validate_insert
BEFORE INSERT ON run_log_artifacts
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR NOT EXISTS (
        SELECT 1 FROM immutable_artifacts artifact
        WHERE artifact.content_sha256 = NEW.content_sha256
          AND artifact.byte_size = NEW.captured_bytes
      )
      OR NOT EXISTS (
        SELECT 1 FROM runs run
        WHERE run.id = NEW.run_id
          AND run.status IN ('stopped','review','completed','start_failed','failed')
      )
      THEN RAISE(ABORT, 'invalid immutable run log artifact') END;
END;

CREATE TRIGGER run_log_artifact_reject_update
BEFORE UPDATE ON run_log_artifacts
BEGIN SELECT RAISE(ABORT, 'run log artifacts cannot be updated'); END;

CREATE TRIGGER run_log_artifact_reject_delete
BEFORE DELETE ON run_log_artifacts
BEGIN SELECT RAISE(ABORT, 'run log artifacts cannot be deleted'); END;

CREATE TRIGGER run_loss_resolution_validate_insert
BEFORE INSERT ON run_loss_resolutions
BEGIN
    SELECT CASE WHEN crewfold_run_loss_resolution_active() IS NOT 1
      OR crewfold_timestamp_canonical(NEW.resolved_at) IS NOT 1
      OR crewfold_utf8_valid(NEW.note) IS NOT 1
      OR NOT EXISTS (
        SELECT 1 FROM runs run
        WHERE run.id = NEW.run_id AND run.status = 'failed'
          AND run.revision = NEW.lost_revision + 1
          AND run.failure_code = 'runtime_retired_by_owner'
          AND run.updated_at = NEW.resolved_at
          AND run.finished_at = NEW.resolved_at
          AND run.updated_by = 'local-owner'
      )
      OR NOT EXISTS (
        SELECT 1 FROM events event
        WHERE event.sequence = NEW.event_sequence
          AND event.type = 'run.lost_resolved'
          AND event.entity_type = 'run' AND event.entity_id = NEW.run_id
          AND event.entity_revision = NEW.lost_revision + 1
          AND event.actor_id = 'local-owner' AND event.actor_type = 'human'
          AND event.occurred_at = NEW.resolved_at
          AND json_extract(event.data_json, '$.prior_status') = 'lost'
          AND json_extract(event.data_json, '$.status') = 'failed'
          AND json_extract(event.data_json, '$.resolution') = NEW.resolution
          AND json_extract(event.data_json, '$.lost_revision') = NEW.lost_revision
          AND json_extract(event.data_json, '$.note') = NEW.note
          AND json_extract(event.data_json, '$.capacity_released') = 1
      )
      THEN RAISE(ABORT, 'invalid owner run-loss resolution') END;
END;

CREATE TRIGGER run_loss_resolution_reject_update
BEFORE UPDATE ON run_loss_resolutions
BEGIN SELECT RAISE(ABORT, 'run-loss resolutions cannot be updated'); END;

CREATE TRIGGER run_loss_resolution_reject_delete
BEFORE DELETE ON run_loss_resolutions
BEGIN SELECT RAISE(ABORT, 'run-loss resolutions cannot be deleted'); END;

CREATE TRIGGER check_definition_argument_validate_insert
BEFORE INSERT ON check_definition_arguments BEGIN
  SELECT CASE WHEN EXISTS(SELECT 1 FROM check_definitions WHERE id=NEW.definition_id)
    OR crewfold_utf8_valid(NEW.argument) IS NOT 1
    OR NEW.ordinal IS NOT (SELECT COUNT(*) FROM check_definition_arguments WHERE definition_id=NEW.definition_id)
    THEN RAISE(ABORT,'check definition arguments are not contiguous immutable children') END;
END;

CREATE TRIGGER check_definition_argument_reject_update BEFORE UPDATE ON check_definition_arguments BEGIN SELECT RAISE(ABORT,'check definition arguments are immutable'); END;

CREATE TRIGGER check_definition_argument_reject_delete BEFORE DELETE ON check_definition_arguments BEGIN SELECT RAISE(ABORT,'check definition arguments are immutable'); END;

CREATE TRIGGER check_definition_validate_insert
BEFORE INSERT ON check_definitions BEGIN
  SELECT CASE WHEN NEW.status<>'active' OR NEW.revision<>1 OR NEW.content_revision<>1
    OR crewfold_utf8_valid(NEW.name) IS NOT 1 OR crewfold_utf8_valid(NEW.executable) IS NOT 1 OR crewfold_utf8_valid(NEW.working_directory) IS NOT 1
    OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NEW.created_at IS NOT NEW.updated_at
    OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256
    OR json_extract(NEW.content_json,'$.workspace_id') IS NOT NEW.workspace_id
    OR json_extract(NEW.content_json,'$.project_id') IS NOT NEW.project_id
    OR json_extract(NEW.content_json,'$.name') IS NOT NEW.name
    OR json_extract(NEW.content_json,'$.executable') IS NOT NEW.executable
    OR json_extract(NEW.content_json,'$.working_directory') IS NOT NEW.working_directory
    OR json_extract(NEW.content_json,'$.timeout_millis') IS NOT NEW.timeout_millis
    OR json_extract(NEW.content_json,'$.output_byte_limit') IS NOT NEW.output_byte_limit
    OR json(json_extract(NEW.content_json,'$.arguments'))<>json(NEW.arguments_json)
    OR json_array_length(NEW.arguments_json) IS NOT (SELECT COUNT(*) FROM check_definition_arguments WHERE definition_id=NEW.id)
    OR EXISTS(SELECT 1 FROM check_definition_arguments argument WHERE argument.definition_id=NEW.id AND json_extract(NEW.arguments_json,'$['||argument.ordinal||']') IS NOT argument.argument)
    OR NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND workspace_id=NEW.workspace_id)
    THEN RAISE(ABORT,'invalid immutable check definition') END;
END;

CREATE TRIGGER check_definition_validate_update
BEFORE UPDATE ON check_definitions BEGIN
  SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id
    OR NEW.name IS NOT OLD.name OR NEW.executable IS NOT OLD.executable OR NEW.working_directory IS NOT OLD.working_directory
    OR NEW.timeout_millis IS NOT OLD.timeout_millis OR NEW.output_byte_limit IS NOT OLD.output_byte_limit
    OR NEW.arguments_json IS NOT OLD.arguments_json OR NEW.content_json IS NOT OLD.content_json
    OR NEW.content_revision IS NOT OLD.content_revision OR NEW.content_sha256 IS NOT OLD.content_sha256
    OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
    OR OLD.status<>'active' OR NEW.status<>'retired' OR NEW.revision<>OLD.revision+1
    OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at)
    OR NEW.updated_by<>'local-owner' THEN RAISE(ABORT,'check definitions are immutable except retirement') END;
END;

CREATE TRIGGER check_definition_reject_delete BEFORE DELETE ON check_definitions BEGIN SELECT RAISE(ABORT,'check definitions are immutable'); END;

CREATE TRIGGER task_check_requirement_validate_insert BEFORE INSERT ON task_check_requirements BEGIN
  SELECT CASE WHEN NEW.status<>'active' OR NEW.revision<>1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NEW.created_at IS NOT NEW.updated_at
    OR crewfold_utf8_valid(NEW.criterion_key) IS NOT 1 OR crewfold_utf8_valid(NEW.statement) IS NOT 1
    OR NOT EXISTS(SELECT 1 FROM tasks task JOIN check_definitions definition ON definition.id=NEW.definition_id
      WHERE task.id=NEW.task_id AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id
        AND task.revision=NEW.task_revision_at_creation AND definition.workspace_id=NEW.workspace_id
        AND definition.project_id=NEW.project_id AND definition.content_revision=NEW.definition_content_revision AND definition.status='active')
    THEN RAISE(ABORT,'invalid exact task check requirement') END;
END;

CREATE TRIGGER task_check_requirement_validate_update BEFORE UPDATE ON task_check_requirements BEGIN
 SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.task_id IS NOT OLD.task_id
   OR NEW.task_revision_at_creation IS NOT OLD.task_revision_at_creation OR NEW.criterion_key IS NOT OLD.criterion_key OR NEW.statement IS NOT OLD.statement
   OR NEW.definition_id IS NOT OLD.definition_id OR NEW.definition_content_revision IS NOT OLD.definition_content_revision
   OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR OLD.status<>'active' OR NEW.status<>'retired'
   OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
   OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by<>'local-owner'
   THEN RAISE(ABORT,'task check requirements are immutable except retirement') END;
END;

CREATE TRIGGER task_check_requirement_reject_delete BEFORE DELETE ON task_check_requirements BEGIN SELECT RAISE(ABORT,'task check requirements are immutable'); END;

CREATE TRIGGER check_watch_grant_operation_validate_insert BEFORE INSERT ON check_watch_grant_operations BEGIN
 SELECT CASE WHEN EXISTS(SELECT 1 FROM check_watch_grants WHERE id=NEW.grant_id) OR NEW.ordinal IS NOT (SELECT COUNT(*) FROM check_watch_grant_operations WHERE grant_id=NEW.grant_id)
  OR EXISTS(SELECT 1 FROM check_watch_grant_operations prior WHERE prior.grant_id=NEW.grant_id AND CASE prior.operation WHEN 'run' THEN 0 WHEN 'inspect' THEN 1 ELSE 2 END >= CASE NEW.operation WHEN 'run' THEN 0 WHEN 'inspect' THEN 1 ELSE 2 END)
  THEN RAISE(ABORT,'check-watch grant operations are not canonical') END;
END;

CREATE TRIGGER check_watch_grant_definition_validate_insert BEFORE INSERT ON check_watch_grant_definitions BEGIN
 SELECT CASE WHEN EXISTS(SELECT 1 FROM check_watch_grants WHERE id=NEW.grant_id) OR NEW.ordinal IS NOT (SELECT COUNT(*) FROM check_watch_grant_definitions WHERE grant_id=NEW.grant_id)
  OR EXISTS(SELECT 1 FROM check_watch_grant_definitions prior WHERE prior.grant_id=NEW.grant_id AND prior.definition_id>=NEW.definition_id)
  OR NOT EXISTS(SELECT 1 FROM check_definitions definition WHERE definition.id=NEW.definition_id AND definition.content_revision=NEW.definition_content_revision
      AND definition.content_sha256=NEW.definition_sha256 AND definition.status='active')
  THEN RAISE(ABORT,'check-watch grant definitions are not exact canonical children') END;
END;

CREATE TRIGGER check_watch_grant_operation_reject_update BEFORE UPDATE ON check_watch_grant_operations BEGIN SELECT RAISE(ABORT,'check-watch grant authority is immutable'); END;

CREATE TRIGGER check_watch_grant_operation_reject_delete BEFORE DELETE ON check_watch_grant_operations BEGIN SELECT RAISE(ABORT,'check-watch grant authority is immutable'); END;

CREATE TRIGGER check_watch_grant_definition_reject_update BEFORE UPDATE ON check_watch_grant_definitions BEGIN SELECT RAISE(ABORT,'check-watch grant authority is immutable'); END;

CREATE TRIGGER check_watch_grant_definition_reject_delete BEFORE DELETE ON check_watch_grant_definitions BEGIN SELECT RAISE(ABORT,'check-watch grant authority is immutable'); END;

CREATE TRIGGER check_watch_grant_validate_insert BEFORE INSERT ON check_watch_grants BEGIN
 SELECT CASE WHEN NEW.status<>'active' OR NEW.revision<>1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NEW.created_at IS NOT NEW.updated_at
   OR (NEW.expires_at IS NOT NULL AND (crewfold_timestamp_canonical(NEW.expires_at) IS NOT 1 OR crewfold_timestamp_key(NEW.expires_at)<=crewfold_timestamp_key(NEW.created_at)))
   OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256
   OR json_extract(NEW.content_json,'$.workspace_id') IS NOT NEW.workspace_id OR json_extract(NEW.content_json,'$.project_id') IS NOT NEW.project_id
   OR json_extract(NEW.content_json,'$.agent_id') IS NOT NEW.agent_id OR json_extract(NEW.content_json,'$.agent_revision') IS NOT NEW.agent_revision
   OR json(json_extract(NEW.content_json,'$.operations'))<>json(NEW.operations_json) OR json(json_extract(NEW.content_json,'$.definitions'))<>json(NEW.definitions_json)
   OR json_extract(NEW.content_json,'$.max_pending') IS NOT NEW.max_pending OR json_extract(NEW.content_json,'$.max_in_flight') IS NOT NEW.max_in_flight
   OR COALESCE(json_extract(NEW.content_json,'$.expires_at'),'')<>COALESCE(NEW.expires_at,'')
   OR NEW.operations_json IS NOT (SELECT json_group_array(operation) FROM (SELECT operation FROM check_watch_grant_operations WHERE grant_id=NEW.id ORDER BY ordinal))
   OR json_array_length(NEW.definitions_json) IS NOT (SELECT COUNT(*) FROM check_watch_grant_definitions WHERE grant_id=NEW.id)
   OR EXISTS(SELECT 1 FROM check_watch_grant_definitions child WHERE child.grant_id=NEW.id AND (json_extract(NEW.definitions_json,'$['||child.ordinal||'].definition_id') IS NOT child.definition_id OR json_extract(NEW.definitions_json,'$['||child.ordinal||'].content_revision') IS NOT child.definition_content_revision OR json_extract(NEW.definitions_json,'$['||child.ordinal||'].definition_sha256') IS NOT child.definition_sha256))
   OR NOT EXISTS(SELECT 1 FROM projects project JOIN agents agent ON agent.id=NEW.agent_id WHERE project.id=NEW.project_id AND project.workspace_id=NEW.workspace_id AND agent.workspace_id=NEW.workspace_id AND agent.enabled=1 AND agent.revision=NEW.agent_revision)
   OR EXISTS(SELECT 1 FROM check_watch_grant_definitions child JOIN check_definitions definition ON definition.id=child.definition_id WHERE child.grant_id=NEW.id AND (definition.workspace_id<>NEW.workspace_id OR definition.project_id<>NEW.project_id))
   THEN RAISE(ABORT,'invalid exact check-watch grant') END;
END;

CREATE TRIGGER check_watch_grant_validate_update BEFORE UPDATE ON check_watch_grants BEGIN
 SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.agent_id IS NOT OLD.agent_id OR NEW.agent_revision IS NOT OLD.agent_revision
  OR NEW.operations_json IS NOT OLD.operations_json OR NEW.definitions_json IS NOT OLD.definitions_json OR NEW.max_pending IS NOT OLD.max_pending OR NEW.max_in_flight IS NOT OLD.max_in_flight
  OR NEW.expires_at IS NOT OLD.expires_at OR NEW.content_json IS NOT OLD.content_json OR NEW.content_sha256 IS NOT OLD.content_sha256 OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
  OR OLD.status<>'active' OR NEW.status NOT IN ('revoked','expired') OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
  OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by<>'local-owner' THEN RAISE(ABORT,'check-watch grants are immutable except terminal lifecycle') END;
END;

CREATE TRIGGER check_watch_grant_reject_delete BEFORE DELETE ON check_watch_grants BEGIN SELECT RAISE(ABORT,'check-watch grants are immutable'); END;

CREATE TRIGGER project_seed_check_policy AFTER INSERT ON projects BEGIN
 INSERT INTO check_policies(workspace_id,project_id,repair_proposals_enabled,repair_launch_profile_id,repair_launch_profile_revision,max_open_repair_proposals,revision,created_at,updated_at,created_by,updated_by)
 VALUES(NEW.workspace_id,NEW.id,0,NULL,NULL,1,1,NEW.created_at,NEW.created_at,'local-owner','local-owner');
END;

CREATE TRIGGER check_policy_validate_insert BEFORE INSERT ON check_policies BEGIN
 SELECT CASE WHEN NEW.revision<>1 OR NEW.created_at IS NOT NEW.updated_at OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND workspace_id=NEW.workspace_id) THEN RAISE(ABORT,'invalid seeded check policy') END;
END;

CREATE TRIGGER check_policy_validate_update BEFORE UPDATE ON check_policies BEGIN
 SELECT CASE WHEN NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by<>'local-owner'
  OR (NEW.repair_proposals_enabled=1 AND NOT EXISTS(SELECT 1 FROM launch_profiles profile WHERE profile.id=NEW.repair_launch_profile_id AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id AND profile.revision=NEW.repair_launch_profile_revision AND profile.status='active' AND profile.manager_grant_id IS NULL))
  THEN RAISE(ABORT,'invalid check policy revision') END;
END;

CREATE TRIGGER check_policy_reject_delete BEFORE DELETE ON check_policies BEGIN SELECT RAISE(ABORT,'check policy is durable'); END;

CREATE TRIGGER check_route_validate_insert BEFORE INSERT ON check_routes BEGIN
 SELECT CASE WHEN NEW.status<>'active' OR NEW.revision<>1 OR NEW.created_at IS NOT NEW.updated_at OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM projects project JOIN agents agent ON agent.id=NEW.agent_id WHERE project.id=NEW.project_id AND project.workspace_id=NEW.workspace_id AND agent.workspace_id=NEW.workspace_id AND agent.enabled=1 AND agent.revision=NEW.agent_revision)
  OR (NEW.definition_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM check_definitions definition WHERE definition.id=NEW.definition_id AND definition.workspace_id=NEW.workspace_id AND definition.project_id=NEW.project_id AND definition.content_revision=NEW.definition_content_revision AND definition.status='active'))
  THEN RAISE(ABORT,'invalid exact check route') END;
END;

CREATE TRIGGER check_route_validate_update BEFORE UPDATE ON check_routes BEGIN
 SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.definition_id IS NOT OLD.definition_id OR NEW.definition_content_revision IS NOT OLD.definition_content_revision OR NEW.trigger IS NOT OLD.trigger OR NEW.duty IS NOT OLD.duty OR NEW.agent_id IS NOT OLD.agent_id OR NEW.agent_revision IS NOT OLD.agent_revision OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR OLD.status<>'active' OR NEW.status<>'retired' OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at) OR NEW.updated_by<>'local-owner' THEN RAISE(ABORT,'check routes are immutable except retirement') END;
END;

CREATE TRIGGER check_route_reject_delete BEFORE DELETE ON check_routes BEGIN SELECT RAISE(ABORT,'check routes are immutable'); END;

CREATE TRIGGER check_notification_validate_insert BEFORE INSERT ON check_notification_receipts BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS(
   SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id
   JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision
   JOIN agents recipient ON recipient.id=NEW.recipient_agent_id
   WHERE result.id=NEW.check_result_id AND run.workspace_id=NEW.workspace_id AND run.project_id=NEW.project_id AND run.task_id=NEW.task_id
     AND recipient.workspace_id=NEW.workspace_id AND recipient.enabled=1 AND recipient.revision=NEW.recipient_agent_revision
 ) THEN RAISE(ABORT,'invalid check notification source or recipient') END;
 SELECT CASE WHEN NEW.duty='task_owner' AND NOT EXISTS(
   SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id
   JOIN task_assignments assignment ON assignment.id=NEW.assignment_id JOIN agents recipient ON recipient.id=assignment.agent_id
   WHERE result.id=NEW.check_result_id AND result.outcome<>'passed' AND assignment.task_id=run.task_id
     AND assignment.status='active' AND assignment.revision=NEW.assignment_revision
     AND (crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(NEW.created_at)
       OR EXISTS(SELECT 1 FROM runs reserved WHERE reserved.assignment_id=assignment.id AND reserved.task_id=assignment.task_id
         AND reserved.agent_id=assignment.agent_id AND reserved.status IN ('requested','starting','active','blocked','stopping','lost')))
     AND recipient.id=NEW.recipient_agent_id AND recipient.revision=NEW.recipient_agent_revision AND recipient.enabled=1
 ) THEN RAISE(ABORT,'check task-owner notification lacks an exact current assignment') END;
 SELECT CASE WHEN NEW.duty IN ('evidence_review','coordination') AND NOT EXISTS(
   SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id
   JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision
   JOIN check_routes route ON route.id=NEW.route_id
   WHERE result.id=NEW.check_result_id AND route.workspace_id=NEW.workspace_id AND route.project_id=NEW.project_id
     AND route.status='active' AND route.duty=NEW.duty AND route.agent_id=NEW.recipient_agent_id AND route.agent_revision=NEW.recipient_agent_revision
     AND (route.definition_id IS NULL OR (route.definition_id=result.definition_id AND route.definition_content_revision=result.definition_content_revision))
     AND ((route.trigger='pass' AND result.outcome='passed') OR (route.trigger='nonpass' AND result.outcome<>'passed') OR (route.trigger='stale' AND freshness.status='stale'))
 ) THEN RAISE(ABORT,'check notification lacks an exact active route') END;
END;

CREATE TRIGGER check_notification_reject_update BEFORE UPDATE ON check_notification_receipts BEGIN SELECT RAISE(ABORT,'check notifications are immutable'); END;

CREATE TRIGGER check_notification_reject_delete BEFORE DELETE ON check_notification_receipts BEGIN SELECT RAISE(ABORT,'check notifications are immutable'); END;

CREATE TRIGGER check_route_failure_validate_insert BEFORE INSERT ON check_route_failures BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR crewfold_utf8_valid(NEW.diagnostic) IS NOT 1 OR NOT EXISTS(
   SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id
   JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision
   WHERE result.id=NEW.check_result_id AND run.workspace_id=NEW.workspace_id AND run.project_id=NEW.project_id AND run.task_id=NEW.task_id
 ) THEN RAISE(ABORT,'invalid check route failure source') END;
 SELECT CASE WHEN NEW.code='unroutable' AND (NEW.duty<>'task_owner' OR NEW.route_id IS NOT NULL OR NEW.recipient_agent_id IS NOT NULL OR NEW.assignment_id IS NOT NULL OR EXISTS(
   SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id JOIN task_assignments assignment ON assignment.task_id=run.task_id
   JOIN agents recipient ON recipient.id=assignment.agent_id
   WHERE result.id=NEW.check_result_id AND assignment.status='active' AND (crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(NEW.created_at)
     OR EXISTS(SELECT 1 FROM runs reserved WHERE reserved.assignment_id=assignment.id AND reserved.task_id=assignment.task_id
       AND reserved.agent_id=assignment.agent_id AND reserved.status IN ('requested','starting','active','blocked','stopping','lost')))
     AND recipient.enabled=1
 )) THEN RAISE(ABORT,'unroutable fact guessed or ignored a current task owner') END;
 SELECT CASE WHEN NEW.code='recipient_unavailable' AND (NEW.route_id IS NULL OR NEW.duty NOT IN ('evidence_review','coordination') OR NEW.recipient_agent_id IS NULL OR NOT EXISTS(
   SELECT 1 FROM check_routes route JOIN check_results result ON result.id=NEW.check_result_id
   JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision
   WHERE route.id=NEW.route_id AND route.status='active' AND route.workspace_id=NEW.workspace_id AND route.project_id=NEW.project_id
     AND route.duty=NEW.duty AND route.agent_id=NEW.recipient_agent_id AND route.agent_revision=NEW.recipient_agent_revision
     AND (route.definition_id IS NULL OR (route.definition_id=result.definition_id AND route.definition_content_revision=result.definition_content_revision))
     AND ((route.trigger='pass' AND result.outcome='passed') OR (route.trigger='nonpass' AND result.outcome<>'passed') OR (route.trigger='stale' AND freshness.status='stale'))
 ) OR EXISTS(SELECT 1 FROM agents recipient WHERE recipient.id=NEW.recipient_agent_id AND recipient.workspace_id=NEW.workspace_id AND recipient.enabled=1 AND recipient.revision=NEW.recipient_agent_revision))
 THEN RAISE(ABORT,'route failure does not prove an unavailable exact recipient') END;
END;

CREATE TRIGGER check_route_failure_reject_update BEFORE UPDATE ON check_route_failures BEGIN SELECT RAISE(ABORT,'check route failures are immutable'); END;

CREATE TRIGGER check_route_failure_reject_delete BEFORE DELETE ON check_route_failures BEGIN SELECT RAISE(ABORT,'check route failures are immutable'); END;

CREATE TRIGGER check_subsystem_message_validate_insert BEFORE INSERT ON messages WHEN NEW.sender_type='subsystem' BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR NEW.sender_id<>'crewfold-check-worker' OR NEW.sender_agent_id IS NOT NULL OR NEW.sender_run_id IS NOT NULL
   OR NEW.kind<>'inform' OR json_type(NEW.artifact_ids_json)<>'array' OR json_array_length(NEW.artifact_ids_json)<>0 OR NEW.reply_to_message_id IS NOT NULL
   OR NOT EXISTS(SELECT 1 FROM check_notification_receipts receipt JOIN message_threads thread ON thread.id=NEW.thread_id
     WHERE receipt.message_id=NEW.id AND receipt.workspace_id=NEW.workspace_id AND receipt.project_id=NEW.project_id AND receipt.task_id=NEW.task_id
       AND thread.workspace_id=NEW.workspace_id AND thread.project_id=NEW.project_id AND thread.task_id=NEW.task_id AND thread.kind='direct' AND thread.status='open')
 THEN RAISE(ABORT,'subsystem message lacks exact check notification provenance') END;
END;

CREATE TRIGGER check_subsystem_recipient_validate_insert BEFORE INSERT ON message_recipients
WHEN (SELECT sender_type FROM messages WHERE id=NEW.message_id)='subsystem' BEGIN
 SELECT CASE WHEN NEW.status<>'queued' OR NEW.recipient_participant_id IS NOT NULL OR NOT EXISTS(
   SELECT 1 FROM check_notification_receipts receipt WHERE receipt.message_id=NEW.message_id AND receipt.recipient_agent_id=NEW.recipient_agent_id AND receipt.created_at=NEW.queued_at
 ) THEN RAISE(ABORT,'subsystem recipient differs from check notification receipt') END;
END;

CREATE TRIGGER check_repair_proposal_validate_insert BEFORE INSERT ON check_repair_proposals BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR NEW.status<>'pending' OR NEW.revision<>1 OR NEW.created_at IS NOT NEW.updated_at OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR crewfold_utf8_valid(NEW.rationale) IS NOT 1 OR crewfold_utf8_valid(NEW.repair_task_title) IS NOT 1 OR crewfold_utf8_valid(NEW.repair_task_description) IS NOT 1
  OR NEW.created_by IS NOT ('agent:'||NEW.source_agent_id) OR NEW.updated_by IS NOT NEW.created_by
  OR NOT EXISTS(SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id
    JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision
    JOIN task_check_requirements requirement ON requirement.id=result.requirement_id
    JOIN tasks task ON task.id=run.task_id JOIN objectives objective ON objective.id=task.objective_id
    JOIN check_policies policy ON policy.workspace_id=run.workspace_id AND policy.project_id=run.project_id
    JOIN launch_profiles profile ON profile.id=policy.repair_launch_profile_id
    JOIN agents repair_agent ON repair_agent.id=profile.agent_id
    WHERE result.id=NEW.check_result_id AND result.outcome='failed' AND freshness.status='fresh'
      AND freshness.revision=(SELECT MAX(current.revision) FROM check_result_freshness current WHERE current.check_result_id=result.id)
      AND NOT EXISTS(SELECT 1 FROM check_results newer WHERE newer.requirement_id=result.requirement_id AND newer.requirement_revision=result.requirement_revision AND (newer.created_at>result.created_at OR (newer.created_at=result.created_at AND newer.id>result.id)))
      AND run.workspace_id=NEW.workspace_id AND run.project_id=NEW.project_id
      AND requirement.status='active' AND requirement.revision=NEW.requirement_revision AND requirement.task_id=run.task_id
      AND task.id=NEW.task_id AND task.revision=NEW.task_revision AND task.status NOT IN ('cancelled','completed')
      AND objective.id=NEW.objective_id AND objective.revision=NEW.objective_revision AND objective.status='active'
      AND result.requirement_id=NEW.requirement_id AND result.requirement_revision=NEW.requirement_revision
      AND result.repository_id=NEW.source_repository_id AND result.checkout_id=NEW.source_checkout_id AND result.head_commit=NEW.source_head_commit
      AND policy.repair_proposals_enabled=1 AND policy.revision=NEW.policy_revision
      AND profile.id=NEW.repair_launch_profile_id AND profile.revision=NEW.repair_launch_profile_revision AND profile.status='active' AND profile.manager_grant_id IS NULL
      AND repair_agent.enabled=1 AND repair_agent.revision=profile.agent_revision)
  OR NOT EXISTS(SELECT 1 FROM runs source_run JOIN run_capabilities capability ON capability.run_id=source_run.id
    JOIN run_context_bindings binding ON binding.run_id=source_run.id JOIN context_packets packet ON packet.id=binding.context_packet_id
    JOIN agents source_agent ON source_agent.id=source_run.agent_id JOIN check_watch_grants grant_row ON grant_row.id=NEW.source_grant_id
    JOIN check_watch_grant_operations operation ON operation.grant_id=grant_row.id AND operation.operation='propose_repair'
    JOIN check_watch_grant_definitions allowed ON allowed.grant_id=grant_row.id
    JOIN check_results result ON result.id=NEW.check_result_id JOIN check_runs checked_run ON checked_run.id=result.check_run_id
    WHERE source_run.id=NEW.source_run_id AND source_run.workspace_id=NEW.workspace_id AND source_run.project_id=NEW.project_id
      AND source_run.agent_id=NEW.source_agent_id AND source_run.status IN ('starting','active','blocked')
      AND source_agent.id=NEW.source_agent_id AND source_agent.enabled=1 AND source_agent.revision=NEW.source_agent_revision
      AND crewfold_timestamp_key(capability.expires_at)>crewfold_timestamp_key(NEW.created_at)
      AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1'
      AND json_extract(packet.packet_json,'$.check_watch_grant.grant_id')=grant_row.id AND json_extract(packet.packet_json,'$.check_watch_grant.grant_revision')=grant_row.revision
      AND grant_row.revision=NEW.source_grant_revision AND grant_row.status='active' AND grant_row.workspace_id=NEW.workspace_id AND grant_row.project_id=NEW.project_id
      AND grant_row.agent_id=NEW.source_agent_id AND grant_row.agent_revision=NEW.source_agent_revision
      AND (grant_row.expires_at IS NULL OR crewfold_timestamp_key(grant_row.expires_at)>crewfold_timestamp_key(NEW.created_at))
      AND allowed.definition_id=checked_run.definition_id AND allowed.definition_content_revision=checked_run.definition_content_revision AND allowed.definition_sha256=checked_run.definition_sha256)
 THEN RAISE(ABORT,'invalid inert check repair proposal') END;
END;

CREATE TRIGGER check_repair_proposal_validate_update BEFORE UPDATE ON check_repair_proposals BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id
  OR NEW.objective_id IS NOT OLD.objective_id OR NEW.objective_revision IS NOT OLD.objective_revision OR NEW.task_id IS NOT OLD.task_id OR NEW.task_revision IS NOT OLD.task_revision
  OR NEW.requirement_id IS NOT OLD.requirement_id OR NEW.requirement_revision IS NOT OLD.requirement_revision OR NEW.check_result_id IS NOT OLD.check_result_id OR NEW.freshness_revision IS NOT OLD.freshness_revision
  OR NEW.source_repository_id IS NOT OLD.source_repository_id OR NEW.source_checkout_id IS NOT OLD.source_checkout_id OR NEW.source_head_commit IS NOT OLD.source_head_commit
  OR NEW.policy_revision IS NOT OLD.policy_revision OR NEW.repair_launch_profile_id IS NOT OLD.repair_launch_profile_id OR NEW.repair_launch_profile_revision IS NOT OLD.repair_launch_profile_revision
  OR NEW.source_run_id IS NOT OLD.source_run_id OR NEW.source_agent_id IS NOT OLD.source_agent_id OR NEW.source_agent_revision IS NOT OLD.source_agent_revision
  OR NEW.source_grant_id IS NOT OLD.source_grant_id OR NEW.source_grant_revision IS NOT OLD.source_grant_revision OR NEW.rationale IS NOT OLD.rationale
  OR NEW.repair_task_title IS NOT OLD.repair_task_title OR NEW.repair_task_description IS NOT OLD.repair_task_description OR NEW.repair_task_priority IS NOT OLD.repair_task_priority
  OR NEW.repair_budget_tokens IS NOT OLD.repair_budget_tokens OR NEW.repair_budget_cost_cents IS NOT OLD.repair_budget_cost_cents OR NEW.repair_budget_time_seconds IS NOT OLD.repair_budget_time_seconds
  OR NEW.recipe_sha256 IS NOT OLD.recipe_sha256 OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR OLD.status<>'pending'
  OR NEW.status NOT IN ('accepted','rejected','stale') OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
  OR (NEW.status IN ('accepted','rejected') AND NEW.updated_by<>'local-owner') OR (NEW.status='stale' AND NEW.updated_by<>'crewfold-check-worker')
  OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at)
 THEN RAISE(ABORT,'check repair proposals are immutable except one terminal decision') END;
END;

CREATE TRIGGER check_repair_proposal_reject_delete BEFORE DELETE ON check_repair_proposals BEGIN SELECT RAISE(ABORT,'check repair proposals are durable'); END;

CREATE TRIGGER check_repair_decision_validate_insert BEFORE INSERT ON check_repair_decisions BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR (NEW.note IS NOT NULL AND crewfold_utf8_valid(NEW.note) IS NOT 1) OR NOT EXISTS(
  SELECT 1 FROM check_repair_proposals proposal WHERE proposal.id=NEW.repair_proposal_id AND proposal.status=NEW.decision AND proposal.revision=NEW.proposal_revision+1 AND proposal.updated_at=NEW.created_at AND proposal.updated_by='local-owner'
 ) THEN RAISE(ABORT,'check repair decision lacks exact owner transition') END;
END;

CREATE TRIGGER check_repair_decision_reject_update BEFORE UPDATE ON check_repair_decisions BEGIN SELECT RAISE(ABORT,'check repair decisions are immutable'); END;

CREATE TRIGGER check_repair_decision_reject_delete BEFORE DELETE ON check_repair_decisions BEGIN SELECT RAISE(ABORT,'check repair decisions are immutable'); END;

CREATE TRIGGER check_repair_effect_reject_update BEFORE UPDATE ON check_repair_effects BEGIN SELECT RAISE(ABORT,'check repair effects are immutable'); END;

CREATE TRIGGER check_repair_effect_reject_delete BEFORE DELETE ON check_repair_effects BEGIN SELECT RAISE(ABORT,'check repair effects are immutable'); END;

CREATE TRIGGER check_repair_effect_validate_insert BEFORE INSERT ON check_repair_effects BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS(
  SELECT 1 FROM check_repair_proposals proposal JOIN check_repair_decisions decision ON decision.repair_proposal_id=proposal.id
  JOIN objectives objective ON objective.id=proposal.objective_id
  JOIN launch_profiles profile ON profile.id=proposal.repair_launch_profile_id JOIN agents agent ON agent.id=profile.agent_id
  JOIN tasks task ON task.id=NEW.repair_task_id JOIN scheduling_intents intent ON intent.id=NEW.scheduling_intent_id
  WHERE proposal.id=NEW.repair_proposal_id AND proposal.status='accepted' AND proposal.revision=decision.proposal_revision+1
    AND proposal.updated_at=NEW.created_at AND proposal.updated_by='local-owner'
    AND decision.decision='accepted' AND decision.created_at=NEW.created_at AND decision.created_by='local-owner'
    AND task.workspace_id=proposal.workspace_id AND task.project_id=proposal.project_id AND task.objective_id=proposal.objective_id
    AND task.title=proposal.repair_task_title AND task.description=proposal.repair_task_description AND task.priority=proposal.repair_task_priority
    AND task.budget_tokens=proposal.repair_budget_tokens AND task.budget_cost_cents=proposal.repair_budget_cost_cents AND task.budget_time_seconds=proposal.repair_budget_time_seconds
    AND task.status='ready' AND task.revision=1 AND task.created_at=NEW.created_at AND task.updated_at=NEW.created_at AND task.created_by='local-owner' AND task.updated_by='local-owner'
    AND objective.workspace_id=proposal.workspace_id AND objective.project_id=proposal.project_id AND objective.status='active' AND objective.revision=proposal.objective_revision
    AND (objective.budget_tokens=0 OR (NOT EXISTS(SELECT 1 FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled' AND allocated.budget_tokens=0)
      AND (SELECT COALESCE(SUM(allocated.budget_tokens),0) FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled')<=objective.budget_tokens))
    AND (objective.budget_cost_cents=0 OR (NOT EXISTS(SELECT 1 FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled' AND allocated.budget_cost_cents=0)
      AND (SELECT COALESCE(SUM(allocated.budget_cost_cents),0) FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled')<=objective.budget_cost_cents))
    AND (objective.budget_time_seconds=0 OR (NOT EXISTS(SELECT 1 FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled' AND allocated.budget_time_seconds=0)
      AND (SELECT COALESCE(SUM(allocated.budget_time_seconds),0) FROM tasks allocated WHERE allocated.objective_id=objective.id AND allocated.status<>'cancelled')<=objective.budget_time_seconds))
    AND intent.workspace_id=proposal.workspace_id AND intent.project_id=proposal.project_id AND intent.objective_id=proposal.objective_id AND intent.task_id=task.id
    AND intent.source_proposal_id IS NULL AND intent.source_action_id IS NULL AND intent.source_check_repair_proposal_id=proposal.id
    AND intent.launch_profile_id=profile.id AND intent.agent_id=profile.agent_id AND intent.status='pending' AND intent.revision=1
    AND intent.created_at=NEW.created_at AND intent.updated_at=NEW.created_at AND intent.created_by='local-owner' AND intent.updated_by='local-owner'
    AND profile.revision=proposal.repair_launch_profile_revision AND profile.workspace_id=proposal.workspace_id AND profile.project_id=proposal.project_id
    AND profile.status='active' AND profile.manager_grant_id IS NULL AND agent.enabled=1 AND agent.revision=profile.agent_revision
    AND EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=proposal.workspace_id AND event.entity_type='task' AND event.entity_id=task.id
      AND event.entity_revision=1 AND event.type='task.created' AND event.occurred_at=NEW.created_at AND event.recorded_at=NEW.created_at
      AND event.actor_id='local-owner' AND event.actor_type='human' AND json_extract(event.data_json,'$.source_check_repair_proposal_id')=proposal.id)
    AND EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=proposal.workspace_id AND event.entity_type='scheduling_intent' AND event.entity_id=intent.id
      AND event.entity_revision=1 AND event.type='supervisor.intent_created' AND event.occurred_at=NEW.created_at AND event.recorded_at=NEW.created_at
      AND event.actor_id='local-owner' AND event.actor_type='human' AND json_extract(event.data_json,'$.source_check_repair_proposal_id')=proposal.id)
 ) THEN RAISE(ABORT,'check repair effect lacks exact accepted task and intent') END;
END;

CREATE TRIGGER manager_proposal_effect_validate_insert
BEFORE INSERT ON manager_proposal_effects
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR EXISTS (SELECT 1 FROM manager_proposal_decisions decision WHERE decision.proposal_id=NEW.proposal_id)
      OR NOT EXISTS (
      SELECT 1 FROM manager_proposals proposal
      JOIN manager_proposal_actions action ON action.proposal_id = proposal.id
      JOIN events event ON event.sequence = NEW.event_sequence
      WHERE proposal.id = NEW.proposal_id AND proposal.status = 'accepted'
        AND proposal.workspace_id = NEW.workspace_id AND proposal.project_id = NEW.project_id
        AND proposal.objective_id = NEW.objective_id AND action.id = NEW.action_id
        AND event.workspace_id = NEW.workspace_id AND event.entity_type = NEW.entity_type
        AND event.entity_id = NEW.entity_id AND event.actor_id = 'local-owner'
        AND (
          (action.type IN ('create_task','request_review') AND NEW.effect_type='created'
            AND NEW.entity_type='task' AND event.type='task.created')
          OR (action.type IN ('create_task','assign_task','request_review') AND NEW.effect_type='created'
            AND NEW.entity_type='scheduling_intent' AND event.type='supervisor.intent_created'
            AND EXISTS (SELECT 1 FROM scheduling_intents intent WHERE intent.id=NEW.entity_id
              AND intent.source_proposal_id=proposal.id AND intent.source_action_id=action.id))
          OR (action.type='declare_claim_requirement' AND NEW.effect_type='created'
            AND NEW.entity_type='task_claim_requirement' AND event.type='task.claim_requirement_created'
            AND EXISTS (SELECT 1 FROM task_claim_requirements requirement WHERE requirement.id=NEW.entity_id
              AND requirement.source_proposal_id=proposal.id AND requirement.source_action_id=action.id))
          OR (action.type IN ('add_dependency','request_review') AND NEW.effect_type='dependency_added'
            AND NEW.entity_type='task' AND event.type='task.dependency_added')
          OR (action.type='request_action' AND NEW.effect_type='created'
            AND NEW.entity_type='supervisor_action' AND event.type='supervisor.action_recorded'
            AND EXISTS (SELECT 1 FROM supervisor_actions supervisor WHERE supervisor.id=NEW.entity_id
              AND supervisor.source_proposal_id=proposal.id AND supervisor.source_action_id=action.id
              AND EXISTS (SELECT 1 FROM supervisor_action_receipts receipt WHERE receipt.action_id=supervisor.id
                AND receipt.condition_key=supervisor.condition_key)))
          OR (action.type='request_action' AND NEW.effect_type='created'
            AND NEW.entity_type='approval_request' AND event.type='approval.requested'
            AND EXISTS (SELECT 1 FROM approval_requests approval JOIN supervisor_actions supervisor ON supervisor.id=approval.action_id
              JOIN supervisor_action_receipts receipt ON receipt.action_id=supervisor.id
              WHERE approval.id=NEW.entity_id AND supervisor.source_proposal_id=proposal.id AND supervisor.source_action_id=action.id
                AND receipt.condition_key=supervisor.condition_key))
        )
    ) THEN RAISE(ABORT, 'manager proposal effect lacks exact accepted authority') END;
END;

CREATE TRIGGER supervisor_action_validate_insert
BEFORE INSERT ON supervisor_actions
BEGIN
    SELECT CASE WHEN NEW.revision <> 1 OR NEW.created_at IS NOT NEW.updated_at
      OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR NEW.updated_by <> 'subsystem:supervisor'
      OR (NEW.status = 'applied' AND NEW.applied_at IS NOT NEW.created_at)
      OR (NEW.status <> 'applied' AND NEW.applied_at IS NOT NULL)
      OR lower(hex(sha256(CAST(json_object(
        'condition_key',NEW.condition_key,'condition',NEW.condition,'response',NEW.response,
        'entity_revision',NEW.entity_revision,'policy_revision',NEW.policy_revision,
        'as_of_event_sequence',NEW.as_of_event_sequence,'reasons',json(NEW.reasons_json),
        'constraints',json(NEW.constraint_snapshot_json)
      ) AS BLOB)))) <> NEW.content_sha256
      OR NOT EXISTS (
        SELECT 1 FROM supervisor_policies policy
        WHERE policy.workspace_id = NEW.workspace_id AND policy.revision = NEW.policy_revision
          AND (NEW.condition='manager_escalation' OR policy.enabled = 1) AND policy.revision = (
            SELECT MAX(current.revision) FROM supervisor_policies current WHERE current.workspace_id=NEW.workspace_id
          )
      )
      OR (NEW.intent_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM scheduling_intents intent WHERE intent.id = NEW.intent_id
          AND intent.workspace_id = NEW.workspace_id AND intent.project_id = NEW.project_id
          AND intent.objective_id = NEW.objective_id AND intent.task_id = NEW.task_id
          AND intent.agent_id = NEW.agent_id
      ))
      OR (NEW.run_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM runs run WHERE run.id = NEW.run_id AND run.workspace_id = NEW.workspace_id
          AND run.project_id = NEW.project_id AND run.task_id = NEW.task_id
          AND run.agent_id = NEW.agent_id
          AND (NEW.intent_id IS NOT NULL OR NEW.response='retry_task' OR run.revision = NEW.entity_revision)
      ))
	  OR (NEW.condition<>'manager_escalation' AND NEW.response<>'retry_task' AND NEW.prior_run_id IS NOT NULL)
	  OR (NEW.condition<>'manager_escalation' AND NEW.response='retry_task' AND (NEW.prior_run_id IS NULL OR NEW.run_id IS NULL OR NEW.prior_run_id=NEW.run_id))
	  OR (NEW.condition<>'manager_escalation' AND (NEW.source_proposal_id IS NOT NULL OR NEW.source_action_id IS NOT NULL))
	  OR (NEW.condition='manager_escalation' AND (NEW.source_proposal_id IS NULL OR NEW.source_action_id IS NULL))
	  OR (NEW.condition='manager_escalation' AND NOT EXISTS (
	    SELECT 1 FROM manager_proposals proposal
	    JOIN manager_proposal_actions source ON source.proposal_id=proposal.id
	    JOIN tasks task ON task.id=NEW.task_id
	    WHERE proposal.id=NEW.source_proposal_id AND proposal.status='accepted'
	      AND proposal.workspace_id=NEW.workspace_id AND proposal.project_id=NEW.project_id
	      AND proposal.objective_id=NEW.objective_id
	      AND source.id=NEW.source_action_id AND source.type='request_action'
	      AND json_extract(source.payload_json,'$.response')=NEW.response
	      AND json_extract(source.payload_json,'$.expected_revision')=NEW.entity_revision
	      AND json_extract(source.payload_json,'$.reason')=json_extract(NEW.reasons_json,'$[0]')
	      AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id
	      AND task.objective_id=NEW.objective_id
	      AND (
	        (NEW.response IN ('resume_run','stop_run')
	          AND json_extract(source.payload_json,'$.target_run_id')=NEW.run_id
	          AND json_extract(source.payload_json,'$.target_task_id') IS NULL
	          AND json_extract(source.payload_json,'$.launch_profile_id') IS NULL
	          AND EXISTS (
	            SELECT 1 FROM runs target JOIN run_jobs job ON job.run_id=target.id
	            WHERE target.id=NEW.run_id AND target.task_id=NEW.task_id
	              AND target.revision=NEW.entity_revision
	              AND ((NEW.response='resume_run' AND target.status='blocked' AND job.status='complete')
	                OR (NEW.response='stop_run' AND target.status IN ('active','blocked')))
	          ))
	        OR (NEW.response='retry_task'
	          AND json_extract(source.payload_json,'$.target_task_id')=NEW.task_id
	          AND json_extract(source.payload_json,'$.target_run_id') IS NULL
	          AND json_extract(source.payload_json,'$.launch_profile_id') IS NULL
	          AND NEW.run_id IS NULL AND NEW.prior_run_id IS NOT NULL
	          AND task.status='assigned' AND task.revision=NEW.entity_revision
	          AND EXISTS (
	            SELECT 1 FROM runs prior JOIN run_jobs job ON job.run_id=prior.id
	            WHERE prior.id=NEW.prior_run_id AND prior.task_id=NEW.task_id
	              AND prior.status='start_failed' AND prior.step_cursor=0 AND job.status='complete'
	              AND prior.assignment_id=(SELECT id FROM task_assignments assignment
	                WHERE assignment.task_id=task.id AND assignment.status='active')
	              AND prior.id=(SELECT latest.id FROM runs latest WHERE latest.task_id=task.id
	                AND latest.status='start_failed' AND latest.step_cursor=0
	                ORDER BY latest.created_at DESC,latest.id DESC LIMIT 1)
	          ))
	        OR (NEW.response='reassign_task'
	          AND json_extract(source.payload_json,'$.target_task_id')=NEW.task_id
	          AND json_extract(source.payload_json,'$.target_run_id') IS NULL
	          AND task.status IN ('ready','blocked','failed','changes_requested') AND task.revision=NEW.entity_revision
	          AND json_extract(source.payload_json,'$.launch_profile_id')=json_extract(NEW.constraint_snapshot_json,'$.launch_profile_id')
	          AND EXISTS (
	            SELECT 1 FROM launch_profiles profile JOIN agents agent ON agent.id=profile.agent_id
	            WHERE profile.id=json_extract(source.payload_json,'$.launch_profile_id')
	              AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id
	              AND profile.status='active' AND profile.manager_grant_id IS NULL
	              AND profile.revision=json_extract(NEW.constraint_snapshot_json,'$.launch_profile_revision')
	              AND agent.enabled=1 AND agent.revision=profile.agent_revision
	              AND agent.runtime=profile.runtime AND agent.provider=profile.provider
	          )
	          AND NOT EXISTS (SELECT 1 FROM runs reserved WHERE reserved.task_id=task.id
	            AND reserved.status IN ('requested','starting','active','blocked','stopping','lost'))
	          AND NOT EXISTS (SELECT 1 FROM scheduling_intents open WHERE open.task_id=task.id
	            AND open.status IN ('pending','deferred','awaiting_approval','run_requested')))
	      )
	  ))
      OR NEW.condition_key <> lower(hex(sha256(CAST(json_object(
        'condition',NEW.condition,'response',NEW.response,
        'intent_id',COALESCE(NEW.intent_id,''),'task_id',COALESCE(NEW.task_id,''),
        'run_id',COALESCE(NEW.run_id,''),'prior_run_id',COALESCE(NEW.prior_run_id,''),
        'source_proposal_id',COALESCE(NEW.source_proposal_id,''),
        'source_action_id',COALESCE(NEW.source_action_id,''),
        'entity_revision',NEW.entity_revision
      ) AS BLOB))))
      OR (NEW.condition = 'dependency_ready' AND (NEW.intent_id IS NULL OR NEW.task_id IS NULL OR NEW.response <> 'schedule'))
      OR (NEW.condition = 'blocked' AND (NEW.run_id IS NULL OR NEW.task_id IS NULL OR NEW.response <> 'resume_run'))
      OR (NEW.condition = 'stale' AND NEW.intent_id IS NULL AND NEW.task_id IS NULL AND NEW.run_id IS NULL)
      OR (NEW.condition IN ('failed','repeated_failure') AND NEW.task_id IS NULL AND NEW.run_id IS NULL)
      OR (NEW.condition = 'over_budget' AND NEW.task_id IS NULL AND NEW.run_id IS NULL)
	  OR (NEW.condition = 'manager_escalation' AND NEW.status<>'awaiting_approval')
      OR (NEW.response = 'schedule' AND NEW.status NOT IN ('applied','deferred'))
      OR (NEW.response = 'retry_task' AND NEW.condition<>'manager_escalation' AND NEW.status <> 'applied')
      OR (NEW.response NOT IN ('schedule','retry_task') AND NEW.status NOT IN ('proposed','awaiting_approval','deferred'))
      OR (NEW.response = 'schedule' AND NEW.status = 'applied' AND NOT EXISTS (
        SELECT 1 FROM supervisor_policies policy WHERE policy.workspace_id=NEW.workspace_id
          AND policy.revision=NEW.policy_revision AND policy.auto_schedule=1
      ))
      OR (NEW.response = 'retry_task' AND NEW.condition<>'manager_escalation' AND NOT EXISTS (
        SELECT 1 FROM runs prior
        JOIN runs fresh ON fresh.id=NEW.run_id
        JOIN run_jobs prior_job ON prior_job.run_id=prior.id
        JOIN run_jobs fresh_job ON fresh_job.run_id=fresh.id
        JOIN launch_profiles profile ON profile.id=COALESCE(
          (SELECT launch_profile_id FROM run_scheduling_receipts WHERE run_id=prior.id),
          (SELECT launch_profile_id FROM run_retry_receipts WHERE run_id=prior.id)
        )
        JOIN task_assignments assignment ON assignment.id=prior.assignment_id
        JOIN agents agent ON agent.id=prior.agent_id
        JOIN supervisor_policies policy ON policy.workspace_id=NEW.workspace_id
          AND policy.revision=NEW.policy_revision
        WHERE prior.id=NEW.prior_run_id AND prior.workspace_id=NEW.workspace_id
          AND prior.status='start_failed' AND prior.step_cursor=0 AND prior.revision=NEW.entity_revision
          AND prior.task_id=NEW.task_id AND prior.agent_id=NEW.agent_id
          AND fresh.workspace_id=prior.workspace_id AND fresh.project_id=prior.project_id
          AND fresh.task_id=prior.task_id AND fresh.agent_id=prior.agent_id
          AND fresh.assignment_id=prior.assignment_id AND fresh.checkout_id=prior.checkout_id
          AND fresh.runtime=prior.runtime AND fresh.provider=prior.provider
          AND fresh.status='requested' AND fresh.revision=1 AND fresh.step_cursor=0
          AND prior_job.status='complete' AND prior_job.origin='supervisor'
          AND fresh_job.status='pending' AND fresh_job.origin='supervisor'
          AND assignment.status='active'
          AND crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(NEW.created_at)
          AND assignment.task_id=prior.task_id AND assignment.agent_id=prior.agent_id
          AND profile.revision=COALESCE(
            (SELECT launch_profile_revision FROM run_scheduling_receipts WHERE run_id=prior.id),
            (SELECT launch_profile_revision FROM run_retry_receipts WHERE run_id=prior.id)
          ) AND profile.status='active'
          AND profile.agent_id=prior.agent_id AND profile.runtime=prior.runtime AND profile.provider=prior.provider
          AND profile.agent_revision=agent.revision AND agent.enabled=1
          AND agent.runtime=profile.runtime AND agent.provider=profile.provider
          AND profile.manager_grant_id IS NULL
          AND (profile.checkout_id IS NULL OR profile.checkout_id=prior.checkout_id)
          AND EXISTS (
            SELECT 1 FROM checkouts checkout WHERE checkout.id=prior.checkout_id
              AND checkout.project_id=prior.project_id AND checkout.availability='available'
              AND checkout.write_mode<>'read_only' AND (
                checkout.write_mode='shared' OR NOT EXISTS (
                  SELECT 1 FROM runs occupant WHERE occupant.checkout_id=prior.checkout_id
                    AND occupant.id NOT IN (prior.id,fresh.id)
                    AND occupant.status IN ('requested','starting','active','blocked','stopping','lost')
                )
              )
          )
          AND policy.enabled=1 AND policy.auto_retry_limit BETWEEN 1 AND 3
		  AND NOT EXISTS (
		    SELECT 1 FROM task_claim_requirements requirement WHERE requirement.task_id=prior.task_id
		      AND NOT EXISTS (
		        SELECT 1 FROM work_claims claim WHERE claim.task_id=requirement.task_id
		          AND claim.status='active' AND crewfold_timestamp_key(claim.lease_expires_at)>crewfold_timestamp_key(NEW.created_at)
		          AND claim.kind=requirement.kind AND claim.target=requirement.target
		          AND claim.mode=requirement.mode AND claim.conflict_policy=requirement.conflict_policy
		      )
		  )
		  AND (SELECT COUNT(*) FROM run_retry_receipts retry WHERE retry.intent_id=COALESCE(
		    (SELECT intent_id FROM run_scheduling_receipts WHERE run_id=prior.id),
		    (SELECT intent_id FROM run_retry_receipts WHERE run_id=prior.id)
		  )) < policy.auto_retry_limit
		  AND crewfold_timestamp_elapsed_seconds(NEW.created_at,prior.updated_at) >= policy.retry_cooldown_seconds
      ))
      OR (NEW.status = 'awaiting_approval' AND NEW.approval_id IS NULL)
      OR (NEW.status <> 'awaiting_approval' AND NEW.approval_id IS NOT NULL)
      THEN RAISE(ABORT, 'supervisor action lacks exact condition authority') END;
END;

CREATE TRIGGER run_scheduling_receipt_validate_insert
BEFORE INSERT ON run_scheduling_receipts
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS (
      SELECT 1 FROM runs run
      JOIN run_jobs job ON job.run_id = run.id
      JOIN scheduling_intents intent ON intent.id = NEW.intent_id
      JOIN supervisor_actions action ON action.id = NEW.action_id
      JOIN supervisor_action_receipts action_receipt ON action_receipt.action_id=action.id
      JOIN launch_profiles profile ON profile.id = NEW.launch_profile_id
      JOIN task_assignments assignment ON assignment.id = NEW.assignment_id
      JOIN tasks task ON task.id = run.task_id
      JOIN agents agent ON agent.id = run.agent_id
      JOIN supervisor_policies policy ON policy.workspace_id = NEW.workspace_id
        AND policy.revision = NEW.policy_revision
      WHERE run.id = NEW.run_id AND run.workspace_id = NEW.workspace_id
        AND run.assignment_id = NEW.assignment_id AND run.task_id = intent.task_id
        AND run.agent_id = intent.agent_id AND run.created_at = NEW.created_at
        AND job.origin = 'supervisor'
        AND assignment.task_id = run.task_id AND assignment.agent_id = run.agent_id
        AND assignment.status = 'active' AND intent.workspace_id = NEW.workspace_id
        AND intent.launch_profile_id = profile.id AND intent.assignment_id = assignment.id
        AND intent.run_id = run.id AND intent.supervisor_action_id = action.id
        AND intent.status = 'run_requested' AND intent.revision > 1
        AND action.intent_id = intent.id AND action.run_id = run.id AND action.response = 'schedule'
        AND action.status = 'applied' AND action.policy_revision = policy.revision
        AND action_receipt.condition_key=action.condition_key AND action_receipt.recorded_status='applied'
        AND action.applied_at = NEW.created_at
        AND profile.revision = NEW.launch_profile_revision
        AND profile.agent_id = run.agent_id AND profile.status = 'active'
        AND profile.agent_revision = agent.revision AND agent.enabled = 1
        AND profile.runtime = run.runtime AND profile.provider = run.provider
        AND (profile.checkout_id IS NULL OR profile.checkout_id = run.checkout_id)
        AND policy.enabled = 1 AND policy.auto_schedule = 1
        AND policy.revision = (SELECT MAX(current.revision) FROM supervisor_policies current WHERE current.workspace_id=NEW.workspace_id)
        AND task.revision = NEW.task_revision
        AND EXISTS (
          SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
            AND event.entity_type='supervisor_action' AND event.entity_id=action.id
            AND event.entity_revision=1 AND event.type='supervisor.action_applied'
            AND event.occurred_at=NEW.created_at AND event.actor_id='subsystem:supervisor'
        )
        AND EXISTS (
          SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
            AND event.entity_type='run' AND event.entity_id=run.id
            AND event.entity_revision=1 AND event.type='run.requested'
            AND event.occurred_at=NEW.created_at AND event.actor_id='subsystem:supervisor'
        )
    ) THEN RAISE(ABORT, 'run scheduling receipt links are not exact') END;
END;

CREATE TRIGGER run_retry_receipt_validate_insert
BEFORE INSERT ON run_retry_receipts
BEGIN
    SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS (
      SELECT 1 FROM runs fresh
      JOIN runs prior ON prior.id=NEW.prior_run_id
      JOIN run_jobs fresh_job ON fresh_job.run_id=fresh.id
      JOIN run_jobs prior_job ON prior_job.run_id=prior.id
      JOIN scheduling_intents intent ON intent.id=NEW.intent_id
      JOIN supervisor_actions action ON action.id=NEW.action_id
      JOIN supervisor_action_receipts action_receipt ON action_receipt.action_id=action.id
      JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id
      JOIN agents agent ON agent.id=fresh.agent_id
      JOIN task_assignments assignment ON assignment.id=NEW.assignment_id
      JOIN run_context_bindings binding ON binding.run_id=fresh.id
      JOIN context_packets packet ON packet.id=binding.context_packet_id
      JOIN supervisor_policies policy ON policy.workspace_id=NEW.workspace_id AND policy.revision=NEW.policy_revision
      WHERE fresh.id=NEW.run_id AND fresh.workspace_id=NEW.workspace_id
        AND fresh.status='requested' AND fresh.revision=1 AND fresh.step_cursor=0
        AND fresh.created_at=NEW.created_at AND fresh_job.status='pending' AND fresh_job.origin='supervisor'
        AND prior.workspace_id=fresh.workspace_id AND prior.project_id=fresh.project_id
        AND prior.task_id=fresh.task_id AND prior.agent_id=fresh.agent_id
        AND prior.checkout_id=fresh.checkout_id AND prior.runtime=fresh.runtime AND prior.provider=fresh.provider
        AND prior.assignment_id=fresh.assignment_id AND prior.status='start_failed' AND prior.step_cursor=0
        AND prior_job.status='complete' AND prior_job.origin='supervisor'
        AND intent.workspace_id=NEW.workspace_id AND intent.task_id=fresh.task_id
        AND intent.agent_id=fresh.agent_id AND intent.launch_profile_id=profile.id
        AND intent.status='run_requested'
        AND action.workspace_id=NEW.workspace_id AND action.prior_run_id=prior.id
        AND action.run_id=fresh.id AND action.task_id=fresh.task_id AND action.agent_id=fresh.agent_id
        AND action.response='retry_task' AND action.status='applied' AND action.revision=1
        AND action_receipt.condition_key=action.condition_key AND action_receipt.recorded_status='applied'
        AND action.policy_revision=policy.revision AND action.applied_at=NEW.created_at
        AND profile.revision=NEW.launch_profile_revision AND profile.status='active'
        AND profile.manager_grant_id IS NULL AND profile.agent_id=fresh.agent_id
        AND profile.agent_revision=agent.revision AND agent.enabled=1
        AND profile.runtime=fresh.runtime AND profile.provider=fresh.provider
        AND (profile.checkout_id IS NULL OR profile.checkout_id=fresh.checkout_id)
        AND lower(hex(sha256(CAST(fresh.scenario_json AS BLOB))))=profile.scenario_sha256
        AND assignment.id=fresh.assignment_id AND assignment.task_id=fresh.task_id
        AND assignment.agent_id=fresh.agent_id AND assignment.status='active'
        AND crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(NEW.created_at)
        AND policy.enabled=1 AND policy.auto_retry_limit>=NEW.attempt
        AND policy.revision=(SELECT MAX(current.revision) FROM supervisor_policies current WHERE current.workspace_id=NEW.workspace_id)
        AND NEW.attempt=(SELECT COUNT(*)+1 FROM run_retry_receipts prior_retry WHERE prior_retry.intent_id=NEW.intent_id)
        AND COALESCE(
          (SELECT intent_id FROM run_scheduling_receipts WHERE run_id=prior.id),
          (SELECT intent_id FROM run_retry_receipts WHERE run_id=prior.id)
        )=NEW.intent_id
        AND COALESCE(
          (SELECT launch_profile_id FROM run_scheduling_receipts WHERE run_id=prior.id),
          (SELECT launch_profile_id FROM run_retry_receipts WHERE run_id=prior.id)
        )=NEW.launch_profile_id
        AND COALESCE(
          (SELECT launch_profile_revision FROM run_scheduling_receipts WHERE run_id=prior.id),
          (SELECT launch_profile_revision FROM run_retry_receipts WHERE run_id=prior.id)
        )=NEW.launch_profile_revision
        AND COALESCE(
          (SELECT assignment_id FROM run_scheduling_receipts WHERE run_id=prior.id),
          (SELECT assignment_id FROM run_retry_receipts WHERE run_id=prior.id)
        )=NEW.assignment_id
        AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1'
        AND json_extract(packet.packet_json,'$.workspace_id')=fresh.workspace_id
        AND json_extract(packet.packet_json,'$.project_id')=fresh.project_id
        AND json_extract(packet.packet_json,'$.task_id')=fresh.task_id
        AND json_extract(packet.packet_json,'$.agent_id')=fresh.agent_id
        AND json_extract(packet.packet_json,'$.checkout_id')=fresh.checkout_id
        AND json_extract(packet.packet_json,'$.task.assignment_id')=fresh.assignment_id
        AND EXISTS (
          SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
            AND event.entity_type='supervisor_action' AND event.entity_id=action.id
            AND event.entity_revision=1 AND event.type='supervisor.action_applied'
            AND event.occurred_at=NEW.created_at AND event.actor_id='subsystem:supervisor'
        )
        AND EXISTS (
          SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
            AND event.entity_type='run' AND event.entity_id=fresh.id
            AND event.entity_revision=1 AND event.type='run.requested'
            AND event.occurred_at=NEW.created_at AND event.actor_id='subsystem:supervisor'
        )
    ) THEN RAISE(ABORT, 'run retry receipt links are not exact') END;
END;

CREATE TRIGGER scheduling_intent_validate_insert BEFORE INSERT ON scheduling_intents BEGIN
 SELECT CASE WHEN NEW.status<>'pending' OR NEW.reason IS NOT NULL OR NEW.assignment_id IS NOT NULL OR NEW.run_id IS NOT NULL OR NEW.supervisor_action_id IS NOT NULL
  OR NEW.attempts<>0 OR NEW.last_evaluated_event_sequence<>0 OR NEW.revision<>1 OR NEW.created_at IS NOT NEW.updated_at OR NEW.next_attempt_at IS NOT NULL
  OR NEW.created_by<>'local-owner' OR NEW.updated_by<>'local-owner' OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR NOT (
   (NEW.source_check_repair_proposal_id IS NULL AND EXISTS(
    SELECT 1 FROM manager_proposals proposal JOIN manager_proposal_actions action ON action.proposal_id=proposal.id
    JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id JOIN tasks task ON task.id=NEW.task_id
    WHERE proposal.id=NEW.source_proposal_id AND proposal.status='accepted' AND proposal.workspace_id=NEW.workspace_id AND proposal.project_id=NEW.project_id
      AND proposal.objective_id=NEW.objective_id AND action.id=NEW.source_action_id AND action.type IN ('create_task','assign_task','request_review','request_action')
      AND ((action.type<>'request_action' AND json_extract(action.payload_json,'$.launch_profile_id')=NEW.launch_profile_id)
        OR (action.type='request_action' AND json_extract(action.payload_json,'$.response')='reassign_task' AND json_extract(action.payload_json,'$.launch_profile_id')=NEW.launch_profile_id)
        OR (action.type='request_action' AND json_extract(action.payload_json,'$.response')='retry_task' AND EXISTS(
          SELECT 1 FROM runs failed WHERE failed.task_id=NEW.task_id AND failed.status='start_failed' AND failed.step_cursor=0
            AND NEW.launch_profile_id=COALESCE((SELECT launch_profile_id FROM run_scheduling_receipts WHERE run_id=failed.id),(SELECT launch_profile_id FROM run_retry_receipts WHERE run_id=failed.id)))))
      AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id AND profile.agent_id=NEW.agent_id AND profile.status='active' AND profile.manager_grant_id IS NULL
      AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id AND task.objective_id=NEW.objective_id
      AND ((action.type='assign_task' AND json_extract(action.payload_json,'$.task.task_id')=NEW.task_id)
        OR (action.type='request_action' AND json_extract(action.payload_json,'$.target_task_id')=NEW.task_id)
        OR (action.type IN ('create_task','request_review') AND EXISTS(SELECT 1 FROM manager_proposal_effects effect WHERE effect.proposal_id=proposal.id AND effect.action_id=action.id AND effect.entity_type='task' AND effect.entity_id=NEW.task_id)))
   ))
   OR
   (NEW.source_check_repair_proposal_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM check_repair_proposals proposal JOIN check_repair_decisions decision ON decision.repair_proposal_id=proposal.id
    JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id JOIN agents agent ON agent.id=profile.agent_id JOIN tasks task ON task.id=NEW.task_id
    WHERE proposal.id=NEW.source_check_repair_proposal_id AND proposal.status='accepted' AND decision.decision='accepted'
      AND proposal.workspace_id=NEW.workspace_id AND proposal.project_id=NEW.project_id AND proposal.objective_id=NEW.objective_id
      AND proposal.repair_launch_profile_id=NEW.launch_profile_id AND proposal.repair_launch_profile_revision=profile.revision
      AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id AND profile.agent_id=NEW.agent_id AND profile.status='active' AND profile.manager_grant_id IS NULL
      AND agent.enabled=1 AND agent.revision=profile.agent_revision AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id AND task.objective_id=NEW.objective_id
      AND task.title=proposal.repair_task_title AND task.description=proposal.repair_task_description AND task.priority=proposal.repair_task_priority
      AND task.budget_tokens=proposal.repair_budget_tokens AND task.budget_cost_cents=proposal.repair_budget_cost_cents AND task.budget_time_seconds=proposal.repair_budget_time_seconds
   ))
   OR
   (NEW.source_owner_turn_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id
    JOIN owner_turn_operations operation ON operation.turn_id=turn.id
    JOIN owner_turn_operations created_task ON created_task.turn_id=turn.id
    JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id JOIN agents agent ON agent.id=profile.agent_id JOIN tasks task ON task.id=NEW.task_id
    WHERE turn.id=NEW.source_owner_turn_id AND turn.status='executing' AND conversation.workspace_id=NEW.workspace_id AND conversation.project_id=NEW.project_id
      AND operation.id=NEW.source_owner_operation_id AND operation.type='schedule_task' AND operation.status='pending'
      AND json_extract(operation.payload_json,'$.task_key')=json_extract(created_task.payload_json,'$.task_key')
      AND json_extract(operation.payload_json,'$.launch_profile_id')=NEW.launch_profile_id
      AND created_task.type='create_task' AND created_task.status='applied' AND created_task.result_entity_type='task' AND created_task.result_entity_id=NEW.task_id
      AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id AND profile.agent_id=NEW.agent_id AND profile.status='active' AND profile.manager_grant_id IS NULL
      AND agent.enabled=1 AND agent.revision=profile.agent_revision AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id AND task.objective_id=NEW.objective_id
   )))
 THEN RAISE(ABORT,'scheduling intent lacks exact accepted typed origin and profile') END;
END;

CREATE TRIGGER scheduling_intent_validate_update BEFORE UPDATE ON scheduling_intents BEGIN
 SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.objective_id IS NOT OLD.objective_id
  OR NEW.task_id IS NOT OLD.task_id OR NEW.agent_id IS NOT OLD.agent_id OR NEW.launch_profile_id IS NOT OLD.launch_profile_id
  OR NEW.source_proposal_id IS NOT OLD.source_proposal_id OR NEW.source_action_id IS NOT OLD.source_action_id OR NEW.source_check_repair_proposal_id IS NOT OLD.source_check_repair_proposal_id
  OR NEW.source_owner_turn_id IS NOT OLD.source_owner_turn_id OR NEW.source_owner_operation_id IS NOT OLD.source_owner_operation_id
  OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR NEW.last_evaluated_event_sequence<OLD.last_evaluated_event_sequence
  OR NEW.last_evaluated_event_sequence>COALESCE((SELECT MAX(event.sequence) FROM events event WHERE event.workspace_id=NEW.workspace_id),0)
  OR (NEW.last_evaluated_event_sequence>0 AND NOT EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id AND event.sequence=NEW.last_evaluated_event_sequence))
  OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
  OR (NEW.next_attempt_at IS NOT NULL AND crewfold_timestamp_canonical(NEW.next_attempt_at) IS NOT 1)
  OR crewfold_timestamp_key(NEW.updated_at)<crewfold_timestamp_key(OLD.updated_at)
  OR NOT (
   (NEW.updated_by='subsystem:supervisor' AND (
    (OLD.status IN ('pending','deferred') AND NEW.status='deferred' AND NEW.assignment_id IS NULL AND NEW.run_id IS NULL AND NEW.supervisor_action_id IS NULL AND NEW.attempts=OLD.attempts AND NEW.reason IS NOT NULL AND NEW.next_attempt_at IS NOT NULL AND crewfold_timestamp_key(NEW.next_attempt_at)>crewfold_timestamp_key(NEW.updated_at))
    OR (OLD.status IN ('pending','deferred') AND NEW.status='run_requested' AND NEW.assignment_id IS NOT NULL AND NEW.run_id IS NOT NULL AND NEW.supervisor_action_id IS NOT NULL AND NEW.attempts=OLD.attempts+1 AND NEW.reason IS NULL AND NEW.next_attempt_at IS NULL)
    OR (OLD.status='run_requested' AND NEW.status IN ('satisfied','failed','cancelled') AND NEW.last_evaluated_event_sequence IS OLD.last_evaluated_event_sequence
      AND NEW.assignment_id IS OLD.assignment_id AND NEW.run_id IS OLD.run_id AND NEW.supervisor_action_id IS OLD.supervisor_action_id AND NEW.attempts=OLD.attempts AND NEW.reason IS NOT NULL
      AND EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id AND event.entity_type='scheduling_intent' AND event.entity_id=NEW.id AND event.entity_revision=NEW.revision
        AND event.type=CASE NEW.status WHEN 'satisfied' THEN 'supervisor.intent_satisfied' WHEN 'failed' THEN 'supervisor.intent_failed' ELSE 'supervisor.intent_cancelled' END
        AND event.occurred_at=NEW.updated_at AND event.recorded_at=NEW.updated_at AND event.actor_id='subsystem:supervisor' AND event.actor_type='subsystem'
        AND json_extract(event.data_json,'$.status')=NEW.status AND json_extract(event.data_json,'$.reason')=NEW.reason))
   ))
   OR (NEW.updated_by='local-owner' AND OLD.status IN ('pending','deferred','run_requested') AND NEW.status='cancelled'
    AND NEW.last_evaluated_event_sequence IS OLD.last_evaluated_event_sequence AND NEW.attempts=OLD.attempts AND NEW.reason='task cancelled by local owner' AND NEW.next_attempt_at IS NULL
    AND ((OLD.status IN ('pending','deferred') AND NEW.assignment_id IS NULL AND NEW.run_id IS NULL AND NEW.supervisor_action_id IS NULL)
      OR (OLD.status='run_requested' AND NEW.assignment_id IS OLD.assignment_id AND NEW.run_id IS OLD.run_id AND NEW.supervisor_action_id IS OLD.supervisor_action_id
       AND EXISTS(SELECT 1 FROM runs latest JOIN run_jobs job ON job.run_id=latest.id
        WHERE latest.id=COALESCE((SELECT retry.run_id FROM run_retry_receipts retry WHERE retry.intent_id=OLD.id ORDER BY retry.attempt DESC,retry.run_id DESC LIMIT 1),OLD.run_id)
          AND latest.workspace_id=OLD.workspace_id AND latest.task_id=OLD.task_id AND latest.assignment_id=OLD.assignment_id AND latest.status='start_failed' AND latest.step_cursor=0
          AND latest.finished_at=latest.updated_at AND job.status='complete' AND job.origin='supervisor'
          AND ((latest.id=OLD.run_id AND EXISTS(SELECT 1 FROM run_scheduling_receipts initial WHERE initial.run_id=latest.id AND initial.intent_id=OLD.id)) OR EXISTS(SELECT 1 FROM run_retry_receipts source WHERE source.run_id=latest.id AND source.intent_id=OLD.id))
          AND NOT EXISTS(SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=latest.id)
          AND EXISTS(SELECT 1 FROM events failure WHERE failure.workspace_id=OLD.workspace_id AND failure.entity_type='run' AND failure.entity_id=latest.id AND failure.entity_revision=latest.revision
            AND failure.type='run.start_failed' AND failure.occurred_at=latest.updated_at AND failure.recorded_at=latest.updated_at AND failure.actor_id='subsystem:run-worker' AND failure.actor_type='subsystem' AND json_extract(failure.data_json,'$.code')='runtime_start_failed'))))
    AND EXISTS(SELECT 1 FROM tasks task JOIN events task_event ON task_event.workspace_id=NEW.workspace_id AND task_event.entity_type='task' AND task_event.entity_id=task.id
      AND task_event.entity_revision=task.revision AND task_event.type='task.cancelled' AND task_event.occurred_at=NEW.updated_at AND task_event.recorded_at=NEW.updated_at
      AND task_event.actor_id='local-owner' AND task_event.actor_type='human' AND json_extract(task_event.data_json,'$.status')='cancelled'
      WHERE task.id=NEW.task_id AND task.workspace_id=NEW.workspace_id AND task.status='cancelled' AND task.updated_at=NEW.updated_at AND task.updated_by='local-owner')
    AND EXISTS(SELECT 1 FROM events intent_event WHERE intent_event.workspace_id=NEW.workspace_id AND intent_event.entity_type='scheduling_intent' AND intent_event.entity_id=NEW.id
      AND intent_event.entity_revision=NEW.revision AND intent_event.type='supervisor.intent_cancelled' AND intent_event.occurred_at=NEW.updated_at AND intent_event.recorded_at=NEW.updated_at
      AND intent_event.actor_id='local-owner' AND intent_event.actor_type='human' AND json_extract(intent_event.data_json,'$.task_id')=NEW.task_id
      AND json_extract(intent_event.data_json,'$.status')='cancelled' AND json_extract(intent_event.data_json,'$.reason')=NEW.reason
      AND ((OLD.status IN ('pending','deferred') AND json_type(intent_event.data_json,'$.run_id') IS NULL AND json_type(intent_event.data_json,'$.run_status') IS NULL)
       OR (OLD.status='run_requested' AND json_extract(intent_event.data_json,'$.run_id')=COALESCE((SELECT retry.run_id FROM run_retry_receipts retry WHERE retry.intent_id=OLD.id ORDER BY retry.attempt DESC,retry.run_id DESC LIMIT 1),OLD.run_id) AND json_extract(intent_event.data_json,'$.run_status')='start_failed')))
   )
  ) OR (NEW.status='run_requested' AND (NEW.assignment_id IS NULL OR NEW.run_id IS NULL OR NEW.supervisor_action_id IS NULL))
 THEN RAISE(ABORT,'invalid scheduling intent lifecycle') END;
END;

CREATE TRIGGER scheduling_intent_reject_delete BEFORE DELETE ON scheduling_intents BEGIN SELECT RAISE(ABORT,'scheduling intents are durable acceptance receipts'); END;

CREATE TRIGGER check_run_validate_insert BEFORE INSERT ON check_runs BEGIN
 SELECT CASE WHEN NEW.status<>'requested' OR NEW.revision<>1 OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL OR NEW.created_at IS NOT NEW.updated_at OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR (NEW.source_type='agent_run' AND NOT EXISTS(SELECT 1 FROM check_watch_grants grant_row WHERE grant_row.id=NEW.source_grant_id AND grant_row.workspace_id=NEW.workspace_id AND grant_row.project_id=NEW.project_id AND grant_row.agent_id=NEW.source_agent_id AND grant_row.agent_revision=NEW.source_agent_revision AND grant_row.revision=NEW.source_grant_revision AND grant_row.max_in_flight=NEW.source_max_in_flight))
  OR NOT EXISTS(SELECT 1 FROM task_check_requirements requirement JOIN check_definitions definition ON definition.id=requirement.definition_id JOIN tasks task ON task.id=requirement.task_id JOIN checkouts checkout ON checkout.id=NEW.checkout_id JOIN repositories repository ON repository.id=checkout.repository_id
    WHERE requirement.id=NEW.requirement_id AND requirement.workspace_id=NEW.workspace_id AND requirement.project_id=NEW.project_id AND requirement.task_id=NEW.task_id AND requirement.revision=NEW.requirement_revision AND requirement.status='active'
      AND definition.id=NEW.definition_id AND definition.content_revision=NEW.definition_content_revision AND definition.content_sha256=NEW.definition_sha256 AND definition.status='active'
      AND task.id=NEW.task_id AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id AND task.revision=NEW.task_revision
      AND checkout.project_id=NEW.project_id AND checkout.revision=NEW.checkout_revision AND checkout.repository_id=NEW.repository_id AND checkout.path=NEW.checkout_path AND checkout.write_mode=NEW.checkout_write_mode AND checkout.availability='available'
      AND repository.id=NEW.repository_id AND repository.object_format=NEW.repository_object_format)
  THEN RAISE(ABORT,'invalid frozen check run request') END;
END;

CREATE TRIGGER check_run_validate_update BEFORE UPDATE ON check_runs BEGIN
 SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.project_id IS NOT OLD.project_id OR NEW.task_id IS NOT OLD.task_id OR NEW.task_revision IS NOT OLD.task_revision OR NEW.requirement_id IS NOT OLD.requirement_id OR NEW.requirement_revision IS NOT OLD.requirement_revision OR NEW.definition_id IS NOT OLD.definition_id OR NEW.definition_content_revision IS NOT OLD.definition_content_revision OR NEW.definition_sha256 IS NOT OLD.definition_sha256 OR NEW.checkout_id IS NOT OLD.checkout_id OR NEW.checkout_revision IS NOT OLD.checkout_revision OR NEW.repository_id IS NOT OLD.repository_id OR NEW.repository_object_format IS NOT OLD.repository_object_format OR NEW.checkout_path IS NOT OLD.checkout_path OR NEW.checkout_write_mode IS NOT OLD.checkout_write_mode OR NEW.source_type IS NOT OLD.source_type OR NEW.source_actor_id IS NOT OLD.source_actor_id OR NEW.source_agent_id IS NOT OLD.source_agent_id OR NEW.source_agent_revision IS NOT OLD.source_agent_revision OR NEW.source_run_id IS NOT OLD.source_run_id OR NEW.source_grant_id IS NOT OLD.source_grant_id OR NEW.source_grant_revision IS NOT OLD.source_grant_revision OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at)
  OR NEW.source_max_in_flight IS NOT OLD.source_max_in_flight
  OR NOT ((OLD.status='requested' AND NEW.status='starting' AND NEW.started_at IS NULL AND NEW.finished_at IS NULL) OR (OLD.status='starting' AND NEW.status='starting' AND EXISTS(SELECT 1 FROM check_runtime_bindings binding WHERE binding.check_run_id=OLD.id)) OR (OLD.status='starting' AND NEW.status='running' AND EXISTS(SELECT 1 FROM check_runtime_bindings binding WHERE binding.check_run_id=OLD.id) AND NEW.started_at IS NOT NULL AND NEW.finished_at IS NULL) OR (OLD.status IN ('starting','running') AND NEW.status='finished' AND NOT EXISTS(SELECT 1 FROM check_runtime_bindings binding WHERE binding.check_run_id=OLD.id) AND NEW.finished_at IS NOT NULL))
  THEN RAISE(ABORT,'illegal or unsealed check run transition') END;
END;

CREATE TRIGGER check_run_reject_delete BEFORE DELETE ON check_runs BEGIN SELECT RAISE(ABORT,'check runs are immutable'); END;

CREATE TRIGGER check_runtime_binding_validate_insert BEFORE INSERT ON check_runtime_bindings BEGIN
 SELECT CASE WHEN NEW.operation_id<>NEW.check_run_id OR NEW.revision<>1 OR NEW.created_at<>NEW.updated_at OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR crewfold_utf8_valid(NEW.runtime_handle) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM check_runs run WHERE run.id=NEW.check_run_id AND run.status='starting')
  THEN RAISE(ABORT,'invalid check runtime binding') END;
END;

CREATE TRIGGER check_runtime_binding_reject_update BEFORE UPDATE ON check_runtime_bindings BEGIN SELECT RAISE(ABORT,'check runtime bindings are immutable'); END;

CREATE TRIGGER check_job_validate_insert BEFORE INSERT ON check_jobs BEGIN SELECT CASE WHEN NEW.status<>'pending' OR NEW.attempts<>0 OR NEW.lease_expires_at IS NOT NULL OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NEW.created_at IS NOT NEW.updated_at OR NOT EXISTS(SELECT 1 FROM check_runs WHERE id=NEW.check_run_id AND status='requested') THEN RAISE(ABORT,'invalid check job') END; END;

CREATE TRIGGER check_job_validate_update BEFORE UPDATE ON check_jobs BEGIN SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.check_run_id IS NOT OLD.check_run_id OR NEW.created_at IS NOT OLD.created_at OR NEW.attempts<OLD.attempts OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR NOT ((OLD.status='pending' AND NEW.status='leased' AND NEW.attempts=OLD.attempts+1 AND NEW.lease_expires_at IS NOT NULL) OR (OLD.status='leased' AND NEW.status='pending' AND NEW.attempts=OLD.attempts AND NEW.lease_expires_at IS NULL) OR (OLD.status='leased' AND NEW.status='complete' AND NEW.attempts=OLD.attempts AND NEW.lease_expires_at IS NULL)) THEN RAISE(ABORT,'invalid check job transition') END; END;

CREATE TRIGGER check_job_reject_delete BEFORE DELETE ON check_jobs BEGIN SELECT RAISE(ABORT,'check jobs are durable'); END;

CREATE TRIGGER check_launch_receipt_validate_insert BEFORE INSERT ON check_launch_receipts BEGIN
 SELECT CASE WHEN NEW.operation_id IS NOT NEW.check_run_id OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR crewfold_timestamp_canonical(NEW.observed_at) IS NOT 1 OR crewfold_utf8_valid(NEW.effective_working_directory) IS NOT 1 THEN RAISE(ABORT,'invalid check launch receipt identity or timestamp') END;
 SELECT CASE WHEN NEW.branch IS NOT NULL AND (length(CAST(NEW.branch AS BLOB))>1024 OR instr(NEW.branch,char(0))<>0 OR crewfold_utf8_valid(NEW.branch) IS NOT 1) THEN RAISE(ABORT,'invalid check launch branch observation') END;
 SELECT CASE WHEN json_type(NEW.dirty_paths_json)<>'array' OR json_array_length(NEW.dirty_paths_json)>256 OR length(CAST(NEW.dirty_paths_json AS BLOB))>262144 OR EXISTS(SELECT 1 FROM json_each(NEW.dirty_paths_json) path WHERE path.type<>'text' OR length(CAST(path.value AS BLOB)) NOT BETWEEN 1 AND 1024 OR substr(path.value,1,1)='/' OR path.value='.' OR path.value='..' OR substr(path.value,1,3)='../' OR instr(path.value,char(0))<>0 OR crewfold_utf8_valid(path.value) IS NOT 1) OR EXISTS(SELECT 1 FROM json_each(NEW.dirty_paths_json) path JOIN json_each(NEW.dirty_paths_json) following ON following.key=path.key+1 WHERE following.value<=path.value) THEN RAISE(ABORT,'invalid check launch dirty-path observation') END;
 SELECT CASE WHEN NEW.observation_available=1 AND (NEW.head_commit IS NULL OR length(NEW.head_commit) IS NOT CASE NEW.object_format WHEN 'sha1' THEN 40 ELSE 64 END OR NEW.head_commit GLOB '*[^0-9a-f]*' OR NEW.diagnostic_code IS NOT NULL OR NEW.diagnostic IS NOT NULL OR (NEW.dirty=0 AND json_array_length(NEW.dirty_paths_json)<>0)) THEN RAISE(ABORT,'invalid available check launch observation') END;
 SELECT CASE WHEN NEW.observation_available=0 AND (NEW.branch IS NOT NULL OR NEW.head_commit IS NOT NULL OR NEW.dirty<>0 OR json_array_length(NEW.dirty_paths_json)<>0 OR NEW.diagnostic_code IS NULL OR NEW.diagnostic IS NULL OR length(CAST(NEW.diagnostic_code AS BLOB)) NOT BETWEEN 1 AND 128 OR length(CAST(NEW.diagnostic AS BLOB)) NOT BETWEEN 1 AND 4096) THEN RAISE(ABORT,'invalid unavailable check launch observation') END;
 SELECT CASE WHEN NEW.preflight_failure_code='authority_revoked' AND (NEW.source_type<>'agent_run' OR EXISTS(SELECT 1 FROM check_watch_grants grant_row JOIN agents agent ON agent.id=grant_row.agent_id JOIN runs source_run ON source_run.id=NEW.source_run_id JOIN run_capabilities capability ON capability.run_id=source_run.id JOIN run_context_bindings binding ON binding.run_id=source_run.id JOIN context_packets packet ON packet.id=binding.context_packet_id WHERE grant_row.id=NEW.source_grant_id AND grant_row.revision=NEW.source_grant_revision AND grant_row.status='active' AND grant_row.agent_id=NEW.source_agent_id AND grant_row.agent_revision=NEW.source_agent_revision AND agent.enabled=1 AND agent.revision=grant_row.agent_revision AND source_run.workspace_id=grant_row.workspace_id AND source_run.project_id=grant_row.project_id AND source_run.agent_id=grant_row.agent_id AND source_run.status IN ('starting','active','blocked') AND crewfold_timestamp_key(capability.expires_at)>crewfold_timestamp_key(NEW.created_at) AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1' AND json_extract(packet.packet_json,'$.check_watch_grant.grant_id')=grant_row.id AND json_extract(packet.packet_json,'$.check_watch_grant.grant_revision')=grant_row.revision)) THEN RAISE(ABORT,'check launch authority denial is not current') END;
 SELECT CASE WHEN NEW.preflight_failure_code='definition_retired' AND EXISTS(SELECT 1 FROM check_definitions definition JOIN check_runs run ON run.definition_id=definition.id WHERE run.id=NEW.check_run_id AND definition.status='active' AND definition.content_sha256=NEW.definition_sha256) THEN RAISE(ABORT,'check launch definition denial is not current') END;
 SELECT CASE WHEN NEW.preflight_failure_code='requirement_retired' AND EXISTS(SELECT 1 FROM task_check_requirements requirement JOIN check_runs run ON run.requirement_id=requirement.id WHERE run.id=NEW.check_run_id AND requirement.status='active' AND requirement.revision=run.requirement_revision) THEN RAISE(ABORT,'check launch requirement denial is not current') END;
 SELECT CASE WHEN NEW.preflight_failure_code='checkout_changed' AND EXISTS(SELECT 1 FROM checkouts checkout JOIN check_runs run ON run.checkout_id=checkout.id WHERE run.id=NEW.check_run_id AND checkout.project_id=run.project_id AND checkout.repository_id=run.repository_id AND checkout.path=run.checkout_path AND checkout.write_mode=run.checkout_write_mode AND checkout.revision=run.checkout_revision AND checkout.availability='available') THEN RAISE(ABORT,'check launch checkout denial is not current') END;
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM check_runs run JOIN check_jobs job ON job.check_run_id=run.id WHERE run.id=NEW.check_run_id AND run.status='starting' AND job.id=NEW.check_job_id AND job.status='leased' AND run.definition_sha256=NEW.definition_sha256 AND run.repository_id=NEW.repository_id AND run.repository_object_format=NEW.object_format AND run.checkout_id=NEW.checkout_id AND run.source_type=NEW.source_type AND run.source_actor_id=NEW.source_actor_id AND COALESCE(run.source_agent_id,'')=COALESCE(NEW.source_agent_id,'') AND COALESCE(run.source_agent_revision,0)=COALESCE(NEW.source_agent_revision,0) AND COALESCE(run.source_run_id,'')=COALESCE(NEW.source_run_id,'') AND COALESCE(run.source_grant_id,'')=COALESCE(NEW.source_grant_id,'') AND COALESCE(run.source_grant_revision,0)=COALESCE(NEW.source_grant_revision,0)) THEN RAISE(ABORT,'check launch receipt differs from exact run and job') END;
END;

CREATE TRIGGER check_launch_receipt_reject_update BEFORE UPDATE ON check_launch_receipts BEGIN SELECT RAISE(ABORT,'check launch receipts are immutable'); END;

CREATE TRIGGER check_launch_receipt_reject_delete BEFORE DELETE ON check_launch_receipts BEGIN SELECT RAISE(ABORT,'check launch receipts are immutable'); END;

CREATE TRIGGER check_result_validate_insert BEFORE INSERT ON check_results BEGIN
 SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR crewfold_timestamp_canonical(NEW.observed_at) IS NOT 1 OR NOT EXISTS(SELECT 1 FROM check_runs run JOIN check_launch_receipts receipt ON receipt.check_run_id=run.id WHERE run.id=NEW.check_run_id AND run.status='finished' AND run.requirement_id=NEW.requirement_id AND run.requirement_revision=NEW.requirement_revision AND run.definition_id=NEW.definition_id AND run.definition_content_revision=NEW.definition_content_revision AND run.repository_id=NEW.repository_id AND run.repository_object_format=NEW.object_format AND run.checkout_id=NEW.checkout_id) THEN RAISE(ABORT,'invalid immutable check result') END;
 SELECT CASE WHEN (NEW.diagnostic_code IS NOT NULL AND (length(CAST(NEW.diagnostic_code AS BLOB)) NOT BETWEEN 1 AND 128 OR instr(NEW.diagnostic_code,char(0))<>0 OR crewfold_utf8_valid(NEW.diagnostic_code) IS NOT 1)) OR (NEW.diagnostic IS NOT NULL AND (length(CAST(NEW.diagnostic AS BLOB)) NOT BETWEEN 1 AND 4096 OR instr(NEW.diagnostic,char(0))<>0 OR crewfold_utf8_valid(NEW.diagnostic) IS NOT 1)) THEN RAISE(ABORT,'invalid check result diagnostic') END;
END;

CREATE TRIGGER check_result_reject_update BEFORE UPDATE ON check_results BEGIN SELECT RAISE(ABORT,'check results are immutable'); END;

CREATE TRIGGER check_result_reject_delete BEFORE DELETE ON check_results BEGIN SELECT RAISE(ABORT,'check results are immutable'); END;

CREATE TRIGGER check_artifact_reject_update BEFORE UPDATE ON check_artifacts BEGIN SELECT RAISE(ABORT,'check artifacts are immutable'); END;

CREATE TRIGGER check_artifact_reject_delete BEFORE DELETE ON check_artifacts BEGIN SELECT RAISE(ABORT,'check artifacts are immutable'); END;

CREATE TRIGGER check_artifact_validate_insert BEFORE INSERT ON check_artifacts BEGIN
 SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR NOT EXISTS(SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id JOIN check_definitions definition ON definition.id=run.definition_id WHERE result.id=NEW.check_result_id AND (NEW.kind='diagnostic' OR NEW.captured_bytes<=definition.output_byte_limit)) OR NOT EXISTS(SELECT 1 FROM immutable_artifacts artifact WHERE artifact.content_sha256=NEW.content_sha256 AND artifact.byte_size=NEW.captured_bytes) THEN RAISE(ABORT,'invalid bounded check artifact') END;
END;

CREATE TRIGGER check_freshness_validate_insert BEFORE INSERT ON check_result_freshness BEGIN
 SELECT CASE WHEN crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR crewfold_timestamp_canonical(NEW.observed_at) IS NOT 1 OR NEW.revision IS NOT COALESCE((SELECT MAX(revision)+1 FROM check_result_freshness WHERE check_result_id=NEW.check_result_id),1)
  OR (NEW.revision>1 AND EXISTS(SELECT 1 FROM check_result_freshness prior WHERE prior.check_result_id=NEW.check_result_id AND prior.ever_stale=1) AND NEW.status<>'stale')
  OR (NEW.revision>1 AND NEW.initially_eligible IS NOT (SELECT initially_eligible FROM check_result_freshness WHERE check_result_id=NEW.check_result_id ORDER BY revision LIMIT 1))
  OR (NEW.status='fresh' AND (NEW.initially_eligible<>1 OR NEW.ever_stale<>0))
  THEN RAISE(ABORT,'invalid append-only check freshness') END;
END;

CREATE TRIGGER check_freshness_reject_update BEFORE UPDATE ON check_result_freshness BEGIN SELECT RAISE(ABORT,'check freshness is append-only'); END;

CREATE TRIGGER check_freshness_reject_delete BEFORE DELETE ON check_result_freshness BEGIN SELECT RAISE(ABORT,'check freshness is append-only'); END;

CREATE TRIGGER check_evidence_validate_insert BEFORE INSERT ON check_requirement_evidence BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM check_results result JOIN check_result_freshness freshness ON freshness.check_result_id=result.id AND freshness.revision=NEW.freshness_revision WHERE result.id=NEW.check_result_id AND result.requirement_id=NEW.requirement_id AND result.requirement_revision=NEW.requirement_revision AND NEW.effect=CASE WHEN result.outcome='passed' AND freshness.status='fresh' THEN 'supports' WHEN result.outcome='failed' AND freshness.status='fresh' THEN 'contradicts' ELSE 'inconclusive' END) THEN RAISE(ABORT,'check evidence is not exact mechanical evidence') END;
END;

CREATE TRIGGER check_evidence_reject_update BEFORE UPDATE ON check_requirement_evidence BEGIN SELECT RAISE(ABORT,'check evidence is immutable'); END;

CREATE TRIGGER check_evidence_reject_delete BEFORE DELETE ON check_requirement_evidence BEGIN SELECT RAISE(ABORT,'check evidence is immutable'); END;

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
                'urn:crewfold:schema:domain:context-packet:v1'
              AND json_extract(packet.packet_json, '$.as_of_event_sequence') = NEW.scan_event_sequence
        ) THEN RAISE(ABORT, 'invalid initial run context delta state') END;
END;

CREATE TRIGGER context_packet_validate_insert
BEFORE INSERT ON context_packets
BEGIN
    SELECT CASE WHEN length(NEW.id) <> 36 OR substr(NEW.id, 1, 4) <> 'ctx_'
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
    SELECT CASE WHEN json_type(NEW.packet_json, '$.schema') IS NOT 'text'
        OR json_extract(NEW.packet_json, '$.schema') IS NOT
          'urn:crewfold:schema:domain:context-packet:v1'
        THEN RAISE(ABORT, 'unsupported context packet schema') END;
    SELECT CASE WHEN (
        json_type(NEW.packet_json, '$.as_of_event_sequence') IS NOT 'integer'
        OR json_extract(NEW.packet_json, '$.as_of_event_sequence') < 0
        OR json_extract(NEW.packet_json, '$.as_of_event_sequence') IS NOT (SELECT COALESCE(MAX(sequence),0) FROM events)
        OR json_extract(NEW.packet_json, '$.live_context.schema') IS NOT 'urn:crewfold:schema:domain:live-context-policy:v1'
        OR json_extract(NEW.packet_json, '$.live_context.delivery') IS NOT 'explicit_pull'
        OR json_extract(NEW.packet_json, '$.live_context.ack_authority') IS NOT 'bound_run'
        OR json_extract(NEW.packet_json, '$.live_context.max_pending') IS NOT 1
        OR json_extract(NEW.packet_json, '$.live_context.max_relevant_events') IS NOT 1000
        OR json_extract(NEW.packet_json, '$.live_context.per_delta_limit_bytes') IS NOT 16384
        OR json_extract(NEW.packet_json, '$.live_context.cumulative_delta_limit_bytes') IS NOT 65536
        OR json_extract(NEW.packet_json, '$.policy.denied_operations') IS NOT json_array(
          'change another run or task','push or merge source','deploy','message a person or broadcast','read unscoped context')
        OR json_extract(NEW.packet_json, '$.policy.approval_required') IS NOT json_array(
          'shared repository mutation','external side effect','destructive operation')
    ) THEN RAISE(ABORT, 'invalid live context policy') END;
    SELECT CASE WHEN json_type(NEW.packet_json, '$.management_grant') IS NULL
      AND json_type(NEW.packet_json, '$.check_watch_grant') IS NULL AND (
        json_extract(NEW.packet_json, '$.policy.allowed_tools') IS NOT json_array(
          'crewfold_acknowledge_context_delta','crewfold_acknowledge_message','crewfold_get_briefing',
          'crewfold_get_context_delta','crewfold_get_status','crewfold_list_inbox','crewfold_propose_knowledge',
          'crewfold_propose_completion','crewfold_publish_artifact','crewfold_read_message',
          'crewfold_report_blocked','crewfold_report_contradiction','crewfold_report_progress','crewfold_send_message')
    ) THEN RAISE(ABORT, 'invalid ordinary context tool policy') END;
    SELECT CASE WHEN json_type(NEW.packet_json, '$.management_grant') IS NOT NULL AND (
        json_type(NEW.packet_json, '$.check_watch_grant') IS NOT NULL
        OR json_extract(NEW.packet_json, '$.management_grant.schema') IS NOT
          'urn:crewfold:schema:domain:context-manager-grant:v1'
        OR json_type(NEW.packet_json, '$.management_grant.owner_executive') NOT IN ('true','false')
        OR json_type(NEW.packet_json, '$.management_grant.invocation_launch_profile_id') IS NOT 'text'
        OR json_type(NEW.packet_json, '$.management_grant.invocation_launch_profile_revision') IS NOT 'integer'
        OR json_extract(NEW.packet_json, '$.management_grant.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.packet_json, '$.management_grant.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.packet_json, '$.management_grant.manager_task_id') IS NOT NEW.task_id
        OR json_extract(NEW.packet_json, '$.management_grant.manager_agent_id') IS NOT NEW.agent_id
        OR json_type(NEW.packet_json, '$.management_grant.launch_profiles') IS NOT 'array'
        OR json_array_length(json_extract(NEW.packet_json, '$.management_grant.launch_profiles')) NOT BETWEEN 1 AND 32
        OR NOT EXISTS (
          SELECT 1 FROM manager_grants grant_row
          WHERE grant_row.id = json_extract(NEW.packet_json, '$.management_grant.grant_id')
            AND grant_row.revision = json_extract(NEW.packet_json, '$.management_grant.grant_revision')
            AND grant_row.status = 'active' AND grant_row.workspace_id = NEW.workspace_id
            AND grant_row.project_id = NEW.project_id AND grant_row.task_id = NEW.task_id
            AND grant_row.task_revision = json_extract(NEW.packet_json, '$.management_grant.manager_task_revision')
            AND grant_row.agent_id = NEW.agent_id
            AND grant_row.agent_revision = json_extract(NEW.packet_json, '$.management_grant.manager_agent_revision')
            AND grant_row.objective_id = json_extract(NEW.packet_json, '$.management_grant.objective_id')
            AND json(grant_row.proposal_kinds_json) = json(json_extract(NEW.packet_json, '$.management_grant.allowed_proposal_kinds'))
            AND json(grant_row.launch_profiles_json) = json(json_extract(NEW.packet_json, '$.management_grant.launch_profiles'))
            AND json(grant_row.allowed_claim_kinds_json) = json(json_extract(NEW.packet_json, '$.management_grant.allowed_claim_kinds'))
            AND grant_row.max_open_proposals = json_extract(NEW.packet_json, '$.management_grant.max_open_proposals')
            AND grant_row.max_actions = json_extract(NEW.packet_json, '$.management_grant.max_actions')
            AND grant_row.max_tasks = json_extract(NEW.packet_json, '$.management_grant.max_tasks')
            AND grant_row.max_dependencies = json_extract(NEW.packet_json, '$.management_grant.max_dependencies')
            AND grant_row.max_claim_requirements = json_extract(NEW.packet_json, '$.management_grant.max_claim_requirements')
            AND grant_row.budget_tokens = json_extract(NEW.packet_json, '$.management_grant.budget.token_limit')
            AND grant_row.budget_cost_cents = json_extract(NEW.packet_json, '$.management_grant.budget.cost_cents')
            AND grant_row.budget_time_seconds = json_extract(NEW.packet_json, '$.management_grant.budget.time_seconds')
            AND COALESCE(grant_row.expires_at,'') = COALESCE(json_extract(NEW.packet_json, '$.management_grant.expires_at'),'')
            AND (grant_row.expires_at IS NULL OR crewfold_timestamp_key(grant_row.expires_at) > crewfold_timestamp_key(NEW.created_at))
        )
        OR NOT EXISTS (
          SELECT 1 FROM objectives objective
          WHERE objective.id=json_extract(NEW.packet_json, '$.management_grant.objective_id')
            AND objective.workspace_id=NEW.workspace_id
            AND objective.revision=json_extract(NEW.packet_json, '$.management_grant.objective_revision')
        )
        OR NOT EXISTS (
          SELECT 1 FROM launch_profiles invocation
          WHERE invocation.id=json_extract(NEW.packet_json, '$.management_grant.invocation_launch_profile_id')
            AND invocation.revision=json_extract(NEW.packet_json, '$.management_grant.invocation_launch_profile_revision')
            AND invocation.workspace_id=NEW.workspace_id AND invocation.project_id=NEW.project_id
            AND invocation.agent_id=NEW.agent_id
            AND invocation.agent_revision=json_extract(NEW.packet_json, '$.management_grant.manager_agent_revision')
            AND invocation.manager_grant_id=json_extract(NEW.packet_json, '$.management_grant.grant_id')
            AND invocation.status='active'
        )
        OR json_extract(NEW.packet_json, '$.management_grant.owner_executive') IS NOT (
          SELECT EXISTS(
            SELECT 1 FROM owner_executive_bindings binding
            WHERE binding.workspace_id=NEW.workspace_id AND binding.project_id=NEW.project_id
              AND binding.objective_id=json_extract(NEW.packet_json, '$.management_grant.objective_id')
              AND binding.planning_task_id=NEW.task_id AND binding.agent_id=NEW.agent_id
              AND binding.manager_grant_id=json_extract(NEW.packet_json, '$.management_grant.grant_id')
              AND binding.launch_profile_id=json_extract(NEW.packet_json, '$.management_grant.invocation_launch_profile_id')
              AND binding.status='active'
          )
        )
        OR json_extract(NEW.packet_json, '$.policy.allowed_tools') IS NOT (
          SELECT json_group_array(tool) FROM (
            SELECT 0 ordinal,'crewfold_acknowledge_context_delta' tool UNION ALL
            SELECT 1,'crewfold_acknowledge_message' UNION ALL SELECT 2,'crewfold_get_briefing' UNION ALL
            SELECT 3,'crewfold_get_context_delta' UNION ALL SELECT 4,'crewfold_get_status' UNION ALL
            SELECT 5,'crewfold_list_inbox' UNION ALL SELECT 6,'crewfold_propose_knowledge' UNION ALL
            SELECT 7,'crewfold_propose_completion' UNION ALL SELECT 8,'crewfold_publish_artifact' UNION ALL
            SELECT 9,'crewfold_read_message' UNION ALL SELECT 10,'crewfold_report_blocked' UNION ALL
            SELECT 11,'crewfold_report_contradiction' UNION ALL SELECT 12,'crewfold_report_progress' UNION ALL
            SELECT 13,'crewfold_send_message' UNION ALL
            SELECT 14,'crewfold_get_executive_context'
              WHERE json_extract(NEW.packet_json, '$.management_grant.owner_executive')=1 UNION ALL
            SELECT 15,'crewfold_respond_to_owner'
              WHERE json_extract(NEW.packet_json, '$.management_grant.owner_executive')=1 UNION ALL
            SELECT 16+kind.ordinal,CASE kind.kind WHEN 'assignment' THEN 'crewfold_propose_assignment'
              WHEN 'escalation' THEN 'crewfold_propose_escalation' WHEN 'review' THEN 'crewfold_propose_review'
              ELSE 'crewfold_propose_tasks' END
            FROM manager_grant_proposal_kinds kind
            WHERE kind.grant_id=json_extract(NEW.packet_json, '$.management_grant.grant_id')
            ORDER BY ordinal
          )
        )
    ) THEN RAISE(ABORT, 'invalid manager context grant') END;
    SELECT CASE WHEN json_type(NEW.packet_json, '$.check_watch_grant') IS NOT NULL AND (
        json_type(NEW.packet_json, '$.management_grant') IS NOT NULL
        OR json_extract(NEW.packet_json, '$.check_watch_grant.schema') IS NOT
          'urn:crewfold:schema:domain:context-check-watch-grant:v1'
        OR json_extract(NEW.packet_json, '$.check_watch_grant.workspace_id') IS NOT NEW.workspace_id
        OR json_extract(NEW.packet_json, '$.check_watch_grant.project_id') IS NOT NEW.project_id
        OR json_extract(NEW.packet_json, '$.check_watch_grant.watcher_agent_id') IS NOT NEW.agent_id
        OR json_extract(NEW.packet_json, '$.check_watch_grant.watcher_agent_revision') IS NOT
          json_extract(NEW.packet_json, '$.role.revision')
        OR json_type(NEW.packet_json, '$.check_watch_grant.operations') IS NOT 'array'
        OR json_array_length(json_extract(NEW.packet_json, '$.check_watch_grant.operations')) NOT BETWEEN 1 AND 3
        OR json_type(NEW.packet_json, '$.check_watch_grant.definitions') IS NOT 'array'
        OR json_array_length(json_extract(NEW.packet_json, '$.check_watch_grant.definitions')) NOT BETWEEN 1 AND 64
        OR NOT EXISTS (
          SELECT 1 FROM check_watch_grants grant_row
          WHERE grant_row.id = json_extract(NEW.packet_json, '$.check_watch_grant.grant_id')
            AND grant_row.revision = json_extract(NEW.packet_json, '$.check_watch_grant.grant_revision')
            AND grant_row.status = 'active' AND grant_row.workspace_id = NEW.workspace_id
            AND grant_row.project_id = NEW.project_id AND grant_row.agent_id = NEW.agent_id
            AND grant_row.agent_revision = json_extract(NEW.packet_json, '$.check_watch_grant.watcher_agent_revision')
            AND EXISTS (SELECT 1 FROM agents agent WHERE agent.id=grant_row.agent_id
              AND agent.workspace_id=grant_row.workspace_id AND agent.enabled=1
              AND agent.revision=grant_row.agent_revision)
            AND json(grant_row.operations_json) = json(json_extract(NEW.packet_json, '$.check_watch_grant.operations'))
            AND json(grant_row.definitions_json) = json(json_extract(NEW.packet_json, '$.check_watch_grant.definitions'))
            AND grant_row.max_pending = json_extract(NEW.packet_json, '$.check_watch_grant.max_pending')
            AND grant_row.max_in_flight = json_extract(NEW.packet_json, '$.check_watch_grant.max_in_flight')
            AND COALESCE(grant_row.expires_at,'') =
              COALESCE(json_extract(NEW.packet_json, '$.check_watch_grant.expires_at'),'')
            AND grant_row.content_sha256 = json_extract(NEW.packet_json, '$.check_watch_grant.content_sha256')
            AND (grant_row.expires_at IS NULL OR
              crewfold_timestamp_key(grant_row.expires_at) > crewfold_timestamp_key(NEW.created_at))
        )
        OR json_extract(NEW.packet_json, '$.policy.allowed_tools') IS NOT (
          SELECT json_group_array(tool) FROM (
            SELECT 0 ordinal,'crewfold_acknowledge_context_delta' tool UNION ALL
            SELECT 1,'crewfold_acknowledge_message' UNION ALL SELECT 2,'crewfold_get_briefing' UNION ALL
            SELECT 3,'crewfold_get_context_delta' UNION ALL SELECT 4,'crewfold_get_status' UNION ALL
            SELECT 5,'crewfold_list_inbox' UNION ALL SELECT 6,'crewfold_propose_knowledge' UNION ALL
            SELECT 7,'crewfold_propose_completion' UNION ALL SELECT 8,'crewfold_publish_artifact' UNION ALL
            SELECT 9,'crewfold_read_message' UNION ALL SELECT 10,'crewfold_report_blocked' UNION ALL
            SELECT 11,'crewfold_report_contradiction' UNION ALL SELECT 12,'crewfold_report_progress' UNION ALL
            SELECT 13,'crewfold_send_message' UNION ALL
            SELECT 14,'crewfold_run_check'
              WHERE EXISTS (SELECT 1 FROM check_watch_grant_operations operation
                WHERE operation.grant_id=json_extract(NEW.packet_json, '$.check_watch_grant.grant_id')
                  AND operation.operation='run') UNION ALL
            SELECT 15,'crewfold_list_check_results'
              WHERE EXISTS (SELECT 1 FROM check_watch_grant_operations operation
                WHERE operation.grant_id=json_extract(NEW.packet_json, '$.check_watch_grant.grant_id')
                  AND operation.operation='inspect') UNION ALL
            SELECT 16,'crewfold_inspect_check_result'
              WHERE EXISTS (SELECT 1 FROM check_watch_grant_operations operation
                WHERE operation.grant_id=json_extract(NEW.packet_json, '$.check_watch_grant.grant_id')
                  AND operation.operation='inspect') UNION ALL
            SELECT 17,'crewfold_propose_check_repair'
              WHERE EXISTS (SELECT 1 FROM check_watch_grant_operations operation
                WHERE operation.grant_id=json_extract(NEW.packet_json, '$.check_watch_grant.grant_id')
                  AND operation.operation='propose_repair')
            ORDER BY ordinal
          )
        )
    ) THEN RAISE(ABORT, 'invalid check-watch context grant') END;
    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM tasks task JOIN agents agent ON agent.id = NEW.agent_id
        JOIN checkouts checkout ON checkout.id = NEW.checkout_id
        JOIN projects project ON project.id = NEW.project_id
        JOIN repositories repository ON repository.id = checkout.repository_id
        WHERE task.id = NEW.task_id AND (
          task.workspace_id IS NOT NEW.workspace_id OR task.project_id IS NOT NEW.project_id
          OR json_extract(NEW.packet_json, '$.role.name') IS NOT agent.name
          OR json_extract(NEW.packet_json, '$.role.role') IS NOT agent.role
          OR json_extract(NEW.packet_json, '$.role.provider') IS NOT agent.provider
          OR json_extract(NEW.packet_json, '$.role.runtime') IS NOT agent.runtime
          OR json_extract(NEW.packet_json, '$.role.revision') IS NOT agent.revision
          OR json_extract(NEW.packet_json, '$.task.assignment_id') IS NOT COALESCE((
             SELECT id FROM task_assignments WHERE task_id = task.id AND status = 'active'), '')
          OR json_extract(NEW.packet_json, '$.task.title') IS NOT task.title
          OR COALESCE(json_extract(NEW.packet_json, '$.task.description'),'') IS NOT COALESCE(task.description,'')
          OR json_extract(NEW.packet_json, '$.task.priority') IS NOT task.priority
          OR json_extract(NEW.packet_json, '$.task.revision') IS NOT task.revision
          OR json_extract(NEW.packet_json, '$.checkout.project_name') IS NOT project.name
          OR json_extract(NEW.packet_json, '$.checkout.repository_id') IS NOT repository.id
          OR json_extract(NEW.packet_json, '$.checkout.repository_fingerprint') IS NOT repository.fingerprint
          OR json_extract(NEW.packet_json, '$.checkout.path') IS NOT checkout.path
          OR json_extract(NEW.packet_json, '$.checkout.write_mode') IS NOT checkout.write_mode
          OR json_extract(NEW.packet_json, '$.checkout.checkout_kind') IS NOT checkout.checkout_kind
          OR checkout.availability <> 'available'
        )
    ) THEN RAISE(ABORT, 'live context packet differs from canonical base authority') END;
    SELECT CASE WHEN NEW.content_hash IS NOT 'sha256:' || lower(hex(sha256(CAST(json_set(
        NEW.packet_json, '$.id','', '$.content_hash','', '$.created_at','', '$.created_by','',
        '$.byte_size',0, '$.budget.total.used_bytes',0, '$.budget.total.remaining_bytes',32768
    ) AS BLOB)))) THEN RAISE(ABORT, 'context packet semantic hash is invalid') END;
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
        JOIN checkouts checkout ON checkout.id = run.checkout_id
        JOIN projects project ON project.id = run.project_id
        JOIN repositories repository ON repository.id = checkout.repository_id
        WHERE run.id = NEW.run_id AND json_extract(packet.packet_json, '$.schema') =
          'urn:crewfold:schema:domain:context-packet:v1' AND (
          json_extract(packet.packet_json, '$.role.agent_id') IS NOT agent.id
          OR json_extract(packet.packet_json, '$.role.name') IS NOT agent.name
          OR json_extract(packet.packet_json, '$.role.role') IS NOT agent.role
          OR json_extract(packet.packet_json, '$.role.provider') IS NOT agent.provider
          OR json_extract(packet.packet_json, '$.role.runtime') IS NOT agent.runtime
          OR json_extract(packet.packet_json, '$.role.revision') IS NOT agent.revision OR agent.enabled <> 1
          OR json_extract(packet.packet_json, '$.task.task_id') IS NOT task.id
          OR json_extract(packet.packet_json, '$.task.assignment_id') IS NOT COALESCE((
              SELECT id FROM task_assignments WHERE task_id = task.id AND status = 'active'), '')
          OR COALESCE(json_extract(packet.packet_json, '$.task.objective_id'),'') IS NOT COALESCE(task.objective_id,'')
          OR json_extract(packet.packet_json, '$.task.title') IS NOT task.title
          OR COALESCE(json_extract(packet.packet_json, '$.task.description'),'') IS NOT COALESCE(task.description,'')
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
          OR COALESCE(json_extract(packet.packet_json, '$.checkout.branch'),'') IS NOT COALESCE(checkout.branch,'')
          OR COALESCE(json_extract(packet.packet_json, '$.checkout.head_commit'),'') IS NOT COALESCE(checkout.head_commit,'')
          OR json_extract(packet.packet_json, '$.checkout.dirty') IS NOT checkout.dirty
          OR json_extract(packet.packet_json, '$.checkout.revision') IS NOT checkout.revision
          OR checkout.availability <> 'available'
          OR json_extract(packet.packet_json, '$.dependencies') IS NOT (
              SELECT json_group_array(json_object('task_id', dependency_task.id, 'title', dependency_task.title,
                  'status', dependency_task.status, 'revision', dependency_task.revision))
              FROM (SELECT depends_on_task_id FROM task_dependencies WHERE task_id = task.id ORDER BY depends_on_task_id) edge
              JOIN tasks dependency_task ON dependency_task.id = edge.depends_on_task_id)
        )
    ) THEN RAISE(ABORT, 'run context packet base differs from canonical authority') END;
    SELECT CASE WHEN EXISTS (
      SELECT 1 FROM runs run JOIN context_packets packet ON packet.id = NEW.context_packet_id
      LEFT JOIN manager_grants grant_row
        ON grant_row.id = json_extract(packet.packet_json, '$.management_grant.grant_id')
      WHERE run.id = NEW.run_id AND json_type(packet.packet_json, '$.management_grant') IS NOT NULL AND (
          grant_row.id IS NULL OR grant_row.status <> 'active'
          OR (grant_row.expires_at IS NOT NULL AND crewfold_timestamp_key(grant_row.expires_at) <= crewfold_timestamp_key(NEW.bound_at))
          OR grant_row.workspace_id <> run.workspace_id OR grant_row.project_id <> run.project_id
          OR grant_row.objective_id <> json_extract(packet.packet_json, '$.task.objective_id')
          OR grant_row.task_id <> run.task_id OR grant_row.agent_id <> run.agent_id
          OR grant_row.revision <> json_extract(packet.packet_json, '$.management_grant.grant_revision')
          OR grant_row.task_revision <> json_extract(packet.packet_json, '$.management_grant.manager_task_revision')
          OR grant_row.agent_revision <> json_extract(packet.packet_json, '$.management_grant.manager_agent_revision')
          OR json(grant_row.proposal_kinds_json) <> json(json_extract(packet.packet_json, '$.management_grant.allowed_proposal_kinds'))
          OR json(grant_row.launch_profiles_json) <> json(json_extract(packet.packet_json, '$.management_grant.launch_profiles'))
          OR json(grant_row.allowed_claim_kinds_json) <> json(json_extract(packet.packet_json, '$.management_grant.allowed_claim_kinds'))
          OR grant_row.max_open_proposals <> json_extract(packet.packet_json, '$.management_grant.max_open_proposals')
          OR grant_row.max_actions <> json_extract(packet.packet_json, '$.management_grant.max_actions')
          OR grant_row.max_tasks <> json_extract(packet.packet_json, '$.management_grant.max_tasks')
          OR grant_row.max_dependencies <> json_extract(packet.packet_json, '$.management_grant.max_dependencies')
          OR grant_row.max_claim_requirements <> json_extract(packet.packet_json, '$.management_grant.max_claim_requirements')
          OR grant_row.budget_tokens <> json_extract(packet.packet_json, '$.management_grant.budget.token_limit')
          OR grant_row.budget_cost_cents <> json_extract(packet.packet_json, '$.management_grant.budget.cost_cents')
          OR grant_row.budget_time_seconds <> json_extract(packet.packet_json, '$.management_grant.budget.time_seconds')
          OR COALESCE(grant_row.expires_at,'') <> COALESCE(json_extract(packet.packet_json, '$.management_grant.expires_at'),'')
          OR NOT EXISTS (
            SELECT 1 FROM launch_profiles invocation
            WHERE invocation.id=json_extract(packet.packet_json, '$.management_grant.invocation_launch_profile_id')
              AND invocation.revision=json_extract(packet.packet_json, '$.management_grant.invocation_launch_profile_revision')
              AND invocation.workspace_id=run.workspace_id AND invocation.project_id=run.project_id
              AND invocation.agent_id=run.agent_id AND invocation.agent_revision=grant_row.agent_revision
              AND invocation.manager_grant_id=grant_row.id AND invocation.status='active'
          )
          OR json_extract(packet.packet_json, '$.management_grant.owner_executive') IS NOT (
            SELECT EXISTS(
              SELECT 1 FROM owner_executive_bindings binding
              WHERE binding.workspace_id=run.workspace_id AND binding.project_id=run.project_id
                AND binding.objective_id=grant_row.objective_id AND binding.planning_task_id=run.task_id
                AND binding.agent_id=run.agent_id AND binding.manager_grant_id=grant_row.id
                AND binding.launch_profile_id=json_extract(packet.packet_json, '$.management_grant.invocation_launch_profile_id')
                AND binding.status='active'
            )
          )
          OR NOT EXISTS (
            SELECT 1 FROM objectives objective WHERE objective.id=grant_row.objective_id
              AND objective.revision=json_extract(packet.packet_json, '$.management_grant.objective_revision')
          )
          OR json_extract(packet.packet_json, '$.policy.allowed_tools') IS NOT (
            SELECT json_group_array(tool) FROM (
              SELECT 0 ordinal,'crewfold_acknowledge_context_delta' tool UNION ALL
              SELECT 1,'crewfold_acknowledge_message' UNION ALL SELECT 2,'crewfold_get_briefing' UNION ALL
              SELECT 3,'crewfold_get_context_delta' UNION ALL SELECT 4,'crewfold_get_status' UNION ALL
              SELECT 5,'crewfold_list_inbox' UNION ALL SELECT 6,'crewfold_propose_knowledge' UNION ALL
              SELECT 7,'crewfold_propose_completion' UNION ALL SELECT 8,'crewfold_publish_artifact' UNION ALL
              SELECT 9,'crewfold_read_message' UNION ALL SELECT 10,'crewfold_report_blocked' UNION ALL
              SELECT 11,'crewfold_report_contradiction' UNION ALL SELECT 12,'crewfold_report_progress' UNION ALL
              SELECT 13,'crewfold_send_message' UNION ALL
              SELECT 14,'crewfold_get_executive_context'
                WHERE json_extract(packet.packet_json, '$.management_grant.owner_executive')=1 UNION ALL
              SELECT 15,'crewfold_respond_to_owner'
                WHERE json_extract(packet.packet_json, '$.management_grant.owner_executive')=1 UNION ALL
              SELECT 16+kind.ordinal,CASE kind.kind WHEN 'assignment' THEN 'crewfold_propose_assignment'
                WHEN 'escalation' THEN 'crewfold_propose_escalation' WHEN 'review' THEN 'crewfold_propose_review'
                ELSE 'crewfold_propose_tasks' END
              FROM manager_grant_proposal_kinds kind WHERE kind.grant_id=grant_row.id
              ORDER BY ordinal
            )
          )
          OR run.assignment_id <> json_extract(packet.packet_json, '$.task.assignment_id')
        )
    ) THEN RAISE(ABORT, 'manager run binding grant is no longer exact') END;
    SELECT CASE WHEN EXISTS (
      SELECT 1 FROM runs run JOIN context_packets packet ON packet.id = NEW.context_packet_id
      LEFT JOIN check_watch_grants grant_row
        ON grant_row.id = json_extract(packet.packet_json, '$.check_watch_grant.grant_id')
      WHERE run.id = NEW.run_id AND json_type(packet.packet_json, '$.check_watch_grant') IS NOT NULL AND (
          grant_row.id IS NULL OR grant_row.status <> 'active'
          OR (grant_row.expires_at IS NOT NULL AND
            crewfold_timestamp_key(grant_row.expires_at) <= crewfold_timestamp_key(NEW.bound_at))
          OR grant_row.workspace_id <> run.workspace_id OR grant_row.project_id <> run.project_id
          OR grant_row.agent_id <> run.agent_id
          OR grant_row.revision <> json_extract(packet.packet_json, '$.check_watch_grant.grant_revision')
          OR grant_row.agent_revision <> json_extract(packet.packet_json, '$.check_watch_grant.watcher_agent_revision')
          OR json(grant_row.operations_json) <>
            json(json_extract(packet.packet_json, '$.check_watch_grant.operations'))
          OR json(grant_row.definitions_json) <>
            json(json_extract(packet.packet_json, '$.check_watch_grant.definitions'))
          OR grant_row.max_pending <> json_extract(packet.packet_json, '$.check_watch_grant.max_pending')
          OR grant_row.max_in_flight <> json_extract(packet.packet_json, '$.check_watch_grant.max_in_flight')
          OR COALESCE(grant_row.expires_at,'') <>
            COALESCE(json_extract(packet.packet_json, '$.check_watch_grant.expires_at'),'')
          OR grant_row.content_sha256 <> json_extract(packet.packet_json, '$.check_watch_grant.content_sha256')
          OR run.assignment_id <> json_extract(packet.packet_json, '$.task.assignment_id')
        )
    ) THEN RAISE(ABORT, 'check-watch run binding grant is no longer exact') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM events event JOIN runs run ON run.id = NEW.run_id
        JOIN context_packets packet ON packet.id = NEW.context_packet_id
        WHERE event.workspace_id = run.workspace_id AND event.entity_type = 'context_packet'
          AND event.entity_id = packet.id AND event.entity_revision = 1
          AND event.type = 'context.packet_built' AND event.actor_id = 'local-owner'
          AND event.actor_type = 'human' AND json_extract(event.data_json, '$.task_id') = run.task_id
          AND json_extract(event.data_json, '$.agent_id') = run.agent_id
          AND json_extract(event.data_json, '$.checkout_id') = run.checkout_id
          AND json_extract(event.data_json, '$.packet_schema') = json_extract(packet.packet_json, '$.schema')
          AND json_extract(event.data_json, '$.as_of_event_sequence') = json_extract(packet.packet_json, '$.as_of_event_sequence')
          AND json_extract(event.data_json, '$.content_hash') = packet.content_hash
          AND json_extract(event.data_json, '$.byte_size') = packet.byte_size
    ) THEN RAISE(ABORT, 'run context packet has no exact built event') END;
END;

CREATE TRIGGER check_definition_require_store_insert BEFORE INSERT ON check_definitions WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_definition_require_store_update BEFORE UPDATE ON check_definitions WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_definition_argument_require_store_insert BEFORE INSERT ON check_definition_arguments WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_requirement_require_store_insert BEFORE INSERT ON task_check_requirements WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_requirement_require_store_update BEFORE UPDATE ON task_check_requirements WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_grant_require_store_insert BEFORE INSERT ON check_watch_grants WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_grant_require_store_update BEFORE UPDATE ON check_watch_grants WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_grant_operation_require_store_insert BEFORE INSERT ON check_watch_grant_operations WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_grant_definition_require_store_insert BEFORE INSERT ON check_watch_grant_definitions WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_policy_require_store_update BEFORE UPDATE ON check_policies WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_route_require_store_insert BEFORE INSERT ON check_routes WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_route_require_store_update BEFORE UPDATE ON check_routes WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_run_require_store_insert BEFORE INSERT ON check_runs WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_run_require_store_update BEFORE UPDATE ON check_runs WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_job_require_store_insert BEFORE INSERT ON check_jobs WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_job_require_store_update BEFORE UPDATE ON check_jobs WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_launch_require_store_insert BEFORE INSERT ON check_launch_receipts WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_result_require_store_insert BEFORE INSERT ON check_results WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_artifact_require_store_insert BEFORE INSERT ON check_artifacts WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_freshness_require_store_insert BEFORE INSERT ON check_result_freshness WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER check_evidence_require_store_insert BEFORE INSERT ON check_requirement_evidence WHEN crewfold_check_mutation_seal_active() IS NOT 1 BEGIN SELECT RAISE(ABORT,'check write requires authenticated store construction'); END;

CREATE TRIGGER project_seed_check_watch_state AFTER INSERT ON projects BEGIN
 INSERT INTO check_watch_state(project_id,workspace_id,last_event_sequence,last_result_id,revision,created_at,updated_at) VALUES(NEW.id,NEW.workspace_id,0,'',1,NEW.created_at,NEW.created_at);
END;

CREATE TRIGGER check_watch_state_validate_update BEFORE UPDATE ON check_watch_state BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR NEW.project_id IS NOT OLD.project_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at OR NEW.last_event_sequence<OLD.last_event_sequence OR NEW.revision<>OLD.revision+1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at) OR (NEW.last_event_sequence<>0 AND NOT EXISTS(SELECT 1 FROM events WHERE workspace_id=NEW.workspace_id AND sequence=NEW.last_event_sequence)) OR EXISTS(SELECT 1 FROM events WHERE workspace_id=NEW.workspace_id AND sequence>OLD.last_event_sequence AND sequence<=NEW.last_event_sequence AND crewfold_check_watch_event_known(type) IS NOT 1) OR (NEW.last_result_id<>'' AND NOT EXISTS(SELECT 1 FROM check_results result JOIN check_runs run ON run.id=result.check_run_id WHERE result.id=NEW.last_result_id AND run.workspace_id=NEW.workspace_id AND run.project_id=NEW.project_id)) THEN RAISE(ABORT,'invalid check-watch cursor advance') END;
END;

CREATE TRIGGER check_watch_state_reject_delete BEFORE DELETE ON check_watch_state BEGIN SELECT RAISE(ABORT,'check-watch state is durable'); END;

CREATE TRIGGER check_watch_receipt_validate_insert BEFORE INSERT ON check_watch_receipts BEGIN
 SELECT CASE WHEN crewfold_check_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1 OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256 OR NOT EXISTS(SELECT 1 FROM check_watch_state state WHERE state.project_id=NEW.project_id AND state.workspace_id=NEW.workspace_id AND state.last_event_sequence=NEW.through_event_sequence) THEN RAISE(ABORT,'invalid immutable check-watch receipt') END;
END;

CREATE TRIGGER check_watch_receipt_reject_update BEFORE UPDATE ON check_watch_receipts BEGIN SELECT RAISE(ABORT,'check-watch receipts are immutable'); END;

CREATE TRIGGER check_watch_receipt_reject_delete BEFORE DELETE ON check_watch_receipts BEGIN SELECT RAISE(ABORT,'check-watch receipts are immutable'); END;

CREATE TABLE deliverable_commitments (
 id TEXT PRIMARY KEY CHECK(length(id)=42 AND substr(id,1,10)='outcommit_' AND substr(id,11) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
 objective_id TEXT NOT NULL REFERENCES objectives(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 commitment_key TEXT NOT NULL CHECK(length(CAST(commitment_key AS BLOB)) BETWEEN 1 AND 128 AND instr(commitment_key,char(0))=0),
 title TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 256 AND instr(title,char(0))=0),
 description TEXT NOT NULL CHECK(length(CAST(description AS BLOB))<=4096 AND instr(description,char(0))=0),
 acceptance_criteria_json TEXT NOT NULL CHECK(json_valid(acceptance_criteria_json) AND json_type(acceptance_criteria_json)='array' AND json_array_length(acceptance_criteria_json) BETWEEN 1 AND 32 AND length(CAST(acceptance_criteria_json AS BLOB))<=16384),
 content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json)='object' AND length(CAST(content_json AS BLOB))<=24576),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
 created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner'),
 UNIQUE(task_id,commitment_key)
) STRICT;

CREATE TABLE outcome_commitment_receipts (
 commitment_id TEXT PRIMARY KEY REFERENCES deliverable_commitments(id),
 event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence), created_at TEXT NOT NULL
) STRICT;

CREATE TABLE outcome_assessments (
 id TEXT PRIMARY KEY CHECK(length(id)=42 AND substr(id,1,10)='outassess_' AND substr(id,11) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), project_id TEXT NOT NULL REFERENCES projects(id),
 objective_id TEXT NOT NULL REFERENCES objectives(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 commitment_id TEXT NOT NULL REFERENCES deliverable_commitments(id), revision INTEGER NOT NULL CHECK(revision>0),
 state_revision INTEGER NOT NULL CHECK(state_revision>0), review_state TEXT NOT NULL CHECK(review_state IN ('proposed','accepted','rejected','superseded')),
 conclusion TEXT NOT NULL CHECK(conclusion IN ('achieved','partial','not_achieved','unknown')),
 delivered_scope_json TEXT NOT NULL CHECK(json_valid(delivered_scope_json) AND json_type(delivered_scope_json)='array' AND json_array_length(delivered_scope_json)<=32),
 unmet_scope_json TEXT NOT NULL CHECK(json_valid(unmet_scope_json) AND json_type(unmet_scope_json)='array' AND json_array_length(unmet_scope_json)<=32),
 content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json)='object' AND length(CAST(content_json AS BLOB))<=49152),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
 supersedes_assessment_id TEXT REFERENCES outcome_assessments(id),
 proposed_at TEXT NOT NULL, proposed_by TEXT NOT NULL CHECK(proposed_by='local-owner'),
 decided_at TEXT, decided_by TEXT CHECK(decided_by IS NULL OR decided_by='local-owner'), decision_note TEXT CHECK(decision_note IS NULL OR length(CAST(decision_note AS BLOB))<=4096),
 UNIQUE(commitment_id,revision), CHECK(supersedes_assessment_id IS NULL OR supersedes_assessment_id<>id),
 CHECK((review_state='proposed' AND state_revision=1 AND decided_at IS NULL AND decided_by IS NULL AND decision_note IS NULL)
    OR (review_state IN ('accepted','rejected','superseded') AND state_revision>=2 AND decided_at IS NOT NULL AND decided_by='local-owner'))
) STRICT;

CREATE UNIQUE INDEX outcome_assessment_one_proposed ON outcome_assessments(commitment_id) WHERE review_state='proposed';
CREATE UNIQUE INDEX outcome_assessment_one_current_accepted ON outcome_assessments(commitment_id) WHERE review_state='accepted';

CREATE TABLE outcome_assessment_decision_refs (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), revision_id TEXT NOT NULL REFERENCES knowledge_revisions(id),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64), event_sequence INTEGER NOT NULL REFERENCES events(sequence),
 PRIMARY KEY(assessment_id,ordinal), UNIQUE(assessment_id,revision_id)
) STRICT;

CREATE TABLE outcome_assessment_evidence_refs (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), source_type TEXT NOT NULL CHECK(source_type IN ('handoff','check_requirement_evidence')),
 source_id TEXT NOT NULL, source_revision INTEGER NOT NULL CHECK(source_revision>0),
 source_sha256 TEXT NOT NULL CHECK(length(source_sha256)=64), event_sequence INTEGER NOT NULL REFERENCES events(sequence),
 class TEXT NOT NULL CHECK(class IN ('agent_self_report','mechanical_check','independent_review')),
 effect TEXT NOT NULL CHECK(effect IN ('supports','contradicts','inconclusive')),
 pinned_freshness TEXT NOT NULL CHECK(pinned_freshness IN ('fresh','stale','unknown')),
 PRIMARY KEY(assessment_id,ordinal), UNIQUE(assessment_id,source_type,source_id)
) STRICT;

CREATE TABLE outcome_assessment_effects (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), kind TEXT NOT NULL CHECK(kind IN ('compatibility','stability')),
 direction TEXT NOT NULL CHECK(direction IN ('positive','neutral','negative','uncertain')),
 summary TEXT NOT NULL CHECK(length(CAST(summary AS BLOB)) BETWEEN 1 AND 2048 AND instr(summary,char(0))=0),
 PRIMARY KEY(assessment_id,ordinal)
) STRICT;

CREATE TABLE outcome_assessment_deviations (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), kind TEXT NOT NULL CHECK(kind IN ('scope_change','duplicate_work')),
 summary TEXT NOT NULL CHECK(length(CAST(summary AS BLOB)) BETWEEN 1 AND 2048 AND instr(summary,char(0))=0),
 related_task_id TEXT REFERENCES tasks(id), related_task_revision INTEGER CHECK(related_task_revision IS NULL OR related_task_revision>0),
 PRIMARY KEY(assessment_id,ordinal), CHECK((related_task_id IS NULL)=(related_task_revision IS NULL))
) STRICT;

CREATE TABLE outcome_assessment_risks (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), severity TEXT NOT NULL CHECK(severity IN ('low','medium','high','critical')),
 summary TEXT NOT NULL CHECK(length(CAST(summary AS BLOB)) BETWEEN 1 AND 2048 AND instr(summary,char(0))=0),
 mitigation TEXT NOT NULL CHECK(length(CAST(mitigation AS BLOB))<=2048 AND instr(mitigation,char(0))=0),
 PRIMARY KEY(assessment_id,ordinal)
) STRICT;

CREATE TABLE outcome_assessment_unknowns (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
 summary TEXT NOT NULL CHECK(length(CAST(summary AS BLOB)) BETWEEN 1 AND 2048 AND instr(summary,char(0))=0),
 PRIMARY KEY(assessment_id,ordinal)
) STRICT;

CREATE TABLE outcome_assessment_follow_up_tasks (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL CHECK(task_revision>0), event_sequence INTEGER NOT NULL REFERENCES events(sequence),
 PRIMARY KEY(assessment_id,ordinal), UNIQUE(assessment_id,task_id)
) STRICT;

CREATE TABLE outcome_assessment_owner_attention (
 assessment_id TEXT NOT NULL REFERENCES outcome_assessments(id) DEFERRABLE INITIALLY DEFERRED,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), urgency TEXT NOT NULL CHECK(urgency IN ('now','next','later')),
 action TEXT NOT NULL CHECK(length(CAST(action AS BLOB)) BETWEEN 1 AND 2048 AND instr(action,char(0))=0),
 reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 2048 AND instr(reason,char(0))=0),
 PRIMARY KEY(assessment_id,ordinal)
) STRICT;

CREATE TABLE outcome_assessment_submissions (
 assessment_id TEXT PRIMARY KEY REFERENCES outcome_assessments(id), event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
 child_count INTEGER NOT NULL CHECK(child_count BETWEEN 0 AND 256), submitted_at TEXT NOT NULL
) STRICT;

CREATE TABLE outcome_assessment_governance (
 assessment_id TEXT PRIMARY KEY REFERENCES outcome_assessments(id), decision TEXT NOT NULL CHECK(decision IN ('accepted','rejected')),
 decision_event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence), superseded_assessment_id TEXT REFERENCES outcome_assessments(id),
 superseded_event_sequence INTEGER UNIQUE REFERENCES events(sequence), decided_at TEXT NOT NULL,
 CHECK((superseded_assessment_id IS NULL)=(superseded_event_sequence IS NULL))
) STRICT;

CREATE TABLE outcome_assessment_acceptance_basis (
 assessment_id TEXT PRIMARY KEY REFERENCES outcome_assessments(id), event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
 source_sha256 TEXT NOT NULL CHECK(length(source_sha256)=64), created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner')
) STRICT;

CREATE TABLE owner_checkpoints (
 id TEXT PRIMARY KEY CHECK(length(id)=40 AND substr(id,1,8)='outcpnt_' AND substr(id,9) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), scope_type TEXT NOT NULL CHECK(scope_type IN ('task','objective','project','workspace')),
 scope_id TEXT NOT NULL, event_sequence INTEGER NOT NULL UNIQUE REFERENCES events(sequence),
 created_at TEXT NOT NULL, created_by TEXT NOT NULL CHECK(created_by='local-owner')
) STRICT;

CREATE TABLE outcome_projector_state (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id), last_event_sequence INTEGER NOT NULL CHECK(last_event_sequence>=0),
 revision INTEGER NOT NULL CHECK(revision>0), updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE management_briefings (
 id TEXT PRIMARY KEY CHECK(length(id)=41 AND substr(id,1,9)='briefing_' AND substr(id,10) NOT GLOB '*[^0-9a-f]*'),
 revision INTEGER NOT NULL CHECK(revision>0), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 scope_type TEXT NOT NULL CHECK(scope_type IN ('task','objective','project','workspace')), scope_id TEXT NOT NULL,
 event_cursor INTEGER NOT NULL CHECK(event_cursor>=0), cutoff_event_sequence INTEGER NOT NULL CHECK(cutoff_event_sequence>=event_cursor),
 checkpoint_id TEXT NOT NULL, since_event_sequence INTEGER NOT NULL CHECK(since_event_sequence>=0), evaluated_at TEXT NOT NULL,
 caught_up INTEGER NOT NULL CHECK(caught_up IN (0,1)), unknown_event_type TEXT, unknown_event_sequence INTEGER,
 content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json)='object' AND length(CAST(content_json AS BLOB))<=65536),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64), byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 65536),
 created_at TEXT NOT NULL, UNIQUE(workspace_id,scope_type,scope_id,event_cursor,checkpoint_id,content_sha256),
 CHECK((unknown_event_type IS NULL)=(unknown_event_sequence IS NULL))
) STRICT;

CREATE TABLE management_briefing_claims (
 briefing_id TEXT NOT NULL REFERENCES management_briefings(id), ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 127),
 claim_id TEXT NOT NULL CHECK(length(claim_id)=71 AND substr(claim_id,1,7)='bclaim_' AND substr(claim_id,8) NOT GLOB '*[^0-9a-f]*'), semantic_key TEXT NOT NULL CHECK(length(CAST(semantic_key AS BLOB)) BETWEEN 1 AND 256), kind TEXT NOT NULL CHECK(kind IN ('required_decision','contradiction','risk','unknown','verification_gap','deviation','unmet_commitment','accepted_delivery','rationale','change')),
 urgency TEXT NOT NULL CHECK(urgency IN ('now','next','later')), summary TEXT NOT NULL, status TEXT NOT NULL,
 project_id TEXT, source_event_sequence INTEGER NOT NULL CHECK(source_event_sequence>0), claim_json TEXT NOT NULL CHECK(json_valid(claim_json)),
 PRIMARY KEY(briefing_id,ordinal), UNIQUE(briefing_id,claim_id)
) STRICT;

CREATE TABLE management_briefing_claim_sources (
 briefing_id TEXT NOT NULL, claim_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
 entity_type TEXT NOT NULL CHECK(entity_type IN ('outcome_assessment','deliverable_commitment','outcome_assessment_acceptance_basis','knowledge_contradiction','knowledge_revision','check_requirement_evidence','task','handoff')), entity_id TEXT NOT NULL, entity_revision INTEGER NOT NULL CHECK(entity_revision>0),
 content_sha256 TEXT NOT NULL CHECK(content_sha256='' OR (length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*')), event_sequence INTEGER NOT NULL REFERENCES events(sequence),
 evidence_class TEXT NOT NULL CHECK(evidence_class IN ('','agent_self_report','mechanical_check','independent_review')),
 evidence_effect TEXT NOT NULL CHECK(evidence_effect IN ('','supports','contradicts','inconclusive')),
 pinned_freshness TEXT NOT NULL CHECK(pinned_freshness IN ('','fresh','stale','unknown')),
 current_freshness TEXT NOT NULL CHECK(current_freshness IN ('','fresh','stale','unknown')),
 PRIMARY KEY(briefing_id,claim_id,ordinal), FOREIGN KEY(briefing_id,claim_id) REFERENCES management_briefing_claims(briefing_id,claim_id)
 ,CHECK((entity_type IN ('handoff','check_requirement_evidence') AND evidence_class<>'' AND evidence_effect<>'' AND pinned_freshness<>'' AND current_freshness<>'') OR (entity_type NOT IN ('handoff','check_requirement_evidence') AND evidence_class='' AND evidence_effect='' AND pinned_freshness='' AND current_freshness=''))
) STRICT;

CREATE TABLE management_briefing_receipts (
 briefing_id TEXT PRIMARY KEY REFERENCES management_briefings(id),
 claim_count INTEGER NOT NULL CHECK(claim_count BETWEEN 0 AND 128),
 source_count INTEGER NOT NULL CHECK(source_count BETWEEN 0 AND 4096),
 sealed_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER outcome_commitment_require_store_insert BEFORE INSERT ON deliverable_commitments BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1
  OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256
  OR json_extract(NEW.content_json,'$.workspace_id') IS NOT NEW.workspace_id
  OR json_extract(NEW.content_json,'$.project_id') IS NOT NEW.project_id
  OR json_extract(NEW.content_json,'$.objective_id') IS NOT NEW.objective_id
  OR json_extract(NEW.content_json,'$.task_id') IS NOT NEW.task_id
  OR json_extract(NEW.content_json,'$.key') IS NOT NEW.commitment_key
  OR json_extract(NEW.content_json,'$.title') IS NOT NEW.title
  OR COALESCE(json_extract(NEW.content_json,'$.description'),'') IS NOT NEW.description
  OR json(json_extract(NEW.content_json,'$.acceptance_criteria')) IS NOT json(NEW.acceptance_criteria_json)
  OR NOT EXISTS(SELECT 1 FROM tasks task JOIN objectives objective ON objective.id=task.objective_id
    WHERE task.id=NEW.task_id AND task.workspace_id=NEW.workspace_id AND task.project_id=NEW.project_id
      AND task.objective_id=NEW.objective_id AND objective.workspace_id=NEW.workspace_id
      AND objective.project_id=NEW.project_id AND objective.status='active'
      AND task.status IN ('ready','assigned') AND NOT EXISTS(SELECT 1 FROM runs run WHERE run.task_id=task.id))
 THEN RAISE(ABORT,'invalid canonical deliverable commitment construction') END;
END;
CREATE TRIGGER outcome_commitment_reject_update BEFORE UPDATE ON deliverable_commitments BEGIN SELECT RAISE(ABORT,'deliverable commitments are immutable'); END;
CREATE TRIGGER outcome_commitment_reject_delete BEFORE DELETE ON deliverable_commitments BEGIN SELECT RAISE(ABORT,'deliverable commitments are immutable'); END;
CREATE TRIGGER outcome_commitment_receipt_require_store_insert BEFORE INSERT ON outcome_commitment_receipts BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM deliverable_commitments commitment JOIN events event ON event.sequence=NEW.event_sequence
   WHERE commitment.id=NEW.commitment_id AND commitment.created_at=NEW.created_at
    AND event.workspace_id=commitment.workspace_id AND event.entity_type='deliverable_commitment'
    AND event.entity_id=commitment.id AND event.entity_revision=1 AND event.type='outcome.commitment_created'
    AND event.occurred_at=commitment.created_at AND event.recorded_at=commitment.created_at
    AND event.actor_id='local-owner' AND event.actor_type='human'
    AND json_type(event.data_json)='object' AND (SELECT COUNT(*) FROM json_each(event.data_json))=5
    AND json_extract(event.data_json,'$.content_sha256')=commitment.content_sha256
    AND json_extract(event.data_json,'$.key')=commitment.commitment_key
    AND json_extract(event.data_json,'$.objective_id')=commitment.objective_id
    AND json_extract(event.data_json,'$.project_id')=commitment.project_id
    AND json_extract(event.data_json,'$.task_id')=commitment.task_id)
 THEN RAISE(ABORT,'invalid deliverable commitment event receipt') END;
END;
CREATE TRIGGER outcome_commitment_receipt_reject_update BEFORE UPDATE ON outcome_commitment_receipts BEGIN SELECT RAISE(ABORT,'outcome receipts are immutable'); END;
CREATE TRIGGER outcome_commitment_receipt_reject_delete BEFORE DELETE ON outcome_commitment_receipts BEGIN SELECT RAISE(ABORT,'outcome receipts are immutable'); END;

CREATE TRIGGER outcome_assessment_require_store_insert BEFORE INSERT ON outcome_assessments BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.proposed_at) IS NOT 1
  OR NEW.review_state<>'proposed' OR NEW.state_revision<>1 OR NEW.proposed_by<>'local-owner'
  OR NEW.revision<>COALESCE((SELECT MAX(revision)+1 FROM outcome_assessments WHERE commitment_id=NEW.commitment_id),1)
  OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256
  OR json_extract(NEW.content_json,'$.workspace_id') IS NOT NEW.workspace_id
  OR json_extract(NEW.content_json,'$.project_id') IS NOT NEW.project_id
  OR json_extract(NEW.content_json,'$.objective_id') IS NOT NEW.objective_id
  OR json_extract(NEW.content_json,'$.task_id') IS NOT NEW.task_id
  OR json_extract(NEW.content_json,'$.commitment_id') IS NOT NEW.commitment_id
  OR json_extract(NEW.content_json,'$.revision') IS NOT NEW.revision
  OR COALESCE(json_extract(NEW.content_json,'$.supersedes_assessment_id'),'') IS NOT COALESCE(NEW.supersedes_assessment_id,'')
  OR json_extract(NEW.content_json,'$.assessment.conclusion') IS NOT NEW.conclusion
  OR json(json_extract(NEW.content_json,'$.assessment.delivered_scope')) IS NOT json(NEW.delivered_scope_json)
  OR json(json_extract(NEW.content_json,'$.assessment.unmet_scope')) IS NOT json(NEW.unmet_scope_json)
  OR NOT EXISTS(SELECT 1 FROM deliverable_commitments commitment WHERE commitment.id=NEW.commitment_id
    AND commitment.workspace_id=NEW.workspace_id AND commitment.project_id=NEW.project_id
    AND commitment.objective_id=NEW.objective_id AND commitment.task_id=NEW.task_id)
  OR (NEW.supersedes_assessment_id IS NULL AND EXISTS(SELECT 1 FROM outcome_assessments current WHERE current.commitment_id=NEW.commitment_id AND current.review_state='accepted'))
  OR (NEW.supersedes_assessment_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM outcome_assessments current WHERE current.id=NEW.supersedes_assessment_id AND current.commitment_id=NEW.commitment_id AND current.review_state='accepted'))
  OR (NEW.conclusion='achieved' AND (json_array_length(NEW.delivered_scope_json)=0 OR json_array_length(NEW.unmet_scope_json)<>0))
  OR (NEW.conclusion='partial' AND (json_array_length(NEW.delivered_scope_json)=0 OR json_array_length(NEW.unmet_scope_json)=0))
  OR (NEW.conclusion='not_achieved' AND (json_array_length(NEW.delivered_scope_json)<>0 OR json_array_length(NEW.unmet_scope_json)=0))
  OR (NEW.conclusion='unknown' AND json_array_length(json_extract(NEW.content_json,'$.assessment.unknowns'))=0)
 THEN RAISE(ABORT,'invalid canonical outcome assessment construction') END;
END;
CREATE TRIGGER outcome_assessment_validate_update BEFORE UPDATE ON outcome_assessments BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1
 OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.project_id<>OLD.project_id OR NEW.objective_id<>OLD.objective_id OR NEW.task_id<>OLD.task_id OR NEW.commitment_id<>OLD.commitment_id OR NEW.revision<>OLD.revision
 OR NEW.conclusion<>OLD.conclusion OR NEW.delivered_scope_json<>OLD.delivered_scope_json OR NEW.unmet_scope_json<>OLD.unmet_scope_json OR NEW.content_json<>OLD.content_json OR NEW.content_sha256<>OLD.content_sha256
 OR COALESCE(NEW.supersedes_assessment_id,'')<>COALESCE(OLD.supersedes_assessment_id,'') OR NEW.proposed_at<>OLD.proposed_at OR NEW.proposed_by<>OLD.proposed_by
 OR NOT ((OLD.review_state='proposed' AND NEW.review_state IN ('accepted','rejected') AND NEW.state_revision=2)
      OR (OLD.review_state='accepted' AND NEW.review_state='superseded' AND NEW.state_revision=OLD.state_revision+1 AND NEW.decided_at=OLD.decided_at AND NEW.decided_by=OLD.decided_by AND COALESCE(NEW.decision_note,'')=COALESCE(OLD.decision_note,'')))
 THEN RAISE(ABORT,'invalid outcome assessment governance transition') END;
END;
CREATE TRIGGER outcome_assessment_reject_delete BEFORE DELETE ON outcome_assessments BEGIN SELECT RAISE(ABORT,'outcome assessments are durable'); END;

CREATE TRIGGER outcome_decision_ref_require_store_insert BEFORE INSERT ON outcome_assessment_decision_refs BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_decision_refs WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id AND json_extract(assessment.content_json,'$.assessment.decision_revision_ids['||NEW.ordinal||']')=NEW.revision_id)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN knowledge_revisions revision ON revision.id=NEW.revision_id JOIN knowledge_items item ON item.id=revision.item_id JOIN events event ON event.sequence=NEW.event_sequence
   WHERE assessment.id=NEW.assessment_id AND revision.review_status='accepted' AND revision.content_hash=NEW.content_sha256
    AND item.workspace_id=assessment.workspace_id AND item.project_id=assessment.project_id AND item.type='decision'
    AND (item.task_scope_id IS NULL OR item.task_scope_id=assessment.task_id)
    AND event.workspace_id=assessment.workspace_id AND event.entity_type='knowledge_revision' AND event.entity_id=revision.id AND event.type='knowledge.accepted')
 THEN RAISE(ABORT,'invalid outcome decision reference') END;
END;
CREATE TRIGGER outcome_evidence_ref_require_store_insert BEFORE INSERT ON outcome_assessment_evidence_refs BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_evidence_refs WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.evidence['||NEW.ordinal||'].source_type')=NEW.source_type
    AND json_extract(assessment.content_json,'$.assessment.evidence['||NEW.ordinal||'].source_id')=NEW.source_id)
  OR (NEW.source_type='handoff' AND NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN run_handoffs handoff ON handoff.id=NEW.source_id JOIN runs run ON run.id=handoff.run_id JOIN events event ON event.sequence=NEW.event_sequence
    WHERE assessment.id=NEW.assessment_id AND run.workspace_id=assessment.workspace_id AND run.project_id=assessment.project_id AND run.status='completed' AND event.workspace_id=assessment.workspace_id
      AND event.entity_type='task' AND event.type='task.handoff_recorded' AND json_extract(event.data_json,'$.handoff_id')=handoff.id
      AND event.entity_revision=NEW.source_revision AND NEW.effect='supports' AND NEW.pinned_freshness='fresh'
      AND NEW.source_sha256=lower(hex(sha256(CAST(json_object('created_at',handoff.created_at,'evidence_json',json(handoff.evidence_json),'id',handoff.id,'run_id',handoff.run_id,'summary',handoff.summary,'task_id',handoff.task_id) AS BLOB))))
      AND ((handoff.task_id=assessment.task_id AND NEW.class='agent_self_report') OR (handoff.task_id<>assessment.task_id AND NEW.class='independent_review' AND EXISTS(
       SELECT 1 FROM manager_proposal_effects effect JOIN manager_proposal_actions action ON action.id=effect.action_id AND action.proposal_id=effect.proposal_id
       JOIN manager_proposals proposal ON proposal.id=effect.proposal_id
       WHERE effect.entity_type='task' AND effect.entity_id=handoff.task_id AND effect.effect_type='created'
        AND action.type='request_review' AND proposal.status='accepted' AND proposal.workspace_id=assessment.workspace_id
        AND json_extract(action.payload_json,'$.task.task_id')=assessment.task_id)))))
  OR (NEW.source_type='check_requirement_evidence' AND NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN check_requirement_evidence evidence ON evidence.id=NEW.source_id JOIN check_results result ON result.id=evidence.check_result_id JOIN check_runs run ON run.id=result.check_run_id JOIN check_result_freshness fresh ON fresh.check_result_id=result.id AND fresh.revision=evidence.freshness_revision JOIN events event ON event.sequence=NEW.event_sequence
    WHERE assessment.id=NEW.assessment_id AND run.workspace_id=assessment.workspace_id AND run.project_id=assessment.project_id AND run.task_id=assessment.task_id
      AND evidence.freshness_revision=NEW.source_revision AND evidence.class=NEW.class AND evidence.effect=NEW.effect AND fresh.status=NEW.pinned_freshness
      AND NEW.source_sha256=lower(hex(sha256(CAST(json_object('check_result_id',evidence.check_result_id,'class',evidence.class,'effect',evidence.effect,'freshness_revision',evidence.freshness_revision,'id',evidence.id,'pinned_freshness',fresh.status,'requirement_id',evidence.requirement_id,'requirement_revision',evidence.requirement_revision) AS BLOB))))
      AND event.workspace_id=assessment.workspace_id AND event.entity_type='check_result' AND event.entity_id=result.id AND event.entity_revision=evidence.freshness_revision
      AND event.type IN ('check.result_recorded','check.freshness_observed','check.freshness_stale')))
 THEN RAISE(ABORT,'invalid outcome evidence reference') END;
END;
CREATE TRIGGER outcome_effect_require_store_insert BEFORE INSERT ON outcome_assessment_effects BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_effects WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.effects['||NEW.ordinal||'].kind')=NEW.kind
    AND json_extract(assessment.content_json,'$.assessment.effects['||NEW.ordinal||'].direction')=NEW.direction
    AND json_extract(assessment.content_json,'$.assessment.effects['||NEW.ordinal||'].summary')=NEW.summary)
 THEN RAISE(ABORT,'invalid normalized outcome effect') END;
END;
CREATE TRIGGER outcome_deviation_require_store_insert BEFORE INSERT ON outcome_assessment_deviations BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_deviations WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.deviations['||NEW.ordinal||'].kind')=NEW.kind
    AND json_extract(assessment.content_json,'$.assessment.deviations['||NEW.ordinal||'].summary')=NEW.summary
    AND COALESCE(json_extract(assessment.content_json,'$.assessment.deviations['||NEW.ordinal||'].related_task_id'),'')=COALESCE(NEW.related_task_id,''))
  OR (NEW.related_task_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN tasks task ON task.id=NEW.related_task_id
    WHERE assessment.id=NEW.assessment_id AND task.workspace_id=assessment.workspace_id AND task.project_id=assessment.project_id AND task.revision=NEW.related_task_revision))
 THEN RAISE(ABORT,'invalid normalized outcome deviation') END;
END;
CREATE TRIGGER outcome_risk_require_store_insert BEFORE INSERT ON outcome_assessment_risks BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_risks WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.risks['||NEW.ordinal||'].severity')=NEW.severity
    AND json_extract(assessment.content_json,'$.assessment.risks['||NEW.ordinal||'].summary')=NEW.summary
    AND COALESCE(json_extract(assessment.content_json,'$.assessment.risks['||NEW.ordinal||'].mitigation'),'')=NEW.mitigation)
 THEN RAISE(ABORT,'invalid normalized outcome risk') END;
END;
CREATE TRIGGER outcome_unknown_require_store_insert BEFORE INSERT ON outcome_assessment_unknowns BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_unknowns WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.unknowns['||NEW.ordinal||'].summary')=NEW.summary)
 THEN RAISE(ABORT,'invalid normalized outcome unknown') END;
END;
CREATE TRIGGER outcome_follow_up_require_store_insert BEFORE INSERT ON outcome_assessment_follow_up_tasks BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_follow_up_tasks WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN tasks task ON task.id=NEW.task_id WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.follow_up_task_ids['||NEW.ordinal||']')=NEW.task_id
    AND task.workspace_id=assessment.workspace_id AND task.project_id=assessment.project_id AND task.revision=NEW.task_revision
    AND EXISTS(SELECT 1 FROM events event WHERE event.sequence=NEW.event_sequence AND event.workspace_id=task.workspace_id AND event.entity_type='task' AND event.entity_id=task.id AND event.entity_revision=task.revision))
 THEN RAISE(ABORT,'invalid normalized outcome follow-up task') END;
END;
CREATE TRIGGER outcome_attention_require_store_insert BEFORE INSERT ON outcome_assessment_owner_attention BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM outcome_assessment_owner_attention WHERE assessment_id=NEW.assessment_id),0)
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND json_extract(assessment.content_json,'$.assessment.owner_attention['||NEW.ordinal||'].urgency')=NEW.urgency
    AND json_extract(assessment.content_json,'$.assessment.owner_attention['||NEW.ordinal||'].action')=NEW.action
    AND json_extract(assessment.content_json,'$.assessment.owner_attention['||NEW.ordinal||'].reason')=NEW.reason)
 THEN RAISE(ABORT,'invalid normalized outcome owner attention') END;
END;
CREATE TRIGGER outcome_submission_require_store_insert BEFORE INSERT ON outcome_assessment_submissions BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.submitted_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
   AND json_type(assessment.content_json,'$.assessment.decision_revision_ids')='array'
   AND json_type(assessment.content_json,'$.assessment.evidence')='array'
   AND json_type(assessment.content_json,'$.assessment.effects')='array'
   AND json_type(assessment.content_json,'$.assessment.deviations')='array'
   AND json_type(assessment.content_json,'$.assessment.risks')='array'
   AND json_type(assessment.content_json,'$.assessment.unknowns')='array'
   AND json_type(assessment.content_json,'$.assessment.follow_up_task_ids')='array'
   AND json_type(assessment.content_json,'$.assessment.owner_attention')='array'
   AND json_array_length(assessment.content_json,'$.assessment.decision_revision_ids')=(SELECT COUNT(*) FROM outcome_assessment_decision_refs WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.evidence')=(SELECT COUNT(*) FROM outcome_assessment_evidence_refs WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.effects')=(SELECT COUNT(*) FROM outcome_assessment_effects WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.deviations')=(SELECT COUNT(*) FROM outcome_assessment_deviations WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.risks')=(SELECT COUNT(*) FROM outcome_assessment_risks WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.unknowns')=(SELECT COUNT(*) FROM outcome_assessment_unknowns WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.follow_up_task_ids')=(SELECT COUNT(*) FROM outcome_assessment_follow_up_tasks WHERE assessment_id=NEW.assessment_id)
   AND json_array_length(assessment.content_json,'$.assessment.owner_attention')=(SELECT COUNT(*) FROM outcome_assessment_owner_attention WHERE assessment_id=NEW.assessment_id)
   AND NEW.child_count=json_array_length(assessment.content_json,'$.assessment.decision_revision_ids')
                      +json_array_length(assessment.content_json,'$.assessment.evidence')
                      +json_array_length(assessment.content_json,'$.assessment.effects')
                      +json_array_length(assessment.content_json,'$.assessment.deviations')
                      +json_array_length(assessment.content_json,'$.assessment.risks')
                      +json_array_length(assessment.content_json,'$.assessment.unknowns')
                      +json_array_length(assessment.content_json,'$.assessment.follow_up_task_ids')
                      +json_array_length(assessment.content_json,'$.assessment.owner_attention'))
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN events event ON event.sequence=NEW.event_sequence
   WHERE assessment.id=NEW.assessment_id AND assessment.proposed_at=NEW.submitted_at
    AND event.workspace_id=assessment.workspace_id AND event.entity_type='outcome_assessment' AND event.entity_id=assessment.id
    AND event.entity_revision=1 AND event.type='outcome.assessment_proposed' AND event.occurred_at=assessment.proposed_at
    AND event.recorded_at=assessment.proposed_at AND event.actor_id='local-owner' AND event.actor_type='human'
    AND json_type(event.data_json)='object' AND (SELECT COUNT(*) FROM json_each(event.data_json))=6
    AND json_extract(event.data_json,'$.assessment_revision')=assessment.revision
    AND json_extract(event.data_json,'$.commitment_id')=assessment.commitment_id
    AND json_extract(event.data_json,'$.conclusion')=assessment.conclusion
    AND json_extract(event.data_json,'$.content_sha256')=assessment.content_sha256
    AND COALESCE(json_extract(event.data_json,'$.supersedes_assessment_id'),'')=COALESCE(assessment.supersedes_assessment_id,'')
    AND json_extract(event.data_json,'$.task_id')=assessment.task_id)
 THEN RAISE(ABORT,'invalid outcome assessment submission receipt') END;
END;
CREATE TRIGGER outcome_governance_require_store_insert BEFORE INSERT ON outcome_assessment_governance BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.decided_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN outcome_assessment_submissions submission ON submission.assessment_id=assessment.id JOIN events event ON event.sequence=NEW.decision_event_sequence
   WHERE assessment.id=NEW.assessment_id AND assessment.review_state=NEW.decision AND assessment.state_revision=2
    AND assessment.decided_at=NEW.decided_at AND event.workspace_id=assessment.workspace_id
    AND event.entity_type='outcome_assessment' AND event.entity_id=assessment.id AND event.entity_revision=2
    AND event.type=CASE NEW.decision WHEN 'accepted' THEN 'outcome.assessment_accepted' ELSE 'outcome.assessment_rejected' END
    AND submission.event_sequence<NEW.decision_event_sequence
    AND event.occurred_at=NEW.decided_at AND event.recorded_at=NEW.decided_at
    AND event.actor_id='local-owner' AND event.actor_type='human'
    AND json_type(event.data_json)='object' AND (SELECT COUNT(*) FROM json_each(event.data_json))=4
    AND json_extract(event.data_json,'$.assessment_revision')=assessment.revision
    AND json_extract(event.data_json,'$.commitment_id')=assessment.commitment_id
    AND json_extract(event.data_json,'$.conclusion')=assessment.conclusion
    AND COALESCE(json_extract(event.data_json,'$.decision_note'),'')=COALESCE(assessment.decision_note,'')
    AND (NEW.decision<>'accepted' OR assessment.conclusion NOT IN ('achieved','partial') OR EXISTS(
      SELECT 1 FROM outcome_assessment_evidence_refs evidence WHERE evidence.assessment_id=assessment.id AND evidence.effect='supports')))
  OR (NEW.decision='accepted' AND NOT EXISTS(SELECT 1 FROM outcome_assessment_acceptance_basis basis JOIN outcome_assessments assessment ON assessment.id=basis.assessment_id
    WHERE basis.assessment_id=NEW.assessment_id AND basis.event_sequence=NEW.decision_event_sequence
     AND basis.created_at=NEW.decided_at AND basis.created_by='local-owner'
     AND basis.source_sha256=crewfold_outcome_acceptance_basis_sha(assessment.id,assessment.content_sha256,NEW.decision_event_sequence,2)))
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.assessment_id
    AND ((NEW.decision='accepted' AND assessment.supersedes_assessment_id IS NOT NULL
      AND NEW.superseded_assessment_id=assessment.supersedes_assessment_id AND NEW.superseded_event_sequence IS NOT NULL)
     OR ((NEW.decision<>'accepted' OR assessment.supersedes_assessment_id IS NULL)
      AND NEW.superseded_assessment_id IS NULL AND NEW.superseded_event_sequence IS NULL)))
  OR (NEW.superseded_assessment_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM outcome_assessments successor JOIN outcome_assessments prior ON prior.id=NEW.superseded_assessment_id JOIN events event ON event.sequence=NEW.superseded_event_sequence
   WHERE successor.id=NEW.assessment_id AND successor.supersedes_assessment_id=prior.id AND successor.commitment_id=prior.commitment_id
    AND successor.review_state='accepted' AND prior.review_state='superseded' AND event.workspace_id=successor.workspace_id
    AND event.entity_type='outcome_assessment' AND event.entity_id=prior.id AND event.entity_revision=prior.state_revision
    AND event.type='outcome.assessment_superseded' AND event.occurred_at=NEW.decided_at
    AND event.recorded_at=NEW.decided_at AND event.actor_id='local-owner' AND event.actor_type='human'
    AND json_type(event.data_json)='object' AND (SELECT COUNT(*) FROM json_each(event.data_json))=2
    AND json_extract(event.data_json,'$.commitment_id')=successor.commitment_id
    AND json_extract(event.data_json,'$.successor_assessment_id')=successor.id))
 THEN RAISE(ABORT,'invalid outcome assessment governance receipt') END;
END;
CREATE TRIGGER outcome_acceptance_basis_require_store_insert BEFORE INSERT ON outcome_assessment_acceptance_basis BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM outcome_assessments assessment JOIN events event ON event.sequence=NEW.event_sequence
   WHERE assessment.id=NEW.assessment_id AND assessment.review_state='accepted' AND assessment.state_revision=2
    AND event.workspace_id=assessment.workspace_id AND event.entity_type='outcome_assessment' AND event.entity_id=assessment.id
    AND event.entity_revision=2 AND event.type='outcome.assessment_accepted' AND event.occurred_at=NEW.created_at)
  OR NEW.source_sha256<>crewfold_outcome_acceptance_basis_sha(NEW.assessment_id,(SELECT content_sha256 FROM outcome_assessments WHERE id=NEW.assessment_id),NEW.event_sequence,2)
 THEN RAISE(ABORT,'invalid outcome acceptance basis') END;
END;

CREATE TRIGGER outcome_decision_ref_immutable_update BEFORE UPDATE ON outcome_assessment_decision_refs BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_decision_ref_immutable_delete BEFORE DELETE ON outcome_assessment_decision_refs BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_evidence_ref_immutable_update BEFORE UPDATE ON outcome_assessment_evidence_refs BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_evidence_ref_immutable_delete BEFORE DELETE ON outcome_assessment_evidence_refs BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_effect_immutable_update BEFORE UPDATE ON outcome_assessment_effects BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_effect_immutable_delete BEFORE DELETE ON outcome_assessment_effects BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_deviation_immutable_update BEFORE UPDATE ON outcome_assessment_deviations BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_deviation_immutable_delete BEFORE DELETE ON outcome_assessment_deviations BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_risk_immutable_update BEFORE UPDATE ON outcome_assessment_risks BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_risk_immutable_delete BEFORE DELETE ON outcome_assessment_risks BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_unknown_immutable_update BEFORE UPDATE ON outcome_assessment_unknowns BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_unknown_immutable_delete BEFORE DELETE ON outcome_assessment_unknowns BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_follow_up_immutable_update BEFORE UPDATE ON outcome_assessment_follow_up_tasks BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_follow_up_immutable_delete BEFORE DELETE ON outcome_assessment_follow_up_tasks BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_attention_immutable_update BEFORE UPDATE ON outcome_assessment_owner_attention BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_attention_immutable_delete BEFORE DELETE ON outcome_assessment_owner_attention BEGIN SELECT RAISE(ABORT,'outcome children are immutable'); END;
CREATE TRIGGER outcome_submission_immutable_update BEFORE UPDATE ON outcome_assessment_submissions BEGIN SELECT RAISE(ABORT,'outcome submissions are immutable'); END;
CREATE TRIGGER outcome_submission_immutable_delete BEFORE DELETE ON outcome_assessment_submissions BEGIN SELECT RAISE(ABORT,'outcome submissions are immutable'); END;
CREATE TRIGGER outcome_governance_immutable_update BEFORE UPDATE ON outcome_assessment_governance BEGIN SELECT RAISE(ABORT,'outcome governance is immutable'); END;
CREATE TRIGGER outcome_governance_immutable_delete BEFORE DELETE ON outcome_assessment_governance BEGIN SELECT RAISE(ABORT,'outcome governance is immutable'); END;
CREATE TRIGGER outcome_acceptance_basis_immutable_update BEFORE UPDATE ON outcome_assessment_acceptance_basis BEGIN SELECT RAISE(ABORT,'outcome acceptance basis is immutable'); END;
CREATE TRIGGER outcome_acceptance_basis_immutable_delete BEFORE DELETE ON outcome_assessment_acceptance_basis BEGIN SELECT RAISE(ABORT,'outcome acceptance basis is immutable'); END;

CREATE TRIGGER owner_checkpoint_require_store_insert BEFORE INSERT ON owner_checkpoints BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
  OR (NEW.scope_type='workspace' AND NEW.scope_id<>NEW.workspace_id)
  OR (NEW.scope_type='project' AND NOT EXISTS(SELECT 1 FROM projects project WHERE project.id=NEW.scope_id AND project.workspace_id=NEW.workspace_id))
  OR (NEW.scope_type='objective' AND NOT EXISTS(SELECT 1 FROM objectives objective WHERE objective.id=NEW.scope_id AND objective.workspace_id=NEW.workspace_id))
  OR (NEW.scope_type='task' AND NOT EXISTS(SELECT 1 FROM tasks task WHERE task.id=NEW.scope_id AND task.workspace_id=NEW.workspace_id AND task.objective_id IS NOT NULL))
  OR NOT EXISTS(SELECT 1 FROM events event WHERE event.sequence=NEW.event_sequence AND event.workspace_id=NEW.workspace_id
    AND event.entity_type='owner_checkpoint' AND event.entity_id=NEW.id AND event.entity_revision=1
    AND event.type='owner_checkpoint.created' AND event.occurred_at=NEW.created_at AND event.recorded_at=NEW.created_at
    AND event.actor_id='local-owner' AND event.actor_type='human'
    AND json_type(event.data_json)='object' AND (SELECT COUNT(*) FROM json_each(event.data_json))=2
    AND json_extract(event.data_json,'$.scope_id')=NEW.scope_id AND json_extract(event.data_json,'$.scope_type')=NEW.scope_type)
 THEN RAISE(ABORT,'invalid canonical owner checkpoint construction') END;
END;
CREATE TRIGGER owner_checkpoint_reject_update BEFORE UPDATE ON owner_checkpoints BEGIN SELECT RAISE(ABORT,'owner checkpoints are immutable'); END;
CREATE TRIGGER owner_checkpoint_reject_delete BEFORE DELETE ON owner_checkpoints BEGIN SELECT RAISE(ABORT,'owner checkpoints are immutable'); END;
CREATE TRIGGER outcome_projector_require_store_insert BEFORE INSERT ON outcome_projector_state BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.last_event_sequence<>0 OR NEW.revision<>1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1 THEN RAISE(ABORT,'invalid outcome projector initialization') END;
END;
CREATE TRIGGER outcome_projector_require_store_update BEFORE UPDATE ON outcome_projector_state BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.workspace_id<>OLD.workspace_id OR NEW.revision<>OLD.revision+1
  OR NEW.last_event_sequence<=OLD.last_event_sequence OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
  OR (NEW.last_event_sequence<>0 AND NOT EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id AND event.sequence=NEW.last_event_sequence))
  OR EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id AND event.sequence>OLD.last_event_sequence AND event.sequence<=NEW.last_event_sequence AND crewfold_outcome_event_known(event.type) IS NOT 1)
 THEN RAISE(ABORT,'invalid outcome projector cursor advance') END;
END;
CREATE TRIGGER outcome_projector_reject_delete BEFORE DELETE ON outcome_projector_state BEGIN SELECT RAISE(ABORT,'outcome projector cursor is durable'); END;
CREATE TRIGGER management_briefing_require_store_insert BEFORE INSERT ON management_briefings BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.evaluated_at) IS NOT 1 OR NEW.created_at<>NEW.evaluated_at
  OR lower(hex(sha256(CAST(NEW.content_json AS BLOB)))) IS NOT NEW.content_sha256 OR length(CAST(NEW.content_json AS BLOB))<>NEW.byte_size
  OR json_extract(NEW.content_json,'$.scope.type')<>NEW.scope_type OR json_extract(NEW.content_json,'$.scope.workspace_id')<>NEW.workspace_id
  OR NEW.scope_id<>CASE NEW.scope_type WHEN 'workspace' THEN json_extract(NEW.content_json,'$.scope.workspace_id') WHEN 'project' THEN json_extract(NEW.content_json,'$.scope.project_id') WHEN 'objective' THEN json_extract(NEW.content_json,'$.scope.objective_id') ELSE json_extract(NEW.content_json,'$.scope.task_id') END
  OR json_extract(NEW.content_json,'$.event_cursor')<>NEW.event_cursor OR json_extract(NEW.content_json,'$.cutoff_event_sequence')<>NEW.cutoff_event_sequence
  OR COALESCE(json_extract(NEW.content_json,'$.checkpoint_id'),'')<>NEW.checkpoint_id OR json_extract(NEW.content_json,'$.since_event_sequence')<>NEW.since_event_sequence
  OR json_extract(NEW.content_json,'$.caught_up')<>NEW.caught_up
  OR COALESCE(json_extract(NEW.content_json,'$.unknown_event_type'),'')<>COALESCE(NEW.unknown_event_type,'')
  OR COALESCE(json_extract(NEW.content_json,'$.unknown_event_sequence'),0)<>COALESCE(NEW.unknown_event_sequence,0)
  OR NEW.revision<>COALESCE((SELECT MAX(revision)+1 FROM management_briefings WHERE workspace_id=NEW.workspace_id AND scope_type=NEW.scope_type AND scope_id=NEW.scope_id),1)
  OR (NEW.checkpoint_id<>'' AND NOT EXISTS(SELECT 1 FROM owner_checkpoints checkpoint WHERE checkpoint.id=NEW.checkpoint_id AND checkpoint.workspace_id=NEW.workspace_id AND checkpoint.scope_type=NEW.scope_type AND checkpoint.scope_id=NEW.scope_id AND checkpoint.event_sequence=NEW.since_event_sequence))
  OR (NEW.checkpoint_id='' AND NEW.since_event_sequence<>0)
  OR (NEW.event_cursor<>0 AND NOT EXISTS(SELECT 1 FROM outcome_projector_state state WHERE state.workspace_id=NEW.workspace_id AND state.last_event_sequence=NEW.event_cursor))
  OR NEW.event_cursor>NEW.cutoff_event_sequence OR NEW.caught_up<>(NEW.event_cursor=NEW.cutoff_event_sequence AND NEW.unknown_event_type IS NULL)
  OR (NEW.unknown_event_type IS NULL AND NEW.event_cursor<>NEW.cutoff_event_sequence)
  OR (NEW.unknown_event_type IS NOT NULL AND NOT EXISTS(SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
    AND event.sequence=NEW.unknown_event_sequence AND event.type=NEW.unknown_event_type
    AND event.sequence>NEW.event_cursor AND event.sequence<=NEW.cutoff_event_sequence
    AND crewfold_outcome_event_known(event.type) IS NOT 1
    AND event.sequence=(SELECT MIN(unknown.sequence) FROM events unknown WHERE unknown.workspace_id=NEW.workspace_id
      AND unknown.sequence>NEW.event_cursor AND unknown.sequence<=NEW.cutoff_event_sequence
      AND crewfold_outcome_event_known(unknown.type) IS NOT 1)))
 THEN RAISE(ABORT,'invalid canonical management briefing construction') END;
END;
CREATE TRIGGER management_briefing_reject_update BEFORE UPDATE ON management_briefings BEGIN SELECT RAISE(ABORT,'management briefings are immutable'); END;
CREATE TRIGGER management_briefing_reject_delete BEFORE DELETE ON management_briefings BEGIN SELECT RAISE(ABORT,'management briefings are immutable'); END;
CREATE TRIGGER management_claim_require_store_insert BEFORE INSERT ON management_briefing_claims BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM management_briefing_claims WHERE briefing_id=NEW.briefing_id),0)
  OR NEW.claim_id<>json_extract(NEW.claim_json,'$.id') OR NEW.kind<>json_extract(NEW.claim_json,'$.kind') OR NEW.urgency<>json_extract(NEW.claim_json,'$.urgency') OR NEW.summary<>json_extract(NEW.claim_json,'$.summary') OR NEW.status<>json_extract(NEW.claim_json,'$.status') OR COALESCE(NEW.project_id,'')<>COALESCE(json_extract(NEW.claim_json,'$.project_id'),'') OR NEW.source_event_sequence<>json_extract(NEW.claim_json,'$.source_event_sequence')
  OR json_type(NEW.claim_json,'$.sources')<>'array' OR json_array_length(NEW.claim_json,'$.sources') NOT BETWEEN 1 AND 32
  OR NEW.source_event_sequence<>(SELECT MAX(json_extract(source.value,'$.event_sequence')) FROM json_each(NEW.claim_json,'$.sources') source)
  OR NOT EXISTS(SELECT 1 FROM management_briefings briefing WHERE briefing.id=NEW.briefing_id
    AND json(json_extract(briefing.content_json,'$.claims['||NEW.ordinal||']'))=json(NEW.claim_json))
  OR NEW.claim_id<>crewfold_outcome_claim_id((SELECT json_extract(briefing.content_json,'$.scope') FROM management_briefings briefing WHERE briefing.id=NEW.briefing_id),NEW.semantic_key,NEW.status,json_extract(NEW.claim_json,'$.sources'))
 THEN RAISE(ABORT,'invalid canonical management briefing claim') END;
END;
CREATE TRIGGER management_claim_reject_update BEFORE UPDATE ON management_briefing_claims BEGIN SELECT RAISE(ABORT,'briefing claims are immutable'); END;
CREATE TRIGGER management_claim_reject_delete BEFORE DELETE ON management_briefing_claims BEGIN SELECT RAISE(ABORT,'briefing claims are immutable'); END;
CREATE TRIGGER management_claim_source_require_store_insert BEFORE INSERT ON management_briefing_claim_sources BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR NEW.ordinal<>COALESCE((SELECT MAX(ordinal)+1 FROM management_briefing_claim_sources WHERE briefing_id=NEW.briefing_id AND claim_id=NEW.claim_id),0)
  OR NOT EXISTS(SELECT 1 FROM management_briefing_claims claim JOIN events event ON event.sequence=NEW.event_sequence
   WHERE claim.briefing_id=NEW.briefing_id AND claim.claim_id=NEW.claim_id
    AND json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].entity_type')=NEW.entity_type
    AND json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].entity_id')=NEW.entity_id
    AND json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].revision')=NEW.entity_revision
    AND COALESCE(json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].content_sha256'),'')=NEW.content_sha256
    AND json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].event_sequence')=NEW.event_sequence
    AND COALESCE(json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].evidence_class'),'')=NEW.evidence_class
    AND COALESCE(json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].evidence_effect'),'')=NEW.evidence_effect
    AND COALESCE(json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].pinned_freshness'),'')=NEW.pinned_freshness
    AND COALESCE(json_extract(claim.claim_json,'$.sources['||NEW.ordinal||'].current_freshness'),'')=NEW.current_freshness)
  OR NOT EXISTS(SELECT 1 FROM management_briefings briefing JOIN management_briefing_claims claim ON claim.briefing_id=briefing.id AND claim.claim_id=NEW.claim_id JOIN events event ON event.sequence=NEW.event_sequence
   WHERE briefing.id=NEW.briefing_id AND event.workspace_id=briefing.workspace_id
    AND (NEW.entity_type NOT IN ('handoff','check_requirement_evidence') OR EXISTS(
      SELECT 1 FROM outcome_assessment_evidence_refs reference
      WHERE reference.source_type=NEW.entity_type AND reference.source_id=NEW.entity_id
       AND reference.source_revision=NEW.entity_revision AND reference.source_sha256=NEW.content_sha256
       AND reference.event_sequence=NEW.event_sequence AND reference.class=NEW.evidence_class
       AND reference.effect=NEW.evidence_effect AND reference.pinned_freshness=NEW.pinned_freshness
       AND EXISTS(SELECT 1 FROM json_each(claim.claim_json,'$.sources') base
        WHERE json_extract(base.value,'$.entity_type')='outcome_assessment'
         AND json_extract(base.value,'$.entity_id')=reference.assessment_id)))
    AND (
    (NEW.entity_type='outcome_assessment' AND event.entity_type='outcome_assessment' AND event.entity_id=NEW.entity_id
      AND event.entity_revision=NEW.entity_revision AND event.type IN ('outcome.assessment_accepted','outcome.assessment_superseded')
      AND EXISTS(SELECT 1 FROM outcome_assessments assessment WHERE assessment.id=NEW.entity_id AND assessment.workspace_id=briefing.workspace_id AND assessment.content_sha256=NEW.content_sha256))
    OR (NEW.entity_type='deliverable_commitment' AND event.entity_type='deliverable_commitment' AND event.entity_id=NEW.entity_id
      AND event.entity_revision=NEW.entity_revision AND event.type='outcome.commitment_created'
      AND EXISTS(SELECT 1 FROM deliverable_commitments commitment WHERE commitment.id=NEW.entity_id AND commitment.workspace_id=briefing.workspace_id AND commitment.content_sha256=NEW.content_sha256))
    OR (NEW.entity_type='outcome_assessment_acceptance_basis' AND NEW.entity_revision=1
      AND event.entity_type='outcome_assessment' AND event.entity_id=NEW.entity_id AND event.entity_revision=2 AND event.type='outcome.assessment_accepted'
      AND EXISTS(SELECT 1 FROM outcome_assessment_acceptance_basis basis WHERE basis.assessment_id=NEW.entity_id AND basis.event_sequence=NEW.event_sequence AND basis.source_sha256=NEW.content_sha256))
    OR (NEW.entity_type='knowledge_contradiction' AND NEW.content_sha256='' AND event.entity_type='knowledge_contradiction'
      AND event.entity_id=NEW.entity_id AND event.entity_revision=NEW.entity_revision AND event.type='contradiction.confirmed'
      AND EXISTS(SELECT 1 FROM knowledge_contradictions contradiction WHERE contradiction.id=NEW.entity_id AND contradiction.workspace_id=briefing.workspace_id AND contradiction.confirm_event_sequence=NEW.event_sequence))
    OR (NEW.entity_type='knowledge_revision' AND event.entity_type='knowledge_revision' AND event.entity_id=NEW.entity_id
      AND event.entity_revision=NEW.entity_revision AND event.type='knowledge.accepted'
      AND EXISTS(SELECT 1 FROM knowledge_revisions revision JOIN knowledge_items item ON item.id=revision.item_id WHERE revision.id=NEW.entity_id AND item.workspace_id=briefing.workspace_id AND revision.content_hash=NEW.content_sha256))
    OR (NEW.entity_type='task' AND NEW.content_sha256='' AND event.entity_type='task' AND event.entity_id=NEW.entity_id
      AND event.entity_revision=NEW.entity_revision
      AND event.sequence=(SELECT MAX(exact.sequence) FROM events exact WHERE exact.workspace_id=briefing.workspace_id
        AND exact.entity_type='task' AND exact.entity_id=NEW.entity_id AND exact.entity_revision=NEW.entity_revision)
      AND EXISTS(SELECT 1 FROM tasks task WHERE task.id=NEW.entity_id AND task.workspace_id=briefing.workspace_id AND task.revision=NEW.entity_revision))
    OR (NEW.entity_type='handoff' AND event.entity_type='task' AND event.type='task.handoff_recorded'
      AND event.entity_revision=NEW.entity_revision AND json_extract(event.data_json,'$.handoff_id')=NEW.entity_id
      AND EXISTS(SELECT 1 FROM run_handoffs handoff JOIN runs run ON run.id=handoff.run_id WHERE handoff.id=NEW.entity_id
       AND run.workspace_id=briefing.workspace_id AND run.status='completed' AND event.entity_id=handoff.task_id
       AND NEW.evidence_effect='supports' AND NEW.pinned_freshness='fresh' AND NEW.current_freshness='fresh'
       AND NEW.content_sha256=lower(hex(sha256(CAST(json_object('created_at',handoff.created_at,'evidence_json',json(handoff.evidence_json),'id',handoff.id,'run_id',handoff.run_id,'summary',handoff.summary,'task_id',handoff.task_id) AS BLOB))))
       AND NEW.evidence_class IN ('agent_self_report','independent_review')))
    OR (NEW.entity_type='check_requirement_evidence' AND event.entity_type='check_result'
      AND EXISTS(SELECT 1 FROM check_requirement_evidence evidence JOIN check_results result ON result.id=evidence.check_result_id
       JOIN check_runs run ON run.id=result.check_run_id
       JOIN check_result_freshness pinned ON pinned.check_result_id=result.id AND pinned.revision=evidence.freshness_revision
       JOIN check_result_freshness current ON current.check_result_id=result.id AND current.revision=(SELECT MAX(latest.revision) FROM check_result_freshness latest WHERE latest.check_result_id=result.id)
       WHERE evidence.id=NEW.entity_id AND run.workspace_id=briefing.workspace_id AND event.entity_id=result.id
        AND NEW.entity_revision=evidence.freshness_revision AND event.entity_revision=evidence.freshness_revision
        AND event.type IN ('check.result_recorded','check.freshness_observed','check.freshness_stale')
        AND NEW.evidence_class=evidence.class AND NEW.evidence_effect=evidence.effect
        AND NEW.pinned_freshness=pinned.status AND NEW.current_freshness=current.status
        AND NEW.content_sha256=lower(hex(sha256(CAST(json_object('check_result_id',evidence.check_result_id,'class',evidence.class,'effect',evidence.effect,'freshness_revision',evidence.freshness_revision,'id',evidence.id,'pinned_freshness',pinned.status,'requirement_id',evidence.requirement_id,'requirement_revision',evidence.requirement_revision) AS BLOB))))))
   ))
 THEN RAISE(ABORT,'invalid canonical management briefing provenance') END;
END;
CREATE TRIGGER management_claim_source_reject_update BEFORE UPDATE ON management_briefing_claim_sources BEGIN SELECT RAISE(ABORT,'briefing claim sources are immutable'); END;
CREATE TRIGGER management_claim_source_reject_delete BEFORE DELETE ON management_briefing_claim_sources BEGIN SELECT RAISE(ABORT,'briefing claim sources are immutable'); END;
CREATE TRIGGER management_briefing_receipt_require_store_insert BEFORE INSERT ON management_briefing_receipts BEGIN
 SELECT CASE WHEN crewfold_outcome_mutation_seal_active() IS NOT 1 OR crewfold_timestamp_canonical(NEW.sealed_at) IS NOT 1
  OR NOT EXISTS(SELECT 1 FROM management_briefings briefing WHERE briefing.id=NEW.briefing_id AND briefing.created_at=NEW.sealed_at
   AND json_type(briefing.content_json,'$.claims')='array' AND json_type(briefing.content_json,'$.omitted')='array'
   AND NEW.claim_count=json_array_length(briefing.content_json,'$.claims')
   AND NEW.claim_count=(SELECT COUNT(*) FROM management_briefing_claims claim WHERE claim.briefing_id=NEW.briefing_id)
   AND NEW.source_count=(SELECT COALESCE(SUM(json_array_length(json_extract(item.value,'$.sources'))),0) FROM json_each(briefing.content_json,'$.claims') item)
   AND NEW.source_count=(SELECT COUNT(*) FROM management_briefing_claim_sources source WHERE source.briefing_id=NEW.briefing_id)
   AND NOT EXISTS(SELECT 1 FROM management_briefing_claims claim WHERE claim.briefing_id=NEW.briefing_id
    AND json_array_length(claim.claim_json,'$.sources')<>(SELECT COUNT(*) FROM management_briefing_claim_sources source WHERE source.briefing_id=claim.briefing_id AND source.claim_id=claim.claim_id)))
 THEN RAISE(ABORT,'invalid complete management briefing receipt') END;
END;
CREATE TRIGGER management_briefing_receipt_reject_update BEFORE UPDATE ON management_briefing_receipts BEGIN SELECT RAISE(ABORT,'management briefing receipts are immutable'); END;
CREATE TRIGGER management_briefing_receipt_reject_delete BEFORE DELETE ON management_briefing_receipts BEGIN SELECT RAISE(ABORT,'management briefing receipts are immutable'); END;

-- Owner workbench conversations freeze the interpreted plan and bind every
-- executed effect to its existing canonical mutation receipt. They are
-- authority records, not an alternate event journal.
CREATE TABLE owner_conversations (
 id TEXT PRIMARY KEY CHECK(length(id)=37 AND substr(id,1,5)='conv_' AND substr(id,6) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 160),
 status TEXT NOT NULL CHECK(status IN ('open','archived')),
 revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 created_by TEXT NOT NULL CHECK(created_by IN ('local-owner','subsystem:owner-manager')),
 updated_by TEXT NOT NULL CHECK(updated_by IN ('local-owner','subsystem:owner-manager'))
) STRICT;
CREATE INDEX owner_conversations_scope_idx ON owner_conversations(workspace_id,project_id,status,updated_at,id);

CREATE TABLE owner_turns (
 id TEXT PRIMARY KEY CHECK(length(id)=37 AND substr(id,1,5)='turn_' AND substr(id,6) NOT GLOB '*[^0-9a-f]*'),
 conversation_id TEXT NOT NULL REFERENCES owner_conversations(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>0),
 kind TEXT NOT NULL CHECK(kind IN ('query','plan','act','instruction','review')),
 initiated_by TEXT NOT NULL CHECK(initiated_by IN ('owner','executive')),
 trigger_event_sequence INTEGER CHECK(trigger_event_sequence IS NULL OR trigger_event_sequence>0),
 instruction TEXT NOT NULL CHECK(length(CAST(instruction AS BLOB)) BETWEEN 1 AND 4096),
 status TEXT NOT NULL CHECK(status IN ('queued','running','planned','executing','completed','failed','awaiting_approval')),
 as_of_event_sequence INTEGER NOT NULL CHECK(as_of_event_sequence>=0),
 answer TEXT CHECK(answer IS NULL OR length(CAST(answer AS BLOB)) BETWEEN 1 AND 8192),
 interpretation_json TEXT NOT NULL CHECK(json_valid(interpretation_json) AND json_type(interpretation_json)='object' AND length(CAST(interpretation_json AS BLOB))<=131072),
 citations_json TEXT NOT NULL CHECK(json_valid(citations_json) AND json_type(citations_json)='array' AND json_array_length(citations_json)<=16 AND length(CAST(citations_json AS BLOB))<=32768),
 plan_json TEXT NOT NULL CHECK(json_valid(plan_json) AND json_type(plan_json)='array' AND json_array_length(plan_json)<=40 AND length(CAST(plan_json AS BLOB))<=131072),
 plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
 error_code TEXT CHECK(error_code IS NULL OR length(error_code) BETWEEN 1 AND 128),
 completed_event_sequence INTEGER CHECK(completed_event_sequence IS NULL OR completed_event_sequence>0),
 idempotency_key TEXT NOT NULL CHECK(length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
 revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(conversation_id,ordinal),
 UNIQUE(idempotency_key),
 CHECK((initiated_by='owner' AND kind IN ('query','plan','act','instruction') AND trigger_event_sequence IS NULL) OR
       (initiated_by='executive' AND kind='review' AND trigger_event_sequence IS NOT NULL AND trigger_event_sequence=as_of_event_sequence))
) STRICT;

CREATE TABLE owner_manager_review_jobs (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 conversation_id TEXT NOT NULL REFERENCES owner_conversations(id),
 status TEXT NOT NULL CHECK(status IN ('idle','pending','leased','failed')),
 requested_event_sequence INTEGER NOT NULL CHECK(requested_event_sequence>0),
 reviewed_event_sequence INTEGER NOT NULL CHECK(reviewed_event_sequence>=0 AND reviewed_event_sequence<=requested_event_sequence),
 attempts INTEGER NOT NULL CHECK(attempts>=0),
 available_at TEXT NOT NULL,
 lease_expires_at TEXT,
 last_turn_id TEXT REFERENCES owner_turns(id),
 last_error TEXT CHECK(last_error IS NULL OR length(CAST(last_error AS BLOB)) BETWEEN 1 AND 2048),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((status='idle' AND reviewed_event_sequence=requested_event_sequence AND lease_expires_at IS NULL AND last_error IS NULL) OR
       (status='pending' AND reviewed_event_sequence<requested_event_sequence AND lease_expires_at IS NULL AND last_error IS NULL) OR
       (status='leased' AND reviewed_event_sequence<requested_event_sequence AND lease_expires_at IS NOT NULL AND last_error IS NULL) OR
       (status='failed' AND reviewed_event_sequence<requested_event_sequence AND lease_expires_at IS NULL AND last_error IS NOT NULL))
) STRICT;
CREATE INDEX owner_manager_review_jobs_queue_idx ON owner_manager_review_jobs(status,available_at,project_id);

CREATE TABLE owner_executive_bindings (
 id TEXT PRIMARY KEY CHECK(length(id)=37 AND substr(id,1,5)='exec_' AND substr(id,6) NOT GLOB '*[^0-9a-f]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 project_id TEXT NOT NULL UNIQUE REFERENCES projects(id),
 objective_id TEXT NOT NULL REFERENCES objectives(id),
 planning_task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
 agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id),
 manager_grant_id TEXT NOT NULL UNIQUE REFERENCES manager_grants(id),
 launch_profile_id TEXT NOT NULL UNIQUE REFERENCES launch_profiles(id),
 status TEXT NOT NULL CHECK(status IN ('active','retired')),
 revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 created_by TEXT NOT NULL CHECK(created_by='local-owner'),
 updated_by TEXT NOT NULL CHECK(updated_by='local-owner')
) STRICT;

CREATE TRIGGER owner_executive_binding_validate_insert BEFORE INSERT ON owner_executive_bindings BEGIN
 SELECT CASE WHEN NOT EXISTS(
  SELECT 1 FROM projects project
  JOIN objectives objective ON objective.id=NEW.objective_id
  JOIN tasks task ON task.id=NEW.planning_task_id
  JOIN agents agent ON agent.id=NEW.agent_id
  JOIN task_assignments assignment ON assignment.task_id=task.id AND assignment.agent_id=agent.id AND assignment.status='active'
  JOIN manager_grants grant ON grant.id=NEW.manager_grant_id
  JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id
  WHERE project.id=NEW.project_id AND project.workspace_id=NEW.workspace_id
    AND objective.project_id=project.id AND objective.workspace_id=NEW.workspace_id AND objective.status='active'
    AND task.project_id=project.id AND task.workspace_id=NEW.workspace_id AND task.objective_id=objective.id
    AND task.status='assigned'
    AND agent.workspace_id=NEW.workspace_id AND agent.enabled=1
    AND grant.workspace_id=NEW.workspace_id AND grant.project_id=project.id AND grant.objective_id=objective.id
    AND grant.task_id=task.id AND grant.task_revision=task.revision AND grant.agent_id=agent.id AND grant.agent_revision=agent.revision
    AND grant.status='active'
    AND profile.workspace_id=NEW.workspace_id AND profile.project_id=project.id AND profile.agent_id=agent.id
    AND profile.agent_revision=agent.revision AND profile.manager_grant_id=grant.id AND profile.status='active'
 ) THEN RAISE(ABORT,'invalid owner executive binding') END;
END;
CREATE TRIGGER owner_executive_binding_validate_update BEFORE UPDATE ON owner_executive_bindings BEGIN
 SELECT CASE WHEN NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.project_id<>OLD.project_id
  OR NEW.objective_id<>OLD.objective_id OR NEW.planning_task_id<>OLD.planning_task_id OR NEW.agent_id<>OLD.agent_id
  OR NEW.status<>'active' OR OLD.status<>'active' OR NEW.revision<>OLD.revision+1
  OR NEW.created_at<>OLD.created_at OR NEW.created_by<>OLD.created_by OR NEW.updated_by<>'local-owner'
  OR crewfold_timestamp_canonical(NEW.updated_at)<>1
  OR crewfold_timestamp_key(NEW.updated_at)<=crewfold_timestamp_key(OLD.updated_at)
  OR (NEW.manager_grant_id=OLD.manager_grant_id AND NEW.launch_profile_id=OLD.launch_profile_id)
  OR NOT EXISTS(
   SELECT 1 FROM manager_grants grant_row
   JOIN launch_profiles profile ON profile.id=NEW.launch_profile_id
   JOIN tasks task ON task.id=NEW.planning_task_id
   JOIN agents agent ON agent.id=NEW.agent_id
   WHERE grant_row.id=NEW.manager_grant_id AND grant_row.status='active'
    AND grant_row.workspace_id=NEW.workspace_id AND grant_row.project_id=NEW.project_id
    AND grant_row.objective_id=NEW.objective_id AND grant_row.task_id=NEW.planning_task_id
    AND grant_row.task_revision=task.revision AND grant_row.agent_id=NEW.agent_id
    AND grant_row.agent_revision=agent.revision AND task.status='assigned' AND agent.enabled=1
    AND profile.workspace_id=NEW.workspace_id AND profile.project_id=NEW.project_id
    AND profile.agent_id=NEW.agent_id AND profile.agent_revision=agent.revision
    AND profile.manager_grant_id=grant_row.id AND profile.status='active'
  ) THEN RAISE(ABORT,'invalid owner executive binding reconfiguration') END;
END;

CREATE TABLE owner_executive_exchanges (
 id TEXT PRIMARY KEY CHECK(length(id)=38 AND substr(id,1,6)='execx_' AND substr(id,7) NOT GLOB '*[^0-9a-f]*'),
 turn_id TEXT NOT NULL UNIQUE REFERENCES owner_turns(id),
 binding_id TEXT NOT NULL REFERENCES owner_executive_bindings(id),
 run_id TEXT UNIQUE REFERENCES runs(id),
 event_sequence INTEGER NOT NULL CHECK(event_sequence>=0),
 context_json TEXT NOT NULL CHECK(json_valid(context_json) AND json_type(context_json)='object' AND length(CAST(context_json AS BLOB))<=262144),
 citations_json TEXT NOT NULL CHECK(json_valid(citations_json) AND json_type(citations_json)='array' AND json_array_length(citations_json)<=256 AND length(CAST(citations_json AS BLOB))<=131072),
 proposal_ids_json TEXT NOT NULL CHECK(json_valid(proposal_ids_json) AND json_type(proposal_ids_json)='array' AND json_array_length(proposal_ids_json)<=32),
 status TEXT NOT NULL CHECK(status IN ('pending','leased','running','responded','failed')),
 attempts INTEGER NOT NULL CHECK(attempts>=0),
 available_at TEXT NOT NULL,
 lease_expires_at TEXT,
 last_error TEXT CHECK(last_error IS NULL OR length(CAST(last_error AS BLOB)) BETWEEN 1 AND 2048),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((status='leased')=(lease_expires_at IS NOT NULL)),
 CHECK((status='running' OR status='responded' OR status='failed') OR run_id IS NULL),
 CHECK(status<>'running' OR run_id IS NOT NULL)
) STRICT;
CREATE INDEX owner_executive_exchanges_queue_idx ON owner_executive_exchanges(status,available_at,id);

CREATE TRIGGER owner_executive_exchange_validate_insert BEFORE INSERT ON owner_executive_exchanges BEGIN
 SELECT CASE WHEN NOT EXISTS(
  SELECT 1 FROM owner_turns turn
  JOIN owner_conversations conversation ON conversation.id=turn.conversation_id
  JOIN owner_executive_bindings binding ON binding.id=NEW.binding_id
  WHERE turn.id=NEW.turn_id AND turn.status='queued'
    AND conversation.workspace_id=binding.workspace_id AND conversation.project_id=binding.project_id
    AND binding.status='active' AND turn.as_of_event_sequence=NEW.event_sequence
 ) THEN RAISE(ABORT,'invalid owner executive exchange') END;
END;

CREATE TRIGGER owner_manager_review_job_validate_insert BEFORE INSERT ON owner_manager_review_jobs BEGIN
 SELECT CASE WHEN crewfold_timestamp_canonical(NEW.available_at)<>1 OR crewfold_timestamp_canonical(NEW.created_at)<>1 OR crewfold_timestamp_canonical(NEW.updated_at)<>1
   OR (NEW.lease_expires_at IS NOT NULL AND crewfold_timestamp_canonical(NEW.lease_expires_at)<>1)
   OR NOT EXISTS(SELECT 1 FROM projects project WHERE project.id=NEW.project_id AND project.workspace_id=NEW.workspace_id)
   OR NOT EXISTS(SELECT 1 FROM owner_conversations conversation WHERE conversation.id=NEW.conversation_id AND conversation.workspace_id=NEW.workspace_id AND conversation.project_id=NEW.project_id AND conversation.status='open')
   OR NOT EXISTS(SELECT 1 FROM events event WHERE event.sequence=NEW.requested_event_sequence AND event.workspace_id=NEW.workspace_id)
 THEN RAISE(ABORT,'owner manager review job is outside its exact scope') END;
END;
CREATE TRIGGER owner_manager_review_job_validate_update BEFORE UPDATE ON owner_manager_review_jobs BEGIN
 SELECT CASE WHEN NEW.project_id<>OLD.project_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.created_at<>OLD.created_at
   OR crewfold_timestamp_canonical(NEW.available_at)<>1 OR crewfold_timestamp_canonical(NEW.updated_at)<>1
   OR (NEW.lease_expires_at IS NOT NULL AND crewfold_timestamp_canonical(NEW.lease_expires_at)<>1)
   OR NOT EXISTS(SELECT 1 FROM owner_conversations conversation WHERE conversation.id=NEW.conversation_id AND conversation.workspace_id=NEW.workspace_id AND conversation.project_id=NEW.project_id AND conversation.status='open')
   OR NOT EXISTS(SELECT 1 FROM events event WHERE event.sequence=NEW.requested_event_sequence AND event.workspace_id=NEW.workspace_id)
   OR (NEW.last_turn_id IS NOT NULL AND NOT EXISTS(
     SELECT 1 FROM owner_turns turn JOIN owner_conversations conversation ON conversation.id=turn.conversation_id
     WHERE turn.id=NEW.last_turn_id AND turn.initiated_by='executive' AND turn.kind='review'
       AND turn.trigger_event_sequence=NEW.reviewed_event_sequence AND conversation.workspace_id=NEW.workspace_id AND conversation.project_id=NEW.project_id))
 THEN RAISE(ABORT,'owner manager review job update is outside its exact scope') END;
END;

CREATE TABLE owner_turn_operations (
 id TEXT PRIMARY KEY CHECK(length(id)=35 AND substr(id,1,3)='op_' AND substr(id,4) NOT GLOB '*[^0-9a-f]*'),
 turn_id TEXT NOT NULL REFERENCES owner_turns(id),
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 40),
 type TEXT NOT NULL CHECK(type IN ('create_objective','create_task','add_dependency','schedule_task')),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json) AND json_type(payload_json)='object' AND length(CAST(payload_json AS BLOB))<=8192),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
 policy_result TEXT NOT NULL CHECK(policy_result IN ('allowed','gated','denied')),
 status TEXT NOT NULL CHECK(status IN ('pending','applied','awaiting_approval','failed','skipped')),
 result_entity_type TEXT CHECK(result_entity_type IS NULL OR result_entity_type IN ('objective','task','task_dependency','scheduling_intent')),
 result_entity_id TEXT CHECK(result_entity_id IS NULL OR length(result_entity_id) BETWEEN 1 AND 64),
 event_sequence INTEGER CHECK(event_sequence IS NULL OR event_sequence>0),
 diagnosis TEXT CHECK(diagnosis IS NULL OR length(CAST(diagnosis AS BLOB)) BETWEEN 1 AND 2048),
 revision INTEGER NOT NULL CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(turn_id,ordinal)
) STRICT;

CREATE TABLE owner_effect_receipts (
 operation_id TEXT PRIMARY KEY REFERENCES owner_turn_operations(id),
 method TEXT NOT NULL CHECK(method IN ('objective.create','task.create','task.dependency.add','supervisor.intent.create')),
 idempotency_key TEXT NOT NULL CHECK(length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128),
 request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64 AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
 response_json TEXT NOT NULL CHECK(json_valid(response_json) AND json_type(response_json)='object' AND length(CAST(response_json AS BLOB))<=32768),
 response_sha256 TEXT NOT NULL CHECK(length(response_sha256)=64 AND response_sha256 NOT GLOB '*[^0-9a-f]*'),
 event_sequence INTEGER CHECK(event_sequence IS NULL OR event_sequence>0),
 committed_at TEXT NOT NULL
) STRICT;
CREATE INDEX owner_turns_conversation_idx ON owner_turns(conversation_id,ordinal);
CREATE INDEX owner_turn_operations_turn_idx ON owner_turn_operations(turn_id,ordinal);

-- The disposable retrieval projection still needs one current generation on a
-- clean database. Its source set and canonical event interval are both empty.
INSERT INTO knowledge_search(revision_id,workspace_id,title,body)
SELECT revision.id,item.workspace_id,revision.title,revision.body
FROM knowledge_revisions revision
JOIN knowledge_items item ON item.id=revision.item_id
ORDER BY revision.id;
INSERT INTO knowledge_search_metadata(
    singleton,generation,built_at,source_event_sequence,source_count,source_digest
) VALUES(
    1,1,rtrim(rtrim(strftime('%Y-%m-%dT%H:%M:%f','now'),'0'),'.') || 'Z',0,0,
    lower(hex(sha256(CAST('' AS BLOB))))
);
