package jobtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// logCases are the cases of the suite on the runs and their logs.
func logCases(t *testing.T, setup func(t *testing.T) job.Store) {
	ctx := context.Background()

	t.Run("a run is read by its identifier", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(time.Second))
		got, err := s.Run(ctx, c.Run.ID)
		if err != nil || got.ID != c.Run.ID || got.Job != c.Job.ID || got.Runner != "r1" || !got.Open() {
			t.Fatalf("Run = %+v, %v", got, err)
		}
		if err := s.Report(ctx, c.Run.ID, c.Token, ok(), at(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Run(ctx, c.Run.ID); got.Outcome != job.OutcomeSucceeded || got.Result == nil {
			t.Errorf("Run after its result = %+v", got)
		}
		if _, err := s.Run(ctx, id32("ff")); !errors.Is(err, job.ErrNotFound) {
			t.Errorf("Run of an unknown identifier = %v, want ErrNotFound", err)
		}
	})

	t.Run("chunks are appended in order, once each", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		if l, err := s.Log(ctx, c.Run.ID); err != nil || l != (job.Log{}) {
			t.Fatalf("Log of a run that sent none = %+v, %v", l, err)
		}
		for seq, text := range []string{"fetch\n", "noop\n"} {
			if err := s.AppendLog(ctx, c.Run.ID, c.Token, seq, text, at(time.Second)); err != nil {
				t.Fatalf("AppendLog %d: %v", seq, err)
			}
		}
		// Sent again, it is ignored, whatever it holds.
		if err := s.AppendLog(ctx, c.Run.ID, c.Token, 1, "other\n", at(time.Second)); err != nil {
			t.Errorf("a chunk sent again = %v", err)
		}
		err := s.AppendLog(ctx, c.Run.ID, c.Token, 3, "skipped\n", at(time.Second))
		var seqErr *job.SequenceError
		if !errors.As(err, &seqErr) || seqErr.Expected != 2 || !errors.Is(err, job.ErrLogSequence) {
			t.Errorf("a chunk that skips a number = %v, want a SequenceError expecting 2", err)
		}
		for _, bad := range []struct {
			seq  int
			text string
		}{{-1, "x"}, {2, "\xff"}, {2, strings.Repeat("x", job.MaxLogChunk+1)}} {
			if err := s.AppendLog(ctx, c.Run.ID, c.Token, bad.seq, bad.text, at(time.Second)); !errors.Is(err, job.ErrInvalidLog) {
				t.Errorf("AppendLog(%d, %d bytes) = %v, want ErrInvalidLog", bad.seq, len(bad.text), err)
			}
		}
		if l, _ := s.Log(ctx, c.Run.ID); l.Text != "fetch\nnoop\n" || l.Next != 2 || l.Cut {
			t.Errorf("Log = %+v", l)
		}
		if l, _ := s.Log(ctx, id32("ff")); l != (job.Log{}) {
			t.Errorf("Log of an unknown run = %+v", l)
		}
	})

	t.Run("a log is cut at its bound and keeps counting", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		chunk := strings.Repeat("é", job.MaxLogChunk/2)
		n := job.MaxLog/len(chunk) + 2
		for seq := range n {
			if err := s.AppendLog(ctx, c.Run.ID, c.Token, seq, chunk, at(time.Second)); err != nil {
				t.Fatalf("AppendLog %d: %v", seq, err)
			}
		}
		l, err := s.Log(ctx, c.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Text) > job.MaxLog || !strings.HasSuffix(l.Text, "\n"+job.Truncated) || strings.Count(l.Text, job.Truncated) != 1 || !l.Cut || l.Next != n {
			t.Errorf("a log past its bound: %d bytes, cut %v, next %d, ends %q", len(l.Text), l.Cut, l.Next, l.Text[max(len(l.Text)-20, 0):])
		}
		if body := strings.TrimSuffix(l.Text, "\n"+job.Truncated); strings.Trim(body, "é") != "" {
			t.Error("the log was cut inside a character")
		}
	})

	t.Run("only the current run holding its lease appends", func(t *testing.T) {
		s := setup(t)
		j := enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		if err := s.AppendLog(ctx, c.Run.ID, "another token", 0, "x", at(time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("a foreign lease = %v", err)
		}
		if err := s.AppendLog(ctx, id32("ff"), c.Token, 0, "x", at(time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("an unknown run = %v", err)
		}
		if err := s.AppendLog(ctx, c.Run.ID, c.Token, 0, "x", at(job.Lease)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("an expired lease = %v", err)
		}
		if l, _ := s.Log(ctx, c.Run.ID); l != (job.Log{}) {
			t.Errorf("a refused chunk was kept: %+v", l)
		}
		c2 := claim(t, s, instance, at(job.Lease))
		if _, err := s.Cancel(ctx, j.ID, job.ReasonCancelled, at(job.Lease+time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendLog(ctx, c2.Run.ID, c2.Token, 0, "x", at(job.Lease+2*time.Second)); !errors.Is(err, job.ErrCancelled) {
			t.Errorf("a cancelled job = %v", err)
		}
	})

	t.Run("concurrent chunks are each taken once", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		c := claim(t, s, instance, at(0))
		const chunks = 16
		var wg sync.WaitGroup
		for seq := range chunks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each chunk is sent until it is taken, as a runner sends
				// again from the number it is told.
				for {
					err := s.AppendLog(ctx, c.Run.ID, c.Token, seq, fmt.Sprintf("%02d\n", seq), at(time.Second))
					if err == nil {
						return
					}
					if !errors.Is(err, job.ErrLogSequence) {
						t.Errorf("AppendLog %d: %v", seq, err)
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}
		wg.Wait()
		var want strings.Builder
		for seq := range chunks {
			fmt.Fprintf(&want, "%02d\n", seq)
		}
		if l, _ := s.Log(ctx, c.Run.ID); l.Text != want.String() || l.Next != chunks {
			t.Errorf("Log = %q, next %d", l.Text, l.Next)
		}
	})

	t.Run("logs are purged after their run ended", func(t *testing.T) {
		s := setup(t)
		enqueue(t, s, acme, "2001", "", at(0))
		enqueue(t, s, acme, "2002", "", at(0))
		done := claim(t, s, instance, at(0))
		open := claim(t, s, instance, at(0))
		for _, c := range []job.Claimed{done, open} {
			if err := s.AppendLog(ctx, c.Run.ID, c.Token, 0, "x\n", at(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Report(ctx, done.Run.ID, done.Token, ok(), at(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if n, err := s.PurgeLogs(ctx, at(2*time.Second)); err != nil || n != 0 {
			t.Errorf("PurgeLogs at the end of the run = %d, %v", n, err)
		}
		if n, err := s.PurgeLogs(ctx, at(3*time.Second)); err != nil || n != 1 {
			t.Errorf("PurgeLogs after the end of the run = %d, %v", n, err)
		}
		if l, _ := s.Log(ctx, done.Run.ID); l != (job.Log{}) {
			t.Errorf("the purged log: %+v", l)
		}
		if l, _ := s.Log(ctx, open.Run.ID); l.Text != "x\n" {
			t.Errorf("the log of an open run was purged: %+v", l)
		}
	})
}

// wakeCases are the cases of the suite on the notifier of the store.
func wakeCases(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()

	t.Run("the store tells when a job may have become claimable", func(t *testing.T) {
		f := newFixture(t)
		if f.Queued == nil {
			t.Skip("the fixture does not count")
		}
		f.AddWorkspace(t, job.Workspace{ID: acme, Name: "acme"})
		f.AddRepository(t, acme, "github", "2001")
		s := f.Store
		step := func(what string, want bool, do func()) {
			t.Helper()
			before := f.Queued()
			do()
			if got := f.Queued() > before; got != want {
				t.Errorf("%s: told %v, want %v", what, got, want)
			}
		}
		var j job.Job
		step("an enqueue", true, func() { j = enqueue(t, s, acme, "2001", "", at(0)) })
		var c job.Claimed
		step("a claim that sweeps nothing", false, func() { c = claim(t, s, instance, at(0)) })
		step("a heartbeat", false, func() {
			if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(time.Second)); err != nil {
				t.Fatal(err)
			}
		})
		step("a sweep that sends a lost run back", true, func() {
			if n, err := s.Sweep(ctx, at(job.Lease+time.Second)); err != nil || n != 1 {
				t.Fatalf("Sweep = %d, %v", n, err)
			}
		})
		step("a sweep that changes nothing", false, func() {
			if _, err := s.Sweep(ctx, at(job.Lease+2*time.Second)); err != nil {
				t.Fatal(err)
			}
		})
		c = claim(t, s, instance, at(2*job.Lease))
		step("a claim whose sweep sends a lost run back", true, func() {
			if _, err := s.Claim(ctx, instance, at(4*job.Lease)); err != nil {
				t.Fatalf("Claim = %v", err)
			}
		})
		if got := mustJob(t, s, j.ID); got.State != job.StateRunning {
			t.Fatalf("the job is %s", got.State)
		}
	})

	t.Run("a revocation tells when it sends runs back", func(t *testing.T) {
		f := newFixture(t)
		if f.Queued == nil {
			t.Skip("the fixture does not count")
		}
		f.AddWorkspace(t, job.Workspace{ID: acme, Name: "acme"})
		f.AddRepository(t, acme, "github", "2001")
		r, _, _ := createRunner(t, f.Runners, "builder", "", at(0))
		idle, _, _ := createRunner(t, f.Runners, "idle", "", at(0))
		enqueue(t, f.Store, acme, "2001", "", at(0))
		if _, err := f.Runners.ClaimFor(ctx, r.ID, at(time.Second)); err != nil {
			t.Fatal(err)
		}
		before := f.Queued()
		if _, err := f.Runners.RevokeRunner(ctx, idle.ID, at(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if f.Queued() != before {
			t.Error("a revocation that sent nothing back told")
		}
		if n, err := f.Runners.RevokeRunner(ctx, r.ID, at(2*time.Second)); err != nil || n != 1 {
			t.Fatalf("RevokeRunner = %d, %v", n, err)
		}
		if f.Queued() == before {
			t.Error("a revocation that sent a run back did not tell")
		}
	})
}
