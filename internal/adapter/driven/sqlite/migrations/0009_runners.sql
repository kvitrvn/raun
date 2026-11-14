-- The runners (epic 6, #38). Times are fixed-width UTC text, as in 0002.

-- A runner: a raun runner process an administrator of the instance created,
-- serving the instance (workspace NULL) or one workspace, with the
-- capabilities the administrator gave it, a comma-separated sorted list. Only
-- the SHA-256 of its token is kept, unique, so a request finds its runner by
-- it. A revoked runner stays, with its name, for the runs that name it.
CREATE TABLE runners (
    id              TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32),
    name            TEXT NOT NULL UNIQUE CHECK (name <> ''),
    workspace       TEXT REFERENCES workspaces (id),
    capabilities    TEXT NOT NULL CHECK (capabilities <> ''),
    token_hash      TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    created_by      TEXT NOT NULL CHECK (created_by <> ''),
    created_at      TEXT NOT NULL,
    last_contact_at TEXT,
    release         TEXT NOT NULL,
    revoked_at      TEXT
) STRICT;

-- The open runs of a runner, which its revocation loses.
CREATE INDEX runs_runner ON runs (runner) WHERE outcome IS NULL;
