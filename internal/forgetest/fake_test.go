package forgetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
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
