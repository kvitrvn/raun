package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driving/web"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// sweeper ends the runs whose lease or deadline has passed and the jobs
// queued too long: job.Store.Sweep.
type sweeper interface {
	Sweep(ctx context.Context, now time.Time) (int, error)
}

// credentialReaper revokes the clone credentials of the runs that ended:
// job.Dispatcher.Reap.
type credentialReaper interface {
	Reap(ctx context.Context) int
}

// runsBoard is b as the web handler takes it: nil without repositories,
// whose pages the runs belong to.
func runsBoard(repositories *repository.Service, b *job.Board) web.RunsBoard {
	if repositories == nil {
		return nil
	}
	return b
}

// runnerDispatcher is d as the web handler takes it: nil without one.
func runnerDispatcher(d *job.Dispatcher) web.RunnerDispatcher {
	if d == nil {
		return nil
	}
	return d
}

// reapEvery revokes, on each tick until done, the clone credentials of the
// runs that ended, apart from the sweep of the queue, which a forge that
// hangs never holds up. The stop of the server cancels ctx, and with it the
// revocations in progress.
func reapEvery(ctx context.Context, logger *slog.Logger, reap credentialReaper, ticks <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticks:
			if n := reap.Reap(ctx); n > 0 {
				logger.Info("clone credentials of ended runs let go", slog.Int("credentials", n))
			}
		}
	}
}

// sweepEvery sweeps the queue on each tick until done, telling swept, when
// set, what each sweep did. A failure is logged and the next tick tries
// again; the stop of the server cancels ctx, and with it a sweep in
// progress.
func sweepEvery(ctx context.Context, logger *slog.Logger, s sweeper, ticks <-chan time.Time, swept func(int, error), done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case now := <-ticks:
			n, err := s.Sweep(ctx, now)
			switch {
			case err != nil && ctx.Err() == nil:
				logger.Warn("sweep the queue of jobs", slog.String("error", err.Error()))
			case n > 0:
				logger.Info("jobs ended or sent back to the queue", slog.Int("jobs", n))
			}
			if swept != nil {
				swept(n, err)
			}
		}
	}
}

// sweepInterval is how often the queue is swept.
const sweepInterval = job.SweepEvery
