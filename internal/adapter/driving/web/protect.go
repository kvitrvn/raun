package web

import (
	"fmt"
	"log/slog"
	"net/http"
)

// contentSecurityPolicy refuses inline scripts, inline styles, eval and
// every other origin (docs/implementation/03-server-foundation.md#request-protections).
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// hsts is sent when the public address is https.
const hsts = "max-age=31536000"

// securityHeaders sets the headers of the record on every response: pages,
// fragments, assets, health endpoints and errors. A response may replace
// Cache-Control, as assets do.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if s.scheme == "https" {
			h.Set("Strict-Transport-Security", hsts)
		}
		next.ServeHTTP(w, r)
	})
}

// bypass holds the routes that opt out of the cross-origin protection, the
// session requirement and the CSRF check, each because it authenticates
// requests its own way (webhooks in epic 5, the runner API in epic 6).
type bypass struct {
	patterns []string
	mux      *http.ServeMux
	origin   *http.CrossOriginProtection
}

func newBypass(patterns []string) (*bypass, error) {
	b := &bypass{patterns: patterns, mux: http.NewServeMux(), origin: http.NewCrossOriginProtection()}
	for _, p := range patterns {
		if err := registerBypass(b, p); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// registerBypass adds pattern, turning the panics of ServeMux and of the
// cross-origin protection into an error.
func registerBypass(b *bypass, pattern string) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("bypass pattern %q: %v", pattern, v)
		}
	}()
	b.mux.Handle(pattern, http.NotFoundHandler())
	b.origin.AddInsecureBypassPattern(pattern)
	return nil
}

// skips reports whether r matches a bypass pattern.
func (b *bypass) skips(r *http.Request) bool {
	_, pattern := b.mux.Handler(r)
	return pattern != ""
}

// checkOrigin refuses with 403 a state-changing request that a browser sent
// from another origin, by the rules of http.CrossOriginProtection: a
// Sec-Fetch-Site other than same-origin or none, or, without it, an Origin
// whose host is not the Host. Safe methods and bypassed routes pass.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.bypass.origin.Check(r); err != nil {
			s.logger.LogAttrs(r.Context(), slog.LevelWarn, "cross-origin request refused",
				slog.String("request_id", RequestID(r.Context())),
				slog.String("method", r.Method),
				slog.String("error", err.Error()))
			writeText(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSafe reports whether method changes nothing: the protections let it
// through, so no handler may change state on it.
func isSafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
