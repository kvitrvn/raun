package job_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
)

// clones is a Clone in memory: it mints numbered credentials and records
// what it revoked.
type clones struct {
	mu      sync.Mutex
	n       int
	live    map[string]bool
	fail    error
	failRev error
	// minting, when set, runs during each mint.
	minting func()
}

func (c *clones) MintClone(_ context.Context, repo string) (job.Credential, error) {
	c.mu.Lock()
	if c.fail != nil {
		defer c.mu.Unlock()
		return job.Credential{}, c.fail
	}
	c.n++
	secret := fmt.Sprintf("clone-secret-%d", c.n)
	if c.live == nil {
		c.live = map[string]bool{}
	}
	c.live[secret] = true
	minting := c.minting
	c.mu.Unlock()
	if minting != nil {
		minting()
	}
	return job.Credential{URL: "https://forge.test/acme/" + repo + ".git", FullName: "acme/" + repo, Username: "x",
		Password: job.Secret(secret), Expires: time.Now().Add(time.Hour)}, nil
}

func (c *clones) RevokeClone(_ context.Context, cred job.Credential) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failRev != nil {
		return c.failRev
	}
	c.live[cred.Password.Reveal()] = false
	return nil
}

func (c *clones) isLive(cred job.Credential) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live[cred.Password.Reveal()]
}

type dispatchWorld struct {
	store  *jobtest.Memory
	clones *clones
	d      *job.Dispatcher
	runner job.Runner
	other  job.Runner
	logs   *bytes.Buffer
}

func newDispatchWorld(t *testing.T, wait time.Duration) *dispatchWorld {
	t.Helper()
	n := &job.Notifier{}
	m := &jobtest.Memory{Queued: n.Notify}
	m.AddWorkspace(acme)
	w := &dispatchWorld{store: m, clones: &clones{}, logs: &bytes.Buffer{}}
	var err error
	ctx := context.Background()
	w.runner, _, _, err = job.NewRunner(rand.Reader, job.RunnerRequest{Name: "builder", Capabilities: []job.Capability{job.CapabilityNoop}, CreatedBy: admin}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CreateRunner(ctx, w.runner, job.HashToken("t1")); err != nil {
		t.Fatal(err)
	}
	w.other, _, _, _ = job.NewRunner(rand.Reader, job.RunnerRequest{Name: "other", Capabilities: []job.Capability{job.CapabilityNoop}, CreatedBy: admin}, time.Now())
	if err := m.CreateRunner(ctx, w.other, job.HashToken("t2")); err != nil {
		t.Fatal(err)
	}
	w.d = job.NewDispatcher(job.DispatcherOptions{
		Runners: m, Jobs: m, Clone: w.clones, Forge: "github", Notifier: n, Wait: wait,
		Logger: slog.New(slog.NewTextHandler(w.logs, nil)),
	})
	return w
}

func (w *dispatchWorld) enqueue(t *testing.T) job.Job {
	t.Helper()
	j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: acme.ID, Forge: "github", Repository: "2001",
		Commit: strings.Repeat("c", 40)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Enqueue(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestDispatcherClaim(t *testing.T) {
	ctx := context.Background()

	t.Run("a queued job is given at once with its credential", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		j := w.enqueue(t)
		a, err := w.d.Claim(ctx, w.runner.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.Job.ID != j.ID || a.Token == "" || a.Clone.FullName != "acme/2001" || !w.clones.isLive(a.Clone) || w.d.Held() != 1 {
			t.Errorf("Claim = %+v", a)
		}
	})

	t.Run("an empty queue answers no job after the wait", func(t *testing.T) {
		w := newDispatchWorld(t, 20*time.Millisecond)
		start := time.Now()
		if _, err := w.d.Claim(ctx, w.runner.ID, nil); !errors.Is(err, job.ErrNoJob) {
			t.Fatalf("Claim = %v, want ErrNoJob", err)
		}
		if time.Since(start) < 20*time.Millisecond {
			t.Error("the claim did not wait")
		}
	})

	t.Run("an enqueue wakes a waiting claim", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		got := make(chan job.Assignment, 1)
		go func() {
			a, err := w.d.Claim(ctx, w.runner.ID, nil)
			if err != nil {
				t.Errorf("Claim = %v", err)
			}
			got <- a
		}()
		time.Sleep(20 * time.Millisecond)
		j := w.enqueue(t)
		select {
		case a := <-got:
			if a.Job.ID != j.ID {
				t.Errorf("woken with %s, want %s", a.Job.ID, j.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the enqueue woke nobody")
		}
	})

	t.Run("a revocation that sends a run back wakes a waiting claim", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		j := w.enqueue(t)
		if _, err := w.d.Claim(ctx, w.other.ID, nil); err != nil {
			t.Fatal(err)
		}
		got := make(chan job.Assignment, 1)
		go func() {
			a, _ := w.d.Claim(ctx, w.runner.ID, nil)
			got <- a
		}()
		time.Sleep(20 * time.Millisecond)
		if _, err := w.store.RevokeRunner(ctx, w.other.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		select {
		case a := <-got:
			if a.Job.ID != j.ID {
				t.Errorf("woken with %q, want %s", a.Job.ID, j.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the revocation woke nobody")
		}
	})

	t.Run("a stop or a done context ends the wait", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		stop := make(chan struct{})
		close(stop)
		if _, err := w.d.Claim(ctx, w.runner.ID, stop); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("Claim after a stop = %v", err)
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		if _, err := w.d.Claim(cctx, w.runner.ID, nil); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("Claim of a done context = %v", err)
		}
	})

	t.Run("a revoked runner is refused", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		if _, err := w.store.RevokeRunner(ctx, w.runner.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := w.d.Claim(ctx, w.runner.ID, nil); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("Claim = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("a credential that cannot be minted gives nothing and delays the job", func(t *testing.T) {
		w := newDispatchWorld(t, 30*time.Millisecond)
		w.clones.fail = errors.New("forge down")
		j := w.enqueue(t)
		before := time.Now()
		if _, err := w.d.Claim(ctx, w.runner.ID, nil); !errors.Is(err, job.ErrNoJob) {
			t.Fatalf("Claim = %v, want ErrNoJob", err)
		}
		got, _ := w.store.Job(ctx, j.ID)
		if got.State != job.StateQueued || got.NotBefore.Before(before.Add(job.MintDelay)) {
			t.Errorf("the job after a failed mint: %+v", got)
		}
		if runs, _ := w.store.Runs(ctx, j.ID); len(runs) != 0 {
			t.Errorf("the run of a failed mint was kept: %+v", runs)
		}
		if w.d.Held() != 0 {
			t.Error("a credential is held")
		}
		if !strings.Contains(w.logs.String(), "forge down") {
			t.Errorf("the failure was not logged: %s", w.logs)
		}
	})

	t.Run("a forge without the capability gives nothing", func(t *testing.T) {
		n := &job.Notifier{}
		m := &jobtest.Memory{Queued: n.Notify}
		m.AddWorkspace(acme)
		r, _, _, _ := job.NewRunner(rand.Reader, job.RunnerRequest{Name: "builder", Capabilities: []job.Capability{job.CapabilityNoop}, CreatedBy: admin}, time.Now())
		if err := m.CreateRunner(ctx, r, job.HashToken("t1")); err != nil {
			t.Fatal(err)
		}
		d := job.NewDispatcher(job.DispatcherOptions{Runners: m, Jobs: m, Forge: "github", Notifier: n, Wait: 10 * time.Millisecond})
		j, _ := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: acme.ID, Forge: "github", Repository: "2001", Commit: strings.Repeat("c", 40)}, time.Now())
		if err := m.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Claim(ctx, r.ID, nil); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("Claim = %v, want ErrNoJob", err)
		}
	})

	t.Run("a job cancelled during the mint is not given", func(t *testing.T) {
		w := newDispatchWorld(t, 20*time.Millisecond)
		j := w.enqueue(t)
		w.clones.minting = func() {
			if _, err := w.store.Cancel(ctx, j.ID, job.ReasonCancelled, time.Now()); err != nil {
				t.Error(err)
			}
		}
		if _, err := w.d.Claim(ctx, w.runner.ID, nil); !errors.Is(err, job.ErrNoJob) {
			t.Fatalf("Claim = %v, want ErrNoJob", err)
		}
		if w.d.Held() != 0 {
			t.Error("the credential of a cancelled run is held")
		}
		w.clones.mu.Lock()
		defer w.clones.mu.Unlock()
		if w.clones.live["clone-secret-1"] {
			t.Error("the credential of a cancelled run was not revoked")
		}
	})
}

func TestDispatcherRun(t *testing.T) {
	ctx := context.Background()
	claimed := func(t *testing.T) (*dispatchWorld, job.Assignment) {
		t.Helper()
		w := newDispatchWorld(t, time.Minute)
		w.enqueue(t)
		a, err := w.d.Claim(ctx, w.runner.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w, a
	}

	t.Run("a run is refused to another runner", func(t *testing.T) {
		w, a := claimed(t)
		if _, err := w.d.Heartbeat(ctx, w.other.ID, a.Run.ID, a.Token); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat of another runner = %v", err)
		}
		if err := w.d.AppendLog(ctx, w.other.ID, a.Run.ID, a.Token, 0, "x"); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("AppendLog of another runner = %v", err)
		}
		if _, err := w.d.Kind(ctx, w.other.ID, a.Run.ID); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Kind of another runner = %v", err)
		}
		if err := w.d.Report(ctx, w.other.ID, a.Run.ID, a.Token, job.Result{Outcome: job.OutcomeSucceeded}); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Report of another runner = %v", err)
		}
		if _, err := w.d.Heartbeat(ctx, w.runner.ID, strings.Repeat("f", 32), a.Token); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat of an unknown run = %v", err)
		}
		// The runner of the run with a wrong lease token: refused, and the
		// run, which still holds its lease, keeps its credential.
		if _, err := w.d.Heartbeat(ctx, w.runner.ID, a.Run.ID, "another token"); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat with another lease token = %v", err)
		}
		if err := w.d.AppendLog(ctx, w.runner.ID, a.Run.ID, "another token", 0, "x"); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("AppendLog with another lease token = %v", err)
		}
		if !w.clones.isLive(a.Clone) || w.d.Held() != 1 {
			t.Error("a refused request revoked the credential")
		}
	})

	t.Run("a request past the lease revokes", func(t *testing.T) {
		w := newDispatchWorld(t, time.Minute)
		w.enqueue(t)
		now := time.Now()
		w.d = job.NewDispatcher(job.DispatcherOptions{Runners: w.store, Jobs: w.store, Clone: w.clones, Forge: "github",
			Notifier: &job.Notifier{}, Wait: 10 * time.Millisecond, Now: func() time.Time { return now }})
		a, err := w.d.Claim(ctx, w.runner.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(job.Lease)
		if _, err := w.d.Heartbeat(ctx, w.runner.ID, a.Run.ID, a.Token); !errors.Is(err, job.ErrStaleLease) {
			t.Fatalf("Heartbeat past the lease = %v", err)
		}
		if w.clones.isLive(a.Clone) || w.d.Held() != 0 {
			t.Error("the credential outlived the lease")
		}
	})

	t.Run("a heartbeat renews the lease, a log is appended, a result revokes", func(t *testing.T) {
		w, a := claimed(t)
		expires, err := w.d.Heartbeat(ctx, w.runner.ID, a.Run.ID, a.Token)
		if err != nil || !expires.After(a.Run.LeaseExpires.Add(-time.Second)) {
			t.Errorf("Heartbeat = %s, %v", expires, err)
		}
		if err := w.d.AppendLog(ctx, w.runner.ID, a.Run.ID, a.Token, 0, "fetch\n"); err != nil {
			t.Error(err)
		}
		if k, err := w.d.Kind(ctx, w.runner.ID, a.Run.ID); err != nil || k != job.KindNoop {
			t.Errorf("Kind = %q, %v", k, err)
		}
		res := job.Result{Outcome: job.OutcomeSucceeded, Summary: "done"}
		if err := w.d.Report(ctx, w.runner.ID, a.Run.ID, a.Token, res); err != nil {
			t.Fatal(err)
		}
		if w.clones.isLive(a.Clone) || w.d.Held() != 0 {
			t.Error("the credential outlived the result")
		}
		if err := w.d.Report(ctx, w.runner.ID, a.Run.ID, a.Token, res); err != nil {
			t.Errorf("the same result again = %v", err)
		}
	})

	t.Run("an invalid result fails the job and revokes", func(t *testing.T) {
		w, a := claimed(t)
		err := w.d.Report(ctx, w.runner.ID, a.Run.ID, a.Token, job.Result{Outcome: job.OutcomeSucceeded, Data: []byte(`{"x":1}`)})
		if !errors.Is(err, job.ErrInvalidResult) {
			t.Fatalf("Report = %v", err)
		}
		if w.clones.isLive(a.Clone) {
			t.Error("the credential outlived an invalid result")
		}
		if got, _ := w.store.Job(ctx, a.Job.ID); got.State != job.StateFailed || got.Reason != job.ReasonInvalidResult {
			t.Errorf("the job: %+v", got)
		}
	})

	t.Run("a cancellation is told by the heartbeat and revokes", func(t *testing.T) {
		w, a := claimed(t)
		if _, err := w.store.Cancel(ctx, a.Job.ID, job.ReasonCancelled, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := w.d.Heartbeat(ctx, w.runner.ID, a.Run.ID, a.Token); !errors.Is(err, job.ErrCancelled) {
			t.Errorf("Heartbeat = %v, want ErrCancelled", err)
		}
		if w.clones.isLive(a.Clone) {
			t.Error("the credential outlived the cancellation")
		}
	})

	t.Run("reap revokes the credentials of runs ended elsewhere", func(t *testing.T) {
		w, a := claimed(t)
		if n := w.d.Reap(ctx); n != 0 || !w.clones.isLive(a.Clone) {
			t.Fatalf("Reap of a running run = %d", n)
		}
		if _, err := w.store.RevokeRunner(ctx, w.runner.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if n := w.d.Reap(ctx); n != 1 || w.clones.isLive(a.Clone) || w.d.Held() != 0 {
			t.Errorf("Reap after the revocation of the runner = %d, live %v", n, w.clones.isLive(a.Clone))
		}
	})

	t.Run("a revocation that fails is tried again", func(t *testing.T) {
		w, a := claimed(t)
		w.clones.failRev = errors.New("forge down")
		if _, err := w.store.Cancel(ctx, a.Job.ID, job.ReasonCancelled, time.Now()); err != nil {
			t.Fatal(err)
		}
		w.d.Reap(ctx)
		if w.d.Held() != 1 {
			t.Fatal("a credential whose revocation failed was let go")
		}
		w.clones.mu.Lock()
		w.clones.failRev = nil
		w.clones.mu.Unlock()
		if n := w.d.Reap(ctx); n != 1 || w.clones.isLive(a.Clone) || w.d.Held() != 0 {
			t.Errorf("Reap again = %d", n)
		}
		if strings.Contains(w.logs.String(), a.Clone.Password.Reveal()) {
			t.Error("the log holds the credential")
		}
	})
}

func TestSecretIsRedacted(t *testing.T) {
	s := job.Secret("clone-secret")
	c := job.Credential{URL: "https://forge.test/a.git", Password: s}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "secret", s, "credential", c)
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x", s, c, c, s, s, s), buf.String()} {
		if strings.Contains(out, "clone-secret") {
			t.Errorf("the secret shows: %s", out)
		}
	}
	if s.Reveal() != "clone-secret" {
		t.Error("Reveal")
	}
}

func TestNotifierWakesEveryWaiter(t *testing.T) {
	var n job.Notifier
	a, b := n.Wait(), n.Wait()
	n.Notify()
	for _, ch := range []<-chan struct{}{a, b} {
		select {
		case <-ch:
		default:
			t.Error("a waiter was not woken")
		}
	}
	c := n.Wait()
	select {
	case <-c:
		t.Error("a wait taken after the wake-up was woken")
	default:
	}
	n.Notify()
	<-c
}
