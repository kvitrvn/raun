package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
)

var _ repository.EventStore = Repositories{}

func (s Repositories) Record(ctx context.Context, k repository.Key, row *repository.LoggedEvent) (bool, error) {
	logged := false
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		logged = false
		// The write transaction holds the write lock: what the check sees
		// stays true until the insert, and the unique keys hold anyway.
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE forge = ? AND (delivery = ? OR digest = ?)`,
			k.Forge, k.Delivery, k.Digest).Scan(&known); err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		if known > 0 {
			return repository.ErrDuplicate
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries (forge, delivery, digest, received_at) VALUES (?, ?, ?, ?)`,
			k.Forge, k.Delivery, k.Digest, formatTime(k.ReceivedAt)); err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		if row == nil {
			return nil
		}
		if row.Forge != k.Forge || row.Delivery != k.Delivery {
			return fmt.Errorf("record delivery: the row of %s/%s under the key of %s/%s", row.Forge, row.Delivery, k.Forge, k.Delivery)
		}
		// Logged only while the repository is active, as the same
		// transaction sees it: a deactivation cannot fall in between.
		res, err := tx.ExecContext(ctx, `INSERT INTO repository_events
			(forge, repository, delivery, event, action, ref, before, after, received_at, result)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			WHERE EXISTS (SELECT 1 FROM repositories WHERE forge = ? AND forge_id = ? AND active = 1)`,
			row.Forge, row.Repository, row.Delivery, row.Event, row.Action, row.Ref, row.Before, row.After,
			formatTime(row.ReceivedAt), string(row.Result), row.Forge, row.Repository)
		if err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		logged = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return logged, nil
}

func (s Repositories) Log(ctx context.Context, forge, repo string, limit int) ([]repository.LoggedEvent, error) {
	var out []repository.LoggedEvent
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT forge, repository, delivery, event, action, ref, before, after, received_at, result
			FROM repository_events WHERE forge = ? AND repository = ?
			ORDER BY received_at DESC, delivery DESC LIMIT ?`, forge, repo, max(limit, 0))
		if err != nil {
			return fmt.Errorf("read event log: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e repository.LoggedEvent
			var at, result string
			if err := rows.Scan(&e.Forge, &e.Repository, &e.Delivery, &e.Event, &e.Action, &e.Ref, &e.Before, &e.After, &at, &result); err != nil {
				return fmt.Errorf("read event log: %w", err)
			}
			t, err := parseTime(at)
			if err != nil {
				return fmt.Errorf("read event log: received_at: %w", err)
			}
			e.ReceivedAt, e.Result = t, repository.Result(result)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func (s Repositories) PurgeEvents(ctx context.Context, before time.Time) (int, error) {
	n := 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		n = 0
		for _, table := range []string{"deliveries", "repository_events"} {
			res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE received_at < ?`, formatTime(before))
			if err != nil {
				return fmt.Errorf("purge %s: %w", table, err)
			}
			d, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("purge %s: %w", table, err)
			}
			n += int(d)
		}
		return nil
	})
	return n, err
}
