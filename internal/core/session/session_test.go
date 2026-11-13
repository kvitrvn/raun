package session

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// counter is a deterministic source of bytes.
type counter struct{ n byte }

func (c *counter) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}

func TestNew(t *testing.T) {
	id, s, err := New(&counter{}, t0.In(time.FixedZone("CET", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 43 || strings.ContainsAny(id, "+/=") {
		t.Errorf("identifier %q is not 32 bytes of unpadded base64url", id)
	}
	if len(s.CSRF) != 43 || s.CSRF == id {
		t.Errorf("CSRF token %q", s.CSRF)
	}
	hash, err := HashOf(id)
	if err != nil || hash != s.Hash || len(hash) != 64 || strings.Contains(s.Hash, id) {
		t.Errorf("hash %q of %q: %q, %v", s.Hash, id, hash, err)
	}
	if !s.CreatedAt.Equal(t0) || s.CreatedAt.Location() != time.UTC || !s.LastSeenAt.Equal(t0) {
		t.Errorf("times %v %v", s.CreatedAt, s.LastSeenAt)
	}

	// Two sessions never share an identifier or a token.
	src := &counter{}
	a, sa, _ := New(src, t0)
	b, sb, _ := New(src, t0)
	if a == b || sa.CSRF == sb.CSRF || sa.Hash == sb.Hash {
		t.Error("two sessions share a secret")
	}
}

func TestNewFailsWithoutRandomness(t *testing.T) {
	if _, _, err := New(bytes.NewReader(make([]byte, 40)), t0); err == nil {
		t.Fatal("New succeeded with 40 random bytes, needs 64")
	}
	if _, _, err := New(bytes.NewReader(nil), t0); err == nil {
		t.Fatal("New succeeded without random bytes")
	}
}

func TestRenewKeepsTheAbsoluteLifetime(t *testing.T) {
	src := &counter{}
	oldID, old, err := New(src, t0)
	if err != nil {
		t.Fatal(err)
	}
	old.Account = "account-1"
	later := t0.Add(3 * 24 * time.Hour)
	id, s, err := Renew(src, old, later)
	if err != nil {
		t.Fatal(err)
	}
	if id == oldID || s.Hash == old.Hash || s.CSRF == old.CSRF {
		t.Error("renewal kept a secret")
	}
	if !s.CreatedAt.Equal(old.CreatedAt) || !s.LastSeenAt.Equal(later) {
		t.Errorf("renewed times %v %v", s.CreatedAt, s.LastSeenAt)
	}
	if s.Account != "account-1" {
		t.Errorf("renewal dropped the account: %q", s.Account)
	}
	if got, want := s.ExpiresAt(), later.Add(IdleLifetime); !got.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got, want)
	}
}

func TestHashOfRefusesMalformedIdentifiers(t *testing.T) {
	id, _, err := New(&counter{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "x", id[:42], id + "A", id + "=", strings.Replace(id, id[:1], "+", 1), strings.Repeat("A", 44), " " + id} {
		if _, err := HashOf(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("HashOf(%q) = %v, want ErrMalformed", bad, err)
		}
	}
	// The last character of 32 bytes in base64url carries 2 unused bits: a
	// value that sets them decodes to the same bytes, but is not one this
	// package issued, and would be a second key for one session.
	raw, _ := encoding.DecodeString(id)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, c := range alphabet {
		alt := id[:len(id)-1] + string(c)
		if alt == id {
			continue
		}
		if got, err := encoding.DecodeString(alt); err == nil && bytes.Equal(got, raw) {
			if _, err := HashOf(alt); !errors.Is(err, ErrMalformed) {
				t.Errorf("non-canonical %q accepted", alt)
			}
		}
	}
}

func TestLifetimes(t *testing.T) {
	s := Session{CreatedAt: t0, LastSeenAt: t0}
	tests := []struct {
		name     string
		lastSeen time.Time
		now      time.Time
		expired  bool
	}{
		{"just created", t0, t0, false},
		{"one second before the idle end", t0, t0.Add(IdleLifetime - time.Second), false},
		{"at the idle end", t0, t0.Add(IdleLifetime), true},
		{"used every 6 days, before the absolute end", t0.Add(24 * 24 * time.Hour), t0.Add(MaxLifetime - time.Second), false},
		{"used every 6 days, at the absolute end", t0.Add(24 * 24 * time.Hour), t0.Add(MaxLifetime), true},
		{"used a minute ago, past the absolute end", t0.Add(MaxLifetime), t0.Add(MaxLifetime + time.Minute), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.LastSeenAt = tt.lastSeen
			if got := s.Expired(tt.now); got != tt.expired {
				t.Errorf("Expired = %v, want %v (ends %v)", got, tt.expired, s.ExpiresAt())
			}
			idle, absolute := ExpiredBefore(tt.now)
			byBounds := !s.LastSeenAt.After(idle) || !s.CreatedAt.After(absolute)
			if byBounds != tt.expired {
				t.Errorf("ExpiredBefore disagrees with Expired: %v", byBounds)
			}
		})
	}
}

func TestNeedsTouch(t *testing.T) {
	s := Session{CreatedAt: t0, LastSeenAt: t0}
	if s.NeedsTouch(t0.Add(TouchInterval - time.Second)) {
		t.Error("touch before the interval")
	}
	if !s.NeedsTouch(t0.Add(TouchInterval)) {
		t.Error("no touch after the interval")
	}
}

func TestMaxAge(t *testing.T) {
	s := Session{CreatedAt: t0, LastSeenAt: t0}
	if got := s.MaxAge(t0); got != int(MaxLifetime/time.Second) {
		t.Errorf("MaxAge at creation = %d", got)
	}
	if got := s.MaxAge(t0.Add(MaxLifetime - 1500*time.Millisecond)); got != 1 {
		t.Errorf("MaxAge 1.5 s before the end = %d, want 1", got)
	}
	if got := s.MaxAge(t0.Add(MaxLifetime + time.Hour)); got != 0 {
		t.Errorf("MaxAge after the end = %d, want 0", got)
	}
}

func TestCheckCSRF(t *testing.T) {
	_, s, err := New(&counter{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CheckCSRF(s.CSRF) {
		t.Error("the session's token is refused")
	}
	for _, bad := range []string{"", s.CSRF[:42], s.CSRF + "x", strings.ToUpper(s.CSRF)} {
		if bad != s.CSRF && s.CheckCSRF(bad) {
			t.Errorf("token %q accepted", bad)
		}
	}
	if (Session{}).CheckCSRF("") {
		t.Error("an empty token matches a session without one")
	}
}
