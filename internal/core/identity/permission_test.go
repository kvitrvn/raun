package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
)

// permissionsOf is a permission service on the stub forge where Alice (a1)
// and Bob (a2) have accounts, at the time s.now.
type permissionsOf struct {
	*tokensOf
	permissions *identity.PermissionService
}

func newPermissionsOf(t *testing.T) *permissionsOf {
	t.Helper()
	s := newTokensOf(t)
	ctx := context.Background()
	bob := s.forge.people["bob"]
	if err := s.store.PutSignup(ctx, identity.Signup{Session: "s2", Identity: bob, Tokens: s.forge.issue(bob.ID), ExpiresAt: s.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CompleteSignup(ctx, "s2", identity.Account{ID: "a2", Name: "bob"}, s.now); err != nil {
		t.Fatal(err)
	}
	return &permissionsOf{tokensOf: s, permissions: identity.NewPermissionService(s.forge, s.service, s.store, func() time.Time { return s.now })}
}

func api() identity.Repository {
	return identity.Repository{Forge: "stub", ID: "2001", FullName: "acme/api"}
}

func (p *permissionsOf) check(t *testing.T, account string, repo identity.Repository, want identity.Permission, wantErr error) {
	t.Helper()
	got, err := p.permissions.Permission(context.Background(), account, repo)
	if got != want || (wantErr == nil && err != nil) || (wantErr != nil && !errors.Is(err, wantErr)) {
		t.Errorf("Permission(%s, %s) = %s, %v; want %s, %v", account, repo.ID, got, err, want, wantErr)
	}
}

func TestPermissionsComeFromTheForge(t *testing.T) {
	for _, p := range []identity.Permission{identity.PermissionDecide, identity.PermissionView, identity.PermissionNone} {
		s := newPermissionsOf(t)
		if p != identity.PermissionNone {
			s.forge.grant("1001", "2001", p)
		}
		s.check(t, "a1", api(), p, nil)
	}
}

func TestPermissionsAreKeptFiveMinutes(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.now = s.now.Add(identity.PermissionLifetime - time.Second)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if n := s.forge.calls(); n != 1 {
		t.Errorf("%d calls to the forge within the lifetime, want 1", n)
	}
	// A read never extends the entry: the lifetime counts from the answer.
	s.now = s.now.Add(time.Second)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if n := s.forge.calls(); n != 2 {
		t.Errorf("%d calls to the forge once the lifetime passed, want 2", n)
	}
}

// TestAccessRemovedOnTheForge: a right taken away on the forge is refused
// once the lifetime of the kept one has passed.
func TestAccessRemovedOnTheForge(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.forge.grant("1001", "2001", identity.PermissionNone)
	s.now = s.now.Add(identity.PermissionLifetime - time.Second)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.now = s.now.Add(time.Second)
	s.check(t, "a1", api(), identity.PermissionNone, nil)
}

func TestAForgeThatCannotAnswer(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionView)
	s.check(t, "a1", api(), identity.PermissionView, nil)
	s.forge.setErr(identity.ErrUnavailable)
	// A kept permission still holds.
	s.check(t, "a1", api(), identity.PermissionView, nil)
	// Past its lifetime, access is refused, and a failure extends nothing.
	s.now = s.now.Add(identity.PermissionLifetime)
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrUnavailable)
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrUnavailable)
	// No kept permission at all: refused.
	s.check(t, "a2", api(), identity.PermissionNone, identity.ErrUnavailable)
	s.forge.setErr(nil)
	s.check(t, "a1", api(), identity.PermissionView, nil)
}

func TestARefusedToken(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	// A token refused once is refreshed, and the permission given.
	s.forge.refuseOnce = true
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if s.forge.refreshes != 1 {
		t.Errorf("%d refreshes after a refused token, want 1", s.forge.refreshes)
	}
	// Refused even refreshed: no access, and nothing kept for the
	// repository.
	s.now = s.now.Add(identity.PermissionLifetime)
	s.forge.setErr(identity.ErrUnauthorized)
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrUnauthorized)
	s.forge.setErr(nil)
	before := s.forge.calls()
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if s.forge.calls() != before+1 {
		t.Error("the forge was not asked again after a refusal")
	}
	// A caller that the forge refused drops the permission too.
	s.permissions.Refused("a1", api())
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if s.forge.calls() != before+2 {
		t.Error("Refused did not drop the kept permission")
	}
}

func TestLostTokensDropThePermissions(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.grant("1002", "2001", identity.PermissionView)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.check(t, "a2", api(), identity.PermissionView, nil)
	// Alice's authorization is revoked on the forge: her tokens are lost.
	s.forge.refuseOnce = true
	s.forge.refreshErr = identity.ErrRevoked
	s.now = s.now.Add(identity.PermissionLifetime)
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrRevoked)
	s.forge.refreshErr = nil
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrNotFound)
	// Bob's permission is asked again since it expired, and holds.
	s.check(t, "a2", api(), identity.PermissionView, nil)
}

func TestRevokedTokensDropKeptPermissions(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.grant("1001", "2002", identity.PermissionView)
	other := identity.Repository{Forge: "stub", ID: "2002"}
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.check(t, "a1", other, identity.PermissionView, nil)
	// Another call of the token service finds the refresh refused for
	// good: every kept permission of the identity goes at once.
	s.now = s.now.Add(8 * time.Hour)
	s.forge.refreshErr = identity.ErrRevoked
	if err := s.service.With(context.Background(), "stub:1001", ok); !errors.Is(err, identity.ErrRevoked) {
		t.Fatalf("With = %v", err)
	}
	s.now = s.now.Add(-8*time.Hour + time.Second)
	s.check(t, "a1", other, identity.PermissionNone, identity.ErrNotFound)
}

func TestForget(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.grant("1002", "2001", identity.PermissionView)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.check(t, "a2", api(), identity.PermissionView, nil)
	s.permissions.Forget("a1")
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.check(t, "a2", api(), identity.PermissionView, nil)
	if n := s.forge.calls(); n != 3 {
		t.Errorf("%d calls to the forge, want 3: Forget drops one account only", n)
	}
}

func TestPermissionsAreKeptPerAccountAndRepository(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.grant("1001", "2002", identity.PermissionView)
	s.forge.grant("1002", "2001", identity.PermissionView)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.check(t, "a1", identity.Repository{Forge: "stub", ID: "2002"}, identity.PermissionView, nil)
	s.check(t, "a2", api(), identity.PermissionView, nil)
	// A renamed repository keeps its identifier, and its entry.
	renamed := api()
	renamed.FullName = "acme/api-v2"
	s.check(t, "a1", renamed, identity.PermissionDecide, nil)
	if n := s.forge.calls(); n != 3 {
		t.Errorf("%d calls to the forge, want one per account and repository", n)
	}
}

// twoIdentities is a store where every account has two identities on the
// stub forge.
type twoIdentities struct{ *identitytest.Memory }

func (twoIdentities) IdentitiesOf(context.Context, string) ([]identity.Identity, error) {
	return []identity.Identity{{Forge: "stub", ID: "1001"}, {Forge: "stub", ID: "1009"}}, nil
}

func TestPermissionsNeedOneIdentity(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	for name, tc := range map[string]struct {
		account string
		repo    identity.Repository
	}{
		"no account":                {"", api()},
		"an account without tokens": {"nobody", api()},
		"no repository":             {"a1", identity.Repository{Forge: "stub"}},
		"a repository elsewhere":    {"a1", identity.Repository{Forge: "other", ID: "2001"}},
	} {
		if got, err := s.permissions.Permission(context.Background(), tc.account, tc.repo); got != identity.PermissionNone || !errors.Is(err, identity.ErrNoIdentity) {
			t.Errorf("%s: Permission = %s, %v; want a refusal", name, got, err)
		}
	}
	two := identity.NewPermissionService(s.forge, s.service, twoIdentities{s.store}, func() time.Time { return s.now })
	if got, err := two.Permission(context.Background(), "a1", api()); got != identity.PermissionNone || !errors.Is(err, identity.ErrNoIdentity) {
		t.Errorf("two identities on the forge: Permission = %s, %v; want a refusal", got, err)
	}
}

// TestAnAnswerAcrossAForget: a forge answer that arrives after the account
// was forgotten grants nothing and is not kept.
func TestAnAnswerAcrossAForget(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	wait := make(chan struct{})
	s.forge.mu.Lock()
	s.forge.permissionWait = wait
	s.forge.mu.Unlock()
	type answer struct {
		p   identity.Permission
		err error
	}
	done := make(chan answer)
	go func() {
		p, err := s.permissions.Permission(context.Background(), "a1", api())
		done <- answer{p, err}
	}()
	<-wait // the forge is answering
	s.permissions.Forget("a1")
	s.forge.mu.Lock()
	s.forge.permissionWait = nil
	s.forge.mu.Unlock()
	wait <- struct{}{}
	got := <-done
	if got.p != identity.PermissionNone || !errors.Is(got.err, identity.ErrUnavailable) {
		t.Errorf("an answer across a Forget = %s, %v; want a refusal to try again", got.p, got.err)
	}
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if n := s.forge.calls(); n != 2 {
		t.Errorf("%d calls to the forge: the answer across the Forget was kept", n)
	}
}

func TestPermissionsUnderConcurrency(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.grant("1002", "2001", identity.PermissionView)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			account := []string{"a1", "a2"}[i%2]
			// A Forget of another caller may turn an answer into one to
			// try again; nothing else fails.
			if _, err := s.permissions.Permission(context.Background(), account, api()); err != nil && !errors.Is(err, identity.ErrUnavailable) {
				t.Error(err)
			}
			if i%5 == 0 {
				s.permissions.Forget(account)
			}
		}()
	}
	wg.Wait()
}
