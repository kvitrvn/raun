package job

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The timings of raun runner (epic 6 record, Runner and Git).
const (
	// FetchTries is how many times a runner tries to fetch the commit of a
	// job before it fails the job.
	FetchTries = 3
	// fetchPause is the wait after the first failed fetch, doubled after
	// each one.
	fetchPause = time.Second
	// resendFirst and resendMax bound the wait between two sends of a
	// result that did not get an answer.
	resendFirst = time.Second
	resendMax   = 10 * time.Second
	// flushEvery is how often the log of a run is sent while it runs.
	flushEvery = 2 * time.Second
	// claimPauseFirst and claimPauseMax bound the wait after a claim that
	// failed for want of a server.
	claimPauseFirst = time.Second
	claimPauseMax   = 30 * time.Second
)

// RunPrefix starts the name of the directory of each run below the work
// directory; only those are removed at start.
const RunPrefix = "run-"

// ErrProtocol: the server does not speak the protocol of this runner. The
// runner stops: it cannot work with that server.
var ErrProtocol = errors.New("the server does not speak the protocol of this runner")

// Work is a job given to a runner: its run, the lease token of the run, the
// job, and the clone credential that fetches its commit.
type Work struct {
	Run          string
	Lease        Secret
	LeaseExpires time.Time
	Deadline     time.Time
	Kind         Kind
	Forge        string
	Repository   string
	FullName     string
	Commit       string
	Ref          string
	Clone        Credential
}

// Renewal answers a heartbeat: the new end of the lease, or that the job
// is cancelled.
type Renewal struct {
	Expires time.Time
	Cancel  bool
}

// Remote is the server as a runner reaches it: the runner API. Any other
// error than those named is transient, and the runner tries again.
type Remote interface {
	// Claim waits for a job the runner may take: ErrNoJob after the wait
	// of the server. ErrUnauthenticated and ErrProtocol stop the runner.
	Claim(ctx context.Context) (Work, error)
	// Heartbeat renews the lease of run. ErrStaleLease when the run is no
	// longer current.
	Heartbeat(ctx context.Context, run string, lease Secret) (Renewal, error)
	// AppendLog sends the chunk numbered seq of the log of run. A
	// SequenceError names the chunk the server expects; ErrStaleLease and
	// ErrCancelled end the run.
	AppendLog(ctx context.Context, run string, lease Secret, seq int, text string) error
	// Report sends the result of run. ErrStaleLease, ErrCancelled and
	// ErrReported end the run; ErrInvalidResult says the job failed.
	Report(ctx context.Context, run string, lease Secret, r Result) error
}

// Snapshot is the commit of a job as the checkout fetched it. The kinds that
// read files (epic 7) read it through the commit reader of the knowledge
// context, which the snapshot of the checkout also is.
type Snapshot interface {
	// Commit is the full hash of the commit fetched.
	Commit() string
}

// Checkout fetches the one commit of a job into an empty directory of its
// own, with the clone credential of the job, once; the loop tries again.
type Checkout interface {
	Fetch(ctx context.Context, dir string, w Work) (Snapshot, error)
}

// Task is what the handler of a kind is given: the job and its commit,
// never a credential.
type Task struct {
	// Run is the identifier of the run, which the knowledge it finds
	// names (epic 10).
	Run        string
	Kind       Kind
	Forge      string
	Repository string
	FullName   string
	Commit     string
	Ref        string
	Source     Snapshot
	// Log is the log of the run: what the handler writes there is sent to
	// the server, the secrets the runner knows redacted.
	Log io.Writer
}

// Handler runs the jobs of one kind. An error fails the job; its text stays
// in the log of the runner, never in the log of the run, since it may hold
// what the handler read.
type Handler interface {
	Run(ctx context.Context, t Task) (Result, error)
}

// HandlerFunc is a function as a Handler.
type HandlerFunc func(ctx context.Context, t Task) (Result, error)

// Run calls f.
func (f HandlerFunc) Run(ctx context.Context, t Task) (Result, error) { return f(ctx, t) }

// Noop is the handler of noop: it does nothing, and succeeds.
func Noop() Handler {
	return HandlerFunc(func(context.Context, Task) (Result, error) {
		return Result{Outcome: OutcomeSucceeded, Summary: "nothing to do"}, nil
	})
}

// Handlers are the handlers of a runner, by kind.
type Handlers map[Kind]Handler

// DefaultHandlers are the handlers of the kinds that exist.
func DefaultHandlers() Handlers {
	return Handlers{KindNoop: Noop()}
}

// LoopOptions configure a Loop.
type LoopOptions struct {
	Remote   Remote
	Checkout Checkout
	Handlers Handlers
	// Capacity is how many jobs run at a time; below 1 means 1.
	Capacity int
	// WorkDir holds the directory of each run.
	WorkDir string
	// Logger receives the steps of the runner; never a credential or the
	// content of a repository. Nil discards them.
	Logger *slog.Logger
	// Now gives the time; nil means time.Now.
	Now func() time.Time
	// After waits; nil means time.After. Tests make the waits short.
	After func(time.Duration) <-chan time.Time
	// Every gives the ticks of a run: its heartbeats every HeartbeatEvery
	// and its log every flush interval; nil means a time.Ticker.
	Every func(time.Duration) (<-chan time.Time, func())
}

// Loop is the loop of raun runner: it claims a job when a slot is free,
// fetches its commit into a directory of its own, runs the handler of its
// kind and reports, renewing the lease of each run meanwhile.
type Loop struct {
	remote   Remote
	checkout Checkout
	handlers Handlers
	capacity int
	workDir  string
	logger   *slog.Logger
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time
	every    func(time.Duration) (<-chan time.Time, func())
}

// NewLoop returns the loop of opts.
func NewLoop(opts LoopOptions) *Loop {
	if opts.Capacity < 1 {
		opts.Capacity = 1
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.Every == nil {
		opts.Every = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &Loop{
		remote: opts.Remote, checkout: opts.Checkout, handlers: opts.Handlers, capacity: opts.Capacity,
		workDir: opts.WorkDir, logger: opts.Logger, now: opts.Now, after: opts.After, every: opts.Every,
	}
}

// CleanWorkDir creates the work directory when it is missing and removes
// the directories of runs that a killed runner left there, and nothing else.
// A link is removed, never followed.
func CleanWorkDir(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create the work directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read the work directory: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), RunPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return n, fmt.Errorf("remove a directory left by a run: %w", err)
		}
		n++
	}
	return n, nil
}

// Run claims and runs jobs until claims is done, then waits for the runs in
// progress, each within its deadline; runs done abandons them, without a
// result. It returns nil after such a stop, and ErrUnauthenticated or
// ErrProtocol, wrapped, when the server refuses the runner: it stops at
// once then.
func (l *Loop) Run(claims, runs context.Context) error {
	claimCtx, stopClaims := context.WithCancel(claims)
	defer stopClaims()
	runCtx, stopRuns := context.WithCancel(runs)
	defer stopRuns()
	var mu sync.Mutex
	var fatal error
	refuse := func(err error) {
		mu.Lock()
		if fatal == nil {
			fatal = err
		}
		mu.Unlock()
		stopClaims()
		stopRuns()
	}

	slots := make(chan struct{}, l.capacity)
	var wg sync.WaitGroup
	pause := time.Duration(0)
claiming:
	for {
		select {
		case slots <- struct{}{}:
		case <-claimCtx.Done():
			break claiming
		}
		w, err := l.remote.Claim(claimCtx)
		switch {
		case claimCtx.Err() != nil:
			<-slots
			break claiming
		case errors.Is(err, ErrNoJob):
			<-slots
			pause = 0
			continue
		case errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrProtocol):
			<-slots
			l.logger.Error("the server refuses this runner", slog.String("error", err.Error()))
			refuse(err)
			break claiming
		case err != nil:
			<-slots
			pause = min(max(pause*2, claimPauseFirst), claimPauseMax)
			l.logger.Warn("claim a job; trying again", slog.String("error", err.Error()), slog.Duration("in", pause))
			select {
			case <-l.after(pause):
			case <-claimCtx.Done():
				break claiming
			}
			continue
		}
		pause = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := l.execute(runCtx, w); err != nil {
				refuse(err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if fatal != nil {
		return fmt.Errorf("runner stopped: %w", fatal)
	}
	return nil
}

// run is one run in progress.
type run struct {
	l      *Loop
	w      Work
	log    *runLog
	logger *slog.Logger

	// flushing serializes the sends of the log: the one of the lease and
	// the last one.
	flushing sync.Mutex

	mu      sync.Mutex
	expires time.Time
	// ended says why the run must stop, once it must: cancelled, no
	// longer current, abandoned or refused.
	ended error
	// fatal is a refusal of the runner by the server.
	fatal error
}

func (r *run) end(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended == nil {
		r.ended = err
	}
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrProtocol) {
		r.fatal = err
	}
}

func (r *run) leaseExpires() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expires
}

// execute runs w, and returns a refusal of the runner by the server, or nil.
func (l *Loop) execute(ctx context.Context, w Work) error {
	r := &run{l: l, w: w, expires: w.LeaseExpires,
		logger: l.logger.With(slog.String("run", w.Run), slog.String("kind", string(w.Kind)))}
	r.log = newRunLog(cloneSecrets(w.Clone)...)
	ctx, cancel := context.WithDeadline(ctx, w.Deadline)
	defer cancel()
	r.logger.Info("job claimed", slog.String("commit", w.Commit))

	// The lease is renewed, and the log sent, from the claim until the
	// result is answered, the fetch included.
	alive, stopAlive := context.WithCancel(ctx)
	kept := make(chan struct{})
	go func() {
		defer close(kept)
		r.keepAlive(alive, cancel)
	}()
	defer func() {
		stopAlive()
		<-kept
	}()

	res := r.work(ctx)
	if res == nil || ctx.Err() != nil {
		return r.stopped(ctx)
	}
	r.logf("%s: %s\n", res.Outcome, res.Summary)
	if err := r.flush(ctx, true); err != nil {
		r.end(err)
	}
	if err := r.report(ctx, *res); err != nil {
		r.end(err)
	}
	return r.stopped(ctx)
}

// stopped logs why the run stopped early, if it did, and returns a refusal
// of the runner, or nil.
func (r *run) stopped(ctx context.Context) error {
	r.mu.Lock()
	ended, fatal := r.ended, r.fatal
	r.mu.Unlock()
	switch {
	case ended != nil:
		r.logger.Warn("run stopped", slog.String("reason", ended.Error()))
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.logger.Warn("run stopped", slog.String("reason", "the deadline of the job passed"))
	case ctx.Err() != nil:
		r.logger.Warn("run abandoned", slog.String("reason", "the runner stops"))
	}
	return fatal
}

// work fetches the commit and runs the handler, in a directory of its own
// removed whatever happens, and returns the result, or nil when the run
// must stop without one.
func (r *run) work(ctx context.Context) *Result {
	dir, err := os.MkdirTemp(r.l.workDir, RunPrefix)
	if err != nil {
		r.logger.Error("create the directory of the run", slog.String("error", err.Error()))
		return &Result{Outcome: OutcomeFailed, Summary: "the runner could not create the directory of the run"}
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			r.logger.Error("remove the directory of the run", slog.String("error", err.Error()))
		}
	}()

	r.logf("fetching %s\n", r.w.Commit)
	snapshot, err := r.fetch(ctx, dir)
	// The credential is forgotten before the handler starts.
	r.w.Clone = Credential{}
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return &Result{Outcome: OutcomeFailed, Summary: "the commit could not be fetched"}
	}
	h, ok := r.l.handlers[r.w.Kind]
	if !ok {
		return &Result{Outcome: OutcomeFailed, Summary: "this runner has no handler for kind " + string(r.w.Kind)}
	}
	r.logf("running %s\n", r.w.Kind)
	res, err := r.handle(ctx, h, Task{
		Run: r.w.Run, Kind: r.w.Kind, Forge: r.w.Forge, Repository: r.w.Repository, FullName: r.w.FullName,
		Commit: r.w.Commit, Ref: r.w.Ref, Source: snapshot, Log: r.log,
	})
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.logger.Warn("the handler failed", slog.String("error", err.Error()))
		return &Result{Outcome: OutcomeFailed, Summary: "the handler failed"}
	}
	return &res
}

// handle runs h, a panic of which fails the job.
func (r *run) handle(ctx context.Context, h Handler, t Task) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the handler panicked: %v", p)
		}
	}()
	return h.Run(ctx, t)
}

// fetch fetches the commit, FetchTries times at most.
func (r *run) fetch(ctx context.Context, dir string) (Snapshot, error) {
	pause := fetchPause
	var err error
	for try := 1; try <= FetchTries; try++ {
		var s Snapshot
		if s, err = r.fetchOnce(ctx, dir, try); err == nil {
			r.logf("fetched\n")
			return s, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.logger.Warn("fetch the commit", slog.Int("try", try), slog.String("error", err.Error()))
		r.logf("the commit could not be fetched, try %d of %d\n", try, FetchTries)
		if try == FetchTries {
			break
		}
		select {
		case <-r.l.after(pause):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		pause *= 2
	}
	return nil, err
}

// fetchOnce fetches into a directory of its own below dir, so a failed try
// leaves nothing for the next one.
func (r *run) fetchOnce(ctx context.Context, dir string, try int) (Snapshot, error) {
	target := filepath.Join(dir, fmt.Sprintf("checkout-%d", try))
	if err := os.Mkdir(target, 0o700); err != nil {
		return nil, err
	}
	s, err := r.l.checkout.Fetch(ctx, target, r.w)
	if err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	if s.Commit() != r.w.Commit {
		return nil, fmt.Errorf("the checkout holds %s, not the commit of the job", s.Commit())
	}
	return s, nil
}

func (r *run) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.log, format, args...)
}

// keepAlive renews the lease every HeartbeatEvery and sends the log every
// flushEvery, until ctx is done; it ends the run, through cancel, when the
// server says it must stop or when its lease has passed.
func (r *run) keepAlive(ctx context.Context, cancel context.CancelFunc) {
	beats, stopBeats := r.l.every(HeartbeatEvery)
	defer stopBeats()
	flushes, stopFlushes := r.l.every(flushEvery)
	defer stopFlushes()
	for {
		select {
		case <-ctx.Done():
			return
		case <-flushes:
			if err := r.flush(ctx, false); err != nil {
				r.end(err)
				cancel()
				return
			}
		case <-beats:
			renewal, err := r.l.remote.Heartbeat(ctx, r.w.Run, r.w.Lease)
			switch {
			case ctx.Err() != nil:
				return
			case err == nil && renewal.Cancel:
				r.end(ErrCancelled)
			case err == nil:
				r.mu.Lock()
				r.expires = renewal.Expires
				r.mu.Unlock()
				continue
			case errors.Is(err, ErrStaleLease) || errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrProtocol):
				r.end(err)
			default:
				r.logger.Warn("renew the lease; trying again", slog.String("error", err.Error()))
				if r.l.now().Before(r.leaseExpires()) {
					continue
				}
				r.end(ErrStaleLease)
			}
			cancel()
			return
		}
	}
}

// flush sends the log taken so far, all of it when final; it returns an
// error that ends the run, and nil when the server could not be reached:
// the next flush tries again.
func (r *run) flush(ctx context.Context, final bool) error {
	r.flushing.Lock()
	defer r.flushing.Unlock()
	for {
		seq, text, ok := r.log.take(final)
		if !ok {
			return nil
		}
		err := r.l.remote.AppendLog(ctx, r.w.Run, r.w.Lease, seq, text)
		var gap *SequenceError
		switch {
		case err == nil:
			r.log.ack(seq)
		case errors.As(err, &gap) && gap.Expected > seq:
			// The server took the chunk, and the answer was lost.
			r.log.ack(seq)
		case errors.As(err, &gap):
			r.logger.Error("the server lost chunks of the log; the rest is dropped", slog.Int("expected", gap.Expected), slog.Int("seq", seq))
			r.log.drop()
			return nil
		case errors.Is(err, ErrStaleLease) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrProtocol):
			return err
		default:
			if ctx.Err() == nil {
				r.logger.Warn("send the log; trying again", slog.String("error", err.Error()))
			}
			if !final {
				return nil
			}
			// The last chunks are sent again until the lease or the
			// deadline passes, with the result after them.
			if !r.wait(ctx) {
				return nil
			}
		}
	}
}

// report sends res until the server answers, within the lease and the
// deadline: a result sent again is the same, and accepted again.
func (r *run) report(ctx context.Context, res Result) error {
	pause := resendFirst
	for {
		err := r.l.remote.Report(ctx, r.w.Run, r.w.Lease, res)
		switch {
		case err == nil:
			r.logger.Info("result sent", slog.String("outcome", string(res.Outcome)))
			return nil
		case errors.Is(err, ErrInvalidResult):
			r.logger.Error("the server refused the result; the job failed", slog.String("error", err.Error()))
			return nil
		case errors.Is(err, ErrStaleLease) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrReported) ||
			errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrProtocol):
			return err
		}
		r.logger.Warn("send the result; trying again", slog.String("error", err.Error()))
		select {
		case <-r.l.after(pause):
		case <-ctx.Done():
			return nil
		}
		if !r.l.now().Before(r.leaseExpires()) {
			return ErrStaleLease
		}
		pause = min(pause*2, resendMax)
	}
}

// wait waits before a chunk is sent again, and reports whether the lease
// still holds.
func (r *run) wait(ctx context.Context) bool {
	select {
	case <-r.l.after(resendFirst):
	case <-ctx.Done():
		return false
	}
	return r.l.now().Before(r.leaseExpires())
}

// cloneSecrets are the forms of the credential a log could show: the
// password, and the user and password of an HTTP Basic header.
func cloneSecrets(c Credential) []string {
	p := c.Password.Reveal()
	if p == "" {
		return nil
	}
	return []string{p, base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + p))}
}
