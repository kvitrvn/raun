-- Repositories and their activation (epic 5, #31). Times are fixed-width UTC
-- text, as in 0002.

-- A repository of a forge Raun knows: activated once in a workspace, which it
-- belongs to from then on, active or not. The forge and the stable identifier
-- the forge gives it are its identity: the primary key holds on every row, so
-- a repository belongs to one workspace on the instance. The full name, the
-- default branch and private are snapshots refreshed from the forge. A full
-- name is unique on its forge, as on the forge itself: the forge gave it to
-- one repository at a time, and a snapshot the forge has moved on from is
-- NULL until the forge lists that repository again. A repository the App no
-- longer reaches is never active.
CREATE TABLE repositories (
    forge          TEXT NOT NULL CHECK (forge <> ''),
    forge_id       TEXT NOT NULL CHECK (forge_id <> ''),
    workspace      TEXT NOT NULL REFERENCES workspaces (id),
    full_name      TEXT CHECK (full_name <> ''),
    default_branch TEXT NOT NULL,
    private        INTEGER NOT NULL CHECK (private IN (0, 1)),
    active         INTEGER NOT NULL CHECK (active IN (0, 1)),
    reachable      INTEGER NOT NULL CHECK (reachable IN (0, 1)),
    changed_by     TEXT NOT NULL REFERENCES accounts (id),
    changed_at     TEXT NOT NULL,
    PRIMARY KEY (forge, forge_id),
    UNIQUE (forge, full_name),
    CHECK (reachable = 1 OR active = 0)
) STRICT;

CREATE INDEX repositories_workspace ON repositories (workspace);
