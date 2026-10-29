-- Typed governed relations and conflict audit. Relations connect observations or
-- canonical memories; the service validates endpoints before insert. A relation
-- is a persisted semantic fact, never a database action.

CREATE TABLE memory_relations (
    id           TEXT NOT NULL PRIMARY KEY,
    kind         TEXT NOT NULL,
    from_type    TEXT NOT NULL CHECK (from_type IN ('observation', 'memory')),
    from_id      TEXT NOT NULL,
    from_version INTEGER NOT NULL DEFAULT 0,
    to_type      TEXT NOT NULL CHECK (to_type IN ('observation', 'memory')),
    to_id        TEXT NOT NULL,
    to_version   INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_memory_relations_from
    ON memory_relations(from_type, from_id, kind);
CREATE INDEX idx_memory_relations_to
    ON memory_relations(to_type, to_id, kind);

-- Repeated detection of the exact same relation is idempotent.
CREATE UNIQUE INDEX idx_memory_relations_unique
    ON memory_relations(from_type, from_id, from_version, to_type, to_id, to_version, kind);

-- Explicit contradiction review remains a management capability; it never
-- embeds fact-group identity or temporal intervals in the current view.
CREATE TABLE memory_conflicts (
    id            TEXT NOT NULL PRIMARY KEY,
    status        TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    reason_code   TEXT NOT NULL,
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
