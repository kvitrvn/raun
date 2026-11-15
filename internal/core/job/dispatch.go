package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ClaimWait is how long a claim waits for a job before it answers that
// there is none: below the usual timeouts of a proxy, and well within the
// time the server gives a response.
const ClaimWait = 30 * time.Second

// revokeTimeout bounds one revocation of a clone credential, which runs
// apart from the request that ended the run.
const revokeTimeout = 10 * time.Second

// mintTimeout bounds the mint of one clone credential: a claim waits
// ClaimWait, then mints once more at most, and answers well within the time
// the server gives a response.
const mintTimeout = 15 * time.Second

// reapParallel bounds the revocations Reap sends at once.
const reapParallel = 8

// ErrNoClone: the forge of the job cannot mint a clone credential.
var ErrNoClone = errors.New("the forge of the job cannot mint a clone credential")

// DispatcherOptions configure a Dispatcher.
type DispatcherOptions struct {
	Runners RunnerStore
	Jobs    Store
	// Clone mints the clone credentials of the jobs of Forge; nil means
	// none can be minted, and no job is given.
	Clone Clone
	// Forge is the name of the forge of Clone.
	Forge string
	// Notifier wakes the claims that wait; the stores tell it.
	Notifier *Notifier
	// Wait is how long a claim waits; zero means ClaimWait.
	Wait time.Duration
	// Logger receives what fails apart from a request: a credential that
	// cannot be minted or revoked. Nil discards it.
	Logger *slog.Logger
	// Now gives the time of an operation; nil means time.Now.
	Now func() time.Time
}

// Dispatcher hands jobs to runners over the protocol of epic 6: a claim
// that waits for a job, the clone credential of the run, minted after the
// claim and held in memory only, and the operations of a run, each bound
// to the runner that holds it. It revokes the credential of a run when the
// run ends.
type Dispatcher struct {
	runners  RunnerStore
	jobs     Store
	clone    Clone
	forge    string
	notifier *Notifier
	wait     time.Duration
	logger   *slog.Logger
	now      func() time.Time

	mu sync.Mutex
	// held are the clone credentials of the runs that may still use them,
	// by run.
	held map[string]Credential
}

// NewDispatcher returns the dispatcher of opts.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	if opts.Wait <= 0 {
		opts.Wait = ClaimWait
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Notifier == nil {
		opts.Notifier = &Notifier{}
	}
	return &Dispatcher{
		runners: opts.Runners, jobs: opts.Jobs, clone: opts.Clone, forge: opts.Forge,
		notifier: opts.Notifier, wait: opts.Wait, logger: opts.Logger, now: opts.Now,
		held: map[string]Credential{},
	}
}

// Assignment is a job given to a runner: its run, the lease token of the
// run and the clone credential that fetches its commit.
type Assignment struct {
	Claimed
	Clone Credential
}

// Claim gives the runner with this identifier a job it may take, waiting
// for one up to the wait of the dispatcher, until ctx is done or stop is
// closed: ErrNoJob then. The wait holds no transaction: each wake-up claims
// again. A job whose credential cannot be minted is not given: its run is
// removed, the job may be claimed again MintDelay later, and the claim goes
// on waiting, until its wait has passed. ErrUnauthenticated for a runner
// revoked meanwhile.
func (d *Dispatcher) Claim(ctx context.Context, runner string, stop <-chan struct{}) (Assignment, error) {
	timer := time.NewTimer(d.wait)
	defer timer.Stop()
	for {
		// The wait ends between two tries too, after a mint that failed.
		select {
		case <-timer.C:
			return Assignment{}, ErrNoJob
		case <-stop:
			return Assignment{}, ErrNoJob
		case <-ctx.Done():
			return Assignment{}, ErrNoJob
		default:
		}
		wake := d.notifier.Wait()
		c, err := d.runners.ClaimFor(ctx, runner, d.now())
		switch {
		case errors.Is(err, ErrNoJob):
			select {
			case <-wake:
				continue
			case <-timer.C:
			case <-stop:
			case <-ctx.Done():
			}
			return Assignment{}, ErrNoJob
		case err != nil:
			return Assignment{}, err
		}
		if a, ok := d.assign(ctx, c); ok {
			return a, nil
		}
	}
}

// assign mints the credential of c, outside any transaction, and holds it
// for its run. A run whose credential cannot be minted is released; one
// that ended during the mint, such as by a cancellation, is not given and
// its credential is revoked at once.
func (d *Dispatcher) assign(ctx context.Context, c Claimed) (Assignment, bool) {
	cred, err := d.mint(ctx, c.Job)
	if err != nil {
		d.logger.Warn("mint the clone credential of a run; the job waits", slog.String("run", c.Run.ID),
			slog.String("job", c.Job.ID), slog.String("error", err.Error()))
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
		defer cancel()
		if err := d.jobs.Release(release, c.Run.ID, c.Token, d.now()); err != nil && !errors.Is(err, ErrStaleLease) {
			// The run stays, unseen: its lease expires and it is lost.
			d.logger.Warn("release a run whose credential could not be minted", slog.String("run", c.Run.ID),
				slog.String("error", err.Error()))
		}
		return Assignment{}, false
	}
	d.hold(c.Run.ID, cred)
	r, err := d.jobs.Run(ctx, c.Run.ID)
	if err != nil || !r.Open() {
		d.revoke(ctx, c.Run.ID)
		return Assignment{}, false
	}
	return Assignment{Claimed: c, Clone: cred}, true
}

func (d *Dispatcher) mint(ctx context.Context, j Job) (Credential, error) {
	if d.clone == nil || j.Forge != d.forge {
		return Credential{}, fmt.Errorf("%w: %s", ErrNoClone, j.Forge)
	}
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	return d.clone.MintClone(ctx, j.Repository)
}

func (d *Dispatcher) hold(run string, c Credential) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.held[run] = c
}

// revoke revokes the credential of run, if one is held, apart from ctx: a
// request that ends does not stop it.
func (d *Dispatcher) revoke(ctx context.Context, run string) {
	d.revokeWithin(context.WithoutCancel(ctx), run)
}

// revokeWithin revokes the credential of run, if one is held, until ctx is
// done. Taking it from the held ones first, one revocation at most runs
// for a credential. It is sent whatever the clock of the server says of the
// expiry, which the clock of the forge decides; a revocation that fails is
// tried again by Reap until the credential expires by the clock of the
// server.
func (d *Dispatcher) revokeWithin(ctx context.Context, run string) {
	d.mu.Lock()
	c, ok := d.held[run]
	delete(d.held, run)
	d.mu.Unlock()
	if !ok || d.clone == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	if err := d.clone.RevokeClone(rctx, c); err != nil {
		if !d.now().Before(c.Expires) {
			return
		}
		d.logger.Warn("revoke the clone credential of a run; tried again later", slog.String("run", run),
			slog.String("error", err.Error()))
		d.mu.Lock()
		if _, again := d.held[run]; !again {
			d.held[run] = c
		}
		d.mu.Unlock()
	}
}

// forget lets go of the credential of run without revoking it.
func (d *Dispatcher) forget(run string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.held, run)
}

// Held returns how many clone credentials the dispatcher holds.
func (d *Dispatcher) Held() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.held)
}

// Reap revokes the credentials of the runs that have ended, whatever ended
// them: a sweep, a cancellation, the revocation of their runner, the
// deactivation of their repository; it tries again the revocations that
// failed, and forgets the credentials that expired. It sends reapParallel
// revocations at once at most, and stops with ctx. It returns how many
// credentials it let go or tried to revoke.
func (d *Dispatcher) Reap(ctx context.Context) int {
	d.mu.Lock()
	runs := make([]string, 0, len(d.held))
	for run := range d.held {
		runs = append(runs, run)
	}
	d.mu.Unlock()
	var ended []string
	n := 0
	for _, run := range runs {
		r, err := d.jobs.Run(ctx, run)
		switch {
		case errors.Is(err, ErrNotFound) || (err == nil && !r.Open()):
			ended = append(ended, run)
		case err != nil:
			continue
		default:
			// A run still open keeps its credential until it expires.
			d.mu.Lock()
			c, ok := d.held[run]
			d.mu.Unlock()
			if !ok || d.now().Before(c.Expires) {
				continue
			}
			d.forget(run)
			n++
		}
	}
	slots := make(chan struct{}, reapParallel)
	var wg sync.WaitGroup
	for _, run := range ended {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return n
		}
		n++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			d.revokeWithin(ctx, run)
		}()
	}
	wg.Wait()
	return n
}

// own returns the run with this identifier when the runner with this
// identifier holds it; ErrStaleLease otherwise, so a run of another runner
// is refused as a foreign lease is.
func (d *Dispatcher) own(ctx context.Context, runner, run string) (Run, error) {
	r, err := d.jobs.Run(ctx, run)
	if errors.Is(err, ErrNotFound) {
		return Run{}, ErrStaleLease
	}
	if err != nil {
		return Run{}, err
	}
	if r.Runner != runner {
		return Run{}, ErrStaleLease
	}
	return r, nil
}

// ended revokes the credential of run when err says the run no longer
// runs: cancelled, or no longer holding its lease. A wrong lease token
// alone leaves the credential of a run that still holds its lease.
func (d *Dispatcher) ended(ctx context.Context, run string, err error) {
	switch {
	case errors.Is(err, ErrCancelled):
		d.revoke(ctx, run)
	case errors.Is(err, ErrStaleLease):
		r, rerr := d.jobs.Run(ctx, run)
		if errors.Is(rerr, ErrNotFound) || (rerr == nil && !r.Holds(d.now())) {
			d.revoke(ctx, run)
		}
	}
}

// Heartbeat renews the lease of the run with this identifier, held by the
// runner with this identifier with the lease token token, and returns the
// new end of its lease. ErrCancelled when its job was cancelled, after
// which the runner stops; ErrStaleLease when the run is not its current
// one, holding its lease.
func (d *Dispatcher) Heartbeat(ctx context.Context, runner, run, token string) (time.Time, error) {
	if _, err := d.own(ctx, runner, run); err != nil {
		return time.Time{}, err
	}
	r, err := d.jobs.Heartbeat(ctx, run, token, d.now())
	if err != nil {
		d.ended(ctx, run, err)
		return time.Time{}, err
	}
	return r.LeaseExpires, nil
}

// AppendLog appends the chunk numbered seq holding text to the log of the
// run, as Store.AppendLog, for the runner that holds it.
func (d *Dispatcher) AppendLog(ctx context.Context, runner, run, token string, seq int, text string) error {
	if _, err := d.own(ctx, runner, run); err != nil {
		return err
	}
	err := d.jobs.AppendLog(ctx, run, token, seq, text, d.now())
	d.ended(ctx, run, err)
	return err
}

// Kind returns the kind of the job of the run, for the runner that holds
// it, so its result can be bounded before it is read.
func (d *Dispatcher) Kind(ctx context.Context, runner, run string) (Kind, error) {
	r, err := d.own(ctx, runner, run)
	if err != nil {
		return "", err
	}
	j, err := d.jobs.Job(ctx, r.Job)
	if err != nil {
		return "", err
	}
	return j.Kind, nil
}

// Report ends the run with the result res, as Store.Report, for the runner
// that holds it, and revokes its credential: whatever the answer, but a
// failure of the store, the run has ended.
func (d *Dispatcher) Report(ctx context.Context, runner, run, token string, res Result) error {
	if _, err := d.own(ctx, runner, run); err != nil {
		return err
	}
	err := d.jobs.Report(ctx, run, token, res, d.now())
	if err == nil || errors.Is(err, ErrInvalidResult) || errors.Is(err, ErrReported) {
		d.revoke(ctx, run)
	}
	d.ended(ctx, run, err)
	return err
}
