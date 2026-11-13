package github

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// login logs person in on g through a and returns the tokens.
func login(t *testing.T, g *gitHub, a *App, person int64) identity.Tokens {
	t.Helper()
	verifier, err := identity.NewVerifier(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := g.approve(person, a.AuthorizeURL("state", identity.Challenge(verifier)))
	if err != nil {
		t.Fatal(err)
	}
	_, tokens, err := a.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

// on answers the requests to path with h, before the routes of g.
func (g *gitHub) on(path string, h func(w http.ResponseWriter, r *http.Request) bool) {
	g.change(func() {
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			return r.URL.Path == path && h(w, r)
		}
	})
}

func (g *gitHub) callsTo(method, path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[method+" "+path]
}

func TestAuthorizeURL(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	u, err := url.Parse(a.AuthorizeURL("the-state", "the-challenge"))
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"client_id": {g.clientID}, "redirect_uri": {g.callback}, "state": {"the-state"},
		"code_challenge": {"the-challenge"}, "code_challenge_method": {"S256"},
	}
	if u.Scheme+"://"+u.Host != g.srv.URL || u.Path != "/login/oauth/authorize" || u.Query().Encode() != want.Encode() {
		t.Errorf("AuthorizeURL = %s", u)
	}
}

func TestExchange(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	verifier, _ := identity.NewVerifier(rand.Reader)
	code, _, _ := g.approve(1001, a.AuthorizeURL("s", identity.Challenge(verifier)))
	id, tokens, err := a.Exchange(ctx, code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if id != (identity.Identity{Forge: "github", ID: "1001", Login: "alice", Name: "Alice Martin"}) {
		t.Errorf("identity = %+v", id)
	}
	now := g.clock.Now()
	if !tokens.AccessExpiry.Equal(now.Add(accessLifetime)) || !tokens.RefreshExpiry.Equal(now.Add(refreshLifetime)) {
		t.Errorf("expiries = %v, %v", tokens.AccessExpiry, tokens.RefreshExpiry)
	}
}

func TestTokenEndpointFailures(t *testing.T) {
	key := testKey(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		want   error
		detail string
	}{
		{"wrong client credentials", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		}, identity.ErrUnavailable, "incorrect_client_credentials"},
		{"an error that is not a code", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]string{"error": "<b>code-sentinel</b>"})
		}, identity.ErrUnavailable, "(invalid)"},
		{"user tokens that do not expire", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "ghu_sentinel", "token_type": "bearer", "scope": ""})
		}, identity.ErrUnavailable, "enable the expiration"},
		{"no access token", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"refresh_token": "ghr_sentinel", "refresh_token_expires_in": 10})
		}, identity.ErrUnavailable, "malformed response"},
		{"a server error, never retried", func(w http.ResponseWriter) {
			http.Error(w, "boom", http.StatusBadGateway)
		}, identity.ErrUnavailable, "server error 502"},
		{"a rate limit, never retried", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		}, identity.ErrUnavailable, "rate limited"},
		{"a status GitHub does not give", func(w http.ResponseWriter) {
			http.Error(w, "no", http.StatusUnauthorized)
		}, identity.ErrUnavailable, "unexpected status 401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitHub(t, key)
			a := g.app(t, key, nil)
			verifier, _ := identity.NewVerifier(rand.Reader)
			code, _, _ := g.approve(1001, a.AuthorizeURL("s", identity.Challenge(verifier)))
			g.on(tokenPath, func(w http.ResponseWriter, _ *http.Request) bool { tc.answer(w); return true })
			_, _, exchangeErr := a.Exchange(ctx, code, verifier)
			if !errors.Is(exchangeErr, tc.want) || errors.Is(exchangeErr, identity.ErrInvalidCode) || !strings.Contains(exchangeErr.Error(), tc.detail) {
				t.Errorf("Exchange = %v, want %v with %q", exchangeErr, tc.want, tc.detail)
			}
			_, refreshErr := a.Refresh(ctx, identity.Tokens{Refresh: "ghr_refresh-sentinel"})
			if !errors.Is(refreshErr, tc.want) || errors.Is(refreshErr, identity.ErrRevoked) {
				t.Errorf("Refresh = %v, want %v", refreshErr, tc.want)
			}
			for _, e := range []error{exchangeErr, refreshErr} {
				if strings.Contains(e.Error(), "sentinel") || strings.Contains(e.Error(), verifier) || strings.Contains(e.Error(), code) {
					t.Errorf("error %q holds a secret", e)
				}
			}
			if n := g.callsTo(http.MethodPost, tokenPath); n != 2 {
				t.Errorf("%d posts to the token endpoint, want one per call", n)
			}
		})
	}
}

func TestExchangeRetriesTheIdentity(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	var waits []time.Duration
	a := g.app(t, key, &waits)
	failed := false
	g.on("/api/v3/user", func(w http.ResponseWriter, _ *http.Request) bool {
		if failed {
			return false
		}
		failed = true
		http.Error(w, "boom", http.StatusServiceUnavailable)
		return true
	})
	login(t, g, a, 1001)
	if n := g.callsTo(http.MethodGet, "/api/v3/user"); n != 2 || len(waits) != 1 {
		t.Errorf("%d calls to /user and waits %v, want one retry", n, waits)
	}
}

func TestPermissionMapsEveryRole(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	tokens := login(t, g, a, 1001)
	repo := identity.Repository{Forge: "github", ID: "2001", FullName: "acme/api"}
	for role, want := range map[string]identity.Permission{
		"admin": identity.PermissionDecide, "maintain": identity.PermissionDecide, "write": identity.PermissionDecide,
		"triage": identity.PermissionView, "read": identity.PermissionView, "": identity.PermissionNone,
	} {
		g.change(func() { g.roles[1001][2001] = role })
		if got, err := a.Permission(context.Background(), tokens, repo); err != nil || got != want {
			t.Errorf("Permission with the role %q = %s, %v; want %s", role, got, err, want)
		}
	}
}

func TestPermissionByStableIdentifier(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	tokens := login(t, g, a, 1001)
	// acme/api is renamed: the identifier finds it under its new name.
	g.change(func() { g.repos[0].fullName = "acme/api-v2" })
	if p, err := a.Permission(ctx, tokens, identity.Repository{Forge: "github", ID: "2001", FullName: "acme/api"}); err != nil || p != identity.PermissionDecide {
		t.Errorf("Permission after a rename = %s, %v", p, err)
	}
	// A repository that only borrows the name of acme/web gets nothing.
	if p, err := a.Permission(ctx, tokens, identity.Repository{Forge: "github", ID: "9999", FullName: "acme/web"}); err != nil || p != identity.PermissionNone {
		t.Errorf("Permission of another identifier = %s, %v", p, err)
	}
	for _, r := range []identity.Repository{{Forge: "gitea", ID: "2001"}, {Forge: "github", ID: "acme/api"}, {Forge: "github", ID: "-1"}} {
		if p, err := a.Permission(ctx, tokens, r); err != nil || p != identity.PermissionNone {
			t.Errorf("Permission(%+v) = %s, %v", r, p, err)
		}
	}
}

func TestRepositoriesAcrossPagesAndInstallations(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	g.change(func() {
		g.installations[502] = false
		for i := range 250 {
			id := int64(5000 + i)
			inst := int64(501)
			if i%2 == 1 {
				inst = 502
			}
			g.repos = append(g.repos, ghRepo{id, fmt.Sprintf("bulk/r%03d", 249-i), "main", false, inst})
			g.roles[1001][id] = "read"
		}
	})
	tokens := login(t, g, a, 1001)
	repos, err := a.Repositories(context.Background(), tokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 252 || !slices.IsSortedFunc(repos, func(x, y identity.Repository) int { return strings.Compare(x.FullName, y.FullName) }) {
		t.Fatalf("%d repositories, sorted %v", len(repos), slices.IsSortedFunc(repos, func(x, y identity.Repository) int { return strings.Compare(x.FullName, y.FullName) }))
	}
	if n := g.callsTo(http.MethodGet, "/api/v3/user/installations/501/repositories"); n != 2 {
		t.Errorf("%d pages of installation 501, want 2", n)
	}
	// Permission stops at the repository it looks for.
	before := g.callsTo(http.MethodGet, "/api/v3/user/installations/502/repositories")
	if p, err := a.Permission(context.Background(), tokens, identity.Repository{Forge: "github", ID: "2001"}); err != nil || p != identity.PermissionDecide {
		t.Errorf("Permission = %s, %v", p, err)
	}
	if after := g.callsTo(http.MethodGet, "/api/v3/user/installations/502/repositories"); after != before {
		t.Errorf("Permission walked installation 502 after finding the repository")
	}
}

func TestRepositoriesNeverCutShort(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	g.change(func() {
		for i := range 150 {
			g.repos = append(g.repos, ghRepo{int64(5000 + i), fmt.Sprintf("bulk/r%03d", i), "main", false, 501})
			g.roles[1001][int64(5000+i)] = "read"
		}
	})
	tokens := login(t, g, a, 1001)
	g.on("/api/v3/user/installations/501/repositories", func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("page") != "2" {
			return false
		}
		http.Error(w, "boom", http.StatusServiceUnavailable)
		return true
	})
	if repos, err := a.Repositories(context.Background(), tokens); !errors.Is(err, identity.ErrUnavailable) || repos != nil {
		t.Errorf("Repositories with a failing page 2 = %d repositories, %v", len(repos), err)
	}
	if p, err := a.Permission(context.Background(), tokens, identity.Repository{Forge: "github", ID: "5149"}); !errors.Is(err, identity.ErrUnavailable) || p != identity.PermissionNone {
		t.Errorf("Permission with a failing page 2 = %s, %v", p, err)
	}
}

func TestRepositoriesSkipLostInstallations(t *testing.T) {
	key := testKey(t)
	ctx := context.Background()
	for name, answer := range map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"suspended": func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/api/v3/user/installations" {
				return false
			}
			writeJSON(w, http.StatusOK, map[string]any{"total_count": 1, "installations": []any{
				map[string]any{"id": 501, "suspended_at": "2026-11-12T09:00:00Z"},
			}})
			return true
		},
		"removed during the walk": func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/api/v3/user/installations/501/repositories" {
				return false
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGitHub(t, key)
			a := g.app(t, key, nil)
			tokens := login(t, g, a, 1001)
			g.change(func() { g.intercept = answer })
			if repos, err := a.Repositories(ctx, tokens); err != nil || len(repos) != 0 {
				t.Errorf("Repositories = %v, %v; want none", repos, err)
			}
			if p, err := a.Permission(ctx, tokens, identity.Repository{Forge: "github", ID: "2001"}); err != nil || p != identity.PermissionNone {
				t.Errorf("Permission = %s, %v; want none", p, err)
			}
		})
	}
}

func TestMalformedListings(t *testing.T) {
	key := testKey(t)
	for name, body := range map[string]string{
		"no identifier": `{"repositories": [{"full_name": "acme/api", "default_branch": "main"}]}`,
		"no full name":  `{"repositories": [{"id": 2001, "default_branch": "main"}]}`,
		"a string id":   `{"repositories": [{"id": "2001", "full_name": "acme/api", "default_branch": "main"}]}`,
		"two documents": `{"repositories": []} {}`,
	} {
		g := newGitHub(t, key)
		a := g.app(t, key, nil)
		tokens := login(t, g, a, 1001)
		g.on("/api/v3/user/installations/501/repositories", func(w http.ResponseWriter, _ *http.Request) bool {
			_, _ = w.Write([]byte(body))
			return true
		})
		if _, err := a.Repositories(context.Background(), tokens); !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("%s: Repositories = %v", name, err)
		}
	}
}

// TestAnEmptyRepository: GitHub gives a repository without a commit no
// default branch. It is left out of the listing, which still succeeds, and
// its permission still resolves.
func TestAnEmptyRepository(t *testing.T) {
	key := testKey(t)
	for name, branch := range map[string]string{"null": `null`, "absent": ``} {
		t.Run(name, func(t *testing.T) {
			g := newGitHub(t, key)
			a := g.app(t, key, nil)
			tokens := login(t, g, a, 1001)
			field := ``
			if branch != "" {
				field = `, "default_branch": ` + branch
			}
			g.on("/api/v3/user/installations/501/repositories", func(w http.ResponseWriter, _ *http.Request) bool {
				_, _ = w.Write([]byte(`{"total_count": 2, "repositories": [
					{"id": 2001, "full_name": "acme/api", "default_branch": "main", "permissions": {"admin": true, "pull": true}},
					{"id": 2005, "full_name": "acme/empty"` + field + `, "permissions": {"push": true, "pull": true}}]}`))
				return true
			})
			repos, err := a.Repositories(context.Background(), tokens)
			if err != nil || len(repos) != 1 || repos[0].FullName != "acme/api" {
				t.Errorf("Repositories = %v, %v; want acme/api only", repos, err)
			}
			if p, err := a.Permission(context.Background(), tokens, identity.Repository{Forge: "github", ID: "2005"}); err != nil || p != identity.PermissionDecide {
				t.Errorf("Permission of the empty repository = %s, %v", p, err)
			}
		})
	}
}

func TestIsMember(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	g.change(func() {
		for i := range 150 {
			g.orgs[int64(4000+i)] = map[int64]string{1001: "active"}
		}
	})
	tokens := login(t, g, a, 1001)
	for org, want := range map[string]bool{"3001": true, "4149": true, "3002": false, "9999": false, "abc": false, "03001": false, "": false} {
		if got, err := a.IsMember(ctx, tokens, org); err != nil || got != want {
			t.Errorf("IsMember(%q) = %v, %v; want %v", org, got, err, want)
		}
	}
	// A pending invitation does not count, whatever GitHub filters.
	g.on("/api/v3/user/memberships/orgs", func(w http.ResponseWriter, _ *http.Request) bool {
		writeJSON(w, http.StatusOK, []any{map[string]any{"state": "pending", "organization": map[string]any{"id": 3002}}})
		return true
	})
	if in, err := a.IsMember(ctx, tokens, "3002"); err != nil || in {
		t.Errorf("IsMember on a pending invitation = %v, %v", in, err)
	}
}

// TestAnExpiredUserToken: every call refuses it with ErrUnauthorized, and
// works again once the tokens are refreshed.
func TestAnExpiredUserToken(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	tokens := login(t, g, a, 1001)
	g.clock.Advance(accessLifetime)
	repo := identity.Repository{Forge: "github", ID: "2001"}
	_, err1 := a.Repositories(ctx, tokens)
	_, err2 := a.Permission(ctx, tokens, repo)
	_, err3 := a.IsMember(ctx, tokens, "3001")
	for _, err := range []error{err1, err2, err3} {
		if !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("a call with an expired token = %v, want ErrUnauthorized", err)
		}
	}
	fresh, err := a.Refresh(ctx, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := a.Permission(ctx, fresh, repo); err != nil || p != identity.PermissionDecide {
		t.Errorf("Permission after Refresh = %s, %v", p, err)
	}
}

// TestARevokedInstallation: once the App is uninstalled, its repositories
// are gone for every person, and no installation token is minted for it.
func TestARevokedInstallation(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	tokens := login(t, g, a, 1001)
	s := scope{installation: 501, repositories: []int64{2001}, permissions: contents()}
	if _, err := a.installationToken(ctx, s); err != nil {
		t.Fatal(err)
	}
	g.change(func() { delete(g.installations, 501) })
	if repos, err := a.Repositories(ctx, tokens); err != nil || len(repos) != 0 {
		t.Errorf("Repositories = %v, %v; want none", repos, err)
	}
	if p, err := a.Permission(ctx, tokens, identity.Repository{Forge: "github", ID: "2001"}); err != nil || p != identity.PermissionNone {
		t.Errorf("Permission = %s, %v; want none", p, err)
	}
	a.forgetInstallation(501)
	if _, err := a.installationToken(ctx, s); !errors.Is(err, errInstallation) {
		t.Errorf("installationToken = %v, want an unavailable installation", err)
	}
}

// TestARateLimit: a spent quota is waited out when it resets soon, and fails
// the call with ErrUnavailable when it resets later.
func TestARateLimit(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	var waits []time.Duration
	a := g.app(t, key, &waits)
	ctx := context.Background()
	tokens := login(t, g, a, 1001)
	limited := 1
	resetIn := 3 * time.Second
	g.on("/api/v3/user/installations", func(w http.ResponseWriter, _ *http.Request) bool {
		if limited == 0 {
			return false
		}
		limited--
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(g.clock.Now().Add(resetIn).Unix(), 10))
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "API rate limit exceeded"})
		return true
	})
	if repos, err := a.Repositories(ctx, tokens); err != nil || len(repos) != 2 {
		t.Fatalf("Repositories after a rate limit = %v, %v", repos, err)
	}
	if !slices.Equal(waits, []time.Duration{resetIn + time.Second}) {
		t.Errorf("waits = %v", waits)
	}
	limited, resetIn = 1, 30*time.Minute
	if _, err := a.Repositories(ctx, tokens); !errors.Is(err, identity.ErrUnavailable) || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("Repositories with a quota spent for 30 minutes = %v", err)
	}
	if len(waits) != 1 {
		t.Errorf("waits = %v, want no wait past the budget", waits)
	}
}
