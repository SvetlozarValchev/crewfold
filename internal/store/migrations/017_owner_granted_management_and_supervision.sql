-- M16 keeps model-originated management advisory. Every executable launch
-- decision is an owner-authored immutable profile plus a durable accepted
-- scheduling intent. Agent role labels never appear in an authority check.

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

CREATE INDEX manager_grants_scope_idx
    ON manager_grants(workspace_id, project_id, objective_id, task_id, agent_id, status, id);

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

CREATE INDEX launch_profiles_scope_idx
    ON launch_profiles(workspace_id, project_id, agent_id, status, id);
CREATE INDEX launch_profiles_grant_idx
    ON launch_profiles(manager_grant_id, status, id);

-- Grant authority is normalized into immutable child rows. Creation inserts
-- these rows first under deferred FKs and inserts the active parent last; the
-- parent trigger proves the complete canonical JSON mirrors before commit.
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

CREATE INDEX manager_proposals_queue_idx
    ON manager_proposals(workspace_id, status, created_at, id);
CREATE INDEX manager_proposals_scope_idx
    ON manager_proposals(project_id, objective_id, kind, status, id);
CREATE INDEX manager_proposals_run_idx ON manager_proposals(source_run_id, id);

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
          AND json_extract(packet.packet_json, '$.schema') = 'urn:crewfold:schema:domain:context-packet:v5'
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
		    AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v5'
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

CREATE TABLE scheduling_intents (
    id TEXT PRIMARY KEY CHECK (
        length(id) = 40 AND substr(id, 1, 8) = 'sintent_'
        AND substr(id, 9) NOT GLOB '*[^0-9a-f]*'
    ),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT NOT NULL REFERENCES objectives(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    launch_profile_id TEXT NOT NULL REFERENCES launch_profiles(id),
    source_proposal_id TEXT NOT NULL REFERENCES manager_proposals(id),
    source_action_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','deferred','awaiting_approval','run_requested','satisfied','failed','cancelled')),
    reason TEXT,
    assignment_id TEXT REFERENCES task_assignments(id),
    run_id TEXT REFERENCES runs(id),
    supervisor_action_id TEXT,
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 0 AND 100),
    last_evaluated_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_evaluated_event_sequence >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    next_attempt_at TEXT,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (source_proposal_id, source_action_id),
    FOREIGN KEY (source_action_id, source_proposal_id)
      REFERENCES manager_proposal_actions(id, proposal_id)
) STRICT;

CREATE TRIGGER scheduling_intent_validate_insert
BEFORE INSERT ON scheduling_intents
BEGIN
    SELECT CASE WHEN NEW.status <> 'pending' OR NEW.reason IS NOT NULL
      OR NEW.assignment_id IS NOT NULL OR NEW.run_id IS NOT NULL OR NEW.supervisor_action_id IS NOT NULL
      OR NEW.attempts <> 0 OR NEW.last_evaluated_event_sequence <> 0
      OR NEW.revision <> 1 OR NEW.created_at IS NOT NEW.updated_at
      OR NEW.next_attempt_at IS NOT NULL OR NEW.created_by <> 'local-owner'
      OR NEW.updated_by <> 'local-owner' OR crewfold_timestamp_canonical(NEW.created_at) IS NOT 1
      OR NOT EXISTS (
        SELECT 1 FROM manager_proposals proposal
        JOIN manager_proposal_actions action ON action.proposal_id = proposal.id
        JOIN launch_profiles profile ON profile.id = NEW.launch_profile_id
        JOIN tasks task ON task.id = NEW.task_id
        WHERE proposal.id = NEW.source_proposal_id AND proposal.status = 'accepted'
          AND proposal.workspace_id = NEW.workspace_id AND proposal.project_id = NEW.project_id
          AND proposal.objective_id = NEW.objective_id AND action.id = NEW.source_action_id
          AND action.type IN ('create_task','assign_task','request_review','request_action')
          AND (
            (action.type<>'request_action' AND json_extract(action.payload_json, '$.launch_profile_id') = NEW.launch_profile_id)
            OR (action.type='request_action' AND json_extract(action.payload_json,'$.response')='reassign_task'
              AND json_extract(action.payload_json,'$.launch_profile_id')=NEW.launch_profile_id)
            OR (action.type='request_action' AND json_extract(action.payload_json,'$.response')='retry_task'
              AND EXISTS (
                SELECT 1 FROM runs failed
                WHERE failed.task_id=NEW.task_id AND failed.status='start_failed' AND failed.step_cursor=0
                  AND NEW.launch_profile_id=COALESCE(
                    (SELECT launch_profile_id FROM run_scheduling_receipts WHERE run_id=failed.id),
                    (SELECT launch_profile_id FROM run_retry_receipts WHERE run_id=failed.id)
                  )
              ))
          )
          AND profile.workspace_id = NEW.workspace_id AND profile.project_id = NEW.project_id
          AND profile.agent_id = NEW.agent_id AND profile.status = 'active'
          AND profile.manager_grant_id IS NULL
          AND task.workspace_id = NEW.workspace_id AND task.project_id = NEW.project_id
          AND task.objective_id = NEW.objective_id
          AND (
            (action.type = 'assign_task' AND json_extract(action.payload_json, '$.task.task_id') = NEW.task_id)
            OR (action.type = 'request_action' AND json_extract(action.payload_json,'$.target_task_id')=NEW.task_id)
            OR (action.type IN ('create_task','request_review') AND EXISTS (
              SELECT 1 FROM manager_proposal_effects effect
              WHERE effect.proposal_id = proposal.id AND effect.action_id = action.id
                AND effect.entity_type = 'task' AND effect.entity_id = NEW.task_id
            ))
          )
      ) THEN RAISE(ABORT, 'scheduling intent lacks exact accepted profile action') END;
END;

CREATE TRIGGER scheduling_intent_validate_update
BEFORE UPDATE ON scheduling_intents
BEGIN
    SELECT CASE WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id
      OR NEW.project_id IS NOT OLD.project_id OR NEW.objective_id IS NOT OLD.objective_id
      OR NEW.task_id IS NOT OLD.task_id OR NEW.agent_id IS NOT OLD.agent_id
      OR NEW.launch_profile_id IS NOT OLD.launch_profile_id
      OR NEW.source_proposal_id IS NOT OLD.source_proposal_id
      OR NEW.source_action_id IS NOT OLD.source_action_id
      OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by
      OR NEW.last_evaluated_event_sequence < OLD.last_evaluated_event_sequence
      OR NEW.last_evaluated_event_sequence > COALESCE((
        SELECT MAX(event.sequence) FROM events event WHERE event.workspace_id=NEW.workspace_id
      ),0)
      OR (NEW.last_evaluated_event_sequence > 0 AND NOT EXISTS (
        SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
          AND event.sequence=NEW.last_evaluated_event_sequence
      ))
      OR NEW.revision <> OLD.revision + 1 OR crewfold_timestamp_canonical(NEW.updated_at) IS NOT 1
      OR (NEW.next_attempt_at IS NOT NULL AND crewfold_timestamp_canonical(NEW.next_attempt_at) IS NOT 1)
      OR crewfold_timestamp_key(NEW.updated_at) < crewfold_timestamp_key(OLD.updated_at)
      OR NOT (
        (NEW.updated_by='subsystem:supervisor' AND (
        (OLD.status IN ('pending','deferred') AND NEW.status='deferred'
            AND NEW.assignment_id IS NULL AND NEW.run_id IS NULL AND NEW.supervisor_action_id IS NULL
            AND NEW.attempts=OLD.attempts AND NEW.reason IS NOT NULL AND NEW.next_attempt_at IS NOT NULL
            AND crewfold_timestamp_key(NEW.next_attempt_at)>crewfold_timestamp_key(NEW.updated_at))
          OR
        (OLD.status IN ('pending','deferred') AND NEW.status='run_requested'
            AND NEW.assignment_id IS NOT NULL AND NEW.run_id IS NOT NULL AND NEW.supervisor_action_id IS NOT NULL
            AND NEW.attempts=OLD.attempts+1 AND NEW.reason IS NULL AND NEW.next_attempt_at IS NULL)
          OR
        (OLD.status='run_requested' AND NEW.status IN ('satisfied','failed','cancelled')
          AND NEW.last_evaluated_event_sequence IS OLD.last_evaluated_event_sequence
            AND NEW.assignment_id IS OLD.assignment_id AND NEW.run_id IS OLD.run_id
            AND NEW.supervisor_action_id IS OLD.supervisor_action_id AND NEW.attempts=OLD.attempts
            AND NEW.reason IS NOT NULL AND EXISTS (
              SELECT 1 FROM events event WHERE event.workspace_id=NEW.workspace_id
                AND event.entity_type='scheduling_intent' AND event.entity_id=NEW.id
                AND event.entity_revision=NEW.revision
                AND event.type=CASE NEW.status WHEN 'satisfied' THEN 'supervisor.intent_satisfied'
                  WHEN 'failed' THEN 'supervisor.intent_failed' ELSE 'supervisor.intent_cancelled' END
                AND event.occurred_at=NEW.updated_at AND event.recorded_at=NEW.updated_at
                AND event.actor_id='subsystem:supervisor' AND event.actor_type='subsystem'
                AND json_extract(event.data_json,'$.status')=NEW.status
                AND json_extract(event.data_json,'$.reason')=NEW.reason
            ))
        ))
        OR
        (NEW.updated_by='local-owner' AND OLD.status IN ('pending','deferred','run_requested') AND NEW.status='cancelled'
          AND NEW.last_evaluated_event_sequence IS OLD.last_evaluated_event_sequence
          AND NEW.attempts=OLD.attempts AND NEW.reason='task cancelled by local owner' AND NEW.next_attempt_at IS NULL
          AND (
            (OLD.status IN ('pending','deferred')
              AND NEW.assignment_id IS NULL AND NEW.run_id IS NULL AND NEW.supervisor_action_id IS NULL)
            OR
            (OLD.status='run_requested'
              AND NEW.assignment_id IS OLD.assignment_id AND NEW.run_id IS OLD.run_id
              AND NEW.supervisor_action_id IS OLD.supervisor_action_id
              AND EXISTS (
                SELECT 1 FROM runs latest
                JOIN run_jobs job ON job.run_id=latest.id
                WHERE latest.id=COALESCE((
                    SELECT retry.run_id FROM run_retry_receipts retry
                    WHERE retry.intent_id=OLD.id
                    ORDER BY retry.attempt DESC,retry.run_id DESC LIMIT 1
                  ),OLD.run_id)
                  AND latest.workspace_id=OLD.workspace_id AND latest.task_id=OLD.task_id
                  AND latest.assignment_id=OLD.assignment_id
                  AND latest.status='start_failed' AND latest.step_cursor=0
                  AND latest.finished_at=latest.updated_at
                  AND job.status='complete' AND job.origin='supervisor'
                  AND (
                    (latest.id=OLD.run_id AND EXISTS (
                      SELECT 1 FROM run_scheduling_receipts initial
                      WHERE initial.run_id=latest.id AND initial.intent_id=OLD.id
                    ))
                    OR EXISTS (
                      SELECT 1 FROM run_retry_receipts source
                      WHERE source.run_id=latest.id AND source.intent_id=OLD.id
                    )
                  )
                  AND NOT EXISTS (
                    SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=latest.id
                  )
                  AND EXISTS (
                    SELECT 1 FROM events failure
                    WHERE failure.workspace_id=OLD.workspace_id
                      AND failure.entity_type='run' AND failure.entity_id=latest.id
                      AND failure.entity_revision=latest.revision AND failure.type='run.start_failed'
                      AND failure.occurred_at=latest.updated_at AND failure.recorded_at=latest.updated_at
                      AND failure.actor_id='local-owner' AND failure.actor_type='human'
                      AND json_extract(failure.data_json,'$.code')='runtime_start_failed'
                  )
              ))
          )
          AND EXISTS (
            SELECT 1 FROM tasks task
            JOIN events task_event ON task_event.workspace_id=NEW.workspace_id
              AND task_event.entity_type='task' AND task_event.entity_id=task.id
              AND task_event.entity_revision=task.revision AND task_event.type='task.cancelled'
              AND task_event.occurred_at=NEW.updated_at AND task_event.recorded_at=NEW.updated_at
              AND task_event.actor_id='local-owner' AND task_event.actor_type='human'
              AND json_extract(task_event.data_json,'$.status')='cancelled'
            WHERE task.id=NEW.task_id AND task.workspace_id=NEW.workspace_id
              AND task.status='cancelled' AND task.updated_at=NEW.updated_at AND task.updated_by='local-owner'
          )
          AND EXISTS (
            SELECT 1 FROM events intent_event WHERE intent_event.workspace_id=NEW.workspace_id
              AND intent_event.entity_type='scheduling_intent' AND intent_event.entity_id=NEW.id
              AND intent_event.entity_revision=NEW.revision AND intent_event.type='supervisor.intent_cancelled'
              AND intent_event.occurred_at=NEW.updated_at AND intent_event.recorded_at=NEW.updated_at
              AND intent_event.actor_id='local-owner' AND intent_event.actor_type='human'
              AND json_extract(intent_event.data_json,'$.task_id')=NEW.task_id
              AND json_extract(intent_event.data_json,'$.status')='cancelled'
              AND json_extract(intent_event.data_json,'$.reason')=NEW.reason
              AND (
                (OLD.status IN ('pending','deferred')
                  AND json_type(intent_event.data_json,'$.run_id') IS NULL
                  AND json_type(intent_event.data_json,'$.run_status') IS NULL)
                OR
                (OLD.status='run_requested'
                  AND json_extract(intent_event.data_json,'$.run_id')=COALESCE((
                    SELECT retry.run_id FROM run_retry_receipts retry
                    WHERE retry.intent_id=OLD.id
                    ORDER BY retry.attempt DESC,retry.run_id DESC LIMIT 1
                  ),OLD.run_id)
                  AND json_extract(intent_event.data_json,'$.run_status')='start_failed')
              )
          ))
      )
      OR (NEW.status = 'run_requested' AND (
        NEW.assignment_id IS NULL OR NEW.run_id IS NULL OR NEW.supervisor_action_id IS NULL
      ))
      THEN RAISE(ABORT, 'invalid scheduling intent lifecycle') END;
END;

CREATE TRIGGER scheduling_intent_reject_delete
BEFORE DELETE ON scheduling_intents BEGIN
    SELECT RAISE(ABORT, 'scheduling intents are durable acceptance receipts');
END;

CREATE UNIQUE INDEX scheduling_intents_one_open_task_idx
    ON scheduling_intents(task_id)
    WHERE status IN ('pending','deferred','awaiting_approval','run_requested');
CREATE INDEX scheduling_intents_queue_idx
    ON scheduling_intents(workspace_id, status, next_attempt_at, task_id, id);

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

INSERT INTO supervisor_policies(
    workspace_id, revision, enabled, max_active_runs, max_starting_runs,
    default_project_concurrency, default_provider_concurrency,
    project_concurrency_json, provider_concurrency_json, auto_schedule,
    auto_retry_limit, retry_cooldown_seconds, event_sequence,
    created_at, updated_at, created_by, updated_by
)
SELECT id, 1, 0, 8, 2, 4, 4, '{}', '{}', 0, 0, 0, 0,
       created_at, created_at, 'subsystem:supervisor', 'subsystem:supervisor'
FROM workspaces;

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
	          AND task.status IN ('ready','failed','changes_requested') AND task.revision=NEW.entity_revision
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

CREATE INDEX supervisor_actions_queue_idx
    ON supervisor_actions(workspace_id, status, created_at, id);
CREATE INDEX supervisor_actions_entity_idx
    ON supervisor_actions(workspace_id, task_id, run_id, condition, entity_revision, id);

-- The action row is inserted before its journal event so foreign-key consumers
-- can cite its stable ID. Only this immutable receipt makes the row observable
-- or eligible for condition deduplication; an unsealed raw row cannot poison a
-- canonical condition key.
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

CREATE INDEX approval_requests_queue_idx
    ON approval_requests(workspace_id, status, created_at, id);

CREATE TABLE supervisor_state (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
    last_event_sequence INTEGER NOT NULL CHECK (last_event_sequence >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO supervisor_state(workspace_id, last_event_sequence, revision, updated_at)
SELECT id, 0, 1, created_at FROM workspaces;

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
	      AND crewfold_supervisor_event_known(event.type) IS NOT 1
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

-- A run job's origin makes a missing supervisor receipt fail closed rather than
-- silently looking like a legacy owner launch.
ALTER TABLE runs ADD COLUMN assignment_id TEXT REFERENCES task_assignments(id);

UPDATE runs
SET assignment_id = (
    SELECT json_extract(packet.packet_json, '$.task.assignment_id')
    FROM run_context_bindings binding
    JOIN context_packets packet ON packet.id = binding.context_packet_id
    WHERE binding.run_id = runs.id
);

-- Packets before v4 (and legacy contextless runs) did not freeze an assignment
-- ID. Recover it only when the old run's exact task+agent tuple has one active
-- assignment; never guess between historical candidates.
UPDATE runs
SET assignment_id = (
    SELECT assignment.id
    FROM task_assignments assignment
    WHERE assignment.task_id = runs.task_id
      AND assignment.agent_id = runs.agent_id
      AND assignment.status = 'active'
      AND NOT EXISTS (
        SELECT 1 FROM task_assignments other
        WHERE other.task_id = assignment.task_id
          AND other.agent_id = assignment.agent_id
          AND other.status = 'active'
          AND other.id <> assignment.id
      )
)
WHERE assignment_id IS NULL;

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

-- Force the trigger to validate every capacity-reserving legacy row during the
-- migration itself. An ambiguous/missing assignment fails the upgrade closed;
-- terminal historical rows may remain unlinked because they hold no capacity.
UPDATE runs
SET assignment_id = assignment_id
WHERE assignment_id IS NULL
  AND status IN ('requested','starting','active','blocked','stopping','lost');

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

-- Run history is a forward-only execution projection. In particular, a
-- terminal run can never be reset to the revision-1 requested state for which
-- its original launch receipt and event remain valid.
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
      )
      THEN RAISE(ABORT, 'invalid run lifecycle transition') END;
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

ALTER TABLE run_jobs ADD COLUMN origin TEXT NOT NULL DEFAULT 'owner'
    CHECK (origin IN ('owner','supervisor'));

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

CREATE TRIGGER run_scheduling_receipt_reject_update
BEFORE UPDATE ON run_scheduling_receipts BEGIN
    SELECT RAISE(ABORT, 'run scheduling receipts are immutable');
END;
CREATE TRIGGER run_scheduling_receipt_reject_delete
BEFORE DELETE ON run_scheduling_receipts BEGIN
    SELECT RAISE(ABORT, 'run scheduling receipts are immutable');
END;

-- A retry is a fresh external operation. The prior failed run remains
-- immutable; this receipt proves that the new run reused only the exact
-- still-live assignment, claims, profile, agent, and accepted intent.
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
        AND json_extract(packet.packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v4'
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

CREATE TRIGGER run_retry_receipt_reject_update
BEFORE UPDATE ON run_retry_receipts BEGIN SELECT RAISE(ABORT, 'run retry receipts are immutable'); END;
CREATE TRIGGER run_retry_receipt_reject_delete
BEFORE DELETE ON run_retry_receipts BEGIN SELECT RAISE(ABORT, 'run retry receipts are immutable'); END;

-- Migration 016 froze v4. Recreate the two packet boundary triggers so v4
-- retains its exact base tools while v5 additionally proves a current manager
-- grant. Go performs the full canonical snapshot validation; SQL independently
-- rejects schema widening, forged scope, and a missing/non-current grant.
DROP TRIGGER run_context_delta_state_validate_insert;
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
              AND json_extract(packet.packet_json, '$.schema') IN (
                'urn:crewfold:schema:domain:context-packet:v4',
                'urn:crewfold:schema:domain:context-packet:v5'
              )
              AND json_extract(packet.packet_json, '$.as_of_event_sequence') = NEW.scan_event_sequence
        ) THEN RAISE(ABORT, 'invalid initial run context delta state') END;
END;

DROP TRIGGER context_packet_validate_insert;
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
        OR json_extract(NEW.packet_json, '$.schema') NOT IN (
          'urn:crewfold:schema:domain:context-packet:v1',
          'urn:crewfold:schema:domain:context-packet:v2',
          'urn:crewfold:schema:domain:context-packet:v3',
          'urn:crewfold:schema:domain:context-packet:v4',
          'urn:crewfold:schema:domain:context-packet:v5'
        ) THEN RAISE(ABORT, 'unsupported context packet schema') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') IN (
        'urn:crewfold:schema:domain:context-packet:v4',
        'urn:crewfold:schema:domain:context-packet:v5'
    ) AND (
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
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v4' AND (
        json_type(NEW.packet_json, '$.management_grant') IS NOT NULL
        OR json_extract(NEW.packet_json, '$.policy.allowed_tools') IS NOT json_array(
          'crewfold_acknowledge_context_delta','crewfold_acknowledge_message','crewfold_get_briefing',
          'crewfold_get_context_delta','crewfold_get_status','crewfold_list_inbox','crewfold_propose_knowledge',
          'crewfold_propose_completion','crewfold_publish_artifact','crewfold_read_message',
          'crewfold_report_blocked','crewfold_report_contradiction','crewfold_report_progress','crewfold_send_message')
    ) THEN RAISE(ABORT, 'invalid version-four tool policy') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v5' AND (
        json_extract(NEW.packet_json, '$.management_grant.schema') IS NOT
          'urn:crewfold:schema:domain:context-manager-grant:v1'
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
            SELECT 14+kind.ordinal,CASE kind.kind WHEN 'assignment' THEN 'crewfold_propose_assignment'
              WHEN 'escalation' THEN 'crewfold_propose_escalation' WHEN 'review' THEN 'crewfold_propose_review'
              ELSE 'crewfold_propose_tasks' END
            FROM manager_grant_proposal_kinds kind
            WHERE kind.grant_id=json_extract(NEW.packet_json, '$.management_grant.grant_id')
            ORDER BY ordinal
          )
        )
    ) THEN RAISE(ABORT, 'invalid version-five manager grant') END;
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') IN (
        'urn:crewfold:schema:domain:context-packet:v4','urn:crewfold:schema:domain:context-packet:v5'
    ) AND EXISTS (
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
    SELECT CASE WHEN json_extract(NEW.packet_json, '$.schema') IN (
        'urn:crewfold:schema:domain:context-packet:v4','urn:crewfold:schema:domain:context-packet:v5'
    ) AND NEW.content_hash IS NOT 'sha256:' || lower(hex(sha256(CAST(json_set(
        NEW.packet_json, '$.id','', '$.content_hash','', '$.created_at','', '$.created_by','',
        '$.byte_size',0, '$.budget.total.used_bytes',0, '$.budget.total.remaining_bytes',32768
    ) AS BLOB)))) THEN RAISE(ABORT, 'context packet semantic hash is invalid') END;
END;

DROP TRIGGER run_context_binding_validate_insert;
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
        WHERE run.id = NEW.run_id AND json_extract(packet.packet_json, '$.schema') IN (
          'urn:crewfold:schema:domain:context-packet:v4','urn:crewfold:schema:domain:context-packet:v5'
        ) AND (
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
      WHERE run.id = NEW.run_id AND json_extract(packet.packet_json, '$.schema') =
        'urn:crewfold:schema:domain:context-packet:v5' AND (
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
              SELECT 14+kind.ordinal,CASE kind.kind WHEN 'assignment' THEN 'crewfold_propose_assignment'
                WHEN 'escalation' THEN 'crewfold_propose_escalation' WHEN 'review' THEN 'crewfold_propose_review'
                ELSE 'crewfold_propose_tasks' END
              FROM manager_grant_proposal_kinds kind WHERE kind.grant_id=grant_row.id
              ORDER BY ordinal
            )
          )
          OR run.assignment_id <> json_extract(packet.packet_json, '$.task.assignment_id')
        )
    ) THEN RAISE(ABORT, 'version-five run binding grant is no longer exact') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM events event JOIN runs run ON run.id = NEW.run_id
        JOIN context_packets packet ON packet.id = NEW.context_packet_id
        WHERE event.workspace_id = run.workspace_id AND event.entity_type = 'context_packet'
          AND event.entity_id = packet.id AND event.entity_revision = 1
          AND event.type = 'context.packet_built' AND event.actor_id = 'local-owner'
          AND event.actor_type = 'human' AND json_extract(event.data_json, '$.task_id') = run.task_id
          AND json_extract(event.data_json, '$.agent_id') = run.agent_id
          AND json_extract(event.data_json, '$.checkout_id') = run.checkout_id
          AND (json_extract(packet.packet_json, '$.schema') NOT IN (
            'urn:crewfold:schema:domain:context-packet:v4','urn:crewfold:schema:domain:context-packet:v5'
          ) OR (
            json_extract(event.data_json, '$.packet_schema') = json_extract(packet.packet_json, '$.schema')
            AND json_extract(event.data_json, '$.as_of_event_sequence') = json_extract(packet.packet_json, '$.as_of_event_sequence')
          ))
          AND json_extract(event.data_json, '$.content_hash') = packet.content_hash
          AND json_extract(event.data_json, '$.byte_size') = packet.byte_size
    ) THEN RAISE(ABORT, 'run context packet has no exact built event') END;
END;
