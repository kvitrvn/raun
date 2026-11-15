package job_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

const secret = "clone-secret-value"

// remote is a job.Remote in memory: Claim hands out the works sent on
// works, and the other calls follow the functions set.
type remote struct {
	works chan job.Work
	// claimErr, when set, answers every claim.
	claimErr error

	mu        sync.Mutex
	heartbeat func(run string) (job.Renewal, error)
	appendLog func(run string, seq int) error
	report    func(run string, n int) error
	logs      map[string]string
	results   map[string][]job.Result
	reported  chan string
}

func newRemote() *remote {
	return &remote{works: make(chan job.Work, 16), logs: map[string]string{}, results: map[string][]job.Result{}, reported: make(chan string, 16)}
}

func (r *remote) Claim(ctx context.Context) (job.Work, error) {
	if r.claimErr != nil {
		return job.Work{}, r.claimErr
	}
	select {
	case w := <-r.works:
		return w, nil
	case <-ctx.Done():
		return job.Work{}, job.ErrNoJob
	case <-time.After(20 * time.Millisecond):
		return job.Work{}, job.ErrNoJob
	}
}

func (r *remote) Heartbeat(_ context.Context, run string, _ job.Secret) (job.Renewal, error) {
	r.mu.Lock()
	f := r.heartbeat
	r.mu.Unlock()
	if f != nil {
		return f(run)
	}
	return job.Renewal{Expires: time.Now().Add(job.Lease)}, nil
}

func (r *remote) AppendLog(_ context.Context, run string, _ job.Secret, seq int, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendLog != nil {
		if err := r.appendLog(run, seq); err != nil {
			return err
		}
	}
	r.logs[run] += text
	return nil
}

func (r *remote) Report(_ context.Context, run string, _ job.Secret, res job.Result) error {
	r.mu.Lock()
	r.results[run] = append(r.results[run], res)
	n := len(r.results[run])
	f := r.report
	r.mu.Unlock()
	if f != nil {
		if err := f(run, n); err != nil {
			return err
		}
	}
	r.reported <- run
	return nil
}

func (r *remote) log(run string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs[run]
}

func (r *remote) resultsOf(run string) []job.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]job.Result(nil), r.results[run]...)
}

// snapshot is the commit a checkout fetched.
type snapshot string

func (s snapshot) Commit() string { return string(s) }

// checkout fetches by writing a file into the directory it is given, and
// fails the first fail tries.
type checkout struct {
	mu    sync.Mutex
	fail  int
	tries int
	dirs  []string
	creds []job.Credential
}

func (c *checkout) Fetch(_ context.Context, dir string, w job.Work) (job.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries++
	c.dirs = append(c.dirs, dir)
	c.creds = append(c.creds, w.Clone)
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(w.Commit), 0o600); err != nil {
		return nil, err
	}
	if c.tries <= c.fail {
		return nil, errors.New("the forge is down")
	}
	return snapshot(w.Commit), nil
}

var commit = strings.Repeat("c", 40)

func work(run string) job.Work {
	return job.Work{
		Run: run, Lease: "lease-" + job.Secret(run), LeaseExpires: time.Now().Add(job.Lease), Deadline: time.Now().Add(time.Minute),
		Kind: job.KindNoop, Forge: "github", Repository: "2001", FullName: "acme/api", Commit: commit, Ref: "refs/heads/main",
		Clone: job.Credential{URL: "https://github.test/acme/api.git", Username: "x-access-token", Password: secret, Expires: time.Now().Add(time.Hour)},
	}
}

type world struct {
	remote   *remote
	checkout *checkout
	workDir  string
	loop     *job.Loop
	tasks    chan job.Task
}

func newWorld(t *testing.T, capacity int, handler job.Handler) *world {
	t.Helper()
	w := &world{remote: newRemote(), checkout: &checkout{}, workDir: t.TempDir(), tasks: make(chan job.Task, 16)}
	if handler == nil {
		handler = job.HandlerFunc(func(ctx context.Context, task job.Task) (job.Result, error) {
			w.tasks <- task
			fmt.Fprintf(task.Log, "handled %s\n", task.Commit)
			return job.Result{Outcome: job.OutcomeSucceeded, Summary: "nothing to do"}, nil
		})
	}
	w.loop = job.NewLoop(job.LoopOptions{
		Remote: w.remote, Checkout: w.checkout, Handlers: job.Handlers{job.KindNoop: handler},
		Capacity: capacity, WorkDir: w.workDir,
		After: func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) },
		Every: func(time.Duration) (<-chan time.Time, func()) {
			tk := time.NewTicker(2 * time.Millisecond)
			return tk.C, tk.Stop
		},
	})
	return w
}

// start runs the loop until stop, and returns a function that stops the
// claims and waits for the loop.
func (w *world) start(t *testing.T) (stopClaims func() error, abandon context.CancelFunc) {
	t.Helper()
	claims, cancelClaims := context.WithCancel(context.Background())
	runs, cancelRuns := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.loop.Run(claims, runs) }()
	t.Cleanup(func() { cancelClaims(); cancelRuns() })
	return func() error {
		cancelClaims()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("the loop did not stop")
			return nil
		}
	}, cancelRuns
}

func (w *world) waitReported(t *testing.T, run string) {
	t.Helper()
	select {
	case got := <-w.remote.reported:
		if got != run {
			t.Fatalf("reported %s, want %s", got, run)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s was not reported", run)
	}
}

func emptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the work directory holds %v", entries)
	}
}

func TestTheLoopRunsAJob(t *testing.T) {
	w := newWorld(t, 1, nil)
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	w.waitReported(t, "r1")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	res := w.remote.resultsOf("r1")
	if len(res) != 1 || res[0].Outcome != job.OutcomeSucceeded || res[0].Summary != "nothing to do" {
		t.Errorf("results %+v", res)
	}
	log := w.remote.log("r1")
	for _, step := range []string{"fetching " + commit, "fetched", "running noop", "handled " + commit, "succeeded: nothing to do"} {
		if !strings.Contains(log, step+"\n") {
			t.Errorf("the log lacks %q: %q", step, log)
		}
	}
	if strings.Contains(log, secret) {
		t.Error("the log holds the credential")
	}
	task := <-w.tasks
	if task.Source.Commit() != commit || task.FullName != "acme/api" || fmt.Sprintf("%+v", task) == "" || strings.Contains(fmt.Sprintf("%#v", task), secret) {
		t.Errorf("the task %+v", task)
	}
	if c := w.checkout.creds[0]; c.Password.Reveal() != secret || c.Username != "x-access-token" {
		t.Errorf("the checkout got %+v", c)
	}
	emptyDir(t, w.workDir)
}

func TestTheLoopRunsCapacityJobsAtATime(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	h := job.HandlerFunc(func(ctx context.Context, _ job.Task) (job.Result, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return job.Result{Outcome: job.OutcomeSucceeded, Summary: "ok"}, nil
	})
	w := newWorld(t, 2, h)
	for i := range 5 {
		w.remote.works <- work(fmt.Sprintf("r%d", i))
	}
	stop, _ := w.start(t)
	time.Sleep(50 * time.Millisecond)
	if got := running.Load(); got != 2 {
		t.Errorf("%d jobs run at once, want 2", got)
	}
	close(release)
	for range 5 {
		select {
		case <-w.remote.reported:
		case <-time.After(10 * time.Second):
			t.Fatal("a job was not reported")
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Errorf("peak %d, want 2", peak.Load())
	}
	emptyDir(t, w.workDir)
}

func TestAFetchThatFailsThreeTimesFailsTheJob(t *testing.T) {
	w := newWorld(t, 1, nil)
	w.checkout.fail = job.FetchTries
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	w.waitReported(t, "r1")
	_ = stop()
	res := w.remote.resultsOf("r1")
	if len(res) != 1 || res[0].Outcome != job.OutcomeFailed || res[0].Summary != "the commit could not be fetched" {
		t.Errorf("results %+v", res)
	}
	if w.checkout.tries != job.FetchTries || len(w.tasks) != 0 {
		t.Errorf("%d tries, %d handler runs", w.checkout.tries, len(w.tasks))
	}
	if dirs := w.checkout.dirs; dirs[0] == dirs[1] {
		t.Error("a try fetched into the directory of another")
	}
	emptyDir(t, w.workDir)

	// Failing twice, the third try succeeds.
	w2 := newWorld(t, 1, nil)
	w2.checkout.fail = job.FetchTries - 1
	w2.remote.works <- work("r2")
	stop2, _ := w2.start(t)
	w2.waitReported(t, "r2")
	_ = stop2()
	if res := w2.remote.resultsOf("r2"); len(res) != 1 || res[0].Outcome != job.OutcomeSucceeded {
		t.Errorf("results after two failed tries %+v", res)
	}
}

func TestARunStopsWhenTheServerSays(t *testing.T) {
	for name, answer := range map[string]func() (job.Renewal, error){
		"cancelled":            func() (job.Renewal, error) { return job.Renewal{Cancel: true}, nil },
		"no longer current":    func() (job.Renewal, error) { return job.Renewal{}, job.ErrStaleLease },
		"its lease has passed": func() (job.Renewal, error) { return job.Renewal{}, errors.New("connection refused") },
	} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			stopped := make(chan error, 1)
			h := job.HandlerFunc(func(ctx context.Context, _ job.Task) (job.Result, error) {
				close(started)
				<-ctx.Done()
				stopped <- ctx.Err()
				return job.Result{Outcome: job.OutcomeSucceeded, Summary: "late"}, nil
			})
			w := newWorld(t, 1, h)
			wk := work("r1")
			if name == "its lease has passed" {
				wk.LeaseExpires = time.Now()
			}
			w.remote.heartbeat = func(string) (job.Renewal, error) { return answer() }
			w.remote.works <- wk
			stop, _ := w.start(t)
			<-started
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Fatal("the handler was not stopped")
			}
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			if res := w.remote.resultsOf("r1"); len(res) != 0 {
				t.Errorf("a stopped run reported %+v", res)
			}
			emptyDir(t, w.workDir)
		})
	}
}

func TestARunPastItsDeadlineSendsNothing(t *testing.T) {
	h := job.HandlerFunc(func(ctx context.Context, _ job.Task) (job.Result, error) {
		<-ctx.Done()
		return job.Result{Outcome: job.OutcomeSucceeded}, nil
	})
	w := newWorld(t, 1, h)
	wk := work("r1")
	wk.Deadline = time.Now().Add(30 * time.Millisecond)
	w.remote.works <- wk
	stop, _ := w.start(t)
	time.Sleep(100 * time.Millisecond)
	_ = stop()
	if res := w.remote.resultsOf("r1"); len(res) != 0 {
		t.Errorf("a run past its deadline reported %+v", res)
	}
}

func TestARefusalStopsTheRunner(t *testing.T) {
	for _, refusal := range []error{job.ErrUnauthenticated, fmt.Errorf("%w: it speaks 2", job.ErrProtocol)} {
		w := newWorld(t, 1, nil)
		w.remote.claimErr = refusal
		claims, runs := context.Background(), context.Background()
		err := w.loop.Run(claims, runs)
		if !errors.Is(err, refusal) {
			t.Errorf("Run = %v, want %v", err, refusal)
		}
	}
	// During a run, from a heartbeat.
	w := newWorld(t, 1, job.HandlerFunc(func(ctx context.Context, _ job.Task) (job.Result, error) {
		<-ctx.Done()
		return job.Result{}, ctx.Err()
	}))
	w.remote.heartbeat = func(string) (job.Renewal, error) { return job.Renewal{}, job.ErrUnauthenticated }
	w.remote.works <- work("r1")
	done := make(chan error, 1)
	go func() { done <- w.loop.Run(context.Background(), context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a refused heartbeat did not stop the runner")
	}
}

func TestAResultIsSentAgainUntilAnswered(t *testing.T) {
	w := newWorld(t, 1, nil)
	w.remote.report = func(_ string, n int) error {
		if n < 3 {
			return errors.New("connection reset")
		}
		return nil
	}
	w.remote.appendLog = func(_ string, seq int) error {
		if seq == 0 {
			// The first answer is lost once; the server took the chunk.
			return nil
		}
		return nil
	}
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	w.waitReported(t, "r1")
	_ = stop()
	res := w.remote.resultsOf("r1")
	if len(res) != 3 || !res[0].Same(res[1]) || !res[1].Same(res[2]) {
		t.Errorf("results sent %+v, want the same three times", res)
	}
	if len(w.tasks) != 1 {
		t.Errorf("the handler ran %d times", len(w.tasks))
	}
}

func TestAHandlerThatPanicsFailsTheJob(t *testing.T) {
	w := newWorld(t, 1, job.HandlerFunc(func(context.Context, job.Task) (job.Result, error) { panic("boom") }))
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	w.waitReported(t, "r1")
	_ = stop()
	if res := w.remote.resultsOf("r1"); len(res) != 1 || res[0].Outcome != job.OutcomeFailed || res[0].Summary != "the handler failed" {
		t.Errorf("results %+v", res)
	}
	emptyDir(t, w.workDir)
}

func TestShutdownFinishesThenAbandons(t *testing.T) {
	var mu sync.Mutex
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	h := job.HandlerFunc(func(ctx context.Context, _ job.Task) (job.Result, error) {
		mu.Lock()
		wait := release
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-wait:
			return job.Result{Outcome: job.OutcomeSucceeded, Summary: "done"}, nil
		case <-ctx.Done():
			return job.Result{}, ctx.Err()
		}
	})

	// The first signal: no more claims, the current job finishes.
	w := newWorld(t, 1, h)
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	time.Sleep(20 * time.Millisecond)
	w.remote.works <- work("r2")
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if res := w.remote.resultsOf("r1"); len(res) != 1 || res[0].Outcome != job.OutcomeSucceeded {
		t.Errorf("the current job: %+v", res)
	}
	if len(w.remote.works) != 1 {
		t.Error("a job was claimed after the stop")
	}

	// The second signal: the current job is abandoned, without a result.
	mu.Lock()
	release = make(chan struct{})
	mu.Unlock()
	w2 := newWorld(t, 1, h)
	w2.remote.works <- work("r3")
	stop2, abandon := w2.start(t)
	<-started
	abandon()
	if err := stop2(); err != nil {
		t.Fatal(err)
	}
	if res := w2.remote.resultsOf("r3"); len(res) != 0 {
		t.Errorf("an abandoned job reported %+v", res)
	}
	emptyDir(t, w2.workDir)
}

func TestCleanWorkDirRemovesTheRunsLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	if n, err := job.CleanWorkDir(dir); err != nil || n != 0 {
		t.Fatalf("CleanWorkDir of a missing directory = %d, %v", n, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"run-1", "run-2/checkout-1"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "run-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "operator.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := job.CleanWorkDir(dir); err != nil || n != 3 {
		t.Fatalf("CleanWorkDir = %d, %v", n, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "operator.txt" {
		t.Errorf("left %v", entries)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Error("a link was followed")
	}
}

// TestALogThatCannotBeSentHoldsUpNeitherTheLeaseNorTheResult: the log
// route failing for good, the heartbeats go on and the result is sent.
func TestALogThatCannotBeSentHoldsUpNeitherTheLeaseNorTheResult(t *testing.T) {
	release := make(chan struct{})
	h := job.HandlerFunc(func(ctx context.Context, task job.Task) (job.Result, error) {
		fmt.Fprintln(task.Log, "working")
		<-release
		return job.Result{Outcome: job.OutcomeSucceeded, Summary: "done"}, nil
	})
	w := newWorld(t, 1, h)
	var beats atomic.Int32
	w.remote.heartbeat = func(string) (job.Renewal, error) {
		beats.Add(1)
		return job.Renewal{Expires: time.Now().Add(job.Lease)}, nil
	}
	w.remote.appendLog = func(string, int) error { return errors.New("the log route is down") }
	w.remote.works <- work("r1")
	stop, _ := w.start(t)
	time.Sleep(30 * time.Millisecond)
	before := beats.Load()
	time.Sleep(30 * time.Millisecond)
	if beats.Load() <= before {
		t.Error("the heartbeats stopped while the log could not be sent")
	}
	close(release)
	w.waitReported(t, "r1")
	_ = stop()
	if res := w.remote.resultsOf("r1"); len(res) != 1 || res[0].Outcome != job.OutcomeSucceeded {
		t.Errorf("results %+v", res)
	}
}

// TestAResultItsKindRefusesIsNotSent: the runner checks the result of a
// handler, and fails the job instead of sending one the server would
// refuse, or one it cannot send.
func TestAResultItsKindRefusesIsNotSent(t *testing.T) {
	for name, res := range map[string]job.Result{
		"data noop refuses": {Outcome: job.OutcomeSucceeded, Summary: "x", Data: []byte(`{"found":1}`)},
		"data not JSON":     {Outcome: job.OutcomeSucceeded, Summary: "x", Data: []byte(`{`)},
		"no outcome":        {Summary: "x"},
		"two lines":         {Outcome: job.OutcomeSucceeded, Summary: "a\nb"},
	} {
		w := newWorld(t, 1, job.HandlerFunc(func(context.Context, job.Task) (job.Result, error) { return res, nil }))
		w.remote.works <- work("r1")
		stop, _ := w.start(t)
		w.waitReported(t, "r1")
		_ = stop()
		got := w.remote.resultsOf("r1")
		if len(got) != 1 || got[0].Outcome != job.OutcomeFailed || got[0].Summary != "the handler gave a result its kind refuses" || got[0].Data != nil {
			t.Errorf("%s: sent %+v", name, got)
		}
	}
}

// TestAKindWithoutHandlerFailsAndSaysSo: the summary names the kind.
func TestAKindWithoutHandlerFailsAndSaysSo(t *testing.T) {
	w := newWorld(t, 1, nil)
	wk := work("r1")
	wk.Kind = "check"
	w.remote.works <- wk
	stop, _ := w.start(t)
	w.waitReported(t, "r1")
	_ = stop()
	if res := w.remote.resultsOf("r1"); len(res) != 1 || res[0].Outcome != job.OutcomeFailed || res[0].Summary != "this runner has no handler for kind check" {
		t.Errorf("results %+v", res)
	}
}
