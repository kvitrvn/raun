package job

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// MaxListed is how many jobs the Runs list of a repository shows: the
// newest.
const MaxListed = 50

// JobRuns is a job with its runs, oldest first.
type JobRuns struct {
	Job  Job
	Runs []Run
}

// Reads are the reads of the screens of jobs, runs and runners, apart from
// Store. Every store passes the contract suite of package jobtest.
type Reads interface {
	// JobsOf returns the jobs of the repository of forge with this stable
	// identifier, newest first by creation then identifier, limit at most,
	// each with its runs; in every state.
	JobsOf(ctx context.Context, forge, repository string, limit int) ([]JobRuns, error)
	// OpenRuns returns how many open runs each runner holds, by runner;
	// those without any are left out.
	OpenRuns(ctx context.Context) (map[string]int, error)
}

// Wait says why a queued job is not claimed yet.
type Wait string

const (
	// WaitNone: a runner may take it at its next claim.
	WaitNone Wait = ""
	// WaitDelayed: it may not be claimed before NotBefore, after its
	// clone credential could not be minted.
	WaitDelayed Wait = "delayed"
	// WaitNoRunner: no runner that is not revoked serves its workspace
	// with the capability of its kind.
	WaitNoRunner Wait = "no-runner"
	// WaitNotSeen: such runners exist, but none was seen in the last two
	// minutes.
	WaitNotSeen Wait = "not-seen"
)

// seenWithin is how recent the last contact of a runner must be for it to
// count as running: two of the intervals at which it is written.
const seenWithin = 2 * ContactEvery

// Waiting returns why j, queued, waits at now, given every runner; WaitNone
// for a job that is not queued.
func Waiting(j Job, runners []Runner, now time.Time) Wait {
	if j.State != StateQueued {
		return WaitNone
	}
	if now.Before(j.NotBefore) {
		return WaitDelayed
	}
	able := false
	for _, r := range runners {
		if r.Revoked() || !r.Claimant().May(j) {
			continue
		}
		able = true
		if !r.LastContact.IsZero() && now.Sub(r.LastContact) < seenWithin {
			return WaitNone
		}
	}
	if !able {
		return WaitNoRunner
	}
	return WaitNotSeen
}

// JobView is a job as the screens show it: its runs and why it waits.
type JobView struct {
	JobRuns
	Wait Wait
}

// LogPart is what a screen shows of the log of a run from an offset.
type LogPart struct {
	// Text is the log from the offset; all of it when Reset.
	Text string
	// Next is the offset to read from next time.
	Next int
	// Reset says the text shown must be replaced by Text: the log was cut
	// since, which may have shortened its end.
	Reset bool
	// Cut says the log ends with Truncated.
	Cut bool
	// Ended says the run has ended: nothing more will come.
	Ended bool
}

// ErrBadOffset: an offset is not one of the log.
var ErrBadOffset = errors.New("not an offset of the log")

// BoardOptions configure a Board.
type BoardOptions struct {
	Store   Store
	Reads   Reads
	Runners RunnerStore
	// Now gives the time; nil means time.Now.
	Now func() time.Time
}

// Board is what the screens of a repository read of its jobs and runs, and
// the cancellation by hand. It knows nothing of who asks: its caller
// checks, with the forge, that the person may read the repository, or
// decide on it to cancel; every job and run it gives belongs to the
// repository it is asked about, and ErrNotFound answers any other.
type Board struct {
	store   Store
	reads   Reads
	runners RunnerStore
	now     func() time.Time
}

// NewBoard returns the board of opts.
func NewBoard(opts BoardOptions) *Board {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Board{store: opts.Store, reads: opts.Reads, runners: opts.Runners, now: opts.Now}
}

// Jobs returns the newest jobs of the repository of forge with this stable
// identifier, MaxListed at most, newest first, with their runs and why
// those queued wait.
func (b *Board) Jobs(ctx context.Context, forge, repository string) ([]JobView, error) {
	jobs, err := b.reads.JobsOf(ctx, forge, repository, MaxListed)
	if err != nil {
		return nil, fmt.Errorf("list the jobs: %w", err)
	}
	var runners []Runner
	if slices.ContainsFunc(jobs, func(j JobRuns) bool { return j.Job.State == StateQueued }) {
		if runners, err = b.runners.Runners(ctx); err != nil {
			return nil, fmt.Errorf("list the runners: %w", err)
		}
	}
	now := b.now()
	out := make([]JobView, len(jobs))
	for i, j := range jobs {
		out[i] = JobView{JobRuns: j, Wait: Waiting(j.Job, runners, now)}
	}
	return out, nil
}

// Deliveries returns how many of the newest jobs of the repository, MaxListed
// at most, there are, and how many of them each delivery created, without
// reading the runners.
func (b *Board) Deliveries(ctx context.Context, forge, repository string) (int, map[string]int, error) {
	jobs, err := b.reads.JobsOf(ctx, forge, repository, MaxListed)
	if err != nil {
		return 0, nil, fmt.Errorf("list the jobs: %w", err)
	}
	per := map[string]int{}
	for _, j := range jobs {
		if j.Job.Delivery != "" {
			per[j.Job.Delivery]++
		}
	}
	return len(jobs), per, nil
}

// Job returns the job with this identifier, of the repository of forge with
// this stable identifier, with its runs and why it waits. ErrNotFound for
// no such job of that repository.
func (b *Board) Job(ctx context.Context, forge, repository, id string) (JobView, error) {
	j, err := b.own(ctx, forge, repository, id)
	if err != nil {
		return JobView{}, err
	}
	runs, err := b.store.Runs(ctx, j.ID)
	if err != nil {
		return JobView{}, fmt.Errorf("read the runs: %w", err)
	}
	var runners []Runner
	if j.State == StateQueued {
		if runners, err = b.runners.Runners(ctx); err != nil {
			return JobView{}, fmt.Errorf("list the runners: %w", err)
		}
	}
	return JobView{JobRuns: JobRuns{Job: j, Runs: runs}, Wait: Waiting(j, runners, b.now())}, nil
}

// own returns the job with this identifier when it belongs to the
// repository; ErrNotFound otherwise, the same as for no job.
func (b *Board) own(ctx context.Context, forge, repository, id string) (Job, error) {
	j, err := b.store.Job(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.Forge != forge || j.Repository != repository {
		return Job{}, ErrNotFound
	}
	return j, nil
}

// Log returns the log of the run with this identifier of the job with this
// identifier, of the repository, from offset, a byte of its text at the
// start of a character, as a screen that showed it up to there sees it;
// shownCut says whether the text shown ended with the cut. ErrNotFound for
// no such run of that job; ErrBadOffset for an offset past the text, inside
// a character, or negative, unless the log was cut since, which resets it.
func (b *Board) Log(ctx context.Context, forge, repository, job, run string, offset int, shownCut bool) (LogPart, error) {
	j, err := b.own(ctx, forge, repository, job)
	if err != nil {
		return LogPart{}, err
	}
	r, err := b.store.Run(ctx, run)
	if err != nil {
		return LogPart{}, err
	}
	if r.Job != j.ID {
		return LogPart{}, ErrNotFound
	}
	l, err := b.store.Log(ctx, run)
	if err != nil {
		return LogPart{}, fmt.Errorf("read the log: %w", err)
	}
	part := LogPart{Next: len(l.Text), Cut: l.Cut, Ended: !r.Open()}
	// A cut may shorten the end of what was shown: the whole log then.
	if l.Cut && !shownCut {
		part.Text, part.Reset = l.Text, true
		return part, nil
	}
	if offset < 0 || offset > len(l.Text) || (offset < len(l.Text) && !utf8.RuneStart(l.Text[offset])) {
		return LogPart{}, ErrBadOffset
	}
	part.Text = l.Text[offset:]
	return part, nil
}

// Cancel cancels by hand the job with this identifier of the repository,
// queued or running. ErrNotFound for no such job of that repository;
// ErrFinal, with the job, for one that has ended.
func (b *Board) Cancel(ctx context.Context, forge, repository, id string) (Job, error) {
	if _, err := b.own(ctx, forge, repository, id); err != nil {
		return Job{}, err
	}
	return b.store.Cancel(ctx, id, ReasonCancelled, b.now())
}
