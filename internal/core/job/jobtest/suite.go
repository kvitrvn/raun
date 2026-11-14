package jobtest

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Fixture is a job store under test, empty, and a way to create in the data
// it reads the repository of forge with this identifier in the workspace
// with this identifier, which a job names.
type Fixture struct {
	Store         job.Store
	AddRepository func(t *testing.T, workspace, forge, repository string)
}

// Identifiers of the suite: 32 hexadecimal digits, as workspaces have.
func id32(n string) string { return strings.Repeat("0", 32-len(n)) + n }

var (
	acme = id32("w1")
	beta = id32("w2")
	t0   = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	// commit is the commit the jobs of the suite work on.
	commit = strings.Repeat("c", 40)
	// instance takes noop jobs of every workspace.
	instance = job.Claimant{Runner: "r1", Capabilities: []job.Capability{job.CapabilityNoop}}
)

// at is t0 plus d.
func at(d time.Duration) time.Time { return t0.Add(d) }

// Suite runs the contract of job.Store on the stores newFixture gives, each
// with the repositories github/2001 and github/2002 in the workspace acme,
// and github/3001 in beta.
func Suite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()
	setup := func(t *testing.T) job.Store {
		t.Helper()
		f := newFixture(t)
		f.AddRepository(t, acme, "github", "2001")
		f.AddRepository(t, acme, "github", "2002")
		f.AddRepository(t, beta, "github", "3001")
		return f.Store
	}

	t.Run("enqueue then read", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		got, err := s.Job(ctx, j.ID)
		if err != nil || got != j {
			t.Fatalf("Job = %+v, %v, want %+v", got, err, j)
		}
		if _, err := s.Job(ctx, id32("ff")); !errors.Is(err, job.ErrNotFound) {
			t.Errorf("Job of an unknown identifier = %v, want ErrNotFound", err)
		}
		if runs, err := s.Runs(ctx, j.ID); err != nil || len(runs) != 0 {
			t.Errorf("Runs of a queued job = %v, %v", runs, err)
		}
		if err := s.Enqueue(ctx, j); err == nil {
			t.Error("a job enqueued twice was accepted")
		}
	})

	t.Run("claim gives the oldest job the runner may take", func(t *testing.T) {
		s := setup(t)
		older := enqueue(t, s, beta, "3001", "", at(0))
		newer := enqueue(t, s, acme, "2001", "", at(time.Second))
		ws := job.Claimant{Runner: "r2", Workspace: acme, Capabilities: []job.Capability{job.CapabilityNoop}}
		c := claim(t, s, ws, at(2*time.Second))
		if c.Job.ID != newer.ID {
			t.Fatalf("a runner of acme claimed %s, want the job of acme %s", c.Job.ID, newer.ID)
		}
		if c.Job.State != job.StateRunning || c.Run.Job != newer.ID || c.Run.Runner != "r2" || c.Token == "" {
			t.Errorf("claimed %+v", c)
		}
		if c.Run.LeaseExpires != at(2*time.Second+job.Lease) || c.Run.Deadline != at(2*time.Second+5*time.Minute) || c.Run.StartedAt != at(2*time.Second) {
			t.Errorf("run %+v: lease or deadline", c.Run)
		}
		if _, err := s.Claim(ctx, ws, at(3*time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a runner of acme claimed a job of beta: %v", err)
		}
		none := job.Claimant{Runner: "r3"}
		if _, err := s.Claim(ctx, none, at(3*time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a runner without the capability claimed: %v", err)
		}
		if c := claim(t, s, instance, at(3*time.Second)); c.Job.ID != older.ID {
			t.Errorf("the runner of the instance claimed %s, want %s", c.Job.ID, older.ID)
		}
		if _, err := s.Claim(ctx, instance, at(4*time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a claim of an empty queue = %v, want ErrNoJob", err)
		}
		if got := mustJob(t, s, newer.ID); got.State != job.StateRunning {
			t.Errorf("the claimed job is %s", got.State)
		}
	})

	t.Run("concurrent claims never give a job twice", func(t *testing.T) {
		s := setup(t)
		const jobs, runners = 12, 8
		for i := range jobs {
			enqueue(t, s, acme, "2001", "", at(time.Duration(i)*time.Millisecond))
		}
		var mu sync.Mutex
		seen := map[string]int{}
		var wg sync.WaitGroup
		for range runners {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					c, err := s.Claim(ctx, instance, at(time.Second))
					if errors.Is(err, job.ErrNoJob) {
						return
					}
					if err != nil {
						t.Errorf("Claim: %v", err)
						return
					}
					mu.Lock()
					seen[c.Job.ID]++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(seen) != jobs {
			t.Errorf("%d jobs claimed, want %d", len(seen), jobs)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("job %s claimed %d times", id, n)
			}
		}
	})

	t.Run("a heartbeat renews the lease of the current run only", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		r, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(50*time.Second))
		if err != nil || r.LeaseExpires != at(50*time.Second+job.Lease) {
			t.Fatalf("Heartbeat = %+v, %v", r, err)
		}
		for name, tc := range map[string]struct {
			run, token string
			now        time.Time
		}{
			"another token":   {c.Run.ID, strings.Repeat("0", 64), at(51 * time.Second)},
			"an unknown run":  {id32("ff"), c.Token, at(51 * time.Second)},
			"a lease expired": {c.Run.ID, c.Token, at(50*time.Second + job.Lease)},
		} {
			if _, err := s.Heartbeat(ctx, tc.run, tc.token, tc.now); !errors.Is(err, job.ErrStaleLease) {
				t.Errorf("Heartbeat with %s = %v, want ErrStaleLease", name, err)
			}
		}
	})

	t.Run("a lost run is retried twice, then the job fails", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		now := at(0)
		var tokens []job.Claimed
		for i := range job.MaxRuns {
			c := claim(t, s, instance, now)
			if c.Job.ID != j.ID {
				t.Fatalf("run %d claimed %s", i, c.Job.ID)
			}
			tokens = append(tokens, c)
			now = now.Add(job.Lease)
			if i == 0 {
				// The sweeper ends the first one; a claim ends the next
				// ones itself.
				if n, err := s.Sweep(ctx, now); err != nil || n != 1 {
					t.Fatalf("Sweep = %d, %v", n, err)
				}
				if got := mustJob(t, s, j.ID); got.State != job.StateQueued {
					t.Fatalf("after a lost run the job is %s", got.State)
				}
			}
		}
		if _, err := s.Claim(ctx, instance, now); !errors.Is(err, job.ErrNoJob) {
			t.Fatalf("a job lost %d times was claimed again: %v", job.MaxRuns, err)
		}
		got := mustJob(t, s, j.ID)
		if got.State != job.StateFailed || got.Reason != job.ReasonLost || got.FinishedAt != now {
			t.Errorf("after %d lost runs: %+v", job.MaxRuns, got)
		}
		runs := mustRuns(t, s, j.ID)
		if len(runs) != job.MaxRuns {
			t.Fatalf("%d runs", len(runs))
		}
		for i, r := range runs {
			if r.Outcome != job.OutcomeLost || r.ID != tokens[i].Run.ID {
				t.Errorf("run %d: %+v", i, r)
			}
		}
		// A lost run is refused, even with its own token.
		first := tokens[0]
		if _, err := s.Heartbeat(ctx, first.Run.ID, first.Token, now); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat of a lost run = %v", err)
		}
		if err := s.Report(ctx, first.Run.ID, first.Token, ok(), now); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Report of a lost run = %v", err)
		}
	})

	t.Run("a run past its deadline fails its job without retry", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		for now := at(job.HeartbeatEvery); now.Before(c.Run.Deadline); now = now.Add(job.HeartbeatEvery) {
			if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, now); err != nil {
				t.Fatalf("Heartbeat at %s: %v", now, err)
			}
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, c.Run.Deadline); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat at the deadline = %v", err)
		}
		// The lease has expired too by then: the deadline wins.
		if _, err := s.Sweep(ctx, c.Run.Deadline.Add(job.Lease)); err != nil {
			t.Fatal(err)
		}
		got := mustJob(t, s, j.ID)
		if got.State != job.StateFailed || got.Reason != job.ReasonTimeout {
			t.Errorf("job past its deadline: %+v", got)
		}
		if runs := mustRuns(t, s, j.ID); len(runs) != 1 || runs[0].Outcome != job.OutcomeFailed {
			t.Errorf("runs: %+v", runs)
		}
	})

	t.Run("a job waits in the queue a day at most", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		// Lost once, sent back: it keeps its time of creation.
		claim(t, s, instance, at(0))
		if _, err := s.Sweep(ctx, at(job.Lease)); err != nil {
			t.Fatal(err)
		}
		if n, err := s.Sweep(ctx, at(job.QueueExpiry-time.Nanosecond)); err != nil || n != 0 {
			t.Fatalf("Sweep before the expiry = %d, %v", n, err)
		}
		if _, err := s.Sweep(ctx, at(job.QueueExpiry)); err != nil {
			t.Fatal(err)
		}
		got := mustJob(t, s, j.ID)
		if got.State != job.StateFailed || got.Reason != job.ReasonExpired || got.FinishedAt != at(job.QueueExpiry) {
			t.Errorf("a job a day old: %+v", got)
		}
	})

	t.Run("one result is accepted per job", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		res := ok()
		if err := s.Report(ctx, c.Run.ID, c.Token, res, at(time.Second)); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if err := s.Report(ctx, c.Run.ID, c.Token, res, at(2*time.Second)); err != nil {
			t.Errorf("the same result sent again = %v", err)
		}
		other := job.Result{Outcome: job.OutcomeFailed, Summary: "no"}
		if err := s.Report(ctx, c.Run.ID, c.Token, other, at(2*time.Second)); !errors.Is(err, job.ErrReported) {
			t.Errorf("another result = %v, want ErrReported", err)
		}
		if err := s.Report(ctx, c.Run.ID, strings.Repeat("0", 64), res, at(2*time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("a result with another token = %v", err)
		}
		got := mustJob(t, s, j.ID)
		if got.State != job.StateSucceeded || got.Reason != job.ReasonNone || got.FinishedAt != at(time.Second) {
			t.Errorf("job: %+v", got)
		}
		runs := mustRuns(t, s, j.ID)
		if len(runs) != 1 || runs[0].Outcome != job.OutcomeSucceeded || runs[0].Result == nil || !runs[0].Result.Same(res) || runs[0].EndedAt != at(time.Second) {
			t.Errorf("runs: %+v", runs)
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(3*time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat after the result = %v", err)
		}
		if _, err := s.Cancel(ctx, j.ID, job.ReasonCancelled, at(3*time.Second)); !errors.Is(err, job.ErrFinal) {
			t.Errorf("Cancel of a finished job = %v, want ErrFinal", err)
		}
	})

	t.Run("an empty object is an empty object however it is written", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		if err := s.Report(ctx, c.Run.ID, c.Token, job.Result{Outcome: job.OutcomeSucceeded, Data: []byte("{\n }")}, at(time.Second)); err != nil {
			t.Fatalf("Report of a spaced empty object = %v", err)
		}
		if got := mustJob(t, s, j.ID); got.State != job.StateSucceeded {
			t.Errorf("job: %+v", got)
		}
	})

	t.Run("a reported failure is final", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		if err := s.Report(ctx, c.Run.ID, c.Token, job.Result{Outcome: job.OutcomeFailed, Summary: "the commit could not be fetched"}, at(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := mustJob(t, s, j.ID); got.State != job.StateFailed || got.Reason != job.ReasonNone {
			t.Errorf("job: %+v", got)
		}
		if _, err := s.Claim(ctx, instance, at(2*time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a failed job was claimed again: %v", err)
		}
	})

	t.Run("a refused result fails the job and keeps nothing", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		bad := job.Result{Outcome: job.OutcomeSucceeded, Data: []byte(`{"x":1}`)}
		if err := s.Report(ctx, c.Run.ID, c.Token, bad, at(time.Second)); !errors.Is(err, job.ErrInvalidResult) {
			t.Fatalf("Report of a refused result = %v", err)
		}
		if got := mustJob(t, s, j.ID); got.State != job.StateFailed || got.Reason != job.ReasonInvalidResult {
			t.Errorf("job: %+v", got)
		}
		if runs := mustRuns(t, s, j.ID); len(runs) != 1 || runs[0].Outcome != job.OutcomeFailed || runs[0].Result != nil {
			t.Errorf("runs: %+v", runs)
		}
		if err := s.Report(ctx, c.Run.ID, c.Token, ok(), at(2*time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("a result after a refused one = %v", err)
		}
	})

	t.Run("a result after the lease is refused", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		if err := s.Report(ctx, c.Run.ID, c.Token, ok(), at(job.Lease)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Report at the end of the lease = %v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		s := setup(t)
		queued := enqueue(t, s, acme, "2001", "", at(0))
		running := enqueue(t, s, acme, "2001", "", at(time.Second))
		got, err := s.Cancel(ctx, queued.ID, job.ReasonCancelled, at(2*time.Second))
		if err != nil || got.State != job.StateCancelled || got.Reason != job.ReasonCancelled || got.FinishedAt != at(2*time.Second) {
			t.Fatalf("Cancel of a queued job = %+v, %v", got, err)
		}
		c := claim(t, s, instance, at(2*time.Second))
		if c.Job.ID != running.ID {
			t.Fatalf("claimed the cancelled job %s", c.Job.ID)
		}
		if _, err := s.Cancel(ctx, running.ID, job.ReasonCancelled, at(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(4*time.Second)); !errors.Is(err, job.ErrCancelled) {
			t.Errorf("Heartbeat of a cancelled job = %v, want ErrCancelled", err)
		}
		if err := s.Report(ctx, c.Run.ID, c.Token, ok(), at(4*time.Second)); !errors.Is(err, job.ErrCancelled) {
			t.Errorf("Report of a cancelled job = %v, want ErrCancelled", err)
		}
		if runs := mustRuns(t, s, running.ID); len(runs) != 1 || runs[0].Outcome != job.OutcomeCancelled || runs[0].EndedAt != at(3*time.Second) {
			t.Errorf("runs: %+v", runs)
		}
		if got, err := s.Cancel(ctx, running.ID, job.ReasonCancelled, at(5*time.Second)); !errors.Is(err, job.ErrFinal) || got.FinishedAt != at(3*time.Second) {
			t.Errorf("Cancel of a cancelled job = %+v, %v", got, err)
		}
		if _, err := s.Cancel(ctx, id32("ff"), job.ReasonCancelled, at(5*time.Second)); !errors.Is(err, job.ErrNotFound) {
			t.Errorf("Cancel of an unknown job = %v", err)
		}
	})

	t.Run("cancel the jobs of a repository", func(t *testing.T) {
		s := setup(t)
		queued := enqueue(t, s, acme, "2001", "", at(0))
		running := enqueue(t, s, acme, "2001", "", at(time.Second))
		done := enqueue(t, s, acme, "2001", "", at(2*time.Second))
		other := enqueue(t, s, acme, "2002", "", at(3*time.Second))
		if _, err := s.Cancel(ctx, queued.ID, job.ReasonCancelled, at(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		queued = enqueue(t, s, acme, "2001", "", at(4*time.Second))
		c := claim(t, s, instance, at(5*time.Second))
		if c.Job.ID != running.ID {
			t.Fatalf("claimed %s", c.Job.ID)
		}
		d := claim(t, s, instance, at(5*time.Second))
		if d.Job.ID != done.ID {
			t.Fatalf("claimed %s", d.Job.ID)
		}
		if err := s.Report(ctx, d.Run.ID, d.Token, ok(), at(6*time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err := s.CancelRepository(ctx, "github", "2001", job.ReasonDeactivated, at(7*time.Second))
		if err != nil || n != 2 {
			t.Fatalf("CancelRepository = %d, %v, want 2", n, err)
		}
		for _, id := range []string{queued.ID, running.ID} {
			if got := mustJob(t, s, id); got.State != job.StateCancelled || got.Reason != job.ReasonDeactivated {
				t.Errorf("job %s: %+v", id, got)
			}
		}
		if got := mustJob(t, s, done.ID); got.State != job.StateSucceeded {
			t.Errorf("a finished job was changed: %+v", got)
		}
		if got := mustJob(t, s, other.ID); got.State != job.StateQueued {
			t.Errorf("a job of another repository was changed: %+v", got)
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(8*time.Second)); !errors.Is(err, job.ErrCancelled) {
			t.Errorf("Heartbeat of a job of a deactivated repository = %v", err)
		}
	})

	t.Run("a run whose credential could not be minted is not counted", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		for i := range job.MaxRuns + 1 {
			now := at(time.Duration(i) * time.Minute)
			c := claim(t, s, instance, now)
			if err := s.Release(ctx, c.Run.ID, c.Token, now); err != nil {
				t.Fatalf("Release %d: %v", i, err)
			}
			if err := s.Release(ctx, c.Run.ID, c.Token, now); !errors.Is(err, job.ErrStaleLease) {
				t.Errorf("Release of a removed run = %v", err)
			}
			got := mustJob(t, s, j.ID)
			if got.State != job.StateQueued || got.NotBefore != now.Add(job.MintDelay) || got.CreatedAt != at(0) {
				t.Fatalf("after Release: %+v", got)
			}
			if _, err := s.Claim(ctx, instance, now.Add(job.MintDelay-time.Nanosecond)); !errors.Is(err, job.ErrNoJob) {
				t.Fatalf("claimed before the delay: %v", err)
			}
		}
		if runs := mustRuns(t, s, j.ID); len(runs) != 0 {
			t.Errorf("released runs were kept: %+v", runs)
		}
		// Cancelled before its release: it stays cancelled.
		c := claim(t, s, instance, at(10*time.Minute))
		if _, err := s.Cancel(ctx, j.ID, job.ReasonCancelled, at(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := s.Release(ctx, c.Run.ID, c.Token, at(10*time.Minute)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Release of a cancelled run = %v", err)
		}
		if got := mustJob(t, s, j.ID); got.State != job.StateCancelled {
			t.Errorf("a release brought a cancelled job back: %+v", got)
		}
	})

	t.Run("an enqueue with a key supersedes the queued jobs of the key", func(t *testing.T) {
		s := setup(t)
		running := enqueue(t, s, acme, "2001", "main", at(0))
		claim(t, s, instance, at(0))
		old := enqueue(t, s, acme, "2001", "main", at(time.Second))
		otherKey := enqueue(t, s, acme, "2001", "dev", at(time.Second))
		noKey := enqueue(t, s, acme, "2001", "", at(time.Second))
		otherWorkspace := enqueue(t, s, beta, "3001", "main", at(time.Second))
		latest := enqueue(t, s, acme, "2001", "main", at(2*time.Second))
		got := mustJob(t, s, old.ID)
		if got.State != job.StateCancelled || got.Reason != job.ReasonSuperseded || got.FinishedAt != at(2*time.Second) {
			t.Errorf("the superseded job: %+v", got)
		}
		for _, j := range []job.Job{otherKey, noKey, otherWorkspace, latest} {
			if got := mustJob(t, s, j.ID); got.State != job.StateQueued {
				t.Errorf("job %s (key %q) is %s, want queued", j.ID, j.Key, got.State)
			}
		}
		if got := mustJob(t, s, running.ID); got.State != job.StateRunning {
			t.Errorf("a running job was superseded: %+v", got)
		}
	})
}

func ok() job.Result {
	return job.Result{Outcome: job.OutcomeSucceeded, Summary: "nothing to do", Data: []byte("{}")}
}

func enqueue(t *testing.T, s job.Store, workspace, repo, key string, when time.Time) job.Job {
	t.Helper()
	j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: workspace, Forge: "github", Repository: repo,
		Commit: commit, Ref: "refs/heads/main", Delivery: "d-" + repo, Key: key}, when)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return j
}

func claim(t *testing.T, s job.Store, c job.Claimant, now time.Time) job.Claimed {
	t.Helper()
	got, err := s.Claim(context.Background(), c, now)
	if err != nil {
		t.Fatalf("Claim at %s: %v", now, err)
	}
	return got
}

func mustJob(t *testing.T, s job.Store, id string) job.Job {
	t.Helper()
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	return j
}

func mustRuns(t *testing.T, s job.Store, id string) []job.Run {
	t.Helper()
	runs, err := s.Runs(context.Background(), id)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	return runs
}
