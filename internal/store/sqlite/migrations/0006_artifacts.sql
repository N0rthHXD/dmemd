-- Derived memories at L1-L4. One row is one current object; revisions are
-- immutable and every revision records its typed inputs in derived_dependencies.
-- Only the active current version is indexed for derived recall.

CREATE TABLE artifacts (
    id              TEXT NOT NULL PRIMARY KEY,
    layer           TEXT NOT NULL CHECK (layer IN ('l1', 'l2', 'l3', 'l4')),
    kind            TEXT NOT NULL,
    target_key      TEXT NOT NULL,
    scope_key       TEXT NOT NULL DEFAULT '',
    topic           TEXT NOT NULL DEFAULT '',
    current_version INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    state           TEXT NOT NULL CHECK (state IN ('active', 'stale', 'disabled')),
    reason_code     TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL,
    UNIQUE (kind, target_key)
);

CREATE INDEX idx_artifacts_layer_state ON artifacts(layer, state, updated_at);
CREATE INDEX idx_artifacts_kind_state ON artifacts(kind, state, updated_at);
CREATE INDEX idx_artifacts_scope ON artifacts(scope_key, layer, kind);

CREATE TABLE artifact_revisions (
    artifact_id      TEXT NOT NULL,
    version          INTEGER NOT NULL CHECK (version >= 1),
    title            TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL DEFAULT '',
    payload_json     TEXT NOT NULL DEFAULT '{}',
    content_hash     TEXT NOT NULL DEFAULT '',
    generator        TEXT NOT NULL DEFAULT '',
    protocol_version TEXT NOT NULL DEFAULT '',
    truncated        INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    created_at       TEXT NOT NULL,
    PRIMARY KEY (artifact_id, version),
    FOREIGN KEY (artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
);

-- Generic derived inputs. L1 depends on observations, L2 on L1, L3 on L2 and L4
-- on stable memories, derived objects and experience runs. The table deliberately
-- has no foreign key on derived_id: invalidation enumerates the complete affected
-- set before any artifact row is removed.
CREATE TABLE derived_dependencies (
    derived_id      TEXT NOT NULL,
    derived_version INTEGER NOT NULL,
    input_type      TEXT NOT NULL CHECK (input_type IN ('observation', 'memory', 'derived', 'experience')),
    input_id        TEXT NOT NULL,
    input_version   INTEGER NOT NULL DEFAULT 0,
    required        INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (derived_id, derived_version, input_type, input_id, input_version)
);

CREATE INDEX idx_derived_dependencies_input
    ON derived_dependencies(input_type, input_id, input_version);

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

-- A target may be evaluated without producing any artifact. Keeping this
-- separate from `artifacts` distinguishes "not eligible yet" from a disabled or
-- failed content object with retained history. A successful commit clears the
-- evaluation row for its target.
CREATE TABLE derived_target_evaluations (
    kind            TEXT NOT NULL,
    target_key      TEXT NOT NULL,
    scope_key       TEXT NOT NULL DEFAULT '',
    topic           TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL CHECK (status IN
                     ('no_eligible_input', 'model_unavailable', 'generation_failed', 'invalid_output')),
    reason_code     TEXT NOT NULL DEFAULT '',
    evaluated_at    TEXT NOT NULL,
    PRIMARY KEY (kind, target_key)
);

CREATE INDEX idx_derived_target_evaluations_scope
    ON derived_target_evaluations(scope_key, kind, evaluated_at);
