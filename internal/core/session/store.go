package session

import (
	"context"
	"time"
)

// Store keeps sessions by the hash of their identifier. Every store passes
// the contract suite of package sessiontest.
type Store interface {
	// Create stores s. ErrExists if a session has its hash.
	Create(ctx context.Context, s Session) error
	// Get returns the session with this hash, expired or not: expiry is
	// the rule of this package. ErrNotFound if none.
	Get(ctx context.Context, hash string) (Session, error)
	// Touch sets the time of last use of the session with this hash.
	// ErrNotFound if none.
	Touch(ctx context.Context, hash string, at time.Time) error
	// Replace deletes the session with hash old and stores next, in one
	// operation: a renewal never leaves both or neither. ErrNotFound if old
	// is gone, ErrExists if next's hash is taken; nothing changes then.
	Replace(ctx context.Context, old string, next Session) error
	// Delete removes the session with this hash, if any: a revocation
	// succeeds whether or not the session still exists.
	Delete(ctx context.Context, hash string) error
	// DeleteExpired removes every session that has ended at now, by the
	// bounds of ExpiredBefore, and returns how many.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}
