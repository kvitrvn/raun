package knowledge

import (
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	it := newRequirement(t)
	if it.Status != StatusProposed || it.Revision != 1 {
		t.Errorf("status %s revision %d, want proposed at revision 1", it.Status, it.Revision)
	}
	if len(it.History) != 1 {
		t.Fatalf("%d events, want the creation event only", len(it.History))
	}
	e := it.History[0]
	if e.From != "" || e.To != StatusProposed || e.Actor != ActorRun || e.By != testRun {
		t.Errorf("creation event = %+v", e)
	}
	if !it.CreatedAt.Equal(at(0)) {
		t.Errorf("created_at = %v", it.CreatedAt)
	}
}

func TestNewRefusesBadDrafts(t *testing.T) {
	good := func() Draft {
		return Draft{ID: testPerID, Type: TypePersona, Title: "Accountant", Persona: &Persona{Description: "Issues invoices."}}
	}
	tests := []struct {
		name  string
		draft func() Draft
	}{
		{name: "unknown type", draft: func() Draft { d := good(); d.Type = "epic"; return d }},
		{name: "identifier of another type", draft: func() Draft { d := good(); d.ID = testReqID; return d }},
		{name: "malformed identifier", draft: func() Draft { d := good(); d.ID = "persona-ABCDEF"; return d }},
		{name: "missing title", draft: func() Draft { d := good(); d.Title = ""; return d }},
		{name: "missing content", draft: func() Draft { d := good(); d.Persona = nil; return d }},
		{name: "content of another type", draft: func() Draft {
			d := good()
			d.Requirement = &Requirement{Statement: "x"}
			return d
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.draft(), testRun, "", at(0))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a *ValidationError", err)
			}
		})
	}
	if _, err := New(good(), "", "", at(0)); !errors.Is(err, ErrActorRequired) {
		t.Errorf("no run: err = %v", err)
	}
	if it, err := New(good(), testRun, "", at(0)); err != nil || it.Type != TypePersona {
		t.Errorf("persona: %v", err)
	}
}

func TestNewCopiesTheDraft(t *testing.T) {
	persona := &Persona{Description: "Issues invoices.", Goals: []string{"get paid"}}
	it, err := New(Draft{ID: testPerID, Type: TypePersona, Title: "Accountant", Persona: persona}, testRun, "", at(0))
	if err != nil {
		t.Fatal(err)
	}
	persona.Goals[0] = "changed"
	persona.Description = "changed"
	if it.Persona.Goals[0] != "get paid" || it.Persona.Description != "Issues invoices." {
		t.Error("the item shares memory with the draft")
	}
}

func TestAppendFromRunStatusEffects(t *testing.T) {
	additions := map[string]func() RunAdditions{
		"disagreement": func() RunAdditions {
			return RunAdditions{OpenPoints: []OpenPoint{disagreement("p9", testRun2)}}
		},
		"uncertainty": func() RunAdditions {
			return RunAdditions{OpenPoints: []OpenPoint{{ID: "p9", Kind: PointUncertainty, Summary: "Unclear for credit notes."}}}
		},
		"question": func() RunAdditions {
			return RunAdditions{OpenPoints: []OpenPoint{question("p9")}}
		},
		"support and evidence": func() RunAdditions {
			return RunAdditions{Support: []Support{support(testRun2, "e9")}, Evidence: []Evidence{evidence("e9")}}
		},
	}
	tests := []struct {
		status     Status
		unresolved bool
		addition   string
		want       Status
	}{
		{StatusProposed, false, "disagreement", StatusContested},
		{StatusProposed, false, "uncertainty", StatusProposed},
		{StatusProposed, false, "question", StatusProposed},
		{StatusProposed, false, "support and evidence", StatusProposed},
		{StatusContested, true, "disagreement", StatusContested},
		{StatusContested, true, "support and evidence", StatusContested},
		{StatusValidated, false, "disagreement", StatusNeedsReview},
		{StatusValidated, false, "uncertainty", StatusValidated},
		{StatusValidated, false, "question", StatusValidated},
		{StatusValidated, false, "support and evidence", StatusValidated},
		{StatusRejected, false, "disagreement", StatusRejected},
		{StatusRejected, false, "support and evidence", StatusRejected},
		{StatusNeedsReview, false, "disagreement", StatusNeedsReview},
		{StatusNeedsReview, true, "question", StatusNeedsReview},
	}
	for _, tt := range tests {
		t.Run(string(tt.status)+" + "+tt.addition, func(t *testing.T) {
			it := itemIn(t, tt.status, tt.unresolved)
			before := it.clone()
			add := additions[tt.addition]()
			add.Run, add.Reason, add.At = testRun2, "found again", at(40)

			got, err := AppendFromRun(it, add)
			if err != nil {
				t.Fatalf("AppendFromRun: %v", err)
			}
			unchanged(t, before, it)
			keepsGround(t, before, got)
			if got.Status != tt.want {
				t.Errorf("status = %s, want %s", got.Status, tt.want)
			}
			wantEvents := len(before.History)
			if tt.want != tt.status {
				wantEvents++
			}
			if len(got.History) != wantEvents {
				t.Errorf("%d events, want %d", len(got.History), wantEvents)
			}
			if !got.UpdatedAt.Equal(at(40)) {
				t.Errorf("updated_at = %v", got.UpdatedAt)
			}
		})
	}
}

func TestAppendFromRunRefusals(t *testing.T) {
	resolved := disagreement("p9", testRun2)
	resolved.Resolution = &Resolution{At: at(40), By: testHuman, Note: "n"}
	foreignPosition := disagreement("p9", testRun2)
	foreignPosition.Positions[1].Run = testRun

	tests := []struct {
		name    string
		add     RunAdditions
		wantErr error
	}{
		{name: "nothing", add: RunAdditions{}, wantErr: ErrNothingToAppend},
		{name: "support of another run", add: RunAdditions{Support: []Support{support(testRun, "e1")}}, wantErr: ErrForeignRun},
		{name: "position of another run", add: RunAdditions{OpenPoints: []OpenPoint{foreignPosition}}, wantErr: ErrForeignRun},
		{name: "resolved open point", add: RunAdditions{OpenPoints: []OpenPoint{resolved}}, wantErr: ErrResolvedByRun},
		{name: "disagreement without a reason", add: RunAdditions{OpenPoints: []OpenPoint{disagreement("p9", testRun2)}}, wantErr: ErrReasonRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, StatusProposed, false)
			before := it.clone()
			tt.add.Run, tt.add.At = testRun2, at(40)
			if _, err := AppendFromRun(it, tt.add); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			unchanged(t, before, it)
		})
	}
}

func TestAppendFromRunValidatesTheResult(t *testing.T) {
	tests := []struct {
		name     string
		add      RunAdditions
		wantPath string
	}{
		{
			name:     "taken evidence identifier",
			add:      RunAdditions{Evidence: []Evidence{evidence("e1")}},
			wantPath: "evidence[1].id",
		},
		{
			name:     "dangling evidence reference",
			add:      RunAdditions{Support: []Support{support(testRun2, "e7")}},
			wantPath: "support[1].observations[0].evidence[0]",
		},
		{
			name:     "taken open point identifier",
			add:      RunAdditions{OpenPoints: []OpenPoint{question("p1"), question("p1")}},
			wantPath: "open_points[1].id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, StatusProposed, false)
			before := it.clone()
			tt.add.Run, tt.add.At = testRun2, at(40)
			_, err := AppendFromRun(it, tt.add)
			assertFieldError(t, err, tt.wantPath)
			unchanged(t, before, it)
		})
	}
}

func TestAppendFromRunReusesExistingEvidence(t *testing.T) {
	it := itemIn(t, StatusProposed, false)
	ids := FreeEvidenceIDs(it, 2)
	got, err := AppendFromRun(it, RunAdditions{
		Run:      testRun2,
		At:       at(40),
		Support:  []Support{support(testRun2, "e1", ids[0], ids[1])},
		Evidence: []Evidence{evidence(ids[0]), evidence(ids[1])},
	})
	if err != nil {
		t.Fatalf("AppendFromRun: %v", err)
	}
	if len(got.Support) != 2 || len(got.Evidence) != 3 {
		t.Errorf("%d support entries and %d evidence, want 2 and 3", len(got.Support), len(got.Evidence))
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		status     Status
		unresolved bool
		to         Status
		wantErr    error
	}{
		{name: "validate a proposed item", status: StatusProposed, to: StatusValidated},
		{name: "reject a proposed item", status: StatusProposed, to: StatusRejected},
		{name: "reject a contested item", status: StatusContested, unresolved: true, to: StatusRejected},
		{name: "validate a contested item", status: StatusContested, unresolved: true, to: StatusValidated, wantErr: ErrTransitionNotAllowed},
		{name: "reject a validated item", status: StatusValidated, to: StatusRejected},
		{name: "validate a needs-review item", status: StatusNeedsReview, to: StatusValidated},
		{name: "validate a needs-review item with a disagreement", status: StatusNeedsReview, unresolved: true, to: StatusValidated, wantErr: ErrUnresolvedDisagreement},
		{name: "reject a needs-review item", status: StatusNeedsReview, unresolved: true, to: StatusRejected},
		{name: "validate a rejected item", status: StatusRejected, to: StatusValidated, wantErr: ErrTransitionNotAllowed},
		{name: "decide proposed", status: StatusContested, unresolved: true, to: StatusProposed, wantErr: ErrTransitionNotAllowed},
		{name: "decide needs-review", status: StatusValidated, to: StatusNeedsReview, wantErr: ErrTransitionNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, tt.status, tt.unresolved)
			before := it.clone()
			got, err := Decide(it, tt.to, testHuman, testReasonOK, at(50))
			unchanged(t, before, it)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			keepsGround(t, before, got)
			last := got.History[len(got.History)-1]
			if got.Status != tt.to || last.Actor != ActorHuman || last.By != testHuman || last.Reason != testReasonOK {
				t.Errorf("status %s, last event %+v", got.Status, last)
			}
		})
	}
}

func TestDecideAllowsOpenQuestions(t *testing.T) {
	it := itemIn(t, StatusProposed, false)
	it, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(5), OpenPoints: []OpenPoint{question("p1"), {ID: "p2", Kind: PointUncertainty, Summary: "s"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decide(it, StatusValidated, testHuman, testReasonOK, at(50)); err != nil {
		t.Errorf("questions and uncertainties must not block validation: %v", err)
	}
}

func TestReopen(t *testing.T) {
	tests := []struct {
		name       string
		unresolved bool
		want       Status
	}{
		{name: "without a disagreement", want: StatusProposed},
		{name: "with an unresolved disagreement", unresolved: true, want: StatusContested},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, StatusRejected, tt.unresolved)
			before := it.clone()
			got, err := Reopen(it, testHuman, "the rule exists after all", at(60))
			if err != nil {
				t.Fatalf("Reopen: %v", err)
			}
			unchanged(t, before, it)
			keepsGround(t, before, got)
			if got.Status != tt.want {
				t.Errorf("status = %s, want %s", got.Status, tt.want)
			}
		})
	}
	if _, err := Reopen(itemIn(t, StatusProposed, false), testHuman, "r", at(60)); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Errorf("reopen a proposed item: err = %v", err)
	}
	if _, err := Reopen(itemIn(t, StatusRejected, false), testHuman, "", at(60)); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("reopen without a reason: err = %v", err)
	}
}

func TestResolveOpenPoint(t *testing.T) {
	zero := 0
	t.Run("last disagreement makes a contested item proposed", func(t *testing.T) {
		it := itemIn(t, StatusContested, true)
		before := it.clone()
		got, err := ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Position: &zero, Note: "the docs are outdated"})
		if err != nil {
			t.Fatalf("ResolveOpenPoint: %v", err)
		}
		unchanged(t, before, it)
		keepsGround(t, before, got)
		if got.Status != StatusProposed {
			t.Fatalf("status = %s, want proposed", got.Status)
		}
		last := got.History[len(got.History)-1]
		if last.Actor != ActorHuman || last.By != testHuman || !last.At.Equal(at(70)) || last.Reason != ReasonDisagreementsResolved {
			t.Errorf("event = %+v", last)
		}
		if p := got.OpenPoints[0].Resolution.Position; p == nil || *p != 0 {
			t.Errorf("retained position = %v, want 0", p)
		}
		if !equalPositions(got.OpenPoints[0].Positions, before.OpenPoints[0].Positions) {
			t.Error("positions changed")
		}
	})

	t.Run("the item moves on the last disagreement only", func(t *testing.T) {
		it := itemIn(t, StatusContested, true)
		it, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(6), OpenPoints: []OpenPoint{disagreement("p2", testRun2), question("p3")}})
		if err != nil {
			t.Fatal(err)
		}
		it, err = ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Note: "n"})
		if err != nil || it.Status != StatusContested {
			t.Fatalf("after the first: status %s, err %v", it.Status, err)
		}
		it, err = ResolveOpenPoint(it, "p2", Resolution{At: at(71), By: testHuman, Note: "n"})
		if err != nil || it.Status != StatusProposed {
			t.Fatalf("after the second: status %s, err %v; an open question must not block", it.Status, err)
		}
	})

	t.Run("a needs-review item stays needs-review", func(t *testing.T) {
		it := itemIn(t, StatusNeedsReview, true)
		got, err := ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Note: "n"})
		if err != nil || got.Status != StatusNeedsReview || len(got.History) != len(it.History) {
			t.Fatalf("status %s, %d events, err %v", got.Status, len(got.History), err)
		}
	})

	t.Run("resolving a question changes no status", func(t *testing.T) {
		it := itemIn(t, StatusProposed, false)
		it, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(6), OpenPoints: []OpenPoint{question("p1")}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Note: "yes"})
		if err != nil || got.Status != StatusProposed || len(got.History) != len(it.History) {
			t.Fatalf("status %s, %d events, err %v", got.Status, len(got.History), err)
		}
	})

	refusals := []struct {
		name    string
		point   string
		res     Resolution
		wantErr error
	}{
		{name: "unknown point", point: "p7", res: Resolution{At: at(70), By: testHuman, Note: "n"}, wantErr: ErrUnknownOpenPoint},
		{name: "no note", point: "p1", res: Resolution{At: at(70), By: testHuman}, wantErr: ErrNoteRequired},
		{name: "no author", point: "p1", res: Resolution{At: at(70), Note: "n"}, wantErr: ErrActorRequired},
		{name: "no time", point: "p1", res: Resolution{By: testHuman, Note: "n"}, wantErr: ErrTimeRequired},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, StatusContested, true)
			before := it.clone()
			if _, err := ResolveOpenPoint(it, tt.point, tt.res); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			unchanged(t, before, it)
		})
	}

	t.Run("position out of range", func(t *testing.T) {
		it := itemIn(t, StatusContested, true)
		two := 2
		_, err := ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Position: &two, Note: "n"})
		assertFieldError(t, err, "open_points[0].resolution.position")
	})

	t.Run("twice", func(t *testing.T) {
		it := itemIn(t, StatusContested, true)
		it, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(6), OpenPoints: []OpenPoint{disagreement("p2", testRun2)}})
		if err != nil {
			t.Fatal(err)
		}
		it, err = ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Note: "n"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveOpenPoint(it, "p1", Resolution{At: at(71), By: testHuman, Note: "n"}); !errors.Is(err, ErrAlreadyResolved) {
			t.Fatalf("err = %v, want ErrAlreadyResolved", err)
		}
	})

	t.Run("the caller's position index is copied", func(t *testing.T) {
		it := itemIn(t, StatusContested, true)
		idx := 1
		got, err := ResolveOpenPoint(it, "p1", Resolution{At: at(70), By: testHuman, Position: &idx, Note: "n"})
		if err != nil {
			t.Fatal(err)
		}
		idx = 0
		if *got.OpenPoints[0].Resolution.Position != 1 {
			t.Error("the resolution shares memory with the caller")
		}
	})
}

func TestMarkNeedsReview(t *testing.T) {
	it := itemIn(t, StatusValidated, false)
	if _, err := MarkNeedsReview(it, testRun2, "e1 no longer holds", at(80)); !errors.Is(err, ErrNoStaleEvidence) {
		t.Errorf("without stale evidence: err = %v", err)
	}

	it.Evidence[0].State, it.Evidence[0].Reason = EvidenceStale, ReasonMissingFile
	before := it.clone()
	got, err := MarkNeedsReview(it, testRun2, "e1 no longer holds", at(80))
	if err != nil {
		t.Fatalf("MarkNeedsReview: %v", err)
	}
	unchanged(t, before, it)
	last := got.History[len(got.History)-1]
	if got.Status != StatusNeedsReview || last.Actor != ActorRun || last.By != testRun2 {
		t.Errorf("status %s, last event %+v", got.Status, last)
	}

	proposed := itemIn(t, StatusProposed, false)
	proposed.Evidence[0].State, proposed.Evidence[0].Reason = EvidenceStale, ReasonNotFound
	if _, err := MarkNeedsReview(proposed, testRun2, "r", at(80)); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Errorf("on a proposed item: err = %v", err)
	}
}

// Two results derived from the same item must not share memory, even when
// the item's slices have spare capacity that an append would write into.
func TestResultsDoNotShareMemory(t *testing.T) {
	it := itemIn(t, StatusProposed, false)
	it.History = append(make([]Event, 0, 16), it.History...)
	it.Support = append(make([]Support, 0, 16), it.Support...)
	it.Evidence = append(make([]Evidence, 0, 16), it.Evidence...)
	it.OpenPoints = make([]OpenPoint, 0, 16)

	validated, err := Decide(it, StatusValidated, testHuman, testReasonOK, at(50))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decide(it, StatusRejected, testHuman, "no", at(51)); err != nil {
		t.Fatal(err)
	}
	if last := validated.History[len(validated.History)-1]; last.To != StatusValidated {
		t.Errorf("the second decision overwrote the first one's history: %+v", last)
	}

	first := evidence("e2")
	first.Path = "billing/first.go"
	a, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(52), Support: []Support{support(testRun2, "e1")}, Evidence: []Evidence{first}, OpenPoints: []OpenPoint{question("p1")}})
	if err != nil {
		t.Fatal(err)
	}
	other := support(testRun2, "e1")
	other.Statement = "another interpretation"
	second := evidence("e2")
	second.Path = "billing/second.go"
	if _, err := AppendFromRun(it, RunAdditions{Run: testRun2, At: at(53), Support: []Support{other}, Evidence: []Evidence{second}, OpenPoints: []OpenPoint{{ID: "p1", Kind: PointUncertainty, Summary: "s"}}}); err != nil {
		t.Fatal(err)
	}
	if a.Support[1].Statement == "another interpretation" || a.Evidence[1].Path != "billing/first.go" || a.OpenPoints[0].Kind != PointQuestion {
		t.Error("the second append overwrote the first one's additions")
	}
}

func TestLifecycle(t *testing.T) {
	it := newRequirement(t)
	step := func(name string, got Item, err error, want Status) Item {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Status != want {
			t.Fatalf("%s: status %s, want %s", name, got.Status, want)
		}
		return got
	}
	got, err := AppendFromRun(it, RunAdditions{Run: testRun2, Reason: "conflict", At: at(1), OpenPoints: []OpenPoint{disagreement("p1", testRun2)}})
	it = step("disagreement", got, err, StatusContested)
	got, err = ResolveOpenPoint(it, "p1", Resolution{At: at(2), By: testHuman, Note: "docs outdated"})
	it = step("resolve", got, err, StatusProposed)
	got, err = Decide(it, StatusValidated, testHuman, testReasonOK, at(3))
	it = step("validate", got, err, StatusValidated)
	got, err = AppendFromRun(it, RunAdditions{Run: testRun2, Reason: "new conflict", At: at(4), OpenPoints: []OpenPoint{disagreement("p2", testRun2)}})
	it = step("run disagreement", got, err, StatusNeedsReview)
	got, err = Decide(it, StatusRejected, testHuman, "superseded", at(5))
	it = step("reject", got, err, StatusRejected)
	got, err = Reopen(it, testHuman, "back on the roadmap", at(6))
	it = step("reopen", got, err, StatusContested)

	if len(it.History) != 7 {
		t.Errorf("%d events, want 7", len(it.History))
	}
	if err := it.Validate(); err != nil {
		t.Errorf("final item: %v", err)
	}
}

func equalPositions(a, b []Position) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Statement != b[i].Statement || a[i].Run != b[i].Run {
			return false
		}
	}
	return true
}

// assertFieldError fails unless err is a *ValidationError naming path.
func assertFieldError(t *testing.T, err error, path string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *ValidationError", err)
	}
	for _, fe := range ve.Errors {
		if fe.Path == path {
			return
		}
	}
	t.Fatalf("no error at %s in:\n%v", path, ve)
}
