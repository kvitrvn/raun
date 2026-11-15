package jobtest

import (
	"context"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// readCases are the cases of the suite on the reads of the screens.
func readCases(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()
	setup := func(t *testing.T) Fixture {
		t.Helper()
		f := newFixture(t)
		if f.Reads == nil {
			t.Skip("the fixture has no reads")
		}
		f.AddWorkspace(t, job.Workspace{ID: acme, Name: "acme"})
		f.AddRepository(t, acme, "github", "2001")
		f.AddRepository(t, acme, "github", "2002")
		return f
	}

	t.Run("the jobs of a repository, newest first, with their runs", func(t *testing.T) {
		f := setup(t)
		s := f.Store
		old := enqueue(t, s, acme, "2001", "", at(0))
		claim(t, s, instance, at(time.Second))
		// The first run is lost, the second one runs.
		claim(t, s, instance, at(job.Lease+2*time.Second))
		sameTime := []job.Job{enqueue(t, s, acme, "2001", "", at(time.Minute)), enqueue(t, s, acme, "2001", "", at(time.Minute))}
		enqueue(t, s, acme, "2002", "", at(2*time.Minute))
		got, err := f.Reads.JobsOf(ctx, "github", "2001", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[2].Job.ID != old.ID {
			t.Fatalf("JobsOf = %d jobs, the last %+v", len(got), got[len(got)-1].Job)
		}
		// Of two jobs of the same time, the greater identifier first.
		first, second := sameTime[0].ID, sameTime[1].ID
		if first < second {
			first, second = second, first
		}
		if got[0].Job.ID != first || got[1].Job.ID != second {
			t.Errorf("the order of two jobs of the same time: %s, %s", got[0].Job.ID, got[1].Job.ID)
		}
		if runs := got[2].Runs; len(runs) != 2 || runs[0].Outcome != job.OutcomeLost || !runs[1].Open() {
			t.Errorf("the runs of the old job: %+v", runs)
		}
		if len(got[0].Runs) != 0 {
			t.Errorf("a queued job has runs: %+v", got[0].Runs)
		}
		if got, _ := f.Reads.JobsOf(ctx, "github", "2001", 1); len(got) != 1 || got[0].Job.ID != first {
			t.Errorf("JobsOf with a limit of 1 = %+v", got)
		}
		for _, limit := range []int{0, -1} {
			if got, err := f.Reads.JobsOf(ctx, "github", "2001", limit); err != nil || len(got) != 0 {
				t.Errorf("JobsOf with a limit of %d = %d, %v", limit, len(got), err)
			}
		}
		if got, _ := f.Reads.JobsOf(ctx, "github", "9999", 10); len(got) != 0 {
			t.Errorf("JobsOf of another repository = %+v", got)
		}
	})

	t.Run("the open runs of each runner", func(t *testing.T) {
		f := setup(t)
		s := f.Store
		for range 3 {
			enqueue(t, s, acme, "2001", "", at(0))
		}
		other := job.Claimant{Runner: "r2", Capabilities: []job.Capability{job.CapabilityNoop}}
		a := claim(t, s, instance, at(time.Second))
		claim(t, s, instance, at(time.Second))
		claim(t, s, other, at(time.Second))
		if err := s.Report(ctx, a.Run.ID, a.Token, ok(), at(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err := f.Reads.OpenRuns(ctx)
		if err != nil || len(got) != 2 || got["r1"] != 1 || got["r2"] != 1 {
			t.Errorf("OpenRuns = %v, %v", got, err)
		}
	})
}
