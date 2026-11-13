package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
	"github.com/kvitrvn/raun/internal/core/session"
	"github.com/kvitrvn/raun/internal/core/session/sessiontest"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// loginServer is a server with the fake forge of the contract scenario:
// Alice (1001) may sign up, Bob (1002) may not.
type loginServer struct {
	*Server
	forge    *forgetest.Fake
	sessions *sessiontest.Memory
	accounts *identitytest.Memory
	logs     *bytes.Buffer
	forgot   *forgotten
}

// forgotten records the accounts whose permissions logout forgets.
type forgotten struct {
	mu       sync.Mutex
	accounts []string
}

func (f *forgotten) Forget(account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts = append(f.accounts, account)
}

func (f *forgotten) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.accounts)
}

func newLoginServer(t *testing.T) *loginServer {
	t.Helper()
	fake, _ := forgetest.Scenario("github", false)
	l := &loginServer{forge: fake, sessions: &sessiontest.Memory{}, accounts: &identitytest.Memory{}, logs: &bytes.Buffer{}, forgot: &forgotten{}}
	now := func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }
	policy := identity.Policy{Users: []identity.Entry{{Forge: "github", ID: "1001"}}}
	s, err := New(Options{
		Logger:      slog.New(slog.NewJSONHandler(l.logs, nil)),
		PublicHost:  testHost,
		Scheme:      "https",
		Ready:       func(context.Context) error { return nil },
		Sessions:    l.sessions,
		Login:       identity.NewLogin(fake, policy, l.accounts, rand.Reader, now),
		Accounts:    l.accounts,
		Workspaces:  identity.NewWorkspaces(l.accounts, rand.Reader, now),
		Permissions: l.forgot,
		Now:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	l.Server = s
	return l
}

// browser keeps the session cookie of one browser across requests.
type browser struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (l *loginServer) browser(t *testing.T) *browser { return &browser{t: t, h: l} }

func (b *browser) do(method, target string, form url.Values) *http.Response {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := request(method, target, testHost, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://"+testHost)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if b.cookie != nil {
		r.AddCookie(b.cookie)
	}
	resp := serve(b.h, r)
	for _, c := range resp.Cookies() {
		if c.Name == secureCookie {
			if c.MaxAge < 0 {
				b.cookie = nil
			} else {
				b.cookie = c
			}
		}
	}
	return resp
}

// session is the session of the browser in store.
func (b *browser) session(store session.Store) session.Session {
	b.t.Helper()
	if b.cookie == nil {
		b.t.Fatal("the browser has no session")
	}
	hash, err := session.HashOf(b.cookie.Value)
	if err != nil {
		b.t.Fatal(err)
	}
	s, err := store.Get(context.Background(), hash)
	if err != nil {
		b.t.Fatalf("the session of the browser: %v", err)
	}
	return s
}

// post posts form with the CSRF token of the session of the browser.
func (b *browser) post(l *loginServer, target string, form url.Values) *http.Response {
	b.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set(csrfField, b.session(l.sessions).CSRF)
	return b.do(http.MethodPost, target, form)
}

func location(t *testing.T, resp *http.Response, status int) string {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, status, readBody(t, resp))
	}
	return resp.Header.Get("Location")
}

// authorize starts a login of who in b, back to returnPath, and returns the
// callback the forge sends the browser to.
func (l *loginServer) authorize(t *testing.T, b *browser, who, returnPath string) string {
	t.Helper()
	target := "/login"
	if returnPath != "" {
		target += "?" + url.Values{returnParam: {returnPath}}.Encode()
	}
	forge := location(t, b.do(http.MethodGet, target, nil), http.StatusSeeOther)
	code, state, err := l.forge.Approve(who, forge)
	if err != nil {
		t.Fatal(err)
	}
	return "/login/callback?" + url.Values{"state": {state}, "code": {code}}.Encode()
}

// signUp takes a new browser through the whole sign-up of who under name.
func (l *loginServer) signUp(t *testing.T, who, name string) *browser {
	t.Helper()
	b := l.browser(t)
	callback := l.authorize(t, b, who, "")
	if got := location(t, b.do(http.MethodGet, callback, nil), http.StatusSeeOther); got != "/login/signup" {
		t.Fatalf("callback went to %s", got)
	}
	location(t, b.post(l, "/login/signup", url.Values{"name": {name}}), http.StatusSeeOther)
	return b
}

func TestSignUpThenLogInAgain(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	callback := l.authorize(t, b, "1001", "/acme/api?tab=items")
	anonymous := b.session(l.sessions)
	if anonymous.Account != "" {
		t.Fatal("the session has an account before the login")
	}
	if got := location(t, b.do(http.MethodGet, callback, nil), http.StatusSeeOther); got != "/login/signup" {
		t.Fatalf("callback went to %q, want the sign-up", got)
	}
	page := b.do(http.MethodGet, "/login/signup", nil)
	if body := readBody(t, page); page.StatusCode != http.StatusOK || !strings.Contains(body, `value="alice"`) || !strings.Contains(body, "Choose your name") {
		t.Fatalf("sign-up page = %d %s", page.StatusCode, body)
	}

	// A name the rule refuses, then a reserved one: the form comes back.
	for name, status := range map[string]int{"Al--ice": http.StatusUnprocessableEntity, "admin": http.StatusUnprocessableEntity} {
		resp := b.post(l, "/login/signup", url.Values{"name": {name}})
		if body := readBody(t, resp); resp.StatusCode != status || !strings.Contains(body, `aria-invalid="true"`) {
			t.Errorf("sign-up as %q = %d %s", name, resp.StatusCode, body)
		}
	}
	oldCookie, oldCSRF := b.cookie, anonymous.CSRF
	back := location(t, b.post(l, "/login/signup", url.Values{"name": {"alice"}}), http.StatusSeeOther)
	if back != "/acme/api?tab=items" {
		t.Errorf("sign-up went back to %q", back)
	}
	logged := b.session(l.sessions)
	if logged.Account == "" || b.cookie.Value == oldCookie.Value || logged.CSRF == oldCSRF {
		t.Fatalf("the session was not renewed with the account: %+v", logged)
	}
	if _, err := l.sessions.Get(context.Background(), anonymous.Hash); err == nil {
		t.Error("the session before the login still lives")
	}
	home := readBody(t, b.do(http.MethodGet, "/", nil))
	if !strings.Contains(home, `data-slot="account"`) || !strings.Contains(home, ">alice<") || !strings.Contains(home, `action="/logout"`) {
		t.Errorf("the top bar does not show the account: %s", home)
	}

	// /login with an account goes back at once.
	if got := location(t, b.do(http.MethodGet, "/login?return=/x", nil), http.StatusSeeOther); got != "/x" {
		t.Errorf("/login with an account went to %q", got)
	}

	// Log out, then log in again from another browser: the same account.
	if got := location(t, b.post(l, "/logout", nil), http.StatusSeeOther); got != "/" || b.cookie != nil {
		t.Errorf("logout went to %q, cookie %v", got, b.cookie)
	}
	other := l.browser(t)
	if got := location(t, other.do(http.MethodGet, l.authorize(t, other, "1001", ""), nil), http.StatusSeeOther); got != "/" {
		t.Errorf("a second login went to %q, want / without a sign-up", got)
	}
	if again := other.session(l.sessions); again.Account != logged.Account {
		t.Errorf("the second login reached account %q, want %q", again.Account, logged.Account)
	}
}

func TestCallbackRefusals(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	callback := l.authorize(t, b, "1001", "")
	u, _ := url.Parse(callback)
	q := u.Query()
	forged := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	stranger := l.browser(t)
	stranger.do(http.MethodGet, "/login", nil)
	for _, tc := range []struct {
		name    string
		b       *browser
		target  string
		status  int
		message string
	}{
		{"a forged state", b, "/login/callback?" + url.Values{"state": {forged}, "code": {q.Get("code")}}.Encode(), http.StatusBadRequest, "no longer valid"},
		{"no session", l.browser(t), callback, http.StatusBadRequest, "without the session"},
		{"the forge says no", b, "/login/callback?error=access_denied&error_description=%3Cb%3Eno%3C%2Fb%3E", http.StatusBadRequest, "not given"},
		{"no code", b, "/login/callback?state=" + q.Get("state"), http.StatusBadRequest, "incomplete"},
		{"two states", b, callback + "&state=x", http.StatusBadRequest, "incomplete"},
	} {
		resp := tc.b.do(http.MethodGet, tc.target, nil)
		body := readBody(t, resp)
		if resp.StatusCode != tc.status || !strings.Contains(body, tc.message) || strings.Contains(body, "<b>no") {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
	// The state of b, sent back in another browser, logs nobody in, and is
	// spent for b too.
	if resp := stranger.do(http.MethodGet, callback, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a callback in another browser = %d", resp.StatusCode)
	}
	if resp := b.do(http.MethodGet, callback, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the callback after another browser used its state = %d", resp.StatusCode)
	}

	// A replayed callback.
	callback = l.authorize(t, b, "1001", "")
	location(t, b.do(http.MethodGet, callback, nil), http.StatusSeeOther)
	if resp := b.do(http.MethodGet, callback, nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(t, resp), "used already") {
		t.Errorf("a replayed callback = %d", resp.StatusCode)
	}
	if !strings.Contains(l.logs.String(), `"login refused"`) {
		t.Error("a refused callback is not logged")
	}
}

func TestLoginNeverLeavesTheInstance(t *testing.T) {
	l := newLoginServer(t)
	signedUp := l.signUp(t, "1001", "alice")
	location(t, signedUp.post(l, "/logout", nil), http.StatusSeeOther)
	for _, back := range []string{"https://evil.example/", "//evil.example", "/\\evil.example", "/%0d%0aSet-Cookie:x", "javascript:alert(1)"} {
		b := l.browser(t)
		got := location(t, b.do(http.MethodGet, l.authorize(t, b, "1001", back), nil), http.StatusSeeOther)
		if got != "/" && !strings.HasPrefix(got, "/%") {
			t.Errorf("return %q led to %q", back, got)
		}
		if strings.Contains(got, "evil") || strings.ContainsAny(got, "\r\n") {
			t.Errorf("return %q led off the instance: %q", back, got)
		}
		// With an account, /login goes back by the same rule.
		if got := location(t, b.do(http.MethodGet, "/login?"+url.Values{returnParam: {back}}.Encode(), nil), http.StatusSeeOther); strings.Contains(got, "evil") {
			t.Errorf("/login with an account and return %q led to %q", back, got)
		}
	}
}

func TestSomeoneOutsideTheRuleIsRefused(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	resp := b.do(http.MethodGet, l.authorize(t, b, "1002", ""), nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "You cannot sign up here") ||
		!strings.Contains(body, "not listed in RAUN_SIGNUP_USERS or RAUN_ADMINS") || strings.Contains(body, "3001") {
		t.Errorf("refusal = %d %s", resp.StatusCode, body)
	}
	if b.session(l.sessions).Account != "" {
		t.Error("a refused person got an account")
	}
	if resp := b.do(http.MethodGet, "/login/signup", nil); location(t, resp, http.StatusSeeOther) != "/login" {
		t.Error("a refused person got a sign-up")
	}
}

// downMembers is a forge whose memberships cannot be checked.
type downMembers struct{ *forgetest.Fake }

func (downMembers) IsMember(context.Context, identity.Tokens, string) (bool, error) {
	return false, identity.ErrUnavailable
}

// TestAMembershipThatCannotBeChecked: the login is refused, as a failure of
// the forge to try again, not as a refusal of the person.
func TestAMembershipThatCannotBeChecked(t *testing.T) {
	l := newLoginServer(t)
	policy := identity.Policy{Orgs: []identity.Entry{{Forge: "github", ID: "3001"}}}
	l.Server.login = identity.NewLogin(downMembers{l.forge}, policy, l.accounts, rand.Reader, l.Server.now)
	b := l.browser(t)
	resp := b.do(http.MethodGet, l.authorize(t, b, "1001", ""), nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "The forge cannot be reached") || strings.Contains(body, "You cannot sign up here") {
		t.Errorf("callback with memberships down = %d %s", resp.StatusCode, body)
	}
	if b.session(l.sessions).Account != "" {
		t.Error("a login whose rule could not be checked got an account")
	}
}

func TestATakenName(t *testing.T) {
	l := newLoginServer(t)
	// Alice takes the name bob, then the rule admits Bob, who wants it.
	l.signUp(t, "1001", "bob")
	l.Server.login = identity.NewLogin(l.forge, identity.Policy{Users: []identity.Entry{{Forge: "github", ID: "1002"}}}, l.accounts, rand.Reader, l.Server.now)
	b := l.browser(t)
	location(t, b.do(http.MethodGet, l.authorize(t, b, "1002", ""), nil), http.StatusSeeOther)
	resp := b.post(l, "/login/signup", url.Values{"name": {"bob"}})
	if body := readBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This name is taken") {
		t.Errorf("a taken name = %d %s", resp.StatusCode, body)
	}
	location(t, b.post(l, "/login/signup", url.Values{"name": {"bob-durand"}}), http.StatusSeeOther)
}

func TestSignupNeedsItsCSRFToken(t *testing.T) {
	l := newLoginServer(t)
	b := l.browser(t)
	location(t, b.do(http.MethodGet, l.authorize(t, b, "1001", ""), nil), http.StatusSeeOther)
	if resp := b.do(http.MethodPost, "/login/signup", url.Values{"name": {"alice"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("sign-up without the CSRF token = %d", resp.StatusCode)
	}
	form := url.Values{"name": {"alice"}, csrfField: {b.session(l.sessions).CSRF}}
	r := request(http.MethodPost, "/login/signup", testHost, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(b.cookie)
	if resp := serve(l, r); resp.StatusCode != http.StatusForbidden {
		t.Errorf("sign-up from another site = %d", resp.StatusCode)
	}
	if _, err := l.accounts.AccountOf(context.Background(), "github:1001"); err == nil {
		t.Error("a refused sign-up created the account")
	}
}

func TestLogoutEndsOneSession(t *testing.T) {
	l := newLoginServer(t)
	first := l.signUp(t, "1001", "alice")
	second := l.browser(t)
	location(t, second.do(http.MethodGet, l.authorize(t, second, "1001", ""), nil), http.StatusSeeOther)
	kept := second.session(l.sessions)
	location(t, first.post(l, "/logout", nil), http.StatusSeeOther)
	if got, err := l.sessions.Get(context.Background(), kept.Hash); err != nil || got.Account == "" {
		t.Errorf("logout ended another session of the account: %v", err)
	}
	if got := l.forgot.list(); len(got) != 1 || got[0] != kept.Account {
		t.Errorf("logout forgot the permissions of %v, want the account %s once", got, kept.Account)
	}
	// A browser without an account forgets nothing.
	anonymous := l.browser(t)
	anonymous.do(http.MethodGet, "/login", nil)
	anonymous.post(l, "/logout", nil)
	if got := l.forgot.list(); len(got) != 1 {
		t.Errorf("an anonymous logout forgot %v", got)
	}
	if resp := first.do(http.MethodPost, "/logout", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("logout without a session = %d", resp.StatusCode)
	}
}

func TestHeadOnTheLoginChangesNothing(t *testing.T) {
	l := newLoginServer(t)
	for _, target := range []string{"/login", "/login/callback?state=x&code=y"} {
		b := l.browser(t)
		resp := b.do(http.MethodHead, target, nil)
		if resp.StatusCode != http.StatusOK || b.cookie != nil || resp.Header.Get("Location") != "" {
			t.Errorf("HEAD %s = %d, cookie %v", target, resp.StatusCode, b.cookie)
		}
	}
	if n, _ := l.accounts.DeleteExpiredLoginStates(context.Background(), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); n != 0 {
		t.Errorf("HEAD stored %d login states", n)
	}
}

func TestLoginWithoutAForge(t *testing.T) {
	s, _ := newTestServer(t, nil)
	for _, target := range []string{"/login", "/login/callback?state=x&code=y", "/login/signup"} {
		resp := serve(s, request(http.MethodGet, target, testHost, nil))
		if body := readBody(t, resp); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "No forge is configured") {
			t.Errorf("GET %s without a forge = %d %s", target, resp.StatusCode, body)
		}
	}
	if body := readBody(t, serve(s, request(http.MethodGet, "/", testHost, nil))); strings.Contains(body, `href="/login"`) {
		t.Error("the top bar offers a login without a forge")
	}
}

func TestALoginNeedsItsServices(t *testing.T) {
	fake, _ := forgetest.Scenario("github", false)
	store := &identitytest.Memory{}
	login := identity.NewLogin(fake, identity.Policy{}, store, rand.Reader, time.Now)
	base := Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicHost: testHost, Scheme: "https",
		Ready: func(context.Context) error { return nil }, Sessions: &sessiontest.Memory{}, Login: login,
		Accounts: store, Workspaces: identity.NewWorkspaces(store, rand.Reader, time.Now), Permissions: &forgotten{},
	}
	for name, drop := range map[string]func(o *Options){
		"Accounts":    func(o *Options) { o.Accounts = nil },
		"Workspaces":  func(o *Options) { o.Workspaces = nil },
		"Permissions": func(o *Options) { o.Permissions = nil },
	} {
		o := base
		drop(&o)
		if _, err := New(o); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("New without %s = %v", name, err)
		}
	}
	if _, err := New(base); err != nil {
		t.Errorf("New with every service = %v", err)
	}
}

func TestRequireAccount(t *testing.T) {
	l := newLoginServer(t)
	handle(l.app, "GET /private", l.requireAccount(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeText(w, http.StatusOK, "private")
	})))
	b := l.browser(t)
	if got := location(t, b.do(http.MethodGet, "/private?x=1", nil), http.StatusSeeOther); got != "/login?return=%2Fprivate%3Fx%3D1" {
		t.Errorf("an anonymous request went to %q", got)
	}
	signed := l.signUp(t, "1001", "alice")
	if resp := signed.do(http.MethodGet, "/private", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a request with an account = %d", resp.StatusCode)
	}
}
