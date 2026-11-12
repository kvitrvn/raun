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
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	f := New("fake", func() time.Time { return clock })
	f.AddPerson(Person{ID: "1", Login: "a"})
	v := "verifier-verifier-verifier-verifier-verifier"
	code, _, err := f.Approve("1", f.AuthorizeURL("s", identity.Challenge(v)))
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(CodeLifetime)
	if _, _, err := f.Exchange(context.Background(), code, v); !errors.Is(err, identity.ErrInvalidCode) {
		t.Errorf("Exchange of an expired code = %v, want ErrInvalidCode", err)
	}
}

func TestRefreshRotatesTheRefreshToken(t *testing.T) {
	_, fx := Scenario("fake", false)
	ctx := context.Background()
	v, _ := identity.NewVerifier(stubReader{})
	code, _ := fx.Approve(t, fx.Forge.AuthorizeURL("s", identity.Challenge(v)))
	_, tokens, err := fx.Forge.Exchange(ctx, code, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.Forge.Refresh(ctx, tokens); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.Forge.Refresh(ctx, tokens); !errors.Is(err, identity.ErrRevoked) {
		t.Errorf("second use of a refresh token = %v, want ErrRevoked", err)
	}
}

type stubReader struct{}

func (stubReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 7
	}
	return len(p), nil
}
