package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kvitrvn/raun/internal/adapter/driven/git"
	"github.com/kvitrvn/raun/internal/adapter/driven/runnerclient"
	"github.com/kvitrvn/raun/internal/core/job"
)

// runnerDeps holds what tests replace in the wiring of the runner. The zero
// value is the production wiring.
type runnerDeps struct {
	// signals, when set, receives the stop requests instead of SIGINT and
	// SIGTERM.
	signals <-chan os.Signal
	// git, when set, is the git binary instead of the one in PATH.
	git string
	// release, when set, is the release the runner says instead of the
	// one of the build.
	release string
	// started, when set, is called once the runner claims.
	started func()
	// handlers, when set, are the handlers instead of job.DefaultHandlers.
	handlers job.Handlers
}

// runRunner runs raun runner until it is stopped, and returns its exit code:
// 0 after a stop by a signal, 1 when it cannot start or the server refuses
// it. The first signal stops the claims and lets the current jobs finish
// within their deadlines; a second one abandons them. Standard output stays
// empty.
func runRunner(environ []string, stderr io.Writer, deps runnerDeps) int {
	cfg, err := loadRunnerConfig(environ)
	if err != nil {
		fmt.Fprintf(stderr, "raun runner: invalid configuration:\n%v\n", err)
		return exitFailure
	}
	logger := newLogger(slog.LevelInfo, "json", stderr)
	logger.LogAttrs(context.Background(), slog.LevelInfo, "configuration", cfg.logAttrs()...)

	version, err := git.CheckVersion(context.Background(), deps.git)
	if err != nil {
		logger.Error("check the git binary: Git 2.32 or later is required", slog.String("error", err.Error()))
		return exitFailure
	}
	logger.Info("git", slog.String("version", version))
	left, err := job.CleanWorkDir(cfg.workDir)
	if err != nil {
		logger.Error("prepare the work directory (RAUN_RUNNER_WORKDIR)", slog.String("error", err.Error()))
		return exitFailure
	}
	if left > 0 {
		logger.Info("directories left by a stopped runner removed", slog.Int("count", left))
	}

	release := deps.release
	if release == "" {
		release = runnerRelease(readBuildInfo())
	}
	client, err := runnerclient.New(runnerclient.Options{Server: cfg.server, Token: cfg.token, Release: release})
	if err != nil {
		logger.Error("build the client of the server", slog.String("error", err.Error()))
		return exitFailure
	}
	handlers := deps.handlers
	if handlers == nil {
		handlers = job.DefaultHandlers()
	}
	loop := job.NewLoop(job.LoopOptions{
		Remote:   client,
		Checkout: git.Fetcher{Options: git.Options{Git: deps.git}},
		Handlers: handlers,
		Capacity: cfg.capacity,
		WorkDir:  cfg.workDir,
		Logger:   logger,
	})

	signals := deps.signals
	if signals == nil {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ch)
		signals = ch
	}
	claims, stopClaims := context.WithCancel(context.Background())
	defer stopClaims()
	runs, abandon := context.WithCancel(context.Background())
	defer abandon()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for n := 0; ; n++ {
			select {
			case <-done:
				return
			case <-signals:
			}
			if n == 0 {
				logger.Info("stopping: no more claims; the current jobs finish within their deadlines")
				stopClaims()
				continue
			}
			logger.Warn("stopping now: the current jobs are abandoned")
			abandon()
			return
		}
	}()

	logger.Info("runner started", slog.String("server", cfg.server), slog.String("release", release), slog.Int("capacity", cfg.capacity))
	if deps.started != nil {
		deps.started()
	}
	if err := loop.Run(claims, runs); err != nil {
		logger.Error("the server refuses this runner", slog.String("error", err.Error()))
		return exitFailure
	}
	logger.Info("runner stopped")
	return exitOK
}
