package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// Repositories is the store of the repository context on the same database
// file: the repositories Raun knows and their activation.
type Repositories struct {
	s *Store
}

var _ repository.Store = Repositories{}

// Repositories returns the repository store of this database.
func (s *Store) Repositories() Repositories {
	return Repositories{s: s}
}

const repositoryColumns = `r.forge, r.forge_id, r.workspace, w.name, r.full_name, r.default_branch, r.private, r.active, r.reachable, r.changed_by, r.changed_at`

const repositoryFrom = ` FROM repositories r JOIN workspaces w ON w.id = r.workspace`

func scanRepository(scan func(dest ...any) error) (repository.Repository, error) {
	var r repository.Repository
	var fullName sql.NullString
	var private, active, reachable int
	var changed string
	if err := scan(&r.Forge, &r.ID, &r.Workspace.ID, &r.Workspace.Name, &fullName, &r.DefaultBranch, &private, &active, &reachable, &r.ChangedBy, &changed); err != nil {
		return repository.Repository{}, err
	}
	r.FullName = fullName.String
	r.Private, r.Active, r.Reachable = private == 1, active == 1, reachable == 1
	t, err := parseTime(changed)
	if err != nil {
		return repository.Repository{}, fmt.Errorf("changed_at: %w", err)
	}
	r.ChangedAt = t
	return r, nil
}

// query reads the repositories a query gives.
func queryRepositories(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]repository.Repository, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+repositoryColumns+repositoryFrom+` `+query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []repository.Repository
	for rows.Next() {
		r, err := scanRepository(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// knownBatch bounds the identifiers of one query, under the limit of SQLite
// on the parameters of a statement.
const knownBatch = 500

func (s Repositories) Known(ctx context.Context, forge string, ids []string) ([]repository.Repository, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var out []repository.Repository
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		for chunk := range slices.Chunk(ids, knownBatch) {
			args := []any{forge}
			for _, id := range chunk {
				args = append(args, id)
			}
			rows, err := queryRepositories(ctx, tx, `WHERE r.forge = ? AND r.forge_id IN (?`+strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
			if err != nil {
				return fmt.Errorf("read repositories: %w", err)
			}
			out = append(out, rows...)
		}
		return nil
	})
	slices.SortFunc(out, func(a, b repository.Repository) int { return strings.Compare(a.ID, b.ID) })
	return out, err
}

func (s Repositories) Find(ctx context.Context, workspace, forge, fullName string) (repository.Repository, error) {
	var r repository.Repository
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		r, err = scanRepository(tx.QueryRowContext(ctx, `SELECT `+repositoryColumns+repositoryFrom+`
			WHERE w.name = ? AND r.forge = ? AND r.full_name = ?`, workspace, forge, fullName).Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return repository.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find repository: %w", err)
		}
		return nil
	})
	return r, err
}

func (s Repositories) InWorkspace(ctx context.Context, workspace string) ([]repository.Repository, error) {
	var out []repository.Repository
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		// A NULL full name sorts first, as an empty one does in Go.
		out, err = queryRepositories(ctx, tx, `WHERE r.workspace = ? ORDER BY coalesce(r.full_name, ''), r.forge_id`, workspace)
		if err != nil {
			return fmt.Errorf("list repositories: %w", err)
		}
		return nil
	})
	return out, err
}

// takeName empties the full name of every other repository of forge that
// holds name: the forge gave it to id since.
func takeName(ctx context.Context, tx *sql.Tx, forge, id, name string) error {
	_, err := tx.ExecContext(ctx, `UPDATE repositories SET full_name = NULL WHERE forge = ? AND full_name = ? AND forge_id <> ?`, forge, name, id)
	return err
}

// nullName is a full name as the table keeps it: NULL when empty.
func nullName(name string) sql.NullString {
	return sql.NullString{String: name, Valid: name != ""}
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s Repositories) Activate(ctx context.Context, r repository.Repository) error {
	return s.s.write(ctx, func(tx *sql.Tx) error {
		if err := takeName(ctx, tx, r.Forge, r.ID, r.FullName); err != nil {
			return fmt.Errorf("activate repository: %w", err)
		}
		// One statement decides: a repository of another workspace is
		// left as it is, and the change counts no row.
		res, err := tx.ExecContext(ctx, `INSERT INTO repositories
			(forge, forge_id, workspace, full_name, default_branch, private, active, reachable, changed_by, changed_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
			ON CONFLICT (forge, forge_id) DO UPDATE SET
				full_name = excluded.full_name, default_branch = excluded.default_branch, private = excluded.private,
				active = 1, reachable = 1, changed_by = excluded.changed_by, changed_at = excluded.changed_at
			WHERE repositories.workspace = excluded.workspace`,
			r.Forge, r.ID, r.Workspace.ID, nullName(r.FullName), r.DefaultBranch, bit(r.Private), r.ChangedBy, formatTime(r.ChangedAt))
		if err != nil {
			return fmt.Errorf("activate repository: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("activate repository: %w", err)
		}
		if n == 0 {
			return repository.ErrElsewhere
		}
		return nil
	})
}

func (s Repositories) Deactivate(ctx context.Context, workspace, forge, id, account string, at time.Time) error {
	return s.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE repositories SET active = 0, changed_by = ?, changed_at = ?
			WHERE workspace = ? AND forge = ? AND forge_id = ?`, account, formatTime(at), workspace, forge, id)
		if err != nil {
			return fmt.Errorf("deactivate repository: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("deactivate repository: %w", err)
		}
		if n == 0 {
			return repository.ErrNotFound
		}
		return nil
	})
}

func (s Repositories) Refresh(ctx context.Context, listed []repository.Listed) error {
	return s.s.write(ctx, func(tx *sql.Tx) error {
		for _, l := range listed {
			var known int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repositories WHERE forge = ? AND forge_id = ?`, l.Forge, l.ID).Scan(&known); err != nil {
				return fmt.Errorf("refresh repository: %w", err)
			}
			if known == 0 {
				continue
			}
			if err := takeName(ctx, tx, l.Forge, l.ID, l.FullName); err != nil {
				return fmt.Errorf("refresh repository: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE repositories SET full_name = ?, default_branch = ?, private = ? WHERE forge = ? AND forge_id = ?`,
				nullName(l.FullName), l.DefaultBranch, bit(l.Private), l.Forge, l.ID); err != nil {
				return fmt.Errorf("refresh repository: %w", err)
			}
		}
		return nil
	})
}

func (s Repositories) SetReach(ctx context.Context, forge string, reached []string) ([]string, error) {
	in := map[string]bool{}
	for _, id := range reached {
		in[id] = true
	}
	var lost []string
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		lost = nil
		rows, err := tx.QueryContext(ctx, `SELECT forge_id, reachable FROM repositories WHERE forge = ?`, forge)
		if err != nil {
			return fmt.Errorf("set reach: %w", err)
		}
		type row struct {
			id        string
			reachable bool
		}
		var all []row
		for rows.Next() {
			var r row
			var reachable int
			if err := rows.Scan(&r.id, &reachable); err != nil {
				_ = rows.Close()
				return fmt.Errorf("set reach: %w", err)
			}
			r.reachable = reachable == 1
			all = append(all, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("set reach: %w", err)
		}
		for _, r := range all {
			switch {
			case in[r.id] && !r.reachable:
				_, err = tx.ExecContext(ctx, `UPDATE repositories SET reachable = 1 WHERE forge = ? AND forge_id = ?`, forge, r.id)
			case !in[r.id] && r.reachable:
				lost = append(lost, r.id)
				_, err = tx.ExecContext(ctx, `UPDATE repositories SET reachable = 0, active = 0 WHERE forge = ? AND forge_id = ?`, forge, r.id)
			}
			if err != nil {
				return fmt.Errorf("set reach: %w", err)
			}
		}
		return nil
	})
	slices.Sort(lost)
	return lost, err
}
