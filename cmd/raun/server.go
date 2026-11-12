package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/adapter/driving/web"
	"github.com/kvitrvn/raun/internal/core/session"
)

// serverDeps holds what tests replace in the server's wiring. The zero value
// is the production wiring.
type serverDeps struct {
	// listener, when set, is served instead of listening on RAUN_HTTP_ADDR.
	listener net.Listener
	// reload, when set, receives the reload requests instead of SIGHUP.
	reload <-chan os.Signal
	// wrap, when set, wraps the handler of the server.
	wrap func(http.Handler) http.Handler
	// started, when set, is called once the server accepts connections.
	started func(addr net.Addr)
	// purge, when set, receives the times of the hourly session purge
	// instead of a ticker.
	purge <-chan time.Time
}

// sessionPurgeInterval is how often expired sessions are deleted, after the
// purge at start.
const sessionPurgeInterval = time.Hour

// purgeSessions deletes the sessions that have ended at now. At start, an
// error stops the server: the database does not answer.
func purgeSessions(ctx context.Context, logger *slog.Logger, sessions session.Store, now time.Time) error {
	n, err := sessions.DeleteExpired(ctx, now)
	if err != nil {
		logger.Error("delete expired sessions", slog.String("error", err.Error()))
		return err
	}
	if n > 0 {
		logger.Info("expired sessions deleted", slog.Int("count", n))
	}
	return nil
}

// purgeSessionsEvery deletes expired sessions on each tick until done. A
// failure is logged and the next tick tries again; the stop of the server
// cancels ctx, and with it a purge in progress.
func purgeSessionsEvery(ctx context.Context, logger *slog.Logger, sessions session.Store, ticks <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case now := <-ticks:
			_ = purgeSessions(ctx, logger, sessions, now)
		}
	}
}

// newLogger returns the logger of the server: one record per line on w, in
// JSON or text, from level up.
func newLogger(level slog.Level, format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// runServer runs raun server until ctx is done, and returns its exit code.
// The order is the one of the epic 3 record: configuration, logger,
// database, migrations, listener; a failure stops it before the listener
// opens. Standard output stays empty.
func runServer(ctx context.Context, environ []string, stderr io.Writer, deps serverDeps) int {
	cfg, err := loadConfig(environ)
	if err != nil {
		fmt.Fprintf(stderr, "raun server: invalid configuration:\n%v\n", err)
		return exitFailure
	}
	logger := newLogger(cfg.logLevel, cfg.logFormat, stderr)
	logger.LogAttrs(ctx, slog.LevelInfo, "configuration", cfg.logAttrs()...)

	store, err := sqlite.Open(ctx, cfg.databasePath, sqlite.Options{})
	if err != nil {
		logger.Error("open the database (RAUN_DATABASE_PATH)", slog.String("error", err.Error()))
		return exitFailure
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close the database", slog.String("error", err.Error()))
		}
	}()
	if err := store.Migrate(ctx, cfg.databaseMigrate); err != nil {
		logger.Error("migrate the database", slog.String("error", err.Error()))
		return exitFailure
	}
	sessions := store.Sessions()
	if err := purgeSessions(ctx, logger, sessions, time.Now()); err != nil {
		return exitFailure
	}

	handler, err := web.New(web.Options{
		Logger:     logger,
		PublicHost: cfg.publicHost,
		Scheme:     cfg.publicURL.Scheme,
		Ready:      store.CheckReady,
		Sessions:   sessions,
	})
	if err != nil {
		logger.Error("build the handler", slog.String("error", err.Error()))
		return exitFailure
	}
	var h http.Handler = handler
	if deps.wrap != nil {
		h = deps.wrap(h)
	}

	ln := deps.listener
	if ln == nil {
		ln, err = net.Listen("tcp", cfg.httpAddr)
		if err != nil {
			logger.Error("listen (RAUN_HTTP_ADDR)", slog.String("error", err.Error()))
			return exitFailure
		}
	}

	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: web.ReadHeaderTimeout,
		ReadTimeout:       web.ReadTimeout,
		WriteTimeout:      web.WriteTimeout,
		IdleTimeout:       web.IdleTimeout,
		MaxHeaderBytes:    web.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	if cfg.certificates != nil {
		srv.TLSConfig = cfg.certificates.TLSConfig()
	}

	reload := deps.reload
	if reload == nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		reload = hup
	}
	done := make(chan struct{})
	defer close(done)
	go reloadCertificates(logger, cfg.certificates, reload, done)
	ticks := deps.purge
	if ticks == nil {
		ticker := time.NewTicker(sessionPurgeInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	go purgeSessionsEvery(ctx, logger, sessions, ticks, done)

	served := make(chan error, 1)
	go func() {
		if cfg.certificates != nil {
			served <- srv.ServeTLS(ln, "", "")
			return
		}
		served <- srv.Serve(ln)
	}()
	logger.Info("serving", slog.String("addr", ln.Addr().String()), slog.String("public_url", cfg.publicURL.String()))
	if deps.started != nil {
		deps.started(ln.Addr())
	}

	select {
	case err := <-served:
		logger.Error("serve", slog.String("error", err.Error()))
		return exitFailure
	case <-ctx.Done():
	}
	return shutdown(logger, srv, handler, served, cfg)
}

// shutdown reports the server not ready, stops accepting connections and
// waits for in-flight requests up to the deadline. Past it, the remaining
// connections are closed and the exit code is a failure.
func shutdown(logger *slog.Logger, srv *http.Server, handler *web.Server, served <-chan error, cfg config) int {
	logger.Info("shutting down", slog.Duration("timeout", cfg.shutdownTimeout))
	handler.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	code := exitOK
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("in-flight requests dropped at the shutdown deadline (RAUN_SHUTDOWN_TIMEOUT)", slog.String("error", err.Error()))
		_ = srv.Close()
		code = exitFailure
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", slog.String("error", err.Error()))
		code = exitFailure
	}
	logger.Info("stopped")
	return code
}

// reloadCertificates reloads the TLS pair on each request until done. A pair
// that fails to load is logged and the previous one is kept.
func reloadCertificates(logger *slog.Logger, certs *web.Certificates, reload <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-reload:
			if certs == nil {
				logger.Info("reload requested, but TLS is off")
				continue
			}
			if err := certs.Reload(); err != nil {
				logger.Error("reload the TLS pair; keeping the previous one", slog.String("error", err.Error()))
				continue
			}
			logger.Info("TLS pair reloaded")
		}
	}
}
