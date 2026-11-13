package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/forgetest"
)

// visitor is a browser on a started server: it keeps its cookies and follows
// no redirect, so a test sees each step of a login.
type visitor struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newVisitor(t *testing.T, s *server) *visitor {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &visitor{t: t, base: "http://" + s.addr, client: &http.Client{
		Jar:           jar,
		Timeout:       wait,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var csrfInput = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// send sends a request and returns its status, its Location and its body.
func (v *visitor) send(method, path string, form url.Values) (int, string, string) {
	v.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, v.base+path, body)
	if err != nil {
		v.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", v.base)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		v.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// submit posts form to path with the CSRF token of the page at page.
func (v *visitor) submit(page, path string, form url.Values) (int, string, string) {
	v.t.Helper()
	_, _, body := v.send(http.MethodGet, page, nil)
	m := csrfInput.FindStringSubmatch(body)
	if m == nil {
		v.t.Fatalf("no CSRF token on %s: %s", page, body)
	}
	form.Set("csrf", m[1])
	return v.send(http.MethodPost, path, form)
}

// logIn takes v through the forge as who and returns where the callback
// leads.
func (v *visitor) logIn(fake *forgetest.Fake, who string) (int, string, string) {
	v.t.Helper()
	status, forge, _ := v.send(http.MethodGet, "/login", nil)
	if status != http.StatusSeeOther {
		v.t.Fatalf("/login = %d", status)
	}
	code, state, err := fake.Approve(who, forge)
	if err != nil {
		v.t.Fatal(err)
	}
	return v.send(http.MethodGet, "/login/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
}

// failsToStart runs the server on env and returns what it wrote, failing the
// test unless it stops before listening.
func failsToStart(t *testing.T, dir string, env []string, deps serverDeps) string {
	t.Helper()
	environ := append(env, "RAUN_DATABASE_PATH="+filepath.Join(dir, "raun.db"), "RAUN_DATABASE_MIGRATE=true")
	var stderr syncBuffer
	started := false
	deps.started = func(net.Addr) { started = true }
	if code := runServer(context.Background(), environ, &stderr, deps); code != exitFailure || started {
		t.Fatalf("exit code %d, started %v:\n%s", code, started, stderr.String())
	}
	return stderr.String()
}

// keyed is env with the secret keys given, the first value of a variable
// winning.
func keyed(env []string, keys ...string) []string {
	return append(keys, env...)
}

// TestLoginThroughTheServer signs Alice up through the fake forge, logs her
// out and in again, rotates the secret key across restarts, and checks that
// the tokens never reach the database file in clear.
func TestLoginThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	env := append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001")
	deps := serverDeps{forge: fake}

	s := start(t, dir, env, deps)
	alice := newVisitor(t, s)
	if status, next, _ := alice.logIn(fake, "1001"); status != http.StatusSeeOther || next != "/login/signup" {
		t.Fatalf("first callback = %d %s, want the sign-up", status, next)
	}
	if status, next, body := alice.submit("/login/signup", "/login/signup", url.Values{"name": {"alice"}}); status != http.StatusSeeOther || next != "/" {
		t.Fatalf("sign-up = %d %s %s", status, next, body)
	}
	if _, _, home := alice.send(http.MethodGet, "/", nil); !strings.Contains(home, ">alice<") {
		t.Errorf("home after the sign-up does not show the account: %s", home)
	}
	if status, next, _ := alice.submit("/", "/logout", url.Values{}); status != http.StatusSeeOther || next != "/" {
		t.Errorf("logout = %d %s", status, next)
	}
	if _, _, home := alice.send(http.MethodGet, "/", nil); strings.Contains(home, ">alice<") || !strings.Contains(home, `href="/login"`) {
		t.Errorf("home after the logout: %s", home)
	}

	// Bob is outside the rule.
	if status, _, body := newVisitor(t, s).logIn(fake, "1002"); status != http.StatusForbidden || !strings.Contains(body, "You cannot sign up here") {
		t.Errorf("Bob's callback = %d %s", status, body)
	}
	if code := s.stop(t); code != exitOK {
		t.Fatalf("exit code %d:\n%s", code, s.stderr)
	}

	for _, f := range []string{"raun.db", "raun.db-wal"} {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "github-access-") || strings.Contains(string(data), "github-refresh-") {
			t.Errorf("%s holds a token in clear", f)
		}
	}

	// A new key, the old one as previous: the start seals the tokens again.
	rotated := keyed(env, "RAUN_SECRET_KEY="+secretKey(2), "RAUN_SECRET_KEY_PREVIOUS="+secretKey(1))
	s = start(t, dir, rotated, deps)
	if !strings.Contains(s.stderr.String(), `"msg":"stored secrets sealed under the new key","count":1`) {
		t.Errorf("the rotation is not logged:\n%s", s.stderr)
	}
	s.stop(t)

	// The previous key goes: Alice logs in again to the same account.
	s = start(t, dir, keyed(env, "RAUN_SECRET_KEY="+secretKey(2)), deps)
	again := newVisitor(t, s)
	if status, next, _ := again.logIn(fake, "1001"); status != http.StatusSeeOther || next != "/" {
		t.Errorf("second login = %d %s, want home without a sign-up", status, next)
	}
	if _, _, home := again.send(http.MethodGet, "/", nil); !strings.Contains(home, ">alice<") {
		t.Errorf("the second login did not reach the account: %s", home)
	}
	s.stop(t)

	// A key that sealed nothing here: the server refuses to start.
	out := failsToStart(t, dir, keyed(env, "RAUN_SECRET_KEY="+secretKey(3)), deps)
	if !strings.Contains(out, "RAUN_SECRET_KEY") || !strings.Contains(out, "neither RAUN_SECRET_KEY nor RAUN_SECRET_KEY_PREVIOUS") {
		t.Errorf("start under an unknown key:\n%s", out)
	}
}

func TestServerNeedsASecretKeyForItsSecrets(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	env := append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001")
	s := start(t, dir, env, serverDeps{forge: fake})
	v := newVisitor(t, s)
	v.logIn(fake, "1001")
	v.submit("/login/signup", "/login/signup", url.Values{"name": {"alice"}})
	s.stop(t)
	// Without a forge, and so without a key, the stored tokens stop the
	// server all the same.
	out := failsToStart(t, dir, nil, serverDeps{})
	if !strings.Contains(out, "RAUN_SECRET_KEY is not set") {
		t.Errorf("start without a key over stored secrets:\n%s", out)
	}
}

func TestSignupOrgsNeedMemberships(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", true)
	out := failsToStart(t, dir, append(app(keyFile), "RAUN_SIGNUP_ORGS=github:3001"), serverDeps{forge: fake.Bare()})
	if !strings.Contains(out, "RAUN_SIGNUP_ORGS: github:3001 names an organization of github, which cannot tell the members of one") {
		t.Errorf("start with RAUN_SIGNUP_ORGS on a forge without memberships:\n%s", out)
	}
}
