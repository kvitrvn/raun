package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// noopOnPush is a policy point of the tests: a noop job for each push.
func noopOnPush(d repository.Delivery) []repository.JobRequest {
	if d.Push == nil || d.Push.Deleted {
		return nil
	}
	return []repository.JobRequest{{Kind: string(job.KindNoop), Commit: d.Push.After, Ref: d.Push.Ref}}
}

// jobServer starts a server on the fake forge with policy as its policy
// point, Alice signed up, and acme/api activated in her workspace; it
// returns the server, the fake forge, the ticks of its sweep and what each
// sweep did.
func jobServer(t *testing.T, policy repository.JobPolicy) (*server, *forgetest.Fake, chan time.Time, chan int) {
	t.Helper()
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	ticks, swept := make(chan time.Time), make(chan int, 4)
	s := start(t, dir, append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001"), serverDeps{
		forge: fake, missing: allCapabilities, jobs: policy, sweep: ticks,
		swept: func(n int, err error) {
			if err != nil {
				t.Errorf("sweep: %v", err)
			}
			swept <- n
		},
	})
	alice := signedUp(t, s, fake, "1001", "alice")
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d %s", status, body)
	}
	return s, fake, ticks, swept
}

// openJobs opens the job store of the database of s, beside the server.
func openJobs(t *testing.T, s *server) sqlite.Jobs {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(s.dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.Jobs()
}

var anyRunner = job.Claimant{Runner: "r1", Capabilities: []job.Capability{job.CapabilityNoop}}

// TestNoJobInProduction: the policy point of production creates no job
// for a push, until epic 7 decides one.
func TestNoJobInProduction(t *testing.T) {
	s, fake, _, _ := jobServer(t, nil)
	h, b := fake.Deliver("d1", push("2001", strings.Repeat("b", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	if _, err := openJobs(t, s).Claim(context.Background(), anyRunner, time.Now()); !errors.Is(err, job.ErrNoJob) {
		t.Errorf("a claim after a push in production = %v, want ErrNoJob", err)
	}
}

// TestJobsThroughTheServer: with a policy that asks for a noop job on a
// push, a signed push queues one job, claimed once; the sweeper of the
// server sends it back to the queue once its lease has expired.
func TestJobsThroughTheServer(t *testing.T) {
	ctx := context.Background()
	s, fake, ticks, swept := jobServer(t, noopOnPush)
	after := strings.Repeat("b", 40)
	h, b := fake.Deliver("d1", push("2001", after))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	jobs := openJobs(t, s)
	now := time.Now()
	c, err := jobs.Claim(ctx, anyRunner, now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if c.Job.Kind != job.KindNoop || c.Job.Repository != "2001" || c.Job.Commit != after || c.Job.Ref != "refs/heads/main" || c.Job.Delivery != "d1" {
		t.Errorf("the job of the push: %+v", c.Job)
	}
	if _, err := jobs.Claim(ctx, anyRunner, now); !errors.Is(err, job.ErrNoJob) {
		t.Errorf("a second claim = %v, want ErrNoJob", err)
	}
	// The same delivery sent again creates nothing more.
	if got := post(t, s, h, b); got != http.StatusOK {
		t.Errorf("a delivery sent again = %d", got)
	}
	ticks <- now.Add(job.Lease)
	select {
	case n := <-swept:
		if n != 1 {
			t.Errorf("the sweep changed %d jobs, want 1", n)
		}
	case <-time.After(wait):
		t.Fatal("no sweep")
	}
	again, err := jobs.Claim(ctx, anyRunner, now.Add(job.Lease))
	if err != nil || again.Job.ID != c.Job.ID {
		t.Fatalf("the claim after the lease = %+v, %v", again, err)
	}
	if runs, err := jobs.Runs(ctx, c.Job.ID); err != nil || len(runs) != 2 || runs[0].Outcome != job.OutcomeLost {
		t.Errorf("runs = %+v, %v", runs, err)
	}
}
