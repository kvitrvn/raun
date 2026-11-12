// Package session holds the rules of a browser session: its identifier and
// how it is stored, its lifetimes, its renewal and its CSRF token, and the
// port of the store that keeps sessions.
//
// A session is a row on the server; the browser holds only its identifier,
// 32 random bytes sent base64url-encoded in a cookie. The store keeps the
// SHA-256 of the identifier, never the identifier, so a copy of the database
// opens no session. See docs/implementation/03-server-foundation.md#sessions.
//
// What this package does not do:
//   - read a cookie or write a response: the web adapter does, with these rules;
//   - attach an account: a session holds none until epic 4;
//   - draw randomness or read the clock itself: both are parameters, so the
//     rules can be tested exactly.
package session

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// Lifetimes of a session, constants by decision of the epic 3 review.
const (
	// IdleLifetime ends a session after this long without a request.
	IdleLifetime = 7 * 24 * time.Hour
	// MaxLifetime ends a session this long after its creation, whatever
	// its use. Renewal does not extend it.
	MaxLifetime = 30 * 24 * time.Hour
	// TouchInterval is how often the time of last use is written: at most
	// once per interval, so reading pages does not write on every request.
	TouchInterval = time.Hour
)

// secretBytes is the size of an identifier and of a CSRF token.
const secretBytes = 32

// encoding is how an identifier and a token travel: base64url, unpadded.
var encoding = base64.RawURLEncoding

var (
	// ErrNotFound: no session has this identifier.
	ErrNotFound = errors.New("session not found")
	// ErrExists: a session already has this identifier.
	ErrExists = errors.New("session already exists")
	// ErrMalformed: a value is not an identifier this package issues.
	ErrMalformed = errors.New("malformed session identifier")
)

// Session is what the store keeps of a session.
type Session struct {
	// Hash is the hexadecimal SHA-256 of the identifier: the key of the
	// session in the store.
	Hash string
	// CSRF is the token every state-changing request of the session
	// carries. It is distinct from the identifier and stored in clear: it
	// is useless without the cookie.
	CSRF       string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// New draws a session created at now from random. It returns the identifier,
// for the cookie, and the session, for the store.
func New(random io.Reader, now time.Time) (string, Session, error) {
	id, err := draw(random)
	if err != nil {
		return "", Session{}, fmt.Errorf("draw a session identifier: %w", err)
	}
	csrf, err := draw(random)
	if err != nil {
		return "", Session{}, fmt.Errorf("draw a CSRF token: %w", err)
	}
	now = now.UTC()
	return id, Session{Hash: hashOf(id), CSRF: csrf, CreatedAt: now, LastSeenAt: now}, nil
}

// Renew draws a new identifier and a new CSRF token for s at now, as at login
// or at a change of privilege. The creation time is kept, so the absolute
// lifetime is not extended. The caller replaces s with the result in one
// operation of the store.
func Renew(random io.Reader, s Session, now time.Time) (string, Session, error) {
	id, fresh, err := New(random, now)
	if err != nil {
		return "", Session{}, err
	}
	fresh.CreatedAt = s.CreatedAt
	return id, fresh, nil
}

// HashOf checks that id is an identifier this package issues and returns
// the hash the store knows it by. Any other value is ErrMalformed, so a
// forged cookie never reaches the store as a key.
func HashOf(id string) (string, error) {
	raw, err := encoding.DecodeString(id)
	if err != nil || len(raw) != secretBytes || encoding.EncodeToString(raw) != id {
		return "", ErrMalformed
	}
	return hashOf(id), nil
}

func hashOf(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func draw(random io.Reader) (string, error) {
	b := make([]byte, secretBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", err
	}
	return encoding.EncodeToString(b), nil
}

// ExpiresAt is when s ends if it is not used again: the earlier of its idle
// and its absolute end.
func (s Session) ExpiresAt() time.Time {
	idle := s.LastSeenAt.Add(IdleLifetime)
	if absolute := s.CreatedAt.Add(MaxLifetime); absolute.Before(idle) {
		return absolute
	}
	return idle
}

// Expired reports whether s has ended at now.
func (s Session) Expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt())
}

// NeedsTouch reports whether a request at now should write the time of last
// use: once per TouchInterval at most.
func (s Session) NeedsTouch(now time.Time) bool {
	return now.Sub(s.LastSeenAt) >= TouchInterval
}

// MaxAge is the number of whole seconds left before the absolute end of s,
// for the Max-Age of its cookie; 0 once it is reached.
func (s Session) MaxAge(now time.Time) int {
	left := s.CreatedAt.Add(MaxLifetime).Sub(now)
	if left <= 0 {
		return 0
	}
	return int(left / time.Second)
}

// CheckCSRF reports whether token is the CSRF token of s, in constant time.
// An empty token never matches.
func (s Session) CheckCSRF(token string) bool {
	if token == "" || s.CSRF == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRF)) == 1
}

// ExpiredBefore gives the bounds a store deletes expired sessions by at now:
// a session last seen at or before idle, or created at or before absolute,
// has ended.
func ExpiredBefore(now time.Time) (idle, absolute time.Time) {
	return now.Add(-IdleLifetime), now.Add(-MaxLifetime)
}
