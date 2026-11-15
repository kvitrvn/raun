package github

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestCloneIsApartFromTheCache: a clone credential is minted for each call,
// for one repository with Contents read only, never taken from the tokens
// the adapter caches for its own reads, and revoking it leaves them.
func TestCloneIsApartFromTheCache(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	if _, err := a.Head(ctx, "2001", "main"); err != nil {
		t.Fatal(err)
	}
	before := len(g.recorded())
	c, err := a.MintClone(ctx, "2001")
	if err != nil {
		t.Fatal(err)
	}
	mints := g.recorded()
	if len(mints) != before+1 {
		t.Fatalf("MintClone sent %d mints, want 1", len(mints)-before)
	}
	m := mints[len(mints)-1]
	if !slices.Equal(m.Repositories, []int64{2001}) || len(m.Permissions) != 1 || m.Permissions["contents"] != "read" {
		t.Errorf("the mint asked for %+v", m)
	}
	if c.URL != g.srv.URL+"/acme/api.git" || c.Username != "x-access-token" {
		t.Errorf("credential %+v", c)
	}
	if err := a.RevokeClone(ctx, c); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	revoked := slices.Clone(g.revoked)
	g.mu.Unlock()
	if !slices.Equal(revoked, []string{c.Password.Reveal()}) {
		t.Errorf("revoked %v", revoked)
	}
	// The read token of the server stays in its cache and keeps working.
	if _, err := a.Head(ctx, "2001", "main"); err != nil {
		t.Fatalf("Head after the revocation = %v", err)
	}
	if got := len(g.recorded()); got != before+1 {
		t.Errorf("the server minted again after a revocation: %d mints", got-before-1)
	}
	if _, err := a.MintClone(ctx, "2001"); err != nil {
		t.Fatal(err)
	}
	if got := len(g.recorded()); got != before+2 {
		t.Errorf("a second clone credential came from a cache: %d mints", got-before)
	}
}

// TestCloneRefusesATokenOfOtherRepositories: a token GitHub says covers
// another repository, several, or none, is not given, and is revoked.
func TestCloneRefusesATokenOfOtherRepositories(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	for name, repos := range map[string][]map[string]any{
		"none":           nil,
		"another":        {{"id": 2002, "full_name": "acme/web"}},
		"several":        {{"id": 2001, "full_name": "acme/api"}, {"id": 2002, "full_name": "acme/web"}},
		"a strange name": {{"id": 2001, "full_name": "acme/../api"}},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGitHub(t, key)
			a := g.app(t, key, nil)
			g.change(func() {
				g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
						return false
					}
					g.installTokens["s-odd"] = ghToken{person: 1, expires: g.clock.Now().Add(3600e9)}
					writeJSON(w, http.StatusCreated, map[string]any{"token": "s-odd", "expires_at": g.clock.Now().Add(3600e9).Format("2006-01-02T15:04:05Z"), "repositories": repos})
					return true
				}
			})
			c, err := a.MintClone(ctx, "2001")
			if err == nil || c.Password.Reveal() != "" {
				t.Fatalf("MintClone = %+v, %v; want an error", c, err)
			}
			if strings.Contains(err.Error(), "s-odd") {
				t.Errorf("the error holds the token: %v", err)
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if !slices.Contains(g.revoked, "s-odd") {
				t.Error("the token was not revoked")
			}
		})
	}
}
