package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// sweeper ends the runs whose lease or deadline has passed and the jobs
// queued too long: job.Store.Sweep.
type sweeper interface {
	Sweep(ctx context.Context, now time.Time) (int, error)
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
