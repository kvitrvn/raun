package jobtest

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

var noop = []job.Capability{job.CapabilityNoop}

// RunnerSuite runs the contract of job.RunnerStore on the stores newFixture
// gives, each with the workspaces acme and beta, and the repositories
// github/2001 in acme and github/3001 in beta.
func RunnerSuite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()
	setup := func(t *testing.T) (job.Store, job.RunnerStore) {
		t.Helper()
		f := newFixture(t)
		f.AddWorkspace(t, job.Workspace{ID: beta, Name: "beta"})
		f.AddWorkspace(t, job.Workspace{ID: acme, Name: "acme"})
		f.AddRepository(t, acme, "github", "2001")
		f.AddRepository(t, beta, "github", "3001")
		return f.Store, f.Runners
	}

	t.Run("create then read", func(t *testing.T) {
		_, rs := setup(t)
		b, _, _ := createRunner(t, rs, "builder-b", "", at(0))
		a, _, _ := createRunner(t, rs, "builder-a", acme, at(time.Second))
		got, err := rs.Runner(ctx, a.ID)
		if err != nil || !sameRunner(got, a) {
			t.Fatalf("Runner = %+v, %v, want %+v", got, err, a)
		}
		all, err := rs.Runners(ctx)
		if err != nil || len(all) != 2 || !sameRunner(all[0], a) || !sameRunner(all[1], b) {
			t.Errorf("Runners = %+v, %v", all, err)
		}
		if _, err := rs.Runner(ctx, id32("ff")); !errors.Is(err, job.ErrNoRunner) {
			t.Errorf("Runner of an unknown identifier = %v", err)
		}
		if ws, err := rs.Workspaces(ctx); err != nil || len(ws) != 2 || ws[0].Name != "acme" || ws[0].ID != acme || ws[1].Name != "beta" {
			t.Errorf("Workspaces = %+v, %v", ws, err)
		}
	})

	t.Run("a name is taken once", func(t *testing.T) {
		_, rs := setup(t)
		createRunner(t, rs, "builder", "", at(0))
		r, _, hash, err := job.NewRunner(rand.Reader, job.RunnerRequest{Name: "builder", Workspace: acme, Capabilities: noop, CreatedBy: id32("e1")}, at(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.CreateRunner(ctx, r, hash); !errors.Is(err, job.ErrRunnerNameTaken) {
			t.Errorf("a taken name = %v, want ErrRunnerNameTaken", err)
		}
		if all, _ := rs.Runners(ctx); len(all) != 1 {
			t.Errorf("after a refusal: %+v", all)
		}
	})

	t.Run("authenticate by the hash of the token", func(t *testing.T) {
		_, rs := setup(t)
		r, _, hash := createRunner(t, rs, "builder", "", at(0))
		got, err := rs.Authenticate(ctx, hash, at(time.Minute))
		if err != nil || got.ID != r.ID || !got.LastContact.Equal(at(time.Minute)) {
			t.Fatalf("Authenticate = %+v, %v", got, err)
		}
		// Within ContactEvery the last contact stays; past it, it moves.
		if got, _ := rs.Authenticate(ctx, hash, at(time.Minute+job.ContactEvery-time.Second)); !got.LastContact.Equal(at(time.Minute)) {
			t.Errorf("the last contact moved within ContactEvery: %s", got.LastContact)
		}
		if got, _ := rs.Authenticate(ctx, hash, at(time.Minute+job.ContactEvery)); !got.LastContact.Equal(at(time.Minute + job.ContactEvery)) {
			t.Errorf("the last contact did not move: %s", got.LastContact)
		}
		if stored, _ := rs.Runner(ctx, r.ID); !stored.LastContact.Equal(at(time.Minute + job.ContactEvery)) {
			t.Errorf("the stored last contact: %s", stored.LastContact)
		}
		if _, err := rs.Authenticate(ctx, job.HashToken("another"), at(time.Minute)); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("an unknown token = %v", err)
		}
	})

	t.Run("a rotation refuses the old token and keeps the runs", func(t *testing.T) {
		s, rs := setup(t)
		r, _, old := createRunner(t, rs, "builder", "", at(0))
		enqueue(t, s, acme, "2001", "", at(0))
		c, err := rs.ClaimFor(ctx, r.ID, at(0))
		if err != nil {
			t.Fatal(err)
		}
		_, hash, err := job.NewToken(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.RotateRunner(ctx, r.ID, hash); err != nil {
			t.Fatal(err)
		}
		if _, err := rs.Authenticate(ctx, old, at(time.Second)); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("the old token after a rotation = %v", err)
		}
		if _, err := rs.Authenticate(ctx, hash, at(time.Second)); err != nil {
			t.Errorf("the new token = %v", err)
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(time.Second)); err != nil {
			t.Errorf("a run after a rotation = %v, want its lease kept", err)
		}
		if err := rs.RotateRunner(ctx, id32("ff"), hash); !errors.Is(err, job.ErrNoRunner) {
			t.Errorf("RotateRunner of an unknown runner = %v", err)
		}
	})

	t.Run("a revocation refuses the runner and loses its runs at once", func(t *testing.T) {
		s, rs := setup(t)
		r, _, hash := createRunner(t, rs, "builder", "", at(0))
		other, _, _ := createRunner(t, rs, "other", "", at(0))
		mine := enqueue(t, s, acme, "2001", "", at(0))
		theirs := enqueue(t, s, beta, "3001", "", at(time.Second))
		done := enqueue(t, s, acme, "2001", "", at(2*time.Second))
		c, err := rs.ClaimFor(ctx, r.ID, at(3*time.Second))
		if err != nil || c.Job.ID != mine.ID {
			t.Fatalf("ClaimFor = %+v, %v", c, err)
		}
		o, err := rs.ClaimFor(ctx, other.ID, at(3*time.Second))
		if err != nil || o.Job.ID != theirs.ID {
			t.Fatalf("ClaimFor = %+v, %v", o, err)
		}
		d, err := rs.ClaimFor(ctx, r.ID, at(3*time.Second))
		if err != nil || d.Job.ID != done.ID {
			t.Fatalf("ClaimFor = %+v, %v", d, err)
		}
		if err := s.Report(ctx, d.Run.ID, d.Token, ok(), at(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err := rs.RevokeRunner(ctx, r.ID, at(5*time.Second))
		if err != nil || n != 1 {
			t.Fatalf("RevokeRunner = %d, %v, want 1", n, err)
		}
		if got, _ := rs.Runner(ctx, r.ID); !got.Revoked() || !got.RevokedAt.Equal(at(5*time.Second)) {
			t.Errorf("the revoked runner: %+v", got)
		}
		if _, err := rs.Authenticate(ctx, hash, at(6*time.Second)); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("Authenticate of a revoked runner = %v", err)
		}
		if _, err := rs.ClaimFor(ctx, r.ID, at(6*time.Second)); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("ClaimFor of a revoked runner = %v", err)
		}
		if got := mustJob(t, s, mine.ID); got.State != job.StateQueued {
			t.Errorf("the job of the revoked runner: %+v", got)
		}
		if runs := mustRuns(t, s, mine.ID); len(runs) != 1 || runs[0].Outcome != job.OutcomeLost || !runs[0].EndedAt.Equal(at(5*time.Second)) {
			t.Errorf("its run: %+v", runs)
		}
		if _, err := s.Heartbeat(ctx, c.Run.ID, c.Token, at(6*time.Second)); !errors.Is(err, job.ErrStaleLease) {
			t.Errorf("Heartbeat of a lost run = %v", err)
		}
		if got := mustJob(t, s, theirs.ID); got.State != job.StateRunning {
			t.Errorf("a job of another runner changed: %+v", got)
		}
		if got := mustJob(t, s, done.ID); got.State != job.StateSucceeded {
			t.Errorf("a finished job changed: %+v", got)
		}
		if n, err := rs.RevokeRunner(ctx, r.ID, at(7*time.Second)); err != nil || n != 0 {
			t.Errorf("RevokeRunner again = %d, %v", n, err)
		}
		if got, _ := rs.Runner(ctx, r.ID); !got.RevokedAt.Equal(at(5 * time.Second)) {
			t.Errorf("a second revocation moved the time: %s", got.RevokedAt)
		}
		_, fresh, _ := job.NewToken(rand.Reader)
		if err := rs.RotateRunner(ctx, r.ID, fresh); !errors.Is(err, job.ErrRevoked) {
			t.Errorf("RotateRunner of a revoked runner = %v", err)
		}
		if _, err := rs.RevokeRunner(ctx, id32("ff"), at(7*time.Second)); !errors.Is(err, job.ErrNoRunner) {
			t.Errorf("RevokeRunner of an unknown runner = %v", err)
		}
	})

	t.Run("a revocation fails a job out of runs", func(t *testing.T) {
		s, rs := setup(t)
		r, _, _ := createRunner(t, rs, "builder", "", at(0))
		j := enqueue(t, s, acme, "2001", "", at(0))
		now := at(0)
		for range job.MaxRuns - 1 {
			if _, err := rs.ClaimFor(ctx, r.ID, now); err != nil {
				t.Fatal(err)
			}
			now = now.Add(job.Lease)
			if _, err := s.Sweep(ctx, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := rs.ClaimFor(ctx, r.ID, now); err != nil {
			t.Fatal(err)
		}
		if n, err := rs.RevokeRunner(ctx, r.ID, now); err != nil || n != 1 {
			t.Fatalf("RevokeRunner = %d, %v", n, err)
		}
		if got := mustJob(t, s, j.ID); got.State != job.StateFailed || got.Reason != job.ReasonLost {
			t.Errorf("a job whose last run was lost to a revocation: %+v", got)
		}
	})

	t.Run("a claim takes the scope and the capabilities of the store", func(t *testing.T) {
		s, rs := setup(t)
		inAcme, _, _ := createRunner(t, rs, "acme-builder", acme, at(0))
		instance, _, _ := createRunner(t, rs, "builder", "", at(0))
		agents := job.Runner{ID: id32("c3"), Name: "agents", Capabilities: []job.Capability{"agents"}, CreatedBy: id32("e1"), CreatedAt: at(0)}
		_, hash, _ := job.NewToken(rand.Reader)
		if err := rs.CreateRunner(ctx, agents, hash); err != nil {
			t.Fatal(err)
		}
		other := enqueue(t, s, beta, "3001", "", at(0))
		if _, err := rs.ClaimFor(ctx, inAcme.ID, at(time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a runner of acme claimed a job of beta: %v", err)
		}
		if _, err := rs.ClaimFor(ctx, agents.ID, at(time.Second)); !errors.Is(err, job.ErrNoJob) {
			t.Errorf("a runner without noop claimed a noop job: %v", err)
		}
		c, err := rs.ClaimFor(ctx, instance.ID, at(time.Second))
		if err != nil || c.Job.ID != other.ID || c.Run.Runner != instance.ID {
			t.Errorf("the runner of the instance: %+v, %v", c, err)
		}
		mine := enqueue(t, s, acme, "2001", "", at(2*time.Second))
		if c, err := rs.ClaimFor(ctx, inAcme.ID, at(3*time.Second)); err != nil || c.Job.ID != mine.ID {
			t.Errorf("a runner of acme and a job of acme: %+v, %v", c, err)
		}
		if _, err := rs.ClaimFor(ctx, id32("ff"), at(3*time.Second)); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("ClaimFor of an unknown runner = %v", err)
		}
	})
}

func createRunner(t *testing.T, rs job.RunnerStore, name, workspace string, when time.Time) (job.Runner, string, string) {
	t.Helper()
	r, token, hash, err := job.NewRunner(rand.Reader, job.RunnerRequest{Name: name, Workspace: workspace, Capabilities: noop, CreatedBy: id32("e1")}, when)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.CreateRunner(context.Background(), r, hash); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	return r, token, hash
}

func sameRunner(a, b job.Runner) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Workspace == b.Workspace && slices.Equal(a.Capabilities, b.Capabilities) &&
		a.CreatedBy == b.CreatedBy && a.CreatedAt.Equal(b.CreatedAt) && a.LastContact.Equal(b.LastContact) &&
		a.Release == b.Release && a.RevokedAt.Equal(b.RevokedAt)
}
