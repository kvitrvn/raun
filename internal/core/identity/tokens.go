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
	locks map[string]*sync.Mutex
}

// NewTokenService returns the token service of forge on store, at the time
// now gives.
func NewTokenService(forge Forge, store Store, now func() time.Time) *TokenService {
	return &TokenService{forge: forge, store: store, now: now, locks: map[string]*sync.Mutex{}}
}

// lock returns the lock of the identity with this key.
func (s *TokenService) lock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	return l
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
	l.Lock()
	defer l.Unlock()
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
	return cause
}
