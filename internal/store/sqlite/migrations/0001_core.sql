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

INSERT INTO schema_meta(key, value) VALUES ('contract', 'dmemd-p16-2026-10-30-layer-recall');

-- L0 observations. Every row is extracted from exactly one source; identity,
-- scope, origin role and status are service-owned. Observations never carry
-- temporal roles, fact groups, topics, procedure structures or qualifiers.
CREATE TABLE observations (
    id                 TEXT NOT NULL PRIMARY KEY,
    source_id          TEXT NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    scope_key          TEXT NOT NULL DEFAULT '',
    kind               TEXT NOT NULL DEFAULT 'fact',
    content            TEXT NOT NULL,
    content_mode       TEXT NOT NULL CHECK (content_mode IN ('extractive', 'normalized')),
    origin_role        TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL CHECK (status IN ('observed', 'pending', 'retracted', 'deleted')),
    extraction_version TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE INDEX idx_observations_scope_status
    ON observations(scope_key, status, updated_at, id);
CREATE INDEX idx_observations_source
    ON observations(source_id, created_at, id);

-- Evidence is generated only by code from a selected source segment. Quote,
-- message and location are copied from the source view; the model never
-- supplies them. Redaction clears the copied content while keeping the link.
CREATE TABLE evidence (
    id             TEXT NOT NULL PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    segment_id     TEXT NOT NULL DEFAULT '',
    message_id     TEXT NOT NULL DEFAULT '',
    quote          TEXT,
    location_field TEXT,
    location_start INTEGER,
    location_end   INTEGER,
    redacted       INTEGER NOT NULL DEFAULT 0 CHECK (redacted IN (0, 1)),
    redacted_at    TEXT,
    created_at     TEXT NOT NULL
);

CREATE INDEX idx_evidence_source_id ON evidence(source_id);

CREATE TABLE observation_evidence (
    observation_id TEXT NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
    evidence_id    TEXT NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
    PRIMARY KEY (observation_id, evidence_id)
);

CREATE INDEX idx_observation_evidence_evidence
    ON observation_evidence(evidence_id);

-- Canonical memory is the current governed view, not the first extraction
-- result. Revisions are complete, immutable snapshots of that view.
CREATE TABLE memories (
    id               TEXT NOT NULL PRIMARY KEY,
    kind             TEXT NOT NULL DEFAULT 'fact',
    title            TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL,
    state            TEXT NOT NULL CHECK (state IN ('active', 'archived', 'deleted')),
    assertion_status TEXT NOT NULL CHECK (assertion_status IN ('accepted', 'pending', 'rejected')),
    version          INTEGER NOT NULL CHECK (version >= 1),
    scope_key        TEXT NOT NULL DEFAULT '',
    origin_source_id TEXT REFERENCES sources(id) ON DELETE SET NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX idx_memories_updated_at ON memories(updated_at, id);
CREATE INDEX idx_memories_state ON memories(state);
CREATE INDEX idx_memories_kind ON memories(kind);
CREATE INDEX idx_memories_assertion_status ON memories(assertion_status);
CREATE INDEX idx_memories_scope_key ON memories(scope_key);

CREATE TABLE memory_revisions (
    memory_id        TEXT NOT NULL,
    version          INTEGER NOT NULL CHECK (version >= 1),
    operation        TEXT NOT NULL,
    reason           TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL,
    title            TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL,
    state            TEXT NOT NULL,
    assertion_status TEXT NOT NULL,
    scope_key        TEXT NOT NULL DEFAULT '',
    origin_source_id TEXT,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (memory_id, version),
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

-- Observations supporting one exact canonical revision. A memory can be
-- supported by several observations and later revisions keep their own set.
CREATE TABLE memory_observation_refs (
    memory_id      TEXT NOT NULL,
    memory_version INTEGER NOT NULL,
    observation_id TEXT NOT NULL REFERENCES observations(id) ON DELETE RESTRICT,
    PRIMARY KEY (memory_id, memory_version, observation_id),
    FOREIGN KEY (memory_id, memory_version)
        REFERENCES memory_revisions(memory_id, version) ON DELETE CASCADE
);

CREATE INDEX idx_memory_observation_refs_observation
    ON memory_observation_refs(observation_id);

-- Interim revision-level evidence link used by the current materializer until
-- observation refs are the only memory source. Evidence itself is always
-- generated by code and observation_evidence remains the L0 link.
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
	stage           TEXT NOT NULL DEFAULT '',
	reason_code     TEXT NOT NULL DEFAULT '',
    next_attempt_at TEXT,
    error           TEXT NOT NULL DEFAULT '',
    observation_ids TEXT NOT NULL DEFAULT '[]',
    warnings        TEXT NOT NULL DEFAULT '[]',
    quality_outcome TEXT NOT NULL DEFAULT '' CHECK (quality_outcome IN
                    ('', 'empty', 'complete', 'partial', 'unusable')),
    extraction_stats TEXT NOT NULL DEFAULT '{}',
    timings         TEXT NOT NULL DEFAULT '{}',
    model_usage     TEXT NOT NULL DEFAULT '{}',
    diagnostics     TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    started_at      TEXT,
    finished_at     TEXT,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX idx_ingest_jobs_claim ON ingest_jobs(status, next_attempt_at, created_at, id);
CREATE INDEX idx_ingest_jobs_source_id ON ingest_jobs(source_id);
CREATE INDEX idx_ingest_jobs_wait_reason ON ingest_jobs(status, wait_reason);
