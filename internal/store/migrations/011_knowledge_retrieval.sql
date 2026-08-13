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

INSERT INTO knowledge_search(revision_id, workspace_id, title, body)
SELECT kr.id, ki.workspace_id, kr.title, kr.body
FROM knowledge_revisions kr
JOIN knowledge_items ki ON ki.id = kr.item_id
ORDER BY kr.id;

INSERT INTO knowledge_search_metadata(
    singleton, generation, built_at, source_event_sequence, source_count, source_digest
) VALUES (
    1,
    1,
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    COALESCE((SELECT MAX(sequence) FROM events), 0),
    (SELECT COUNT(*) FROM knowledge_revisions),
    lower(hex(sha256(COALESCE((
        SELECT group_concat(id || char(0) || content_hash, char(10)) || char(10)
        FROM (SELECT id, content_hash FROM knowledge_revisions ORDER BY id)
    ), ''))))
);
