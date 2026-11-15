package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Runners is the runner store (job.RunnerStore) on the database of a Store.
// A claim of a runner reads the runner in its own write transaction, so a
// revocation or a change committed before it always holds.
type Runners struct {
	s *Store
}

// Runners returns the runner store on the database of s.
func (s *Store) Runners() Runners {
	return Runners{s: s}
}

var _ job.RunnerStore = Runners{}

const runnerColumns = `id, name, workspace, capabilities, created_by, created_at, last_contact_at, release, revoked_at`

func scanRunner(row scanner) (job.Runner, error) {
	var r job.Runner
	var workspace, contact, revoked sql.NullString
	var caps, created string
	if err := row.Scan(&r.ID, &r.Name, &workspace, &caps, &r.CreatedBy, &created, &contact, &r.Release, &revoked); err != nil {
		return job.Runner{}, err
	}
	r.Workspace = workspace.String
	for _, c := range strings.Split(caps, ",") {
		r.Capabilities = append(r.Capabilities, job.Capability(c))
	}
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return job.Runner{}, err
	}
	if contact.Valid {
		if r.LastContact, err = parseTime(contact.String); err != nil {
			return job.Runner{}, err
		}
	}
	if revoked.Valid {
		if r.RevokedAt, err = parseTime(revoked.String); err != nil {
			return job.Runner{}, err
		}
	}
	return r, nil
}

func (s Runners) CreateRunner(ctx context.Context, r job.Runner, hash string) error {
	caps := make([]string, len(r.Capabilities))
	for i, c := range r.Capabilities {
		if c == "" || strings.Contains(string(c), ",") {
			return fmt.Errorf("create runner: %w: capability %q", job.ErrInvalidRunner, c)
		}
		caps[i] = string(c)
	}
	var workspace any
	if r.Workspace != "" {
		workspace = r.Workspace
	}
	return s.s.write(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runners WHERE name = ?`, r.Name).Scan(&taken); err != nil {
			return fmt.Errorf("create runner: %w", err)
		}
		if taken > 0 {
			return job.ErrRunnerNameTaken
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runners (`+runnerColumns+`, token_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, workspace, strings.Join(caps, ","), r.CreatedBy, formatTime(r.CreatedAt),
			nullTime(r.LastContact), r.Release, nullTime(r.RevokedAt), hash); err != nil {
			return fmt.Errorf("create runner: %w", err)
		}
		return nil
	})
}

func (s Runners) Runner(ctx context.Context, id string) (job.Runner, error) {
	var out job.Runner
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		r, err := scanRunner(tx.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return job.ErrNoRunner
		}
		if err != nil {
			return fmt.Errorf("read runner: %w", err)
		}
		out = r
		return nil
	})
	return out, err
}

func (s Runners) Runners(ctx context.Context) ([]job.Runner, error) {
	var out []job.Runner
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+runnerColumns+` FROM runners ORDER BY name`)
		if err != nil {
			return fmt.Errorf("list runners: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanRunner(rows)
			if err != nil {
				return fmt.Errorf("list runners: %w", err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// runnerIn returns the runner with this identifier in tx.
func runnerIn(ctx context.Context, tx *sql.Tx, id string) (job.Runner, error) {
	r, err := scanRunner(tx.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return job.Runner{}, job.ErrNoRunner
	}
	return r, err
}

func (s Runners) RotateRunner(ctx context.Context, id, hash string) error {
	return s.s.write(ctx, func(tx *sql.Tx) error {
		r, err := runnerIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Revoked() {
			return job.ErrRevoked
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET token_hash = ? WHERE id = ?`, hash, id); err != nil {
			return fmt.Errorf("rotate runner token: %w", err)
		}
		return nil
	})
}

func (s Runners) RevokeRunner(ctx context.Context, id string, at time.Time) (int, error) {
	n, revoked := 0, false
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		n, revoked = 0, false
		r, err := runnerIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Revoked() {
			return nil
		}
		revoked = true
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET revoked_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
			return fmt.Errorf("revoke runner: %w", err)
		}
		if n, err = loseRuns(ctx, tx, `runner = ?`, id, at.UTC()); err != nil {
			return fmt.Errorf("revoke runner: %w", err)
		}
		return nil
	})
	// Told for every revocation: the runs sent back may be claimed, and the
	// claims of the runner, which wait, learn at once that it is revoked.
	s.s.tellQueued(err == nil && revoked)
	return n, err
}

func (s Runners) Authenticate(ctx context.Context, hash, release string, now time.Time) (job.Runner, error) {
	now = now.UTC()
	var out job.Runner
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		r, err := scanRunner(tx.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE token_hash = ? AND revoked_at IS NULL`, hash))
		if errors.Is(err, sql.ErrNoRows) {
			return job.ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("authenticate runner: %w", err)
		}
		out = r
		return nil
	})
	upgraded := release != "" && release != out.Release
	if err != nil || (now.Before(out.LastContact.Add(job.ContactEvery)) && !upgraded) {
		return out, err
	}
	// The last contact is written every ContactEvery at most, and the
	// release when it changes, apart from the read: most requests write
	// nothing, and of concurrent requests one writes. It is advisory: a
	// write that fails leaves the runner authenticated, and a later request
	// writes it.
	written := false
	err = s.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE runners SET last_contact_at = ?, release = CASE WHEN ? <> '' THEN ? ELSE release END
			WHERE id = ? AND revoked_at IS NULL AND (last_contact_at IS NULL OR last_contact_at <= ? OR (? <> '' AND release <> ?))`,
			formatTime(now), release, release, out.ID, formatTime(now.Add(-job.ContactEvery)), release, release)
		if err != nil {
			return err
		}
		written = affected(res) == 1
		return nil
	})
	if err == nil && written {
		out.LastContact = now
		if release != "" {
			out.Release = release
		}
	}
	return out, nil
}

func (s Runners) ClaimFor(ctx context.Context, runner string, now time.Time) (job.Claimed, error) {
	now = now.UTC()
	var out job.Claimed
	found, refused, swept := false, false, 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		found, refused, swept = false, false, 0
		r, err := runnerIn(ctx, tx, runner)
		if errors.Is(err, job.ErrNoRunner) || (err == nil && r.Revoked()) {
			refused = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim job: %w", err)
		}
		out, found, swept, err = claimIn(ctx, tx, r.Claimant(), now)
		return err
	})
	s.s.tellQueued(err == nil && swept > 0)
	switch {
	case err != nil:
		return job.Claimed{}, err
	case refused:
		return job.Claimed{}, job.ErrUnauthenticated
	case !found:
		return job.Claimed{}, job.ErrNoJob
	}
	return out, nil
}

func (s Runners) Workspaces(ctx context.Context) ([]job.Workspace, error) {
	var out []job.Workspace
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM workspaces ORDER BY name`)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var w job.Workspace
			if err := rows.Scan(&w.ID, &w.Name); err != nil {
				return fmt.Errorf("list workspaces: %w", err)
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}
