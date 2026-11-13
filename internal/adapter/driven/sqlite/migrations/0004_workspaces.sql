-- Workspaces and their members (epic 4, #27). Times are fixed-width UTC
-- text, as in 0002.

-- A workspace. Its name belongs to the one namespace of accounts and
-- workspaces: every account has its personal workspace, with the identifier
-- and the name of the account, so this table holds every name of the
-- namespace once, and its unique name refuses a collision between an account
-- and a workspace whichever comes first.
CREATE TABLE workspaces (
    id         TEXT PRIMARY KEY CHECK (length(id) = 32),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 39),
    personal   INTEGER NOT NULL CHECK (personal IN (0, 1)),
    created_at TEXT NOT NULL
) STRICT;

-- The accounts of a workspace and their role. A personal workspace has its
-- account, as owner, and no one else.
CREATE TABLE workspace_members (
    workspace TEXT NOT NULL REFERENCES workspaces (id),
    account   TEXT NOT NULL REFERENCES accounts (id),
    role      TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    PRIMARY KEY (workspace, account)
) STRICT;

CREATE INDEX workspace_members_account ON workspace_members (account);

-- The accounts created before this migration get their personal workspace.
INSERT INTO workspaces (id, name, personal, created_at)
    SELECT id, name, 1, created_at FROM accounts;

INSERT INTO workspace_members (workspace, account, role)
    SELECT id, id, 'owner' FROM accounts;
