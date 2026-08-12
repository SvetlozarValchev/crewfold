-- Representative coordination records applied after the coordination schema.
INSERT INTO agents VALUES (
    'agent_00000000000000000000000000000003',
    'ws_00000000000000000000000000000002',
    'fixture-implementer', 'implementer', 'fake', 'fake', 1, 2, 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO objectives VALUES (
    'obj_00000000000000000000000000000003',
    'ws_00000000000000000000000000000002',
    'prj_00000000000000000000000000000002',
    'Fixture objective', 'active', 10000, 500, 3600, 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO tasks VALUES (
    'task_00000000000000000000000000000031',
    'ws_00000000000000000000000000000002',
    'prj_00000000000000000000000000000002',
    'obj_00000000000000000000000000000003',
    'Fixture foundation', 'Already complete', 'completed', NULL, 200,
    1000, 100, 600, 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO tasks VALUES (
    'task_00000000000000000000000000000032',
    'ws_00000000000000000000000000000002',
    'prj_00000000000000000000000000000002',
    'obj_00000000000000000000000000000003',
    'Fixture assigned work', 'Must survive the run schema upgrade', 'assigned', NULL, 100,
    5000, 250, 1800, 2,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO task_dependencies VALUES (
    'task_00000000000000000000000000000032',
    'task_00000000000000000000000000000031',
    '2026-08-12T00:00:00Z', 'local-owner'
);

INSERT INTO task_assignments VALUES (
    'asg_00000000000000000000000000000003',
    'task_00000000000000000000000000000032',
    'agent_00000000000000000000000000000003',
    'active', '2099-08-12T00:00:00Z', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO schema_migrations VALUES (3, '003_agents_objectives_tasks.sql', '2026-08-12T00:00:00Z');
PRAGMA user_version = 3;
