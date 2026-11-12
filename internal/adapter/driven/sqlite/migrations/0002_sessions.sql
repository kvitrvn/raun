-- Browser sessions (epic 3, #20). A session is known by the SHA-256 of its
-- identifier, never the identifier: a copy of this file opens no session.
-- The CSRF token is stored in clear; it is useless without the cookie.
-- Times are fixed-width UTC text, so their text order is their time order.
CREATE TABLE sessions (
    hash         TEXT PRIMARY KEY CHECK (length(hash) = 64),
    csrf         TEXT NOT NULL CHECK (csrf <> ''),
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
) STRICT;

-- The hourly purge deletes by either end of a session.
CREATE INDEX sessions_last_seen_at ON sessions (last_seen_at);
CREATE INDEX sessions_created_at ON sessions (created_at);
