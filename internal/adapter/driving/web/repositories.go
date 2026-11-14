package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// RepositoryService lists the repositories of the forge, activates and
// deactivates them, and opens the page of one.
type RepositoryService interface {
	Missing() []repository.Capability
	List(ctx context.Context, account, query string) ([]repository.Entry, error)
	Activate(ctx context.Context, account, workspace, id string) (repository.Repository, error)
	Deactivate(ctx context.Context, account, workspace, fullName string) (repository.Repository, error)
	Open(ctx context.Context, account, workspace, fullName string) (repository.View, error)
	InWorkspace(ctx context.Context, account, workspace string) ([]repository.Repository, error)
}

// maxQuery bounds the search of the Repositories screen.
const maxQuery = 200

// repositoryRoutes registers the routes of repositories. The page of a
// repository is /<workspace>/<owner>/<name>, at its current full name; its
// workspace wildcard is {name}, as on the page of the workspace, so the
// switcher marks it current.
func (s *Server) repositoryRoutes() {
	s.route(s.app, "POST /repositories", s.requireAccount(http.HandlerFunc(s.repositoryActivate)))
	s.route(s.app, "GET /{name}/{owner}/{repo}", http.HandlerFunc(s.repositoryPage))
	s.route(s.app, "POST /{name}/{owner}/{repo}/deactivate", s.requireAccount(http.HandlerFunc(s.repositoryDeactivate)))
}

// repositoryPath is the page of the repository with this full name in the
// workspace with this name.
func repositoryPath(workspace, fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return "/" + url.PathEscape(workspace) + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// forgeHref is the page of the repository on the forge.
func (s *Server) forgeHref(fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return strings.TrimSuffix(s.forgeURL, "/") + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func missingNames(missing []repository.Capability) []string {
	var out []string
	for _, c := range missing {
		out = append(out, string(c))
	}
	return out
}

// forgeFailed answers a request the forge could not serve, and reports
// whether err was one.
func (s *Server) forgeFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, repository.ErrUnavailable):
		s.warn(r, "forge unavailable", err.Error())
		s.render(w, r, http.StatusServiceUnavailable, ui.ErrorPage(s.page(r, "Forge unavailable", ""), ui.ErrorProps{
			Status: http.StatusServiceUnavailable, Title: "The forge does not answer",
			Message: "Raun cannot ask the forge what you may reach right now. Try again in a moment.", BackHref: "/", BackLabel: "Repositories",
		}))
		return true
	case errors.Is(err, repository.ErrRefused):
		s.warn(r, "forge refused the tokens", err.Error())
		s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Log in again", ""), ui.ErrorProps{
			Status: http.StatusForbidden, Title: "The forge refuses your session",
			Message: "Log in again so that Raun can ask the forge what you may reach.", BackHref: "/login", BackLabel: "Log in",
		}))
		return true
	}
	return false
}

// repositoriesPage is the Repositories screen of the account: the
// repositories the forge lists for it, searched by full name.
func (s *Server) repositoriesPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if len(query) > maxQuery {
		query = query[:maxQuery]
	}
	s.repositoriesForm(w, r, http.StatusOK, query, "")
}

func (s *Server) repositoriesForm(w http.ResponseWriter, r *http.Request, status int, query, problem string) {
	entries, err := s.repositories.List(r.Context(), account(r), query)
	if s.forgeFailed(w, r, err) {
		return
	}
	if err != nil {
		s.fail(w, r, "list the repositories", err)
		return
	}
	memberships, err := s.workspaces.List(r.Context(), account(r))
	if err != nil {
		s.fail(w, r, "list the workspaces", err)
		return
	}
	props := ui.RepositoriesProps{Query: query, Missing: missingNames(s.repositories.Missing()), Error: problem}
	for _, m := range memberships {
		props.Workspaces = append(props.Workspaces, m.Workspace.Name)
	}
	for _, e := range entries {
		row := ui.RepositoryRow{ID: e.Listed.ID, FullName: e.Listed.FullName, ForgeHref: s.forgeHref(e.Listed.FullName), Private: e.Listed.Private}
		if e.Known {
			// The forge lets the account read it: it may see its page,
			// in whichever workspace it belongs to.
			row.State = string(e.Repository.State())
			row.Workspace = e.Repository.Workspace.Name
			if e.Repository.FullName != "" {
				row.Href = repositoryPath(row.Workspace, e.Repository.FullName)
			}
		}
		props.Rows = append(props.Rows, row)
	}
	s.render(w, r, status, ui.RepositoriesPage(s.page(r, "Repositories", "/"), props))
}

// repositoryActivate activates a repository of the forge, by its stable
// identifier, in a workspace of the account, or activates it again there.
// What Raun keeps of it comes from the forge, never from the form.
func (s *Server) repositoryActivate(w http.ResponseWriter, r *http.Request) {
	form, ok := formValues(w, r, "workspace", "repository")
	if !ok {
		return
	}
	repo, err := s.repositories.Activate(r.Context(), account(r), form["workspace"], form["repository"])
	var missing *repository.MissingError
	again := func(status int, problem string) {
		s.repositoriesForm(w, r, status, "", problem)
	}
	switch {
	case err == nil:
		redirect(w, repositoryPath(repo.Workspace.Name, repo.FullName))
	case s.forgeFailed(w, r, err):
	case errors.As(err, &missing):
		again(http.StatusConflict, "The forge lacks what activation needs: "+strings.Join(missingNames(missing.Missing), ", ")+".")
	case errors.Is(err, repository.ErrNotMember):
		again(http.StatusUnprocessableEntity, "Choose a workspace you belong to.")
	case errors.Is(err, repository.ErrNotFound):
		again(http.StatusUnprocessableEntity, "The forge lists no such repository for you.")
	case errors.Is(err, repository.ErrNotDecider):
		s.warn(r, "activation refused", "the forge does not let the account decide")
		again(http.StatusForbidden, "Only a person with write access to the repository on the forge activates it.")
	case errors.Is(err, repository.ErrElsewhere):
		again(http.StatusConflict, "This repository belongs to another workspace: a repository is activated in one workspace only.")
	default:
		s.fail(w, r, "activate a repository", err)
	}
}

// repositoryTarget reads the workspace and the full name of a repository
// path. Only a name of the namespace can be a workspace: anything else is
// not found, without asking for a login.
func repositoryTarget(r *http.Request) (workspace, fullName string, ok bool) {
	workspace = r.PathValue("name")
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	if identity.CheckName(workspace) != nil || owner == "" || name == "" {
		return "", "", false
	}
	return workspace, owner + "/" + name, true
}

// repositoryPage shows a repository to whoever the forge lets read it,
// member of its workspace or not; anyone else gets the page of an address
// that does not exist, as for an old name.
func (s *Server) repositoryPage(w http.ResponseWriter, r *http.Request) {
	workspace, fullName, ok := repositoryTarget(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.requireAccount(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.repositories.Open(r.Context(), account(r), workspace, fullName)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			s.notFound(w, r)
			return
		case s.forgeFailed(w, r, err):
			return
		case err != nil:
			s.fail(w, r, "open a repository", err)
			return
		}
		rp := v.Repository
		s.render(w, r, http.StatusOK, ui.RepositoryPage(s.page(r, rp.FullName, ""), ui.RepositoryProps{
			Workspace:     rp.Workspace.Name,
			ID:            rp.ID,
			FullName:      rp.FullName,
			ForgeHref:     s.forgeHref(rp.FullName),
			DefaultBranch: rp.DefaultBranch,
			Private:       rp.Private,
			State:         string(rp.State()),
			CanDecide:     v.CanDecide(),
			Missing:       missingNames(s.repositories.Missing()),
		}))
	})).ServeHTTP(w, r)
}

// repositoryDeactivate deactivates a repository, on behalf of a member of
// its workspace whom the forge lets decide on it.
func (s *Server) repositoryDeactivate(w http.ResponseWriter, r *http.Request) {
	workspace, fullName, ok := repositoryTarget(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	if _, ok := formValues(w, r); !ok {
		return
	}
	repo, err := s.repositories.Deactivate(r.Context(), account(r), workspace, fullName)
	switch {
	case err == nil:
		redirect(w, repositoryPath(repo.Workspace.Name, repo.FullName))
	case s.forgeFailed(w, r, err):
	case errors.Is(err, repository.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, repository.ErrNotMember), errors.Is(err, repository.ErrNotDecider):
		s.warn(r, "deactivation refused", err.Error())
		s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Forbidden", ""), ui.ErrorProps{
			Status: http.StatusForbidden, Title: "Only a member who decides deactivates",
			Message:  "A member of the workspace with write access to the repository on the forge deactivates it.",
			BackHref: repositoryPath(workspace, fullName), BackLabel: "Back to the repository",
		}))
	default:
		s.fail(w, r, "deactivate a repository", err)
	}
}

// workspaceRepositories are the repositories of the workspace with this name
// the forge lets the account read, as the page of the workspace shows them,
// or why they cannot be shown.
func (s *Server) workspaceRepositories(r *http.Request, workspace string) ([]ui.RepositoryLink, string, error) {
	if s.repositories == nil {
		return nil, "", nil
	}
	repos, err := s.repositories.InWorkspace(r.Context(), account(r), workspace)
	switch {
	case errors.Is(err, repository.ErrUnavailable):
		s.warn(r, "forge unavailable", err.Error())
		return nil, "The forge does not answer: the repositories of this workspace cannot be shown right now.", nil
	case errors.Is(err, repository.ErrRefused):
		s.warn(r, "forge refused the tokens", err.Error())
		return nil, "The forge refuses your session: log in again to see the repositories of this workspace.", nil
	case err != nil:
		return nil, "", err
	}
	var out []ui.RepositoryLink
	for _, rp := range repos {
		if rp.FullName == "" {
			continue
		}
		out = append(out, ui.RepositoryLink{FullName: rp.FullName, Href: repositoryPath(rp.Workspace.Name, rp.FullName), State: string(rp.State())})
	}
	return out, "", nil
}
