-- Derived artifacts (topic summaries, profiles, experience cases and skills).
-- One row represents one current object; revisions are
-- immutable and every revision records the atomic memories it was built from.
-- Only the active current version is indexed for derived recall.

CREATE TABLE artifacts (
    id              TEXT NOT NULL PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN
                     ('topic_summary', 'profile', 'experience_case', 'skill')),
    target_key      TEXT NOT NULL,
    scope_key       TEXT NOT NULL DEFAULT '',
    topic           TEXT NOT NULL DEFAULT '',
    current_version INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    state           TEXT NOT NULL CHECK (state IN ('active', 'stale', 'disabled')),
    reason_code     TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL,
    UNIQUE (kind, target_key)
);

CREATE INDEX idx_artifacts_kind_state ON artifacts(kind, state, updated_at);
CREATE INDEX idx_artifacts_scope ON artifacts(scope_key, kind);

CREATE TABLE artifact_revisions (
    artifact_id       TEXT NOT NULL,
    version           INTEGER NOT NULL CHECK (version >= 1),
    title             TEXT NOT NULL DEFAULT '',
    content           TEXT NOT NULL DEFAULT '',
    payload_json      TEXT NOT NULL DEFAULT '{}',
    content_hash      TEXT NOT NULL DEFAULT '',
    generator         TEXT NOT NULL DEFAULT '',
    protocol_version  TEXT NOT NULL DEFAULT '',
    taxonomy_version  TEXT NOT NULL DEFAULT '',
    generator_version TEXT NOT NULL DEFAULT '',
    run_id            TEXT NOT NULL DEFAULT '',
    provenance_json   TEXT NOT NULL DEFAULT '{}',
    reason_code       TEXT NOT NULL DEFAULT '',
    truncated         INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    created_at        TEXT NOT NULL,
    PRIMARY KEY (artifact_id, version),
    FOREIGN KEY (artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
);

-- Direct leaf dependencies. Artifacts never depend on other artifacts in the
-- first release, so purge can enumerate the complete affected set before any
-- foreign-key cascade removes the edge.
CREATE TABLE artifact_memory_dependencies (
    artifact_id     TEXT NOT NULL,
    artifact_version INTEGER NOT NULL,
    memory_id       TEXT NOT NULL,
    memory_version  INTEGER NOT NULL CHECK (memory_version >= 1),
    PRIMARY KEY (artifact_id, artifact_version, memory_id),
    FOREIGN KEY (artifact_id, artifact_version)
        REFERENCES artifact_revisions(artifact_id, version) ON DELETE CASCADE,
    FOREIGN KEY (memory_id, memory_version)
        REFERENCES memory_revisions(memory_id, version) ON DELETE CASCADE
);

CREATE INDEX idx_artifact_memory_dependencies_memory
    ON artifact_memory_dependencies(memory_id, memory_version);
CREATE INDEX idx_artifact_memory_dependencies_artifact
    ON artifact_memory_dependencies(artifact_id, artifact_version);

CREATE TABLE artifact_search_docs (
    row_id           INTEGER PRIMARY KEY,
    artifact_id      TEXT NOT NULL UNIQUE,
    artifact_version INTEGER NOT NULL,
    title_terms      TEXT NOT NULL,
    content_terms    TEXT NOT NULL,
    FOREIGN KEY (artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE artifact_fts USING fts5(
    title_terms,
    content_terms,
    content = 'artifact_search_docs',
    content_rowid = 'row_id',
    tokenize = 'unicode61'
);

CREATE TRIGGER artifact_search_docs_ai AFTER INSERT ON artifact_search_docs BEGIN
    INSERT INTO artifact_fts(rowid, title_terms, content_terms)
    VALUES (new.row_id, new.title_terms, new.content_terms);
END;

CREATE TRIGGER artifact_search_docs_ad AFTER DELETE ON artifact_search_docs BEGIN
    INSERT INTO artifact_fts(artifact_fts, rowid, title_terms, content_terms)
    VALUES ('delete', old.row_id, old.title_terms, old.content_terms);
END;

CREATE TRIGGER artifact_search_docs_au AFTER UPDATE ON artifact_search_docs BEGIN
    INSERT INTO artifact_fts(artifact_fts, rowid, title_terms, content_terms)
    VALUES ('delete', old.row_id, old.title_terms, old.content_terms);
    INSERT INTO artifact_fts(rowid, title_terms, content_terms)
    VALUES (new.row_id, new.title_terms, new.content_terms);
END;
