-- The deliveries of the forges and the event log of the repositories (epic
-- 5, #32). Times are fixed-width UTC text, as in 0002.

-- The keys of every delivery a forge signed, of no repository or of one that
-- is not active included: its identifier, which the forge sends again with a
-- delivery sent again, and the SHA-256 of its raw body, since the identifier
-- is not signed and a body sent again under a new one is a replay. Both are
-- unique on their forge. They are kept 30 days.
CREATE TABLE deliveries (
    forge       TEXT NOT NULL CHECK (forge <> ''),
    delivery    TEXT NOT NULL CHECK (delivery <> ''),
    digest      TEXT NOT NULL CHECK (length(digest) = 64),
    received_at TEXT NOT NULL,
    PRIMARY KEY (forge, delivery),
    UNIQUE (forge, digest)
) STRICT;

CREATE INDEX deliveries_received_at ON deliveries (received_at);

-- The event log: one row per delivery of a repository while it was active,
-- written with the keys of its delivery. A repository keeps its log once
-- deactivated; the rows are kept 30 days, until the epic 13 review decides
-- the retention of events.
CREATE TABLE repository_events (
    forge       TEXT NOT NULL,
    repository  TEXT NOT NULL,
    delivery    TEXT NOT NULL,
    event       TEXT NOT NULL,
    action      TEXT NOT NULL,
    ref         TEXT NOT NULL,
    before      TEXT NOT NULL,
    after       TEXT NOT NULL,
    received_at TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('recorded', 'ignored')),
    PRIMARY KEY (forge, delivery),
    FOREIGN KEY (forge, repository) REFERENCES repositories (forge, forge_id)
) STRICT;

CREATE INDEX repository_events_log ON repository_events (forge, repository, received_at);
CREATE INDEX repository_events_received_at ON repository_events (received_at);
