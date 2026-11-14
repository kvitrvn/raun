package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// allCapabilities is a forge that lacks nothing activation needs, which no
// forge is before #32 and #33.
func allCapabilities(identity.Forge) []repository.Capability { return nil }

// signedUp is a visitor of s signed up as name through the fake forge.
func signedUp(t *testing.T, s *server, fake *forgetest.Fake, who, name string) *visitor {
	t.Helper()
	v := newVisitor(t, s)
	v.logIn(fake, who)
	if status, _, body := v.submit("/login/signup", "/login/signup", url.Values{"name": {name}}); status != http.StatusSeeOther {
		t.Fatalf("sign-up of %s = %d %s", name, status, body)
	}
	return v
}

// TestRepositoriesThroughTheServer activates, deactivates and reactivates a
// repository of the fake forge in a workspace, refuses a person without
// write access and a second workspace, and follows a rename. Alice decides
// on acme/api (2001) and views acme/web (2002); Bob decides on every
// repository.
func TestRepositoriesThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	env := append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001,github:1002")
	s := start(t, dir, env, serverDeps{forge: fake, missing: allCapabilities})
	alice := signedUp(t, s, fake, "1001", "alice")
	bob := signedUp(t, s, fake, "1002", "bob")

	// Only what the forge lets Alice read, through the App.
	_, _, home := alice.send(http.MethodGet, "/", nil)
	if !strings.Contains(home, ">acme/api<") || !strings.Contains(home, ">acme/web<") || strings.Contains(home, "acme/secret") || strings.Contains(home, "other/lib") {
		t.Fatalf("Alice's repositories: %s", home)
	}
	if !strings.Contains(home, `href="https://github.com/acme/api"`) {
		t.Errorf("no link to the forge: %s", home)
	}
	if _, _, found := alice.send(http.MethodGet, "/?q=WEB", nil); strings.Contains(found, ">acme/api<") || !strings.Contains(found, ">acme/web<") {
		t.Errorf("the search for WEB: %s", found)
	}

	activate := func(v *visitor, workspace, id string) (int, string, string) {
		return v.submit("/", "/repositories", url.Values{"workspace": {workspace}, "repository": {id}})
	}
	if status, _, body := activate(alice, "alice", "2002"); status != http.StatusForbidden || !strings.Contains(body, "write access") {
		t.Errorf("activation without write access = %d %s", status, body)
	}
	if status, _, body := activate(alice, "bob", "2001"); status != http.StatusUnprocessableEntity {
		t.Errorf("activation in a workspace of someone else = %d %s", status, body)
	}
	if status, _, body := activate(alice, "alice", "2003"); status != http.StatusUnprocessableEntity {
		t.Errorf("activation of a repository the forge hides = %d %s", status, body)
	}
	status, next, body := activate(alice, "alice", "2001")
	if status != http.StatusSeeOther || next != "/alice/acme/api" {
		t.Fatalf("activation = %d %s %s", status, next, body)
	}
	if _, _, page := alice.send(http.MethodGet, next, nil); !strings.Contains(page, ">active<") || !strings.Contains(page, `action="/alice/acme/api/deactivate"`) {
		t.Errorf("the page of the repository: %s", page)
	}
	if _, _, ws := alice.send(http.MethodGet, "/alice", nil); !strings.Contains(ws, `href="/alice/acme/api"`) {
		t.Errorf("the workspace does not list the repository: %s", ws)
	}

	// One workspace for good, active or not.
	if status, _, body := activate(bob, "bob", "2001"); status != http.StatusConflict || !strings.Contains(body, "another workspace") {
		t.Errorf("activation in a second workspace = %d %s", status, body)
	}
	// The forge lets Bob read it: he sees the page, and cannot change it.
	if status, _, page := bob.send(http.MethodGet, "/alice/acme/api", nil); status != http.StatusOK || strings.Contains(page, "/deactivate") {
		t.Errorf("Bob's view of the page = %d %s", status, page)
	}
	if status, _, _ := bob.submit("/alice/acme/api", "/alice/acme/api/deactivate", url.Values{}); status != http.StatusForbidden {
		t.Errorf("deactivation by a stranger to the workspace = %d", status)
	}
	if status, next, _ := alice.submit("/alice/acme/api", "/alice/acme/api/deactivate", url.Values{}); status != http.StatusSeeOther || next != "/alice/acme/api" {
		t.Fatalf("deactivation = %d %s", status, next)
	}
	if _, _, page := alice.send(http.MethodGet, "/alice/acme/api", nil); !strings.Contains(page, ">deactivated<") || !strings.Contains(page, "Activate again") {
		t.Errorf("the page once deactivated: %s", page)
	}
	if status, _, _ := activate(bob, "bob", "2001"); status != http.StatusConflict {
		t.Errorf("activation of a deactivated repository in a second workspace = %d", status)
	}
	if status, next, _ := activate(alice, "alice", "2001"); status != http.StatusSeeOther || next != "/alice/acme/api" {
		t.Fatalf("reactivation = %d %s", status, next)
	}

	// A rename on the forge keeps the activation; the page answers at the
	// new name once a listing brought it, and the old one is not found.
	fake.RenameRepository("2001", "acme/backend")
	alice.send(http.MethodGet, "/", nil)
	if status, _, _ := alice.send(http.MethodGet, "/alice/acme/api", nil); status != http.StatusNotFound {
		t.Errorf("the old name = %d, want 404", status)
	}
	if status, _, page := alice.send(http.MethodGet, "/alice/acme/backend", nil); status != http.StatusOK || !strings.Contains(page, ">active<") {
		t.Errorf("the new name = %d %s", status, page)
	}
}

// TestNoActivationWithoutTheCapabilities: a forge without files, webhooks
// and events activates nothing, and the screen says why; it has no hook.
func TestNoActivationWithoutTheCapabilities(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	s := start(t, dir, append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001"), serverDeps{forge: fake.Bare()})
	alice := signedUp(t, s, fake, "1001", "alice")
	_, _, home := alice.send(http.MethodGet, "/", nil)
	if !strings.Contains(home, "No repository can be activated yet") || !strings.Contains(home, `<span class="font-mono">files, webhooks, events</span>`) || strings.Contains(home, `action="/repositories"`) {
		t.Fatalf("the screen: %s", home)
	}
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusConflict {
		t.Errorf("activation = %d %s", status, body)
	}
	if got := post(t, s, map[string]string{}, []byte(`{}`)); got != http.StatusForbidden {
		t.Errorf("a delivery to a server without a hook = %d", got)
	}
}

func TestForgeErrorsOfTheRepositoryContext(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{identity.ErrUnavailable, repository.ErrUnavailable},
		{identity.ErrUnauthorized, repository.ErrRefused},
		{identity.ErrRevoked, repository.ErrRefused},
		{identity.ErrNotFound, repository.ErrRefused},
		{identity.ErrNoIdentity, repository.ErrRefused},
	} {
		if got := forgeError(tc.err); !errors.Is(got, tc.want) || !errors.Is(got, tc.err) {
			t.Errorf("forgeError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	other := errors.New("other")
	if got := forgeError(other); got != other {
		t.Errorf("forgeError(other) = %v", got)
	}
}

// TestNoRepositoriesWithoutAForge: a server without a forge has no
// repository service.
func TestNoRepositoriesWithoutAForge(t *testing.T) {
	if r := newRepositories(nil, nil, nil, nil, missingCapabilities); r != nil {
		t.Error("a repository service without a forge")
	}
	if repositoryService(nil) != nil {
		t.Error("a nil service is not nil for the handler")
	}
	if got := missingCapabilities(nil); len(got) != len(repository.Preconditions()) {
		t.Errorf("missingCapabilities = %v", got)
	}
	// The fake lacks nothing; without its optional capabilities, it lacks
	// them all.
	f := forgetest.New("github", nil)
	if got := missingCapabilities(f); len(got) != 0 {
		t.Errorf("missingCapabilities(fake) = %v, want none", got)
	}
	if got := missingCapabilities(f.Bare()); len(got) != 3 {
		t.Errorf("missingCapabilities(bare) = %v, want all three", got)
	}
	if hooks(f.Bare(), &repository.Service{}, newReachSync(), newConfigRefresh()) != nil || hooks(f, nil, newReachSync(), newConfigRefresh()) != nil {
		t.Error("a hook without a forge that receives events")
	}
	if _, ok := any(f).(repository.Reach); !ok {
		t.Error("the fake forge cannot tell its reach")
	}
}

// TestPermissionOfRefusedTokens: tokens the forge revoked, tokens gone and an
// account without an identity on the forge are refusals of the tokens, which
// the page answers with a new login; a forge that cannot answer stays one.
func TestPermissionOfRefusedTokens(t *testing.T) {
	ctx := context.Background()
	fake, _ := forgetest.Scenario("github", false)
	store := &identitytest.Memory{}
	now := func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }
	tokens := identity.NewTokenService(fake, store, now)
	permissions := identity.NewPermissionService(fake, tokens, store, now)
	f := repositoryForge{name: "github", permissions: permissions}

	code, _, err := fake.Approve("1001", fake.AuthorizeURL("s", identity.Challenge("verifier-0123456789-0123456789-0123456789")))
	if err != nil {
		t.Fatal(err)
	}
	id, tk, err := fake.Exchange(ctx, code, "verifier-0123456789-0123456789-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSignup(ctx, identity.Signup{Session: "s", Identity: id, Tokens: tk, ExpiresAt: now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteSignup(ctx, "s", identity.Account{ID: "a1", Name: "alice"}, now()); err != nil {
		t.Fatal(err)
	}
	if p, err := f.Permission(ctx, "a1", "2001"); err != nil || p != repository.PermissionDecide {
		t.Fatalf("Permission = %v, %v", p, err)
	}
	if p, err := f.Permission(ctx, "a1", "2003"); err != nil || p != repository.PermissionNone {
		t.Errorf("Permission of a hidden repository = %v, %v; want none", p, err)
	}
	if _, err := f.Permission(ctx, "nobody", "2001"); !errors.Is(err, repository.ErrRefused) {
		t.Errorf("Permission without an identity = %v, want ErrRefused", err)
	}
	fake.SetDown(true)
	if _, err := f.Permission(ctx, "a1", "2002"); !errors.Is(err, repository.ErrUnavailable) {
		t.Errorf("Permission with the forge down = %v, want ErrUnavailable", err)
	}
	fake.SetDown(false)
	if err := store.RevokeTokens(ctx, id.Key()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Permission(ctx, "a1", "2002"); !errors.Is(err, repository.ErrRefused) {
		t.Errorf("Permission once the tokens are gone = %v, want ErrRefused", err)
	}
}

// TestAFailedSyncIsTriedAgain: a sync that fails is tried again, after a
// wait that grows, without a new request; a success resets the wait.
func TestAFailedSyncIsTriedAgain(t *testing.T) {
	r := newReachSync()
	r.retryFirst, r.retry = time.Millisecond, 4*time.Millisecond
	var calls int
	results := make(chan error, 8)
	sync := func(context.Context) ([]string, error) {
		calls++
		if calls <= 3 {
			return nil, repository.ErrUnavailable
		}
		return []string{"2001"}, nil
	}
	done := make(chan struct{})
	defer close(done)
	var logs syncBuffer
	go r.run(context.Background(), newLogger(slog.LevelInfo, "json", &logs), sync, func(_ []string, err error) { results <- err }, done)
	r.request()
	for i := range 4 {
		select {
		case err := <-results:
			if (i < 3) != (err != nil) {
				t.Fatalf("sync %d: %v", i, err)
			}
		case <-time.After(wait):
			t.Fatalf("sync %d did not run: the failure was not tried again", i)
		}
	}
	select {
	case err := <-results:
		t.Errorf("a sync ran after the success, without a request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if got := strings.Count(logs.String(), "trying again later"); got != 3 {
		t.Errorf("%d failures logged, want 3", got)
	}
	// No reach on the forge is no failure: nothing is tried again.
	r2 := newReachSync()
	r2.retryFirst = time.Millisecond
	none := make(chan error, 4)
	go r2.run(context.Background(), newLogger(slog.LevelInfo, "json", &logs), func(context.Context) ([]string, error) { return nil, repository.ErrNoReach }, func(_ []string, err error) { none <- err }, done)
	r2.request()
	<-none
	select {
	case <-none:
		t.Error("a forge without reach was asked again")
	case <-time.After(20 * time.Millisecond):
	}
}
