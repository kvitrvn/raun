package github

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// installation is an installation of the App the person can reach.
type installation struct {
	ID          int64      `json:"id"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

// repository is a repository as GitHub lists it for a person, with that
// person's rights on it.
type repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Permissions   struct {
		Admin    bool `json:"admin"`
		Maintain bool `json:"maintain"`
		Push     bool `json:"push"`
		Triage   bool `json:"triage"`
		Pull     bool `json:"pull"`
	} `json:"permissions"`
}

// permission maps the rights of the person on r, by the table of
// docs/forge.md: admin, maintain and write decide; triage and read view.
func (r repository) permission() identity.Permission {
	p := r.Permissions
	switch {
	case p.Admin || p.Maintain || p.Push:
		return identity.PermissionDecide
	case p.Triage || p.Pull:
		return identity.PermissionView
	default:
		return identity.PermissionNone
	}
}

// walk gives each repository the person of t reaches through an
// installation of the App to visit, until visit returns false. GitHub
// answers with the intersection of the person's access and the App's, so a
// repository the App is not installed on, or the person cannot see, never
// comes. An installation removed during the walk is skipped; any other
// failure stops it with an error, never a shorter list.
func (a *App) walk(ctx context.Context, t identity.Tokens, visit func(repository) bool) error {
	auth := bearer(t.Access)
	var installations []int64
	err := a.pages(ctx, "/user/installations", nil, auth, func(req request, r *response) (int, error) {
		if r.status != http.StatusOK {
			return 0, status(req, r)
		}
		var out struct {
			Installations []installation `json:"installations"`
		}
		if err := decode(req, r, &out); err != nil {
			return 0, err
		}
		for _, in := range out.Installations {
			if in.ID <= 0 {
				return 0, fail(req, identity.ErrUnavailable, "malformed response")
			}
			// A suspended installation reaches nothing.
			if in.SuspendedAt == nil {
				installations = append(installations, in.ID)
			}
		}
		return len(out.Installations), nil
	})
	if err != nil {
		return err
	}
	for _, id := range installations {
		done := false
		path := "/user/installations/" + strconv.FormatInt(id, 10) + "/repositories"
		err := a.pages(ctx, path, nil, auth, func(req request, r *response) (int, error) {
			if done {
				return 0, nil
			}
			switch r.status {
			case http.StatusOK:
			case http.StatusNotFound:
				// Removed since it was listed: it reaches nothing.
				return 0, nil
			default:
				return 0, status(req, r)
			}
			var out struct {
				Repositories []repository `json:"repositories"`
			}
			if err := decode(req, r, &out); err != nil {
				return 0, err
			}
			for _, repo := range out.Repositories {
				if repo.ID <= 0 || repo.FullName == "" || repo.DefaultBranch == "" {
					return 0, fail(req, identity.ErrUnavailable, "malformed response")
				}
				if !visit(repo) {
					done = true
					return 0, nil
				}
			}
			return len(out.Repositories), nil
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return nil
}

// Repositories implements identity.Forge: the repositories the person of t
// reaches through the installations of the App, sorted by full name.
func (a *App) Repositories(ctx context.Context, t identity.Tokens) ([]identity.Repository, error) {
	seen := map[int64]bool{}
	var out []identity.Repository
	err := a.walk(ctx, t, func(r repository) bool {
		if !seen[r.ID] && r.permission() != identity.PermissionNone {
			seen[r.ID] = true
			out = append(out, identity.Repository{
				Forge:         Name,
				ID:            strconv.FormatInt(r.ID, 10),
				FullName:      r.FullName,
				DefaultBranch: r.DefaultBranch,
				Private:       r.Private,
			})
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(x, y identity.Repository) int {
		if c := strings.Compare(x.FullName, y.FullName); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out, nil
}

// Permission implements identity.Forge: the rights of the person of t on
// repo, found by its stable identifier among the repositories the person
// reaches through the App; PermissionNone when it is not among them. It
// gives PermissionNone with every error.
func (a *App) Permission(ctx context.Context, t identity.Tokens, repo identity.Repository) (identity.Permission, error) {
	id, err := strconv.ParseInt(repo.ID, 10, 64)
	if err != nil || id <= 0 || repo.Forge != Name {
		// Not a repository of GitHub: nobody reaches it through this App.
		return identity.PermissionNone, nil
	}
	p := identity.PermissionNone
	err = a.walk(ctx, t, func(r repository) bool {
		if r.ID != id {
			return true
		}
		p = r.permission()
		return false
	})
	if err != nil {
		return identity.PermissionNone, err
	}
	return p, nil
}
