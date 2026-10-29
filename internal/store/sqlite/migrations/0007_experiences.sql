-- Application-reported completion results. The report is not a verified
-- execution record: outcome is copied from the caller and never upgraded by
-- generation or recall. run_id is an optional client deduplication key; NULL
-- rows always create a new result. Derived revisions reference runs through the
-- generic derived_dependencies table.
CREATE TABLE experience_runs (
    id            TEXT NOT NULL PRIMARY KEY,
    run_id        TEXT UNIQUE,
    source_id     TEXT NOT NULL,
    skill_id      TEXT NOT NULL DEFAULT '',
    skill_version INTEGER NOT NULL DEFAULT 0 CHECK (skill_version >= 0),
    scope_key     TEXT NOT NULL DEFAULT '',
    goal          TEXT NOT NULL,
    conditions_json TEXT NOT NULL DEFAULT '[]',
    actions_json  TEXT NOT NULL DEFAULT '[]',
    outcome       TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'unknown', 'partial_success')),
    state         TEXT NOT NULL CHECK (state IN ('recorded', 'revoked')),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    revoked_at    TEXT,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX idx_experience_runs_scope ON experience_runs(scope_key, created_at, id);
CREATE INDEX idx_experience_runs_skill ON experience_runs(skill_id, skill_version);
CREATE INDEX idx_experience_runs_source ON experience_runs(source_id);
