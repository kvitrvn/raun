package web

import (
	"bytes"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
)

// page returns what the layout needs for a page of the request r with this
// title; current is the href of the top bar link to mark.
func (s *Server) page(r *http.Request, title, current string) ui.Page {
	nav := []ui.NavItem{{Label: "Repositories", Href: "/"}}
	// The link shows to administrators only; the screen checks the role
	// again itself.
	if s.isAdministrator(r) {
		nav = append(nav, ui.NavItem{Label: "Instance settings", Href: "/instance"})
	}
	for i := range nav {
		nav[i].Current = nav[i].Href == current
	}
	page := ui.Page{
		Title:    title,
		Instance: s.publicHost,
		Assets: ui.Assets{
			CSS:     s.assets.url("app.css"),
			ThemeJS: s.assets.url("theme.js"),
			AppJS:   s.assets.url("app.js"),
			HTMX:    s.assets.url("htmx.min.js"),
			Icon:    s.assets.url("favicon.svg"),
		},
		Nav: nav,
	}
	page.Login = s.login != nil
	if sess, ok := sessionFrom(r.Context()); ok {
		page.CSRF = sess.CSRF
		if sess.Account != "" && s.accounts != nil {
			// An account that cannot be read shows as a session without
			// one: the page still renders.
			if a, err := s.accounts.Account(r.Context(), sess.Account); err == nil {
				page.Account = a.Name
			}
		}
		if sess.Account != "" && s.workspaces != nil {
			// Only the workspaces of the account: a failed read leaves
			// the switcher empty, and the page still renders.
			if list, err := s.workspaces.List(r.Context(), sess.Account); err == nil {
				current := r.PathValue("name")
				for _, m := range list {
					page.Workspaces = append(page.Workspaces, ui.WorkspaceItem{
						Name:     m.Workspace.Name,
						Href:     "/" + m.Workspace.Name,
						Personal: m.Workspace.Personal,
						Current:  m.Workspace.Name == current,
					})
				}
			}
		}
	}
	return page
}

// render writes a page with status. It renders into a buffer first, so a
// failing template answers 500 instead of half a page. A page is never
// cached: it shows the state of the moment.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		s.logger.LogAttrs(r.Context(), slog.LevelError, "render page",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("error", err.Error()))
		writeText(w, http.StatusInternalServerError, "internal error, request "+RequestID(r.Context()))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, ui.HomePage(s.page(r, "Repositories", "/")))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, ui.NotFoundPage(s.page(r, "Not found", "")))
}
