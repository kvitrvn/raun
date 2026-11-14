package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// webhook of the App covers every repository it reaches. No forge reads
// files before #33, which declares the operations and detects them here;
// until then no repository can be activated, and the Repositories screen
// says why.
func missingCapabilities(forge identity.Forge) []repository.Capability {
	missing := []repository.Capability{repository.CapabilityFiles}
	if _, ok := forge.(repository.Events); !ok {
		missing = append(missing, repository.CapabilityWebhooks, repository.CapabilityEvents)
	}
	return missing
}

// newRepositories returns the repository service of forge, nil without a
// forge.
func newRepositories(forge identity.Forge, permissions *identity.PermissionService, workspaces identity.WorkspaceStore, store repository.Store, log repository.EventStore, missing func(identity.Forge) []repository.Capability) *repository.Service {
	if forge == nil {
		return nil
	}
	reach, _ := forge.(repository.Reach)
	events, _ := forge.(repository.Events)
	return repository.New(repository.Options{
		Forge:      repositoryForge{name: forge.Name(), permissions: permissions},
		Workspaces: repositoryWorkspaces{store: workspaces},
		Store:      store,
		Reach:      reach,
		Events:     events,
		Log:        log,
		Missing:    missing(forge),
		Now:        time.Now,
	})
}

// reachTimeout bounds one sync of the reach of the App.
const reachTimeout = 2 * time.Minute

// reachSync asks the forge which repositories its App reaches, one sync at
// a time: after the answer to an installation delivery, and once at start,
// which recovers a sync a stop cut short. A request that comes while one
// runs makes one more run afterwards, never more: each run reads the state
// of the forge at that moment, never a payload.
type reachSync struct {
	requests chan struct{}
}

func newReachSync() *reachSync { return &reachSync{requests: make(chan struct{}, 1)} }

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
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-r.requests:
			c, cancel := context.WithTimeout(ctx, reachTimeout)
			lost, err := sync(c)
			cancel()
			switch {
			case errors.Is(err, repository.ErrNoReach):
			case err != nil:
				logger.Warn("sync the repositories the App reaches", slog.String("error", err.Error()))
			case len(lost) > 0:
				logger.Info("repositories the App no longer reaches deactivated", slog.Int("count", len(lost)))
			}
			if synced != nil {
				synced(lost, err)
			}
		}
	}
}

// hookReceiver receives the deliveries of the forge for the web handler, and
// asks for a sync of the reach of the App after an installation delivery.
type hookReceiver struct {
	repositories *repository.Service
	sync         *reachSync
}

func (h hookReceiver) Receive(ctx context.Context, header func(string) string, body []byte) (repository.Outcome, error) {
	outcome, err := h.repositories.Receive(ctx, header, body)
	if err == nil && outcome == repository.OutcomeInstallation {
		h.sync.request()
	}
	return outcome, err
}

// hooks is the receiver of the web handler: nil without a forge that
// receives events.
func hooks(forge identity.Forge, r *repository.Service, sync *reachSync) web.HookReceiver {
	if r == nil {
		return nil
	}
	if _, ok := forge.(repository.Events); !ok {
		return nil
	}
	return hookReceiver{repositories: r, sync: sync}
}

// repositoryService is r as the web handler takes it: nil without a forge.
func repositoryService(r *repository.Service) web.RepositoryService {
	if r == nil {
		return nil
	}
	return r
}
