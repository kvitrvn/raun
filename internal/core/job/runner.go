package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxRunnerName bounds the name of a runner, in bytes.
const MaxRunnerName = 64

// ContactEvery is how often the last contact of a runner is written at
// most: the Runners screen tells a runner seen in the last two minutes
// from one that is not, and a runner asks every 15 seconds.
const ContactEvery = time.Minute

var (
	// ErrInvalidRunner: a request for a runner lacks a field or holds one
	// that is malformed; the error names the field.
	ErrInvalidRunner = errors.New("invalid runner")
	// ErrRunnerNameTaken: another runner has this name, revoked or not.
	ErrRunnerNameTaken = errors.New("a runner has this name already")
	// ErrNoRunner: no runner has this identifier.
	ErrNoRunner = errors.New("runner not found")
	// ErrRevoked: the runner is revoked; nothing changes it any more.
	ErrRevoked = errors.New("the runner is revoked")
	// ErrUnauthenticated: the token is not the one of a runner that is not
	// revoked, whatever the reason: unknown, replaced or revoked.
	ErrUnauthenticated = errors.New("not the token of a runner")
	// ErrNotAdministrator: only an administrator of the instance manages
	// runners.
	ErrNotAdministrator = errors.New("not an administrator of the instance")
	// ErrNoWorkspace: no workspace has this name.
	ErrNoWorkspace = errors.New("no workspace has this name")
)

// Runner is a raun runner process the server knows: who it serves, what it
// may run, and its token, of which the store keeps the hash only.
type Runner struct {
	ID   string
	Name string
	// Workspace is the identifier of the workspace whose jobs it takes;
	// empty for a runner of the instance, which takes the jobs of every
	// workspace.
	Workspace string
	// Capabilities are what the administrator gave it, sorted; the runner
	// never declares them.
	Capabilities []Capability
	// CreatedBy is the account of the administrator who created it.
	CreatedBy string
	CreatedAt time.Time
	// LastContact is when it last authenticated, written every
	// ContactEvery at most; zero until then.
	LastContact time.Time
	// Release is the release it last said it runs, in the User-Agent of
	// its requests; empty until it said one.
	Release string
	// RevokedAt is when it was revoked; zero while it is not.
	RevokedAt time.Time
}

// Revoked reports whether r is revoked.
func (r Runner) Revoked() bool { return !r.RevokedAt.IsZero() }

// Claimant is r asking for a job, with the scope and the capabilities the
// store holds for it.
func (r Runner) Claimant() Claimant {
	return Claimant{Runner: r.ID, Workspace: r.Workspace, Capabilities: slices.Clone(r.Capabilities)}
}

// KnownCapabilities returns the capabilities of the kinds that exist,
// sorted: what an administrator may give a runner.
func KnownCapabilities() []Capability {
	var out []Capability
	for _, s := range kinds {
		if !slices.Contains(out, s.Capability) {
			out = append(out, s.Capability)
		}
	}
	slices.Sort(out)
	return out
}

// RunnerRequest is what an administrator asks a runner for.
type RunnerRequest struct {
	Name string
	// Workspace is the identifier of the workspace it serves; empty for the
	// instance.
	Workspace    string
	Capabilities []Capability
	CreatedBy    string
}

// NewRunner returns the runner asked for by req at at, its identifier and
// its token drawn from random, with the hash of the token, which is all a
// store keeps. Errors wrap ErrInvalidRunner and name the field.
func NewRunner(random io.Reader, req RunnerRequest, at time.Time) (Runner, string, string, error) {
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return Runner{}, "", "", fmt.Errorf("%w: the name is empty", ErrInvalidRunner)
	case len(name) > MaxRunnerName:
		return Runner{}, "", "", fmt.Errorf("%w: the name holds more than %d bytes", ErrInvalidRunner, MaxRunnerName)
	case !utf8.ValidString(name) || strings.ContainsFunc(name, isControl):
		return Runner{}, "", "", fmt.Errorf("%w: the name is not one line of text", ErrInvalidRunner)
	case req.Workspace != "" && !isID(req.Workspace):
		return Runner{}, "", "", fmt.Errorf("%w: the workspace %q is not an identifier", ErrInvalidRunner, req.Workspace)
	case req.CreatedBy == "":
		return Runner{}, "", "", fmt.Errorf("%w: no administrator created it", ErrInvalidRunner)
	case len(req.Capabilities) == 0:
		return Runner{}, "", "", fmt.Errorf("%w: no capability", ErrInvalidRunner)
	case at.IsZero():
		return Runner{}, "", "", fmt.Errorf("%w: no time of creation", ErrInvalidRunner)
	}
	known := KnownCapabilities()
	var caps []Capability
	for _, c := range req.Capabilities {
		if !slices.Contains(known, c) {
			return Runner{}, "", "", fmt.Errorf("%w: unknown capability %q", ErrInvalidRunner, c)
		}
		if !slices.Contains(caps, c) {
			caps = append(caps, c)
		}
	}
	slices.Sort(caps)
	id, err := NewID(random)
	if err != nil {
		return Runner{}, "", "", err
	}
	token, hash, err := NewToken(random)
	if err != nil {
		return Runner{}, "", "", err
	}
	return Runner{
		ID: id, Name: name, Workspace: req.Workspace, Capabilities: caps,
		CreatedBy: req.CreatedBy, CreatedAt: at.UTC(),
	}, token, hash, nil
}

// isID reports whether s is the identifier of a workspace: 32 lowercase
// hexadecimal digits.
func isID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// RunnerStore keeps the runners. Every store passes the contract suite of
// package jobtest.
type RunnerStore interface {
	// CreateRunner stores r with the hash of its token.
	// ErrRunnerNameTaken when another runner has its name; nothing changes
	// then.
	CreateRunner(ctx context.Context, r Runner, hash string) error
	// Runner returns the runner with this identifier; ErrNoRunner if none.
	Runner(ctx context.Context, id string) (Runner, error)
	// Runners returns every runner, revoked ones included, sorted by name.
	Runners(ctx context.Context) ([]Runner, error)
	// RotateRunner replaces the token of the runner with this identifier
	// by the one of hash: the old one is refused at once, and the runs of
	// the runner keep their leases. ErrNoRunner, ErrRevoked.
	RotateRunner(ctx context.Context, id, hash string) error
	// RevokeRunner revokes the runner with this identifier at at and, in
	// the same operation, ends its open runs as lost, their jobs queued
	// again or failed by AfterLost. It returns how many jobs changed; a
	// runner revoked already is left as it is. ErrNoRunner.
	RevokeRunner(ctx context.Context, id string, at time.Time) (int, error)
	// Authenticate returns the runner whose token has hash, and writes now
	// as its last contact, with release when it is not empty, when the
	// contact stored is ContactEvery old or more or the release stored is
	// another one; that write is advisory, and failing it fails nothing.
	// The last contact and the release it returns may lag behind the
	// stored ones, written by a concurrent request. ErrUnauthenticated for
	// no runner, or a revoked one.
	Authenticate(ctx context.Context, hash, release string, now time.Time) (Runner, error)
	// ClaimFor claims for the runner with this identifier as Store.Claim
	// does, with the scope and the capabilities the store holds for it,
	// read in the same operation: a runner revoked or narrowed before
	// never gets a job it may no longer take. ErrUnauthenticated for no
	// runner, or a revoked one.
	ClaimFor(ctx context.Context, runner string, now time.Time) (Claimed, error)
	// Workspaces returns every workspace of the instance, by name, for the
	// scope of a runner.
	Workspaces(ctx context.Context) ([]Workspace, error)
}

// Workspace is a workspace a runner may serve.
type Workspace struct {
	ID   string
	Name string
}

// Administrators tells the administrators of the instance.
type Administrators interface {
	IsAdministrator(ctx context.Context, account string) (bool, error)
}

// RegistryOptions configure a Registry.
type RegistryOptions struct {
	Store  RunnerStore
	Admins Administrators
	// Random is the source of identifiers and tokens.
	Random io.Reader
	// Now gives the time of a change; nil means time.Now.
	Now func() time.Time
}

// Registry applies the rules of runners: only an administrator of the
// instance creates, lists, rotates and revokes them, and a runner proves
// who it is by its token alone.
type Registry struct {
	store  RunnerStore
	admins Administrators
	random io.Reader
	now    func() time.Time
}

// NewRegistry returns the registry of opts.
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Registry{store: opts.Store, admins: opts.Admins, random: opts.Random, now: opts.Now}
}

// admin checks that actor is an administrator; a role that cannot be
// established is refused.
func (g *Registry) admin(ctx context.Context, actor string) error {
	ok, err := g.admins.IsAdministrator(ctx, actor)
	if err != nil {
		return fmt.Errorf("check the administrator role: %w", err)
	}
	if !ok {
		return ErrNotAdministrator
	}
	return nil
}

// Runners returns every runner, for actor, an administrator, with the
// workspaces a runner may serve.
func (g *Registry) Runners(ctx context.Context, actor string) ([]Runner, []Workspace, error) {
	if err := g.admin(ctx, actor); err != nil {
		return nil, nil, err
	}
	runners, err := g.store.Runners(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list the runners: %w", err)
	}
	workspaces, err := g.store.Workspaces(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list the workspaces: %w", err)
	}
	return runners, workspaces, nil
}

// Create creates for actor, an administrator, the runner with this name,
// serving the workspace with this name, or the instance when it is empty,
// with capabilities, and returns it with its token, which is shown once.
// ErrNotAdministrator, ErrNoWorkspace, ErrInvalidRunner, ErrRunnerNameTaken.
func (g *Registry) Create(ctx context.Context, actor, name, workspace string, capabilities []Capability) (Runner, string, error) {
	if err := g.admin(ctx, actor); err != nil {
		return Runner{}, "", err
	}
	id := ""
	if workspace != "" {
		all, err := g.store.Workspaces(ctx)
		if err != nil {
			return Runner{}, "", fmt.Errorf("list the workspaces: %w", err)
		}
		i := slices.IndexFunc(all, func(w Workspace) bool { return w.Name == workspace })
		if i < 0 {
			return Runner{}, "", fmt.Errorf("%w: %q", ErrNoWorkspace, workspace)
		}
		id = all[i].ID
	}
	r, token, hash, err := NewRunner(g.random, RunnerRequest{Name: name, Workspace: id, Capabilities: capabilities, CreatedBy: actor}, g.now())
	if err != nil {
		return Runner{}, "", err
	}
	if err := g.store.CreateRunner(ctx, r, hash); err != nil {
		return Runner{}, "", err
	}
	return r, token, nil
}

// Rotate gives the runner with this identifier a new token, for actor, an
// administrator, and returns it, shown once; the old one is refused at
// once. ErrNotAdministrator, ErrNoRunner, ErrRevoked.
func (g *Registry) Rotate(ctx context.Context, actor, id string) (Runner, string, error) {
	if err := g.admin(ctx, actor); err != nil {
		return Runner{}, "", err
	}
	token, hash, err := NewToken(g.random)
	if err != nil {
		return Runner{}, "", err
	}
	if err := g.store.RotateRunner(ctx, id, hash); err != nil {
		return Runner{}, "", err
	}
	r, err := g.store.Runner(ctx, id)
	if err != nil {
		return Runner{}, "", err
	}
	return r, token, nil
}

// Revoke revokes the runner with this identifier for actor, an
// administrator: its token is refused, and the jobs it runs go back to the
// queue. It returns the runner and how many jobs changed.
// ErrNotAdministrator, ErrNoRunner.
func (g *Registry) Revoke(ctx context.Context, actor, id string) (Runner, int, error) {
	if err := g.admin(ctx, actor); err != nil {
		return Runner{}, 0, err
	}
	n, err := g.store.RevokeRunner(ctx, id, g.now())
	if err != nil {
		return Runner{}, 0, err
	}
	r, err := g.store.Runner(ctx, id)
	if err != nil {
		return Runner{}, 0, err
	}
	return r, n, nil
}

// MaxToken bounds a token a runner presents, in bytes: a token is 64
// hexadecimal digits, and anything longer is refused before it is hashed.
const MaxToken = 128

// MaxRelease bounds the release a runner says it runs, in bytes.
const MaxRelease = 64

// ValidRelease reports whether release is one a runner may say it runs:
// 1 to MaxRelease letters, digits and the characters . + - _.
func ValidRelease(release string) bool {
	if release == "" || len(release) > MaxRelease {
		return false
	}
	for _, r := range release {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '+' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// Authenticate returns the runner whose token is token, recording release,
// the release it says it runs, unless it is not a ValidRelease.
// ErrUnauthenticated, the same whatever the reason.
func (g *Registry) Authenticate(ctx context.Context, token, release string) (Runner, error) {
	if token == "" || len(token) > MaxToken {
		return Runner{}, ErrUnauthenticated
	}
	if !ValidRelease(release) {
		release = ""
	}
	return g.store.Authenticate(ctx, HashToken(token), release, g.now())
}
