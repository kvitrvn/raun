package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// stubRepositories answers as told and records what it was asked; the
// rules themselves are tested in the repository context and through the
// server in cmd/raun.
type stubRepositories struct {
	mu  sync.Mutex
	err error
	// change is the error of Activate and Deactivate, after err.
	change  error
	entries []repository.Entry
	view    repository.View
	missing []repository.Capability
	asked   []string
}

func (s *stubRepositories) ask(call string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, call)
	return err
}

func (s *stubRepositories) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *stubRepositories) Missing() []repository.Capability { return s.missing }

func (s *stubRepositories) List(_ context.Context, _, query string) ([]repository.Entry, error) {
	return s.entries, s.ask("list "+query, s.err)
}

func (s *stubRepositories) Activate(_ context.Context, _, workspace, id string) (repository.Repository, error) {
	r := repository.Repository{Workspace: repository.Workspace{Name: workspace}, ID: id, FullName: "acme/api"}
	return r, s.ask("activate "+workspace+" "+id, errors.Join(s.err, s.change))
}

func (s *stubRepositories) Deactivate(_ context.Context, _, workspace, fullName string) (repository.Repository, error) {
	r := repository.Repository{Workspace: repository.Workspace{Name: workspace}, FullName: fullName}
	return r, s.ask("deactivate "+workspace+" "+fullName, errors.Join(s.err, s.change))
}

func (s *stubRepositories) Open(_ context.Context, _, workspace, fullName string) (repository.View, error) {
	return s.view, s.ask("open "+workspace+" "+fullName, s.err)
}

func (s *stubRepositories) RefreshConfiguration(_ context.Context, id string) error {
	return s.ask("refresh "+id, nil)
}

func (s *stubRepositories) InWorkspace(_ context.Context, _, workspace string) ([]repository.Repository, error) {
	return nil, s.ask("in "+workspace, s.err)
}

func TestRepositoryPagesNeedAnAccount(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	if got := location(t, b.do(http.MethodGet, "/acme/acme/api", nil), http.StatusSeeOther); got != "/login?return=%2Facme%2Facme%2Fapi" {
		t.Errorf("GET /acme/acme/api without an account went to %q", got)
	}
	// What no workspace can be is not found, without a session.
	for _, path := range []string{"/Acme/acme/api", "/login/acme/api", "/-/acme/api"} {
		fresh := l.browser(t)
		if resp := fresh.do(http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound || fresh.cookie != nil {
			t.Errorf("GET %s = %d, cookie %v; want 404 without a session", path, resp.StatusCode, fresh.cookie)
		}
	}
	// A visitor without an account sees the empty home page.
	if body := readBody(t, l.browser(t).do(http.MethodGet, "/", nil)); !strings.Contains(body, "No repository yet") {
		t.Errorf("the home page of a visitor: %s", body)
	}
	if got := l.repos.calls(); len(got) != 0 {
		t.Errorf("the service was asked %v", got)
	}
}

func TestRepositoryRequestsReachTheService(t *testing.T) {
	l, alice, _ := newTeam(t)
	l.repos.entries = []repository.Entry{{Listed: repository.Listed{ID: "2001", FullName: "acme/api"}}}
	l.repos.view = repository.View{Repository: repository.Repository{ID: "2001", FullName: "acme/api", Workspace: repository.Workspace{Name: "alice"}, Active: true, Reachable: true}, Permission: repository.PermissionDecide, Member: true}
	body := readBody(t, alice.do(http.MethodGet, "/?q=api", nil))
	if !strings.Contains(body, `href="https://github.example/acme/api"`) || !strings.Contains(body, `<option value="alice">`) || !strings.Contains(body, `name="repository" value="2001"`) {
		t.Errorf("the Repositories screen: %s", body)
	}
	if got := location(t, alice.post(l, "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}), http.StatusSeeOther); got != "/alice/acme/api" {
		t.Errorf("activation went to %q", got)
	}
	page := readBody(t, alice.do(http.MethodGet, "/alice/acme/api", nil))
	if !strings.Contains(page, `action="/alice/acme/api/deactivate"`) {
		t.Errorf("the page of the repository: %s", page)
	}
	// The switcher marks the workspace of the repository current.
	if sw := switcher(t, page); !regexp.MustCompile(`<a href="/alice"[^>]*aria-current="page"`).MatchString(sw) {
		t.Errorf("the switcher on the page of a repository: %s", sw)
	}
	if got := location(t, alice.post(l, "/alice/acme/api/deactivate", url.Values{}), http.StatusSeeOther); got != "/alice/acme/api" {
		t.Errorf("deactivation went to %q", got)
	}
	readBody(t, alice.do(http.MethodGet, "/alice", nil))
	want := []string{"list api", "activate alice 2001", "refresh 2001", "open alice acme/api", "deactivate alice acme/api", "in alice"}
	if got := l.repos.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", got, want)
	}
	// A repeated field is refused before the service is asked.
	if resp := alice.post(l, "/repositories", url.Values{"workspace": {"alice", "bob"}, "repository": {"2001"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a repeated field = %d, want 400", resp.StatusCode)
	}
}

func TestRepositoryErrorsOfTheForge(t *testing.T) {
	l, alice, _ := newTeam(t)
	for _, tc := range []struct {
		err    error
		status int
		text   string
	}{
		{repository.ErrUnavailable, http.StatusServiceUnavailable, "The forge does not answer"},
		{repository.ErrRefused, http.StatusForbidden, "The forge refuses your session"},
		{errors.New("disk"), http.StatusInternalServerError, "internal error"},
	} {
		l.repos.err = tc.err
		for _, req := range []struct{ method, path string }{{http.MethodGet, "/"}, {http.MethodGet, "/alice/acme/api"}, {http.MethodPost, "/repositories"}} {
			var resp *http.Response
			if req.method == http.MethodPost {
				resp = alice.post(l, req.path, url.Values{"workspace": {"alice"}, "repository": {"2001"}})
			} else {
				resp = alice.do(req.method, req.path, nil)
			}
			if body := readBody(t, resp); resp.StatusCode != tc.status || !strings.Contains(body, tc.text) {
				t.Errorf("%s %s with %v = %d %s", req.method, req.path, tc.err, resp.StatusCode, body)
			}
		}
	}
	// The page of a workspace still shows, and says why its repositories
	// do not.
	l.repos.err = repository.ErrUnavailable
	if resp := alice.do(http.MethodGet, "/alice", nil); resp.StatusCode != http.StatusOK || !strings.Contains(readBody(t, resp), "cannot be shown right now") {
		t.Errorf("the workspace page with the forge down = %d", resp.StatusCode)
	}
}

func TestRepositoryRefusals(t *testing.T) {
	l, alice, _ := newTeam(t)
	for _, tc := range []struct {
		err    error
		status int
		text   string
	}{
		{repository.ErrNotMember, http.StatusUnprocessableEntity, "Choose a workspace you belong to."},
		{repository.ErrNotFound, http.StatusUnprocessableEntity, "The forge lists no such repository for you."},
		{repository.ErrNotDecider, http.StatusForbidden, "write access"},
		{repository.ErrElsewhere, http.StatusConflict, "belongs to another workspace"},
		{&repository.MissingError{Missing: []repository.Capability{repository.CapabilityFiles}}, http.StatusConflict, "lacks what activation needs: files."},
	} {
		l.repos.change = tc.err
		resp := alice.post(l, "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}})
		if body := readBody(t, resp); resp.StatusCode != tc.status || !strings.Contains(body, tc.text) {
			t.Errorf("activation with %v = %d %s", tc.err, resp.StatusCode, body)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{repository.ErrNotFound, http.StatusNotFound},
		{repository.ErrNotMember, http.StatusForbidden},
		{repository.ErrNotDecider, http.StatusForbidden},
	} {
		l.repos.change = tc.err
		if resp := alice.post(l, "/alice/acme/api/deactivate", url.Values{}); resp.StatusCode != tc.status {
			t.Errorf("deactivation with %v = %d, want %d", tc.err, resp.StatusCode, tc.status)
		}
	}
	l.repos.change, l.repos.err = nil, repository.ErrNotFound
	if resp := alice.do(http.MethodGet, "/alice/acme/api", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a repository not found = %d", resp.StatusCode)
	}
}
