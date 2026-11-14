package job

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func request() Request {
	return Request{Kind: KindNoop, Workspace: strings.Repeat("1", 32), Forge: "github", Repository: "2001", Commit: strings.Repeat("c", 40)}
}

func TestNew(t *testing.T) {
	j, err := New(rand.Reader, request(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.ID) != 32 || j.State != StateQueued || j.CreatedAt != t0 || j.NotBefore != t0 || !j.FinishedAt.IsZero() || j.Priority != 0 {
		t.Errorf("New = %+v", j)
	}
	for name, tc := range map[string]struct {
		change func(*Request)
		at     time.Time
		want   error
	}{
		"unknown kind":     {func(r *Request) { r.Kind = "check" }, t0, ErrUnknownKind},
		"no workspace":     {func(r *Request) { r.Workspace = "" }, t0, ErrInvalidJob},
		"no repository":    {func(r *Request) { r.Repository = "" }, t0, ErrInvalidJob},
		"no forge":         {func(r *Request) { r.Forge = "" }, t0, ErrInvalidJob},
		"short commit":     {func(r *Request) { r.Commit = "abc" }, t0, ErrInvalidJob},
		"uppercase commit": {func(r *Request) { r.Commit = strings.Repeat("C", 40) }, t0, ErrInvalidJob},
		"no time":          {func(*Request) {}, time.Time{}, ErrInvalidJob},
	} {
		r := request()
		tc.change(&r)
		if _, err := New(rand.Reader, r, tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: New = %v, want %v", name, err, tc.want)
		}
	}
	r := request()
	r.Commit = strings.Repeat("a", 64)
	if _, err := New(rand.Reader, r, t0); err != nil {
		t.Errorf("a SHA-256 commit: %v", err)
	}
	if _, err := New(bytes.NewReader(nil), request(), t0); err == nil {
		t.Error("New with no randomness succeeded")
	}
}

func TestKinds(t *testing.T) {
	s, err := SpecOf(KindNoop)
	if err != nil || s.Capability != CapabilityNoop || s.MaxDuration != 5*time.Minute || s.MaxResult != 4<<10 {
		t.Errorf("SpecOf(noop) = %+v, %v", s, err)
	}
	if _, err := SpecOf("analysis"); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("SpecOf(analysis) = %v", err)
	}
	if got := KindsFor([]Capability{CapabilityNoop, "agents"}); len(got) != 1 || got[0] != KindNoop {
		t.Errorf("KindsFor = %v", got)
	}
	if got := KindsFor(nil); len(got) != 0 {
		t.Errorf("KindsFor(nil) = %v", got)
	}
}

func TestStates(t *testing.T) {
	for s, final := range map[State]bool{StateQueued: false, StateRunning: false, StateSucceeded: true, StateFailed: true, StateCancelled: true} {
		if s.Final() != final {
			t.Errorf("%s.Final() = %v", s, !final)
		}
	}
	for runs, want := range map[int]State{1: StateQueued, 2: StateQueued, 3: StateFailed} {
		if s, r := AfterLost(runs); s != want || (s == StateFailed) != (r == ReasonLost) {
			t.Errorf("AfterLost(%d) = %s, %s", runs, s, r)
		}
	}
}

func TestCheckResult(t *testing.T) {
	for name, tc := range map[string]struct {
		r    Result
		want bool
	}{
		"success":            {Result{Outcome: OutcomeSucceeded, Summary: "done"}, true},
		"failure":            {Result{Outcome: OutcomeFailed, Summary: "the commit could not be fetched"}, true},
		"empty object":       {Result{Outcome: OutcomeSucceeded, Data: []byte(" {} ")}, true},
		"lost":               {Result{Outcome: OutcomeLost}, false},
		"no outcome":         {Result{}, false},
		"data for noop":      {Result{Outcome: OutcomeSucceeded, Data: []byte(`{"a":1}`)}, false},
		"not JSON":           {Result{Outcome: OutcomeSucceeded, Data: []byte(`{`)}, false},
		"too much data":      {Result{Outcome: OutcomeSucceeded, Data: append([]byte(`"`), append(bytes.Repeat([]byte("a"), 4<<10), '"')...)}, false},
		"two lines":          {Result{Outcome: OutcomeSucceeded, Summary: "a\nb"}, false},
		"invalid UTF-8":      {Result{Outcome: OutcomeSucceeded, Summary: "\xff"}, false},
		"long summary":       {Result{Outcome: OutcomeSucceeded, Summary: strings.Repeat("a", MaxSummary+1)}, false},
		"summary at the cap": {Result{Outcome: OutcomeSucceeded, Summary: strings.Repeat("a", MaxSummary)}, true},
	} {
		err := CheckResult(KindNoop, tc.r)
		if (err == nil) != tc.want || (err != nil && !errors.Is(err, ErrInvalidResult)) {
			t.Errorf("%s: CheckResult = %v", name, err)
		}
	}
	if err := CheckResult("check", Result{Outcome: OutcomeSucceeded}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("CheckResult of an unknown kind = %v", err)
	}
}

func TestTokens(t *testing.T) {
	token, hash, err := NewToken(rand.Reader)
	if err != nil || len(token) != 64 || len(hash) != 64 || token == hash {
		t.Fatalf("NewToken = %q, %q, %v", token, hash, err)
	}
	if !TokenMatches(token, hash) {
		t.Error("a token does not match its hash")
	}
	other, _, _ := NewToken(rand.Reader)
	if TokenMatches(other, hash) || TokenMatches("", hash) || TokenMatches(hash, hash) {
		t.Error("another token matches")
	}
}

func TestClaimant(t *testing.T) {
	j, _ := New(rand.Reader, request(), t0)
	for name, tc := range map[string]struct {
		c    Claimant
		want bool
	}{
		"instance":           {Claimant{Capabilities: []Capability{CapabilityNoop}}, true},
		"its workspace":      {Claimant{Workspace: j.Workspace, Capabilities: []Capability{CapabilityNoop}}, true},
		"another workspace":  {Claimant{Workspace: strings.Repeat("2", 32), Capabilities: []Capability{CapabilityNoop}}, false},
		"another capability": {Claimant{Capabilities: []Capability{"agents"}}, false},
	} {
		if got := tc.c.May(j); got != tc.want {
			t.Errorf("%s: May = %v", name, got)
		}
	}
	if j.Claimable(t0.Add(-time.Nanosecond)) || !j.Claimable(t0) {
		t.Error("Claimable around NotBefore")
	}
	if j.Expired(t0.Add(QueueExpiry-time.Nanosecond)) || !j.Expired(t0.Add(QueueExpiry)) {
		t.Error("Expired around QueueExpiry")
	}
	a, b := j, j
	b.Priority = -1
	if Before(a, b) <= 0 {
		t.Error("a lower priority does not come first")
	}
	b.Priority, b.CreatedAt = a.Priority, a.CreatedAt.Add(time.Second)
	if Before(a, b) >= 0 {
		t.Error("the older job does not come first")
	}
}
