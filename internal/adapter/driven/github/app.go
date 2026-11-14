package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// minKeyBits is the smallest RSA key accepted; GitHub generates 2048 bits.
const minKeyBits = 2048

// ParsePrivateKey reads the private key of a GitHub App: a PEM RSA key in
// PKCS #1, as GitHub gives it, or PKCS #8. Its errors never hold the key.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a valid PKCS #1 RSA private key")
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a valid PKCS #8 private key")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("not an RSA private key")
		}
		key = rk
	default:
		return nil, fmt.Errorf("PEM block %q is not an RSA private key", block.Type)
	}
	if key.N.BitLen() < minKeyBits {
		return nil, fmt.Errorf("RSA key of %d bits, want %d at least", key.N.BitLen(), minKeyBits)
	}
	return key, nil
}

// JWT lifetimes: GitHub refuses an expiry more than 10 minutes ahead, and
// the issue time is set in the past against a clock drift.
const (
	jwtDrift    = time.Minute
	jwtLifetime = 9 * time.Minute
)

// jwt returns a JSON Web Token of the App, signed with RS256.
func (a *App) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{now.Add(-jwtDrift).Unix(), now.Add(jwtLifetime).Unix(), strconv.FormatInt(a.appID, 10)})
	if err != nil {
		return "", fmt.Errorf("encode the claims of the JWT: %w", err)
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign the JWT: %w", err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// scope is what an installation token gives: one installation, the
// repositories of the call and the permissions it needs, never more.
type scope struct {
	installation int64
	repositories []int64
	// permissions maps a permission of the App to read or write, such as
	// contents: read.
	permissions map[string]string
}

// key is the identity of s in the cache: the installation, the sorted
// repositories and the sorted permissions.
func (s scope) key() string {
	repos := slices.Clone(s.repositories)
	slices.Sort(repos)
	var b strings.Builder
	b.WriteString(strconv.FormatInt(s.installation, 10))
	b.WriteString("|")
	for i, r := range repos {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strconv.FormatInt(r, 10))
	}
	b.WriteString("|")
	for i, p := range slices.Sorted(maps.Keys(s.permissions)) {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(p + "=" + s.permissions[p])
	}
	return b.String()
}

// tokenMargin is how long before its expiry an installation token stops
// being used.
const tokenMargin = 5 * time.Minute

// tokenCache holds installation tokens in memory, per scope.
type tokenCache struct {
	mu       sync.Mutex
	tokens   map[string]installationToken
	inflight map[string]*mint
}

type installationToken struct {
	installation int64
	token        identity.Secret
	expires      time.Time
}

// mint is a request for a token in progress, which callers of the same scope
// wait on instead of minting another.
type mint struct {
	done  chan struct{}
	token identity.Secret
	err   error
}

func newTokenCache() *tokenCache {
	return &tokenCache{tokens: map[string]installationToken{}, inflight: map[string]*mint{}}
}

// errInstallation marks an installation GitHub no longer serves: removed,
// suspended, or no longer covering the repositories of the scope.
var errInstallation = errors.New("installation unavailable")

// installationToken returns a token of the scope s, from the cache while it
// has more than five minutes left, or minted. Callers of one scope share one
// mint; a failed mint is never cached.
func (a *App) installationToken(ctx context.Context, s scope) (identity.Secret, error) {
	key := s.key()
	for {
		a.tokens.mu.Lock()
		if t, ok := a.tokens.tokens[key]; ok {
			if a.now().Add(tokenMargin).Before(t.expires) {
				a.tokens.mu.Unlock()
				return t.token, nil
			}
			// Past its margin, a token is dropped rather than kept.
			delete(a.tokens.tokens, key)
		}
		if m, ok := a.tokens.inflight[key]; ok {
			a.tokens.mu.Unlock()
			select {
			case <-m.done:
			case <-ctx.Done():
				return "", fmt.Errorf("github: installation token: %w: %w", identity.ErrUnavailable, ctx.Err())
			}
			// A mint given up by its own caller is tried again.
			if m.err != nil && (errors.Is(m.err, context.Canceled) || errors.Is(m.err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return m.token, m.err
		}
		m := &mint{done: make(chan struct{})}
		a.tokens.inflight[key] = m
		a.tokens.mu.Unlock()

		m.token, m.err = a.mintToken(ctx, s, key)
		a.tokens.mu.Lock()
		delete(a.tokens.inflight, key)
		a.tokens.mu.Unlock()
		close(m.done)
		return m.token, m.err
	}
}

// mintToken asks GitHub for a token of s and caches it under key.
func (a *App) mintToken(ctx context.Context, s scope, key string) (identity.Secret, error) {
	jwt, err := a.jwt()
	if err != nil {
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	path := "/app/installations/" + strconv.FormatInt(s.installation, 10) + "/access_tokens"
	req := request{
		method: http.MethodPost,
		url:    a.api + path,
		path:   path,
		auth:   "Bearer " + jwt,
		body: struct {
			Repositories []int64           `json:"repository_ids"`
			Permissions  map[string]string `json:"permissions"`
		}{s.repositories, s.permissions},
		// A mint consumes nothing: sending it again gives another token.
		retry: true,
	}
	r, err := a.do(ctx, req)
	if err != nil {
		return "", err
	}
	switch r.status {
	case http.StatusCreated:
	case http.StatusNotFound, http.StatusForbidden, http.StatusUnprocessableEntity:
		a.forgetInstallation(s.installation)
		return "", fmt.Errorf("github: %s %s: %w: %w: status %d", req.method, req.path, identity.ErrUnavailable, errInstallation, r.status)
	case http.StatusUnauthorized:
		return "", fail(req, identity.ErrUnavailable, "GitHub refuses the JWT of the App: check RAUN_GITHUB_APP_ID, the private key and the clock")
	default:
		return "", status(req, r)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := decode(req, r, &out); err != nil {
		return "", err
	}
	if out.Token == "" || out.ExpiresAt.IsZero() {
		return "", fail(req, identity.ErrUnavailable, "malformed response")
	}
	t := installationToken{installation: s.installation, token: identity.Secret(out.Token), expires: out.ExpiresAt}
	a.tokens.mu.Lock()
	// Tokens of other scopes past their margin are dropped too, so the
	// cache holds no token that would not be used.
	now := a.now()
	for k, old := range a.tokens.tokens {
		if !now.Add(tokenMargin).Before(old.expires) {
			delete(a.tokens.tokens, k)
		}
	}
	a.tokens.tokens[key] = t
	a.tokens.mu.Unlock()
	return t.token, nil
}

// forgetInstallation drops every token of installation, and the
// repositories found in it.
func (a *App) forgetInstallation(installation int64) {
	a.tokens.mu.Lock()
	for k, t := range a.tokens.tokens {
		if t.installation == installation {
			delete(a.tokens.tokens, k)
		}
	}
	a.tokens.mu.Unlock()
	a.installs.forget(installation)
}

// installCache keeps the installation of the App that reaches each
// repository, as GitHub last said; an installation GitHub no longer serves
// is forgotten with its tokens.
type installCache struct {
	mu     sync.Mutex
	byRepo map[int64]int64
}

func (c *installCache) get(repo int64) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.byRepo[repo]
	return id, ok
}

func (c *installCache) put(repo, installation int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRepo[repo] = installation
}

func (c *installCache) forget(installation int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for repo, id := range c.byRepo {
		if id == installation {
			delete(c.byRepo, repo)
		}
	}
}

// forgetToken drops token, a token of s GitHub refused, unless the cache
// already holds another one: a late refusal of an old token never drops the
// token minted to replace it.
func (a *App) forgetToken(s scope, token identity.Secret) {
	a.tokens.mu.Lock()
	defer a.tokens.mu.Unlock()
	key := s.key()
	if t, ok := a.tokens.tokens[key]; ok && t.token == token {
		delete(a.tokens.tokens, key)
	}
}

// asInstallation sends the request build makes with a token of s. A token
// GitHub refuses is dropped and minted again, once; the scope is never
// widened.
func (a *App) asInstallation(ctx context.Context, s scope, build func(auth string) request) (*response, error) {
	for attempt := 1; ; attempt++ {
		token, err := a.installationToken(ctx, s)
		if err != nil {
			return nil, err
		}
		req := build(bearer(token))
		r, err := a.do(ctx, req)
		if err != nil {
			return nil, err
		}
		if r.status != http.StatusUnauthorized {
			return r, nil
		}
		a.forgetToken(s, token)
		if attempt == 2 {
			return nil, fail(req, identity.ErrUnavailable, "GitHub refuses a new installation token")
		}
	}
}
