package forgetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/repository"
)

func TestFake(t *testing.T) {
	Suite(t, func(*testing.T) Fixture {
		_, fx := Scenario("fake", false)
		return fx
	})
}

// TestBare runs the suite on the fake without its optional capabilities: the
// required port is enough, and a missing capability is a normal state.
func TestBare(t *testing.T) {
	_, fx := Scenario("fake", true)
	if _, ok := identity.MembershipsOf(fx.Forge); ok {
		t.Fatal("the bare forge has the membership capability")
	}
	if _, ok := identity.MembershipsOf(New("x", time.Now)); !ok {
		t.Fatal("the fake lacks the membership capability")
	}
	if _, ok := fx.Forge.(repository.Reach); ok {
		t.Fatal("the bare forge has the reach capability")
	}
	if _, ok := fx.Forge.(job.Clone); ok {
		t.Fatal("the bare forge has the clone capability")
	}
	Suite(t, func(*testing.T) Fixture {
		_, fx := Scenario("fake", true)
		return fx
	})
}

func TestApproveRefusesAnIncompleteURL(t *testing.T) {
	f, _ := Scenario("fake", false)
	for _, u := range []string{"https://x/?state=s", "https://x/?state=s&code_challenge=c", "https://x/?code_challenge=c&code_challenge_method=S256", ":"} {
		if _, _, err := f.Approve("1001", u); err == nil {
			t.Errorf("Approve(%q) accepted", u)
		}
	}
	if _, _, err := f.Approve("nobody", f.AuthorizeURL("s", "c")); err == nil {
		t.Error("an unknown person approved")
	}
}

func TestCodesExpire(t *testing.T) {
	f := New("fake", func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) })
	f.AddPerson(Person{ID: "1", Login: "a"})
	v := "verifier-verifier-verifier-verifier-verifier"
	code, _, err := f.Approve("1", f.AuthorizeURL("s", identity.Challenge(v)))
	if err != nil {
		t.Fatal(err)
	}
	f.Advance(CodeLifetime)
	if _, _, err := f.Exchange(context.Background(), code, v); !errors.Is(err, identity.ErrInvalidCode) {
		t.Errorf("Exchange of an expired code = %v, want ErrInvalidCode", err)
	}
}

// TestARefreshTokenServesOnce: the fake refuses a used refresh token, as
// GitHub does; the suite allows a forge that accepts it.
func TestARefreshTokenServesOnce(t *testing.T) {
	f, fx := Scenario("fake", false)
	ctx := context.Background()
	v, _ := identity.NewVerifier(stubReader{})
	code, _ := fx.Approve(t, f.AuthorizeURL("s", identity.Challenge(v)))
	_, tokens, err := f.Exchange(ctx, code, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(ctx, tokens); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(ctx, tokens); !errors.Is(err, identity.ErrRevoked) {
		t.Errorf("second use of a refresh token = %v, want ErrRevoked", err)
	}
}

// TestRefreshKeepsSomeoneElsesAccess refreshes Alice's refresh token paired
// with Bob's access token: Alice gets new tokens, and Bob keeps his access.
func TestRefreshKeepsSomeoneElsesAccess(t *testing.T) {
	f, _ := Scenario("fake", false)
	ctx := context.Background()
	loginAs := func(person string) identity.Tokens {
		t.Helper()
		v, _ := identity.NewVerifier(stubReader{})
		code, _, err := f.Approve(person, f.AuthorizeURL("s", identity.Challenge(v)))
		if err != nil {
			t.Fatal(err)
		}
		_, tokens, err := f.Exchange(ctx, code, v)
		if err != nil {
			t.Fatal(err)
		}
		return tokens
	}
	alice, bob := loginAs("1001"), loginAs("1002")
	if _, err := f.Refresh(ctx, identity.Tokens{Access: bob.Access, Refresh: alice.Refresh}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := f.Repositories(ctx, bob); err != nil {
		t.Errorf("Bob's access after Alice's refresh: %v", err)
	}
}

// TestAdvanceIsSafeForConcurrentUse moves the clock while the fake reads it
// to check tokens: go test -race reports a clock written outside the lock.
func TestAdvanceIsSafeForConcurrentUse(t *testing.T) {
	f, fx := Scenario("fake", false)
	ctx := context.Background()
	v, _ := identity.NewVerifier(stubReader{})
	code, _ := fx.Approve(t, f.AuthorizeURL("s", identity.Challenge(v)))
	_, tokens, err := f.Exchange(ctx, code, v)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			f.Advance(time.Second)
		}
	}()
	for range 100 {
		if _, err := f.Repositories(ctx, tokens); err != nil {
			t.Fatal(err)
		}
	}
	<-done
}

type stubReader struct{}

func (stubReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 7
	}
	return len(p), nil
}

func TestRenameAndInstallRepositories(t *testing.T) {
	ctx := context.Background()
	f, fx := Scenario("fake", false)
	code, _ := fx.Approve(t, f.AuthorizeURL("s", identity.Challenge("verifier-0123456789-0123456789-0123456789")))
	_, tokens, err := f.Exchange(ctx, code, "verifier-0123456789-0123456789-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if !f.RenameRepository("2001", "acme/backend") || f.RenameRepository("9999", "x/y") {
		t.Fatal("RenameRepository told the wrong existence")
	}
	repos, err := f.Repositories(ctx, tokens)
	if err != nil || len(repos) != 2 || repos[0].ID != "2001" || repos[0].FullName != "acme/backend" {
		t.Errorf("Repositories after a rename = %+v, %v", repos, err)
	}
	if !f.SetInstalled("2001", false) || f.SetInstalled("9999", true) {
		t.Fatal("SetInstalled told the wrong existence")
	}
	if p, err := f.Permission(ctx, tokens, identity.Repository{Forge: "fake", ID: "2001"}); err != nil || p != identity.PermissionNone {
		t.Errorf("Permission once uninstalled = %v, %v", p, err)
	}
	if got, err := f.Reached(ctx); err != nil || len(got) != 2 || got[0] != "2002" || got[1] != "2003" {
		t.Errorf("Reached once uninstalled = %v, %v; want 2002 and 2003", got, err)
	}
}

func TestFakeCommentsKeepTheOldest(t *testing.T) {
	ctx := context.Background()
	f, _ := Scenario("fake", false)
	first, _ := f.PublishComment(ctx, "2001", 1, "a")
	// A second comment of Raun, as a crash between a listing and a write
	// could leave: the oldest is the one updated, the other left alone.
	f.mu.Lock()
	f.comments["2001#1"] = append(f.comments["2001#1"], PublishedComment{ID: "late", Raun: true, Body: Marker + "\nb"})
	f.mu.Unlock()
	if id, _ := f.PublishComment(ctx, "2001", 1, "c"); id != first {
		t.Errorf("updated %q, want the oldest %q", id, first)
	}
	if got := f.Comments("2001", 1); got[1].Body != Marker+"\nb" {
		t.Errorf("the later comment changed: %+v", got)
	}
	// The marker on another line is no marker.
	f.mu.Lock()
	f.comments["2001#2"] = []PublishedComment{{ID: "x", Raun: true, Body: "text\n" + Marker}}
	f.mu.Unlock()
	if id, _ := f.PublishComment(ctx, "2001", 2, "c"); id == "x" {
		t.Error("a comment with the marker on its second line was updated")
	}
	if _, ok := f.Bare().(Statuses); ok {
		t.Error("the bare forge publishes statuses")
	}
	if _, ok := f.Bare().(Comments); ok {
		t.Error("the bare forge publishes comments")
	}
}

func TestEscapeText(t *testing.T) {
	for in, want := range map[string]string{
		"plain words":           "plain words",
		"a\nb":                  "a\nb",
		"<b>&</b>":              "&lt;b&gt;&amp;&lt;/b&gt;",
		"@alice":                "&#64;alice",
		"#1 *x* _y_ `z` [a](b)": `\#1 \*x\* \_y\_ \` + "`" + `z\` + "`" + ` \[a\]\(b\)`,
		"1. item":               `1\. item`,
		"a\r\nb":                "a\nb",
	} {
		if got := EscapeText(in, MaxComment); got != want {
			t.Errorf("EscapeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarked(t *testing.T) {
	for body, want := range map[string]bool{
		Marker:               true,
		Marker + "\nx":       true,
		Marker + "\r\nx":     true,
		Marker + "x":         false,
		" " + Marker + "\nx": false,
		"x\n" + Marker:       false,
		"<!-- RAUN -->\nx":   false,
	} {
		if got := Marked(body); got != want {
			t.Errorf("Marked(%q) = %v", body, got)
		}
	}
}
