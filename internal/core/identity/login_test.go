package identity_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/kvitrvn/raun/internal/core/identity"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
)

func TestNewLoginState(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	state, row, err := identity.NewLoginState(rand.Reader, now, "session-hash", "https://evil.example/")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil || len(raw) != 32 {
		t.Errorf("state %q decodes to %d bytes", state, len(raw))
	}
	if h, err := identity.HashState(state); err != nil || h != row.Hash || strings.Contains(row.Hash, state) {
		t.Errorf("HashState = %q, %v; row %q", h, err, row.Hash)
	}
	if len(row.Verifier) != 43 || row.Session != "session-hash" || row.ReturnPath != "/" || !row.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Errorf("row = %+v", row)
	}
	for _, bad := range []string{"", "x", state + "A", state + "=", base64.RawURLEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := identity.HashState(bad); !errors.Is(err, identity.ErrInvalidState) {
			t.Errorf("HashState(%q) = %v", bad, err)
		}
	}
	if _, _, err := identity.NewLoginState(bytes.NewReader(make([]byte, 40)), now, "s", "/"); err == nil {
		t.Error("NewLoginState with too little randomness succeeded")
	}
}

func TestSafeReturnPath(t *testing.T) {
	for p, want := range map[string]string{
		"/":                     "/",
		"/acme/api":             "/acme/api",
		"/acme/api?tab=items#x": "/acme/api?tab=items#x",
		"/a%2F%2Fb":             "/a%2F%2Fb",
		"":                      "/",
		"acme":                  "/",
		"https://evil.example/": "/",
		"//evil.example":        "/",
		"/\\evil.example":       "/",
		"/a\\b":                 "/",
		"/a\tb":                 "/",
		"/a\nb":                 "/",
		"/a\x7fb":               "/",
		"///evil.example":       "/",
		"javascript:alert(1)":   "/",
		"/%zz":                  "/",
		"\\\\evil.example":      "/",
		" /acme":                "/",
	} {
		if got := identity.SafeReturnPath(p); got != want {
			t.Errorf("SafeReturnPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// loginOf is a login of the stub forge on a memory store, at the time of c.
type loginOf struct {
	forge *stubForge
	store *identitytest.Memory
	login *identity.Login
	now   time.Time
}

func newLoginOf(t *testing.T, policy identity.Policy) *loginOf {
	t.Helper()
	l := &loginOf{store: &identitytest.Memory{}, now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return l.now }
	l.forge = newStubForge(clock)
	l.login = identity.NewLogin(l.forge, policy, l.store, rand.Reader, clock)
	return l
}

// callback runs Begin for session, login approving, and returns the state and
// code the forge sends back.
func (l *loginOf) callback(t *testing.T, session, who, returnPath string) (state, code string) {
	t.Helper()
	u, err := l.login.Begin(context.Background(), session, returnPath)
	if err != nil {
		t.Fatal(err)
	}
	code, state = l.forge.approve(who, u)
	return state, code
}

func everyone() identity.Policy {
	return identity.Policy{Users: []identity.Entry{{"stub", "1001"}, {"stub", "1002"}}}
}

func TestLoginSignsUpThenLogsIn(t *testing.T) {
	ctx := context.Background()
	l := newLoginOf(t, everyone())
	state, code := l.callback(t, "s1", "alice", "/acme/api")
	out, err := l.login.Complete(ctx, "s1", state, code)
	if err != nil || !out.Signup || out.ReturnPath != "/acme/api" {
		t.Fatalf("Complete = %+v, %v; want a sign-up", out, err)
	}
	sg, err := l.login.Signup(ctx, "s1")
	if err != nil || sg.Proposed != "alice" || sg.Identity.Key() != "stub:1001" {
		t.Fatalf("Signup = %+v, %v", sg, err)
	}
	if _, err := l.login.Signup(ctx, "s2"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("Signup of another session = %v", err)
	}
	for name, want := range map[string]error{"Admin": identity.ErrReservedName, "a--b": identity.ErrInvalidName, "": identity.ErrInvalidName} {
		if _, _, err := l.login.FinishSignup(ctx, "s1", name); !errors.Is(err, want) {
			t.Errorf("FinishSignup(%q) = %v, want %v", name, err, want)
		}
	}
	account, back, err := l.login.FinishSignup(ctx, "s1", "  Alice-M ")
	if err != nil || account.Name != "alice-m" || len(account.ID) != 32 || back != "/acme/api" {
		t.Fatalf("FinishSignup = %+v, %q, %v", account, back, err)
	}
	if _, _, err := l.login.FinishSignup(ctx, "s1", "alice-again"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("a second FinishSignup = %v, want identity.ErrNotFound", err)
	}

	// Alice logs in again, from another browser, renamed on the forge.
	l.forge.people["alice"] = identity.Identity{Forge: "stub", ID: "1001", Login: "alice-renamed"}
	state, code = l.callback(t, "s2", "alice", "")
	out, err = l.login.Complete(ctx, "s2", state, code)
	if err != nil || out.Signup || out.Account != account || out.ReturnPath != "/" {
		t.Errorf("Complete of a known identity = %+v, %v; want the account %+v", out, err, account)
	}
	if tk, err := l.store.Tokens(ctx, "stub:1001"); err != nil || tk.Access == "" {
		t.Errorf("the login did not store the tokens: %v", err)
	}
}

func TestLoginRefusesABadCallback(t *testing.T) {
	ctx := context.Background()
	l := newLoginOf(t, everyone())
	state, code := l.callback(t, "s1", "alice", "/")
	forged := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for name, call := range map[string]func() error{
		"a forged state":    func() error { _, err := l.login.Complete(ctx, "s1", forged, code); return err },
		"a malformed state": func() error { _, err := l.login.Complete(ctx, "s1", "nope", code); return err },
	} {
		if err := call(); !errors.Is(err, identity.ErrInvalidState) {
			t.Errorf("%s: Complete = %v, want identity.ErrInvalidState", name, err)
		}
	}
	// The state of s1 sent back by another browser serves nobody, not even
	// s1 afterwards.
	if _, err := l.login.Complete(ctx, "s2", state, code); !errors.Is(err, identity.ErrInvalidState) {
		t.Errorf("Complete from another session = %v", err)
	}
	if _, err := l.login.Complete(ctx, "s1", state, code); !errors.Is(err, identity.ErrInvalidState) {
		t.Errorf("Complete after a refused attempt = %v", err)
	}

	// A replayed callback: the state served once.
	state, code = l.callback(t, "s1", "alice", "/")
	if _, err := l.login.Complete(ctx, "s1", state, code); err != nil {
		t.Fatal(err)
	}
	if _, err := l.login.Complete(ctx, "s1", state, code); !errors.Is(err, identity.ErrInvalidState) {
		t.Errorf("a replayed callback = %v, want identity.ErrInvalidState", err)
	}

	// A code the forge refuses spends the state all the same.
	state, _ = l.callback(t, "s1", "alice", "/")
	if _, err := l.login.Complete(ctx, "s1", state, "not-a-code"); !errors.Is(err, identity.ErrInvalidCode) {
		t.Errorf("Complete with a bad code = %v", err)
	}
	if _, err := l.login.Complete(ctx, "s1", state, "not-a-code"); !errors.Is(err, identity.ErrInvalidState) {
		t.Errorf("the state outlived a refused code: %v", err)
	}

	// An expired state.
	state, code = l.callback(t, "s1", "alice", "/")
	l.now = l.now.Add(identity.LoginStateLifetime)
	if _, err := l.login.Complete(ctx, "s1", state, code); !errors.Is(err, identity.ErrInvalidState) {
		t.Errorf("Complete once the state expired = %v", err)
	}
}

func TestLoginAppliesTheRuleAtEveryLogin(t *testing.T) {
	ctx := context.Background()
	l := newLoginOf(t, identity.Policy{Orgs: []identity.Entry{{"stub", "3001"}}})
	state, code := l.callback(t, "s1", "bob", "/")
	if _, err := l.login.Complete(ctx, "s1", state, code); !errors.Is(err, identity.ErrNotAllowed) {
		t.Errorf("Complete for someone outside the rule = %v, want identity.ErrNotAllowed", err)
	}
	if _, err := l.login.Signup(ctx, "s1"); !errors.Is(err, identity.ErrNotFound) {
		t.Error("a refused person got a sign-up")
	}

	state, code = l.callback(t, "s1", "alice", "/")
	if _, err := l.login.Complete(ctx, "s1", state, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.login.FinishSignup(ctx, "s1", "alice"); err != nil {
		t.Fatal(err)
	}
	// Alice leaves the organization: she can no longer log in.
	l.forge.members["3001"] = nil
	state, code = l.callback(t, "s2", "alice", "/")
	if _, err := l.login.Complete(ctx, "s2", state, code); !errors.Is(err, identity.ErrNotAllowed) {
		t.Errorf("Complete after leaving the organization = %v, want identity.ErrNotAllowed", err)
	}
}

func TestFinishSignupWithATakenName(t *testing.T) {
	ctx := context.Background()
	l := newLoginOf(t, everyone())
	for _, who := range []string{"alice", "bob"} {
		state, code := l.callback(t, "s-"+who, who, "/")
		if _, err := l.login.Complete(ctx, "s-"+who, state, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := l.login.FinishSignup(ctx, "s-alice", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.login.FinishSignup(ctx, "s-bob", "alice"); !errors.Is(err, identity.ErrNameTaken) {
		t.Errorf("FinishSignup with a taken name = %v", err)
	}
	if a, _, err := l.login.FinishSignup(ctx, "s-bob", "bob"); err != nil || a.Name != "bob" {
		t.Errorf("FinishSignup after a taken name = %+v, %v", a, err)
	}
}
