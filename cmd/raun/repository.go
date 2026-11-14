package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driving/web"
	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// repositoryForge is the forge as the repository context asks it: through
// the permission service, the one place that decides access to repository
// data, on the account's own tokens.
type repositoryForge struct {
	name        string
	permissions *identity.PermissionService
}

func (f repositoryForge) Name() string { return f.name }

// forgeError is err of the identity context as the repository context tells
// it: a forge that cannot answer, or that refuses the tokens of the person.
func forgeError(err error) error {
	switch {
	case errors.Is(err, identity.ErrUnavailable):
		return fmt.Errorf("%w: %w", repository.ErrUnavailable, err)
	case errors.Is(err, identity.ErrUnauthorized), errors.Is(err, identity.ErrRevoked),
		errors.Is(err, identity.ErrNotFound), errors.Is(err, identity.ErrNoIdentity):
		return fmt.Errorf("%w: %w", repository.ErrRefused, err)
	}
	return err
}

func (f repositoryForge) Repositories(ctx context.Context, account string) ([]repository.Listed, error) {
	repos, err := f.permissions.Repositories(ctx, account)
	if err != nil {
		return nil, forgeError(err)
	}
	out := make([]repository.Listed, len(repos))
	for i, r := range repos {
		out[i] = repository.Listed{Forge: r.Forge, ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private}
	}
	return out, nil
}

// Permission maps the permission service. A forge that cannot answer, or
// tokens it refuses, revoked or gone, are errors, as the port says; a token
// the forge refuses for this repository alone is a refusal, none.
func (f repositoryForge) Permission(ctx context.Context, account, id string) (repository.Permission, error) {
	p, err := f.permissions.Permission(ctx, account, identity.Repository{Forge: f.name, ID: id})
	if err != nil && !errors.Is(err, identity.ErrUnauthorized) {
		return repository.PermissionNone, forgeError(err)
	}
	switch p {
	case identity.PermissionDecide:
		return repository.PermissionDecide, nil
	case identity.PermissionView:
		return repository.PermissionView, nil
	default:
		return repository.PermissionNone, nil
	}
}

// repositoryWorkspaces finds the workspaces of an account in the workspace
// store of the identity context.
type repositoryWorkspaces struct {
	store identity.WorkspaceStore
}

func (w repositoryWorkspaces) WorkspaceOf(ctx context.Context, name, account string) (repository.Workspace, error) {
	m, err := w.store.WorkspaceOf(ctx, name, account)
	if errors.Is(err, identity.ErrNotFound) {
		return repository.Workspace{}, repository.ErrNotMember
	}
	if err != nil {
		return repository.Workspace{}, err
	}
	return repository.Workspace{ID: m.Workspace.ID, Name: m.Workspace.Name}, nil
}

// missingCapabilities are the capabilities of repository.Preconditions forge
// lacks. A forge that receives events has webhooks too: on GitHub, the
// webhook of the App covers every repository it reaches.
func missingCapabilities(forge identity.Forge) []repository.Capability {
	var missing []repository.Capability
	if _, ok := forge.(repository.Files); !ok {
		missing = append(missing, repository.CapabilityFiles)
	}
	if _, ok := forge.(repository.Events); !ok {
		missing = append(missing, repository.CapabilityWebhooks, repository.CapabilityEvents)
	}
	return missing
}

// newRepositories returns the repository service of forge, nil without a
// forge.
func newRepositories(forge identity.Forge, permissions *identity.PermissionService, workspaces identity.WorkspaceStore, store repositoryStores, missing func(identity.Forge) []repository.Capability) *repository.Service {
	if forge == nil {
		return nil
	}
	reach, _ := forge.(repository.Reach)
	events, _ := forge.(repository.Events)
	files, _ := forge.(repository.Files)
	return repository.New(repository.Options{
		Forge:          repositoryForge{name: forge.Name(), permissions: permissions},
		Workspaces:     repositoryWorkspaces{store: workspaces},
		Store:          store,
		Reach:          reach,
		Events:         events,
		Log:            store,
		Files:          files,
		Configurations: store,
		Missing:        missing(forge),
		Now:            time.Now,
	})
}

// repositoryStores are the stores of the repository context, on one
// database.
type repositoryStores interface {
	repository.Store
	repository.EventStore
	repository.ConfigurationStore
}

// configRefresh reads again the configuration of the repositories it is
// asked for, one at a time, after the answer to a push to their default
// branch, and of every active repository at start, which recovers what a
// stop cut short. A repository asked for twice before its read is read
// once: each read resolves the head of the default branch then.
type configRefresh struct {
	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
}

func newConfigRefresh() *configRefresh {
	return &configRefresh{pending: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// request asks for a read of the configuration of the repository with this
// stable identifier, without waiting.
func (c *configRefresh) request(id string) {
	c.mu.Lock()
	c.pending[id] = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// take returns the repositories asked for, sorted, and forgets them.
func (c *configRefresh) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := slices.Sorted(maps.Keys(c.pending))
	clear(c.pending)
	return ids
}

// run reads on each request until done or the end of ctx, through refresh,
// and tells refreshed, when set, of each read.
func (c *configRefresh) run(ctx context.Context, logger *slog.Logger, refresh func(context.Context, string) error, refreshed func(string, error), done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-c.wake:
		}
		for _, id := range c.take() {
			err := refresh(ctx, id)
			if err != nil && !errors.Is(err, repository.ErrNotFound) && ctx.Err() == nil {
				logger.Warn("read the configuration of a repository", slog.String("repository", id), slog.String("error", err.Error()))
			}
			if refreshed != nil {
				refreshed(id, err)
			}
		}
	}
}

// reachTimeout bounds one sync of the reach of the App.
const reachTimeout = 2 * time.Minute

// The waits before a failed sync is tried again: doubled after each failure
// in a row, up to the last.
const (
	reachRetryFirst = 30 * time.Second
	reachRetryMax   = 30 * time.Minute
)

// reachSync asks the forge which repositories its App reaches, one sync at
// a time: after the answer to an installation delivery, and once at start,
// which recovers a sync a stop cut short. A request that comes while one
// runs makes one more run afterwards, never more: each run reads the state
// of the forge at that moment, never a payload. A sync that fails is tried
// again after a wait that grows with each failure in a row, until one
// succeeds, a request comes, or the server stops.
type reachSync struct {
	requests          chan struct{}
	retryFirst, retry time.Duration // the first wait, and the longest
}

func newReachSync() *reachSync {
	return &reachSync{requests: make(chan struct{}, 1), retryFirst: reachRetryFirst, retry: reachRetryMax}
}

// request asks for a sync, without waiting.
func (r *reachSync) request() {
	select {
	case r.requests <- struct{}{}:
	default:
	}
}

// run syncs on each request until done or the end of ctx, through sync, and
// tells synced, when set, what each sync lost.
func (r *reachSync) run(ctx context.Context, logger *slog.Logger, sync func(context.Context) ([]string, error), synced func([]string, error), done <-chan struct{}) {
	var retry <-chan time.Time
	wait := r.retryFirst
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-r.requests:
		case <-retry:
		}
		retry = nil
		c, cancel := context.WithTimeout(ctx, reachTimeout)
		lost, err := sync(c)
		cancel()
		switch {
		case errors.Is(err, repository.ErrNoReach):
		case err != nil && ctx.Err() == nil:
			logger.Warn("sync the repositories the App reaches; trying again later",
				slog.String("error", err.Error()), slog.Duration("wait", wait))
			retry = time.After(wait)
			wait = min(2*wait, r.retry)
		case err == nil:
			wait = r.retryFirst
			if len(lost) > 0 {
				logger.Info("repositories the App no longer reaches deactivated", slog.Int("count", len(lost)))
			}
		}
		if synced != nil {
			synced(lost, err)
		}
	}
}

// hookReceiver receives the deliveries of the forge for the web handler, and
// asks for a sync of the reach of the App after an installation delivery.
type hookReceiver struct {
	repositories *repository.Service
	sync         *reachSync
	configs      *configRefresh
}

func (h hookReceiver) Receive(ctx context.Context, header func(string) string, body []byte) (repository.Outcome, error) {
	got, err := h.repositories.Receive(ctx, header, body)
	if err != nil {
		return "", err
	}
	if got.Outcome == repository.OutcomeInstallation {
		h.sync.request()
	}
	if got.Refresh != "" {
		h.configs.request(got.Refresh)
	}
	return got.Outcome, nil
}

// hooks is the receiver of the web handler: nil without a forge that
// receives events.
func hooks(forge identity.Forge, r *repository.Service, sync *reachSync, configs *configRefresh) web.HookReceiver {
	if r == nil {
		return nil
	}
	if _, ok := forge.(repository.Events); !ok {
		return nil
	}
	return hookReceiver{repositories: r, sync: sync, configs: configs}
}

// repositoryService is r as the web handler takes it: nil without a forge.
func repositoryService(r *repository.Service) web.RepositoryService {
	if r == nil {
		return nil
	}
	return r
}
