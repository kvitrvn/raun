package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kvitrvn/raun/internal/core/repository"
)

var _ repository.ConfigurationStore = Repositories{}

func (s Repositories) Active(ctx context.Context, forge string) ([]string, error) {
	var out []string
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT forge_id FROM repositories WHERE forge = ? AND active = 1 ORDER BY forge_id`, forge)
		if err != nil {
			return fmt.Errorf("list active repositories: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("list active repositories: %w", err)
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func (s Repositories) PutConfiguration(ctx context.Context, forge, repo string, c repository.Configuration) error {
	return s.s.write(ctx, func(tx *sql.Tx) error {
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repositories WHERE forge = ? AND forge_id = ?`, forge, repo).Scan(&known); err != nil {
			return fmt.Errorf("store configuration: %w", err)
		}
		if known == 0 {
			return repository.ErrNotFound
		}
		content := c.Content
		if content == nil {
			content = []byte{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repository_configurations (forge, repository, state, commit_id, content, reason, read_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (forge, repository) DO UPDATE SET state = excluded.state, commit_id = excluded.commit_id,
				content = excluded.content, reason = excluded.reason, read_at = excluded.read_at`,
			forge, repo, string(c.State), c.Commit, content, c.Reason, formatTime(c.ReadAt)); err != nil {
			return fmt.Errorf("store configuration: %w", err)
		}
		return nil
	})
}

func (s Repositories) Configuration(ctx context.Context, forge, repo string) (repository.Configuration, bool, error) {
	var c repository.Configuration
	found := false
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		var state, at string
		err := tx.QueryRowContext(ctx, `SELECT state, commit_id, content, reason, read_at FROM repository_configurations WHERE forge = ? AND repository = ?`,
			forge, repo).Scan(&state, &c.Commit, &c.Content, &c.Reason, &at)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read configuration: %w", err)
		}
		t, err := parseTime(at)
		if err != nil {
			return fmt.Errorf("read configuration: read_at: %w", err)
		}
		c.State, c.ReadAt, found = repository.ConfigurationState(state), t, true
		if len(c.Content) == 0 {
			c.Content = nil
		}
		return nil
	})
	return c, found, err
}
