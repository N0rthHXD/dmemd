-- Core business tables. Times are stored as fixed-width UTC RFC 3339
-- strings so lexical ordering matches chronological ordering. SQLite sidecar
-- files remain the only derived persistence outside this schema.

CREATE TABLE sources (
    id           TEXT NOT NULL PRIMARY KEY,
    app_id       TEXT NOT NULL DEFAULT '',
    external_id  TEXT NOT NULL DEFAULT '',
    scope_key    TEXT NOT NULL DEFAULT '',
    content      BLOB,
    redacted     INTEGER NOT NULL DEFAULT 0 CHECK (redacted IN (0, 1)),
    redacted_at  TEXT,
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_sources_scope_key ON sources(scope_key);

-- The schema contract is a single, explicit initialization identity. A database
-- created by an incompatible pre-release layout is rejected at startup instead
-- of being silently upgraded or rebuilt.
CREATE TABLE schema_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO schema_meta(key, value) VALUES ('contract', 'dmemd-s3-2026-10-22');

CREATE TABLE memories (
    id                TEXT NOT NULL PRIMARY KEY,
    kind              TEXT NOT NULL,
    title             TEXT NOT NULL DEFAULT '',
    content           TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL CHECK (state IN ('active', 'archived', 'deleted')),
    assertion_status  TEXT NOT NULL DEFAULT 'accepted'
                      CHECK (assertion_status IN ('accepted', 'pending', 'rejected')),
    version           INTEGER NOT NULL,
    observed_at       TEXT NOT NULL,
    valid_from        TEXT,
    valid_to          TEXT,
    valid_from_basis  TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (valid_from_basis IN ('explicit', 'inferred', 'unknown')),
    valid_to_basis    TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (valid_to_basis IN ('explicit', 'inferred', 'unknown')),
    fact_group_id     TEXT REFERENCES memory_fact_groups(id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    created_by_app    TEXT NOT NULL DEFAULT '',
    origin_source_id  TEXT REFERENCES sources(id) ON DELETE SET NULL,
    scope_key         TEXT NOT NULL DEFAULT '',
    topics_json       TEXT NOT NULL DEFAULT '[]',
    CHECK (version >= 1),
    CHECK ((valid_from IS NULL) = (valid_from_basis = 'unknown')),
    CHECK ((valid_to IS NULL) = (valid_to_basis = 'unknown')),
    CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from < valid_to)
);

CREATE INDEX idx_memories_updated_at ON memories(updated_at, id);
CREATE INDEX idx_memories_state ON memories(state);
CREATE INDEX idx_memories_kind ON memories(kind);
CREATE INDEX idx_memories_created_by_app ON memories(created_by_app);
CREATE INDEX idx_memories_assertion_status ON memories(assertion_status);
CREATE INDEX idx_memories_fact_group ON memories(fact_group_id);
CREATE INDEX idx_memories_valid_range ON memories(valid_from, valid_to);
CREATE INDEX idx_memories_scope_key ON memories(scope_key);

-- A revision stores a complete snapshot. Evidence is linked separately so one
-- version can own a complete, immutable evidence set without copying quotes
-- into the revision row.
CREATE TABLE memory_revisions (
    memory_id         TEXT NOT NULL,
    version           INTEGER NOT NULL,
    operation         TEXT NOT NULL,
    reason            TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL,
    title             TEXT NOT NULL DEFAULT '',
    content           TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL CHECK (state IN ('active', 'archived', 'deleted')),
    assertion_status  TEXT NOT NULL DEFAULT 'accepted'
                      CHECK (assertion_status IN ('accepted', 'pending', 'rejected')),
    observed_at       TEXT NOT NULL,
    valid_from        TEXT,
    valid_to          TEXT,
    valid_from_basis  TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (valid_from_basis IN ('explicit', 'inferred', 'unknown')),
    valid_to_basis    TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (valid_to_basis IN ('explicit', 'inferred', 'unknown')),
    fact_group_id     TEXT REFERENCES memory_fact_groups(id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    created_by_app    TEXT NOT NULL DEFAULT '',
    origin_source_id  TEXT,
    scope_key         TEXT NOT NULL DEFAULT '',
    topics_json       TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (memory_id, version),
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE,
    CHECK ((valid_from IS NULL) = (valid_from_basis = 'unknown')),
    CHECK ((valid_to IS NULL) = (valid_to_basis = 'unknown')),
    CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from < valid_to)
);

CREATE TABLE evidence (
    id               TEXT NOT NULL PRIMARY KEY,
    source_id        TEXT NOT NULL,
    message_id       TEXT NOT NULL DEFAULT '',
    quote            TEXT,
    location_present INTEGER NOT NULL DEFAULT 0 CHECK (location_present IN (0, 1)),
    location_field   TEXT,
    location_start   INTEGER,
    location_end     INTEGER,
    model_name       TEXT NOT NULL DEFAULT '',
    redacted         INTEGER NOT NULL DEFAULT 0 CHECK (redacted IN (0, 1)),
    redacted_at      TEXT,
    created_at       TEXT NOT NULL,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX idx_evidence_source_id ON evidence(source_id);

CREATE TABLE revision_evidence (
    memory_id   TEXT NOT NULL,
    version     INTEGER NOT NULL,
    evidence_id TEXT NOT NULL,
    PRIMARY KEY (memory_id, version, evidence_id),
    FOREIGN KEY (memory_id, version) REFERENCES memory_revisions(memory_id, version) ON DELETE CASCADE,
    FOREIGN KEY (evidence_id) REFERENCES evidence(id) ON DELETE CASCADE
);

CREATE INDEX idx_revision_evidence_evidence_id ON revision_evidence(evidence_id);

CREATE TABLE ingest_jobs (
    id              TEXT NOT NULL PRIMARY KEY,
    source_id       TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN
                    ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    protocol_failures INTEGER NOT NULL DEFAULT 0 CHECK (protocol_failures >= 0),
    wait_reason     TEXT NOT NULL DEFAULT '',
    next_attempt_at TEXT,
    error           TEXT NOT NULL DEFAULT '',
    memory_ids      TEXT NOT NULL DEFAULT '[]',
    conflict_ids    TEXT NOT NULL DEFAULT '[]',
    pending_memory_ids TEXT NOT NULL DEFAULT '[]',
    warnings        TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    started_at      TEXT,
    finished_at     TEXT,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX idx_ingest_jobs_claim ON ingest_jobs(status, next_attempt_at, created_at, id);
CREATE INDEX idx_ingest_jobs_source_id ON ingest_jobs(source_id);
CREATE INDEX idx_ingest_jobs_wait_reason ON ingest_jobs(status, wait_reason);
