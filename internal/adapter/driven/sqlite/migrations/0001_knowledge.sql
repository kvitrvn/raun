-- Knowledge items and their history (epic 2, #14).
--
-- Released: never change this file. A change of the schema is a new numbered
-- migration.
--
-- An item is one row: the columns that queries filter or sort on, and a JSON
-- document for the rest. Its history is a table of its own, append-only.
-- Workspaces and repositories have no table yet (epics 4 and 5): their
-- identifiers are opaque text, and the foreign keys to them come with those
-- epics.

CREATE TABLE knowledge_items (
    workspace  TEXT    NOT NULL,
    repository TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    type       TEXT    NOT NULL,
    status     TEXT    NOT NULL,
    revision   INTEGER NOT NULL CHECK (revision >= 1),
    title      TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    document   TEXT    NOT NULL,
    PRIMARY KEY (workspace, repository, id)
) STRICT;

CREATE INDEX knowledge_items_by_type ON knowledge_items (workspace, repository, type, id);
CREATE INDEX knowledge_items_by_status ON knowledge_items (workspace, repository, status, id);

CREATE TABLE knowledge_history (
    workspace   TEXT    NOT NULL,
    repository  TEXT    NOT NULL,
    item        TEXT    NOT NULL,
    position    INTEGER NOT NULL CHECK (position >= 0),
    at          TEXT    NOT NULL,
    actor       TEXT    NOT NULL,
    actor_id    TEXT    NOT NULL,
    from_status TEXT    NOT NULL,
    to_status   TEXT    NOT NULL,
    reason      TEXT    NOT NULL,
    PRIMARY KEY (workspace, repository, item, position),
    FOREIGN KEY (workspace, repository, item) REFERENCES knowledge_items (workspace, repository, id)
) STRICT;

CREATE TRIGGER knowledge_history_no_update
BEFORE UPDATE ON knowledge_history
BEGIN
    SELECT RAISE(ABORT, 'knowledge_history is append-only');
END;

CREATE TRIGGER knowledge_history_no_delete
BEFORE DELETE ON knowledge_history
BEGIN
    SELECT RAISE(ABORT, 'knowledge_history is append-only');
END;
