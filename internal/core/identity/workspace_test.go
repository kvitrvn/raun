package identity_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// workspacesOf signs Alice and Bob up on a memory store and returns its
// workspaces service, and the identifiers of Alice and Bob.
func workspacesOf(t *testing.T) (*identity.Workspaces, *identity.Login, string, string) {
	t.Helper()
	l := newLoginOf(t, everyone())
	ctx := context.Background()
	var ids []string
	for _, who := range []string{"alice", "bob"} {
		state, code := l.callback(t, "s-"+who, who, "/")
		if _, err := l.login.Complete(ctx, "s-"+who, state, code); err != nil {
			t.Fatal(err)
		}
		a, _, err := l.login.FinishSignup(ctx, "s-"+who, who)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	return identity.NewWorkspaces(l.store, rand.Reader, func() time.Time { return l.now }), l.login, ids[0], ids[1]
}

func TestCreateAWorkspace(t *testing.T) {
	ctx := context.Background()
	w, _, alice, bob := workspacesOf(t)
	ws, err := w.Create(ctx, alice, "  Acme-Corp ")
	if err != nil || ws.Name != "acme-corp" || ws.Personal || len(ws.ID) != 32 {
		t.Fatalf("Create = %+v, %v", ws, err)
	}
	for name, want := range map[string]error{
		"acme-corp":  identity.ErrNameTaken,
		"bob":        identity.ErrNameTaken,
		"workspaces": identity.ErrReservedName,
		"login":      identity.ErrReservedName,
		"a--b":       identity.ErrInvalidName,
		"":           identity.ErrInvalidName,
	} {
		if _, err := w.Create(ctx, bob, name); !errors.Is(err, want) {
			t.Errorf("Create(%q) = %v, want %v", name, err, want)
		}
	}
	list, err := w.List(ctx, alice)
	if err != nil || len(list) != 2 || list[0].Workspace.Name != "acme-corp" || list[0].Role != identity.RoleOwner || list[1].Workspace.Name != "alice" {
		t.Errorf("List = %+v, %v", list, err)
	}
	if list, _ := w.List(ctx, bob); len(list) != 1 {
		t.Errorf("Bob sees %+v", list)
	}
}

func TestOpenAndAddMembers(t *testing.T) {
	ctx := context.Background()
	w, _, alice, bob := workspacesOf(t)
	if _, err := w.Create(ctx, alice, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Open(ctx, bob, "acme"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("Open by a stranger = %v", err)
	}
	if err := w.AddMember(ctx, bob, "acme", "bob", identity.RoleMember); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("AddMember by a stranger = %v", err)
	}
	if err := w.AddMember(ctx, alice, "acme", "bob", identity.Role("admin")); !errors.Is(err, identity.ErrInvalidRole) {
		t.Errorf("AddMember with no role = %v", err)
	}
	if err := w.AddMember(ctx, alice, "ACME", " Bob ", identity.RoleMember); err != nil {
		t.Fatal(err)
	}
	m, members, err := w.Open(ctx, bob, "acme")
	if err != nil || m.Role != identity.RoleMember || len(members) != 2 {
		t.Errorf("Open by a member = %+v, %+v, %v", m, members, err)
	}
	if err := w.AddMember(ctx, bob, "acme", "alice", identity.RoleMember); !errors.Is(err, identity.ErrNotOwner) {
		t.Errorf("AddMember by a member = %v", err)
	}
	if err := w.AddMember(ctx, alice, "alice", "bob", identity.RoleMember); !errors.Is(err, identity.ErrPersonalWorkspace) {
		t.Errorf("AddMember to a personal workspace = %v", err)
	}
}

// TestASignupCannotTakeAWorkspaceName: the name of a shared workspace is
// taken for a sign-up too, and the sign-up waits for another one.
func TestASignupCannotTakeAWorkspaceName(t *testing.T) {
	ctx := context.Background()
	l := newLoginOf(t, everyone())
	state, code := l.callback(t, "s1", "alice", "/")
	if _, err := l.login.Complete(ctx, "s1", state, code); err != nil {
		t.Fatal(err)
	}
	alice, _, err := l.login.FinishSignup(ctx, "s1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	w := identity.NewWorkspaces(l.store, rand.Reader, func() time.Time { return l.now })
	if _, err := w.Create(ctx, alice.ID, "acme"); err != nil {
		t.Fatal(err)
	}
	state, code = l.callback(t, "s2", "bob", "/")
	if _, err := l.login.Complete(ctx, "s2", state, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.login.FinishSignup(ctx, "s2", "acme"); !errors.Is(err, identity.ErrNameTaken) {
		t.Errorf("FinishSignup with the name of a workspace = %v, want ErrNameTaken", err)
	}
	if _, err := l.login.Signup(ctx, "s2"); err != nil {
		t.Errorf("the sign-up did not outlive the refusal: %v", err)
	}
}
