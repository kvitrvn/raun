package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
	"github.com/kvitrvn/raun/internal/core/session/sessiontest"
)

// clock is a time a test moves by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// protected is a server for https://raun.example with a store and a clock
// the test holds, a route POST /act that answers 204, and the given bypass
// patterns.
type protected struct {
	*Server
	store *sessiontest.Memory
	clock *clock
	logs  *bytes.Buffer
}

func newProtected(t *testing.T, scheme string, bypass ...string) *protected {
	t.Helper()
	var logs bytes.Buffer
	host := testHost
	p := &protected{store: &sessiontest.Memory{}, clock: &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}, logs: &logs}
	s, err := newServer(Options{
		Logger:     slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PublicHost: host,
		Scheme:     scheme,
		Ready:      func(context.Context) error { return nil },
		Sessions:   p.store,
		Now:        p.clock.now,
		Random:     rand.Reader,
	}, bypass)
	if err != nil {
		t.Fatal(err)
	}
	act := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	handle(s.app, "POST /act", act)
	handle(s.app, "POST /hooks/{forge}", act)
	handle(s.app, "GET /private", s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	p.Server = s
	return p
}

// login stores a new session and returns its cookie and CSRF token.
func (p *protected) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	id, sess, err := session.New(rand.Reader, p.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.store.Create(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: p.cookieName(), Value: id}, sess.CSRF
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	p := newProtected(t, "https")
	cookie, token := p.login(t)
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		status  int
	}{
		{"cross-site POST", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site POST", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign Origin without Sec-Fetch-Site", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"same-origin POST", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNoContent},
		{"own Origin without Sec-Fetch-Site", http.MethodPost, map[string]string{"Origin": "https://raun.example"}, http.StatusNoContent},
		{"neither header, as a non-browser client", http.MethodPost, nil, http.StatusNoContent},
		{"cross-site GET changes nothing and passes", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(tt.method, "/act", testHost, nil)
			r.AddCookie(cookie)
			r.Header.Set(csrfHeader, token)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			resp := serve(p, r)
			if tt.method == http.MethodGet {
				// GET /act has no route; reaching the mux is the point.
				if resp.StatusCode == http.StatusForbidden {
					t.Fatal("a GET was refused as cross-origin")
				}
				return
			}
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
		})
	}
	if !strings.Contains(p.logs.String(), "cross-origin request refused") {
		t.Error("a refusal is not logged")
	}
}

func TestCSRFToken(t *testing.T) {
	p := newProtected(t, "https")
	cookie, token := p.login(t)
	_, other := p.login(t)
	form := func(v url.Values) io.Reader { return strings.NewReader(v.Encode()) }
	tests := []struct {
		name   string
		target string
		cookie bool
		header string
		body   io.Reader
		status int
	}{
		{"no session", "/act", false, "", nil, http.StatusForbidden},
		{"session without a token", "/act", true, "", nil, http.StatusForbidden},
		{"token of another session", "/act", true, other, nil, http.StatusForbidden},
		{"truncated token", "/act", true, token[:20], nil, http.StatusForbidden},
		{"token in the header", "/act", true, token, nil, http.StatusNoContent},
		{"token in the form", "/act", true, "", form(url.Values{csrfField: {token}}), http.StatusNoContent},
		{"wrong token in the form", "/act", true, "", form(url.Values{csrfField: {other}}), http.StatusForbidden},
		{"token twice in the form", "/act", true, "", form(url.Values{csrfField: {token, token}}), http.StatusForbidden},
		{"token in the query only", "/act?csrf=" + url.QueryEscape(token), true, "", nil, http.StatusForbidden},
		{"form above the body limit", "/act", true, "", form(url.Values{csrfField: {token}, "pad": {strings.Repeat("x", MaxBodyBytes)}}), http.StatusRequestEntityTooLarge},
		{"wrong method on a page, with the token", "/", true, token, nil, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(http.MethodPost, tt.target, testHost, tt.body)
			if tt.body != nil {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			if tt.cookie {
				r.AddCookie(cookie)
			}
			if tt.header != "" {
				r.Header.Set(csrfHeader, tt.header)
			}
			if resp := serve(p, r); resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
		})
	}
	if strings.Contains(p.logs.String(), token) {
		t.Error("the log holds a CSRF token")
	}
}

func TestBypassedRoutesSkipTheProtections(t *testing.T) {
	p := newProtected(t, "https", "POST /hooks/{forge}")
	r := request(http.MethodPost, "/hooks/github", testHost, strings.NewReader("{}"))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp := serve(p, r); resp.StatusCode != http.StatusNoContent {
		t.Errorf("bypassed route = %d, want 204", resp.StatusCode)
	}
	// Other routes keep every protection.
	r = request(http.MethodPost, "/act", testHost, nil)
	if resp := serve(p, r); resp.StatusCode != http.StatusForbidden {
		t.Errorf("unlisted route = %d, want 403", resp.StatusCode)
	}
	// A bypass skips the protections, never the Host check.
	r = request(http.MethodPost, "/hooks/github", "evil.example", nil)
	if resp := serve(p, r); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("bypassed route with a foreign Host = %d, want 421", resp.StatusCode)
	}
	if _, err := newServer(Options{Logger: slog.New(slog.DiscardHandler), PublicHost: testHost, Scheme: "https", Ready: func(context.Context) error { return nil }, Sessions: &sessiontest.Memory{}}, []string{"POST /a/{"}); err == nil {
		t.Error("an invalid bypass pattern was accepted")
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, scheme := range []string{"https", "http"} {
		t.Run(scheme, func(t *testing.T) {
			p := newProtected(t, scheme)
			host := testHost
			if scheme == "http" {
				// A loopback address is the only http public address.
				var err error
				p.Server, err = newServer(Options{Logger: slog.New(slog.DiscardHandler), PublicHost: "localhost:8080", Scheme: "http", Ready: func(context.Context) error { return nil }, Sessions: p.store}, nil)
				if err != nil {
					t.Fatal(err)
				}
				host = "localhost:8080"
			}
			targets := []struct {
				method, path, host string
			}{
				{http.MethodGet, "/", host},
				{http.MethodGet, "/nowhere", host},
				{http.MethodGet, p.assets.url("app.css"), host},
				{http.MethodGet, "/healthz", host},
				{http.MethodGet, "/", "evil.example"},
				{http.MethodPost, "/act", host},
			}
			for _, tt := range targets {
				resp := serve(p, request(tt.method, tt.path, tt.host, nil))
				h := resp.Header
				if h.Get("Content-Security-Policy") != contentSecurityPolicy || h.Get("X-Content-Type-Options") != "nosniff" ||
					h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" {
					t.Errorf("%s %s (%d): headers %v", tt.method, tt.path, resp.StatusCode, h)
				}
				if got := h.Get("Strict-Transport-Security"); (scheme == "https") != (got == hsts) {
					t.Errorf("%s %s: Strict-Transport-Security = %q", tt.method, tt.path, got)
				}
				if cc := h.Get("Cache-Control"); cc == "" {
					t.Errorf("%s %s: no Cache-Control", tt.method, tt.path)
				}
			}
		})
	}
}

func TestUncleanPathsMatchNoBypass(t *testing.T) {
	p := newProtected(t, "https", "POST /hooks/{forge}")
	for _, target := range []string{"/hooks//github", "/x/../hooks/github"} {
		r := request(http.MethodPost, target, testHost, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if p.bypass.skips(r) {
			t.Errorf("%s matches the bypass", target)
		}
		// Refused, or redirected to the clean path before any protection:
		// never served by the bypassed route.
		if resp := serve(p, r); resp.StatusCode == http.StatusNoContent {
			t.Errorf("cross-site POST %s reached the bypassed route", target)
		}
	}
}
