package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
)

// Sessions is the session store on the same database file as the knowledge
// store, through its pools. It is a type of its own because the two ports
// name their operations alike.
type Sessions struct {
	s *Store
}

var _ session.Store = Sessions{}

// Sessions returns the session store of this database.
func (s *Store) Sessions() Sessions {
	return Sessions{s: s}
}

func (st Sessions) Create(ctx context.Context, sess session.Session) error {
	return st.s.write(ctx, func(tx *sql.Tx) error {
		return insertSession(ctx, tx, sess)
	})
}

// insertSession stores sess inside the write transaction tx, which holds the
// write lock, so the hash checked free stays free until the insert.
func insertSession(ctx context.Context, tx *sql.Tx, sess session.Session) error {
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE hash = ?`, sess.Hash).Scan(&taken); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	if taken > 0 {
		return session.ErrExists
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (hash, csrf, created_at, last_seen_at) VALUES (?, ?, ?, ?)`,
		sess.Hash, sess.CSRF, formatTime(sess.CreatedAt), formatTime(sess.LastSeenAt))
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (st Sessions) Get(ctx context.Context, hash string) (session.Session, error) {
	var sess session.Session
	err := st.s.read(ctx, func(tx *sql.Tx) error {
		var created, seen string
		err := tx.QueryRowContext(ctx,
			`SELECT hash, csrf, created_at, last_seen_at FROM sessions WHERE hash = ?`, hash).
			Scan(&sess.Hash, &sess.CSRF, &created, &seen)
		if errors.Is(err, sql.ErrNoRows) {
			return session.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read session: %w", err)
		}
		if sess.CreatedAt, err = parseTime(created); err != nil {
			return fmt.Errorf("read session: created_at: %w", err)
		}
		if sess.LastSeenAt, err = parseTime(seen); err != nil {
			return fmt.Errorf("read session: last_seen_at: %w", err)
		}
		return nil
	})
	if err != nil {
		return session.Session{}, err
	}
	return sess, nil
}

func (st Sessions) Touch(ctx context.Context, hash string, at time.Time) error {
	return st.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE hash = ?`, formatTime(at), hash)
		if err != nil {
			return fmt.Errorf("touch session: %w", err)
		}
		return requireOneRow(res)
	})
}

func (st Sessions) Replace(ctx context.Context, old string, next session.Session) error {
	return st.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, old)
		if err != nil {
			return fmt.Errorf("replace session: %w", err)
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		return insertSession(ctx, tx, next)
	})
}

func (st Sessions) Delete(ctx context.Context, hash string) error {
	return st.s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hash); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		return nil
	})
}

func (st Sessions) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	idle, absolute := session.ExpiredBefore(now)
	var n int64
	err := st.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE last_seen_at <= ? OR created_at <= ?`,
			formatTime(idle), formatTime(absolute))
		if err != nil {
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return session.ErrNotFound
	}
	return nil
}
