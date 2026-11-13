package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Lifetimes the scripted GitHub gives, the ones of github.com.
const (
	codeLifetime    = 10 * time.Minute
	accessLifetime  = 8 * time.Hour
	refreshLifetime = 15897600 * time.Second
)

// clock is a clock shared by the scripted GitHub and the adapter.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type ghPerson struct {
	id    int64
	login string
	name  string
}

type ghRepo struct {
	id           int64
	fullName     string
	branch       string
	private      bool
	installation int64 // 0: the App is not installed on it
}

type ghToken struct {
	person  int64
	expires time.Time
}

type ghCode struct {
	person    int64
	challenge string
	redirect  string
	expires   time.Time
}

// mintRequest is a request for an installation token the scripted GitHub
// received.
type mintRequest struct {
	installation int64
	Repositories []int64           `json:"repository_ids"`
	Permissions  map[string]string `json:"permissions"`
}

// gitHub is a GitHub in memory behind a TLS server, laid out as a GitHub
// Enterprise Server: the API under /api/v3.
type gitHub struct {
	t            *testing.T
	srv          *httptest.Server
	clock        *clock
	appID        int64
	clientID     string
	clientSecret string
	callback     string
	key          *rsa.PublicKey

	mu            sync.Mutex
	people        map[int64]*ghPerson
	repos         []ghRepo
	roles         map[int64]map[int64]string // person -> repository -> role
	installations map[int64]bool             // installation -> suspended
	orgs          map[int64]map[int64]string // organization -> person -> state
	codes         map[string]ghCode
	access        map[string]ghToken
	refresh       map[string]ghToken
	installTokens map[string]ghToken // token -> installation, as person
	down          bool
	seq           int
	mints         []mintRequest
	calls         map[string]int
	// intercept, when set, answers a request before the routes do, when
	// it returns true.
	intercept func(w http.ResponseWriter, r *http.Request) bool
}

// newGitHub starts the scripted GitHub of the contract scenario: Alice
// (1001) decides on acme/api, views acme/web, cannot see acme/secret, and has
// write access on other/lib, where the App is not installed; she is an
// active member of 3001 and invited to 3002, where Bob is a member.
func newGitHub(t *testing.T, key *rsa.PrivateKey) *gitHub {
	t.Helper()
	g := &gitHub{
		t:             t,
		clock:         &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		appID:         42,
		clientID:      "Iv23liClientID",
		clientSecret:  "client-secret-sentinel",
		key:           &key.PublicKey,
		people:        map[int64]*ghPerson{1001: {1001, "alice", "Alice Martin"}, 1002: {1002, "bob", "Bob Durand"}},
		installations: map[int64]bool{501: false},
		repos: []ghRepo{
			{2001, "acme/api", "main", true, 501},
			{2002, "acme/web", "main", false, 501},
			{2003, "acme/secret", "main", true, 501},
			{2004, "other/lib", "trunk", false, 0},
		},
		roles: map[int64]map[int64]string{
			1001: {2001: "admin", 2002: "read", 2004: "write"},
			1002: {2001: "write", 2002: "write", 2003: "write", 2004: "write"},
		},
		orgs:          map[int64]map[int64]string{3001: {1001: "active"}, 3002: {1001: "pending", 1002: "active"}},
		codes:         map[string]ghCode{},
		access:        map[string]ghToken{},
		refresh:       map[string]ghToken{},
		installTokens: map[string]ghToken{},
		calls:         map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", g.token)
	mux.HandleFunc("GET /api/v3/user", g.user)
	mux.HandleFunc("GET /api/v3/user/installations", g.userInstallations)
	mux.HandleFunc("GET /api/v3/user/installations/{id}/repositories", g.installationRepositories)
	mux.HandleFunc("GET /api/v3/user/memberships/orgs", g.memberships)
	mux.HandleFunc("POST /api/v3/app/installations/{id}/access_tokens", g.mint)
	mux.HandleFunc("GET /api/v3/installation/repositories", g.asInstallation)
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.calls[r.Method+" "+r.URL.Path]++
		intercept, down := g.intercept, g.down
		g.mu.Unlock()
		if intercept != nil && intercept(w, r) {
			return
		}
		if down {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	g.callback = "https://raun.example/login/callback"
	return g
}

// app returns an adapter of g, on the clock of g, that never waits.
func (g *gitHub) app(t *testing.T, key *rsa.PrivateKey, waits *[]time.Duration) *App {
	t.Helper()
	a, err := New(Options{
		WebURL:       g.srv.URL,
		AppID:        g.appID,
		ClientID:     g.clientID,
		ClientSecret: identity.Secret(g.clientSecret),
		PrivateKey:   key,
		CallbackURL:  g.callback,
		HTTPClient:   g.srv.Client(),
		Now:          g.clock.Now,
		Wait: func(_ context.Context, d time.Duration) error {
			if waits != nil {
				*waits = append(*waits, d)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// revoke withdraws every user token of person, as a person who revokes the
// App.
func (g *gitHub) revoke(person int64) {
	g.change(func() {
		for k, tok := range g.access {
			if tok.person == person {
				delete(g.access, k)
			}
		}
		for k, tok := range g.refresh {
			if tok.person == person {
				delete(g.refresh, k)
			}
		}
	})
}

// recorded returns the mints g received.
func (g *gitHub) recorded() []mintRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.mints)
}

// change runs f on the state of g, under its lock.
func (g *gitHub) change(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

func (g *gitHub) next(kind string) string {
	g.seq++
	return fmt.Sprintf("gh%s_%d_sentinel", kind, g.seq)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// approve is person approving the App at authorizeURL.
func (g *gitHub) approve(person int64, authorizeURL string) (code, state string, err error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	if u.Path != "/login/oauth/authorize" || q.Get("client_id") != g.clientID || q.Get("redirect_uri") != g.callback ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		return "", "", fmt.Errorf("authorization URL %q is not one of the App", authorizeURL)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	code = g.next("code")
	g.codes[code] = ghCode{person: person, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), expires: g.clock.Now().Add(codeLifetime)}
	return code, q.Get("state"), nil
}

func (g *gitHub) issue(person int64) map[string]any {
	now := g.clock.Now()
	a, r := g.next("u"), g.next("r")
	g.access[a] = ghToken{person: person, expires: now.Add(accessLifetime)}
	g.refresh[r] = ghToken{person: person, expires: now.Add(refreshLifetime)}
	return map[string]any{
		"access_token": a, "expires_in": int64(accessLifetime / time.Second),
		"refresh_token": r, "refresh_token_expires_in": int64(refreshLifetime / time.Second),
		"scope": "", "token_type": "bearer",
	}
}

func (g *gitHub) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.PostForm.Get("client_id") != g.clientID || r.PostForm.Get("client_secret") != g.clientSecret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials", "error_description": "The client_id and/or client_secret passed are incorrect."})
		return
	}
	now := g.clock.Now()
	if r.PostForm.Get("grant_type") == "refresh_token" {
		rt := r.PostForm.Get("refresh_token")
		tok, ok := g.refresh[rt]
		delete(g.refresh, rt)
		if !ok || !now.Before(tok.expires) {
			writeJSON(w, http.StatusOK, map[string]string{"error": "bad_refresh_token", "error_description": "The refresh token passed is incorrect or expired."})
			return
		}
		writeJSON(w, http.StatusOK, g.issue(tok.person))
		return
	}
	code := r.PostForm.Get("code")
	c, ok := g.codes[code]
	delete(g.codes, code)
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || !now.Before(c.expires) || r.PostForm.Get("redirect_uri") != c.redirect ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."})
		return
	}
	writeJSON(w, http.StatusOK, g.issue(c.person))
}

// person returns the person of the user token of r; the caller holds the
// lock. A refused token is answered with a 401.
func (g *gitHub) person(w http.ResponseWriter, r *http.Request) (int64, bool) {
	tok, ok := g.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok || !g.clock.Now().Before(tok.expires) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return 0, false
	}
	return tok.person, true
}

func (g *gitHub) user(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.person(w, r)
	if !ok {
		return
	}
	p := g.people[id]
	writeJSON(w, http.StatusOK, map[string]any{"id": p.id, "login": p.login, "name": p.name, "type": "User"})
}

// page cuts items by the per_page and page parameters of r.
func page[T any](r *http.Request, items []T) []T {
	per, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil || per <= 0 {
		per = 30
	}
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n <= 0 {
		n = 1
	}
	start := min((n-1)*per, len(items))
	return items[start:min(start+per, len(items))]
}

// reachable are the repositories person reaches through installation, or
// through any installation when it is 0; the caller holds the lock.
func (g *gitHub) reachable(person, installation int64) []ghRepo {
	var out []ghRepo
	for _, repo := range g.repos {
		suspended, installed := g.installations[repo.installation]
		if repo.installation == 0 || !installed || suspended || g.roles[person][repo.id] == "" {
			continue
		}
		if installation == 0 || repo.installation == installation {
			out = append(out, repo)
		}
	}
	return out
}

func (g *gitHub) userInstallations(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	person, ok := g.person(w, r)
	if !ok {
		return
	}
	var ids []int64
	for _, repo := range g.reachable(person, 0) {
		if !slices.Contains(ids, repo.installation) {
			ids = append(ids, repo.installation)
		}
	}
	slices.Sort(ids)
	out := []map[string]any{}
	for _, id := range page(r, ids) {
		out = append(out, map[string]any{"id": id, "app_id": g.appID, "suspended_at": nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(ids), "installations": out})
}

// flags are the rights GitHub lists for a role.
func flags(role string) map[string]bool {
	order := []string{"pull", "triage", "push", "maintain", "admin"}
	level := map[string]int{"read": 0, "triage": 1, "write": 2, "maintain": 3, "admin": 4}[role]
	out := map[string]bool{}
	for i, f := range order {
		out[f] = i <= level
	}
	return out
}

func (g *gitHub) installationRepositories(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	person, ok := g.person(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, ok := g.installations[id]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	repos := g.reachable(person, id)
	out := []map[string]any{}
	for _, repo := range page(r, repos) {
		out = append(out, map[string]any{
			"id": repo.id, "full_name": repo.fullName, "default_branch": repo.branch, "private": repo.private,
			"permissions": flags(g.roles[person][repo.id]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": out})
}

func (g *gitHub) memberships(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	person, ok := g.person(w, r)
	if !ok {
		return
	}
	var orgs []int64
	for org, members := range g.orgs {
		if state := members[person]; state != "" && (r.URL.Query().Get("state") == "" || r.URL.Query().Get("state") == state) {
			orgs = append(orgs, org)
		}
	}
	slices.Sort(orgs)
	out := []map[string]any{}
	for _, org := range page(r, orgs) {
		out = append(out, map[string]any{"state": g.orgs[org][person], "role": "member", "organization": map[string]any{"id": org, "login": fmt.Sprintf("org%d", org)}})
	}
	writeJSON(w, http.StatusOK, out)
}

// checkJWT verifies the JWT of the App on r: RS256, signed by the key of the
// App, issued by it and not expired.
func (g *gitHub) checkJWT(r *http.Request) error {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || string(header) != `{"alg":"RS256","typ":"JWT"}` {
		return fmt.Errorf("header %q", header)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(g.key, crypto.SHA256, sum[:], sig); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return err
	}
	now := g.clock.Now().Unix()
	if claims.Issuer != strconv.FormatInt(g.appID, 10) || claims.IssuedAt > now || claims.ExpiresAt <= now || claims.ExpiresAt-now > 600 {
		return fmt.Errorf("claims %+v at %d", claims, now)
	}
	return nil
}

func (g *gitHub) mint(w http.ResponseWriter, r *http.Request) {
	if err := g.checkJWT(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded: " + err.Error()})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var req mintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	req.installation = id
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mints = append(g.mints, req)
	suspended, ok := g.installations[id]
	switch {
	case !ok:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	case suspended:
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "This installation has been suspended"})
		return
	}
	token := g.next("s")
	expires := g.clock.Now().Add(time.Hour)
	g.installTokens[token] = ghToken{person: id, expires: expires}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expires.Format(time.RFC3339), "permissions": req.Permissions})
}

func (g *gitHub) asInstallation(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tok, ok := g.installTokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok || !g.clock.Now().Before(tok.expires) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": 0, "repositories": []any{}})
}
