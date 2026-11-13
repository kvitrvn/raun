package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
)

// tokensOf is a token service of the stub forge on a memory store where
// Alice (stub:1001) has an account and tokens, at the time now.
type tokensOf struct {
	forge   *stubForge
	store   *identitytest.Memory
	service *identity.TokenService
	now     time.Time
	tokens  identity.Tokens
}

func newTokensOf(t *testing.T) *tokensOf {
	t.Helper()
	ctx := context.Background()
	s := &tokensOf{store: &identitytest.Memory{}, now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return s.now }
	s.forge = newStubForge(clock)
	s.tokens = s.forge.issue("1001")
	alice := s.forge.people["alice"]
	err := s.store.PutSignup(ctx, identity.Signup{Session: "s", Identity: alice, Tokens: s.tokens, ExpiresAt: s.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CompleteSignup(ctx, "s", identity.Account{ID: "a1", Name: "alice"}, s.now); err != nil {
		t.Fatal(err)
	}
	s.service = identity.NewTokenService(s.forge, s.store, clock)
	return s
}

// used calls With and returns the access token f was given each time.
func (s *tokensOf) used(t *testing.T, f func(identity.Tokens) error) ([]string, error) {
	t.Helper()
	var seen []string
	err := s.service.With(context.Background(), "stub:1001", func(tk identity.Tokens) error {
		seen = append(seen, tk.Access.Reveal())
		return f(tk)
	})
	return seen, err
}

func ok(identity.Tokens) error { return nil }

func TestTokensAreRefreshedNearTheirExpiry(t *testing.T) {
	s := newTokensOf(t)
	s.now = s.now.Add(8*time.Hour - identity.RefreshMargin - time.Second)
	if seen, err := s.used(t, ok); err != nil || len(seen) != 1 || seen[0] != s.tokens.Access.Reveal() || s.forge.refreshes != 0 {
		t.Fatalf("With before the margin = %v, %v, %d refreshes", seen, err, s.forge.refreshes)
	}
	s.now = s.now.Add(time.Second)
	seen, err := s.used(t, ok)
	if err != nil || len(seen) != 1 || seen[0] == s.tokens.Access.Reveal() || s.forge.refreshes != 1 {
		t.Fatalf("With inside the margin = %v, %v, %d refreshes", seen, err, s.forge.refreshes)
	}
	stored, _ := s.store.Tokens(context.Background(), "stub:1001")
	if stored.Access.Reveal() != seen[0] || stored.Refresh == s.tokens.Refresh {
		t.Errorf("stored tokens %v, want the refreshed ones with the rotated refresh token", stored.Access.Reveal())
	}
}

func TestARefusedTokenIsRefreshedOnce(t *testing.T) {
	s := newTokensOf(t)
	calls := 0
	seen, err := s.used(t, func(tk identity.Tokens) error {
		calls++
		if calls == 1 {
			return identity.ErrUnauthorized
		}
		return nil
	})
	if err != nil || len(seen) != 2 || seen[0] == seen[1] || s.forge.refreshes != 1 {
		t.Errorf("With after a refusal = %v, %v, %d refreshes", seen, err, s.forge.refreshes)
	}
	seen, err = s.used(t, func(identity.Tokens) error { return identity.ErrUnauthorized })
	if !errors.Is(err, identity.ErrUnauthorized) || len(seen) != 2 || s.forge.refreshes != 2 {
		t.Errorf("With refused twice = %v, %v, %d refreshes; want one refresh and the error", seen, err, s.forge.refreshes)
	}
}

// TestConcurrentCallersRefreshOnce: callers that find the token expired
// together spend the refresh token once and share the new tokens.
func TestConcurrentCallersRefreshOnce(t *testing.T) {
	s := newTokensOf(t)
	s.now = s.now.Add(8 * time.Hour)
	s.forge.refreshWait = make(chan struct{})
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.service.With(context.Background(), "stub:1001", func(tk identity.Tokens) error {
				got[i] = tk.Access.Reveal()
				return nil
			})
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(s.forge.refreshWait)
	wg.Wait()
	if s.forge.refreshes != 1 {
		t.Errorf("%d refreshes, want 1", s.forge.refreshes)
	}
	if n := identity.Held(s.service); n != 0 {
		t.Errorf("the service keeps %d locks once every caller is done", n)
	}
	for _, a := range got {
		if a == "" || a != got[0] || a == s.tokens.Access.Reveal() {
			t.Fatalf("access tokens used = %v, want one shared new token", got)
		}
	}
}

func TestARefusedRefreshRevokesTheTokens(t *testing.T) {
	for name, setup := range map[string]func(s *tokensOf){
		"refused by the forge":        func(s *tokensOf) { s.forge.refreshErr = identity.ErrRevoked },
		"refresh token past its date": func(s *tokensOf) { s.now = s.tokens.RefreshExpiry },
	} {
		t.Run(name, func(t *testing.T) {
			s := newTokensOf(t)
			alive := s.store.StartSession(t, "a1")
			other := s.store.StartSession(t, "a2")
			s.now = s.now.Add(8 * time.Hour)
			setup(s)
			before := s.forge.refreshes
			if _, err := s.used(t, ok); !errors.Is(err, identity.ErrRevoked) {
				t.Errorf("With = %v, want ErrRevoked", err)
			}
			if _, err := s.store.Tokens(context.Background(), "stub:1001"); !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("the tokens outlived the refusal: %v", err)
			}
			if alive() {
				t.Error("the session of the account outlived the refusal")
			}
			if !other() {
				t.Error("the session of another account was ended")
			}
			if name == "refresh token past its date" && s.forge.refreshes != before {
				t.Error("an expired refresh token was sent to the forge")
			}
		})
	}
}

func TestATransientFailureKeepsTheTokens(t *testing.T) {
	s := newTokensOf(t)
	alive := s.store.StartSession(t, "a1")
	s.now = s.now.Add(8 * time.Hour)
	s.forge.refreshErr = identity.ErrUnavailable
	if _, err := s.used(t, ok); !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("With = %v, want ErrUnavailable", err)
	}
	if got, err := s.store.Tokens(context.Background(), "stub:1001"); err != nil || got.Access != s.tokens.Access || !alive() {
		t.Errorf("a transient failure took the tokens or the session: %v", err)
	}
}

// failingStore refuses to store tokens.
type failingStore struct{ *identitytest.Memory }

func (failingStore) PutTokens(context.Context, string, identity.Tokens) error {
	return errors.New("disk full")
}

func TestAFailedSaveIsNotUsed(t *testing.T) {
	s := newTokensOf(t)
	s.now = s.now.Add(8 * time.Hour)
	service := identity.NewTokenService(s.forge, failingStore{s.store}, func() time.Time { return s.now })
	called := false
	err := service.With(context.Background(), "stub:1001", func(identity.Tokens) error { called = true; return nil })
	if err == nil || called {
		t.Errorf("With after a failed save = %v, f called %v", err, called)
	}
}

func TestTokensOfAnUnknownIdentity(t *testing.T) {
	s := newTokensOf(t)
	err := s.service.With(context.Background(), "stub:9999", ok)
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("With of an unknown identity = %v", err)
	}
}

func TestARevocationIsObserved(t *testing.T) {
	for name, setup := range map[string]func(s *tokensOf){
		"refused by the forge":        func(s *tokensOf) { s.forge.refreshErr = identity.ErrRevoked },
		"refresh token past its date": func(s *tokensOf) { s.now = s.tokens.RefreshExpiry },
	} {
		s := newTokensOf(t)
		var seen []string
		s.service.OnRevoke(func(key string) { seen = append(seen, key) })
		s.now = s.now.Add(8 * time.Hour)
		setup(s)
		_, _ = s.used(t, ok)
		if len(seen) != 1 || seen[0] != "stub:1001" {
			t.Errorf("%s: observed %v, want stub:1001 once", name, seen)
		}
	}
	s := newTokensOf(t)
	called := false
	s.service.OnRevoke(func(string) { called = true })
	s.now = s.now.Add(8 * time.Hour)
	s.forge.refreshErr = identity.ErrUnavailable
	_, _ = s.used(t, ok)
	if called {
		t.Error("a transient failure was observed as a revocation")
	}
}

// TestPutWaitsForARefresh: the tokens of a login, stored while a refresh is
// in flight, are not overwritten by it.
func TestPutWaitsForARefresh(t *testing.T) {
	s := newTokensOf(t)
	s.now = s.now.Add(8 * time.Hour)
	s.forge.refreshWait = make(chan struct{})
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		_ = s.service.With(context.Background(), "stub:1001", ok)
	}()
	// Wait until the refresh holds the lock of the identity.
	for identity.Held(s.service) == 0 {
		time.Sleep(time.Millisecond)
	}
	login := identity.Tokens{Access: "login-access", Refresh: "login-refresh", AccessExpiry: s.now.Add(8 * time.Hour), RefreshExpiry: s.now.Add(4000 * time.Hour)}
	stored := make(chan error)
	go func() { stored <- s.service.Put(context.Background(), "stub:1001", login) }()
	select {
	case <-stored:
		t.Fatal("Put did not wait for the refresh in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(s.forge.refreshWait)
	<-refreshed
	if err := <-stored; err != nil {
		t.Fatal(err)
	}
	if got, _ := s.store.Tokens(context.Background(), "stub:1001"); got.Access != "login-access" {
		t.Errorf("the tokens of the login were overwritten by the refresh: %s", got.Access.Reveal())
	}
}
