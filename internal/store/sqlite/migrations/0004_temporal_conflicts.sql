-- Temporal fact groups, relations, conflicts and the single-row semantic
-- catalog generation. The snapshot tables already carry the valid-time and
-- assertion columns; this migration adds the graph and audit structures.

CREATE TABLE memory_fact_groups (
    id          TEXT NOT NULL PRIMARY KEY,
    subject     TEXT NOT NULL DEFAULT '',
    predicate   TEXT NOT NULL DEFAULT '',
    scope       TEXT NOT NULL DEFAULT '',
    cardinality TEXT NOT NULL DEFAULT 'unknown'
                CHECK (cardinality IN ('single', 'multiple', 'unknown')),
    generation  INTEGER NOT NULL DEFAULT 1 CHECK (generation >= 1),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE INDEX idx_fact_groups_identity
    ON memory_fact_groups(subject, predicate, scope);

CREATE TABLE memory_relations (
    id             TEXT NOT NULL PRIMARY KEY,
    from_memory_id TEXT NOT NULL,
    from_version   INTEGER NOT NULL,
    to_memory_id   TEXT NOT NULL,
    to_version     INTEGER NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('supersedes')),
    created_at     TEXT NOT NULL,
    UNIQUE (from_memory_id, to_memory_id, kind),
    FOREIGN KEY (from_memory_id, from_version)
        REFERENCES memory_revisions(memory_id, version) ON DELETE CASCADE,
    FOREIGN KEY (to_memory_id, to_version)
        REFERENCES memory_revisions(memory_id, version) ON DELETE CASCADE,
    CHECK (from_memory_id <> to_memory_id)
);

CREATE INDEX idx_relations_to ON memory_relations(to_memory_id);

CREATE TABLE memory_conflicts (
    id            TEXT NOT NULL PRIMARY KEY,
    fact_group_id TEXT REFERENCES memory_fact_groups(id) ON DELETE SET NULL,
    status        TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    reason_code   TEXT NOT NULL CHECK (reason_code IN
                  ('contradictory_value', 'uncertain_time', 'ambiguous_scope', 'complex_timeline')),
    resolution    TEXT NOT NULL DEFAULT '',
    revision      INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX idx_conflicts_status ON memory_conflicts(status, updated_at, id);

CREATE TABLE memory_conflict_members (
    conflict_id TEXT NOT NULL,
    memory_id   TEXT NOT NULL,
    version     INTEGER NOT NULL CHECK (version >= 1),
    PRIMARY KEY (conflict_id, memory_id),
    FOREIGN KEY (conflict_id) REFERENCES memory_conflicts(id) ON DELETE CASCADE,
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

CREATE INDEX idx_conflict_members_memory ON memory_conflict_members(memory_id);

CREATE TABLE memory_conflict_events (
    id          TEXT NOT NULL PRIMARY KEY,
    conflict_id TEXT NOT NULL,
    action      TEXT NOT NULL,
    memory_id   TEXT NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    FOREIGN KEY (conflict_id) REFERENCES memory_conflicts(id) ON DELETE CASCADE
);

CREATE INDEX idx_conflict_events_conflict
    ON memory_conflict_events(conflict_id, created_at, id);

CREATE TABLE memory_catalog (
    id         INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0)
);

INSERT INTO memory_catalog(id, generation) VALUES (1, 0);
