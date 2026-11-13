package web

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
)

// cookieOf returns the session cookie a response sets, or nil.
func cookieOf(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// sessionRoutes adds the routes a test drives the session helpers through.
func sessionRoutes(t *testing.T, p *protected) {
	t.Helper()
	handle(p.app, "POST /start", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := p.startSession(w, r); err != nil {
			t.Errorf("startSession: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handle(p.app, "POST /renew", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := p.renewSession(w, r, r.URL.Query().Get("account")); err != nil {
			t.Errorf("renewSession: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handle(p.app, "POST /end", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := p.endSession(w, r); err != nil {
			t.Errorf("endSession: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func TestStartSessionSetsTheCookieOfTheRecord(t *testing.T) {
	// /start is a test route: it has no session yet, so it is bypassed,
	// as the login route of epic 4 will be for its first request.
	p := newProtected(t, "https", "POST /start")
	sessionRoutes(t, p)
	resp := serve(p, request(http.MethodPost, "/start", testHost, nil))
	c := cookieOf(t, resp, secureCookie)
	if c == nil {
		t.Fatalf("no %s cookie in %v", secureCookie, resp.Header["Set-Cookie"])
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Errorf("cookie attributes %+v", c)
	}
	if c.MaxAge != int(session.MaxLifetime/time.Second) {
		t.Errorf("Max-Age = %d, want the absolute lifetime", c.MaxAge)
	}
	hash, err := session.HashOf(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Get(context.Background(), hash); err != nil {
		t.Errorf("the session is not stored: %v", err)
	}
}

func TestLoopbackHTTPUsesThePlainCookie(t *testing.T) {
	p := newProtected(t, "https")
	s, err := newServer(Options{Logger: slog.New(slog.DiscardHandler), PublicHost: "localhost:8080", Scheme: "http", Ready: func(context.Context) error { return nil }, Sessions: p.store, Now: p.clock.now}, []string{"POST /start"})
	if err != nil {
		t.Fatal(err)
	}
	p.Server = s
	sessionRoutes(t, p)
	resp := serve(p, request(http.MethodPost, "/start", "localhost:8080", nil))
	c := cookieOf(t, resp, plainCookie)
	if c == nil || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v, want %s without Secure", c, plainCookie)
	}
	if cookieOf(t, resp, secureCookie) != nil {
		t.Error("a __Host- cookie was set on http")
	}
}

func TestPagesSetNoCookieWithoutASession(t *testing.T) {
	p := newProtected(t, "https")
	resp := serve(p, request(http.MethodGet, "/", testHost, nil))
	if got := resp.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("a page without a session set %v", got)
	}
	if body := readBody(t, resp); strings.Contains(body, "hx-headers") {
		t.Error("a page without a session sends a CSRF header")
	}
}

func TestSessionLookup(t *testing.T) {
	p := newProtected(t, "https")
	cookie, token := p.login(t)
	hash, _ := session.HashOf(cookie.Value)

	// A page of a session carries its token for HTMX.
	r := request(http.MethodGet, "/", testHost, nil)
	r.AddCookie(cookie)
	resp := serve(p, r)
	if body := readBody(t, resp); !strings.Contains(body, `hx-headers="{&#34;X-CSRF-Token&#34;:&#34;`+token+`&#34;}"`) {
		t.Errorf("the page lacks the hx-headers of the session")
	}
	if got := resp.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("a lookup set %v", got)
	}

	// requireSession lets it through.
	r = request(http.MethodGet, "/private", testHost, nil)
	r.AddCookie(cookie)
	if resp := serve(p, r); resp.StatusCode != http.StatusNoContent {
		t.Errorf("/private with a session = %d", resp.StatusCode)
	}
	if resp := serve(p, request(http.MethodGet, "/private", testHost, nil)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/private without a session = %d, want 401", resp.StatusCode)
	}

	// The time of last use is written once an hour at most.
	p.clock.t = p.clock.t.Add(30 * time.Minute)
	serve(p, withCookie(request(http.MethodGet, "/", testHost, nil), cookie))
	if s, _ := p.store.Get(context.Background(), hash); !s.LastSeenAt.Equal(s.CreatedAt) {
		t.Error("the time of last use was written within the interval")
	}
	p.clock.t = p.clock.t.Add(31 * time.Minute)
	serve(p, withCookie(request(http.MethodGet, "/", testHost, nil), cookie))
	if s, _ := p.store.Get(context.Background(), hash); !s.LastSeenAt.Equal(p.clock.t) {
		t.Errorf("last seen %v, want %v", s.LastSeenAt, p.clock.t)
	}
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request {
	r.AddCookie(c)
	return r
}

func TestExpiredSessionsAreDeleted(t *testing.T) {
	tests := []struct {
		name    string
		advance func(p *protected, cookie *http.Cookie)
	}{
		{"idle for 7 days", func(p *protected, _ *http.Cookie) {
			p.clock.t = p.clock.t.Add(session.IdleLifetime)
		}},
		{"used every day for 30 days", func(p *protected, cookie *http.Cookie) {
			for range 30 {
				p.clock.t = p.clock.t.Add(24 * time.Hour)
				serve(p, withCookie(request(http.MethodGet, "/", testHost, nil), cookie))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProtected(t, "https")
			cookie, _ := p.login(t)
			hash, _ := session.HashOf(cookie.Value)
			tt.advance(p, cookie)
			resp := serve(p, withCookie(request(http.MethodGet, "/private", testHost, nil), cookie))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("an expired session reached /private: %d", resp.StatusCode)
			}
			if c := cookieOf(t, resp, secureCookie); c == nil || c.MaxAge >= 0 {
				t.Errorf("the cookie of an expired session is not cleared: %+v", c)
			}
			if _, err := p.store.Get(context.Background(), hash); !errors.Is(err, session.ErrNotFound) {
				t.Errorf("the expired session is kept: %v", err)
			}
		})
	}
}

func TestUnknownCookiesAreCleared(t *testing.T) {
	p := newProtected(t, "https")
	id, _, err := session.New(rand.Reader, p.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"forged", id} {
		resp := serve(p, withCookie(request(http.MethodGet, "/", testHost, nil), &http.Cookie{Name: secureCookie, Value: value}))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("cookie %q: status %d", value, resp.StatusCode)
		}
		if c := cookieOf(t, resp, secureCookie); c == nil || c.MaxAge >= 0 {
			t.Errorf("cookie %q is not cleared", value)
		}
	}
}

func TestRenewAndEndSession(t *testing.T) {
	p := newProtected(t, "https")
	sessionRoutes(t, p)
	cookie, token := p.login(t)
	oldHash, _ := session.HashOf(cookie.Value)
	old, _ := p.store.Get(context.Background(), oldHash)

	p.clock.t = p.clock.t.Add(48 * time.Hour)
	r := withCookie(request(http.MethodPost, "/renew", testHost, nil), cookie)
	r.Header.Set(csrfHeader, token)
	resp := serve(p, r)
	fresh := cookieOf(t, resp, secureCookie)
	if fresh == nil || fresh.Value == cookie.Value {
		t.Fatalf("renewal set %+v", fresh)
	}
	if want := int((session.MaxLifetime - 48*time.Hour) / time.Second); fresh.MaxAge != want {
		t.Errorf("renewed Max-Age = %d, want %d: the absolute lifetime must not grow", fresh.MaxAge, want)
	}
	if _, err := p.store.Get(context.Background(), oldHash); !errors.Is(err, session.ErrNotFound) {
		t.Error("the old session survives its renewal")
	}
	newHash, _ := session.HashOf(fresh.Value)
	renewed, err := p.store.Get(context.Background(), newHash)
	if err != nil || renewed.CSRF == old.CSRF || !renewed.CreatedAt.Equal(old.CreatedAt) {
		t.Errorf("renewed session %+v, %v", renewed, err)
	}

	// The old token no longer passes; the new one does, and ends it.
	r = withCookie(request(http.MethodPost, "/end", testHost, nil), fresh)
	r.Header.Set(csrfHeader, token)
	if resp := serve(p, r); resp.StatusCode != http.StatusForbidden {
		t.Errorf("the token of the replaced session = %d, want 403", resp.StatusCode)
	}
	r = withCookie(request(http.MethodPost, "/end", testHost, nil), fresh)
	r.Header.Set(csrfHeader, renewed.CSRF)
	resp = serve(p, r)
	if c := cookieOf(t, resp, secureCookie); c == nil || c.MaxAge >= 0 {
		t.Error("ending the session does not clear its cookie")
	}
	if _, err := p.store.Get(context.Background(), newHash); !errors.Is(err, session.ErrNotFound) {
		t.Error("the ended session is kept")
	}
	// A revoked session no longer reaches what needs one.
	if resp := serve(p, withCookie(request(http.MethodGet, "/private", testHost, nil), fresh)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked session reached /private: %d", resp.StatusCode)
	}
}

// failing is a store whose reads fail.
type failing struct{ session.Store }

func (failing) Get(context.Context, string) (session.Session, error) {
	return session.Session{}, errors.New("database is locked")
}

func TestStoreFailureIsAnError(t *testing.T) {
	p := newProtected(t, "https")
	cookie, _ := p.login(t)
	s, err := newServer(Options{Logger: slog.New(slog.DiscardHandler), PublicHost: testHost, Scheme: "https", Ready: func(context.Context) error { return nil }, Sessions: failing{p.store}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := serve(s, withCookie(request(http.MethodGet, "/", testHost, nil), cookie))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a failing store = %d, want 500", resp.StatusCode)
	}
}

// vanishing is a store whose sessions disappear between a lookup and its
// touch, as after a logout in another tab.
type vanishing struct{ session.Store }

func (vanishing) Touch(context.Context, string, time.Time) error { return session.ErrNotFound }

func TestSessionGoneBeforeItsTouch(t *testing.T) {
	p := newProtected(t, "https")
	cookie, _ := p.login(t)
	s, err := newServer(Options{Logger: slog.New(slog.DiscardHandler), PublicHost: testHost, Scheme: "https", Ready: func(context.Context) error { return nil }, Sessions: vanishing{p.store}, Now: func() time.Time { return p.clock.now().Add(2 * time.Hour) }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := serve(s, withCookie(request(http.MethodGet, "/", testHost, nil), cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a session gone before its touch = %d, want the page", resp.StatusCode)
	}
	if c := cookieOf(t, resp, secureCookie); c == nil || c.MaxAge >= 0 {
		t.Error("its cookie is not cleared")
	}
	if body := readBody(t, resp); strings.Contains(body, "hx-headers") {
		t.Error("the page still carries the token of the gone session")
	}
}
