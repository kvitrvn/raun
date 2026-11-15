package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/job"
)

var _ job.Clone = (*App)(nil)

// cloneUsername is the user name Git sends with an installation token.
const cloneUsername = "x-access-token"

// errNotRepository: the identifier is not one GitHub gives a repository.
var errNotRepository = errors.New("not the identifier of a GitHub repository")

// MintClone implements job.Clone: a new installation token of the
// repository alone with Contents read, minted apart from the tokens the
// adapter caches for the reads of the server, so that revoking it leaves
// them. GitHub makes it last one hour.
func (a *App) MintClone(ctx context.Context, repo string) (job.Credential, error) {
	id, ok := parseRepositoryID(repo)
	if !ok {
		return job.Credential{}, fmt.Errorf("github: clone credential: %q: %w", repo, errNotRepository)
	}
	s, err := a.readScope(ctx, id)
	if err != nil {
		return job.Credential{}, err
	}
	t, repos, err := a.requestToken(ctx, s)
	if err != nil {
		return job.Credential{}, err
	}
	// GitHub says which repositories the token covers: exactly this one,
	// or the token is not used and is revoked at once.
	if len(repos) != 1 || repos[0].ID != id || !validFullName(repos[0].FullName) {
		_ = a.revokeToken(ctx, t.token)
		return job.Credential{}, fmt.Errorf("github: clone credential of %d: %w: the token does not cover this repository alone", id, identity.ErrUnavailable)
	}
	return job.Credential{
		URL:      a.web + "/" + repos[0].FullName + ".git",
		FullName: repos[0].FullName,
		Username: cloneUsername,
		Password: job.Secret(t.token.Reveal()),
		Expires:  t.expires,
	}, nil
}

// validFullName reports whether name is the full name of a repository as
// GitHub writes it: an owner and a name of letters, digits, '.', '-' and
// '_', neither of them "." nor "..".
func validFullName(name string) bool {
	owner, repo, ok := strings.Cut(name, "/")
	if !ok {
		return false
	}
	for _, part := range []string{owner, repo} {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

// RevokeClone implements job.Clone: DELETE /installation/token under the
// token itself. A token GitHub already refuses is revoked.
func (a *App) RevokeClone(ctx context.Context, c job.Credential) error {
	return a.revokeToken(ctx, identity.Secret(c.Password.Reveal()))
}

func (a *App) revokeToken(ctx context.Context, token identity.Secret) error {
	const path = "/installation/token"
	req := request{
		method: http.MethodDelete,
		url:    a.api + path,
		path:   path,
		auth:   bearer(token),
		// Revoking twice revokes once.
		retry: true,
	}
	r, err := a.do(ctx, req)
	if err != nil {
		return err
	}
	switch r.status {
	case http.StatusNoContent, http.StatusUnauthorized:
		return nil
	}
	return status(req, r)
}
