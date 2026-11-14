-- The configuration of the repositories (epic 5, #33). Times are fixed-width
-- UTC text, as in 0002.

-- The last read of the configuration of a repository, .raun.yaml at the head
-- of its default branch: configured, with the file, untrusted, kept for the
-- decoder of epic 8 and 64 KiB at most; not configured; or unreadable, with
-- the reason. A deactivated repository keeps it.
CREATE TABLE repository_configurations (
    forge      TEXT NOT NULL,
    repository TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('configured', 'not_configured', 'unreadable')),
    commit_id  TEXT NOT NULL,
    content    BLOB NOT NULL CHECK (length(content) <= 65536),
    reason     TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    PRIMARY KEY (forge, repository),
    FOREIGN KEY (forge, repository) REFERENCES repositories (forge, forge_id),
    CHECK (state = 'configured' OR length(content) = 0)
) STRICT;
