package job_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
)

func TestWaiting(t *testing.T) {
	now := t0.Add(time.Hour)
	queued := job.Job{Kind: job.KindNoop, Workspace: acme.ID, State: job.StateQueued, NotBefore: t0}
	runner := func(workspace string, contact time.Time, caps ...job.Capability) job.Runner {
		return job.Runner{ID: "r", Workspace: workspace, Capabilities: caps, LastContact: contact}
	}
	noop := job.CapabilityNoop
	revoked := runner("", now, noop)
	revoked.RevokedAt = t0
	for _, tt := range []struct {
		name    string
		j       job.Job
		runners []job.Runner
		want    job.Wait
	}{
		{"a runner seen just now", queued, []job.Runner{runner("", now, noop)}, job.WaitNone},
		{"a runner of its workspace", queued, []job.Runner{runner(acme.ID, now, noop)}, job.WaitNone},
		{"no runner", queued, nil, job.WaitNoRunner},
		{"a runner of another workspace", queued, []job.Runner{runner(strings.Repeat("b", 32), now, noop)}, job.WaitNoRunner},
		{"a runner without the capability", queued, []job.Runner{runner("", now)}, job.WaitNoRunner},
		{"a revoked runner", queued, []job.Runner{revoked}, job.WaitNoRunner},
		{"a runner never seen", queued, []job.Runner{runner("", time.Time{}, noop)}, job.WaitNotSeen},
		{"a runner seen just within two minutes", queued, []job.Runner{runner("", now.Add(-2*job.ContactEvery+time.Second), noop)}, job.WaitNone},
		{"a runner seen two minutes ago", queued, []job.Runner{runner("", now.Add(-2*job.ContactEvery), noop)}, job.WaitNotSeen},
		{"one seen among others", queued, []job.Runner{runner("", time.Time{}, noop), runner("", now, noop)}, job.WaitNone},
		{"delayed", job.Job{Kind: job.KindNoop, State: job.StateQueued, NotBefore: now.Add(time.Second)}, []job.Runner{runner("", now, noop)}, job.WaitDelayed},
		{"running", job.Job{Kind: job.KindNoop, State: job.StateRunning}, nil, job.WaitNone},
	} {
		if got := job.Waiting(tt.j, tt.runners, now); got != tt.want {
			t.Errorf("%s: Waiting = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// boardWorld is a board on a memory store with one claimed job of
// github/2001, and its clock.
type boardWorld struct {
	store *jobtest.Memory
	board *job.Board
	now   time.Time
	job   job.Job
	claim job.Claimed
}

func newBoardWorld(t *testing.T) *boardWorld {
	t.Helper()
	w := &boardWorld{store: &jobtest.Memory{}, now: t0}
	w.store.AddWorkspace(acme)
	w.board = job.NewBoard(job.BoardOptions{Store: w.store, Reads: w.store, Runners: w.store, Now: func() time.Time { return w.now }})
	j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: acme.ID, Forge: "github", Repository: "2001", Commit: strings.Repeat("c", 40)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Enqueue(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	w.job = j
	c, err := w.store.Claim(context.Background(), job.Claimant{Runner: "r1", Capabilities: []job.Capability{job.CapabilityNoop}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	w.claim = c
	return w
}

func (w *boardWorld) append(t *testing.T, seq int, text string) {
	t.Helper()
	if err := w.store.AppendLog(context.Background(), w.claim.Run.ID, w.claim.Token, seq, text, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestBoardLog(t *testing.T) {
	ctx := context.Background()
	w := newBoardWorld(t)
	run := w.claim.Run.ID
	w.append(t, 0, "fetching\n")
	part, err := w.board.Log(ctx, "github", "2001", w.job.ID, run, 0, false)
	if err != nil || part.Text != "fetching\n" || part.Next != 9 || part.Reset || part.Ended || part.Cut {
		t.Fatalf("Log from 0 = %+v, %v", part, err)
	}
	w.append(t, 1, "é done\n")
	part, err = w.board.Log(ctx, "github", "2001", w.job.ID, run, part.Next, false)
	if err != nil || part.Text != "é done\n" || part.Next != len("fetching\né done\n") {
		t.Fatalf("Log from the last offset = %+v, %v", part, err)
	}
	if part, err := w.board.Log(ctx, "github", "2001", w.job.ID, run, part.Next, false); err != nil || part.Text != "" {
		t.Errorf("Log at the end = %+v, %v", part, err)
	}
	for _, offset := range []int{-1, len("fetching\né done\n") + 1, len("fetching\n") + 1} {
		if _, err := w.board.Log(ctx, "github", "2001", w.job.ID, run, offset, false); !errors.Is(err, job.ErrBadOffset) {
			t.Errorf("Log from %d = %v, want ErrBadOffset", offset, err)
		}
	}
	for name, try := range map[string]func() error{
		"another repository": func() error { _, err := w.board.Log(ctx, "github", "2002", w.job.ID, run, 0, false); return err },
		"another forge":      func() error { _, err := w.board.Log(ctx, "gitea", "2001", w.job.ID, run, 0, false); return err },
		"another job": func() error {
			_, err := w.board.Log(ctx, "github", "2001", strings.Repeat("f", 32), run, 0, false)
			return err
		},
		"another run": func() error {
			_, err := w.board.Log(ctx, "github", "2001", w.job.ID, strings.Repeat("f", 32), 0, false)
			return err
		},
	} {
		if err := try(); !errors.Is(err, job.ErrNotFound) {
			t.Errorf("%s = %v, want ErrNotFound", name, err)
		}
	}

	// The run ends: nothing more will come.
	if err := w.store.Report(ctx, run, w.claim.Token, job.Result{Outcome: job.OutcomeSucceeded, Summary: "ok"}, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if part, _ := w.board.Log(ctx, "github", "2001", w.job.ID, run, 0, false); !part.Ended {
		t.Errorf("the log of an ended run: %+v", part)
	}
}

func TestBoardLogResetsAtTheCut(t *testing.T) {
	ctx := context.Background()
	w := newBoardWorld(t)
	run := w.claim.Run.ID
	chunk := strings.Repeat("x", job.MaxLogChunk)
	n := 0
	for ; n < job.MaxLog/job.MaxLogChunk; n++ {
		w.append(t, n, chunk)
	}
	part, err := w.board.Log(ctx, "github", "2001", w.job.ID, run, 0, false)
	if err != nil || part.Cut || len(part.Text) != job.MaxLog {
		t.Fatalf("a full log: %d bytes, cut %v, %v", len(part.Text), part.Cut, err)
	}
	shown := part.Next
	// The next chunk cuts the log, and shortens its end.
	w.append(t, n, chunk)
	part, err = w.board.Log(ctx, "github", "2001", w.job.ID, run, shown, false)
	if err != nil || !part.Reset || !part.Cut || !strings.HasSuffix(part.Text, job.Truncated) || part.Next != len(part.Text) || part.Next > shown {
		t.Fatalf("Log after the cut = reset %v, cut %v, %d bytes, %v", part.Reset, part.Cut, len(part.Text), err)
	}
	if again, err := w.board.Log(ctx, "github", "2001", w.job.ID, run, part.Next, true); err != nil || again.Reset || again.Text != "" {
		t.Errorf("Log after the reset = %+v, %v", again, err)
	}
}

func TestBoardJobsAndCancel(t *testing.T) {
	ctx := context.Background()
	w := newBoardWorld(t)
	queued, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: acme.ID, Forge: "github", Repository: "2001", Commit: strings.Repeat("d", 40)}, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Enqueue(ctx, queued); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(2 * time.Second)
	views, err := w.board.Jobs(ctx, "github", "2001")
	if err != nil || len(views) != 2 || views[0].Job.ID != queued.ID || views[0].Wait != job.WaitNoRunner || len(views[1].Runs) != 1 || views[1].Wait != job.WaitNone {
		t.Fatalf("Jobs = %+v, %v", views, err)
	}
	if views, _ := w.board.Jobs(ctx, "github", "2002"); len(views) != 0 {
		t.Errorf("Jobs of another repository = %+v", views)
	}
	if v, err := w.board.Job(ctx, "github", "2001", w.job.ID); err != nil || len(v.Runs) != 1 || v.Runs[0].ID != w.claim.Run.ID {
		t.Errorf("Job = %+v, %v", v, err)
	}
	if _, err := w.board.Job(ctx, "github", "2002", w.job.ID); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Job of another repository = %v", err)
	}

	// Cancel: a queued job, a running one, then one that has ended.
	if _, err := w.board.Cancel(ctx, "github", "2002", queued.ID); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Cancel through another repository = %v", err)
	}
	for _, id := range []string{queued.ID, w.job.ID} {
		j, err := w.board.Cancel(ctx, "github", "2001", id)
		if err != nil || j.State != job.StateCancelled || j.Reason != job.ReasonCancelled {
			t.Errorf("Cancel = %+v, %v", j, err)
		}
	}
	if _, err := w.board.Cancel(ctx, "github", "2001", queued.ID); !errors.Is(err, job.ErrFinal) {
		t.Errorf("Cancel of a cancelled job = %v", err)
	}
	if v, _ := w.board.Job(ctx, "github", "2001", w.job.ID); v.Runs[0].Outcome != job.OutcomeCancelled {
		t.Errorf("the run of a cancelled job: %+v", v.Runs[0])
	}
}

func TestRegistryBusy(t *testing.T) {
	ctx := context.Background()
	store := &jobtest.Memory{}
	g := job.NewRegistry(job.RegistryOptions{Store: store, Admins: admins{admin: true}, Reads: store, Random: rand.Reader, Now: func() time.Time { return t0 }})
	if _, err := g.Busy(ctx, strings.Repeat("0", 32)); !errors.Is(err, job.ErrNotAdministrator) {
		t.Errorf("Busy for another account = %v", err)
	}
	if busy, err := g.Busy(ctx, admin); err != nil || len(busy) != 0 {
		t.Errorf("Busy of idle runners = %v, %v", busy, err)
	}
}
