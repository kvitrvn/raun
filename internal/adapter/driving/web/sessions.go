package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/kvitrvn/raun/internal/core/session"
)

// Session cookie names. The __Host- prefix needs Secure, which browsers do
// not all accept on http://localhost, so a loopback http address uses the
// plain name (docs/implementation/03-server-foundation.md#sessions).
const (
	secureCookie = "__Host-raun-session"
	plainCookie  = "raun-session"
)

// csrfField and csrfHeader carry the CSRF token of a state-changing request:
// the header for HTMX and scripts, the field for a URL-encoded form.
const (
	csrfField  = "csrf"
	csrfHeader = "X-CSRF-Token"
)

// createAttempts bounds the draws of a new identifier: a collision of 32
// random bytes does not happen, but a store refusing one must not loop.
const createAttempts = 3

type sessionKey struct{}

// sessionFrom returns the session the request belongs to, if any.
func sessionFrom(ctx context.Context) (session.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(session.Session)
	return s, ok
}

func (s *Server) cookieName() string {
	if s.scheme == "https" {
		return secureCookie
	}
	return plainCookie
}

// loadSession finds the session of the request from its cookie and puts it
// in the context. A cookie that names no live session is cleared; an
// expired session is deleted. The time of last use is written at most once
// per session.TouchInterval.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookieName())
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		hash, err := session.HashOf(c.Value)
		if err != nil {
			s.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		sess, err := s.sessions.Get(r.Context(), hash)
		switch {
		case errors.Is(err, session.ErrNotFound):
			s.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		case err != nil:
			s.fail(w, r, "read session", err)
			return
		}
		now := s.now()
		if sess.Expired(now) {
			if err := s.sessions.Delete(r.Context(), hash); err != nil {
				s.fail(w, r, "delete expired session", err)
				return
			}
			s.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		if sess.NeedsTouch(now) {
			err := s.sessions.Touch(r.Context(), hash, now)
			switch {
			case errors.Is(err, session.ErrNotFound):
				// Revoked, renewed or purged since the lookup.
				s.clearCookie(w)
				next.ServeHTTP(w, r)
				return
			case err != nil:
				s.fail(w, r, "touch session", err)
				return
			}
			sess.LastSeenAt = now.UTC()
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		// The administrator role is read once per request, at the first
		// need, and forgotten with the request.
		ctx = context.WithValue(ctx, roleKey{}, &roleOnce{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAccount sends a request whose session has no account to the login,
// which brings it back to the same path. Routes of later epics that need an
// account wrap their handler with it.
func (s *Server) requireAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := sessionFrom(r.Context()); !ok || sess.Account == "" {
			redirect(w, "/login?"+url.Values{returnParam: {r.URL.RequestURI()}}.Encode())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSession refuses with 401 a request without a session. Routes of
// later epics wrap their handler with it.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := sessionFrom(r.Context()); !ok {
			writeText(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkCSRF refuses with 403 a state-changing request that has no session,
// or does not carry the CSRF token of its session in the X-CSRF-Token header
// or the csrf form field. The token is never read from the query, which
// would leak it into logs and history. Safe methods and bypassed routes
// pass.
func (s *Server) checkCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafe(r.Method) || s.bypass.skips(r) {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := sessionFrom(r.Context())
		if !ok {
			s.refuse(w, r, "state-changing request without a session")
			return
		}
		token := r.Header.Get(csrfHeader)
		if token == "" {
			// The field of a URL-encoded form; ParseForm reads no other
			// body, so another kind of request sends the header.
			if err := r.ParseForm(); err != nil {
				if IsBodyTooLarge(err) {
					writeText(w, http.StatusRequestEntityTooLarge, "request body too large")
					return
				}
				writeText(w, http.StatusBadRequest, "malformed form")
				return
			}
			// One field exactly: a duplicate could pair a forged value
			// with a copied one.
			if values := r.PostForm[csrfField]; len(values) == 1 {
				token = values[0]
			}
		}
		if !sess.CheckCSRF(token) {
			s.refuse(w, r, "missing or wrong CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	s.logger.LogAttrs(r.Context(), slog.LevelWarn, "request refused",
		slog.String("request_id", RequestID(r.Context())),
		slog.String("method", r.Method),
		slog.String("reason", reason))
	writeText(w, http.StatusForbidden, "forbidden")
}

// fail answers 500 for an error of the session store, logged with the
// request identifier.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.LogAttrs(r.Context(), slog.LevelError, what,
		slog.String("request_id", RequestID(r.Context())),
		slog.String("error", err.Error()))
	writeText(w, http.StatusInternalServerError, "internal error, request "+RequestID(r.Context()))
}

// startSession creates a session and sets its cookie: the login of epic 4
// is the first route to need one.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) (session.Session, error) {
	for range createAttempts {
		id, sess, err := session.New(s.random, s.now())
		if err != nil {
			return session.Session{}, err
		}
		err = s.sessions.Create(r.Context(), sess)
		if errors.Is(err, session.ErrExists) {
			continue
		}
		if err != nil {
			return session.Session{}, fmt.Errorf("create session: %w", err)
		}
		s.setCookie(w, id, sess)
		return sess, nil
	}
	return session.Session{}, errors.New("create session: identifier taken on every draw")
}

// renewSession replaces the session of the request with a new identifier
// and a new CSRF token, keeping its absolute lifetime, logged in to account,
// and sets the new cookie: at login and at any change of privilege.
func (s *Server) renewSession(w http.ResponseWriter, r *http.Request, account string) (session.Session, error) {
	old, ok := sessionFrom(r.Context())
	if !ok {
		return session.Session{}, errors.New("renew session: the request has none")
	}
	for range createAttempts {
		id, next, err := session.Renew(s.random, old, s.now())
		if err != nil {
			return session.Session{}, err
		}
		next.Account = account
		err = s.sessions.Replace(r.Context(), old.Hash, next)
		if errors.Is(err, session.ErrExists) {
			continue
		}
		if err != nil {
			return session.Session{}, fmt.Errorf("renew session: %w", err)
		}
		s.setCookie(w, id, next)
		return next, nil
	}
	return session.Session{}, errors.New("renew session: identifier taken on every draw")
}

// endSession revokes the session of the request, if any, and clears its
// cookie: at logout (epic 4).
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) error {
	if sess, ok := sessionFrom(r.Context()); ok {
		if err := s.sessions.Delete(r.Context(), sess.Hash); err != nil {
			return fmt.Errorf("end session: %w", err)
		}
	}
	s.clearCookie(w)
	return nil
}

// setCookie sends the identifier of sess, kept by the browser no longer than
// the session can live.
func (s *Server) setCookie(w http.ResponseWriter, id string, sess session.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    id,
		Path:     "/",
		MaxAge:   sess.MaxAge(s.now()),
		Secure:   s.scheme == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   s.scheme == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
