package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
)

// Setting is a variable of the effective configuration, its value already
// redacted when it is a secret.
type Setting struct {
	Name  string
	Value string
}

// AdministratorChecker tells whether an account is an administrator of the
// instance, at the moment of the request.
type AdministratorChecker interface {
	IsAdministrator(ctx context.Context, account string) (bool, error)
}

type roleKey struct{}

// roleOnce holds the administrator role of the account of one request, read
// once.
type roleOnce struct {
	once  sync.Once
	admin bool
	err   error
}

// administrator reports whether the account of r is an administrator, read
// from the store once per request: the screen and the top bar of one request
// agree, at the cost of one read. A request without an account is not one.
func (s *Server) administrator(r *http.Request) (bool, error) {
	sess, ok := sessionFrom(r.Context())
	if !ok || sess.Account == "" || s.administrators == nil {
		return false, nil
	}
	read := func() (bool, error) { return s.administrators.IsAdministrator(r.Context(), sess.Account) }
	memo, ok := r.Context().Value(roleKey{}).(*roleOnce)
	if !ok {
		return read()
	}
	memo.once.Do(func() { memo.admin, memo.err = read() })
	return memo.admin, memo.err
}

// instanceRoutes registers the administration screens.
func (s *Server) instanceRoutes() {
	s.route(s.app, "GET /instance", s.requireAccount(http.HandlerFunc(s.instancePage)))
}

// instancePage shows the effective configuration to an administrator. The
// role is checked at every request, and a role that cannot be established
// is refused.
func (s *Server) instancePage(w http.ResponseWriter, r *http.Request) {
	admin, err := s.administrator(r)
	if err != nil {
		s.fail(w, r, "check the administrator role", err)
		return
	}
	if !admin {
		s.warn(r, "instance settings refused", "not an administrator")
		s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Forbidden", ""), ui.ErrorProps{
			Status: http.StatusForbidden, Title: "Administrators only",
			Message: "The settings of the instance are shown to the administrators named in RAUN_ADMINS.", BackHref: "/", BackLabel: "Back to repositories",
		}))
		return
	}
	var rows []ui.InstanceSetting
	for _, st := range s.settings {
		rows = append(rows, ui.InstanceSetting{Name: st.Name, Value: st.Value})
	}
	s.render(w, r, http.StatusOK, ui.InstancePage(s.page(r, "Instance settings", "/instance"), rows))
}

// isAdministrator reports whether the session of r belongs to an
// administrator, for the top bar: an error reads as no.
func (s *Server) isAdministrator(r *http.Request) bool {
	admin, err := s.administrator(r)
	return err == nil && admin
}
