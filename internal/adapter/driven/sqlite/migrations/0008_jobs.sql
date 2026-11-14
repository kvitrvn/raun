-- The jobs of the queue and their runs (epic 6, #37). Times are fixed-width
-- UTC text, as in 0002, so their order is the order of the text.

-- A job: what must be done on a commit of a repository, in its workspace. It
-- names the delivery that caused it, empty for a job asked by hand, without a
-- reference: the keys of a delivery are purged after 30 days, the job stays.
-- A job with a coalescing key replaces the queued jobs of its workspace with
-- the same key. finished_at is set once the job has ended, and only then.
CREATE TABLE jobs (
    id           TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32),
    kind         TEXT NOT NULL CHECK (kind <> ''),
    workspace    TEXT NOT NULL REFERENCES workspaces (id),
    forge        TEXT NOT NULL,
    repository   TEXT NOT NULL,
    commit_id    TEXT NOT NULL CHECK (length(commit_id) IN (40, 64)),
    ref          TEXT NOT NULL,
    delivery     TEXT NOT NULL,
    coalesce_key TEXT NOT NULL,
    priority     INTEGER NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    reason       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    not_before   TEXT NOT NULL,
    finished_at  TEXT,
    FOREIGN KEY (forge, repository) REFERENCES repositories (forge, forge_id),
    CHECK ((state IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL))
) STRICT;

CREATE INDEX jobs_queue ON jobs (priority, created_at, id) WHERE state = 'queued';
CREATE INDEX jobs_key ON jobs (workspace, coalesce_key) WHERE state = 'queued' AND coalesce_key <> '';
CREATE INDEX jobs_repository ON jobs (forge, repository) WHERE state IN ('queued', 'running');
CREATE INDEX jobs_delivery ON jobs (forge, delivery) WHERE delivery <> '';

-- A run: one attempt of a job by one runner, holding a lease the runner
-- renews. Only the SHA-256 of its lease token is kept. A job has one open
-- run at most; a run ends with an outcome, and the result it reported when
-- it was accepted.
CREATE TABLE runs (
    id               TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32),
    job              TEXT NOT NULL REFERENCES jobs (id),
    runner           TEXT NOT NULL CHECK (runner <> ''),
    token_hash       TEXT NOT NULL CHECK (length(token_hash) = 64),
    started_at       TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    deadline         TEXT NOT NULL,
    ended_at         TEXT,
    outcome          TEXT CHECK (outcome IN ('succeeded', 'failed', 'lost', 'cancelled')),
    result_outcome   TEXT CHECK (result_outcome IN ('succeeded', 'failed')),
    result_summary   TEXT,
    result_data      BLOB,
    CHECK ((outcome IS NULL) = (ended_at IS NULL)),
    CHECK ((result_outcome IS NULL) = (result_summary IS NULL) AND (result_outcome IS NULL OR result_outcome = outcome))
) STRICT;

CREATE UNIQUE INDEX runs_open ON runs (job) WHERE outcome IS NULL;
CREATE INDEX runs_job ON runs (job, started_at);
CREATE INDEX runs_lease ON runs (lease_expires_at) WHERE outcome IS NULL;
CREATE INDEX runs_deadline ON runs (deadline) WHERE outcome IS NULL;
