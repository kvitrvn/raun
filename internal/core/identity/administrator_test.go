package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// identitiesOf lists, for each account, its identities.
type identitiesOf map[string][]identity.Identity

func (m identitiesOf) IdentitiesOf(_ context.Context, account string) ([]identity.Identity, error) {
	return m[account], nil
}

func TestAdministrators(t *testing.T) {
	ctx := context.Background()
	alice := identity.Identity{Forge: "github", ID: "1001"}
	accounts := identitiesOf{
		"alice": {alice},
		"bob":   {{Forge: "github", ID: "1002"}},
		"carol": {{Forge: "gitea", ID: "1001"}},
		"dave":  {{Forge: "gitea", ID: "7"}, {Forge: "github", ID: "1001"}},
		"erin":  nil,
	}
	admins := []identity.Entry{{Forge: "github", ID: "1001"}}
	a := identity.NewAdministrators(admins, accounts)
	for account, want := range map[string]bool{
		"alice": true,  // listed
		"bob":   false, // another identity
		"carol": false, // the same identifier on another forge
		"dave":  true,  // one of several identities listed
		"erin":  false, // no identity
		"":      false,
	} {
		if got, err := a.IsAdministrator(ctx, account); err != nil || got != want {
			t.Errorf("IsAdministrator(%q) = %v, %v; want %v", account, got, err, want)
		}
	}
	// The list given is copied: changing it afterwards changes nothing.
	admins[0] = identity.Entry{Forge: "github", ID: "1002"}
	if got, _ := a.IsAdministrator(ctx, "bob"); got {
		t.Error("the administrators follow a list changed after they were made")
	}
	// An empty list makes nobody an administrator.
	if got, _ := identity.NewAdministrators(nil, accounts).IsAdministrator(ctx, "alice"); got {
		t.Error("an empty list made an administrator")
	}
	// The role follows the identities of the account at each call.
	delete(accounts, "alice")
	if got, _ := a.IsAdministrator(ctx, "alice"); got {
		t.Error("an account without its listed identity is still an administrator")
	}
	// A role that cannot be established is not granted.
	broken := identity.NewAdministrators([]identity.Entry{{Forge: "github", ID: "1001"}}, listed{err: errors.New("database locked")})
	if got, err := broken.IsAdministrator(ctx, "alice"); got || err == nil {
		t.Errorf("IsAdministrator with a failing store = %v, %v", got, err)
	}
}

// TestAnAdministratorGetsWhatTheForgeGives: the role grants nothing on a
// repository.
func TestAnAdministratorGetsWhatTheForgeGives(t *testing.T) {
	s := newPermissionsOf(t)
	admins := identity.NewAdministrators([]identity.Entry{{Forge: "stub", ID: "1001"}}, s.store)
	if is, err := admins.IsAdministrator(context.Background(), "a1"); err != nil || !is {
		t.Fatalf("Alice is not an administrator: %v", err)
	}
	s.check(t, "a1", api(), identity.PermissionNone, nil)
	s.forge.grant("1001", "2002", identity.PermissionView)
	s.check(t, "a1", identity.Repository{Forge: "stub", ID: "2002"}, identity.PermissionView, nil)
}
