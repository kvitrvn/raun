package identitytest

import (
	"context"
	"errors"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// workspace is the shared workspace n of the suite.
func workspace(n, name string) identity.Workspace {
	return identity.Workspace{ID: accountID(n), Name: name, CreatedAt: t0Time()}
}

// workspaceSuite runs the contract of the workspace store.
func workspaceSuite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()
	bob := identity.Identity{Forge: "github", ID: "1002", Login: "bob"}
	carol := identity.Identity{Forge: "github", ID: "1003", Login: "carol"}

	t.Run("a sign-up creates the personal workspace of the account", func(t *testing.T) {
		fx := newFixture(t)
		signedUp(t, fx.Store, hash(1), alice(), account("a1", "alice"), tokens("1"))
		got, err := fx.Workspaces.WorkspacesOf(ctx, accountID("a1"))
		want := identity.Workspace{ID: accountID("a1"), Name: "alice", Personal: true, CreatedAt: t0Time()}
		if err != nil || len(got) != 1 || got[0].Role != identity.RoleOwner || got[0].Workspace.ID != want.ID ||
			got[0].Workspace.Name != want.Name || !got[0].Workspace.Personal || !got[0].Workspace.CreatedAt.Equal(want.CreatedAt) {
			t.Fatalf("WorkspacesOf = %+v, %v; want the personal workspace %+v", got, err, want)
		}
		m, err := fx.Workspaces.WorkspaceOf(ctx, "alice", accountID("a1"))
		if err != nil || m.Workspace.ID != accountID("a1") || m.Role != identity.RoleOwner {
			t.Errorf("WorkspaceOf the personal workspace = %+v, %v", m, err)
		}
		members, err := fx.Workspaces.Members(ctx, accountID("a1"))
		if err != nil || len(members) != 1 || members[0] != (identity.Member{Account: accountID("a1"), Name: "alice", Role: identity.RoleOwner}) {
			t.Errorf("Members of the personal workspace = %+v, %v", members, err)
		}
	})

	t.Run("accounts and workspaces share one namespace", func(t *testing.T) {
		fx := newFixture(t)
		signedUp(t, fx.Store, hash(1), alice(), account("a1", "alice"), tokens("1"))
		// A workspace cannot take the name of an account.
		if err := fx.Workspaces.CreateWorkspace(ctx, workspace("w1", "alice"), accountID("a1")); !errors.Is(err, identity.ErrNameTaken) {
			t.Errorf("CreateWorkspace with the name of an account = %v, want ErrNameTaken", err)
		}
		if err := fx.Workspaces.CreateWorkspace(ctx, workspace("w1", "acme"), accountID("a1")); err != nil {
			t.Fatal(err)
		}
		// Nor another workspace.
		if err := fx.Workspaces.CreateWorkspace(ctx, workspace("w2", "acme"), accountID("a1")); !errors.Is(err, identity.ErrNameTaken) {
			t.Errorf("CreateWorkspace with the name of a workspace = %v, want ErrNameTaken", err)
		}
		// An account cannot take the name of a workspace, and its sign-up
		// waits for another name.
		if err := fx.Store.PutSignup(ctx, signup(hash(2), bob, tokens("2"))); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.Store.CompleteSignup(ctx, hash(2), account("a2", "acme"), t0Time()); !errors.Is(err, identity.ErrNameTaken) {
			t.Errorf("CompleteSignup with the name of a workspace = %v, want ErrNameTaken", err)
		}
		if _, err := fx.Store.Account(ctx, accountID("a2")); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a refused sign-up created the account: %v", err)
		}
		if _, err := fx.Store.CompleteSignup(ctx, hash(2), account("a2", "bob"), t0Time()); err != nil {
			t.Errorf("CompleteSignup after ErrNameTaken = %v", err)
		}
		got, _ := fx.Workspaces.WorkspacesOf(ctx, accountID("a1"))
		if len(got) != 2 || got[0].Workspace.Name != "acme" || got[0].Workspace.Personal || got[1].Workspace.Name != "alice" {
			t.Errorf("WorkspacesOf Alice = %+v, want acme then alice", got)
		}
	})

	t.Run("a workspace shows to its members only", func(t *testing.T) {
		fx := newFixture(t)
		signedUp(t, fx.Store, hash(1), alice(), account("a1", "alice"), tokens("1"))
		signedUp(t, fx.Store, hash(2), bob, account("a2", "bob"), tokens("2"))
		if err := fx.Workspaces.CreateWorkspace(ctx, workspace("w1", "acme"), accountID("a1")); err != nil {
			t.Fatal(err)
		}
		for name, ws := range map[string]string{"a shared workspace": "acme", "a personal workspace": "alice", "nothing": "nobody"} {
			if _, err := fx.Workspaces.WorkspaceOf(ctx, ws, accountID("a2")); !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("WorkspaceOf %s for a stranger = %v, want ErrNotFound", name, err)
			}
		}
		if got, _ := fx.Workspaces.WorkspacesOf(ctx, accountID("a2")); len(got) != 1 || got[0].Workspace.Name != "bob" {
			t.Errorf("WorkspacesOf Bob = %+v, want his personal workspace only", got)
		}
	})

	t.Run("an owner adds members", func(t *testing.T) {
		fx := newFixture(t)
		w := fx.Workspaces
		signedUp(t, fx.Store, hash(1), alice(), account("a1", "alice"), tokens("1"))
		signedUp(t, fx.Store, hash(2), bob, account("a2", "bob"), tokens("2"))
		signedUp(t, fx.Store, hash(3), carol, account("a3", "carol"), tokens("3"))
		if err := w.CreateWorkspace(ctx, workspace("w1", "acme"), accountID("a1")); err != nil {
			t.Fatal(err)
		}
		acme := accountID("w1")
		if err := w.AddMember(ctx, accountID("a1"), acme, "bob", identity.RoleMember); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name        string
			actor, whom string
			workspace   string
			role        identity.Role
			want        error
		}{
			{"by a member", accountID("a2"), "carol", acme, identity.RoleMember, identity.ErrNotOwner},
			{"by a stranger", accountID("a3"), "carol", acme, identity.RoleMember, identity.ErrNotFound},
			{"twice", accountID("a1"), "bob", acme, identity.RoleOwner, identity.ErrAlreadyMember},
			{"of nobody", accountID("a1"), "nobody", acme, identity.RoleMember, identity.ErrNotFound},
			{"of a workspace name", accountID("a1"), "acme", acme, identity.RoleMember, identity.ErrNotFound},
			{"to a personal workspace", accountID("a1"), "bob", accountID("a1"), identity.RoleMember, identity.ErrPersonalWorkspace},
			{"to no workspace", accountID("a1"), "bob", accountID("zz"), identity.RoleMember, identity.ErrNotFound},
			{"with no role", accountID("a1"), "carol", acme, identity.Role("admin"), identity.ErrInvalidRole},
		} {
			if err := w.AddMember(ctx, tc.actor, tc.workspace, tc.whom, tc.role); !errors.Is(err, tc.want) {
				t.Errorf("AddMember %s = %v, want %v", tc.name, err, tc.want)
			}
		}
		// A second owner can add members too.
		if err := w.AddMember(ctx, accountID("a1"), acme, "carol", identity.RoleOwner); err != nil {
			t.Fatal(err)
		}
		members, err := w.Members(ctx, acme)
		want := []identity.Member{
			{Account: accountID("a1"), Name: "alice", Role: identity.RoleOwner},
			{Account: accountID("a2"), Name: "bob", Role: identity.RoleMember},
			{Account: accountID("a3"), Name: "carol", Role: identity.RoleOwner},
		}
		if err != nil || len(members) != len(want) {
			t.Fatalf("Members = %+v, %v", members, err)
		}
		for i := range want {
			if members[i] != want[i] {
				t.Errorf("member %d = %+v, want %+v", i, members[i], want[i])
			}
		}
		if m, err := w.WorkspaceOf(ctx, "acme", accountID("a2")); err != nil || m.Role != identity.RoleMember {
			t.Errorf("WorkspaceOf for Bob = %+v, %v", m, err)
		}
		if got, _ := w.Members(ctx, accountID("a1")); len(got) != 1 {
			t.Errorf("the personal workspace got members: %+v", got)
		}
	})
}
