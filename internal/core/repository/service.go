package repository

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	// Events verifies and parses the deliveries of the forge; nil when the
	// forge cannot.
	Events Events
	// Log keeps the keys of the deliveries and the event log; required
	// with Events.
	Log EventStore
	// Files reads the files of a repository; nil when the forge cannot.
	// The service keeps its answers in a cache by repository and commit.
	Files Files
	// Configurations keeps the configuration of each repository; required
	// with Files.
	Configurations ConfigurationStore
	// ReadTimeout bounds a read of a configuration; zero means
	// ReadTimeout.
	ReadTimeout time.Duration
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
	events     Events
	log        EventStore
	files      Files
	configs    ConfigurationStore
	// refreshing serializes the reads of the configuration of each
	// repository, whoever asks: a later read resolves a later head, and
	// stores last.
	refreshing  keyedLocks
	readTimeout time.Duration
	missing     []Capability
	now         func() time.Time
}

// New returns the service of opts.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	var files Files
	if opts.Files != nil {
		files = newFileCache(opts.Files, opts.Forge.Name(), opts.Now)
	}
	return &Service{
		files:       files,
		readTimeout: cmp.Or(opts.ReadTimeout, ReadTimeout),
		configs:     opts.Configurations,
		forge:       opts.Forge,
		workspaces:  opts.Workspaces,
		store:       opts.Store,
		reach:       opts.Reach,
		events:      opts.Events,
		log:         opts.Log,
		missing:     slices.Clone(opts.Missing),
		now:         opts.Now,
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
	// Events are the newest rows of its event log, LogLimit at most,
	// newest first; none without an event store.
	Events []LoggedEvent
	// Configuration is the last read of its configuration; nil when none
	// was read yet.
	Configuration *Configuration
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
	var events []LoggedEvent
	if s.log != nil {
		events, err = s.log.Log(ctx, r.Forge, r.ID, LogLimit)
		if err != nil {
			return View{}, fmt.Errorf("read the event log: %w", err)
		}
	}
	var config *Configuration
	if s.configs != nil {
		c, ok, err := s.configs.Configuration(ctx, r.Forge, r.ID)
		if err != nil {
			return View{}, fmt.Errorf("read the configuration: %w", err)
		}
		if ok {
			config = &c
		}
	}
	return View{Repository: r, Permission: p, Member: member, Events: events, Configuration: config}, nil
}

var (
	// ErrNoFiles: the forge cannot read the files of a repository.
	ErrNoFiles = errors.New("the forge cannot read files")
	// ErrBusy: another read of the configuration of the repository held it
	// past the deadline of the wait; nothing was read.
	ErrBusy = errors.New("another read of the configuration is in progress")
)

// ReadTimeout bounds a read of the configuration of a repository.
const ReadTimeout = 10 * time.Second

// RefreshConfiguration reads the configuration of the active repository of
// the forge with this stable identifier at the head of its default branch,
// as the forge resolves it now, and stores what came of it: configured,
// not configured, or unreadable when the forge could not give it or it is
// larger than MaxConfigurationSize, without its content then. A
// repository that is not active is left as it is. It returns only what
// keeps it from storing the result: ErrNoFiles, ErrNotFound, ErrBusy, the
// end of ctx and the errors of the stores.
func (s *Service) RefreshConfiguration(ctx context.Context, id string) error {
	if s.files == nil || s.configs == nil {
		return ErrNoFiles
	}
	rows, err := s.store.Known(ctx, s.forge.Name(), []string{id})
	if err != nil {
		return fmt.Errorf("read the repository: %w", err)
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	if !rows[0].Active {
		return nil
	}
	// The wait for another read of the repository and the read have a
	// deadline each, never the write of the result.
	waitCtx, cancelWait := context.WithTimeout(ctx, s.readTimeout)
	unlock, err := s.refreshing.lock(waitCtx, id)
	cancelWait()
	if err != nil {
		// Another read held the repository all along: nothing is read,
		// and the caller may ask again.
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	defer unlock()
	// Read again under the lock: the repository may have changed while
	// another read held it.
	rows, err = s.store.Known(ctx, s.forge.Name(), []string{id})
	if err != nil {
		return fmt.Errorf("read the repository: %w", err)
	}
	if len(rows) == 0 || !rows[0].Active {
		return nil
	}
	r := rows[0]
	readCtx, cancel := context.WithTimeout(ctx, s.readTimeout)
	c := s.readConfiguration(readCtx, r)
	cancel()
	if err := ctx.Err(); err != nil && c.State == ConfigurationUnreadable {
		// A read the caller cut short says nothing of the file.
		return err
	}
	if err := s.configs.PutConfiguration(ctx, r.Forge, r.ID, c); err != nil {
		return fmt.Errorf("store the configuration: %w", err)
	}
	return nil
}

// readConfiguration reads the configuration of r.
func (s *Service) readConfiguration(ctx context.Context, r Repository) Configuration {
	c := Configuration{ReadAt: s.now().UTC()}
	commit, err := s.files.Head(ctx, r.ID, r.DefaultBranch)
	switch {
	case errors.Is(err, ErrNoFile):
		c.State = ConfigurationMissing
		return c
	case err != nil:
		c.State, c.Reason = ConfigurationUnreadable, ReasonUnavailable
		return c
	}
	c.Commit = commit
	content, err := s.files.Read(ctx, r.ID, commit, ConfigurationPath, MaxConfigurationSize)
	switch {
	case err == nil:
		c.State, c.Content = ConfigurationConfigured, content
	case errors.Is(err, ErrNoFile):
		c.State = ConfigurationMissing
	case errors.Is(err, ErrFileTooLarge):
		c.State, c.Reason = ConfigurationUnreadable, ReasonTooLarge
	default:
		c.State, c.Reason = ConfigurationUnreadable, ReasonUnavailable
	}
	return c
}

// Active returns the stable identifiers of the active repositories of the
// forge, sorted: those whose configuration a start reads again.
func (s *Service) Active(ctx context.Context) ([]string, error) {
	return s.store.Active(ctx, s.forge.Name())
}

// Receipt is what came of a delivery, and what it asks the caller to do
// after the answer.
type Receipt struct {
	Outcome Outcome
	// Refresh is the stable identifier of a repository whose configuration
	// is to be read again: a push to its default branch, recorded.
	Refresh string
}

// Receive verifies, parses and records a delivery of the forge, header
// giving the headers of its request and body its raw body. It keeps the
// keys of every delivery the forge signed and, for an active repository,
// a row of its event log, in one operation. It changes nothing else: what
// a delivery causes runs after it, such as asking the forge what the App
// reaches on OutcomeInstallation. Errors: ErrNoEvents, ErrSignature,
// ErrMalformed, and those of the store, after which nothing is kept.
func (s *Service) Receive(ctx context.Context, header func(string) string, body []byte) (Receipt, error) {
	if s.events == nil || s.log == nil {
		return Receipt{}, ErrNoEvents
	}
	d, err := s.events.Parse(header, body)
	if err != nil {
		return Receipt{}, err
	}
	sum := sha256.Sum256(body)
	k := Key{Forge: s.forge.Name(), Delivery: d.ID, Digest: hex.EncodeToString(sum[:]), ReceivedAt: s.now().UTC()}
	row := logRow(d, k)
	// Decided before anything is kept: a failure keeps nothing, and the
	// delivery sent again asks again.
	refresh, err := s.pushesDefault(ctx, d)
	if err != nil {
		return Receipt{}, err
	}
	logged, err := s.log.Record(ctx, k, row)
	switch {
	case errors.Is(err, ErrDuplicate):
		return Receipt{Outcome: OutcomeDuplicate}, nil
	case err != nil:
		return Receipt{}, fmt.Errorf("record the delivery: %w", err)
	case d.Kind == KindInstallation:
		return Receipt{Outcome: OutcomeInstallation}, nil
	case !logged:
		return Receipt{Outcome: OutcomeDropped}, nil
	case row.Result == ResultIgnored:
		return Receipt{Outcome: OutcomeIgnored}, nil
	}
	return Receipt{Outcome: OutcomeRecorded, Refresh: refresh}, nil
}

// pushesDefault returns the repository of d when d pushes a commit to its
// default branch, as Raun knows it: its configuration is to be read again.
func (s *Service) pushesDefault(ctx context.Context, d Delivery) (string, error) {
	if s.files == nil || d.Kind != KindPush || d.Push == nil || d.Push.Deleted || d.Push.Tag {
		return "", nil
	}
	rows, err := s.store.Known(ctx, s.forge.Name(), []string{d.Repository})
	if err != nil {
		return "", fmt.Errorf("read the repository: %w", err)
	}
	if len(rows) == 0 || !rows[0].Active || d.Push.Ref != "refs/heads/"+rows[0].DefaultBranch {
		return "", nil
	}
	return d.Repository, nil
}

// logRow is the row of the event log of d, nil for a delivery of no
// repository or of the App itself.
func logRow(d Delivery, k Key) *LoggedEvent {
	if d.Repository == "" || d.Kind == KindInstallation {
		return nil
	}
	row := &LoggedEvent{
		Forge: k.Forge, Repository: d.Repository, Delivery: d.ID, Event: d.Event, Action: d.Action,
		ReceivedAt: k.ReceivedAt, Result: ResultRecorded,
	}
	switch {
	case d.Kind == KindPush && d.Push != nil:
		row.Ref, row.Before, row.After = d.Push.Ref, d.Push.Before, d.Push.After
	case d.Kind == KindPullRequest && d.PullRequest != nil:
		row.Ref, row.Before, row.After = d.PullRequest.HeadRef, d.PullRequest.BaseCommit, d.PullRequest.HeadCommit
	default:
		row.Result = ResultIgnored
	}
	return row
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

// keyedLocks holds one lock per key, while it is held or waited on. Its
// zero value is ready.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

// keyedLock is held while its channel holds a value.
type keyedLock struct {
	held  chan struct{}
	users int
}

// lock takes the lock of key, waiting until ctx ends at most, and returns
// what releases it.
func (k *keyedLocks) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l, ok := k.locks[key]
	if !ok {
		l = &keyedLock{held: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.users++
	k.mu.Unlock()
	leave := func() {
		k.mu.Lock()
		l.users--
		if l.users == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
		return func() {
			<-l.held
			leave()
		}, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}
