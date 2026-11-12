// Package sessiontest holds the contract suite of the session store port and
// an in-memory store that passes it. Every store runs Suite, so the SQLite
// adapter and later ones keep the same rules.
package sessiontest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
)

// Memory is a session store in memory, for the tests of the packages that
// use sessions. Its zero value is ready.
type Memory struct {
	mu       sync.Mutex
	sessions map[string]session.Session
}

var _ session.Store = (*Memory)(nil)

func (m *Memory) Create(_ context.Context, s session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]session.Session{}
	}
	if _, ok := m.sessions[s.Hash]; ok {
		return session.ErrExists
	}
	m.sessions[s.Hash] = s
	return nil
}

func (m *Memory) Get(_ context.Context, hash string) (session.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	if !ok {
		return session.Session{}, session.ErrNotFound
	}
	return s, nil
}

func (m *Memory) Touch(_ context.Context, hash string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	if !ok {
		return session.ErrNotFound
	}
	s.LastSeenAt = at.UTC()
	m.sessions[hash] = s
	return nil
}

func (m *Memory) Replace(_ context.Context, old string, next session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[old]; !ok {
		return session.ErrNotFound
	}
	if _, ok := m.sessions[next.Hash]; ok {
		return session.ErrExists
	}
	delete(m.sessions, old)
	m.sessions[next.Hash] = next
	return nil
}

func (m *Memory) Delete(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}

func (m *Memory) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for hash, s := range m.sessions {
		if s.Expired(now) {
			delete(m.sessions, hash)
			n++
		}
	}
	return n, nil
}

// Suite runs the contract of the session store port on stores made by
// newStore, one per case.
func Suite(t *testing.T, newStore func(t *testing.T) session.Store) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	sess := func(t *testing.T, hash string, created, seen time.Time) session.Session {
		t.Helper()
		return session.Session{Hash: hash, CSRF: "csrf-" + hash, CreatedAt: created, LastSeenAt: seen}
	}
	hash := func(c byte) string {
		b := make([]byte, 64)
		for i := range b {
			b[i] = c
		}
		return string(b)
	}
	a, b, c := hash('a'), hash('b'), hash('c')

	t.Run("create and get", func(t *testing.T) {
		st := newStore(t)
		want := sess(t, a, t0, t0.Add(time.Minute))
		if err := st.Create(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := st.Get(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		if got.Hash != want.Hash || got.CSRF != want.CSRF || !got.CreatedAt.Equal(want.CreatedAt) || !got.LastSeenAt.Equal(want.LastSeenAt) {
			t.Errorf("Get = %+v, want %+v", got, want)
		}
		if _, err := st.Get(ctx, b); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Get of an unknown hash = %v, want ErrNotFound", err)
		}
	})

	t.Run("create refuses a taken hash", func(t *testing.T) {
		st := newStore(t)
		if err := st.Create(ctx, sess(t, a, t0, t0)); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, sess(t, a, t0.Add(time.Hour), t0.Add(time.Hour))); !errors.Is(err, session.ErrExists) {
			t.Errorf("second Create = %v, want ErrExists", err)
		}
		if got, _ := st.Get(ctx, a); !got.CreatedAt.Equal(t0) {
			t.Error("the refused Create changed the session")
		}
	})

	t.Run("touch", func(t *testing.T) {
		st := newStore(t)
		if err := st.Create(ctx, sess(t, a, t0, t0)); err != nil {
			t.Fatal(err)
		}
		later := t0.Add(2 * time.Hour).In(time.FixedZone("CET", 3600))
		if err := st.Touch(ctx, a, later); err != nil {
			t.Fatal(err)
		}
		got, _ := st.Get(ctx, a)
		if !got.LastSeenAt.Equal(later) || !got.CreatedAt.Equal(t0) {
			t.Errorf("after Touch: %+v", got)
		}
		if err := st.Touch(ctx, b, later); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Touch of an unknown hash = %v, want ErrNotFound", err)
		}
	})

	t.Run("replace", func(t *testing.T) {
		st := newStore(t)
		for _, s := range []session.Session{sess(t, a, t0, t0), sess(t, c, t0, t0)} {
			if err := st.Create(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		next := sess(t, b, t0, t0.Add(time.Hour))
		if err := st.Replace(ctx, a, next); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Get(ctx, a); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("old session after Replace: %v", err)
		}
		if got, err := st.Get(ctx, b); err != nil || got.CSRF != next.CSRF {
			t.Errorf("new session after Replace: %+v, %v", got, err)
		}
		// A replaced session cannot be replaced again, and a taken hash
		// is refused with nothing changed.
		if err := st.Replace(ctx, a, sess(t, hash('d'), t0, t0)); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Replace of a gone session = %v, want ErrNotFound", err)
		}
		if err := st.Replace(ctx, b, sess(t, c, t0, t0)); !errors.Is(err, session.ErrExists) {
			t.Errorf("Replace onto a taken hash = %v, want ErrExists", err)
		}
		if _, err := st.Get(ctx, b); err != nil {
			t.Errorf("a refused Replace removed the session: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		st := newStore(t)
		if err := st.Create(ctx, sess(t, a, t0, t0)); err != nil {
			t.Fatal(err)
		}
		if err := st.Delete(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Get(ctx, a); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Get after Delete = %v", err)
		}
		if err := st.Delete(ctx, a); err != nil {
			t.Errorf("Delete of a gone session = %v, want nil", err)
		}
	})

	t.Run("delete expired", func(t *testing.T) {
		st := newStore(t)
		now := t0.Add(40 * 24 * time.Hour)
		live := sess(t, a, now.Add(-time.Hour), now.Add(-time.Minute))
		idle := sess(t, b, now.Add(-10*24*time.Hour), now.Add(-session.IdleLifetime))
		old := sess(t, c, now.Add(-session.MaxLifetime), now.Add(-time.Minute))
		for _, s := range []session.Session{live, idle, old} {
			if err := st.Create(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		n, err := st.DeleteExpired(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("DeleteExpired removed %d, want 2", n)
		}
		if _, err := st.Get(ctx, a); err != nil {
			t.Errorf("a live session was removed: %v", err)
		}
		for _, h := range []string{b, c} {
			if _, err := st.Get(ctx, h); !errors.Is(err, session.ErrNotFound) {
				t.Errorf("an expired session is kept: %v", err)
			}
		}
	})
}
