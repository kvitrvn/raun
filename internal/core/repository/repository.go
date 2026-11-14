package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound: no such repository, or none the person may see.
	ErrNotFound = errors.New("repository not found")
	// ErrNotMember: the person does not belong to the workspace.
	ErrNotMember = errors.New("not a member of the workspace")
	// ErrNotDecider: the forge does not let the person decide on the
	// repository.
	ErrNotDecider = errors.New("the forge does not let the person decide on the repository")
	// ErrElsewhere: the repository belongs to another workspace, active or
	// deactivated.
	ErrElsewhere = errors.New("the repository belongs to another workspace")
	// ErrMissingCapability: the forge lacks a capability activation needs;
	// a MissingError names them.
	ErrMissingCapability = errors.New("the forge lacks a capability activation needs")
	// ErrUnavailable: the forge cannot answer now.
	ErrUnavailable = errors.New("forge unavailable")
	// ErrRefused: the forge refuses to tell what the person may reach, its
	// tokens refused or gone: the person must log in again.
	ErrRefused = errors.New("the forge refuses the tokens of the person")
	// ErrNoReach: the forge cannot tell which repositories its App reaches.
	ErrNoReach = errors.New("the forge cannot tell which repositories its App reaches")
)

// Capability is an optional capability of the forge that activation needs.
type Capability string

// The capabilities activation needs (epic 5 record, Preconditions); the
// clone credential joins them in epic 6.
const (
	// CapabilityFiles reads the configuration of the repository (#33).
	CapabilityFiles Capability = "files"
	// CapabilityWebhooks makes the forge deliver the events (#32).
	CapabilityWebhooks Capability = "webhooks"
	// CapabilityEvents verifies and parses a delivery (#32).
	CapabilityEvents Capability = "events"
)

// Preconditions are the capabilities activation needs, in the order they
// are named.
var Preconditions = []Capability{CapabilityFiles, CapabilityWebhooks, CapabilityEvents}

// MissingError names the capabilities the forge lacks; it wraps
// ErrMissingCapability.
type MissingError struct {
	Missing []Capability
}

func (e *MissingError) Error() string {
	names := make([]string, len(e.Missing))
	for i, c := range e.Missing {
		names[i] = string(c)
	}
	return fmt.Sprintf("%v: %s", ErrMissingCapability, strings.Join(names, ", "))
}

func (e *MissingError) Unwrap() error { return ErrMissingCapability }

// Permission is what a person may do on a repository, as the forge says.
type Permission string

const (
	PermissionNone   Permission = "none"
	PermissionView   Permission = "view"
	PermissionDecide Permission = "decide"
)

// Listed is a repository as the forge lists it for a person: its stable
// identifier, and the snapshots of its full name and default branch.
type Listed struct {
	Forge         string
	ID            string
	FullName      string
	DefaultBranch string
	Private       bool
}

// Workspace is a workspace a repository belongs to: its identifier and its
// name, which never changes.
type Workspace struct {
	ID   string
	Name string
}

// State is where a repository stands in Raun.
type State string

const (
	// StateActive: Raun reacts to the repository.
	StateActive State = "active"
	// StateDeactivated: a person deactivated it; its knowledge stays,
	// read-only.
	StateDeactivated State = "deactivated"
	// StateUnreachable: the forge App no longer reaches it; it is
	// deactivated, and can be activated again once the App reaches it.
	StateUnreachable State = "unreachable"
)

// Repository is a repository Raun knows: activated once in a workspace, to
// which it belongs from then on, active or not. The forge and the stable
// identifier are its identity; the full name, the default branch and Private
// are snapshots, refreshed from the forge. FullName is empty while another
// repository holds the name on the forge, until the forge lists it again.
type Repository struct {
	Forge         string
	ID            string
	Workspace     Workspace
	FullName      string
	DefaultBranch string
	Private       bool
	// Active: Raun reacts to it. A repository the App no longer reaches is
	// never active.
	Active bool
	// Reachable: the forge App reaches it, as the forge last said.
	Reachable bool
	// ChangedBy is the account that last activated or deactivated it, and
	// ChangedAt when; a loss of the App changes neither.
	ChangedBy string
	ChangedAt time.Time
}

// State is the state of r.
func (r Repository) State() State {
	switch {
	case !r.Reachable:
		return StateUnreachable
	case r.Active:
		return StateActive
	default:
		return StateDeactivated
	}
}

// Forge is what the context asks the forge on behalf of a person: the
// repositories the person reaches through Raun, and the permission of the
// person on one of them. Both answer ErrUnavailable when the forge cannot
// answer now and ErrRefused when it refuses the tokens of the person; a
// permission that cannot be established otherwise is PermissionNone.
type Forge interface {
	// Name is the name the server gives the forge: github.
	Name() string
	// Repositories are the repositories the account reaches, sorted by
	// full name.
	Repositories(ctx context.Context, account string) ([]Listed, error)
	// Permission is what the account may do on the repository with this
	// stable identifier.
	Permission(ctx context.Context, account, id string) (Permission, error)
}

// Reach is the optional capability of a forge that tells which repositories
// its App reaches: every one of them, by stable identifier, or an error,
// never a part of them.
type Reach interface {
	Reached(ctx context.Context) ([]string, error)
}

// Workspaces finds the workspaces of an account.
type Workspaces interface {
	// WorkspaceOf returns the workspace with this name when the account
	// belongs to it; ErrNotMember otherwise.
	WorkspaceOf(ctx context.Context, name, account string) (Workspace, error)
}

// Store keeps the repositories Raun knows. A repository belongs to one
// workspace for good: no operation moves it. Every store passes the
// contract suite of package repositorytest.
type Store interface {
	// Known returns the repositories of forge among ids, sorted by
	// identifier; an identifier Raun does not know is left out.
	Known(ctx context.Context, forge string, ids []string) ([]Repository, error)
	// Find returns the repository of forge with this full name in the
	// workspace with this name; ErrNotFound if none.
	Find(ctx context.Context, workspace, forge, fullName string) (Repository, error)
	// InWorkspace returns the repositories of the workspace with this
	// identifier, sorted by full name, then identifier.
	InWorkspace(ctx context.Context, workspace string) ([]Repository, error)
	// Activate makes r active and reachable in r.Workspace with its
	// snapshots, by r.ChangedBy at r.ChangedAt: it stores r when Raun does
	// not know it, and activates it again when it belongs to that
	// workspace. A full name another known repository of the forge holds
	// is taken from it, as Refresh does. ErrElsewhere when it belongs to
	// another workspace; nothing changes then.
	Activate(ctx context.Context, r Repository) error
	// Deactivate makes the repository of forge with this identifier, in
	// the workspace with this identifier, not active, by account at at.
	// ErrNotFound when the workspace holds no such repository.
	Deactivate(ctx context.Context, workspace, forge, id, account string, at time.Time) error
	// Refresh stores the snapshots of the listed repositories Raun knows,
	// in one operation; the others are ignored. A full name another known
	// repository of the forge holds is taken from it: the forge gave the
	// name to the listed one since.
	Refresh(ctx context.Context, listed []Listed) error
	// SetReach makes the repositories of forge whose identifier is in
	// reached reachable, and every other one unreachable and not active, in
	// one operation. It returns the identifiers of the repositories that
	// became unreachable, sorted.
	SetReach(ctx context.Context, forge string, reached []string) ([]string, error)
}
