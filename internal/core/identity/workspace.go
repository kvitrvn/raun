package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Workspace groups repositories, and later their agents and secrets. It never
// grants access to a repository: the forge does. Its name belongs to the one
// namespace of accounts and workspaces, and never changes. A personal
// workspace is the workspace of an account: it has the account's identifier
// and name, and only that account belongs to it.
type Workspace struct {
	ID        string
	Name      string
	Personal  bool
	CreatedAt time.Time
}

// Role is what an account may do in a workspace.
type Role string

const (
	// RoleOwner manages the members and the settings of the workspace.
	RoleOwner Role = "owner"
	// RoleMember sees the workspace and activates in it a repository the
	// forge lets them decide on (epic 5).
	RoleMember Role = "member"
)

// Valid reports whether r is a role.
func (r Role) Valid() bool { return r == RoleOwner || r == RoleMember }

// Membership is a workspace and the role of an account in it.
type Membership struct {
	Workspace Workspace
	Role      Role
}

// Member is an account of a workspace: its identifier, its name and its
// role.
type Member struct {
	Account string
	Name    string
	Role    Role
}

var (
	// ErrNotOwner: the account is a member of the workspace, not an owner.
	ErrNotOwner = errors.New("not an owner of the workspace")
	// ErrAlreadyMember: the account belongs to the workspace already.
	ErrAlreadyMember = errors.New("already a member of the workspace")
	// ErrPersonalWorkspace: a personal workspace takes no other member.
	ErrPersonalWorkspace = errors.New("a personal workspace takes no other member")
	// ErrInvalidRole: the role is neither owner nor member.
	ErrInvalidRole = errors.New("invalid role")
)

// WorkspaceStore keeps workspaces and their members. The names of
// workspaces and accounts are one namespace: the store refuses a name any of
// them has. Every check of a change runs in the operation that makes it.
// Every store passes the contract suite of package identitytest.
type WorkspaceStore interface {
	// CreateWorkspace stores w, a shared workspace, with owner, the
	// identifier of an account, as its owner. ErrNameTaken when an account
	// or a workspace has the name; nothing changes then.
	CreateWorkspace(ctx context.Context, w Workspace, owner string) error
	// WorkspacesOf returns the workspaces of the account with this
	// identifier, sorted by name, with its role in each.
	WorkspacesOf(ctx context.Context, account string) ([]Membership, error)
	// WorkspaceOf returns the workspace with this name and the role of the
	// account in it. ErrNotFound when there is none or the account does not
	// belong to it: a stranger learns nothing of a workspace.
	WorkspaceOf(ctx context.Context, name, account string) (Membership, error)
	// Members returns the members of the workspace with this identifier,
	// sorted by name.
	Members(ctx context.Context, workspace string) ([]Member, error)
	// AddMember makes the account named name a member of the workspace with
	// this identifier, with role, on behalf of actor. ErrNotFound when the
	// actor does not belong to the workspace or no account has the name,
	// ErrNotOwner when the actor is not an owner, ErrPersonalWorkspace for a
	// personal workspace, ErrAlreadyMember when the account belongs to it.
	AddMember(ctx context.Context, actor, workspace, name string, role Role) error
}

// Workspaces creates workspaces and manages their members. It is safe for
// concurrent use.
type Workspaces struct {
	store  WorkspaceStore
	random io.Reader
	now    func() time.Time
}

// NewWorkspaces returns the workspaces of store, drawing identifiers from
// random at the time now gives.
func NewWorkspaces(store WorkspaceStore, random io.Reader, now func() time.Time) *Workspaces {
	return &Workspaces{store: store, random: random, now: now}
}

// normalName is a name as a person types it, as the namespace keeps it.
func normalName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Create creates the shared workspace name, lowercased, with actor, the
// identifier of an account, as its owner. ErrInvalidName or ErrReservedName
// when the name breaks the rule, ErrNameTaken when an account or a workspace
// has it.
func (w *Workspaces) Create(ctx context.Context, actor, name string) (Workspace, error) {
	name = normalName(name)
	if err := CheckName(name); err != nil {
		return Workspace{}, err
	}
	id, err := newAccountID(w.random)
	if err != nil {
		return Workspace{}, fmt.Errorf("draw a workspace identifier: %w", err)
	}
	ws := Workspace{ID: id, Name: name, CreatedAt: w.now().UTC()}
	if err := w.store.CreateWorkspace(ctx, ws, actor); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

// List returns the workspaces of actor, sorted by name.
func (w *Workspaces) List(ctx context.Context, actor string) ([]Membership, error) {
	return w.store.WorkspacesOf(ctx, actor)
}

// Open returns the workspace name, the role of actor in it and its members.
// ErrNotFound when there is none or actor does not belong to it.
func (w *Workspaces) Open(ctx context.Context, actor, name string) (Membership, []Member, error) {
	m, err := w.store.WorkspaceOf(ctx, normalName(name), actor)
	if err != nil {
		return Membership{}, nil, err
	}
	members, err := w.store.Members(ctx, m.Workspace.ID)
	if err != nil {
		return Membership{}, nil, fmt.Errorf("list the members: %w", err)
	}
	return m, members, nil
}

// AddMember makes the account named account a member of the workspace name,
// with role, on behalf of actor, who must own it. The errors are those of
// WorkspaceStore.AddMember, and ErrInvalidRole.
func (w *Workspaces) AddMember(ctx context.Context, actor, name, account string, role Role) error {
	if !role.Valid() {
		return ErrInvalidRole
	}
	m, err := w.store.WorkspaceOf(ctx, normalName(name), actor)
	if err != nil {
		return err
	}
	return w.store.AddMember(ctx, actor, m.Workspace.ID, normalName(account), role)
}
