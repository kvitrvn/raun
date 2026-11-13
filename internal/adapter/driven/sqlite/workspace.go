package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kvitrvn/raun/internal/core/identity"
)

var _ identity.WorkspaceStore = Accounts{}

func (a Accounts) CreateWorkspace(ctx context.Context, w identity.Workspace, owner string) error {
	return a.s.write(ctx, func(tx *sql.Tx) error {
		var taken, known int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspaces WHERE name = ?`, w.Name).Scan(&taken); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		if taken > 0 {
			return identity.ErrNameTaken
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id = ?`, owner).Scan(&known); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		if known == 0 {
			return identity.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces (id, name, personal, created_at) VALUES (?, ?, 0, ?)`,
			w.ID, w.Name, formatTime(w.CreatedAt)); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace, account, role) VALUES (?, ?, 'owner')`,
			w.ID, owner); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		return nil
	})
}

// scanMembership reads a workspace and a role from row.
func scanMembership(scan func(dest ...any) error) (identity.Membership, error) {
	var m identity.Membership
	var personal int
	var created, role string
	if err := scan(&m.Workspace.ID, &m.Workspace.Name, &personal, &created, &role); err != nil {
		return identity.Membership{}, err
	}
	m.Workspace.Personal = personal == 1
	m.Role = identity.Role(role)
	t, err := parseTime(created)
	if err != nil {
		return identity.Membership{}, fmt.Errorf("created_at: %w", err)
	}
	m.Workspace.CreatedAt = t
	return m, nil
}

const membershipColumns = `w.id, w.name, w.personal, w.created_at, m.role`

func (a Accounts) WorkspacesOf(ctx context.Context, account string) ([]identity.Membership, error) {
	var out []identity.Membership
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT `+membershipColumns+` FROM workspace_members m JOIN workspaces w ON w.id = m.workspace
			 WHERE m.account = ? ORDER BY w.name`, account)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			m, err := scanMembership(rows.Scan)
			if err != nil {
				return fmt.Errorf("list workspaces: %w", err)
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func (a Accounts) WorkspaceOf(ctx context.Context, name, account string) (identity.Membership, error) {
	var m identity.Membership
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		m, err = scanMembership(tx.QueryRowContext(ctx,
			`SELECT `+membershipColumns+` FROM workspaces w JOIN workspace_members m ON m.workspace = w.id
			 WHERE w.name = ? AND m.account = ?`, name, account).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read workspace: %w", err)
		}
		return nil
	})
	return m, err
}

func (a Accounts) Members(ctx context.Context, workspace string) ([]identity.Member, error) {
	var out []identity.Member
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT a.id, a.name, m.role FROM workspace_members m JOIN accounts a ON a.id = m.account
			 WHERE m.workspace = ? ORDER BY a.name`, workspace)
		if err != nil {
			return fmt.Errorf("list members: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var m identity.Member
			var role string
			if err := rows.Scan(&m.Account, &m.Name, &role); err != nil {
				return fmt.Errorf("list members: %w", err)
			}
			m.Role = identity.Role(role)
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func (a Accounts) AddMember(ctx context.Context, actor, workspace, name string, role identity.Role) error {
	if !role.Valid() {
		return identity.ErrInvalidRole
	}
	return a.s.write(ctx, func(tx *sql.Tx) error {
		// The checks run in the write transaction, which holds the write
		// lock: what they see stays true until the insert.
		var actorRole string
		var personal int
		err := tx.QueryRowContext(ctx,
			`SELECT m.role, w.personal FROM workspace_members m JOIN workspaces w ON w.id = m.workspace
			 WHERE m.workspace = ? AND m.account = ?`, workspace, actor).Scan(&actorRole, &personal)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		if identity.Role(actorRole) != identity.RoleOwner {
			return identity.ErrNotOwner
		}
		if personal == 1 {
			return identity.ErrPersonalWorkspace
		}
		var account string
		err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE name = ?`, name).Scan(&account)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		var member int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members WHERE workspace = ? AND account = ?`,
			workspace, account).Scan(&member); err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		if member > 0 {
			return identity.ErrAlreadyMember
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace, account, role) VALUES (?, ?, ?)`,
			workspace, account, string(role)); err != nil {
			return fmt.Errorf("add member: %w", err)
		}
		return nil
	})
}
