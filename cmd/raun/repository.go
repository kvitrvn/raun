package main

import (
	"context"
	"errors"
	"fmt"
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
// lacks. No forge offers files, webhooks or events before #32 and #33, which
// declare their operations and detect them here; until then no repository
// can be activated, and the Repositories screen says why.
func missingCapabilities(identity.Forge) []repository.Capability {
	return repository.Preconditions()
}

// newRepositories returns the repository service of forge, nil without a
// forge.
func newRepositories(forge identity.Forge, permissions *identity.PermissionService, workspaces identity.WorkspaceStore, store repository.Store, missing func(identity.Forge) []repository.Capability) *repository.Service {
	if forge == nil {
		return nil
	}
	reach, _ := forge.(repository.Reach)
	return repository.New(repository.Options{
		Forge:      repositoryForge{name: forge.Name(), permissions: permissions},
		Workspaces: repositoryWorkspaces{store: workspaces},
		Store:      store,
		Reach:      reach,
		Missing:    missing(forge),
		Now:        time.Now,
	})
}

// repositoryService is r as the web handler takes it: nil without a forge.
func repositoryService(r *repository.Service) web.RepositoryService {
	if r == nil {
		return nil
	}
	return r
}
