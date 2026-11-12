package forgetest

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Fixture is a forge set up with the scenario of the contract suite:
//
//   - a person, Alice, who belongs to the organization Org and not to OtherOrg;
//   - acme/api, where she decides, and acme/web, where she views;
//   - Unreachable: acme/secret, which she cannot see, and other/lib, on
//     which she has write access on the forge but which Raun cannot reach.
//
// An adapter builds it from recorded responses; New builds it from the fake.
type Fixture struct {
	Forge    identity.Forge
	Alice    string
	Org      string
	OtherOrg string
	// Unreachable are the two repositories Alice must not reach.
	Unreachable []identity.Repository
	// Approve is Alice approving Raun at url: the code and the state the
	// forge sends back.
	Approve func(t *testing.T, url string) (code, state string)
	// ExpireAccess makes the forge refuse the access tokens it gave, as
	// time passes, while their refresh tokens stay valid.
	ExpireAccess func(t *testing.T)
	// Revoke makes the forge refuse Alice's authorization for good.
	Revoke func(t *testing.T)
	// SetDown makes the forge unavailable, or available again.
	SetDown func(t *testing.T, down bool)
	// RenameAlice gives Alice a new login on the forge.
	RenameAlice func(t *testing.T, login string)
}

// Scenario returns the fake forge called name set up with the scenario of
// the suite, and its fixture. bare drops the optional capabilities.
func Scenario(name string, bare bool) (*Fake, Fixture) {
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	f := New(name, now)
	f.AddPerson(Person{ID: "1001", Login: "alice", Name: "Alice Martin"})
	f.AddPerson(Person{ID: "1002", Login: "bob", Name: "Bob Durand"})
	repos := []struct {
		r         identity.Repository
		installed bool
		alice     identity.Permission
	}{
		{identity.Repository{ID: "2001", FullName: "acme/api", DefaultBranch: "main", Private: true}, true, identity.PermissionDecide},
		{identity.Repository{ID: "2002", FullName: "acme/web", DefaultBranch: "main"}, true, identity.PermissionView},
		{identity.Repository{ID: "2003", FullName: "acme/secret", DefaultBranch: "main", Private: true}, true, ""},
		{identity.Repository{ID: "2004", FullName: "other/lib", DefaultBranch: "trunk"}, false, identity.PermissionDecide},
	}
	var unreachable []identity.Repository
	for _, r := range repos {
		f.AddRepository(r.r, r.installed)
		if r.alice != "" {
			f.Grant("1001", r.r.ID, r.alice)
		}
		f.Grant("1002", r.r.ID, identity.PermissionDecide)
		if r.r.FullName == "acme/secret" || r.r.FullName == "other/lib" {
			rr := r.r
			rr.Forge = name
			unreachable = append(unreachable, rr)
		}
	}
	f.AddMember("3001", "1001")
	f.AddMember("3002", "1002")

	var forge identity.Forge = f
	if bare {
		forge = f.Bare()
	}
	return f, Fixture{
		Forge:       forge,
		Alice:       "1001",
		Org:         "3001",
		OtherOrg:    "3002",
		Unreachable: unreachable,
		Approve: func(t *testing.T, url string) (string, string) {
			t.Helper()
			code, state, err := f.Approve("1001", url)
			if err != nil {
				t.Fatal(err)
			}
			return code, state
		},
		ExpireAccess: func(*testing.T) { clock = clock.Add(AccessLifetime + time.Second) },
		Revoke:       func(*testing.T) { f.Revoke("1001") },
		SetDown:      func(_ *testing.T, down bool) { f.SetDown(down) },
		RenameAlice: func(t *testing.T, login string) {
			t.Helper()
			if !f.Rename("1001", login) {
				t.Fatal("Alice is unknown")
			}
		},
	}
}

// noToken fails the test if err holds the value of a token: an error is
// logged, a token never is.
func noToken(t *testing.T, err error, tokens identity.Tokens) {
	t.Helper()
	if err == nil {
		return
	}
	for _, v := range []string{tokens.Access.Reveal(), tokens.Refresh.Reveal()} {
		if v != "" && strings.Contains(err.Error(), v) {
			t.Errorf("error %q holds a token", err)
		}
	}
}

// Suite runs the contract of the forge port on the fixtures newFixture
// builds, one per case.
func Suite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()

	login := func(t *testing.T, fx Fixture) (identity.Identity, identity.Tokens) {
		t.Helper()
		verifier, err := identity.NewVerifier(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		state := "state-of-the-suite"
		code, back := fx.Approve(t, fx.Forge.AuthorizeURL(state, identity.Challenge(verifier)))
		if back != state {
			t.Fatalf("the forge sent back state %q, want %q", back, state)
		}
		id, tokens, err := fx.Forge.Exchange(ctx, code, verifier)
		if err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		return id, tokens
	}

	t.Run("login", func(t *testing.T) {
		fx := newFixture(t)
		id, tokens := login(t, fx)
		if id.Forge != fx.Forge.Name() || id.ID != fx.Alice || id.Login == "" {
			t.Errorf("identity = %+v", id)
		}
		if tokens.Access.Reveal() == "" || tokens.Refresh.Reveal() == "" || !tokens.AccessExpiry.Before(tokens.RefreshExpiry) {
			t.Errorf("tokens have expiries %v and %v", tokens.AccessExpiry, tokens.RefreshExpiry)
		}
	})

	t.Run("a code serves once and needs its verifier", func(t *testing.T) {
		fx := newFixture(t)
		verifier, _ := identity.NewVerifier(rand.Reader)
		other, _ := identity.NewVerifier(rand.Reader)
		url := fx.Forge.AuthorizeURL("s", identity.Challenge(verifier))
		code, _ := fx.Approve(t, url)
		if _, _, err := fx.Forge.Exchange(ctx, code, other); !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("Exchange with another verifier = %v, want ErrInvalidCode", err)
		}
		code, _ = fx.Approve(t, url)
		if _, _, err := fx.Forge.Exchange(ctx, code, verifier); err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		if _, _, err := fx.Forge.Exchange(ctx, code, verifier); !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("second Exchange of a code = %v, want ErrInvalidCode", err)
		}
		if _, _, err := fx.Forge.Exchange(ctx, "not-a-code", verifier); !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("Exchange of an unknown code = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("repositories", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repos, err := fx.Forge.Repositories(ctx, tokens)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range repos {
			names = append(names, r.FullName)
			if r.Forge != fx.Forge.Name() || r.ID == "" || r.DefaultBranch == "" {
				t.Errorf("repository %+v", r)
			}
		}
		if !slices.Equal(names, []string{"acme/api", "acme/web"}) {
			t.Errorf("repositories = %v, want acme/api and acme/web in order", names)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repos, err := fx.Forge.Repositories(ctx, tokens)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]identity.Permission{"acme/api": identity.PermissionDecide, "acme/web": identity.PermissionView}
		for _, r := range append(repos, fx.Unreachable...) {
			got, err := fx.Forge.Permission(ctx, tokens, r)
			if err != nil {
				t.Fatalf("Permission(%s): %v", r.FullName, err)
			}
			w, ok := want[r.FullName]
			if !ok {
				w = identity.PermissionNone
			}
			if got != w {
				t.Errorf("Permission(%s) = %s, want %s", r.FullName, got, w)
			}
		}
	})

	t.Run("an expired access token is refused, then refreshed", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		fx.ExpireAccess(t)
		_, err := fx.Forge.Repositories(ctx, tokens)
		if !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("Repositories with an expired token = %v, want ErrUnauthorized", err)
		}
		noToken(t, err, tokens)
		fresh, err := fx.Forge.Refresh(ctx, tokens)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if fresh.Access.Reveal() == tokens.Access.Reveal() {
			t.Error("Refresh gave the same access token")
		}
		if _, err := fx.Forge.Repositories(ctx, fresh); err != nil {
			t.Errorf("Repositories after Refresh: %v", err)
		}
	})

	t.Run("a revoked authorization is refused for good", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		fx.Revoke(t)
		_, err := fx.Forge.Refresh(ctx, tokens)
		if !errors.Is(err, identity.ErrRevoked) {
			t.Errorf("Refresh after revocation = %v, want ErrRevoked", err)
		}
		noToken(t, err, tokens)
		if _, err := fx.Forge.Repositories(ctx, tokens); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("Repositories after revocation = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("an unavailable forge is a transient error", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		fx.SetDown(t, true)
		_, err := fx.Forge.Repositories(ctx, tokens)
		if !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Repositories while down = %v, want ErrUnavailable", err)
		}
		noToken(t, err, tokens)
		if _, err := fx.Forge.Refresh(ctx, tokens); !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Refresh while down = %v, want ErrUnavailable", err)
		}
		fx.SetDown(t, false)
		// A transient failure takes nothing away: the tokens still work.
		if _, err := fx.Forge.Repositories(ctx, tokens); err != nil {
			t.Errorf("Repositories once back: %v", err)
		}
	})

	t.Run("a renamed login keeps its identifier", func(t *testing.T) {
		fx := newFixture(t)
		before, _ := login(t, fx)
		fx.RenameAlice(t, "alice-renamed")
		after, _ := login(t, fx)
		if after.ID != before.ID || after.Login != "alice-renamed" || after.Key() != before.Key() {
			t.Errorf("after a rename: %+v, before %+v", after, before)
		}
	})

	t.Run("memberships, when the forge has them", func(t *testing.T) {
		fx := newFixture(t)
		m, ok := identity.MembershipsOf(fx.Forge)
		if !ok {
			t.Skip("the forge has no membership capability")
		}
		_, tokens := login(t, fx)
		if in, err := m.IsMember(ctx, tokens, fx.Org); err != nil || !in {
			t.Errorf("IsMember(Org) = %v, %v; want true", in, err)
		}
		if in, err := m.IsMember(ctx, tokens, fx.OtherOrg); err != nil || in {
			t.Errorf("IsMember(OtherOrg) = %v, %v; want false", in, err)
		}
	})
}
