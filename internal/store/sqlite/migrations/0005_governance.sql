-- Persistent governance targets. One row represents one convergent target
-- rather than an event log: input changes refresh the row and increment its
-- revision instead of creating duplicate work. Targets are observations,
-- aggregation targets or dependency-invalidation targets, never the old
-- extraction/decision candidates.
CREATE TABLE governance_jobs (
    id                   TEXT NOT NULL PRIMARY KEY,
    kind                 TEXT NOT NULL CHECK (kind IN
                         ('govern_observation', 'classify_observation',
                          'aggregate_l1', 'aggregate_l2', 'aggregate_l3',
                          'build_guidance', 'review_conflict',
                          'invalidate_dependency', 'reconcile')),
    target_key           TEXT NOT NULL,
    revision             INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    status               TEXT NOT NULL CHECK (status IN
                         ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
    attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    next_attempt_at      TEXT,
    last_error_code      TEXT NOT NULL DEFAULT '',
    wait_reason          TEXT NOT NULL DEFAULT '',
    input_hash           TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    started_at           TEXT,
    finished_at          TEXT,
    UNIQUE (kind, target_key)
);

CREATE INDEX idx_governance_jobs_claim
    ON governance_jobs(status, next_attempt_at, created_at, id);
CREATE INDEX idx_governance_jobs_target
    ON governance_jobs(kind, target_key);
