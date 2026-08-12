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

CREATE INDEX meetings_workspace_status_idx ON meetings(workspace_id, status, created_at, id);
CREATE INDEX meetings_overlap_idx ON meetings(overlap_id, created_at, id);

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

CREATE INDEX task_roles_task_idx ON task_roles(task_id, role, agent_id);
