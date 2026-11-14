// Package jobtest holds the contract suite of the job stores, and an
// in-memory store that passes it.
package jobtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Memory is a job.Store in memory, safe for concurrent use. Its zero value
// is ready.
type Memory struct {
	mu   sync.Mutex
	jobs map[string]job.Job
	runs map[string]*storedRun
	// order holds the runs of each job, oldest first.
	order map[string][]string
	// runners are the runners by identifier, with the hash of their
	// token.
	runners    map[string]*storedRunner
	workspaces []job.Workspace
}

type storedRunner struct {
	runner job.Runner
	hash   string
}

type storedRun struct {
	run  job.Run
	hash string
}

var (
	_ job.Store       = (*Memory)(nil)
	_ job.RunnerStore = (*Memory)(nil)
)

func (m *Memory) init() {
	if m.jobs == nil {
		m.jobs, m.runs, m.order = map[string]job.Job{}, map[string]*storedRun{}, map[string][]string{}
		m.runners = map[string]*storedRunner{}
	}
}

// AddWorkspace makes w known, as the workspace store of the same data
// would.
func (m *Memory) AddWorkspace(w job.Workspace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.workspaces, w) {
		m.workspaces = append(m.workspaces, w)
	}
}

func (m *Memory) Enqueue(_ context.Context, j job.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if _, err := job.SpecOf(j.Kind); err != nil {
		return err
	}
	if _, ok := m.jobs[j.ID]; ok || j.ID == "" {
		return fmt.Errorf("%w: identifier %q", job.ErrInvalidJob, j.ID)
	}
	if j.Key != "" {
		for id, o := range m.jobs {
			if o.State == job.StateQueued && o.Workspace == j.Workspace && o.Key == j.Key {
				o.State, o.Reason, o.FinishedAt = job.StateCancelled, job.ReasonSuperseded, j.CreatedAt.UTC()
				m.jobs[id] = o
			}
		}
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *Memory) Claim(_ context.Context, c job.Claimant, now time.Time) (job.Claimed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.claim(c, now.UTC())
}

// claim applies the rules of Claim.
func (m *Memory) claim(c job.Claimant, now time.Time) (job.Claimed, error) {
	m.sweep(now)
	var candidates []job.Job
	for _, j := range m.jobs {
		if j.Claimable(now) && c.May(j) {
			candidates = append(candidates, j)
		}
	}
	if len(candidates) == 0 {
		return job.Claimed{}, job.ErrNoJob
	}
	j := slices.MinFunc(candidates, job.Before)
	run, token, hash, err := job.Start(rand.Reader, j, c, now)
	if err != nil {
		return job.Claimed{}, err
	}
	j.State = job.StateRunning
	m.jobs[j.ID] = j
	m.runs[run.ID] = &storedRun{run: run, hash: hash}
	m.order[j.ID] = append(m.order[j.ID], run.ID)
	return job.Claimed{Job: j, Run: run, Token: token}, nil
}

// lookup returns the run with this identifier when token is its own.
func (m *Memory) lookup(run, token string) (*storedRun, error) {
	r, ok := m.runs[run]
	if !ok || !job.TokenMatches(token, r.hash) {
		return nil, job.ErrStaleLease
	}
	if r.run.Outcome == job.OutcomeCancelled {
		return nil, job.ErrCancelled
	}
	return r, nil
}

func (m *Memory) Heartbeat(_ context.Context, run, token string, now time.Time) (job.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, err := m.lookup(run, token)
	if err != nil {
		return job.Run{}, err
	}
	if !r.run.Holds(now) {
		return job.Run{}, job.ErrStaleLease
	}
	r.run.LeaseExpires = now.UTC().Add(job.Lease)
	return r.run, nil
}

func (m *Memory) Report(_ context.Context, run, token string, res job.Result, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, err := m.lookup(run, token)
	if err != nil {
		return err
	}
	if !r.run.Open() {
		switch {
		case r.run.Result == nil:
			// Lost, or ended by its deadline or a refused result.
			return job.ErrStaleLease
		case r.run.Result.Same(res):
			return nil
		}
		return job.ErrReported
	}
	if !r.run.Holds(now) {
		return job.ErrStaleLease
	}
	now = now.UTC()
	j := m.jobs[r.run.Job]
	if err := job.CheckResult(j.Kind, res); err != nil {
		r.run.Outcome, r.run.EndedAt = job.OutcomeFailed, now
		j.State, j.Reason, j.FinishedAt = job.StateFailed, job.ReasonInvalidResult, now
		m.jobs[j.ID] = j
		return err
	}
	kept := job.Result{Outcome: res.Outcome, Summary: res.Summary, Data: slices.Clone(res.Data)}
	r.run.Outcome, r.run.EndedAt, r.run.Result = res.Outcome, now, &kept
	j.State, j.Reason, j.FinishedAt = job.StateSucceeded, job.ReasonNone, now
	if res.Outcome == job.OutcomeFailed {
		j.State = job.StateFailed
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *Memory) Release(_ context.Context, run, token string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, ok := m.runs[run]
	if !ok || !job.TokenMatches(token, r.hash) || !r.run.Open() {
		return job.ErrStaleLease
	}
	j := m.jobs[r.run.Job]
	delete(m.runs, run)
	m.order[j.ID] = slices.DeleteFunc(m.order[j.ID], func(id string) bool { return id == run })
	j.State, j.NotBefore = job.StateQueued, now.UTC().Add(job.MintDelay)
	m.jobs[j.ID] = j
	return nil
}

func (m *Memory) Cancel(_ context.Context, id string, reason job.Reason, now time.Time) (job.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	j, ok := m.jobs[id]
	if !ok {
		return job.Job{}, job.ErrNotFound
	}
	if j.State.Final() {
		return j, job.ErrFinal
	}
	return m.cancel(j, reason, now.UTC()), nil
}

// cancel cancels j, and its open run.
func (m *Memory) cancel(j job.Job, reason job.Reason, now time.Time) job.Job {
	for _, id := range m.order[j.ID] {
		if r := m.runs[id]; r.run.Open() {
			r.run.Outcome, r.run.EndedAt = job.OutcomeCancelled, now
		}
	}
	j.State, j.Reason, j.FinishedAt = job.StateCancelled, reason, now
	m.jobs[j.ID] = j
	return j
}

func (m *Memory) CancelRepository(_ context.Context, forge, repository string, reason job.Reason, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	n := 0
	for _, j := range m.jobs {
		if j.Forge == forge && j.Repository == repository && !j.State.Final() {
			m.cancel(j, reason, now.UTC())
			n++
		}
	}
	return n, nil
}

func (m *Memory) Sweep(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.sweep(now.UTC()), nil
}

// sweep applies the rules of Sweep, and returns how many jobs changed.
func (m *Memory) sweep(now time.Time) int {
	changed := 0
	// The deadline first: a run out of time fails, even when its lease
	// expired too.
	for _, r := range m.runs {
		if r.run.Open() && !now.Before(r.run.Deadline) {
			r.run.Outcome, r.run.EndedAt = job.OutcomeFailed, now
			j := m.jobs[r.run.Job]
			j.State, j.Reason, j.FinishedAt = job.StateFailed, job.ReasonTimeout, now
			m.jobs[j.ID] = j
			changed++
		}
	}
	for _, r := range m.runs {
		if r.run.Open() && !now.Before(r.run.LeaseExpires) {
			r.run.Outcome, r.run.EndedAt = job.OutcomeLost, now
			j := m.jobs[r.run.Job]
			j.State, j.Reason = job.AfterLost(len(m.order[j.ID]))
			if j.State.Final() {
				j.FinishedAt = now
			}
			m.jobs[j.ID] = j
			changed++
		}
	}
	for id, j := range m.jobs {
		if j.Expired(now) {
			j.State, j.Reason, j.FinishedAt = job.StateFailed, job.ReasonExpired, now
			m.jobs[id] = j
			changed++
		}
	}
	return changed
}

func (m *Memory) Job(_ context.Context, id string) (job.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	j, ok := m.jobs[id]
	if !ok {
		return job.Job{}, job.ErrNotFound
	}
	return j, nil
}

func (m *Memory) Runs(_ context.Context, id string) ([]job.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []job.Run
	for _, rid := range m.order[id] {
		r := m.runs[rid].run
		if r.Result != nil {
			res := *r.Result
			res.Data = slices.Clone(res.Data)
			r.Result = &res
		}
		out = append(out, r)
	}
	return out, nil
}

func cloneRunner(r job.Runner) job.Runner {
	r.Capabilities = slices.Clone(r.Capabilities)
	return r
}

func (m *Memory) CreateRunner(_ context.Context, r job.Runner, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if _, ok := m.runners[r.ID]; ok || r.ID == "" || len(hash) != 64 {
		return fmt.Errorf("%w: identifier %q", job.ErrInvalidRunner, r.ID)
	}
	if r.Workspace != "" && !slices.ContainsFunc(m.workspaces, func(w job.Workspace) bool { return w.ID == r.Workspace }) {
		return fmt.Errorf("%w: no workspace %q", job.ErrInvalidRunner, r.Workspace)
	}
	for _, o := range m.runners {
		if o.runner.Name == r.Name {
			return job.ErrRunnerNameTaken
		}
		if o.hash == hash {
			return fmt.Errorf("%w: a token of another runner", job.ErrInvalidRunner)
		}
	}
	m.runners[r.ID] = &storedRunner{runner: cloneRunner(r), hash: hash}
	return nil
}

func (m *Memory) Runner(_ context.Context, id string) (job.Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, ok := m.runners[id]
	if !ok {
		return job.Runner{}, job.ErrNoRunner
	}
	return cloneRunner(r.runner), nil
}

func (m *Memory) Runners(_ context.Context) ([]job.Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []job.Runner
	for _, r := range m.runners {
		out = append(out, cloneRunner(r.runner))
	}
	slices.SortFunc(out, func(a, b job.Runner) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *Memory) RotateRunner(_ context.Context, id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, ok := m.runners[id]
	switch {
	case !ok:
		return job.ErrNoRunner
	case r.runner.Revoked():
		return job.ErrRevoked
	}
	r.hash = hash
	return nil
}

func (m *Memory) RevokeRunner(_ context.Context, id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, ok := m.runners[id]
	if !ok {
		return 0, job.ErrNoRunner
	}
	if r.runner.Revoked() {
		return 0, nil
	}
	at = at.UTC()
	r.runner.RevokedAt = at
	changed := 0
	for _, run := range m.runs {
		if run.run.Open() && run.run.Runner == id {
			run.run.Outcome, run.run.EndedAt = job.OutcomeLost, at
			j := m.jobs[run.run.Job]
			j.State, j.Reason = job.AfterLost(len(m.order[j.ID]))
			if j.State.Final() {
				j.FinishedAt = at
			}
			m.jobs[j.ID] = j
			changed++
		}
	}
	return changed, nil
}

// authenticated returns the runner whose token has hash, when it is not
// revoked.
func (m *Memory) authenticated(hash string) (*storedRunner, error) {
	for _, r := range m.runners {
		if r.hash == hash && !r.runner.Revoked() {
			return r, nil
		}
	}
	return nil, job.ErrUnauthenticated
}

func (m *Memory) Authenticate(_ context.Context, hash string, now time.Time) (job.Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, err := m.authenticated(hash)
	if err != nil {
		return job.Runner{}, err
	}
	now = now.UTC()
	if !now.Before(r.runner.LastContact.Add(job.ContactEvery)) {
		r.runner.LastContact = now
	}
	return cloneRunner(r.runner), nil
}

func (m *Memory) ClaimFor(_ context.Context, runner string, now time.Time) (job.Claimed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r, ok := m.runners[runner]
	if !ok || r.runner.Revoked() {
		return job.Claimed{}, job.ErrUnauthenticated
	}
	return m.claim(r.runner.Claimant(), now.UTC())
}

func (m *Memory) Workspaces(_ context.Context) ([]job.Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.workspaces)
	slices.SortFunc(out, func(a, b job.Workspace) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
