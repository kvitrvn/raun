package web

import (
	"crypto/rand"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// newTeam is a login server where Alice and Bob may sign up, both signed up.
func newTeam(t *testing.T) (*loginServer, *browser, *browser) {
	t.Helper()
	l := newLoginServer(t)
	policy := identity.Policy{Users: []identity.Entry{{Forge: "github", ID: "1001"}, {Forge: "github", ID: "1002"}}}
	l.Server.login = identity.NewLogin(l.forge, policy, l.accounts, rand.Reader, l.Server.now)
	return l, l.signUp(t, "1001", "alice"), l.signUp(t, "1002", "bob")
}

// switcher is the part of the top bar that lists workspaces.
func switcher(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `id="workspace-switcher"`)
	if i < 0 {
		return ""
	}
	end := strings.Index(body[i:], "</div>")
	return body[i : i+end]
}

func TestWorkspacesNeedAnAccount(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	if got := location(t, b.do(http.MethodGet, "/workspaces", nil), http.StatusSeeOther); got != "/login?return=%2Fworkspaces" {
		t.Errorf("GET /workspaces without an account went to %q", got)
	}
	if got := location(t, b.do(http.MethodGet, "/acme", nil), http.StatusSeeOther); got != "/login?return=%2Facme" {
		t.Errorf("GET /acme without an account went to %q", got)
	}
	// What no name can be is not found, without asking for a login.
	for _, path := range []string{"/favicon.ico", "/robots.txt", "/Acme", "/new"} {
		fresh := l.browser(t)
		if resp := fresh.do(http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound || fresh.cookie != nil {
			t.Errorf("GET %s = %d, cookie %v; want 404 without a session", path, resp.StatusCode, fresh.cookie)
		}
	}
}

func TestCreateAndOpenAWorkspace(t *testing.T) {
	l, alice, bob := newTeam(t)
	page := readBody(t, alice.do(http.MethodGet, "/workspaces", nil))
	if !strings.Contains(page, `href="/alice"`) || !strings.Contains(page, "personal") || strings.Contains(page, `href="/bob"`) {
		t.Fatalf("Alice's workspaces: %s", page)
	}
	if got := location(t, alice.post(l, "/workspaces", url.Values{"name": {"  Acme "}}), http.StatusSeeOther); got != "/acme" {
		t.Fatalf("creation went to %q", got)
	}
	for name, status := range map[string]int{
		"a--b":  http.StatusUnprocessableEntity,
		"login": http.StatusUnprocessableEntity,
		"acme":  http.StatusConflict,
		"bob":   http.StatusConflict,
	} {
		resp := alice.post(l, "/workspaces", url.Values{"name": {name}})
		if body := readBody(t, resp); resp.StatusCode != status || !strings.Contains(body, `aria-invalid="true"`) {
			t.Errorf("creating %q = %d %s", name, resp.StatusCode, body)
		}
	}
	if resp := alice.post(l, "/workspaces", url.Values{"name": {"x", "y"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a repeated field = %d", resp.StatusCode)
	}
	if resp := alice.do(http.MethodPost, "/workspaces", url.Values{"name": {"other"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("creation without the CSRF token = %d", resp.StatusCode)
	}

	resp := alice.do(http.MethodGet, "/acme", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "You are owner.") || !strings.Contains(body, `action="/acme/members"`) {
		t.Fatalf("GET /acme for its owner = %d %s", resp.StatusCode, body)
	}
	// A stranger learns nothing of it.
	if resp := bob.do(http.MethodGet, "/acme", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /acme for a stranger = %d", resp.StatusCode)
	}
	if resp := bob.post(l, "/acme/members", url.Values{"account": {"bob"}, "role": {"owner"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a stranger adding himself = %d", resp.StatusCode)
	}
	if resp := bob.do(http.MethodGet, "/alice", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET the personal workspace of another account = %d", resp.StatusCode)
	}
}

func TestOwnersAddMembers(t *testing.T) {
	l, alice, bob := newTeam(t)
	location(t, alice.post(l, "/workspaces", url.Values{"name": {"acme"}}), http.StatusSeeOther)
	if got := location(t, alice.post(l, "/acme/members", url.Values{"account": {"Bob"}, "role": {"member"}}), http.StatusSeeOther); got != "/acme" {
		t.Errorf("adding Bob went to %q", got)
	}
	for _, tc := range []struct {
		name    string
		path    string
		form    url.Values
		status  int
		message string
	}{
		{"twice", "/acme/members", url.Values{"account": {"bob"}, "role": {"owner"}}, http.StatusConflict, "a member already"},
		{"nobody", "/acme/members", url.Values{"account": {"nobody"}, "role": {"member"}}, http.StatusUnprocessableEntity, "No account has this name"},
		{"a bad role", "/acme/members", url.Values{"account": {"bob"}, "role": {"admin"}}, http.StatusUnprocessableEntity, "Choose the role"},
		{"to a personal workspace", "/alice/members", url.Values{"account": {"bob"}, "role": {"member"}}, http.StatusUnprocessableEntity, "personal workspace"},
	} {
		resp := alice.post(l, tc.path, tc.form)
		if body := readBody(t, resp); resp.StatusCode != tc.status || !strings.Contains(body, tc.message) {
			t.Errorf("adding %s = %d %s", tc.name, resp.StatusCode, body)
		}
	}
	// A path a GET refuses is refused for a POST too, before anything
	// changes.
	for _, path := range []string{"/Acme/members", "/acme%20/members", "/assets/members"} {
		if resp := alice.post(l, path, url.Values{"account": {"nobody"}, "role": {"member"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, resp.StatusCode)
		}
	}
	resp := bob.do(http.MethodGet, "/acme", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "You are member.") || strings.Contains(body, `action="/acme/members"`) {
		t.Errorf("GET /acme for a member = %d %s", resp.StatusCode, body)
	}
	if resp := bob.post(l, "/acme/members", url.Values{"account": {"alice"}, "role": {"member"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a member adding a member = %d", resp.StatusCode)
	}
}

// TestTheSwitcherShowsOwnWorkspaces: each account sees its own workspaces
// in the switcher, and no one else's.
func TestTheSwitcherShowsOwnWorkspaces(t *testing.T) {
	l, alice, bob := newTeam(t)
	location(t, alice.post(l, "/workspaces", url.Values{"name": {"acme"}}), http.StatusSeeOther)
	location(t, bob.post(l, "/workspaces", url.Values{"name": {"bobs-lab"}}), http.StatusSeeOther)
	check := func(b *browser, path string, want, not []string) {
		t.Helper()
		menu := switcher(t, readBody(t, b.do(http.MethodGet, path, nil)))
		if menu == "" {
			t.Fatalf("no switcher on %s", path)
		}
		for _, w := range want {
			if !strings.Contains(menu, `href="/`+w+`"`) {
				t.Errorf("the switcher of %s lacks %s: %s", path, w, menu)
			}
		}
		for _, w := range not {
			if strings.Contains(menu, `href="/`+w+`"`) {
				t.Errorf("the switcher of %s shows %s", path, w)
			}
		}
	}
	check(alice, "/", []string{"acme", "alice"}, []string{"bob", "bobs-lab"})
	check(bob, "/", []string{"bob", "bobs-lab"}, []string{"acme", "alice"})
	location(t, alice.post(l, "/acme/members", url.Values{"account": {"bob"}, "role": {"member"}}), http.StatusSeeOther)
	check(bob, "/workspaces", []string{"acme", "bob", "bobs-lab"}, []string{"alice"})
	// The current workspace is marked.
	menu := switcher(t, readBody(t, bob.do(http.MethodGet, "/acme", nil)))
	link := func(name string) string {
		i := strings.Index(menu, `<a href="/`+name+`"`)
		if i < 0 {
			return ""
		}
		return menu[i : i+strings.Index(menu[i:], ">")]
	}
	if !strings.Contains(link("acme"), `aria-current="page"`) || strings.Contains(link("bob"), "aria-current") {
		t.Errorf("the switcher on /acme does not mark it alone: %s", menu)
	}
	if body := readBody(t, l.browser(t).do(http.MethodGet, "/", nil)); switcher(t, body) != "" {
		t.Error("a visitor without an account sees a switcher")
	}
}
