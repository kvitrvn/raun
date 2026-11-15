package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
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
	"github.com/kvitrvn/raun/internal/forgetest"
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
	tokenScopes   map[string]mintRequest
	heads         map[int64]map[string]string                    // repository -> branch -> commit
	files         map[int64]map[string]map[string]string         // repository -> commit -> path -> content
	statuses      map[int64]map[string]forgetest.PublishedStatus // repository -> commit -> status of raun
	comments      []ghComment
	down          bool
	seq           int
	mints         []mintRequest
	revoked       []string // installation tokens revoked, in order
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
		statuses:      map[int64]map[string]forgetest.PublishedStatus{},
		tokenScopes:   map[string]mintRequest{},
		heads: map[int64]map[string]string{
			2001: {"main": forgetest.FilesHead},
			2002: {"main": strings.Repeat("e", 40)},
		},
		files: map[int64]map[string]map[string]string{
			2001: {forgetest.FilesHead: {".raun.yaml": "version: 1\n", "big.txt": strings.Repeat("x", 100), "empty.txt": "", "docs/index.md": "# Docs"}},
		},
		calls: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", g.token)
	mux.HandleFunc("GET /api/v3/user", g.user)
	mux.HandleFunc("GET /api/v3/user/installations", g.userInstallations)
	mux.HandleFunc("GET /api/v3/user/installations/{id}/repositories", g.installationRepositories)
	mux.HandleFunc("GET /api/v3/user/memberships/orgs", g.memberships)
	mux.HandleFunc("GET /api/v3/app/installations", g.appInstallations)
	mux.HandleFunc("POST /api/v3/app/installations/{id}/access_tokens", g.mint)
	mux.HandleFunc("GET /api/v3/installation/repositories", g.asInstallation)
	mux.HandleFunc("DELETE /api/v3/installation/token", g.revokeInstallationToken)
	mux.HandleFunc("GET /api/v3/repositories/{id}/installation", g.repositoryInstallation)
	mux.HandleFunc("GET /api/v3/repositories/{id}/commits/{branch}", g.commit)
	mux.HandleFunc("GET /api/v3/repositories/{id}/contents/{path...}", g.contents)
	mux.HandleFunc("GET /api/v3/app", g.appInfo)
	mux.HandleFunc("POST /api/v3/repositories/{id}/statuses/{sha}", g.postStatus)
	mux.HandleFunc("GET /api/v3/repositories/{id}/issues/{number}/comments", g.listComments)
	mux.HandleFunc("POST /api/v3/repositories/{id}/issues/{number}/comments", g.postComment)
	mux.HandleFunc("PATCH /api/v3/repositories/{id}/issues/comments/{comment}", g.patchComment)
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
		WebURL:        g.srv.URL,
		AppID:         g.appID,
		ClientID:      g.clientID,
		ClientSecret:  identity.Secret(g.clientSecret),
		PrivateKey:    key,
		WebhookSecret: identity.Secret(webhookSecret),
		CallbackURL:   g.callback,
		HTTPClient:    g.srv.Client(),
		Now:           g.clock.Now,
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
	// A token covers repositories of its installation only, and GitHub
	// lists those it was asked for.
	var covered []map[string]any
	for _, rid := range req.Repositories {
		i := slices.IndexFunc(g.repos, func(repo ghRepo) bool { return repo.id == rid && repo.installation == id })
		if i < 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "There is at least one repository that does not exist or is not accessible to the parent installation."})
			return
		}
		covered = append(covered, map[string]any{"id": rid, "full_name": g.repos[i].fullName, "private": g.repos[i].private})
	}
	token := g.next("s")
	expires := g.clock.Now().Add(time.Hour)
	g.installTokens[token] = ghToken{person: id, expires: expires}
	g.tokenScopes[token] = req
	body := map[string]any{"token": token, "expires_at": expires.Format(time.RFC3339), "permissions": req.Permissions}
	if len(req.Repositories) > 0 {
		body["repository_selection"] = "selected"
		body["repositories"] = covered
	}
	writeJSON(w, http.StatusCreated, body)
}

// revokeInstallationToken revokes the installation token the request
// carries.
func (g *gitHub) revokeInstallationToken(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, ok := g.installTokens[token]
	if !ok || !g.clock.Now().Before(tok.expires) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	delete(g.installTokens, token)
	g.revoked = append(g.revoked, token)
	w.WriteHeader(http.StatusNoContent)
}

// cloneReads reports whether token is a live installation token of the
// repository id alone that reads its contents.
func (g *gitHub) cloneReads(token string, id int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	tok, ok := g.installTokens[token]
	s := g.tokenScopes[token]
	return ok && g.clock.Now().Before(tok.expires) && len(s.Repositories) == 1 && s.Repositories[0] == id &&
		s.Permissions["contents"] == "read" && len(s.Permissions) == 1
}

func (g *gitHub) asInstallation(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tok, ok := g.installTokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok || !g.clock.Now().Before(tok.expires) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	var repos []map[string]any
	for _, repo := range g.repos {
		// The token of an installation records the installation in
		// person.
		if repo.installation == tok.person {
			repos = append(repos, map[string]any{"id": repo.id, "full_name": repo.fullName})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": append([]map[string]any{}, page(r, repos)...)})
}

// webhookSecret is the secret of the webhook of the scripted App.
const webhookSecret = "webhook-secret-sentinel"

// appInstallations lists the installations of the App, for its JWT.
func (g *gitHub) appInstallations(w http.ResponseWriter, r *http.Request) {
	if err := g.checkJWT(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded: " + err.Error()})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ids := slices.Sorted(maps.Keys(g.installations))
	out := []map[string]any{}
	for _, id := range page(r, ids) {
		var suspended any
		if g.installations[id] {
			suspended = "2026-11-01T10:00:00Z"
		}
		out = append(out, map[string]any{"id": id, "app_id": g.appID, "suspended_at": suspended})
	}
	writeJSON(w, http.StatusOK, out)
}

// repositoryInstallation is the installation of the App on a repository,
// for its JWT.
func (g *gitHub) repositoryInstallation(w http.ResponseWriter, r *http.Request) {
	if err := g.checkJWT(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded: " + err.Error()})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, repo := range g.repos {
		if _, ok := g.installations[repo.installation]; repo.id == id && repo.installation != 0 && ok {
			writeJSON(w, http.StatusOK, map[string]any{"id": repo.installation, "app_id": g.appID})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// reader checks that r carries a live installation token that may read the
// contents of the repository of its path; the caller holds the lock.
func (g *gitHub) reader(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return g.scoped(w, r, "contents", "read")
}

// scoped checks that r carries a live installation token of the repository
// of its path alone, with permission at level; the caller holds the lock.
func (g *gitHub) scoped(w http.ResponseWriter, r *http.Request, permission, level string) (int64, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, ok := g.installTokens[token]
	if !ok || !g.clock.Now().Before(tok.expires) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return 0, false
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s := g.tokenScopes[token]
	for _, repo := range g.repos {
		if repo.id == id && repo.installation == tok.person && len(s.Repositories) == 1 && s.Repositories[0] == id &&
			(s.Permissions[permission] == level || s.Permissions[permission] == "write") {
			return id, true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	return 0, false
}

func (g *gitHub) commit(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.reader(w, r)
	if !ok {
		return
	}
	sha, ok := g.heads[id][r.PathValue("branch")]
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "No commit found for SHA: " + r.PathValue("branch")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "node_id": "C_x", "commit": map[string]any{"message": "m"}})
}

func (g *gitHub) contents(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.reader(w, r)
	if !ok {
		return
	}
	commit, path := r.URL.Query().Get("ref"), r.PathValue("path")
	files := g.files[id][commit]
	if content, ok := files[path]; ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "file", "encoding": "base64", "size": len(content), "name": path, "path": path,
			"content":      base64.StdEncoding.EncodeToString([]byte(content)) + "\n",
			"download_url": "https://raw.example/never",
		})
		return
	}
	var entries []map[string]any
	for p := range files {
		if strings.HasPrefix(p, path+"/") {
			entries = append(entries, map[string]any{"type": "file", "path": p})
		}
	}
	if len(entries) > 0 {
		writeJSON(w, http.StatusOK, entries)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// appSlug is the slug of the scripted App: its bot is appSlug[bot].
const appSlug = "raun-test"

// ghComment is a comment of a pull request of the scripted GitHub.
type ghComment struct {
	id         int64
	repository int64
	number     int64
	login      string
	kind       string // User or Bot
	app        int64  // the App it was written through, 0 for none
	body       string
}

func (c ghComment) json() map[string]any {
	out := map[string]any{"id": c.id, "body": c.body, "user": map[string]any{"login": c.login, "type": c.kind, "id": 99}}
	if c.app != 0 {
		out["performed_via_github_app"] = map[string]any{"id": c.app, "slug": "x"}
	} else {
		out["performed_via_github_app"] = nil
	}
	return out
}

func (g *gitHub) appInfo(w http.ResponseWriter, r *http.Request) {
	if err := g.checkJWT(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": g.appID, "slug": appSlug, "name": "Raun test"})
}

func (g *gitHub) postStatus(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.scoped(w, r, "statuses", "write")
	if !ok {
		return
	}
	var in struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		Target      string `json:"target_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		return
	}
	if g.statuses[id] == nil {
		g.statuses[id] = map[string]forgetest.PublishedStatus{}
	}
	g.statuses[id][r.PathValue("sha")] = forgetest.PublishedStatus{Context: in.Context, State: in.State, Description: in.Description, Target: in.Target}
	writeJSON(w, http.StatusCreated, map[string]any{"id": 1, "state": in.State, "context": in.Context})
}

func (g *gitHub) listComments(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.scoped(w, r, "pull_requests", "read")
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	var out []map[string]any
	for _, c := range g.comments {
		if c.repository == id && c.number == number {
			out = append(out, c.json())
		}
	}
	writeJSON(w, http.StatusOK, append([]map[string]any{}, page(r, out)...))
}

func (g *gitHub) postComment(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.scoped(w, r, "pull_requests", "write")
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		return
	}
	c := g.addComment(id, number, appSlug+"[bot]", "Bot", 0, in.Body)
	writeJSON(w, http.StatusCreated, c.json())
}

func (g *gitHub) patchComment(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.scoped(w, r, "pull_requests", "write")
	if !ok {
		return
	}
	cid, _ := strconv.ParseInt(r.PathValue("comment"), 10, 64)
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		return
	}
	for i, c := range g.comments {
		if c.id == cid && c.repository == id {
			if c.login != appSlug+"[bot]" {
				writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
				return
			}
			g.comments[i].body = in.Body
			writeJSON(w, http.StatusOK, g.comments[i].json())
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// addComment adds a comment; the caller holds the lock.
func (g *gitHub) addComment(repo, number int64, login, kind string, app int64, body string) ghComment {
	g.seq++
	c := ghComment{id: int64(7000 + g.seq), repository: repo, number: number, login: login, kind: kind, app: app, body: body}
	g.comments = append(g.comments, c)
	return c
}
