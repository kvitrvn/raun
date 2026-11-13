-- Accounts, forge identities, their tokens, login states and sign-ups
-- (epic 4, #26). Times are fixed-width UTC text, as in 0002.

-- An account of Raun. Its name is permanent and unique in the namespace.
CREATE TABLE accounts (
    id         TEXT PRIMARY KEY CHECK (length(id) = 32),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 39),
    created_at TEXT NOT NULL
) STRICT;

-- A forge identity belongs to one account: the stable identifier the forge
-- gives the person, and the login and name of the last login.
CREATE TABLE identities (
    forge    TEXT NOT NULL CHECK (forge <> ''),
    forge_id TEXT NOT NULL CHECK (forge_id <> ''),
    account  TEXT NOT NULL REFERENCES accounts (id),
    login    TEXT NOT NULL,
    name     TEXT NOT NULL,
    PRIMARY KEY (forge, forge_id)
) STRICT;

CREATE INDEX identities_account ON identities (account);

-- The tokens of an identity, sealed: the access and refresh tokens and their
-- expiries, encrypted with AES-256-GCM under RAUN_SECRET_KEY.
CREATE TABLE tokens (
    forge    TEXT NOT NULL,
    forge_id TEXT NOT NULL,
    sealed   BLOB NOT NULL,
    PRIMARY KEY (forge, forge_id),
    FOREIGN KEY (forge, forge_id) REFERENCES identities (forge, forge_id)
) STRICT;

-- A login in progress: the SHA-256 of the state sent to the forge, never the
-- state, the PKCE verifier, the session it belongs to and where to go back.
CREATE TABLE login_states (
    hash        TEXT PRIMARY KEY CHECK (length(hash) = 64),
    verifier    TEXT NOT NULL CHECK (verifier <> ''),
    session     TEXT NOT NULL,
    return_path TEXT NOT NULL,
    expires_at  TEXT NOT NULL
) STRICT;

CREATE INDEX login_states_expires_at ON login_states (expires_at);

-- A sign-up waiting for a name, one per session; its tokens are sealed.
CREATE TABLE signups (
    session     TEXT PRIMARY KEY,
    forge       TEXT NOT NULL,
    forge_id    TEXT NOT NULL,
    login       TEXT NOT NULL,
    name        TEXT NOT NULL,
    proposed    TEXT NOT NULL,
    sealed      BLOB NOT NULL,
    return_path TEXT NOT NULL,
    expires_at  TEXT NOT NULL
) STRICT;

CREATE INDEX signups_expires_at ON signups (expires_at);

-- The account logged in to a session, empty before a login. Revoking the
-- tokens of an identity ends every session of its account.
ALTER TABLE sessions ADD COLUMN account TEXT NOT NULL DEFAULT '';

CREATE INDEX sessions_account ON sessions (account);
