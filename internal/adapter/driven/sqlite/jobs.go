package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Jobs is the job store (job.Store) on the database of a Store. Every
// operation is one write transaction, which the writer begins IMMEDIATE:
// two claims never read the queue at once, so they never take the same
// job.
type Jobs struct {
	s *Store
}

// Jobs returns the job store on the database of s.
func (s *Store) Jobs() Jobs {
	return Jobs{s: s}
}

var _ job.Store = Jobs{}

const jobColumns = `id, kind, workspace, forge, repository, commit_id, ref, delivery, coalesce_key, priority,
	state, reason, created_at, not_before, finished_at`

const runColumns = `id, job, runner, started_at, lease_expires_at, deadline, ended_at, outcome,
	result_outcome, result_summary, result_data`

func scanJob(row scanner) (job.Job, error) {
	var j job.Job
	var kind, state, reason, created, notBefore string
	var finished sql.NullString
	if err := row.Scan(&j.ID, &kind, &j.Workspace, &j.Forge, &j.Repository, &j.Commit, &j.Ref, &j.Delivery, &j.Key, &j.Priority,
		&state, &reason, &created, &notBefore, &finished); err != nil {
		return job.Job{}, err
	}
	j.Kind, j.State, j.Reason = job.Kind(kind), job.State(state), job.Reason(reason)
	var err error
	if j.CreatedAt, err = parseTime(created); err != nil {
		return job.Job{}, err
	}
	if j.NotBefore, err = parseTime(notBefore); err != nil {
		return job.Job{}, err
	}
	if finished.Valid {
		if j.FinishedAt, err = parseTime(finished.String); err != nil {
			return job.Job{}, err
		}
	}
	return j, nil
}

func scanRun(row scanner) (job.Run, error) {
	var r job.Run
	var started, lease, deadline string
	var ended, outcome, resultOutcome, summary sql.NullString
	var data []byte
	if err := row.Scan(&r.ID, &r.Job, &r.Runner, &started, &lease, &deadline, &ended, &outcome, &resultOutcome, &summary, &data); err != nil {
		return job.Run{}, err
	}
	var err error
	if r.StartedAt, err = parseTime(started); err != nil {
		return job.Run{}, err
	}
	if r.LeaseExpires, err = parseTime(lease); err != nil {
		return job.Run{}, err
	}
	if r.Deadline, err = parseTime(deadline); err != nil {
		return job.Run{}, err
	}
	if ended.Valid {
		if r.EndedAt, err = parseTime(ended.String); err != nil {
			return job.Run{}, err
		}
	}
	r.Outcome = job.Outcome(outcome.String)
	if resultOutcome.Valid {
		r.Result = &job.Result{Outcome: job.Outcome(resultOutcome.String), Summary: summary.String, Data: data}
	}
	return r, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

// insertJob stores j in tx, after it cancels as superseded the queued jobs
// of its workspace with its key. The delivery path calls it inside its own
// transaction.
func insertJob(ctx context.Context, tx *sql.Tx, j job.Job) error {
	if _, err := job.SpecOf(j.Kind); err != nil {
		return err
	}
	if j.Key != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'cancelled', reason = ?, finished_at = ?
			WHERE state = 'queued' AND workspace = ? AND coalesce_key = ?`,
			string(job.ReasonSuperseded), formatTime(j.CreatedAt), j.Workspace, j.Key); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, string(j.Kind), j.Workspace, j.Forge, j.Repository, j.Commit, j.Ref, j.Delivery, j.Key, j.Priority,
		string(j.State), string(j.Reason), formatTime(j.CreatedAt), formatTime(j.NotBefore), nullTime(j.FinishedAt))
	return err
}

func (s Jobs) Enqueue(ctx context.Context, j job.Job) error {
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		if err := insertJob(ctx, tx, j); err != nil {
			return fmt.Errorf("enqueue job: %w", err)
		}
		return nil
	})
	s.s.tellQueued(err == nil)
	return err
}

// sweep applies the rules of job.Store.Sweep in tx at now.
func sweep(ctx context.Context, tx *sql.Tx, now time.Time) (int, error) {
	at := formatTime(now)
	changed := 0
	// The deadline first: a run out of time fails, even when its lease
	// expired too.
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'failed', reason = ?, finished_at = ?
		WHERE id IN (SELECT job FROM runs WHERE outcome IS NULL AND deadline <= ?)`, string(job.ReasonTimeout), at, at)
	if err != nil {
		return 0, err
	}
	changed += affected(res)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = 'failed', ended_at = ? WHERE outcome IS NULL AND deadline <= ?`, at, at); err != nil {
		return 0, err
	}
	// Then the lost runs.
	n, err := loseRuns(ctx, tx, `lease_expires_at <= ?`, at, now)
	if err != nil {
		return 0, err
	}
	changed += n
	// Then the jobs queued too long.
	res, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'failed', reason = ?, finished_at = ?
		WHERE state = 'queued' AND created_at <= ?`, string(job.ReasonExpired), at, formatTime(now.Add(-job.QueueExpiry)))
	if err != nil {
		return 0, err
	}
	return changed + affected(res), nil
}

// loseRuns ends as lost at now the open runs of tx that where selects, a
// condition on the columns of runs with one argument arg, and queues each
// of their jobs again or fails it by AfterLost, from how many runs it has
// had. It returns how many jobs changed. The sweeper and the revocation of
// a runner share it, so a run is lost by one rule.
func loseRuns(ctx context.Context, tx *sql.Tx, where string, arg any, now time.Time) (int, error) {
	at := formatTime(now)
	rows, err := tx.QueryContext(ctx, `SELECT r.job, (SELECT count(*) FROM runs c WHERE c.job = r.job)
		FROM runs r WHERE r.outcome IS NULL AND r.`+where, arg)
	if err != nil {
		return 0, err
	}
	type lost struct {
		job  string
		runs int
	}
	var all []lost
	for rows.Next() {
		var l lost
		if err := rows.Scan(&l.job, &l.runs); err != nil {
			_ = rows.Close()
			return 0, err
		}
		all = append(all, l)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = 'lost', ended_at = ? WHERE outcome IS NULL AND `+where, at, arg); err != nil {
		return 0, err
	}
	for _, l := range all {
		state, reason := job.AfterLost(l.runs)
		var finished any
		if state.Final() {
			finished = at
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, reason = ?, finished_at = ? WHERE id = ?`,
			string(state), string(reason), finished, l.job); err != nil {
			return 0, err
		}
	}
	return len(all), nil
}

func affected(res sql.Result) int {
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return int(n)
}

func (s Jobs) Sweep(ctx context.Context, now time.Time) (int, error) {
	n := 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if n, err = sweep(ctx, tx, now.UTC()); err != nil {
			return fmt.Errorf("sweep jobs: %w", err)
		}
		return nil
	})
	s.s.tellQueued(err == nil && n > 0)
	return n, err
}

func (s Jobs) Claim(ctx context.Context, c job.Claimant, now time.Time) (job.Claimed, error) {
	now = now.UTC()
	var out job.Claimed
	found, swept := false, 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		var err error
		out, found, swept, err = claimIn(ctx, tx, c, now)
		return err
	})
	if err != nil {
		return job.Claimed{}, err
	}
	s.s.tellQueued(swept > 0)
	if !found {
		return job.Claimed{}, job.ErrNoJob
	}
	return out, nil
}

// claimIn applies the rules of job.Store.Claim in tx: it sweeps, then
// starts the run of the first job c may take, and reports whether there was
// one, and how many jobs the sweep changed. What the sweep ended is kept
// either way.
func claimIn(ctx context.Context, tx *sql.Tx, c job.Claimant, now time.Time) (job.Claimed, bool, int, error) {
	swept, err := sweep(ctx, tx, now)
	if err != nil {
		return job.Claimed{}, false, 0, fmt.Errorf("claim job: %w", err)
	}
	kinds := job.KindsFor(c.Capabilities)
	if len(kinds) == 0 {
		return job.Claimed{}, false, swept, nil
	}
	query := `SELECT ` + jobColumns + ` FROM jobs WHERE state = 'queued' AND not_before <= ? AND kind IN (?` +
		strings.Repeat(", ?", len(kinds)-1) + `)`
	args := []any{formatTime(now)}
	for _, k := range kinds {
		args = append(args, string(k))
	}
	if c.Workspace != "" {
		query += ` AND workspace = ?`
		args = append(args, c.Workspace)
	}
	query += ` ORDER BY priority, created_at, id LIMIT 1`
	j, err := scanJob(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return job.Claimed{}, false, swept, nil
	}
	if err != nil {
		return job.Claimed{}, false, swept, fmt.Errorf("claim job: %w", err)
	}
	run, token, hash, err := job.Start(rand.Reader, j, c, now)
	if err != nil {
		return job.Claimed{}, false, swept, fmt.Errorf("claim job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, job, runner, token_hash, started_at, lease_expires_at, deadline)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, run.ID, run.Job, run.Runner, hash,
		formatTime(run.StartedAt), formatTime(run.LeaseExpires), formatTime(run.Deadline)); err != nil {
		return job.Claimed{}, false, swept, fmt.Errorf("claim job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'running' WHERE id = ?`, j.ID); err != nil {
		return job.Claimed{}, false, swept, fmt.Errorf("claim job: %w", err)
	}
	j.State = job.StateRunning
	return job.Claimed{Job: j, Run: run, Token: token}, true, swept, nil
}

// lookup returns the run with this identifier in tx when token is its own;
// ErrStaleLease otherwise, and ErrCancelled when the run was cancelled.
func lookup(ctx context.Context, tx *sql.Tx, id, token string) (job.Run, error) {
	var hash string
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return job.Run{}, job.ErrStaleLease
	}
	if err != nil {
		return job.Run{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT token_hash FROM runs WHERE id = ?`, id).Scan(&hash); err != nil {
		return job.Run{}, err
	}
	if !job.TokenMatches(token, hash) {
		return job.Run{}, job.ErrStaleLease
	}
	if r.Outcome == job.OutcomeCancelled {
		return job.Run{}, job.ErrCancelled
	}
	return r, nil
}

func (s Jobs) Heartbeat(ctx context.Context, run, token string, now time.Time) (job.Run, error) {
	now = now.UTC()
	var out job.Run
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		r, err := lookup(ctx, tx, run, token)
		if err != nil {
			return err
		}
		if !r.Holds(now) {
			return job.ErrStaleLease
		}
		r.LeaseExpires = now.Add(job.Lease)
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET lease_expires_at = ? WHERE id = ?`, formatTime(r.LeaseExpires), r.ID); err != nil {
			return fmt.Errorf("renew lease: %w", err)
		}
		out = r
		return nil
	})
	return out, err
}

func (s Jobs) Report(ctx context.Context, run, token string, res job.Result, now time.Time) error {
	now = now.UTC()
	var refused error
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		refused = nil
		r, err := lookup(ctx, tx, run, token)
		if err != nil {
			return err
		}
		if !r.Open() {
			switch {
			case r.Result == nil:
				// Lost, or ended by its deadline or a refused result.
				return job.ErrStaleLease
			case r.Result.Same(res):
				return nil
			}
			return job.ErrReported
		}
		if !r.Holds(now) {
			return job.ErrStaleLease
		}
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM jobs WHERE id = ?`, r.Job).Scan(&kind); err != nil {
			return fmt.Errorf("report result: %w", err)
		}
		at := formatTime(now)
		if err := job.CheckResult(job.Kind(kind), res); err != nil {
			// The failure is kept, the result is not.
			refused = err
			if _, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = 'failed', ended_at = ? WHERE id = ?`, at, r.ID); err != nil {
				return fmt.Errorf("report result: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'failed', reason = ?, finished_at = ? WHERE id = ?`,
				string(job.ReasonInvalidResult), at, r.Job); err != nil {
				return fmt.Errorf("report result: %w", err)
			}
			return nil
		}
		data := res.Data
		if data == nil {
			data = []byte{}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = ?, ended_at = ?, result_outcome = ?, result_summary = ?, result_data = ?
			WHERE id = ?`, string(res.Outcome), at, string(res.Outcome), res.Summary, data, r.ID); err != nil {
			return fmt.Errorf("report result: %w", err)
		}
		state := job.StateSucceeded
		if res.Outcome == job.OutcomeFailed {
			state = job.StateFailed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, reason = '', finished_at = ? WHERE id = ?`, string(state), at, r.Job); err != nil {
			return fmt.Errorf("report result: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return refused
}

func (s Jobs) Release(ctx context.Context, run, token string, now time.Time) error {
	now = now.UTC()
	return s.s.write(ctx, func(tx *sql.Tx) error {
		r, err := lookup(ctx, tx, run, token)
		if errors.Is(err, job.ErrCancelled) {
			return job.ErrStaleLease
		}
		if err != nil {
			return err
		}
		if !r.Open() {
			return job.ErrStaleLease
		}
		// Its log, if any, goes with it (ON DELETE CASCADE).
		if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, r.ID); err != nil {
			return fmt.Errorf("release run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued', not_before = ? WHERE id = ? AND state = 'running'`,
			formatTime(now.Add(job.MintDelay)), r.Job); err != nil {
			return fmt.Errorf("release run: %w", err)
		}
		return nil
	})
}

// cancelJobs cancels for reason at now, in tx, the jobs that are not final
// among those where selects, and their open runs, and returns how many it
// cancelled. where is a condition on the columns of jobs.
func cancelJobs(ctx context.Context, tx *sql.Tx, where string, args []any, reason job.Reason, now time.Time) (int, error) {
	at := formatTime(now)
	selected := `SELECT id FROM jobs WHERE state IN ('queued', 'running') AND ` + where
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = 'cancelled', ended_at = ? WHERE outcome IS NULL AND job IN (`+selected+`)`,
		append([]any{at}, args...)...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'cancelled', reason = ?, finished_at = ?
		WHERE state IN ('queued', 'running') AND `+where, append([]any{string(reason), at}, args...)...)
	if err != nil {
		return 0, err
	}
	return affected(res), nil
}

// cancelRepositoryJobs cancels in tx the queued and running jobs of the
// repositories of forge with these identifiers. Deactivating a repository,
// or losing it to the App, calls it inside its own transaction.
func cancelRepositoryJobs(ctx context.Context, tx *sql.Tx, forge string, ids []string, reason job.Reason, now time.Time) (int, error) {
	n := 0
	for _, id := range ids {
		c, err := cancelJobs(ctx, tx, `forge = ? AND repository = ?`, []any{forge, id}, reason, now)
		if err != nil {
			return 0, err
		}
		n += c
	}
	return n, nil
}

func (s Jobs) Cancel(ctx context.Context, id string, reason job.Reason, now time.Time) (job.Job, error) {
	now = now.UTC()
	var out job.Job
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return job.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("cancel job: %w", err)
		}
		if j.State.Final() {
			out = j
			return job.ErrFinal
		}
		if _, err := cancelJobs(ctx, tx, `id = ?`, []any{id}, reason, now); err != nil {
			return fmt.Errorf("cancel job: %w", err)
		}
		j.State, j.Reason, j.FinishedAt = job.StateCancelled, reason, now
		out = j
		return nil
	})
	return out, err
}

func (s Jobs) CancelRepository(ctx context.Context, forge, repo string, reason job.Reason, now time.Time) (int, error) {
	n := 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if n, err = cancelRepositoryJobs(ctx, tx, forge, []string{repo}, reason, now.UTC()); err != nil {
			return fmt.Errorf("cancel the jobs of a repository: %w", err)
		}
		return nil
	})
	return n, err
}

func (s Jobs) Job(ctx context.Context, id string) (job.Job, error) {
	var out job.Job
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return job.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read job: %w", err)
		}
		out = j
		return nil
	})
	return out, err
}

func (s Jobs) Runs(ctx context.Context, id string) ([]job.Run, error) {
	var out []job.Run
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE job = ? ORDER BY started_at, id`, id)
		if err != nil {
			return fmt.Errorf("read runs: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return fmt.Errorf("read runs: %w", err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s Jobs) Run(ctx context.Context, id string) (job.Run, error) {
	var out job.Run
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return job.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read run: %w", err)
		}
		out = r
		return nil
	})
	return out, err
}

// logIn returns the log of the run with this identifier in tx.
func logIn(ctx context.Context, tx *sql.Tx, run string) (job.Log, error) {
	var l job.Log
	var cut int
	err := tx.QueryRowContext(ctx, `SELECT next_seq, cut, text FROM run_logs WHERE run = ?`, run).Scan(&l.Next, &cut, &l.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return job.Log{}, nil
	}
	if err != nil {
		return job.Log{}, err
	}
	l.Cut = cut == 1
	return l, nil
}

func (s Jobs) AppendLog(ctx context.Context, run, token string, seq int, text string, now time.Time) error {
	// Refused before the write lock is taken.
	if err := job.CheckChunk(seq, text); err != nil {
		return err
	}
	now = now.UTC()
	return s.s.write(ctx, func(tx *sql.Tx) error {
		r, err := lookup(ctx, tx, run, token)
		if err != nil {
			return err
		}
		if !r.Holds(now) {
			return job.ErrStaleLease
		}
		l, err := logIn(ctx, tx, run)
		if err != nil {
			return fmt.Errorf("append log: %w", err)
		}
		next, err := l.Append(seq, text)
		if err != nil || next.Next == l.Next {
			return err
		}
		cut := 0
		if next.Cut {
			cut = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_logs (run, next_seq, cut, text) VALUES (?, ?, ?, ?)
			ON CONFLICT (run) DO UPDATE SET next_seq = excluded.next_seq, cut = excluded.cut, text = excluded.text`,
			run, next.Next, cut, next.Text); err != nil {
			return fmt.Errorf("append log: %w", err)
		}
		return nil
	})
}

func (s Jobs) Log(ctx context.Context, run string) (job.Log, error) {
	var out job.Log
	err := s.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		if out, err = logIn(ctx, tx, run); err != nil {
			return fmt.Errorf("read log: %w", err)
		}
		return nil
	})
	return out, err
}

func (s Jobs) PurgeLogs(ctx context.Context, before time.Time) (int, error) {
	n := 0
	err := s.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM run_logs WHERE run IN
			(SELECT id FROM runs WHERE ended_at IS NOT NULL AND ended_at < ?)`, formatTime(before.UTC()))
		if err != nil {
			return fmt.Errorf("purge logs: %w", err)
		}
		n = affected(res)
		return nil
	})
	return n, err
}
