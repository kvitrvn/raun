package job

import (
	"context"
	"time"
)

// Store keeps the jobs and their runs, and applies each operation of the
// queue atomically. Every time comes from the caller. Every store passes the
// contract suite of package jobtest.
type Store interface {
	// Enqueue stores j, made by New. When j has a key, the queued jobs of
	// its workspace with the same key are cancelled as superseded at
	// j.CreatedAt, in the same operation; a running job is never touched.
	Enqueue(ctx context.Context, j Job) error
	// Claim gives c the first job it may take at now, by Before, and
	// starts its run: the job is running and the run holds its lease. It
	// first ends what Sweep would end at now, so it never waits for the
	// sweeper. ErrNoJob when none may be claimed.
	Claim(ctx context.Context, c Claimant, now time.Time) (Claimed, error)
	// Heartbeat renews the lease of the run with this identifier, whose
	// lease token is token, to now plus Lease, and returns it. ErrCancelled
	// when its job was cancelled; ErrStaleLease when the run is unknown,
	// the token does not match, or the run no longer holds its lease.
	Heartbeat(ctx context.Context, run, token string, now time.Time) (Run, error)
	// Report ends the run with the result r, and its job as r says. A
	// result its kind refuses fails the run and the job, stores nothing of
	// r, and returns an error wrapping ErrInvalidResult. The same result
	// sent again for the same run is accepted again; another one is
	// ErrReported. ErrCancelled and ErrStaleLease as for Heartbeat.
	Report(ctx context.Context, run, token string, r Result, now time.Time) error
	// Release removes the run with this identifier, which its runner was
	// never given because its clone credential could not be minted, and
	// puts its job back in the queue, claimable from now plus MintDelay;
	// the run is not counted. ErrStaleLease when the run is unknown, the
	// token does not match or the run has ended, such as by a
	// cancellation; nothing changes then.
	Release(ctx context.Context, run, token string, now time.Time) error
	// Cancel cancels the job with this identifier for reason at now: a
	// queued job at once, a running one with its run, which ends as
	// cancelled. ErrNotFound for no job; ErrFinal for a job that has
	// ended, which is returned unchanged.
	Cancel(ctx context.Context, id string, reason Reason, now time.Time) (Job, error)
	// CancelRepository cancels for reason at now the queued and running
	// jobs of the repository of forge with this stable identifier, as
	// Cancel does, and returns how many it cancelled.
	CancelRepository(ctx context.Context, forge, repository string, reason Reason, now time.Time) (int, error)
	// Sweep ends, at now: the open runs past their deadline, as failed,
	// with their jobs failed as timeout, without retry; then the open runs
	// whose lease has expired, as lost, with their jobs queued again or
	// failed by AfterLost; then the queued jobs Expired at now, as failed.
	// It returns how many jobs it changed.
	Sweep(ctx context.Context, now time.Time) (int, error)
	// Job returns the job with this identifier; ErrNotFound if none.
	Job(ctx context.Context, id string) (Job, error)
	// Runs returns the runs of the job with this identifier, oldest
	// first; none for an unknown job.
	Runs(ctx context.Context, job string) ([]Run, error)
	// Run returns the run with this identifier; ErrNotFound if none, such
	// as a run removed by Release.
	Run(ctx context.Context, id string) (Run, error)
	// AppendLog appends to the log of the run with this identifier, whose
	// lease token is token, the chunk numbered seq holding text, by the
	// rules of Log.Append, while the run holds its lease at now. ErrInvalidLog
	// and a SequenceError as Log.Append; ErrCancelled and ErrStaleLease as
	// for Heartbeat. Nothing changes on an error.
	AppendLog(ctx context.Context, run, token string, seq int, text string, now time.Time) error
	// Log returns the log of the run with this identifier; an empty one
	// for a run that sent none, or an unknown run.
	Log(ctx context.Context, run string) (Log, error)
	// PurgeLogs deletes the logs of the runs that ended before before, and
	// returns how many it deleted.
	PurgeLogs(ctx context.Context, before time.Time) (int, error)
}
