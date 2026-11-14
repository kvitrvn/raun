package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/forgetest"
)

var (
	_ forgetest.Statuses = (*App)(nil)
	_ forgetest.Comments = (*App)(nil)
)

const target = "https://raun.example/acme/acme/api"

func TestPublishStatusUsesTheNarrowestToken(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	if err := a.PublishStatus(ctx, "2001", forgetest.FilesHead, "success", "Raun checked", target); err != nil {
		t.Fatal(err)
	}
	mints := g.recorded()
	if len(mints) != 1 || len(mints[0].Repositories) != 1 || mints[0].Repositories[0] != 2001 ||
		len(mints[0].Permissions) != 1 || mints[0].Permissions["statuses"] != "write" {
		t.Errorf("mints = %+v", mints)
	}
	// Invalid input sends nothing.
	before := g.calls["POST /api/v3/repositories/2001/statuses/"+forgetest.FilesHead]
	for _, tc := range []struct{ repo, commit, state, description, target string }{
		{"acme/api", forgetest.FilesHead, "success", "d", target},
		{"2001", "main", "success", "d", target},
		{"2001", forgetest.FilesHead, "SUCCESS", "d", target},
		{"2001", forgetest.FilesHead, "success", strings.Repeat("x", 141), target},
		{"2001", forgetest.FilesHead, "success", "d", "ftp://raun.example/x"},
		{"2001", forgetest.FilesHead, "success", "d", "https://user@raun.example/x"},
	} {
		if err := a.PublishStatus(ctx, tc.repo, tc.commit, tc.state, tc.description, tc.target); err == nil {
			t.Errorf("PublishStatus(%+v) published", tc)
		}
	}
	g.mu.Lock()
	after := g.calls["POST /api/v3/repositories/2001/statuses/"+forgetest.FilesHead]
	g.mu.Unlock()
	if after != before {
		t.Errorf("%d statuses sent for invalid input", after-before)
	}
}

// TestWritesAreSentOnce: a write GitHub fails is not sent again, since it
// may have happened.
func TestWritesAreSentOnce(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	for _, path := range []string{"/api/v3/repositories/2001/statuses/" + forgetest.FilesHead, "/api/v3/repositories/2001/issues/42/comments"} {
		g := newGitHub(t, key)
		a := g.app(t, key, nil)
		writes := 0
		g.change(func() {
			g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == http.MethodPost && r.URL.Path == path {
					writes++
					writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Bad Gateway"})
					return true
				}
				return false
			}
		})
		var err error
		if strings.Contains(path, "statuses") {
			err = a.PublishStatus(ctx, "2001", forgetest.FilesHead, "success", "d", target)
		} else {
			_, err = a.PublishComment(ctx, "2001", 42, "x")
		}
		if !errors.Is(err, identity.ErrUnavailable) || writes != 1 {
			t.Errorf("%s: %v after %d writes, want ErrUnavailable after one", path, err, writes)
		}
	}
}

func TestCommentsOfOthersAreIgnored(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	marked := commentMarker + "\nold"
	g.change(func() {
		g.addComment(2001, 42, "alice", "User", 0, marked)                        // a person copying the marker
		g.addComment(2001, 42, "other-app[bot]", "Bot", 77, marked)               // another App
		g.addComment(2001, 42, "bob", "User", 42, marked)                         // a person through the authorization of Raun
		g.addComment(2001, 42, appSlug, "Bot", 0, marked)                         // a lookalike login
		g.addComment(2001, 42, appSlug+"[bot]", "Bot", 77, marked)                // the bot, naming another App
		g.addComment(2001, 42, appSlug+"[bot]", "Bot", 0, "text\n"+commentMarker) // the marker on another line
	})
	before := len(g.comments)
	id, err := a.PublishComment(ctx, "2001", 42, "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.comments) != before+1 || g.comments[before].body != commentMarker+"\nnew" || id != strconv.FormatInt(g.comments[before].id, 10) {
		t.Errorf("published %s; comments = %+v", id, g.comments)
	}
	for _, c := range g.comments[:before] {
		if !strings.HasSuffix(c.body, "old") && !strings.HasPrefix(c.body, "text") {
			t.Errorf("a comment of someone else changed: %+v", c)
		}
	}
}

func TestTheCommentOfRaunIsFoundPastTheFirstPage(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	var ours ghComment
	g.change(func() {
		for range perPage + 5 {
			g.addComment(2001, 42, "alice", "User", 0, "chatter")
		}
		ours = g.addComment(2001, 42, appSlug+"[bot]", "Bot", 0, commentMarker+"\nfirst")
		g.addComment(2001, 42, appSlug+"[bot]", "Bot", 0, commentMarker+"\nlater")
	})
	id, err := a.PublishComment(ctx, "2001", 42, "second")
	if err != nil || id != strconv.FormatInt(ours.id, 10) {
		t.Fatalf("PublishComment = %s, %v; want %d, the oldest of Raun", id, err, ours.id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.comments[perPage+5].body != commentMarker+"\nsecond" || g.comments[perPage+6].body != commentMarker+"\nlater" {
		t.Errorf("comments of Raun = %q, %q", g.comments[perPage+5].body, g.comments[perPage+6].body)
	}
	for _, m := range g.mints {
		if len(m.Repositories) != 1 || len(m.Permissions) != 1 || m.Permissions["pull_requests"] != "write" {
			t.Errorf("mint %+v", m)
		}
	}
}

// TestAFailedListingWritesNothing: a listing that fails, on any page, or
// that is malformed, never leads to a new comment.
func TestAFailedListingWritesNothing(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	for name, answer := range map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"a server error on page 2": func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/42/comments") && r.URL.Query().Get("page") == "2" {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "x"})
				return true
			}
			return false
		},
		"a malformed page": func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/42/comments") {
				writeJSON(w, http.StatusOK, map[string]any{"comments": []any{}})
				return true
			}
			return false
		},
		"no such pull request": func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/42/comments") {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
				return true
			}
			return false
		},
	} {
		g := newGitHub(t, key)
		a := g.app(t, key, nil)
		g.change(func() {
			for range perPage {
				g.addComment(2001, 42, "alice", "User", 0, "chatter")
			}
			g.intercept = answer
		})
		if _, err := a.PublishComment(ctx, "2001", 42, "x"); !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
		g.mu.Lock()
		if n := len(g.comments); n != perPage {
			t.Errorf("%s: %d comments, want none written", name, n-perPage)
		}
		g.mu.Unlock()
	}
}

func TestConcurrentCommentsMakeOne(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = a.PublishComment(ctx, "2001", 42, "x")
		}()
	}
	wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.comments) != 1 {
		t.Errorf("%d comments, want 1", len(g.comments))
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Errorf("ids = %v", ids)
			break
		}
	}
}

func TestEscapeText(t *testing.T) {
	for in, want := range map[string]string{
		"plain words":           "plain words",
		"a\r\nb":                "a\nb",
		"<!-- raun -->":         "&lt;\\!\\-\\- raun \\-\\-&gt;",
		"@alice":                "&#64;alice",
		"#1 *x* _y_ `z` [a](b)": `\#1 \*x\* \_y\_ \` + "`" + `z\` + "`" + ` \[a\]\(b\)`,
		"https://x.example":     `https\://x\.example`,
	} {
		if got := escapeText(in, maxComment); got != want {
			t.Errorf("escapeText(%q) = %q, want %q", in, got, want)
		}
	}
	long := escapeText(strings.Repeat("é<", 100), 50)
	if len(long) > 50 || !strings.HasSuffix(long, truncated) || !strings.HasPrefix(long, "é&lt;") {
		t.Errorf("a text cut = %q (%d bytes)", long, len(long))
	}
	if body := commentBody(strings.Repeat("x", 2*maxComment)); len(body) > maxComment {
		t.Errorf("a body of %d bytes", len(body))
	}
}

func TestTheBotOfTheAppIsLookedUpOnce(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	for range 3 {
		if _, err := a.PublishComment(ctx, "2001", 42, "x"); err != nil {
			t.Fatal(err)
		}
	}
	g.mu.Lock()
	n := g.calls["GET /api/v3/app"]
	g.mu.Unlock()
	if n != 1 {
		t.Errorf("%d lookups of the App, want 1", n)
	}
	// An answer for another App is refused.
	g2 := newGitHub(t, key)
	b := g2.app(t, key, nil)
	g2.change(func() {
		g2.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/api/v3/app" {
				writeJSON(w, http.StatusOK, map[string]any{"id": 77, "slug": "other"})
				return true
			}
			return false
		}
	})
	if _, err := b.PublishComment(ctx, "2001", 42, "x"); !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("a comment as another App = %v", err)
	}
}
