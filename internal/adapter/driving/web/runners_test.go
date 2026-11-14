package web

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
)

// failingRegistry is a registry whose store cannot authenticate.
type failingRegistry struct{ *job.Registry }

func (failingRegistry) Authenticate(context.Context, string) (job.Runner, error) {
	return job.Runner{}, errors.New("database locked")
}

// newRunnerServer is a login server with an Instance screen and runners on
// a memory store, with the workspace acme.
func newRunnerServer(t *testing.T, wrap func(*job.Registry) RunnerRegistry) (*loginServer, *adminSwitch, *job.Registry) {
	t.Helper()
	admins := &adminSwitch{}
	store := &jobtest.Memory{}
	store.AddWorkspace(job.Workspace{ID: strings.Repeat("a", 32), Name: "acme"})
	registry := job.NewRegistry(job.RegistryOptions{Store: store, Admins: admins, Random: rand.Reader,
		Now: func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }})
	var runners RunnerRegistry = registry
	if wrap != nil {
		runners = wrap(registry)
	}
	l := newLoginServer(t, func(o *Options) { o.Administrators = admins; o.Runners = runners })
	return l, admins, registry
}

// call sends a request of a runner to the runner API, with these values of
// Authorization.
func call(l *loginServer, host string, authorization ...string) *http.Response {
	r := request(http.MethodPost, "/api/runner/v1/claim", host, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	for _, a := range authorization {
		r.Header.Add("Authorization", a)
	}
	return serve(l, r)
}

func TestTheRunnerAPIAuthenticatesByToken(t *testing.T) {
	ctx := context.Background()
	l, admins, registry := newRunnerServer(t, nil)
	admin := strings.Repeat("e", 32)
	admins.set(admin, true, nil)
	runner, token, err := registry.Create(ctx, admin, "builder", "", []job.Capability{job.CapabilityNoop})
	if err != nil {
		t.Fatal(err)
	}
	// A known token gets through: no route of the protocol exists yet.
	if resp := call(l, testHost, "Bearer "+token); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a runner = %d %s", resp.StatusCode, readBody(t, resp))
	}
	refused := func(name string, resp *http.Response) string {
		t.Helper()
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s = %d %s", name, resp.StatusCode, body)
		}
		return body
	}
	want := refused("no header", call(l, testHost))
	for name, auth := range map[string][]string{
		"another scheme":   {"Basic " + token},
		"an empty token":   {"Bearer "},
		"two headers":      {"Bearer " + token, "Bearer " + token},
		"an unknown token": {"Bearer " + strings.Repeat("0", 64)},
		"a token too long": {"Bearer " + strings.Repeat("a", job.MaxToken+1)},
		"two tokens":       {"Bearer " + token + " " + token},
	} {
		if body := refused(name, call(l, testHost, auth...)); body != want {
			t.Errorf("%s answers %q, want the same refusal %q", name, body, want)
		}
	}
	if resp := call(l, "evil.example", "Bearer "+token); resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusOK {
		t.Errorf("another host = %d", resp.StatusCode)
	}
	if strings.Contains(l.logs.String(), token) {
		t.Error("a token was logged")
	}

	// The token of a runner opens nothing of the application.
	r := request(http.MethodGet, "/instance/runners", testHost, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if got := location(t, serve(l, r), http.StatusSeeOther); !strings.HasPrefix(got, "/login") {
		t.Errorf("the token of a runner on the Runners screen went to %q", got)
	}
	// The session of an administrator opens nothing of the runner API.
	alice := l.signUp(t, "1001", "alice")
	admins.set(alice.session(l.sessions).Account, true, nil)
	cookie := request(http.MethodPost, "/api/runner/v1/claim", testHost, strings.NewReader("{}"))
	cookie.AddCookie(alice.cookie)
	refused("the cookie of an administrator", serve(l, cookie))

	// A new token refuses the old one; a revocation refuses the runner.
	_, fresh, err := registry.Rotate(ctx, admin, runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	refused("the old token", call(l, testHost, "Bearer "+token))
	if resp := call(l, testHost, "bearer "+fresh); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the new token = %d", resp.StatusCode)
	}
	if _, _, err := registry.Revoke(ctx, admin, runner.ID); err != nil {
		t.Fatal(err)
	}
	refused("a revoked runner", call(l, testHost, "Bearer "+fresh))
}

func TestARunnerStoreThatFailsIsNotARefusal(t *testing.T) {
	l, _, _ := newRunnerServer(t, func(g *job.Registry) RunnerRegistry { return failingRegistry{g} })
	resp := call(l, testHost, "Bearer "+strings.Repeat("0", 64))
	if body := readBody(t, resp); resp.StatusCode != http.StatusInternalServerError || strings.Contains(body, strings.Repeat("0", 64)) {
		t.Errorf("a failing store = %d %s", resp.StatusCode, body)
	}
}

func TestTheRunnersScreen(t *testing.T) {
	l, admins, _ := newRunnerServer(t, nil)
	visitor := l.browser(t)
	if got := location(t, visitor.do(http.MethodGet, "/instance/runners", nil), http.StatusSeeOther); !strings.HasPrefix(got, "/login") {
		t.Errorf("a visitor went to %q", got)
	}
	alice := l.signUp(t, "1001", "alice")
	account := alice.session(l.sessions).Account
	// post sets the CSRF token in the form it sends: each request gets its
	// own.
	create := func() url.Values {
		return url.Values{"name": {"builder"}, "workspace": {"acme"}, "capability": {"noop"}}
	}

	// Not an administrator: refused, every route.
	if resp := alice.do(http.MethodGet, "/instance/runners", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a non-administrator = %d", resp.StatusCode)
	}
	for _, path := range []string{"/instance/runners", "/instance/runners/x/rotate", "/instance/runners/x/revoke"} {
		if resp := alice.post(l, path, create()); resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s by a non-administrator = %d", path, resp.StatusCode)
		}
	}

	admins.set(account, true, nil)
	if resp := alice.do(http.MethodPost, "/instance/runners", create()); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a creation without its CSRF token = %d", resp.StatusCode)
	}
	resp := alice.post(l, "/instance/runners", create())
	page := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "RAUN_RUNNER_TOKEN") || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("a creation = %d %s", resp.StatusCode, page)
	}
	token := between(t, page, `data-slot="token" class="block break-all rounded-md bg-muted px-3 py-2 font-mono text-[13px]">`, "</code>")
	if len(token) != 64 {
		t.Fatalf("the token shown: %q", token)
	}
	list := readBody(t, alice.do(http.MethodGet, "/instance/runners", nil))
	if !strings.Contains(list, "builder") || !strings.Contains(list, "workspace acme") || strings.Contains(list, token) || strings.Contains(list, job.HashToken(token)) {
		t.Errorf("the list after a creation: %s", list)
	}
	if strings.Contains(l.logs.String(), token) {
		t.Error("the token was logged")
	}

	for name, tc := range map[string]struct {
		form url.Values
		want int
	}{
		"a taken name":         {create(), http.StatusConflict},
		"an empty name":        {url.Values{"name": {" "}, "workspace": {""}, "capability": {"noop"}}, http.StatusUnprocessableEntity},
		"no capability":        {url.Values{"name": {"x"}, "workspace": {""}}, http.StatusUnprocessableEntity},
		"an unknown one":       {url.Values{"name": {"x"}, "workspace": {""}, "capability": {"agents"}}, http.StatusUnprocessableEntity},
		"an unknown workspace": {url.Values{"name": {"x"}, "workspace": {"nowhere"}, "capability": {"noop"}}, http.StatusUnprocessableEntity},
		"no workspace field":   {url.Values{"name": {"x"}, "capability": {"noop"}}, http.StatusBadRequest},
	} {
		if resp := alice.post(l, "/instance/runners", tc.form); resp.StatusCode != tc.want {
			t.Errorf("%s = %d, want %d", name, resp.StatusCode, tc.want)
		}
	}

	id := between(t, list, `action="/instance/runners/`, `/rotate"`)
	resp = alice.post(l, "/instance/runners/"+id+"/rotate", nil)
	rotated := readBody(t, resp)
	if fresh := between(t, rotated, `data-slot="token" class="block break-all rounded-md bg-muted px-3 py-2 font-mono text-[13px]">`, "</code>"); resp.StatusCode != http.StatusOK || len(fresh) != 64 || fresh == token {
		t.Errorf("a rotation = %d, token %q", resp.StatusCode, fresh)
	}
	if got := location(t, alice.post(l, "/instance/runners/"+id+"/revoke", nil), http.StatusSeeOther); got != "/instance/runners" {
		t.Errorf("a revocation went to %q", got)
	}
	if list := readBody(t, alice.do(http.MethodGet, "/instance/runners", nil)); !strings.Contains(list, ">revoked<") || strings.Contains(list, "/rotate") {
		t.Errorf("the list after a revocation: %s", list)
	}
	if resp := alice.post(l, "/instance/runners/"+id+"/rotate", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("a rotation of a revoked runner = %d", resp.StatusCode)
	}
	if resp := alice.post(l, "/instance/runners/"+strings.Repeat("f", 32)+"/revoke", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a revocation of an unknown runner = %d", resp.StatusCode)
	}
	if !strings.Contains(readBody(t, alice.do(http.MethodGet, "/instance", nil)), `href="/instance/runners"`) {
		t.Error("the Instance screen has no link to the runners")
	}
}

// between returns the text of s between the first before and the after
// that follows it.
func between(t *testing.T, s, before, after string) string {
	t.Helper()
	_, rest, ok := strings.Cut(s, before)
	if !ok {
		t.Fatalf("no %q in %s", before, s)
	}
	got, _, ok := strings.Cut(rest, after)
	if !ok {
		t.Fatalf("no %q after %q", after, before)
	}
	return got
}

// TestTheRunnerReachesTheHandler: the middleware gives the handlers of the
// runner API the runner of the token, as the store holds it.
func TestTheRunnerReachesTheHandler(t *testing.T) {
	l, admins, registry := newRunnerServer(t, nil)
	admin := strings.Repeat("e", 32)
	admins.set(admin, true, nil)
	runner, token, err := registry.Create(context.Background(), admin, "builder", "acme", []job.Capability{job.CapabilityNoop})
	if err != nil {
		t.Fatal(err)
	}
	var got job.Runner
	h := l.runnerAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = runnerFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	r := request(http.MethodPost, "/api/runner/v1/claim", testHost, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if resp := serve(h, r); resp.StatusCode != http.StatusNoContent || got.ID != runner.ID || got.Workspace != runner.Workspace {
		t.Errorf("the handler got %+v, status %d", got, resp.StatusCode)
	}
}
