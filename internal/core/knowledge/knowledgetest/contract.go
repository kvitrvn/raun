package knowledgetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

// Scopes the suite stores items in. Two repositories share a workspace, and
// one repository identifier appears in two workspaces, so that a leak across
// either boundary shows.
func scopeA() knowledge.Scope { return knowledge.Scope{Workspace: "acme", Repository: "billing"} }
func scopeB() knowledge.Scope { return knowledge.Scope{Workspace: "acme", Repository: "shipping"} }
func otherTenant() knowledge.Scope {
	return knowledge.Scope{Workspace: "globex", Repository: "billing"}
}

const (
	personaID     = "persona-k3x9q2"
	otherPersona  = "persona-m7h4wd"
	requirementID = "requirement-8fz2mc"
	run           = "run-20261112-a1"
	human         = "account-42"
)

// at returns a time of the suite, minutes after its start.
func at(minutes int) time.Time {
	return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

// Suite runs the contract of knowledge.Store on stores made by open, one per
// subtest. Every store implementation runs it.
func Suite(t *testing.T, open func(t *testing.T) knowledge.Store) {
	t.Run("create and get", func(t *testing.T) { testCreateAndGet(t, open(t)) })
	t.Run("taken identifier", func(t *testing.T) { testTakenID(t, open(t)) })
	t.Run("not found", func(t *testing.T) { testNotFound(t, open(t)) })
	t.Run("list", func(t *testing.T) { testList(t, open(t)) })
	t.Run("scopes are isolated", func(t *testing.T) { testScopes(t, open(t)) })
	t.Run("update", func(t *testing.T) { testUpdate(t, open(t)) })
	t.Run("stale revision", func(t *testing.T) { testStaleRevision(t, open(t)) })
	t.Run("failing mutation", func(t *testing.T) { testFailingMutation(t, open(t)) })
	t.Run("history is append-only", func(t *testing.T) { testHistoryAppendOnly(t, open(t)) })
	t.Run("invalid result", func(t *testing.T) { testInvalidResult(t, open(t)) })
	t.Run("persona references", func(t *testing.T) { testReferences(t, open(t)) })
	t.Run("copies", func(t *testing.T) { testCopies(t, open(t)) })
	t.Run("concurrent writers", func(t *testing.T) { testConcurrentWriters(t, open(t)) })
	t.Run("invalid input", func(t *testing.T) { testInvalidInput(t, open(t)) })
}

// Persona returns a new persona item.
func Persona(t *testing.T, id string) knowledge.Item {
	t.Helper()
	it, err := knowledge.New(knowledge.Draft{
		ID:      id,
		Type:    knowledge.TypePersona,
		Title:   "Accountant " + id,
		Persona: &knowledge.Persona{Description: "Issues and corrects invoices.", Goals: []string{"close the month"}},
	}, run, "", at(0))
	if err != nil {
		t.Fatalf("new persona: %v", err)
	}
	return it
}

// Requirement returns a new requirement item that names personas.
func Requirement(t *testing.T, id string, personas ...string) knowledge.Item {
	t.Helper()
	it, err := knowledge.New(knowledge.Draft{
		ID:    id,
		Type:  knowledge.TypeRequirement,
		Title: "Issued invoices are immutable",
		Requirement: &knowledge.Requirement{
			Statement: "An invoice cannot be edited once issued.",
			Kind:      knowledge.KindBusinessRule,
			Personas:  personas,
		},
	}, run, "", at(0))
	if err != nil {
		t.Fatalf("new requirement: %v", err)
	}
	return it
}

func create(t *testing.T, s knowledge.Store, scope knowledge.Scope, it knowledge.Item) {
	t.Helper()
	if err := s.Create(context.Background(), scope, it); err != nil {
		t.Fatalf("create %s in %+v: %v", it.ID, scope, err)
	}
}

func get(t *testing.T, s knowledge.Store, scope knowledge.Scope, id string) knowledge.Item {
	t.Helper()
	it, err := s.Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("get %s in %+v: %v", id, scope, err)
	}
	return it
}

// decide is a mutation that validates the item.
func decide(minute int) knowledge.Mutation {
	return func(it knowledge.Item) (knowledge.Item, error) {
		return knowledge.Decide(it, knowledge.StatusValidated, human, "checked", at(minute))
	}
}

// assertSame fails unless got and want are the same item. Items are compared
// through their JSON form, where every time is in UTC.
func assertSame(t *testing.T, got, want knowledge.Item) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != string(w) {
		t.Errorf("items differ:\n got %s\nwant %s", g, w)
	}
}

func testCreateAndGet(t *testing.T, s knowledge.Store) {
	p := Persona(t, personaID)
	create(t, s, scopeA(), p)
	got := get(t, s, scopeA(), personaID)
	assertSame(t, got, p)
	if got.Revision != 1 {
		t.Errorf("revision at creation = %d, want 1", got.Revision)
	}

	full := fullItem(t)
	create(t, s, scopeA(), full)
	assertSame(t, get(t, s, scopeA(), full.ID), full)
}

// fullItem returns a requirement that uses every field: support with
// observations, evidence with a checked time, a resolved disagreement, a
// question and several events.
func fullItem(t *testing.T) knowledge.Item {
	t.Helper()
	it := Requirement(t, "requirement-q2w3e4")
	excerpt := "if inv.Status == StatusIssued {\n\treturn ErrInvoiceLocked\n}"
	ev := knowledge.Evidence{
		ID: "e1", Source: knowledge.SourceCode, Path: "billing/invoice.go", Commit: strings.Repeat("3f1c2a9b", 5),
		StartLine: 40, EndLine: 42, Excerpt: excerpt, SHA256: knowledge.ExcerptSHA256(excerpt),
		State: knowledge.EvidenceVerified, CheckedAt: at(1), CheckedCommit: strings.Repeat("3f1c2a9b", 5),
	}
	it, err := knowledge.AppendFromRun(it, knowledge.RunAdditions{
		Run: run, At: at(1), Reason: "the lead found a conflict",
		Support: []knowledge.Support{{
			Run: run, Agent: "alpha", Interpretation: "i1", Statement: "Issued invoices are read-only.",
			Nature:       knowledge.NatureSupported,
			Observations: []knowledge.Observation{{Statement: "Update rejects issued invoices.", Evidence: []string{"e1"}}},
		}},
		Evidence: []knowledge.Evidence{ev},
		OpenPoints: []knowledge.OpenPoint{
			{ID: "p1", Kind: knowledge.PointDisagreement, Summary: "Code and docs disagree.", RaisedBy: "lead", Positions: []knowledge.Position{
				{Statement: "Never editable.", Run: run, Agents: []string{"alpha"}, Evidence: []string{"e1"}},
				{Statement: "Editable for 24 hours.", Run: run, Agents: []string{"beta"}},
			}},
			{ID: "p2", Kind: knowledge.PointQuestion, Summary: "Credit notes too?"},
		},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	zero := 0
	it, err = knowledge.ResolveOpenPoint(it, "p1", knowledge.Resolution{At: at(2), By: human, Position: &zero, Note: "the code wins"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return it
}

func testTakenID(t *testing.T, s knowledge.Store) {
	p := Persona(t, personaID)
	create(t, s, scopeA(), p)
	other := Persona(t, personaID)
	other.Title = "Someone else"
	if err := s.Create(context.Background(), scopeA(), other); !errors.Is(err, knowledge.ErrIDTaken) {
		t.Fatalf("create a taken identifier: %v, want ErrIDTaken", err)
	}
	assertSame(t, get(t, s, scopeA(), personaID), p)
}

func testNotFound(t *testing.T, s knowledge.Store) {
	if _, err := s.Get(context.Background(), scopeA(), personaID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("get: %v, want ErrNotFound", err)
	}
	_, err := s.Update(context.Background(), scopeA(), personaID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		t.Error("mutation called for an unknown item")
		return it, nil
	})
	if !errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("update: %v, want ErrNotFound", err)
	}
}

func testList(t *testing.T, s knowledge.Store) {
	create(t, s, scopeA(), Persona(t, otherPersona))
	create(t, s, scopeA(), Persona(t, personaID))
	create(t, s, scopeA(), Requirement(t, requirementID, personaID))
	if _, err := s.Update(context.Background(), scopeA(), personaID, 1, decide(5)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		filter knowledge.Filter
		want   []string
	}{
		{knowledge.Filter{}, []string{personaID, otherPersona, requirementID}},
		{knowledge.Filter{Type: knowledge.TypePersona}, []string{personaID, otherPersona}},
		{knowledge.Filter{Type: knowledge.TypeRequirement}, []string{requirementID}},
		{knowledge.Filter{Status: knowledge.StatusValidated}, []string{personaID}},
		{knowledge.Filter{Type: knowledge.TypePersona, Status: knowledge.StatusProposed}, []string{otherPersona}},
		{knowledge.Filter{Type: knowledge.TypeRequirement, Status: knowledge.StatusValidated}, nil},
	}
	for _, tt := range tests {
		items, err := s.List(context.Background(), scopeA(), tt.filter)
		if err != nil {
			t.Fatalf("list %+v: %v", tt.filter, err)
		}
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		if !slices.Equal(ids, tt.want) {
			t.Errorf("list %+v = %v, want %v", tt.filter, ids, tt.want)
		}
	}

	items, err := s.List(context.Background(), scopeA(), knowledge.Filter{Type: knowledge.TypePersona})
	if err != nil {
		t.Fatal(err)
	}
	if len(items[0].History) != 2 || items[0].Revision != 2 {
		t.Errorf("listed item: revision %d with %d events, want 2 and 2", items[0].Revision, len(items[0].History))
	}
}

func testScopes(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))

	for _, other := range []knowledge.Scope{scopeB(), otherTenant()} {
		if _, err := s.Get(ctx, other, personaID); !errors.Is(err, knowledge.ErrNotFound) {
			t.Errorf("get from %+v: %v, want ErrNotFound", other, err)
		}
		items, err := s.List(ctx, other, knowledge.Filter{})
		if err != nil || len(items) != 0 {
			t.Errorf("list %+v = %v, %v, want nothing", other, items, err)
		}
		if _, err := s.Update(ctx, other, personaID, 1, decide(5)); !errors.Is(err, knowledge.ErrNotFound) {
			t.Errorf("update from %+v: %v, want ErrNotFound", other, err)
		}
		// A persona of another scope is no reference.
		if err := s.Create(ctx, other, Requirement(t, requirementID, personaID)); !errors.Is(err, knowledge.ErrUnknownReference) {
			t.Errorf("reference across to %+v: %v, want ErrUnknownReference", other, err)
		}
		// The same identifier is free in another scope.
		p := Persona(t, personaID)
		p.Title = "Of " + other.Workspace + "/" + other.Repository
		create(t, s, other, p)
	}

	if _, err := s.Update(ctx, scopeB(), personaID, 1, decide(5)); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []knowledge.Scope{scopeA(), otherTenant()} {
		if it := get(t, s, scope, personaID); it.Status != knowledge.StatusProposed || it.Revision != 1 {
			t.Errorf("an update in %+v reached %+v: %s at revision %d", scopeB(), scope, it.Status, it.Revision)
		}
	}
	if it := get(t, s, otherTenant(), personaID); !strings.HasPrefix(it.Title, "Of globex") {
		t.Errorf("the item of %+v is %q", otherTenant(), it.Title)
	}
}

func testUpdate(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))

	got, err := s.Update(ctx, scopeA(), personaID, 1, decide(5))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Revision != 2 || got.Status != knowledge.StatusValidated || len(got.History) != 2 {
		t.Errorf("updated: revision %d, %s, %d events", got.Revision, got.Status, len(got.History))
	}
	assertSame(t, get(t, s, scopeA(), personaID), got)

	// A mutation that changes nothing is still a write.
	same, err := s.Update(ctx, scopeA(), personaID, 2, func(it knowledge.Item) (knowledge.Item, error) { return it, nil })
	if err != nil {
		t.Fatalf("update without change: %v", err)
	}
	if same.Revision != 3 {
		t.Errorf("revision after an update without change = %d, want 3", same.Revision)
	}

	// The revision is the store's: a mutation cannot set it.
	forged, err := s.Update(ctx, scopeA(), personaID, 3, func(it knowledge.Item) (knowledge.Item, error) {
		it.Revision = 99
		return it, nil
	})
	if err != nil || forged.Revision != 4 || get(t, s, scopeA(), personaID).Revision != 4 {
		t.Errorf("a mutation set the revision: %+v, %v", forged.Revision, err)
	}
}

func testStaleRevision(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))
	if _, err := s.Update(ctx, scopeA(), personaID, 1, decide(5)); err != nil {
		t.Fatal(err)
	}
	before := get(t, s, scopeA(), personaID)

	for _, rev := range []int64{0, 1, 3} {
		_, err := s.Update(ctx, scopeA(), personaID, rev, func(it knowledge.Item) (knowledge.Item, error) {
			t.Errorf("mutation called with the stale revision %d", rev)
			return it, nil
		})
		if !errors.Is(err, knowledge.ErrStaleRevision) {
			t.Errorf("update at revision %d: %v, want ErrStaleRevision", rev, err)
		}
	}
	assertSame(t, get(t, s, scopeA(), personaID), before)
}

func testFailingMutation(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))
	before := get(t, s, scopeA(), personaID)
	refused := errors.New("refused by the caller")

	_, err := s.Update(ctx, scopeA(), personaID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		// Change the copy before failing: nothing of it may be kept.
		it, _ = knowledge.Decide(it, knowledge.StatusValidated, human, "checked", at(5))
		return it, refused
	})
	if err != refused {
		t.Errorf("update = %v, want the error of the mutation as it is", err)
	}
	assertSame(t, get(t, s, scopeA(), personaID), before)

	// The store still works after a failed update.
	if _, err := s.Update(ctx, scopeA(), personaID, 1, decide(6)); err != nil {
		t.Errorf("update after a failed one: %v", err)
	}
}

func testHistoryAppendOnly(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))
	if _, err := s.Update(ctx, scopeA(), personaID, 1, decide(5)); err != nil {
		t.Fatal(err)
	}
	before := get(t, s, scopeA(), personaID)

	rewrites := map[string]knowledge.Mutation{
		"an event changed": func(it knowledge.Item) (knowledge.Item, error) {
			it.History[0].Reason = "rewritten"
			return it, nil
		},
		"an event removed": func(it knowledge.Item) (knowledge.Item, error) {
			it.History = it.History[1:]
			return it, nil
		},
	}
	for name, mutate := range rewrites {
		if _, err := s.Update(ctx, scopeA(), personaID, 2, mutate); !errors.Is(err, knowledge.ErrHistoryChanged) {
			t.Errorf("%s: %v, want ErrHistoryChanged", name, err)
		}
	}
	assertSame(t, get(t, s, scopeA(), personaID), before)
}

func testInvalidResult(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))
	before := get(t, s, scopeA(), personaID)
	_, err := s.Update(ctx, scopeA(), personaID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		it.Title = ""
		return it, nil
	})
	var invalid *knowledge.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("update to an invalid item: %v, want a validation error", err)
	}
	assertSame(t, get(t, s, scopeA(), personaID), before)

	bad := Persona(t, otherPersona)
	bad.Revision = 2
	if err := s.Create(ctx, scopeA(), bad); err == nil {
		t.Error("create at revision 2 accepted")
	}
	if _, err := s.Get(ctx, scopeA(), otherPersona); !errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("a refused item was stored: %v", err)
	}
}

func testReferences(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	if err := s.Create(ctx, scopeA(), Requirement(t, requirementID, personaID)); !errors.Is(err, knowledge.ErrUnknownReference) {
		t.Errorf("create naming a missing persona: %v, want ErrUnknownReference", err)
	}
	create(t, s, scopeA(), Persona(t, personaID))
	create(t, s, scopeA(), Requirement(t, requirementID, personaID))

	// A requirement is no persona, even with a name that fits.
	_, err := s.Update(ctx, scopeA(), requirementID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		it.Requirement.Personas = append(it.Requirement.Personas, otherPersona)
		return it, nil
	})
	if !errors.Is(err, knowledge.ErrUnknownReference) {
		t.Errorf("update naming a missing persona: %v, want ErrUnknownReference", err)
	}
	if got := get(t, s, scopeA(), requirementID); got.Revision != 1 || len(got.Requirement.Personas) != 1 {
		t.Errorf("a refused update was stored: revision %d, personas %v", got.Revision, got.Requirement.Personas)
	}

	// A rejected persona still exists.
	create(t, s, scopeA(), Persona(t, otherPersona))
	if _, err := s.Update(ctx, scopeA(), otherPersona, 1, func(it knowledge.Item) (knowledge.Item, error) {
		return knowledge.Decide(it, knowledge.StatusRejected, human, "not a real role", at(5))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, scopeA(), requirementID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		it.Requirement.Personas = append(it.Requirement.Personas, otherPersona)
		return it, nil
	}); err != nil {
		t.Errorf("update naming a rejected persona: %v", err)
	}
}

func testCopies(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	full := fullItem(t)
	want := full.Clone()
	create(t, s, scopeA(), full)
	full.Support[0].Statement = "changed by the caller"
	full.History[0].Reason = "changed by the caller"

	got := get(t, s, scopeA(), want.ID)
	got.Evidence[0].Excerpt = "changed by the caller"
	got.Requirement.Statement = "changed by the caller"

	listed, err := s.List(ctx, scopeA(), knowledge.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	listed[0].OpenPoints[0].Positions[0].Agents[0] = "changed by the caller"

	var kept knowledge.Item
	updated, err := s.Update(ctx, scopeA(), want.ID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		kept = it
		return it, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	kept.Support[0].Statement = "changed by the mutation after the update"
	updated.History[0].Reason = "changed by the caller"

	stored := get(t, s, scopeA(), want.ID)
	want.Revision = 2
	assertSame(t, stored, want)
}

func testConcurrentWriters(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	create(t, s, scopeA(), Persona(t, personaID))

	const writers = 4
	var calls atomic.Int32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Update(ctx, scopeA(), personaID, 1, func(it knowledge.Item) (knowledge.Item, error) {
				calls.Add(1)
				return knowledge.Decide(it, knowledge.StatusValidated, fmt.Sprintf("account-%d", i), "checked", at(5))
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	won := 0
	for err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, knowledge.ErrStaleRevision):
			t.Errorf("a writer failed with %v, want ErrStaleRevision", err)
		}
	}
	if won != 1 || calls.Load() != 1 {
		t.Errorf("%d writers won and %d mutations ran, want 1 and 1", won, calls.Load())
	}
	if it := get(t, s, scopeA(), personaID); it.Revision != 2 || len(it.History) != 2 {
		t.Errorf("after the race: revision %d with %d events, want 2 and 2", it.Revision, len(it.History))
	}
}

func testInvalidInput(t *testing.T, s knowledge.Store) {
	ctx := context.Background()
	empty := knowledge.Scope{Workspace: "acme"}
	if err := s.Create(ctx, empty, Persona(t, personaID)); err == nil {
		t.Error("create without a repository accepted")
	}
	if _, err := s.Get(ctx, empty, personaID); err == nil || errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("get without a repository: %v, want a scope error", err)
	}
	if _, err := s.List(ctx, knowledge.Scope{Repository: "billing"}, knowledge.Filter{}); err == nil {
		t.Error("list without a workspace accepted")
	}
	if _, err := s.Update(ctx, empty, personaID, 1, decide(5)); err == nil || errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("update without a repository: %v, want a scope error", err)
	}
	if _, err := s.List(ctx, scopeA(), knowledge.Filter{Status: "done"}); err == nil {
		t.Error("list with an unknown status accepted")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Create(canceled, scopeA(), Persona(t, personaID)); !errors.Is(err, context.Canceled) {
		t.Errorf("create with a canceled context: %v", err)
	}
	if _, err := s.Get(ctx, scopeA(), personaID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Errorf("a canceled create stored the item: %v", err)
	}
}
