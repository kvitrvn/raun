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

// listed lists the identities of every account, or fails with err.
type listed struct {
	ids []identity.Identity
	err error
}

func (l listed) IdentitiesOf(context.Context, string) ([]identity.Identity, error) {
	return l.ids, l.err
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
	two := identity.NewPermissionService(s.forge, s.service, listed{ids: []identity.Identity{{Forge: "stub", ID: "1001"}, {Forge: "stub", ID: "1009"}}}, func() time.Time { return s.now })
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

// TestTheLifetimeCountsFromTheAnswer: a forge slow to answer does not
// shorten the life of what it answered.
func TestTheLifetimeCountsFromTheAnswer(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	s.forge.during = func() { s.now = s.now.Add(2 * time.Minute) }
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	s.forge.during = nil
	s.now = s.now.Add(identity.PermissionLifetime - time.Second)
	s.check(t, "a1", api(), identity.PermissionDecide, nil)
	if n := s.forge.calls(); n != 1 {
		t.Errorf("%d calls to the forge: the entry lived less than its lifetime from the answer", n)
	}
}

// TestCallersShareOneQuestion: callers that miss the same entry together ask
// the forge once.
func TestCallersShareOneQuestion(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	wait := make(chan struct{})
	s.forge.mu.Lock()
	s.forge.permissionWait = wait
	s.forge.mu.Unlock()
	var wg sync.WaitGroup
	got := make([]identity.Permission, 6)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = s.permissions.Permission(context.Background(), "a1", api())
		}()
	}
	<-wait // one caller is asking the forge
	time.Sleep(20 * time.Millisecond)
	s.forge.mu.Lock()
	s.forge.permissionWait = nil
	s.forge.mu.Unlock()
	wait <- struct{}{}
	wg.Wait()
	if n := s.forge.calls(); n != 1 {
		t.Errorf("%d calls to the forge for one entry, want 1", n)
	}
	for _, p := range got {
		if p != identity.PermissionDecide {
			t.Fatalf("permissions = %v, want decide for every caller", got)
		}
	}
}

// TestARefusalOfOneRepositoryKeepsTheOthers: Refused on one repository does
// not spoil an answer in flight about another.
func TestARefusalOfOneRepositoryKeepsTheOthers(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2002", identity.PermissionView)
	other := identity.Repository{Forge: "stub", ID: "2002"}
	wait := make(chan struct{})
	s.forge.mu.Lock()
	s.forge.permissionWait = wait
	s.forge.mu.Unlock()
	done := make(chan error)
	go func() {
		_, err := s.permissions.Permission(context.Background(), "a1", other)
		done <- err
	}()
	<-wait
	s.permissions.Refused("a1", api())
	s.forge.mu.Lock()
	s.forge.permissionWait = nil
	s.forge.mu.Unlock()
	wait <- struct{}{}
	if err := <-done; err != nil {
		t.Errorf("an answer about another repository across a Refused = %v", err)
	}
	s.check(t, "a1", other, identity.PermissionView, nil)
	if n := s.forge.calls(); n != 1 {
		t.Errorf("%d calls to the forge: the answer was not kept", n)
	}
}

func TestFailuresOutsideTheForgeAreTransient(t *testing.T) {
	s := newPermissionsOf(t)
	broken := identity.NewPermissionService(s.forge, s.service, listed{err: errors.New("database locked")}, func() time.Time { return s.now })
	if got, err := broken.Permission(context.Background(), "a1", api()); got != identity.PermissionNone || !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("Permission with a failing store = %s, %v; want a refusal to try again", got, err)
	}
	s.forge.grant("1001", "2001", identity.Permission("admin"))
	s.check(t, "a1", api(), identity.PermissionNone, identity.ErrUnavailable)
}

// failingRead is a store whose tokens cannot be read.
type failingRead struct{ *identitytest.Memory }

func (failingRead) Tokens(context.Context, string) (identity.Tokens, error) {
	return identity.Tokens{}, errors.New("database locked")
}

func TestATokenStoreThatFails(t *testing.T) {
	for name, store := range map[string]func(s *permissionsOf) identity.Store{
		"reading the tokens":           func(s *permissionsOf) identity.Store { return failingRead{s.store} },
		"storing the refreshed tokens": func(s *permissionsOf) identity.Store { return failingStore{s.store} },
	} {
		s := newPermissionsOf(t)
		s.forge.grant("1001", "2001", identity.PermissionDecide)
		s.now = s.now.Add(8 * time.Hour)
		clock := func() time.Time { return s.now }
		tokens := identity.NewTokenService(s.forge, store(s), clock)
		p := identity.NewPermissionService(s.forge, tokens, s.store, clock)
		if got, err := p.Permission(context.Background(), "a1", api()); got != identity.PermissionNone || !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("%s: Permission = %s, %v; want a refusal to try again", name, got, err)
		}
	}
}

// block makes the next question to the stub forge wait, and returns the
// channel that tells it is asked and releases it.
func block(s *permissionsOf) chan struct{} {
	wait := make(chan struct{})
	s.forge.mu.Lock()
	s.forge.permissionWait = wait
	s.forge.mu.Unlock()
	return wait
}

func unblock(s *permissionsOf, wait chan struct{}) {
	s.forge.mu.Lock()
	s.forge.permissionWait = nil
	s.forge.mu.Unlock()
	wait <- struct{}{}
}

// TestAWaiterThatGivesUp: a caller waiting on the question of another one
// stops with its own context.
func TestAWaiterThatGivesUp(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	wait := block(s)
	leader := make(chan error)
	go func() {
		_, err := s.permissions.Permission(context.Background(), "a1", api())
		leader <- err
	}()
	<-wait
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.permissions.Permission(ctx, "a1", api()); got != identity.PermissionNone || !errors.Is(err, identity.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("a waiter that gave up = %s, %v", got, err)
	}
	unblock(s, wait)
	if err := <-leader; err != nil {
		t.Errorf("the leader = %v", err)
	}
}

// TestALeaderThatGivesUp: when the caller that asks gives up, the callers
// waiting on it ask again.
func TestALeaderThatGivesUp(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	wait := block(s)
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error)
	go func() {
		_, err := s.permissions.Permission(ctx, "a1", api())
		leader <- err
	}()
	<-wait
	waiter := make(chan identity.Permission)
	go func() {
		p, _ := s.permissions.Permission(context.Background(), "a1", api())
		waiter <- p
	}()
	// The waiter joins the question before the leader gives up, so the
	// answer it gets is the one it asks again.
	for identity.Waiters(s.permissions, "a1", api()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	unblock(s, wait)
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Errorf("the leader that gave up = %v", err)
	}
	if p := <-waiter; p != identity.PermissionDecide {
		t.Errorf("the waiter = %s, want decide asked again", p)
	}
	// The stub counts the answers it gives: the question given up gave
	// none, so the decide the waiter got is its own question.
	if n := s.forge.calls(); n != 1 {
		t.Errorf("%d answers of the forge, want the waiter's own", n)
	}
}

// TestCountsDoNotGrow: invalidations keep no count once no question is in
// flight, whatever the life of the server.
func TestCountsDoNotGrow(t *testing.T) {
	s := newPermissionsOf(t)
	s.forge.grant("1001", "2001", identity.PermissionDecide)
	for range 50 {
		s.check(t, "a1", api(), identity.PermissionDecide, nil)
		s.permissions.Refused("a1", api())
		s.permissions.Forget("a1")
		s.permissions.Forget("a2")
	}
	wait := block(s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.permissions.Permission(context.Background(), "a1", api())
	}()
	<-wait
	s.permissions.Forget("a1")
	unblock(s, wait)
	<-done
	if n := identity.Counts(s.permissions); n != 0 {
		t.Errorf("the service keeps %d counts with no question in flight", n)
	}
}
