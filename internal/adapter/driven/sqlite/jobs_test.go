package sqlite

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// addRepository activates the repository of forge with this identifier in
// the workspace with this identifier, created first when s has none.
func addRepository(t *testing.T, s *Store, workspace, forge, id string) {
	t.Helper()
	ctx := context.Background()
	owner := strings.Repeat("0", 30) + "a1"
	ws := repository.Workspace{ID: workspace, Name: "w" + workspace[len(workspace)-6:]}
	var n int
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM workspaces WHERE id = ?`, workspace).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		addWorkspace(t, s, ws, owner)
	}
	r := repository.Repository{Forge: forge, ID: id, Workspace: ws, FullName: "o/" + id, DefaultBranch: "main",
		Active: true, Reachable: true, ChangedBy: owner, ChangedAt: when()}
	if err := s.Repositories().Activate(ctx, r); err != nil {
		t.Fatal(err)
	}
}

func jobFixture(t *testing.T) jobtest.Fixture {
	var queued atomic.Int64
	path := filepath.Join(t.TempDir(), "raun.db")
	s, err := Open(context.Background(), path, Options{Queued: func() { queued.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return jobtest.Fixture{
		Queued:  func() int { return int(queued.Load()) },
		Store:   s.Jobs(),
		Runners: s.Runners(),
		AddWorkspace: func(t *testing.T, w job.Workspace) {
			addWorkspace(t, s, repository.Workspace{ID: w.ID, Name: w.Name}, strings.Repeat("0", 30)+"a1")
		},
		AddRepository: func(t *testing.T, workspace, forge, id string) { addRepository(t, s, workspace, forge, id) },
	}
}

func TestJobsContract(t *testing.T) { jobtest.Suite(t, jobFixture) }

func TestRunnersContract(t *testing.T) { jobtest.RunnerSuite(t, jobFixture) }

func noopJob(t *testing.T, workspace, repo string, at time.Time) job.Job {
	t.Helper()
	j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: workspace, Forge: "github", Repository: repo,
		Commit: strings.Repeat("c", 40)}, at)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// TestClaimsAcrossStores claims concurrently through two stores opened on
// the same file, as two processes would: every job is given once, and no
// claim fails on a lock.
func TestClaimsAcrossStores(t *testing.T) {
	ctx := context.Background()
	a, path := openMigrated(t)
	b := openAt(t, path)
	ws := strings.Repeat("0", 30) + "w1"
	addRepository(t, a, ws, "github", "2001")
	const jobs = 40
	t0 := when()
	for i := range jobs {
		if err := a.Jobs().Enqueue(ctx, noopJob(t, ws, "2001", t0.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	c := job.Claimant{Runner: "r", Capabilities: []job.Capability{job.CapabilityNoop}}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for i := range 16 {
		store := a.Jobs()
		if i%2 == 1 {
			store = b.Jobs()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := store.Claim(ctx, c, t0.Add(time.Second))
				if errors.Is(err, job.ErrNoJob) {
					return
				}
				if err != nil {
					t.Errorf("Claim: %v", err)
					return
				}
				mu.Lock()
				seen[got.Job.ID]++
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
}

// TestLeaseTokensAreStoredHashed finds only the hash of a lease token in
// the database, and the queue as it was after a reopen.
func TestLeaseTokensAreStoredHashed(t *testing.T) {
	ctx := context.Background()
	s, path := openMigrated(t)
	ws := strings.Repeat("0", 30) + "w1"
	addRepository(t, s, ws, "github", "2001")
	j := noopJob(t, ws, "2001", when())
	if err := s.Jobs().Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	c, err := s.Jobs().Claim(ctx, job.Claimant{Runner: "r", Capabilities: []job.Capability{job.CapabilityNoop}}, when())
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.reader.QueryRowContext(ctx, `SELECT token_hash FROM runs WHERE id = ?`, c.Run.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != job.HashToken(c.Token) || strings.Contains(stored, c.Token) {
		t.Errorf("stored %q for the token", stored)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := openAt(t, path)
	if _, err := again.Jobs().Heartbeat(ctx, c.Run.ID, c.Token, when().Add(time.Second)); err != nil {
		t.Errorf("Heartbeat after a reopen: %v", err)
	}
	if got, err := again.Jobs().Job(ctx, j.ID); err != nil || got.State != job.StateRunning {
		t.Errorf("Job after a reopen = %+v, %v", got, err)
	}
}

// jobsOf returns the jobs a delivery created.
func jobsOf(t *testing.T, s *Store, delivery string) []job.Job {
	t.Helper()
	rows, err := s.reader.QueryContext(context.Background(), `SELECT `+jobColumns+` FROM jobs WHERE delivery = ? ORDER BY id`, delivery)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []job.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

func deliveryRow(delivery, repo string, result repository.Result) (repository.Key, *repository.LoggedEvent) {
	k := repository.Key{Forge: "github", Delivery: delivery, Digest: strings.Repeat("0", 64-len(delivery)) + strings.ReplaceAll(delivery, "d", "a"), ReceivedAt: when()}
	return k, &repository.LoggedEvent{Forge: "github", Repository: repo, Delivery: delivery, Event: "push", Ref: "refs/heads/main",
		Before: strings.Repeat("a", 40), After: strings.Repeat("c", 40), ReceivedAt: when(), Result: result}
}

// TestADeliveryRecordsItsJobs: the jobs of a delivery are written with its
// keys and its log row, coalesced in the same transaction, and a failure
// keeps none of them.
func TestADeliveryRecordsItsJobs(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	ws := strings.Repeat("0", 30) + "w1"
	addRepository(t, s, ws, "github", "2001")
	addRepository(t, s, ws, "github", "2002")
	if err := s.Repositories().Deactivate(ctx, ws, "github", "2002", strings.Repeat("0", 30)+"a1", when()); err != nil {
		t.Fatal(err)
	}
	earlier := noopJob(t, ws, "2001", when().Add(-time.Minute))
	earlier.Key = "main"
	if err := s.Jobs().Enqueue(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	request := repository.JobRequest{Kind: "noop", Commit: strings.Repeat("c", 40), Ref: "refs/heads/main", Key: "main"}

	// A failure keeps nothing: neither the keys, the row, the jobs nor
	// the coalescing.
	k, row := deliveryRow("d1", "2001", repository.ResultRecorded)
	if _, err := s.Repositories().Record(ctx, k, row, []repository.JobRequest{request, {Kind: "check", Commit: request.Commit}}); !errors.Is(err, job.ErrUnknownKind) {
		t.Fatalf("Record with an unknown kind = %v", err)
	}
	if got := jobsOf(t, s, "d1"); len(got) != 0 {
		t.Errorf("a failed delivery kept jobs: %+v", got)
	}
	if got, _ := s.Jobs().Job(ctx, earlier.ID); got.State != job.StateQueued {
		t.Errorf("a failed delivery superseded a job: %+v", got)
	}
	if got, _ := s.Repositories().Log(ctx, "github", "2001", 10); len(got) != 0 {
		t.Errorf("a failed delivery kept its row: %+v", got)
	}

	// Sent again, it is recorded with its job, which supersedes the other.
	if logged, err := s.Repositories().Record(ctx, k, row, []repository.JobRequest{request}); err != nil || !logged {
		t.Fatalf("Record = %v, %v", logged, err)
	}
	got := jobsOf(t, s, "d1")
	if len(got) != 1 {
		t.Fatalf("jobs of d1: %+v", got)
	}
	if j := got[0]; j.Kind != job.KindNoop || j.Workspace != ws || j.Forge != "github" || j.Repository != "2001" || j.Commit != request.Commit ||
		j.Ref != request.Ref || j.Key != "main" || j.State != job.StateQueued || !j.CreatedAt.Equal(when()) {
		t.Errorf("the job of d1: %+v", j)
	}
	if got, _ := s.Jobs().Job(ctx, earlier.ID); got.State != job.StateCancelled || got.Reason != job.ReasonSuperseded {
		t.Errorf("the earlier job: %+v", got)
	}

	// An ignored delivery, or one of a repository that is not active,
	// creates none.
	k2, ignored := deliveryRow("d2", "2001", repository.ResultIgnored)
	k3, inactive := deliveryRow("d3", "2002", repository.ResultRecorded)
	for _, d := range []struct {
		k   repository.Key
		row *repository.LoggedEvent
	}{{k2, ignored}, {k3, inactive}} {
		if _, err := s.Repositories().Record(ctx, d.k, d.row, []repository.JobRequest{request}); err != nil {
			t.Fatal(err)
		}
		if got := jobsOf(t, s, d.k.Delivery); len(got) != 0 {
			t.Errorf("%s created %+v", d.k.Delivery, got)
		}
	}

	// The purge of the deliveries leaves the jobs.
	if _, err := s.Repositories().PurgeEvents(ctx, when().Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := jobsOf(t, s, "d1"); len(got) != 1 {
		t.Errorf("the purge deleted the jobs: %+v", got)
	}
}

// TestDeliveryJobsWakeTheClaims: the jobs a delivery queues in its own
// transaction tell the notifier once committed; a delivery that queues
// none, or fails, does not.
func TestDeliveryJobsWakeTheClaims(t *testing.T) {
	ctx := context.Background()
	var queued atomic.Int64
	s, err := Open(ctx, filepath.Join(t.TempDir(), "raun.db"), Options{Queued: func() { queued.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	ws := strings.Repeat("0", 30) + "w1"
	addRepository(t, s, ws, "github", "2001")
	request := repository.JobRequest{Kind: "noop", Commit: strings.Repeat("c", 40), Ref: "refs/heads/main"}
	for _, tt := range []struct {
		delivery string
		result   repository.Result
		requests []repository.JobRequest
		want     int64
	}{
		{"d1", repository.ResultRecorded, []repository.JobRequest{{Kind: "check", Commit: request.Commit}}, 0},
		{"d2", repository.ResultRecorded, nil, 0},
		{"d3", repository.ResultIgnored, []repository.JobRequest{request}, 0},
		{"d4", repository.ResultRecorded, []repository.JobRequest{request}, 1},
	} {
		before := queued.Load()
		k, row := deliveryRow(tt.delivery, "2001", tt.result)
		_, _ = s.Repositories().Record(ctx, k, row, tt.requests)
		if got := queued.Load() - before; got != tt.want {
			t.Errorf("%s told %d times, want %d", tt.delivery, got, tt.want)
		}
	}
}

// TestDeactivationCancelsTheJobs: deactivating a repository, or losing it
// to the App, cancels its queued and running jobs, and leaves the finished
// ones and those of other repositories.
func TestDeactivationCancelsTheJobs(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	ws := strings.Repeat("0", 30) + "w1"
	owner := strings.Repeat("0", 30) + "a1"
	for _, id := range []string{"2001", "2002", "2003"} {
		addRepository(t, s, ws, "github", id)
	}
	enqueue := func(repo string, at time.Time) job.Job {
		j := noopJob(t, ws, repo, at)
		if err := s.Jobs().Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	c := job.Claimant{Runner: "r", Capabilities: []job.Capability{job.CapabilityNoop}}
	t0 := when()
	finished := enqueue("2001", t0)
	f, err := s.Jobs().Claim(ctx, c, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs().Report(ctx, f.Run.ID, f.Token, job.Result{Outcome: job.OutcomeSucceeded}, t0); err != nil {
		t.Fatal(err)
	}
	running := enqueue("2001", t0.Add(time.Second))
	r, err := s.Jobs().Claim(ctx, c, t0.Add(time.Second))
	if err != nil || r.Job.ID != running.ID {
		t.Fatalf("Claim = %+v, %v", r, err)
	}
	queued := enqueue("2001", t0.Add(2*time.Second))
	other := enqueue("2002", t0.Add(3*time.Second))
	lost := enqueue("2003", t0.Add(4*time.Second))

	at := t0.Add(time.Minute)
	if err := s.Repositories().Deactivate(ctx, ws, "github", "2001", owner, at); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{running.ID, queued.ID} {
		if got, _ := s.Jobs().Job(ctx, id); got.State != job.StateCancelled || got.Reason != job.ReasonDeactivated || !got.FinishedAt.Equal(at) {
			t.Errorf("after the deactivation: %+v", got)
		}
	}
	if _, err := s.Jobs().Heartbeat(ctx, r.Run.ID, r.Token, at); !errors.Is(err, job.ErrCancelled) {
		t.Errorf("Heartbeat after the deactivation = %v", err)
	}
	if got, _ := s.Jobs().Job(ctx, finished.ID); got.State != job.StateSucceeded {
		t.Errorf("a finished job changed: %+v", got)
	}
	if got, _ := s.Jobs().Job(ctx, other.ID); got.State != job.StateQueued {
		t.Errorf("a job of another repository changed: %+v", got)
	}

	// The App loses 2003.
	if gone, err := s.Repositories().SetReach(ctx, "github", []string{"2001", "2002"}, at); err != nil || len(gone) != 1 || gone[0] != "2003" {
		t.Fatalf("SetReach = %v, %v", gone, err)
	}
	if got, _ := s.Jobs().Job(ctx, lost.ID); got.State != job.StateCancelled || got.Reason != job.ReasonDeactivated {
		t.Errorf("a job of a lost repository: %+v", got)
	}
	if got, _ := s.Jobs().Job(ctx, other.ID); got.State != job.StateQueued {
		t.Errorf("a job of a reached repository changed: %+v", got)
	}
}
