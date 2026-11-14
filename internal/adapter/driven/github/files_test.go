package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// TestReadUsesTheNarrowestToken: a read mints a token for that repository
// alone, that reads contents only.
func TestReadUsesTheNarrowestToken(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	if _, err := a.Read(ctx, "2001", forgetest.FilesHead, ".raun.yaml", 1<<16); err != nil {
		t.Fatal(err)
	}
	mints := g.recorded()
	if len(mints) != 1 || mints[0].installation != 501 || len(mints[0].Repositories) != 1 || mints[0].Repositories[0] != 2001 ||
		len(mints[0].Permissions) != 1 || mints[0].Permissions["contents"] != "read" {
		t.Errorf("mints = %+v", mints)
	}
}

func TestReadAnswers(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		want   error
	}{
		{"a link", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "symlink", "size": 3, "target": "../x"})
		}, repository.ErrNoFile},
		{"a submodule", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "submodule", "size": 0})
		}, repository.ErrNoFile},
		{"no type", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"size": 1, "encoding": "base64", "content": "eA=="})
		}, identity.ErrUnavailable},
		{"an unknown type", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "blob", "size": 1, "encoding": "base64", "content": "eA=="})
		}, identity.ErrUnavailable},
		{"a file too large for the API to inline", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "size": 5 << 20, "encoding": "none", "content": ""})
		}, repository.ErrFileTooLarge},
		{"a size that does not match", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "size": 4, "encoding": "base64", "content": "eA=="})
		}, identity.ErrUnavailable},
		{"no size", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "encoding": "base64", "content": "eA=="})
		}, identity.ErrUnavailable},
		{"not base64", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "size": 1, "encoding": "base64", "content": "!!"})
		}, identity.ErrUnavailable},
		{"another encoding", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "size": 1, "encoding": "utf-8", "content": "x"})
		}, identity.ErrUnavailable},
		{"a server error", func(w http.ResponseWriter) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
		}, identity.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitHub(t, key)
			a := g.app(t, key, nil)
			g.change(func() {
				g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
					if strings.HasPrefix(r.URL.Path, "/api/v3/repositories/2001/contents/") {
						tc.answer(w)
						return true
					}
					return false
				}
			})
			got, err := a.Read(ctx, "2001", forgetest.FilesHead, ".raun.yaml", 1<<16)
			if got != nil || !errors.Is(err, tc.want) {
				t.Errorf("Read = %q, %v; want %v", got, err, tc.want)
			}
			if err != nil && (strings.Contains(err.Error(), "webhook-secret-sentinel") || strings.Contains(err.Error(), "client-secret-sentinel")) {
				t.Errorf("the error holds a secret: %v", err)
			}
		})
	}
}

func TestReadEscapesAndRefusesPaths(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	var seen []string
	g.change(func() {
		g.files[2001][forgetest.FilesHead]["a b/c?d.yaml"] = "x"
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(r.URL.Path, "/contents/") {
				seen = append(seen, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
			}
			return false
		}
	})
	if got, err := a.Read(ctx, "2001", forgetest.FilesHead, "a b/c?d.yaml", 10); err != nil || string(got) != "x" {
		t.Fatalf("Read of a path to escape = %q, %v", got, err)
	}
	if len(seen) != 1 || seen[0] != "/api/v3/repositories/2001/contents/a%20b/c%3Fd.yaml?ref="+forgetest.FilesHead {
		t.Errorf("requests = %v", seen)
	}
	for _, tc := range []struct{ repo, commit, path string }{
		{"2001", forgetest.FilesHead, "../x"},
		{"2001", forgetest.FilesHead, "/x"},
		{"2001", forgetest.FilesHead, "a//b"},
		{"2001", "main", ".raun.yaml"},
		{"acme/api", forgetest.FilesHead, ".raun.yaml"},
		{"-1", forgetest.FilesHead, ".raun.yaml"},
	} {
		if _, err := a.Read(ctx, tc.repo, tc.commit, tc.path, 10); !errors.Is(err, repository.ErrNoFile) {
			t.Errorf("Read(%q, %q, %q) = %v, want ErrNoFile", tc.repo, tc.commit, tc.path, err)
		}
	}
	if len(seen) != 1 {
		t.Errorf("a refused path reached GitHub: %v", seen)
	}
}

func TestHeadOfARepositoryWithoutCommit(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	g.change(func() {
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if strings.HasPrefix(r.URL.Path, "/api/v3/repositories/2001/commits/") {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "Git Repository is empty."})
				return true
			}
			return false
		}
	})
	if _, err := a.Head(ctx, "2001", "main"); !errors.Is(err, repository.ErrNoFile) {
		t.Errorf("Head of an empty repository = %v, want ErrNoFile", err)
	}
	if _, err := a.Head(ctx, "2001", ""); !errors.Is(err, repository.ErrNoFile) {
		t.Errorf("Head of no branch = %v, want ErrNoFile", err)
	}
}

// TestTheInstallationOfARepositoryIsKept: reads of one repository look its
// installation up once, until GitHub no longer serves it.
func TestTheInstallationOfARepositoryIsKept(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	for range 3 {
		if _, err := a.Head(ctx, "2001", "main"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Read(ctx, "2001", forgetest.FilesHead, ".raun.yaml", 1<<16); err != nil {
			t.Fatal(err)
		}
	}
	lookups := func() int {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.calls["GET /api/v3/repositories/2001/installation"]
	}
	if n := lookups(); n != 1 {
		t.Errorf("%d lookups of the installation, want 1", n)
	}
	// Removed: the next mint fails and forgets it, the one after looks it
	// up again.
	g.change(func() {
		delete(g.installations, 501)
		g.installations[502] = false
		for i := range g.repos {
			if g.repos[i].installation == 501 {
				g.repos[i].installation = 502
			}
		}
	})
	a.tokens.mu.Lock()
	clear(a.tokens.tokens)
	a.tokens.mu.Unlock()
	if _, err := a.Head(ctx, "2001", "main"); !errors.Is(err, identity.ErrUnavailable) {
		t.Fatalf("a read through a removed installation = %v", err)
	}
	if _, err := a.Head(ctx, "2001", "main"); err != nil {
		t.Fatalf("a read once the installation moved: %v", err)
	}
	if n := lookups(); n != 2 {
		t.Errorf("%d lookups of the installation, want 2", n)
	}
}
