package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
	"github.com/kvitrvn/raun/internal/core/identity"
)

// AccountReader gives the account a session is logged in to.
type AccountReader interface {
	Account(ctx context.Context, id string) (identity.Account, error)
}

// returnParam is the query parameter of /login that names where to go back.
const returnParam = "return"

// loginRoutes registers the routes of the login. They are safe methods but
// for the sign-up and the logout, which the CSRF check guards like any
// other state change.
func (s *Server) loginRoutes() {
	handle(s.app, "GET /login", http.HandlerFunc(s.loginStart))
	handle(s.app, "GET /login/callback", http.HandlerFunc(s.loginCallback))
	handle(s.app, "GET /login/signup", http.HandlerFunc(s.signupPage))
	handle(s.app, "POST /login/signup", http.HandlerFunc(s.signupFinish))
	handle(s.app, "POST /logout", http.HandlerFunc(s.logout))
}

// redirect answers 303 to location.
func redirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}

// loginPage renders the login page with status.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request, status int, l ui.LoginProps) {
	s.render(w, r, status, ui.LoginPage(s.page(r, "Log in", ""), l))
}

// noForge is the login page of a server without a forge.
func (s *Server) noForge(w http.ResponseWriter, r *http.Request) {
	s.loginPage(w, r, http.StatusServiceUnavailable, ui.LoginProps{
		Title:   "Login is not available",
		Message: "No forge is configured on this instance: its administrator sets up a GitHub App first.",
	})
}

// loginStart starts a login: a session that has an account goes back at
// once; any other gets a login state and goes to the forge. It is the one
// GET that changes state, recorded in the epic 3 record; a HEAD changes
// nothing.
func (s *Server) loginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	back := identity.SafeReturnPath(r.URL.Query().Get(returnParam))
	sess, ok := sessionFrom(r.Context())
	if ok && sess.Account != "" {
		redirect(w, back)
		return
	}
	if s.login == nil {
		s.noForge(w, r)
		return
	}
	if !ok {
		var err error
		if sess, err = s.startSession(w, r); err != nil {
			s.fail(w, r, "start a session for a login", err)
			return
		}
	}
	u, err := s.login.Begin(r.Context(), sess.Hash, back)
	if err != nil {
		s.fail(w, r, "begin a login", err)
		return
	}
	redirect(w, u)
}

// loginCallback completes a login with the answer of the forge: the state
// must be one issued for the session of this browser, and serves once.
func (s *Server) loginCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if s.login == nil {
		s.noForge(w, r)
		return
	}
	retry := func(status int, title, message string) {
		s.loginPage(w, r, status, ui.LoginProps{Title: title, Message: message, Retry: true})
	}
	sess, ok := sessionFrom(r.Context())
	if !ok {
		retry(http.StatusBadRequest, "Login failed",
			"The forge sent you back without the session that started the login. Raun needs its cookie: allow it and try again.")
		return
	}
	q := r.URL.Query()
	if q.Has("error") {
		// The text of the forge is never shown: it comes in the address.
		retry(http.StatusBadRequest, "Login cancelled", "The authorization was not given on the forge.")
		return
	}
	if len(q["state"]) != 1 || len(q["code"]) != 1 {
		retry(http.StatusBadRequest, "Login failed", "The answer of the forge is incomplete.")
		return
	}
	out, err := s.login.Complete(r.Context(), sess.Hash, q.Get("state"), q.Get("code"))
	var refusal *identity.Refusal
	switch {
	case errors.Is(err, identity.ErrInvalidState):
		s.warn(r, "login refused", "unknown, used, expired or foreign state")
		retry(http.StatusBadRequest, "Login failed",
			"This login link is no longer valid: it was used already, it expired, or it was started in another browser.")
		return
	case errors.Is(err, identity.ErrInvalidCode):
		retry(http.StatusBadRequest, "Login failed", "The forge refused the authorization code.")
		return
	case errors.Is(err, identity.ErrUnavailable):
		// Before the refusal: a membership that could not be checked
		// refuses the login, but the forge failed, not the person.
		s.warn(r, "login failed", err.Error())
		retry(http.StatusServiceUnavailable, "The forge cannot be reached", "Try again in a moment.")
		return
	case errors.As(err, &refusal):
		s.warn(r, "login refused", "sign-up rule")
		s.loginPage(w, r, http.StatusForbidden, ui.LoginProps{Title: "You cannot sign up here", Message: sentence(refusal.Reason)})
		return
	case err != nil:
		s.fail(w, r, "complete a login", err)
		return
	}
	if out.Signup {
		redirect(w, "/login/signup")
		return
	}
	if _, err := s.renewSession(w, r, out.Account.ID); err != nil {
		s.fail(w, r, "renew the session at login", err)
		return
	}
	redirect(w, identity.SafeReturnPath(out.ReturnPath))
}

// signupPage asks for the name of a new account, to the session that has a
// sign-up in progress.
func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	if s.login == nil {
		s.noForge(w, r)
		return
	}
	sess, ok := sessionFrom(r.Context())
	if !ok {
		redirect(w, "/login")
		return
	}
	sg, err := s.login.Signup(r.Context(), sess.Hash)
	if errors.Is(err, identity.ErrNotFound) {
		redirect(w, "/login")
		return
	}
	if err != nil {
		s.fail(w, r, "read the sign-up", err)
		return
	}
	s.signupForm(w, r, http.StatusOK, sg, sg.Proposed, "")
}

func (s *Server) signupForm(w http.ResponseWriter, r *http.Request, status int, sg identity.Signup, name, problem string) {
	s.render(w, r, status, ui.SignupPage(s.page(r, "Sign up", ""), ui.SignupProps{
		Forge: s.login.Forge().Name(),
		Login: sg.Identity.Login,
		Name:  name,
		Error: problem,
	}))
}

// signupFinish creates the account of the sign-up of the session with the
// name it chose, and logs the session in to it.
func (s *Server) signupFinish(w http.ResponseWriter, r *http.Request) {
	if s.login == nil {
		s.noForge(w, r)
		return
	}
	sess, ok := sessionFrom(r.Context())
	if !ok {
		redirect(w, "/login")
		return
	}
	if err := r.ParseForm(); err != nil {
		if IsBodyTooLarge(err) {
			writeText(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeText(w, http.StatusBadRequest, "malformed form")
		return
	}
	name := r.PostForm.Get("name")
	account, back, err := s.login.FinishSignup(r.Context(), sess.Hash, name)
	again := func(status int, problem string) {
		sg, err := s.login.Signup(r.Context(), sess.Hash)
		if err != nil {
			redirect(w, "/login")
			return
		}
		s.signupForm(w, r, status, sg, name, problem)
	}
	switch {
	case errors.Is(err, identity.ErrInvalidName):
		again(http.StatusUnprocessableEntity, reason(err, identity.ErrInvalidName))
		return
	case errors.Is(err, identity.ErrReservedName):
		again(http.StatusUnprocessableEntity, reason(err, identity.ErrReservedName))
		return
	case errors.Is(err, identity.ErrNameTaken):
		again(http.StatusConflict, "This name is taken: choose another one.")
		return
	case errors.Is(err, identity.ErrNotFound):
		// The sign-up expired: the login starts over.
		redirect(w, "/login")
		return
	case errors.Is(err, identity.ErrIdentityTaken):
		s.loginPage(w, r, http.StatusConflict, ui.LoginProps{
			Title:   "This account exists already",
			Message: "Your forge account got a Raun account meanwhile, from another browser. Log in again to use it.",
			Retry:   true,
		})
		return
	case err != nil:
		s.fail(w, r, "finish a sign-up", err)
		return
	}
	if _, err := s.renewSession(w, r, account.ID); err != nil {
		s.fail(w, r, "renew the session at sign-up", err)
		return
	}
	redirect(w, identity.SafeReturnPath(back))
}

// logout ends the session of the request. The other sessions of the account
// stay.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		s.fail(w, r, "log out", err)
		return
	}
	redirect(w, "/")
}

// warn logs why a request was refused, with its identifier.
func (s *Server) warn(r *http.Request, what, why string) {
	s.logger.LogAttrs(r.Context(), slog.LevelWarn, what,
		slog.String("request_id", RequestID(r.Context())),
		slog.String("reason", why))
}

// reason is the text of err after its sentinel, as a sentence.
func reason(err, sentinel error) string {
	return sentence(strings.TrimPrefix(err.Error(), sentinel.Error()+": "))
}

// sentence capitalizes s and ends it with a period.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
