package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Options configure a Service.
type Options struct {
	Forge      Forge
	Workspaces Workspaces
	Store      Store
	// Reach tells which repositories the App reaches; nil when the forge
	// cannot.
	Reach Reach
	// Missing are the capabilities of Preconditions() the forge lacks.
	Missing []Capability
	// Now gives the time of a change; nil means time.Now.
	Now func() time.Time
}

// Service applies the rules of repositories: listing, activation,
// deactivation, the page of a repository and the reach of the App. It holds
// no state of its own and is safe for concurrent use.
type Service struct {
	forge      Forge
	workspaces Workspaces
	store      Store
	reach      Reach
	missing    []Capability
	now        func() time.Time
}

// New returns the service of opts.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		forge:      opts.Forge,
		workspaces: opts.Workspaces,
		store:      opts.Store,
		reach:      opts.Reach,
		missing:    slices.Clone(opts.Missing),
		now:        opts.Now,
	}
}

// Missing returns the capabilities activation needs that the forge lacks;
// none when a repository can be activated.
func (s *Service) Missing() []Capability {
	return slices.Clone(s.missing)
}

// Entry is a repository the forge lists for a person, and what Raun knows
// of it: Known is false for a repository never activated.
type Entry struct {
	Listed     Listed
	Known      bool
	Repository Repository
}

// List returns the repositories the account reaches on the forge whose full
// name holds query, ignoring case, sorted by full name, with what Raun knows
// of each. It refreshes the snapshots of those Raun knows, whatever the
// query.
func (s *Service) List(ctx context.Context, account, query string) ([]Entry, error) {
	listed, err := s.forge.Repositories(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("list the repositories of the forge: %w", err)
	}
	known, err := s.refresh(ctx, listed)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var out []Entry
	for _, l := range listed {
		if query != "" && !strings.Contains(strings.ToLower(l.FullName), query) {
			continue
		}
		r, ok := known[l.ID]
		out = append(out, Entry{Listed: l, Known: ok, Repository: r})
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return strings.Compare(a.Listed.FullName, b.Listed.FullName) })
	return out, nil
}

// refresh stores the snapshots of the listed repositories Raun knows when
// they changed, and returns what Raun knows of them, by identifier.
func (s *Service) refresh(ctx context.Context, listed []Listed) (map[string]Repository, error) {
	ids := make([]string, len(listed))
	for i, l := range listed {
		ids[i] = l.ID
	}
	rows, err := s.store.Known(ctx, s.forge.Name(), ids)
	if err != nil {
		return nil, fmt.Errorf("read the known repositories: %w", err)
	}
	known := map[string]Repository{}
	for _, r := range rows {
		known[r.ID] = r
	}
	var changed []Listed
	for _, l := range listed {
		r, ok := known[l.ID]
		if !ok || (r.FullName == l.FullName && r.DefaultBranch == l.DefaultBranch && r.Private == l.Private) {
			continue
		}
		changed = append(changed, l)
	}
	if len(changed) == 0 {
		return known, nil
	}
	if err := s.store.Refresh(ctx, changed); err != nil {
		return nil, fmt.Errorf("refresh the repositories: %w", err)
	}
	// Read again: a name taken from one repository empties another one.
	rows, err = s.store.Known(ctx, s.forge.Name(), ids)
	if err != nil {
		return nil, fmt.Errorf("read the known repositories: %w", err)
	}
	clear(known)
	for _, r := range rows {
		known[r.ID] = r
	}
	return known, nil
}

// Activate activates the repository of the forge with this stable
// identifier in the workspace with this name, on behalf of account, or
// activates it again there. The account must belong to the workspace, the
// forge must offer the capabilities of Preconditions, list the repository
// for the account and let it decide on it; the snapshots come from that
// listing, never from the caller. Errors: ErrNotMember, a MissingError,
// ErrNotFound when the forge does not list it for the account,
// ErrNotDecider, ErrElsewhere, and those of the forge.
func (s *Service) Activate(ctx context.Context, account, workspace, id string) (Repository, error) {
	ws, err := s.member(ctx, workspace, account)
	if err != nil {
		return Repository{}, err
	}
	if len(s.missing) > 0 {
		return Repository{}, &MissingError{Missing: s.Missing()}
	}
	listed, err := s.forge.Repositories(ctx, account)
	if err != nil {
		return Repository{}, fmt.Errorf("list the repositories of the forge: %w", err)
	}
	i := slices.IndexFunc(listed, func(l Listed) bool { return l.ID == id })
	if id == "" || i < 0 {
		return Repository{}, ErrNotFound
	}
	if err := s.decide(ctx, account, id); err != nil {
		return Repository{}, err
	}
	l := listed[i]
	r := Repository{
		Forge:         s.forge.Name(),
		ID:            l.ID,
		Workspace:     ws,
		FullName:      l.FullName,
		DefaultBranch: l.DefaultBranch,
		Private:       l.Private,
		Active:        true,
		Reachable:     true,
		ChangedBy:     account,
		ChangedAt:     s.now().UTC(),
	}
	if err := s.store.Activate(ctx, r); err != nil {
		return Repository{}, err
	}
	return r, nil
}

// Deactivate deactivates the repository with this full name in the
// workspace with this name, on behalf of account, who must belong to the
// workspace and decide on the repository on the forge. Its knowledge, its
// runs and its event log stay. Errors: ErrNotMember, ErrNotFound,
// ErrNotDecider and those of the forge.
func (s *Service) Deactivate(ctx context.Context, account, workspace, fullName string) (Repository, error) {
	ws, err := s.member(ctx, workspace, account)
	if err != nil {
		return Repository{}, err
	}
	r, err := s.find(ctx, ws.Name, fullName)
	if err != nil {
		return Repository{}, err
	}
	if err := s.decide(ctx, account, r.ID); err != nil {
		return Repository{}, err
	}
	at := s.now().UTC()
	if err := s.store.Deactivate(ctx, ws.ID, r.Forge, r.ID, account, at); err != nil {
		return Repository{}, err
	}
	r.Active, r.ChangedBy, r.ChangedAt = false, account, at
	return r, nil
}

// View is a repository as a person sees its page.
type View struct {
	Repository Repository
	// Permission is what the forge lets the person do on it, view at
	// least.
	Permission Permission
	// Member: the person belongs to the workspace of the repository.
	Member bool
}

// CanDecide reports whether the person may activate or deactivate the
// repository: a member of its workspace whom the forge lets decide.
func (v View) CanDecide() bool {
	return v.Member && v.Permission == PermissionDecide
}

// Open returns the repository with this full name in the workspace with
// this name, as account sees it. The forge decides who sees it, member of
// the workspace or not; ErrNotFound when there is no such repository or the
// forge does not let the account see it, so a stranger learns nothing.
func (s *Service) Open(ctx context.Context, account, workspace, fullName string) (View, error) {
	r, err := s.find(ctx, workspace, fullName)
	if err != nil {
		return View{}, err
	}
	p, err := s.forge.Permission(ctx, account, r.ID)
	if err != nil {
		return View{}, fmt.Errorf("ask the permission: %w", err)
	}
	if p != PermissionView && p != PermissionDecide {
		return View{}, ErrNotFound
	}
	member := true
	if _, err := s.member(ctx, workspace, account); errors.Is(err, ErrNotMember) {
		member = false
	} else if err != nil {
		return View{}, err
	}
	return View{Repository: r, Permission: p, Member: member}, nil
}

// InWorkspace returns the repositories of the workspace with this name that
// the forge lists for account, sorted by full name: a member never learns of
// a repository the forge hides from them. ErrNotMember when the account does
// not belong to the workspace.
func (s *Service) InWorkspace(ctx context.Context, account, workspace string) ([]Repository, error) {
	ws, err := s.member(ctx, workspace, account)
	if err != nil {
		return nil, err
	}
	listed, err := s.forge.Repositories(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("list the repositories of the forge: %w", err)
	}
	if _, err := s.refresh(ctx, listed); err != nil {
		return nil, err
	}
	rows, err := s.store.InWorkspace(ctx, ws.ID)
	if err != nil {
		return nil, fmt.Errorf("list the repositories of the workspace: %w", err)
	}
	seen := map[string]bool{}
	for _, l := range listed {
		seen[l.ID] = true
	}
	var out []Repository
	for _, r := range rows {
		if r.Forge == s.forge.Name() && seen[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}

// SyncReach asks the forge which repositories its App reaches, and makes
// every other repository Raun knows unreachable and not active. An answer
// that is not complete changes nothing; a repository the App reaches again
// becomes reachable and stays deactivated until a person activates it. It
// returns the identifiers of the repositories that became unreachable.
// ErrNoReach when the forge cannot tell.
func (s *Service) SyncReach(ctx context.Context) ([]string, error) {
	if s.reach == nil {
		return nil, ErrNoReach
	}
	reached, err := s.reach.Reached(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask which repositories the App reaches: %w", err)
	}
	lost, err := s.store.SetReach(ctx, s.forge.Name(), reached)
	if err != nil {
		return nil, fmt.Errorf("store the reach of the App: %w", err)
	}
	return lost, nil
}

// member returns the workspace with this name when account belongs to it.
func (s *Service) member(ctx context.Context, workspace, account string) (Workspace, error) {
	ws, err := s.workspaces.WorkspaceOf(ctx, workspace, account)
	if errors.Is(err, ErrNotMember) {
		return Workspace{}, ErrNotMember
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("find the workspace: %w", err)
	}
	return ws, nil
}

// find returns the repository of the forge with this full name in the
// workspace with this name.
func (s *Service) find(ctx context.Context, workspace, fullName string) (Repository, error) {
	if fullName == "" {
		return Repository{}, ErrNotFound
	}
	r, err := s.store.Find(ctx, workspace, s.forge.Name(), fullName)
	if errors.Is(err, ErrNotFound) {
		return Repository{}, ErrNotFound
	}
	if err != nil {
		return Repository{}, fmt.Errorf("find the repository: %w", err)
	}
	return r, nil
}

// decide checks that the forge lets account decide on the repository with
// this identifier.
func (s *Service) decide(ctx context.Context, account, id string) error {
	p, err := s.forge.Permission(ctx, account, id)
	if err != nil {
		return fmt.Errorf("ask the permission: %w", err)
	}
	if p != PermissionDecide {
		return ErrNotDecider
	}
	return nil
}
