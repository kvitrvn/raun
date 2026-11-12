package web

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
)

// Limits of the server, constants by decision of the epic 3 review. cmd/raun
// sets the timeouts and the header size on its http.Server; the handler
// applies the body limit.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 120 * time.Second
	// MaxHeaderBytes is the size of the request line and headers; above it,
	// net/http answers 431.
	MaxHeaderBytes = 32 << 10
	// MaxBodyBytes is the default bound of a request body. A route that needs
	// more states its own bound explicitly.
	MaxBodyBytes = 64 << 10
	// ReadyTimeout bounds the readiness check of one probe: /readyz is
	// reachable without the Host check.
	ReadyTimeout = 2 * time.Second
)

// Options configures the handler.
type Options struct {
	// Logger receives the access log and every error of a request.
	Logger *slog.Logger
	// PublicHost is the host, and port if not the default one, of the public
	// address, as CanonicalHost gives it for Scheme.
	PublicHost string
	// Scheme is the scheme of the public address, "http" or "https".
	Scheme string
	// Ready reports whether the server can serve: the database answers and
	// its schema is the one of the binary.
	Ready func(context.Context) error
	// Sessions keeps the browser sessions.
	Sessions session.Store
	// Now gives the time of a request; nil means time.Now.
	Now func() time.Time
	// Random is the source of session identifiers and CSRF tokens; nil
	// means crypto/rand.
	Random io.Reader
}

// Server is the HTTP handler of the Raun server. It holds no global state:
// every value it needs comes from Options.
type Server struct {
	logger     *slog.Logger
	publicHost string
	scheme     string
	ready      func(context.Context) error
	sessions   session.Store
	now        func() time.Time
	random     io.Reader
	bypass     *bypass
	assets     *assets
	stopping   atomic.Bool
	app        *http.ServeMux
	handler    http.Handler
}

// New returns the handler of the server.
func New(opts Options) (*Server, error) {
	return newServer(opts, nil)
}

// newServer is New with the patterns of the routes that opt out of the
// request protections. The server has none yet; tests give their own.
func newServer(opts Options, bypassPatterns []string) (*Server, error) {
	if opts.Logger == nil {
		return nil, errors.New("web: Options.Logger is nil")
	}
	if opts.Ready == nil {
		return nil, errors.New("web: Options.Ready is nil")
	}
	if opts.Sessions == nil {
		return nil, errors.New("web: Options.Sessions is nil")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	skip, err := newBypass(bypassPatterns)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	canonical, err := CanonicalHost(opts.PublicHost, opts.Scheme)
	if err != nil {
		return nil, errors.New("web: Options.PublicHost: " + err.Error())
	}
	if canonical != opts.PublicHost {
		return nil, errors.New("web: Options.PublicHost is not canonical: " + opts.PublicHost)
	}
	files, err := loadAssets(embeddedAssets)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	s := &Server{
		logger:     opts.Logger,
		publicHost: opts.PublicHost,
		scheme:     opts.Scheme,
		ready:      opts.Ready,
		sessions:   opts.Sessions,
		now:        opts.Now,
		random:     opts.Random,
		bypass:     skip,
		assets:     files,
		app:        http.NewServeMux(),
	}
	handle(s.app, "GET /{$}", http.HandlerFunc(s.home))
	handle(s.app, "GET /assets/{file...}", http.HandlerFunc(s.assets.serve))
	// Any other path of the application is a page that does not exist; it
	// has no route pattern in the access log. Only GET and HEAD reach it, so
	// another method on a known path gets 405 with Allow from the mux.
	s.app.Handle("GET /", http.HandlerFunc(s.notFound))

	root := http.NewServeMux()
	handle(root, "GET /healthz", http.HandlerFunc(s.healthz))
	handle(root, "GET /readyz", http.HandlerFunc(s.readyz))
	// Every other path is the application, behind the protections in the
	// order of the record: the public address only, a bounded body, no
	// cross-origin state change, the session, and its CSRF token on every
	// state change.
	root.Handle("/", s.checkHost(limitBody(s.checkOrigin(s.loadSession(s.checkCSRF(s.app))))))

	s.handler = s.logRequests(s.recoverPanics(s.securityHeaders(root)))
	return s, nil
}

// ServeHTTP serves one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// BeginShutdown makes /readyz answer 503 from now on, without asking the
// database, so that whatever routes traffic stops sending requests while the
// server drains.
func (s *Server) BeginShutdown() {
	s.stopping.Store(true)
}

// handle registers h on mux for pattern, and records the pattern for the
// access log once the mux has matched it.
func handle(mux *http.ServeMux, pattern string, h http.Handler) {
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slot, ok := r.Context().Value(routeKey{}).(*string); ok {
			*slot = r.Pattern
		}
		h.ServeHTTP(w, r)
	}))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok")
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
		writeText(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		s.logger.LogAttrs(r.Context(), slog.LevelWarn, "not ready",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("error", err.Error()))
		writeText(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	writeText(w, http.StatusOK, "ok")
}

// writeText answers a short plain-text body.
func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
