package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RefreshMargin is how long before its expiry an access token is refreshed.
const RefreshMargin = 5 * time.Minute

// TokenService gives the tokens of forge identities to the calls that need
// them, refreshed when they are about to expire or the forge refuses them.
// A refresh the forge refuses for good revokes the tokens and ends the
// sessions of the account. Refreshes of one identity are serialized, so a
// refresh token, which serves once, is never spent twice. It holds its
// locks in memory: one server process serves a database.
type TokenService struct {
	forge Forge
	store Store
	now   func() time.Time

	mu    sync.Mutex
	locks map[string]*keyLock
	// revoked are told the key of an identity whose tokens were revoked.
	revoked []func(key string)
}

// keyLock is the lock of one identity, kept while a caller holds or waits
// for it.
type keyLock struct {
	sync.Mutex
	users int
}

// NewTokenService returns the token service of forge on store, at the time
// now gives.
func NewTokenService(forge Forge, store Store, now func() time.Time) *TokenService {
	return &TokenService{forge: forge, store: store, now: now, locks: map[string]*keyLock{}}
}

// lock takes the lock of the identity with this key.
func (s *TokenService) lock(key string) *keyLock {
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &keyLock{}
		s.locks[key] = l
	}
	l.users++
	s.mu.Unlock()
	l.Lock()
	return l
}

// unlock releases l, the lock of key, and forgets it once nobody holds or
// waits for it, so the service keeps no lock per identity it ever saw.
func (s *TokenService) unlock(key string, l *keyLock) {
	l.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	l.users--
	if l.users == 0 {
		delete(s.locks, key)
	}
}

// OnRevoke registers f, told the key of every identity whose tokens the
// service revokes, once they are gone. f runs while the lock of that identity
// is held: it takes no lock but its own and never calls back into s.
func (s *TokenService) OnRevoke(f func(key string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = append(s.revoked, f)
}

// Put stores the tokens of the identity with this key, under the lock of the
// identity: a refresh in flight finishes first, and then reads these tokens,
// so it never overwrites nor revokes the tokens of a login that just
// completed.
func (s *TokenService) Put(ctx context.Context, key string, t Tokens) error {
	l := s.lock(key)
	defer s.unlock(key, l)
	return s.store.PutTokens(ctx, key, t)
}

// held is the number of identities whose lock is kept, for the tests.
func (s *TokenService) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.locks)
}

// With calls f with the tokens of the identity with this key, refreshed
// first when the access token expires within RefreshMargin. When f returns
// ErrUnauthorized, the tokens are refreshed and f is called once more.
// ErrNotFound when the identity has no tokens; ErrRevoked once the forge
// refused the refresh for good and the tokens are revoked; the error of f
// otherwise.
func (s *TokenService) With(ctx context.Context, key string, f func(Tokens) error) error {
	t, err := s.current(ctx, key, "")
	if err != nil {
		return err
	}
	err = f(t)
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}
	t, err = s.current(ctx, key, t.Access)
	if err != nil {
		return err
	}
	return f(t)
}

// current returns the tokens of key, refreshed when they expire within the
// margin, or when refused is their access token: the forge refused it.
func (s *TokenService) current(ctx context.Context, key string, refused Secret) (Tokens, error) {
	l := s.lock(key)
	defer s.unlock(key, l)
	// Read under the lock: a caller that waited finds the tokens another
	// one refreshed, and does not spend the refresh token again.
	t, err := s.store.Tokens(ctx, key)
	if err != nil {
		return Tokens{}, err
	}
	now := s.now()
	stale := refused != "" && t.Access == refused
	if !stale && now.Add(RefreshMargin).Before(t.AccessExpiry) {
		return t, nil
	}
	if !now.Before(t.RefreshExpiry) {
		return Tokens{}, s.revoke(ctx, key, ErrRevoked)
	}
	fresh, err := s.forge.Refresh(ctx, t)
	if errors.Is(err, ErrRevoked) {
		return Tokens{}, s.revoke(ctx, key, err)
	}
	if err != nil {
		return Tokens{}, err
	}
	if err := s.store.PutTokens(ctx, key, fresh); err != nil {
		return Tokens{}, fmt.Errorf("store the refreshed tokens: %w", err)
	}
	return fresh, nil
}

// revoke revokes the tokens of key after cause, and returns cause, or the
// error of the store.
func (s *TokenService) revoke(ctx context.Context, key string, cause error) error {
	if err := s.store.RevokeTokens(ctx, key); err != nil {
		return fmt.Errorf("revoke the tokens: %w (after %w)", err, cause)
	}
	s.mu.Lock()
	observers := append([]func(string){}, s.revoked...)
	s.mu.Unlock()
	for _, f := range observers {
		f(key)
	}
	return cause
}
