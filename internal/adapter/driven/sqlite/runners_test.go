package sqlite

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

func newRunner(t *testing.T, s *Store, name string) (job.Runner, string) {
	t.Helper()
	r, token, hash, err := job.NewRunner(rand.Reader, job.RunnerRequest{Name: name, Capabilities: []job.Capability{job.CapabilityNoop}, CreatedBy: strings.Repeat("e", 32)}, when())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Runners().CreateRunner(context.Background(), r, hash); err != nil {
		t.Fatal(err)
	}
	return r, token
}

// TestRunnerTokensAreStoredHashed finds only the hash of the token of a
// runner in the database.
func TestRunnerTokensAreStoredHashed(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	r, token := newRunner(t, s, "builder")
	var stored string
	if err := s.reader.QueryRowContext(ctx, `SELECT token_hash FROM runners WHERE id = ?`, r.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != job.HashToken(token) || strings.Contains(stored, token) {
		t.Errorf("stored %q for the token", stored)
	}
}

// TestNoClaimAfterARevocation revokes a runner while it claims through
// another store on the same file: once the revocation has committed, no
// claim of the runner starts a run, and every run it started before is
// lost.
func TestNoClaimAfterARevocation(t *testing.T) {
	ctx := context.Background()
	a, path := openMigrated(t)
	b := openAt(t, path)
	ws := strings.Repeat("0", 30) + "w1"
	addRepository(t, a, ws, "github", "2001")
	r, _ := newRunner(t, a, "builder")
	t0 := when()
	for i := range 30 {
		if err := a.Jobs().Enqueue(ctx, noopJob(t, ws, "2001", t0.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var claimed []job.Claimed
	stop, first := make(chan struct{}), make(chan struct{})
	var once sync.Once
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, err := b.Runners().ClaimFor(ctx, r.ID, t0.Add(time.Second))
				if errors.Is(err, job.ErrUnauthenticated) || errors.Is(err, job.ErrNoJob) {
					return
				}
				if err != nil {
					t.Errorf("ClaimFor: %v", err)
					return
				}
				mu.Lock()
				claimed = append(claimed, c)
				mu.Unlock()
				once.Do(func() { close(first) })
			}
		}()
	}
	// Revoke once claims are under way.
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("no claim")
	}
	if _, err := a.Runners().RevokeRunner(ctx, r.ID, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	if _, err := b.Runners().ClaimFor(ctx, r.ID, t0.Add(2*time.Second)); !errors.Is(err, job.ErrUnauthenticated) {
		t.Errorf("ClaimFor after the revocation = %v", err)
	}
	for _, c := range claimed {
		runs, err := a.Jobs().Runs(ctx, c.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Open() {
				t.Errorf("run %s of the revoked runner is still open", run.ID)
			}
		}
	}
}
