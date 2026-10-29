package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/consolidate"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
)

const (
	c1 = "1111111111111111111111111111111111111111"
	c2 = "2222222222222222222222222222222222222222"
)

var at = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

func ev(id, path, commit string, start int, excerpt string) knowledge.Evidence {
	return knowledge.Evidence{
		ID: id, Source: knowledge.SourceCode, Path: path, Commit: commit, StartLine: start, EndLine: start,
		Excerpt: excerpt, SHA256: knowledge.HashExcerpt(excerpt), State: knowledge.EvidenceVerified,
	}
}

// existing is a requirement created by run-1 from agent alpha.
func existing(status knowledge.Status) *knowledge.Item {
	it := &knowledge.Item{
		Version: knowledge.FormatVersion, ID: "requirement-aaaaaa", Type: knowledge.TypeRequirement,
		Title: "Issued invoices are immutable", Status: knowledge.StatusProposed,
		Requirement: &knowledge.Requirement{Statement: "An issued invoice cannot be updated.", Kind: knowledge.KindBusinessRule, Personas: []string{"persona-pppppp"}},
		Support: []knowledge.Support{{
			Run: "run-1", Agent: "alpha", Interpretation: "i2", Statement: "Issued invoices are read-only.", Nature: knowledge.NatureSupported,
			Observations: []knowledge.Observation{{Statement: "Update rejects issued invoices.", Evidence: []string{"e1"}}},
		}},
		Evidence:   []knowledge.Evidence{ev("e1", "billing/invoice.go", c1, 4, "if issued {")},
		OpenPoints: []knowledge.OpenPoint{{ID: "p1", Kind: knowledge.PointQuestion, Summary: "Can admins cancel?", RaisedBy: "alpha"}},
		History:    []knowledge.Event{{At: at.Add(-time.Hour), Actor: knowledge.ActorRun, By: "run-1", To: knowledge.StatusProposed}},
	}
	switch status {
	case knowledge.StatusProposed:
	case knowledge.StatusContested, knowledge.StatusRejected, knowledge.StatusValidated:
		it.History = append(it.History, knowledge.Event{At: at.Add(-time.Hour), Actor: knowledge.ActorHuman, By: "PO", From: knowledge.StatusProposed, To: status, Reason: "checked"})
	case knowledge.StatusNeedsReview:
		it.History = append(it.History,
			knowledge.Event{At: at.Add(-time.Hour), Actor: knowledge.ActorHuman, By: "PO", From: knowledge.StatusProposed, To: knowledge.StatusValidated, Reason: "checked"},
			knowledge.Event{At: at.Add(-time.Hour), Actor: knowledge.ActorRun, By: "run-0", From: knowledge.StatusValidated, To: knowledge.StatusNeedsReview})
	}
	it.Status = it.History[len(it.History)-1].To
	return it
}

// built is the item run-2 built for the existing item: one support from
// beta, citing the same lines as e1 and a new doc line.
func built(statement string) *knowledge.Item {
	return &knowledge.Item{
		Version: knowledge.FormatVersion, ID: "requirement-aaaaaa", Type: knowledge.TypeRequirement,
		Title: "Invoices cannot change", Status: knowledge.StatusProposed,
		Requirement: &knowledge.Requirement{Statement: statement, Kind: knowledge.KindBusinessRule},
		Support: []knowledge.Support{{
			Run: "run-2", Agent: "beta", Interpretation: "i1", Statement: "Invoices are locked once issued.", Nature: knowledge.NatureSupported,
			Observations: []knowledge.Observation{{Statement: "The code and docs agree.", Evidence: []string{"e1", "e2"}}},
		}},
		Evidence: []knowledge.Evidence{ev("e1", "billing/invoice.go", c1, 4, "if issued {"), ev("e2", "docs/billing.md", c1, 3, "Issued invoices are final.")},
		OpenPoints: []knowledge.OpenPoint{
			{ID: "p1", Kind: knowledge.PointQuestion, Summary: "Can admins cancel?", RaisedBy: "beta"},
			{ID: "p2", Kind: knowledge.PointUncertainty, Summary: "The docs are old.", RaisedBy: "lead"},
		},
		History: []knowledge.Event{{At: at, Actor: knowledge.ActorRun, By: "run-2", To: knowledge.StatusProposed}},
	}
}

func encode(t *testing.T, it *knowledge.Item) string {
	t.Helper()
	data, err := knowledge.Encode(it)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInputShowsContentOnly(t *testing.T) {
	persona := &knowledge.Item{
		Version: knowledge.FormatVersion, ID: "persona-pppppp", Type: knowledge.TypePersona, Title: "Accountant", Status: knowledge.StatusValidated,
		Persona: &knowledge.Persona{Description: "Issues invoices."},
		Support: []knowledge.Support{
			{Run: "run-1", Agent: "alpha", Interpretation: "i1", Statement: "Accountants invoice.", Nature: knowledge.NatureHypothesis},
			{Run: "run-1", Agent: "beta", Interpretation: "i1", Statement: "Accountants invoice.", Nature: knowledge.NatureHypothesis},
		},
	}
	reports := []consolidate.CheckedReport{{Agent: "gamma", Report: &agent.Report{Interpretations: []agent.Interpretation{
		{ID: "i1", Statement: "The product serves accountants."},
		{ID: "i2", Statement: "Invoices are final."},
	}}}}
	plan := consolidate.Plan{Items: []consolidate.PlannedItem{
		{Key: "0/i1", Type: knowledge.TypePersona, Title: "Accountant", Persona: &agent.PersonaContent{Description: "Invoices."},
			Support: []consolidate.Ref{{Report: 0, Interpretation: "i1"}}},
		{Key: "0/i2", Type: knowledge.TypeRequirement, Title: "Final invoices",
			Requirement: &agent.RequirementContent{Statement: "Invoices are final.", Kind: knowledge.KindBusinessRule, Personas: []string{"0/i1"}},
			Support:     []consolidate.Ref{{Report: 0, Interpretation: "i2"}}},
	}}

	data, refs, err := Input([]*knowledge.Item{persona, existing(knowledge.StatusRejected)}, plan, reports)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"alpha", "beta", "gamma", "run-1", "status", "validated", "rejected", "history", "evidence", "billing/invoice.go"} {
		if strings.Contains(string(data), hidden) {
			t.Errorf("input reveals %q:\n%s", hidden, data)
		}
	}
	var in input
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if got := in.Existing[0].Interpretations; !slices.Equal(got, []string{"Accountants invoice."}) {
		t.Errorf("existing interpretations = %v", got)
	}
	if in.New[0].ID != "n1" || !slices.Equal(in.New[1].Requirement.Personas, []string{"n1"}) || in.New[1].Interpretations[0] != "Invoices are final." {
		t.Errorf("new items = %+v", in.New)
	}
	want := Refs{
		Existing: map[string]knowledge.Type{"persona-pppppp": knowledge.TypePersona, "requirement-aaaaaa": knowledge.TypeRequirement},
		Items:    map[string]knowledge.Type{"n1": knowledge.TypePersona, "n2": knowledge.TypeRequirement},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v", refs)
	}
}

func TestAssign(t *testing.T) {
	plan := consolidate.Plan{Items: []consolidate.PlannedItem{{Key: "k1"}, {Key: "k2"}, {Key: "k3"}}}
	rec := &agent.Reconciliation{Version: 1, Items: []agent.Match{
		{ID: "n3", Match: "requirement-aaaaaa", Conflict: " Contradicts. "},
		{ID: "n2"},
		{ID: "n1", Match: "persona-pppppp"},
	}}
	got := Assign(&plan, rec)
	want := []Match{{Item: 0, Existing: "persona-pppppp"}, {Item: 2, Existing: "requirement-aaaaaa", Conflict: "Contradicts."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matches = %+v", got)
	}
	if ids := []string{plan.Items[0].ID, plan.Items[1].ID, plan.Items[2].ID}; !slices.Equal(ids, []string{"persona-pppppp", "", "requirement-aaaaaa"}) {
		t.Errorf("planned IDs = %v", ids)
	}
}

func TestApplyAttaches(t *testing.T) {
	it := existing(knowledge.StatusValidated)
	src := built("Issued invoices are final.")
	original := encode(t, src)
	before := *it

	out, err := Apply(it, []Attachment{{Item: src}}, nil, "run-2", at)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || len(out.Stale) != 0 {
		t.Errorf("outcome = %+v", out)
	}
	if encode(t, src) != original {
		t.Error("Apply changed the built item")
	}
	if it.Status != knowledge.StatusValidated || len(it.History) != 2 {
		t.Errorf("confirming support changed the status: %s", it.Status)
	}
	if it.Title != before.Title || it.Requirement.Statement != "An issued invoice cannot be updated." || !reflect.DeepEqual(it.Support[0], before.Support[0]) {
		t.Error("existing content or support changed")
	}

	// e1 is reused, e2 is added; the question is known, the uncertainty new.
	if len(it.Evidence) != 2 || it.Evidence[1].ID != "e2" || it.Evidence[1].Path != "docs/billing.md" {
		t.Errorf("evidence = %+v", it.Evidence)
	}
	if s := it.Support[1]; s.Run != "run-2" || s.Agent != "beta" || !slices.Equal(s.Observations[0].Evidence, []string{"e1", "e2"}) {
		t.Errorf("attached support = %+v", s)
	}
	if len(it.OpenPoints) != 2 || it.OpenPoints[1].ID != "p2" || it.OpenPoints[1].Summary != "The docs are old." {
		t.Errorf("open points = %+v", it.OpenPoints)
	}
	if err := it.Validate(); err != nil {
		t.Errorf("reconciled item is invalid: %v", err)
	}
}

func TestApplyRenumbersAttachedIDs(t *testing.T) {
	it := existing(knowledge.StatusProposed)
	it.Evidence = append(it.Evidence, ev("e3", "a.go", c1, 1, "a")) // numbering may have gaps
	src := built("Issued invoices are final.")
	src.Evidence[0] = ev("e1", "b.go", c2, 9, "b")
	src.OpenPoints = append(src.OpenPoints, knowledge.OpenPoint{
		ID: "p3", Kind: knowledge.PointDisagreement, Summary: "Code and docs differ.", RaisedBy: "lead",
		Positions: []knowledge.Position{
			{Statement: "Final.", Run: "run-2", Agents: []string{"beta"}, Evidence: []string{"e2"}},
			{Statement: "Editable.", Run: "run-2", Agents: []string{"gamma"}, Evidence: []string{"e1"}},
		},
	})

	if _, err := Apply(it, []Attachment{{Item: src}}, nil, "run-2", at); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range it.Evidence {
		ids = append(ids, e.ID+"="+e.Path)
	}
	if !slices.Equal(ids, []string{"e1=billing/invoice.go", "e3=a.go", "e4=b.go", "e5=docs/billing.md"}) {
		t.Errorf("evidence = %v", ids)
	}
	if got := it.Support[1].Observations[0].Evidence; !slices.Equal(got, []string{"e4", "e5"}) {
		t.Errorf("remapped observation evidence = %v", got)
	}
	d := it.OpenPoints[len(it.OpenPoints)-1]
	if d.ID != "p3" || !slices.Equal(d.Positions[0].Evidence, []string{"e5"}) || !slices.Equal(d.Positions[1].Evidence, []string{"e4"}) {
		t.Errorf("disagreement = %+v", d)
	}
	if it.Status != knowledge.StatusContested || !strings.Contains(it.History[len(it.History)-1].Reason, "new disagreement: p3") {
		t.Errorf("status %s, history %+v", it.Status, it.History)
	}
	if err := it.Validate(); err != nil {
		t.Errorf("reconciled item is invalid: %v", err)
	}
}

func TestApplyConflict(t *testing.T) {
	it := existing(knowledge.StatusValidated)
	src := built("Admins can edit issued invoices for 24 hours.")
	if _, err := Apply(it, []Attachment{{Item: src, Conflict: "The docs now allow edits."}}, nil, "run-2", at); err != nil {
		t.Fatal(err)
	}
	d := it.OpenPoints[len(it.OpenPoints)-1]
	want := knowledge.OpenPoint{
		ID: "p3", Kind: knowledge.PointDisagreement, Summary: "The docs now allow edits.", RaisedBy: RaisedByReconciler,
		Positions: []knowledge.Position{
			{Statement: "An issued invoice cannot be updated.", Run: "run-1", Agents: []string{"alpha"}, Evidence: []string{"e1"}},
			{Statement: "Admins can edit issued invoices for 24 hours.", Run: "run-2", Agents: []string{"beta"}, Evidence: []string{"e1", "e2"}},
		},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("conflict = %+v", d)
	}
	last := it.History[len(it.History)-1]
	if it.Status != knowledge.StatusNeedsReview || last.Actor != knowledge.ActorRun || last.By != "run-2" || last.Reason != "new disagreement: p3" {
		t.Errorf("status %s, last event %+v", it.Status, last)
	}

	// The same conflict found again adds its support, but no open point
	// and no status change.
	events := len(it.History)
	src.Support[0].Run = "run-3"
	if _, err := Apply(it, []Attachment{{Item: src, Conflict: "The docs now allow edits."}}, nil, "run-3", at); err != nil {
		t.Fatal(err)
	}
	if len(it.Support) != 3 || len(it.OpenPoints) != 3 || len(it.History) != events {
		t.Errorf("second conflict: %d supports, %d open points, %d events", len(it.Support), len(it.OpenPoints), len(it.History))
	}
}

func TestApplyConflictWithoutOrigin(t *testing.T) {
	it := existing(knowledge.StatusProposed)
	it.Support = nil // edited by hand: no support of the creating run is left
	if _, err := Apply(it, []Attachment{{Item: built("Editable."), Conflict: "Contradicts."}}, nil, "run-2", at); err != nil {
		t.Fatal(err)
	}
	p := it.OpenPoints[len(it.OpenPoints)-1]
	if p.Kind != knowledge.PointUncertainty || p.Summary != "Contradicts." || p.RaisedBy != RaisedByReconciler || it.Status != knowledge.StatusProposed {
		t.Errorf("point %+v, status %s", p, it.Status)
	}
}

func TestApplyStatuses(t *testing.T) {
	disagreement := knowledge.OpenPoint{
		ID: "p9", Kind: knowledge.PointDisagreement, Summary: "Split.", RaisedBy: "lead",
		Positions: []knowledge.Position{{Statement: "A.", Run: "run-2", Agents: []string{"beta"}}, {Statement: "B.", Run: "run-2", Agents: []string{"gamma"}}},
	}
	stale := Freshness{keyOf(ev("e1", "billing/invoice.go", c1, 4, "if issued {")): {State: knowledge.EvidenceStale, Reason: "excerpt not found in billing/invoice.go"}}
	tests := []struct {
		status            knowledge.Status
		staleTo, splitsTo knowledge.Status
	}{
		{knowledge.StatusProposed, knowledge.StatusProposed, knowledge.StatusContested},
		{knowledge.StatusContested, knowledge.StatusContested, knowledge.StatusContested},
		{knowledge.StatusValidated, knowledge.StatusNeedsReview, knowledge.StatusNeedsReview},
		{knowledge.StatusNeedsReview, knowledge.StatusNeedsReview, knowledge.StatusNeedsReview},
		{knowledge.StatusRejected, knowledge.StatusRejected, knowledge.StatusRejected},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			it := existing(tt.status)
			if _, err := Apply(it, nil, stale, "run-2", at); err != nil {
				t.Fatal(err)
			}
			if it.Status != tt.staleTo {
				t.Errorf("stale evidence: status %s, want %s", it.Status, tt.staleTo)
			}
			if err := it.Validate(); err != nil {
				t.Errorf("invalid after stale evidence: %v", err)
			}

			it = existing(tt.status)
			src := built("Final.")
			src.OpenPoints = []knowledge.OpenPoint{disagreement}
			if _, err := Apply(it, []Attachment{{Item: src}}, nil, "run-2", at); err != nil {
				t.Fatal(err)
			}
			if it.Status != tt.splitsTo {
				t.Errorf("new disagreement: status %s, want %s", it.Status, tt.splitsTo)
			}
			if err := it.Validate(); err != nil {
				t.Errorf("invalid after a new disagreement: %v", err)
			}
		})
	}
}

func TestApplyFreshness(t *testing.T) {
	it := existing(knowledge.StatusValidated)
	key := keyOf(it.Evidence[0])

	// Unchanged ground: nothing to write.
	out, err := Apply(it, nil, Freshness{key: {State: knowledge.EvidenceRelocated, StartLine: 7, EndLine: 7}}, "run-2", at)
	if err != nil || out.Changed || it.Evidence[0].StartLine != 4 {
		t.Fatalf("holding evidence: outcome %+v, %v, evidence %+v", out, err, it.Evidence[0])
	}

	out, err = Apply(it, nil, Freshness{key: {State: knowledge.EvidenceStale, Reason: "billing/invoice.go does not exist at this commit"}}, "run-2", at)
	if err != nil || !out.Changed || !slices.Equal(out.Stale, []string{"e1"}) {
		t.Fatalf("stale evidence: outcome %+v, %v", out, err)
	}
	p := it.OpenPoints[len(it.OpenPoints)-1]
	if it.Evidence[0].State != knowledge.EvidenceStale || p.RaisedBy != "raun" ||
		p.Summary != "Evidence e1 (billing/invoice.go:4-4) no longer holds: billing/invoice.go does not exist at this commit." {
		t.Errorf("evidence %+v, point %+v", it.Evidence[0], p)
	}
	if last := it.History[len(it.History)-1]; it.Status != knowledge.StatusNeedsReview || last.Reason != "stale evidence: e1" {
		t.Errorf("status %s, last event %+v", it.Status, last)
	}

	// Still stale: no new point, no new event.
	points, events := len(it.OpenPoints), len(it.History)
	if out, _ := Apply(it, nil, Freshness{key: {State: knowledge.EvidenceStale, Reason: "gone"}}, "run-3", at); out.Changed {
		t.Error("evidence still stale changed the item")
	}
	// Holding again: verified, the status stays for a human to decide.
	if out, _ := Apply(it, nil, Freshness{key: {State: knowledge.EvidenceVerified}}, "run-4", at); !out.Changed || it.Evidence[0].State != knowledge.EvidenceVerified {
		t.Errorf("restored evidence = %+v", it.Evidence[0])
	}
	if len(it.OpenPoints) != points || len(it.History) != events || it.Status != knowledge.StatusNeedsReview {
		t.Errorf("open points %d -> %d, events %d -> %d, status %s", points, len(it.OpenPoints), events, len(it.History), it.Status)
	}
}

type fakeChecker struct {
	results map[string]evidence.Result // by path
	calls   []string
}

func (f *fakeChecker) Verify(_ context.Context, e knowledge.Evidence, at string) (evidence.Result, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s@%.1s", e.Path, at))
	return f.results[e.Path], nil
}

type fakeAncestry map[string]bool // ancestor commit -> is an ancestor of the run commit

func (f fakeAncestry) IsAncestor(_ context.Context, ancestor, _ string) (bool, error) {
	ok, known := f[ancestor]
	if !known {
		return false, fmt.Errorf("%w: %q", gitx.ErrUnknownCommit, ancestor)
	}
	return ok, nil
}

func TestCheckFreshness(t *testing.T) {
	const run, later, unknown = "3333333333333333333333333333333333333333", "4444444444444444444444444444444444444444", "5555555555555555555555555555555555555555"
	a := &knowledge.Item{ID: "requirement-aaaaaa", Evidence: []knowledge.Evidence{
		ev("e1", "stale.go", c1, 1, "x"),
		ev("e2", "same-commit.go", run, 1, "x"),
		ev("e3", "later.go", later, 1, "x"),
		ev("e4", "unknown.go", unknown, 1, "x"),
		ev("e5", "too-long.go", c2, 1, "x"),
	}}
	b := &knowledge.Item{ID: "persona-pppppp", Evidence: []knowledge.Evidence{ev("e1", "stale.go", c1, 1, "x")}}
	checker := &fakeChecker{results: map[string]evidence.Result{
		"stale.go":    {State: knowledge.EvidenceStale, Reason: "gone"},
		"too-long.go": {State: knowledge.EvidenceInvalid, Reason: "cites 50 lines"},
	}}

	fresh, err := CheckFreshness(context.Background(), []*knowledge.Item{a, b}, run, checker, fakeAncestry{c1: true, c2: true, later: false})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checker.calls, []string{"stale.go@3", "too-long.go@3"}) {
		t.Errorf("checked %v", checker.calls)
	}
	if len(fresh) != 1 || fresh[keyOf(a.Evidence[0])].State != knowledge.EvidenceStale {
		t.Errorf("freshness = %+v", fresh)
	}
}

func TestSameContent(t *testing.T) {
	a := existing(knowledge.StatusProposed)
	b := existing(knowledge.StatusValidated)
	b.Support = nil
	if !SameContent(a, b) {
		t.Error("status and support are not content")
	}
	b.Requirement.Rationale = "Legal."
	if SameContent(a, b) {
		t.Error("a changed rationale is a content change")
	}
}
