-- Hybrid retrieval persistence. Vectors and tasks cascade with the search
-- document, so deletion never leaves a stale row that a late worker call could
-- reinsert.
CREATE TABLE memory_embeddings (
    memory_id    TEXT NOT NULL PRIMARY KEY,
    profile_id   TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    dimensions   INTEGER NOT NULL CHECK (dimensions > 0),
    vector       BLOB NOT NULL,
    embedded_at  TEXT NOT NULL,
    FOREIGN KEY (memory_id) REFERENCES memory_search_docs(memory_id) ON DELETE CASCADE,
    CHECK (length(vector) = dimensions * 4)
);

CREATE INDEX idx_memory_embeddings_profile ON memory_embeddings(profile_id);

CREATE TABLE memory_embedding_jobs (
    memory_id       TEXT NOT NULL PRIMARY KEY,
    profile_id      TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('queued', 'running', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TEXT,
    error_code      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    FOREIGN KEY (memory_id) REFERENCES memory_search_docs(memory_id) ON DELETE CASCADE
);

CREATE INDEX idx_memory_embedding_jobs_claim
    ON memory_embedding_jobs(status, next_attempt_at, created_at, memory_id);
CREATE INDEX idx_memory_embedding_jobs_profile
    ON memory_embedding_jobs(profile_id);

-- One row records the document input contract version that produced the current
-- content hashes. A version change triggers one full local rebuild at startup.
CREATE TABLE memory_index_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);
