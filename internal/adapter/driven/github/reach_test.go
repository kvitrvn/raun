package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

func TestReached(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	g.change(func() {
		g.installations[502] = true // suspended
		g.installations[503] = false
		g.repos = append(g.repos, ghRepo{3001, "beta/one", "main", false, 502}, ghRepo{3002, "beta/two", "main", false, 503})
	})
	got, err := a.Reached(ctx)
	if err != nil || strings.Join(got, " ") != "2001 2002 2003 3002" {
		t.Fatalf("Reached = %v, %v; want the repositories of 501 and 503", got, err)
	}
	// A token minted asks for metadata only, for every repository of its
	// installation.
	for _, m := range g.recorded() {
		if len(m.Repositories) != 0 || len(m.Permissions) != 1 || m.Permissions["metadata"] != "read" {
			t.Errorf("mint %+v", m)
		}
	}
	// Removed between the listing and the mint: it reaches nothing.
	g.change(func() {
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/api/v3/app/installations/503/access_tokens" {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
				return true
			}
			return false
		}
	})
	a = g.app(t, key, nil)
	if got, err := a.Reached(ctx); err != nil || strings.Join(got, " ") != "2001 2002 2003" {
		t.Errorf("Reached with an installation removed = %v, %v", got, err)
	}
}

func TestReachedNeverGivesAPart(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	for _, path := range []string{"/api/v3/app/installations", "/api/v3/installation/repositories"} {
		g := newGitHub(t, key)
		a := g.app(t, key, nil)
		g.change(func() {
			g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == path {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Server Error"})
					return true
				}
				return false
			}
		})
		if got, err := a.Reached(ctx); got != nil || !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Reached with %s failing = %v, %v; want nothing and ErrUnavailable", path, got, err)
		}
	}
	// A JWT GitHub refuses is an error, never an empty answer.
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	g.change(func() { g.appID = 43 })
	if got, err := a.Reached(ctx); got != nil || !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("Reached with a refused JWT = %v, %v", got, err)
	}
}
