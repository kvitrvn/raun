package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
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
}

// Server is the HTTP handler of the Raun server. It holds no global state:
// every value it needs comes from Options.
type Server struct {
	logger     *slog.Logger
	publicHost string
	scheme     string
	ready      func(context.Context) error
	assets     *assets
	stopping   atomic.Bool
	app        *http.ServeMux
	handler    http.Handler
}

// New returns the handler of the server.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		return nil, errors.New("web: Options.Logger is nil")
	}
	if opts.Ready == nil {
		return nil, errors.New("web: Options.Ready is nil")
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
		assets:     files,
		app:        http.NewServeMux(),
	}
	handle(s.app, "GET /{$}", http.HandlerFunc(s.home))
	handle(s.app, "GET /assets/{file...}", http.HandlerFunc(s.assets.serve))
	// Any other path of the application is a page that does not exist; it
	// has no route pattern in the access log.
	s.app.Handle("/", http.HandlerFunc(s.notFound))

	root := http.NewServeMux()
	handle(root, "GET /healthz", http.HandlerFunc(s.healthz))
	handle(root, "GET /readyz", http.HandlerFunc(s.readyz))
	// Every other path is the application: it is served at the public
	// address only, with a bounded body.
	root.Handle("/", s.checkHost(limitBody(s.app)))

	s.handler = s.logRequests(s.recoverPanics(root))
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
