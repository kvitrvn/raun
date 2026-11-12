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
	if sess, ok := sessionFrom(r.Context()); ok {
		page.CSRF = sess.CSRF
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
