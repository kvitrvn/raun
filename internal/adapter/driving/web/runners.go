package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
	"github.com/kvitrvn/raun/internal/adapter/runnerapi"
	"github.com/kvitrvn/raun/internal/core/job"
)

// RunnerRegistry creates, lists, rotates and revokes runners for an
// administrator of the instance, and tells the runner of a token.
type RunnerRegistry interface {
	Runners(ctx context.Context, actor string) ([]job.Runner, []job.Workspace, error)
	Create(ctx context.Context, actor, name, workspace string, capabilities []job.Capability) (job.Runner, string, error)
	Rotate(ctx context.Context, actor, id string) (job.Runner, string, error)
	Revoke(ctx context.Context, actor, id string) (job.Runner, int, error)
	Authenticate(ctx context.Context, token, release string) (job.Runner, error)
	Busy(ctx context.Context, actor string) (map[string]int, error)
}

type runnerKey struct{}

// runnerFrom returns the runner a request authenticated as.
func runnerFrom(ctx context.Context) (job.Runner, bool) {
	r, ok := ctx.Value(runnerKey{}).(job.Runner)
	return r, ok
}

// runnerRoute registers the runner API on the root mux, like the hook: the
// Host check, the access log and the security headers apply, none of the
// protections of the application, and every request authenticates by the
// bearer token of its runner, then speaks a protocol of the server. Every
// path below /api/runner/ is the API's.
func (s *Server) runnerRoute(root *http.ServeMux) {
	api := http.NewServeMux()
	s.runnerAPIRoutes(api)
	s.route(root, runnerapi.Prefix, s.checkHost(s.runnerAuth(s.runnerProtocol(api))))
}

// bearer returns the token of the one Authorization header of r, in the
// bearer scheme, or false.
func bearer(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > job.MaxToken || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// runnerAuth lets a request through only with the token of a runner that is
// not revoked, which it puts in the context. Every refusal is the same 401,
// whatever the reason; a cookie is never read. Nothing of the header is
// logged.
func (s *Server) runnerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		var runner job.Runner
		err := job.ErrUnauthenticated
		if ok {
			runner, err = s.runners.Authenticate(r.Context(), token, runnerapi.Release(r.UserAgent()))
		}
		switch {
		case errors.Is(err, job.ErrUnauthenticated):
			s.warn(r, "runner refused", "no token of a runner")
			w.Header().Set("WWW-Authenticate", `Bearer realm="raun"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		case err != nil:
			s.apiFail(w, r, "authenticate a runner", err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runnerKey{}, runner)))
	})
}

// runnerRoutes registers the Runners screen of the instance.
func (s *Server) runnerRoutes() {
	s.route(s.app, "GET /instance/runners", s.requireAccount(http.HandlerFunc(s.runnersPage)))
	s.route(s.app, "POST /instance/runners", s.requireAccount(http.HandlerFunc(s.runnerCreate)))
	s.route(s.app, "POST /instance/runners/{id}/rotate", s.requireAccount(http.HandlerFunc(s.runnerRotate)))
	s.route(s.app, "POST /instance/runners/{id}/revoke", s.requireAccount(http.HandlerFunc(s.runnerRevoke)))
}

// forbidden answers a person who is not an administrator.
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	s.warn(r, "runners refused", "not an administrator")
	s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Forbidden", ""), ui.ErrorProps{
		Status: http.StatusForbidden, Title: "Administrators only",
		Message: "Runners are managed by the administrators named in RAUN_ADMINS.", BackHref: "/", BackLabel: "Back to repositories",
	}))
}

// asAdministrator checks the role of the request, and answers it unless the
// account is an administrator.
func (s *Server) asAdministrator(w http.ResponseWriter, r *http.Request) bool {
	admin, err := s.administrator(r)
	if err != nil {
		s.fail(w, r, "check the administrator role", err)
		return false
	}
	if !admin {
		s.forbidden(w, r)
		return false
	}
	return true
}

func (s *Server) runnersPage(w http.ResponseWriter, r *http.Request) {
	if !s.asAdministrator(w, r) {
		return
	}
	s.runnersForm(w, r, http.StatusOK, ui.RunnerForm{Capabilities: names(job.KnownCapabilities())})
}

// runnersForm renders the Runners screen with the creation form f.
func (s *Server) runnersForm(w http.ResponseWriter, r *http.Request, status int, f ui.RunnerForm) {
	runners, workspaces, err := s.runners.Runners(r.Context(), account(r))
	if errors.Is(err, job.ErrNotAdministrator) {
		s.forbidden(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, "list the runners", err)
		return
	}
	busy, err := s.runners.Busy(r.Context(), account(r))
	if err != nil {
		s.fail(w, r, "count the runs of the runners", err)
		return
	}
	workspaceNames := map[string]string{}
	for _, ws := range workspaces {
		workspaceNames[ws.ID] = ws.Name
		f.Workspaces = append(f.Workspaces, ws.Name)
	}
	f.Known = names(job.KnownCapabilities())
	now := s.now()
	var rows []ui.RunnerRow
	for _, rn := range runners {
		row := ui.RunnerRow{ID: rn.ID, Name: rn.Name, Scope: "instance", Revoked: rn.Revoked(), Release: rn.Release, Running: busy[rn.ID]}
		if rn.Workspace != "" {
			row.Scope = "workspace " + workspaceNames[rn.Workspace]
		}
		row.Capabilities = names(rn.Capabilities)
		if rn.LastContact.IsZero() {
			row.LastContact = "never seen"
		} else {
			row.LastContact = rn.LastContact.UTC().Format("2006-01-02 15:04 UTC")
			row.Seen = !rn.Revoked() && now.Sub(rn.LastContact) < 2*job.ContactEvery
		}
		rows = append(rows, row)
	}
	s.render(w, r, status, ui.RunnersPage(s.page(r, "Runners", "/instance"), ui.RunnersProps{Rows: rows, Form: f}))
}

// runnerCreate creates a runner and shows its token, once, in the answer
// itself: never in a redirect, a flash or the list.
func (s *Server) runnerCreate(w http.ResponseWriter, r *http.Request) {
	if !s.asAdministrator(w, r) {
		return
	}
	form, ok := formValues(w, r, "name", "workspace")
	if !ok {
		return
	}
	var caps []job.Capability
	for _, c := range r.PostForm["capability"] {
		caps = append(caps, job.Capability(c))
	}
	f := ui.RunnerForm{Name: form["name"], Workspace: form["workspace"], Capabilities: r.PostForm["capability"]}
	runner, token, err := s.runners.Create(r.Context(), account(r), form["name"], form["workspace"], caps)
	switch {
	case errors.Is(err, job.ErrNotAdministrator):
		s.forbidden(w, r)
		return
	case errors.Is(err, job.ErrInvalidRunner):
		f.Error = reason(err, job.ErrInvalidRunner)
		s.runnersForm(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, job.ErrNoWorkspace):
		f.Error = "No workspace has this name."
		s.runnersForm(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, job.ErrRunnerNameTaken):
		f.Error = "A runner has this name already, revoked or not: choose another one."
		s.runnersForm(w, r, http.StatusConflict, f)
		return
	case err != nil:
		s.fail(w, r, "create a runner", err)
		return
	}
	s.info(r, "runner created", runner)
	s.render(w, r, http.StatusOK, ui.RunnerTokenPage(s.page(r, "Runner token", "/instance"), ui.RunnerTokenProps{Name: runner.Name, Token: token, Created: true}))
}

// runnerRotate gives a runner a new token and shows it, once.
func (s *Server) runnerRotate(w http.ResponseWriter, r *http.Request) {
	if !s.asAdministrator(w, r) {
		return
	}
	runner, token, err := s.runners.Rotate(r.Context(), account(r), r.PathValue("id"))
	switch {
	case errors.Is(err, job.ErrNotAdministrator):
		s.forbidden(w, r)
		return
	case errors.Is(err, job.ErrNoRunner):
		s.notFound(w, r)
		return
	case errors.Is(err, job.ErrRevoked):
		s.runnersForm(w, r, http.StatusConflict, ui.RunnerForm{Capabilities: names(job.KnownCapabilities()), Error: "A revoked runner gets no new token: create another runner."})
		return
	case err != nil:
		s.fail(w, r, "rotate the token of a runner", err)
		return
	}
	s.info(r, "runner token rotated", runner)
	s.render(w, r, http.StatusOK, ui.RunnerTokenPage(s.page(r, "Runner token", "/instance"), ui.RunnerTokenProps{Name: runner.Name, Token: token}))
}

// runnerRevoke revokes a runner, then shows the list.
func (s *Server) runnerRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.asAdministrator(w, r) {
		return
	}
	runner, _, err := s.runners.Revoke(r.Context(), account(r), r.PathValue("id"))
	switch {
	case errors.Is(err, job.ErrNotAdministrator):
		s.forbidden(w, r)
		return
	case errors.Is(err, job.ErrNoRunner):
		s.notFound(w, r)
		return
	case err != nil:
		s.fail(w, r, "revoke a runner", err)
		return
	}
	s.info(r, "runner revoked", runner)
	redirect(w, "/instance/runners")
}

// info logs a change of runner by the account of r: what, the runner by
// identifier, never its token.
func (s *Server) info(r *http.Request, what string, runner job.Runner) {
	s.logger.InfoContext(r.Context(), what, "request_id", RequestID(r.Context()), "runner", runner.ID,
		"account", account(r), "workspace", runner.Workspace, "capabilities", strings.Join(names(runner.Capabilities), ","))
}

// names returns the names of capabilities.
func names(capabilities []job.Capability) []string {
	out := make([]string, len(capabilities))
	for i, c := range capabilities {
		out[i] = string(c)
	}
	return out
}
