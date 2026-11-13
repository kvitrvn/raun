package sqlite

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// permissionsOnSQLite gives Alice an account on SQLite with the tokens the
// fake forge gave her, and returns the permission service over both, the
// fake, and a function that moves both clocks.
func permissionsOnSQLite(t *testing.T) (*identity.PermissionService, *forgetest.Fake, func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	fake, fx := forgetest.Scenario("github", false)
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var offset time.Duration
	now := func() time.Time { return start.Add(offset) }
	advance := func(d time.Duration) { offset += d; fake.Advance(d) }

	verifier, _ := identity.NewVerifier(rand.Reader)
	code, _ := fx.Approve(t, fake.AuthorizeURL("s", identity.Challenge(verifier)))
	alice, tokens, err := fake.Exchange(ctx, code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := openMigrated(t)
	a := s.Accounts(testKeyring(t, 1, 0))
	if err := a.PutSignup(ctx, identity.Signup{Session: "s1", Identity: alice, Tokens: tokens, ExpiresAt: now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteSignup(ctx, "s1", identity.Account{ID: strings.Repeat("a", 32), Name: "alice", CreatedAt: now()}, now()); err != nil {
		t.Fatal(err)
	}
	return identity.NewPermissionService(fake, identity.NewTokenService(fake, a, now), a, now), fake, advance
}

func apiRepository() identity.Repository {
	return identity.Repository{Forge: "github", ID: "2001", FullName: "acme/api"}
}

// TestRemovedAccessIsRefusedAfterTheLifetime: a permission removed on the
// forge is refused once the kept one has lived PermissionLifetime.
func TestRemovedAccessIsRefusedAfterTheLifetime(t *testing.T) {
	ctx := context.Background()
	p, fake, advance := permissionsOnSQLite(t)
	alice := strings.Repeat("a", 32)
	if got, err := p.Permission(ctx, alice, apiRepository()); err != nil || got != identity.PermissionDecide {
		t.Fatalf("Permission = %s, %v; want decide", got, err)
	}
	fake.Grant("1001", "2001", identity.PermissionNone)
	advance(identity.PermissionLifetime - time.Second)
	if got, _ := p.Permission(ctx, alice, apiRepository()); got != identity.PermissionDecide {
		t.Errorf("Permission within the lifetime = %s, want the kept decide", got)
	}
	advance(time.Second)
	if got, err := p.Permission(ctx, alice, apiRepository()); err != nil || got != identity.PermissionNone {
		t.Errorf("Permission after the lifetime = %s, %v; want none", got, err)
	}
}

func TestAForgeDownKeepsOnlyLivePermissions(t *testing.T) {
	ctx := context.Background()
	p, fake, advance := permissionsOnSQLite(t)
	alice := strings.Repeat("a", 32)
	if got, err := p.Permission(ctx, alice, apiRepository()); err != nil || got != identity.PermissionDecide {
		t.Fatalf("Permission = %s, %v", got, err)
	}
	fake.SetDown(true)
	if got, err := p.Permission(ctx, alice, apiRepository()); err != nil || got != identity.PermissionDecide {
		t.Errorf("Permission kept while the forge is down = %s, %v", got, err)
	}
	advance(identity.PermissionLifetime)
	if got, err := p.Permission(ctx, alice, apiRepository()); got != identity.PermissionNone || !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("Permission expired while the forge is down = %s, %v; want a refusal", got, err)
	}
	fake.SetDown(false)
	if got, err := p.Permission(ctx, alice, apiRepository()); err != nil || got != identity.PermissionDecide {
		t.Errorf("Permission once the forge is back = %s, %v", got, err)
	}
}
