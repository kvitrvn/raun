package job

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The timings and bounds of the queue (epic 6 record).
const (
	// Lease is how long a run holds its job without a heartbeat.
	Lease = 60 * time.Second
	// HeartbeatEvery is how often a runner renews the lease of a run.
	HeartbeatEvery = 15 * time.Second
	// SweepEvery is how often the server ends the runs whose lease or
	// deadline has passed, and the jobs queued too long.
	SweepEvery = 15 * time.Second
	// MaxRuns is how many runs a job has at most: the lost ones are
	// retried, twice at most.
	MaxRuns = 3
	// QueueExpiry is how long after its creation a job may wait in the
	// queue; a job sent back to the queue keeps its creation time.
	QueueExpiry = 24 * time.Hour
	// MintDelay is how long a job waits before it may be claimed again,
	// after the clone credential of its run could not be minted.
	MintDelay = 30 * time.Second
	// MaxSummary bounds the summary of a result, in bytes.
	MaxSummary = 1024
)

var (
	// ErrUnknownKind: no kind has this name.
	ErrUnknownKind = errors.New("unknown kind of job")
	// ErrInvalidJob: a request for a job lacks a field or holds one that
	// is malformed.
	ErrInvalidJob = errors.New("invalid job")
	// ErrNotFound: no job has this identifier.
	ErrNotFound = errors.New("job not found")
	// ErrNoJob: no job may be claimed now by this runner.
	ErrNoJob = errors.New("no job to claim")
	// ErrStaleLease: the run is unknown, its token does not match, or it
	// is no longer the current run of its job holding its lease.
	ErrStaleLease = errors.New("the run is no longer current")
	// ErrCancelled: the job of the run was cancelled.
	ErrCancelled = errors.New("the job was cancelled")
	// ErrReported: the run already reported another result.
	ErrReported = errors.New("the run already reported another result")
	// ErrInvalidResult: the result is not one its kind accepts; the job
	// failed.
	ErrInvalidResult = errors.New("invalid result")
	// ErrFinal: the job has already ended.
	ErrFinal = errors.New("the job has already ended")
)

// Kind is what a job does.
type Kind string

// KindNoop does nothing: the tests of the queue, the runner and the
// end-to-end path run it. The check comes with epic 7, the analysis with
// epic 10.
const KindNoop Kind = "noop"

// Capability is what a runner may run: the administrator gives it, the
// runner never declares it.
type Capability string

// CapabilityNoop runs noop jobs.
const CapabilityNoop Capability = "noop"

// Spec is what a kind declares.
type Spec struct {
	// Capability is what a runner needs to take a job of the kind.
	Capability Capability
	// MaxDuration is how long a run may last before it fails.
	MaxDuration time.Duration
	// MaxResult bounds the data of a result, in bytes.
	MaxResult int
	// Priority orders the queue: a lower one is claimed first; a check
	// comes before an analysis.
	Priority int
	// check accepts the data of a result; nil accepts any JSON value.
	check func(data []byte) error
}

// kinds are the kinds of job, read-only.
var kinds = map[Kind]Spec{
	KindNoop: {Capability: CapabilityNoop, MaxDuration: 5 * time.Minute, MaxResult: 4 << 10, Priority: 0, check: noopData},
}

// noopData accepts no data, or an empty object.
func noopData(data []byte) error {
	if len(data) == 0 || string(bytes.TrimSpace(data)) == "{}" {
		return nil
	}
	return errors.New("a noop result carries no data")
}

// SpecOf returns what kind declares; ErrUnknownKind for no kind.
func SpecOf(kind Kind) (Spec, error) {
	s, ok := kinds[kind]
	if !ok {
		return Spec{}, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	return s, nil
}

// KindsFor returns the kinds a runner with capabilities may take, sorted.
func KindsFor(capabilities []Capability) []Kind {
	var out []Kind
	for k, s := range kinds {
		if slices.Contains(capabilities, s.Capability) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// State is where a job stands.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Final reports whether s is an end: succeeded, failed or cancelled.
func (s State) Final() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// Outcome is how a run ended; empty while it runs.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	// OutcomeLost: its lease expired, or its runner was revoked.
	OutcomeLost      Outcome = "lost"
	OutcomeCancelled Outcome = "cancelled"
)

// Reason says why a job ended as it did, when its runner did not say it.
type Reason string

const (
	// ReasonNone: the job ended as its run reported.
	ReasonNone Reason = ""
	// ReasonSuperseded: a newer job of the same key replaced it in the
	// queue.
	ReasonSuperseded Reason = "superseded"
	// ReasonCancelled: a person cancelled it.
	ReasonCancelled Reason = "cancelled"
	// ReasonDeactivated: its repository was deactivated, or lost by the
	// App.
	ReasonDeactivated Reason = "deactivated"
	// ReasonExpired: it waited in the queue past QueueExpiry.
	ReasonExpired Reason = "expired"
	// ReasonTimeout: its run passed the maximum duration of its kind.
	ReasonTimeout Reason = "timeout"
	// ReasonLost: MaxRuns of its runs were lost.
	ReasonLost Reason = "lost"
	// ReasonInvalidResult: its run reported a result its kind refuses.
	ReasonInvalidResult Reason = "invalid-result"
)

// Request is what a job is asked for.
type Request struct {
	Kind Kind
	// Workspace is the identifier of the workspace of the repository.
	Workspace string
	// Forge and Repository identify the repository: the forge and its
	// stable identifier.
	Forge      string
	Repository string
	// Commit is the commit the job works on; Ref the ref it came from,
	// empty when none.
	Commit string
	Ref    string
	// Delivery is the identifier the forge gave the delivery that caused
	// the job, on the same forge; empty for a job asked by hand.
	Delivery string
	// Key is the coalescing key: a job with a key replaces the queued
	// jobs of the same workspace with the same key. Empty for none.
	Key string
}

// Job is a unit of the queue: what must be done, on which commit.
type Job struct {
	ID         string
	Kind       Kind
	Workspace  string
	Forge      string
	Repository string
	Commit     string
	Ref        string
	Delivery   string
	Key        string
	Priority   int
	State      State
	Reason     Reason
	CreatedAt  time.Time
	// NotBefore is the time before which the job may not be claimed.
	NotBefore time.Time
	// FinishedAt is when the job ended; zero until then.
	FinishedAt time.Time
}

// Run is one attempt of a job by one runner.
type Run struct {
	ID     string
	Job    string
	Runner string
	// StartedAt is when the runner claimed the job.
	StartedAt time.Time
	// LeaseExpires is when the run loses its job without a heartbeat.
	LeaseExpires time.Time
	// Deadline is when the run fails, by the maximum duration of its kind.
	Deadline time.Time
	// EndedAt is when the run ended; zero while it runs.
	EndedAt time.Time
	Outcome Outcome
	// Result is what the run reported; nil when it reported nothing
	// accepted.
	Result *Result
}

// Open reports whether r still runs.
func (r Run) Open() bool { return r.Outcome == "" }

// Holds reports whether r runs and holds its lease at now, before its
// deadline.
func (r Run) Holds(now time.Time) bool {
	return r.Open() && now.Before(r.LeaseExpires) && now.Before(r.Deadline)
}

// Result is what a run reports.
type Result struct {
	// Outcome is succeeded or failed.
	Outcome Outcome
	// Summary is one line for a person, MaxSummary bytes at most.
	Summary string
	// Data is the JSON the kind reads, bounded by its MaxResult.
	Data []byte
}

// Same reports whether r and o are the same result.
func (r Result) Same(o Result) bool {
	return r.Outcome == o.Outcome && r.Summary == o.Summary && bytes.Equal(r.Data, o.Data)
}

// CheckResult reports whether a job of kind accepts r; its errors wrap
// ErrInvalidResult and say what is wrong.
func CheckResult(kind Kind, r Result) error {
	s, err := SpecOf(kind)
	if err != nil {
		return err
	}
	switch {
	case r.Outcome != OutcomeSucceeded && r.Outcome != OutcomeFailed:
		return fmt.Errorf("%w: outcome %q is not succeeded or failed", ErrInvalidResult, r.Outcome)
	case len(r.Summary) > MaxSummary:
		return fmt.Errorf("%w: the summary holds %d bytes, past %d", ErrInvalidResult, len(r.Summary), MaxSummary)
	case !utf8.ValidString(r.Summary) || strings.ContainsFunc(r.Summary, isControl):
		return fmt.Errorf("%w: the summary is not one line of text", ErrInvalidResult)
	case len(r.Data) > s.MaxResult:
		return fmt.Errorf("%w: the data holds %d bytes, past %d", ErrInvalidResult, len(r.Data), s.MaxResult)
	case len(r.Data) > 0 && !json.Valid(r.Data):
		return fmt.Errorf("%w: the data is not JSON", ErrInvalidResult)
	}
	if s.check != nil {
		if err := s.check(r.Data); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidResult, err)
		}
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Claimant is a runner that asks for a job.
type Claimant struct {
	// Runner is the identifier of the runner.
	Runner string
	// Workspace is the identifier of the workspace the runner serves;
	// empty for a runner of the instance.
	Workspace string
	// Capabilities are the kinds of work the administrator gave it.
	Capabilities []Capability
}

// May reports whether c may take j: its scope covers the workspace of j,
// and it has the capability of its kind.
func (c Claimant) May(j Job) bool {
	s, err := SpecOf(j.Kind)
	return err == nil && (c.Workspace == "" || c.Workspace == j.Workspace) && slices.Contains(c.Capabilities, s.Capability)
}

// Claimable reports whether j may be claimed at now: queued, and its
// NotBefore passed.
func (j Job) Claimable(now time.Time) bool {
	return j.State == StateQueued && !now.Before(j.NotBefore)
}

// Expired reports whether j has waited in the queue past QueueExpiry at
// now.
func (j Job) Expired(now time.Time) bool {
	return j.State == StateQueued && !now.Before(j.CreatedAt.Add(QueueExpiry))
}

// Before orders the queue: the lower priority first, then the oldest, then
// the identifier.
func Before(a, b Job) int {
	if a.Priority != b.Priority {
		return a.Priority - b.Priority
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// AfterLost returns what a job becomes once one of its runs is lost, when
// it has had runs runs: queued again while it has had fewer than MaxRuns,
// failed as lost otherwise.
func AfterLost(runs int) (State, Reason) {
	if runs < MaxRuns {
		return StateQueued, ReasonNone
	}
	return StateFailed, ReasonLost
}

// Claimed is a job given to a runner: its run, and the lease token of the
// run, which only the runner holds; the store keeps its hash.
type Claimed struct {
	Job   Job
	Run   Run
	Token string
}

// New returns the job asked for by r at at, queued, with an identifier drawn
// from random. Errors wrap ErrUnknownKind or ErrInvalidJob and name the
// field.
func New(random io.Reader, r Request, at time.Time) (Job, error) {
	s, err := SpecOf(r.Kind)
	if err != nil {
		return Job{}, err
	}
	switch {
	case r.Workspace == "":
		return Job{}, fmt.Errorf("%w: no workspace", ErrInvalidJob)
	case r.Forge == "" || r.Repository == "":
		return Job{}, fmt.Errorf("%w: no repository", ErrInvalidJob)
	case !isCommit(r.Commit):
		return Job{}, fmt.Errorf("%w: commit %q is not 40 or 64 hexadecimal digits", ErrInvalidJob, r.Commit)
	case at.IsZero():
		return Job{}, fmt.Errorf("%w: no time of creation", ErrInvalidJob)
	}
	id, err := NewID(random)
	if err != nil {
		return Job{}, err
	}
	at = at.UTC()
	return Job{
		ID: id, Kind: r.Kind, Workspace: r.Workspace, Forge: r.Forge, Repository: r.Repository,
		Commit: r.Commit, Ref: r.Ref, Delivery: r.Delivery, Key: r.Key, Priority: s.Priority,
		State: StateQueued, CreatedAt: at, NotBefore: at,
	}, nil
}

// Start returns the run of j that c starts at now, and its lease token,
// both drawn from random.
func Start(random io.Reader, j Job, c Claimant, now time.Time) (Run, string, string, error) {
	s, err := SpecOf(j.Kind)
	if err != nil {
		return Run{}, "", "", err
	}
	id, err := NewID(random)
	if err != nil {
		return Run{}, "", "", err
	}
	token, hash, err := NewToken(random)
	if err != nil {
		return Run{}, "", "", err
	}
	now = now.UTC()
	return Run{
		ID: id, Job: j.ID, Runner: c.Runner, StartedAt: now,
		LeaseExpires: now.Add(Lease), Deadline: now.Add(s.MaxDuration),
	}, token, hash, nil
}

func isCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// NewID draws the identifier of a job or a run from random: 32 hexadecimal
// digits.
func NewID(random io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", fmt.Errorf("draw an identifier: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewToken draws a lease token from random, 32 bytes in hexadecimal, and
// returns it with its hash, which is all a store keeps.
func NewToken(random io.Reader) (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", "", fmt.Errorf("draw a lease token: %w", err)
	}
	token = hex.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of token in hexadecimal.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches reports whether token has hash, in constant time.
func TokenMatches(token, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(hash)) == 1
}
