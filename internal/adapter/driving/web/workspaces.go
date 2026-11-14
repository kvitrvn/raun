package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
	"github.com/kvitrvn/raun/internal/core/identity"
)

// WorkspaceService creates workspaces, lists those of an account and manages
// their members.
type WorkspaceService interface {
	Create(ctx context.Context, actor, name string) (identity.Workspace, error)
	List(ctx context.Context, actor string) ([]identity.Membership, error)
	Open(ctx context.Context, actor, name string) (identity.Membership, []identity.Member, error)
	AddMember(ctx context.Context, actor, name, account string, role identity.Role) error
}

// workspaceRoutes registers the routes of workspaces. A workspace lives at
// /<name>, the name of the namespace.
func (s *Server) workspaceRoutes() {
	s.route(s.app, "GET /workspaces", s.requireAccount(http.HandlerFunc(s.workspacesPage)))
	s.route(s.app, "POST /workspaces", s.requireAccount(http.HandlerFunc(s.workspaceCreate)))
	s.route(s.app, "GET /{name}", http.HandlerFunc(s.workspacePage))
	s.route(s.app, "POST /{name}/members", s.requireAccount(http.HandlerFunc(s.memberAdd)))
}

// account is the identifier of the account of the request; requireAccount
// guarantees one.
func account(r *http.Request) string {
	sess, _ := sessionFrom(r.Context())
	return sess.Account
}

// formValues parses the URL-encoded form of r and returns the value of each
// field, exactly one each, or answers the request and returns false.
func formValues(w http.ResponseWriter, r *http.Request, fields ...string) (map[string]string, bool) {
	if err := r.ParseForm(); err != nil {
		if IsBodyTooLarge(err) {
			writeText(w, http.StatusRequestEntityTooLarge, "request body too large")
			return nil, false
		}
		writeText(w, http.StatusBadRequest, "malformed form")
		return nil, false
	}
	out := map[string]string{}
	for _, f := range fields {
		values := r.PostForm[f]
		if len(values) != 1 {
			writeText(w, http.StatusBadRequest, "malformed form")
			return nil, false
		}
		out[f] = values[0]
	}
	return out, true
}

// workspacesPage lists the workspaces of the account, with the form that
// creates one.
func (s *Server) workspacesPage(w http.ResponseWriter, r *http.Request) {
	s.workspacesForm(w, r, http.StatusOK, "", "")
}

func (s *Server) workspacesForm(w http.ResponseWriter, r *http.Request, status int, name, problem string) {
	list, err := s.workspaces.List(r.Context(), account(r))
	if err != nil {
		s.fail(w, r, "list the workspaces", err)
		return
	}
	var rows []ui.WorkspaceRow
	for _, m := range list {
		rows = append(rows, ui.WorkspaceRow{
			Name:     m.Workspace.Name,
			Href:     "/" + m.Workspace.Name,
			Role:     string(m.Role),
			Personal: m.Workspace.Personal,
		})
	}
	s.render(w, r, status, ui.WorkspacesPage(s.page(r, "Workspaces", ""), ui.WorkspacesProps{Rows: rows, Name: name, Error: problem}))
}

// workspaceCreate creates a shared workspace owned by the account.
func (s *Server) workspaceCreate(w http.ResponseWriter, r *http.Request) {
	form, ok := formValues(w, r, "name")
	if !ok {
		return
	}
	ws, err := s.workspaces.Create(r.Context(), account(r), form["name"])
	switch {
	case errors.Is(err, identity.ErrInvalidName):
		s.workspacesForm(w, r, http.StatusUnprocessableEntity, form["name"], reason(err, identity.ErrInvalidName))
		return
	case errors.Is(err, identity.ErrReservedName):
		s.workspacesForm(w, r, http.StatusUnprocessableEntity, form["name"], reason(err, identity.ErrReservedName))
		return
	case errors.Is(err, identity.ErrNameTaken):
		s.workspacesForm(w, r, http.StatusConflict, form["name"], "This name is taken by an account or a workspace: choose another one.")
		return
	case err != nil:
		s.fail(w, r, "create a workspace", err)
		return
	}
	redirect(w, "/"+url.PathEscape(ws.Name))
}

// workspacePage shows a workspace to one of its members: its members, and
// to an owner the form that adds one. Anyone else gets the page of an
// address that does not exist.
func (s *Server) workspacePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Only a name of the namespace can be a workspace: favicon.ico and the
	// like are not found, without asking for a login.
	if identity.CheckName(name) != nil {
		s.notFound(w, r)
		return
	}
	s.requireAccount(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.workspaceForm(w, r, http.StatusOK, name, "", identity.RoleMember, "")
	})).ServeHTTP(w, r)
}

func (s *Server) workspaceForm(w http.ResponseWriter, r *http.Request, status int, name, member string, role identity.Role, problem string) {
	m, members, err := s.workspaces.Open(r.Context(), account(r), name)
	if errors.Is(err, identity.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, "open a workspace", err)
		return
	}
	var rows []ui.MemberRow
	for _, mb := range members {
		rows = append(rows, ui.MemberRow{Name: mb.Name, Role: string(mb.Role)})
	}
	repos, unshown, err := s.workspaceRepositories(r, m.Workspace.Name)
	if err != nil {
		s.fail(w, r, "list the repositories of a workspace", err)
		return
	}
	s.render(w, r, status, ui.WorkspacePage(s.page(r, m.Workspace.Name, ""), ui.WorkspaceProps{
		Name:              m.Workspace.Name,
		Personal:          m.Workspace.Personal,
		Role:              string(m.Role),
		Repositories:      repos,
		RepositoriesError: unshown,
		Members:           rows,
		Owner:             m.Role == identity.RoleOwner,
		Account:           member,
		MemberRole:        string(role),
		Error:             problem,
	}))
}

// memberAdd adds an account to a workspace, on behalf of one of its owners.
func (s *Server) memberAdd(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// The rule of the page: only a name of the namespace, as it is written,
	// is a workspace, so the redirect lands where a GET answers.
	if identity.CheckName(name) != nil {
		s.notFound(w, r)
		return
	}
	form, ok := formValues(w, r, "account", "role")
	if !ok {
		return
	}
	role := identity.Role(form["role"])
	err := s.workspaces.AddMember(r.Context(), account(r), name, form["account"], role)
	again := func(status int, problem string) {
		s.workspaceForm(w, r, status, name, form["account"], role, problem)
	}
	switch {
	case err == nil:
		redirect(w, "/"+url.PathEscape(name))
	case errors.Is(err, identity.ErrNotOwner):
		s.warn(r, "member refused", "not an owner")
		s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Forbidden", ""), ui.ErrorProps{
			Status: http.StatusForbidden, Title: "Only an owner adds members",
			Message: "Ask an owner of this workspace to add the account.", BackHref: "/" + url.PathEscape(name), BackLabel: "Back to the workspace",
		}))
	case errors.Is(err, identity.ErrNotFound):
		// The actor not being a member is told apart by the page itself,
		// which is not found then.
		again(http.StatusUnprocessableEntity, "No account has this name.")
	case errors.Is(err, identity.ErrAlreadyMember):
		again(http.StatusConflict, "This account is a member already.")
	case errors.Is(err, identity.ErrPersonalWorkspace):
		// Its page has no form to show the problem in.
		s.render(w, r, http.StatusUnprocessableEntity, ui.ErrorPage(s.page(r, name, ""), ui.ErrorProps{
			Status: http.StatusUnprocessableEntity, Title: "A personal workspace has no other member",
			Message: "Create a shared workspace to work with others.", BackHref: "/workspaces", BackLabel: "Workspaces",
		}))
	case errors.Is(err, identity.ErrInvalidRole):
		again(http.StatusUnprocessableEntity, "Choose the role owner or member.")
	default:
		s.fail(w, r, "add a member", err)
	}
}
