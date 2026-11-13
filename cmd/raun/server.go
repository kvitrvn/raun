package main

import (
	"context"
	"crypto/rand"
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
	"github.com/kvitrvn/raun/internal/core/identity"
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
	// purge, when set, receives the times of the hourly purge instead of a
	// ticker.
	purge <-chan time.Time
	// forge, when set, is the forge instead of the GitHub App of the
	// configuration: the fake forge of the tests.
	forge identity.Forge
}

// purgeInterval is how often what has expired is deleted, after the purge
// at start.
const purgeInterval = time.Hour

// expiring are the stores the purge cleans: sessions, login states and
// sign-ups.
type expiring struct {
	sessions session.Store
	accounts identity.Store
}

// purge deletes the sessions, login states and sign-ups that have ended at
// now. At start, an error stops the server: the database does not answer.
func purge(ctx context.Context, logger *slog.Logger, e expiring, now time.Time) error {
	for _, p := range []struct {
		what   string
		delete func(context.Context, time.Time) (int, error)
	}{
		{"sessions", e.sessions.DeleteExpired},
		{"login states", e.accounts.DeleteExpiredLoginStates},
		{"sign-ups", e.accounts.DeleteExpiredSignups},
	} {
		n, err := p.delete(ctx, now)
		if err != nil {
			logger.Error("delete expired "+p.what, slog.String("error", err.Error()))
			return err
		}
		if n > 0 {
			logger.Info("expired "+p.what+" deleted", slog.Int("count", n))
		}
	}
	return nil
}

// purgeEvery purges on each tick until done. A failure is logged and the
// next tick tries again; the stop of the server cancels ctx, and with it a
// purge in progress.
func purgeEvery(ctx context.Context, logger *slog.Logger, e expiring, ticks <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case now := <-ticks:
			_ = purge(ctx, logger, e, now)
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
// The order is the one of the epic 3 record, with the forge of epic 4:
// configuration, logger, forge, database, migrations, listener; a failure
// stops it before the listener opens. Standard output stays empty.
func runServer(ctx context.Context, environ []string, stderr io.Writer, deps serverDeps) int {
	cfg, err := loadConfig(environ)
	if err != nil {
		fmt.Fprintf(stderr, "raun server: invalid configuration:\n%v\n", err)
		return exitFailure
	}
	logger := newLogger(cfg.logLevel, cfg.logFormat, stderr)
	logger.LogAttrs(ctx, slog.LevelInfo, "configuration", cfg.logAttrs()...)

	forge, err := buildForge(cfg, deps)
	if err != nil {
		logger.Error("build the forge (RAUN_GITHUB_*)", slog.String("error", err.Error()))
		return exitFailure
	}
	if forge != nil {
		attrs := []any{slog.String("name", forge.Name())}
		if deps.forge == nil {
			attrs = append(attrs, slog.String("url", cfg.github.webURL))
		}
		logger.Info("forge", attrs...)
	}

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
	keys, err := keyring(cfg)
	if err != nil {
		logger.Error("read the secret keys", slog.String("error", err.Error()))
		return exitFailure
	}
	accounts := store.Accounts(keys)
	stale := expiring{sessions: sessions, accounts: accounts}
	// The purge first: an expired sign-up is deleted, not rotated.
	if err := purge(ctx, logger, stale, time.Now()); err != nil {
		return exitFailure
	}
	// Before anything reads a secret: a secret under an unknown key stops
	// the server, and the previous key can go once this has run.
	rotated, err := accounts.RotateSecrets(ctx)
	if err != nil {
		logger.Error("rotate the stored secrets (RAUN_SECRET_KEY, RAUN_SECRET_KEY_PREVIOUS)", slog.String("error", err.Error()))
		return exitFailure
	}
	if rotated > 0 {
		logger.Info("stored secrets sealed under the new key", slog.Int("count", rotated))
	}
	var login *identity.Login
	var workspaces *identity.Workspaces
	if forge != nil {
		login = identity.NewLogin(forge, cfg.signup, accounts, rand.Reader, time.Now)
		workspaces = identity.NewWorkspaces(accounts, rand.Reader, time.Now)
	}

	handler, err := web.New(web.Options{
		Logger:     logger,
		PublicHost: cfg.publicHost,
		Scheme:     cfg.publicURL.Scheme,
		Ready:      store.CheckReady,
		Sessions:   sessions,
		Login:      login,
		Accounts:   accounts,
		Workspaces: workspaceService(workspaces),
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
		ticker := time.NewTicker(purgeInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	go purgeEvery(ctx, logger, stale, ticks, done)

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
