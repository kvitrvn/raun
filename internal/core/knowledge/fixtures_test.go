package knowledge

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Fixtures shared by the tests of this package. Items are built through the
// operations themselves, so that their history is valid.

const (
	testRun      = "run-20261111-a1"
	testRun2     = "run-20261112-b2"
	testHuman    = "account-42"
	testReqID    = "requirement-8fz2mc"
	testPerID    = "persona-k3x9q2"
	testExcerpt  = "if inv.Status == StatusIssued {\n\treturn ErrInvoiceLocked\n}"
	testReasonOK = "checked against the billing rules"
)

var (
	testCommit = strings.Repeat("3f1c2a9b", 5)
	t0         = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
)

// at returns t0 plus n minutes.
func at(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

func evidence(id string) Evidence {
	return Evidence{
		ID:        id,
		Source:    SourceCode,
		Path:      "billing/invoice.go",
		Commit:    testCommit,
		StartLine: 40,
		EndLine:   42,
		Excerpt:   testExcerpt,
		SHA256:    ExcerptSHA256(testExcerpt),
		State:     EvidenceVerified,
	}
}

func support(run string, evidenceIDs ...string) Support {
	return Support{
		Run:            run,
		Agent:          "alpha",
		Interpretation: "i1",
		Statement:      "Issued invoices are read-only.",
		Nature:         NatureSupported,
		Observations: []Observation{{
			Statement: "Update rejects invoices with status issued.",
			Evidence:  evidenceIDs,
		}},
	}
}

func disagreement(id, run string) OpenPoint {
	return OpenPoint{
		ID:       id,
		Kind:     PointDisagreement,
		Summary:  "Code forbids edits, docs allow a 24h correction window.",
		RaisedBy: "lead",
		Positions: []Position{
			{Statement: "Issued invoices are never editable.", Run: run, Agents: []string{"alpha"}, Evidence: []string{"e1"}},
			{Statement: "Admins can edit within 24 hours.", Run: run, Agents: []string{"beta"}},
		},
	}
}

func question(id string) OpenPoint {
	return OpenPoint{ID: id, Kind: PointQuestion, Summary: "Does the rule apply to credit notes?"}
}

// newRequirement returns a proposed requirement with one support entry and
// one evidence.
func newRequirement(t *testing.T) Item {
	t.Helper()
	it, err := New(Draft{
		ID:          testReqID,
		Type:        TypeRequirement,
		Title:       "Issued invoices are immutable",
		Requirement: &Requirement{Statement: "An invoice cannot be edited once issued.", Kind: KindBusinessRule},
	}, testRun, "", at(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it, err = AppendFromRun(it, RunAdditions{
		Run:      testRun,
		At:       at(0),
		Support:  []Support{support(testRun, "e1")},
		Evidence: []Evidence{evidence("e1")},
	})
	if err != nil {
		t.Fatalf("AppendFromRun: %v", err)
	}
	return it
}

// itemIn returns a valid requirement in the given status. unresolved asks for
// an unresolved disagreement, which contested requires and proposed and
// validated forbid.
func itemIn(t *testing.T, status Status, unresolved bool) Item {
	t.Helper()
	it := newRequirement(t)
	addDisagreement := func(it Item) Item {
		t.Helper()
		out, err := AppendFromRun(it, RunAdditions{Run: testRun2, Reason: "the lead found a conflict", At: at(5), OpenPoints: []OpenPoint{disagreement("p1", testRun2)}})
		if err != nil {
			t.Fatalf("add disagreement: %v", err)
		}
		return out
	}
	must := func(it Item, err error) Item {
		t.Helper()
		if err != nil {
			t.Fatalf("build %s item: %v", status, err)
		}
		return it
	}
	switch status {
	case StatusProposed:
		if unresolved {
			t.Fatal("a proposed item holds no unresolved disagreement")
		}
	case StatusContested:
		if !unresolved {
			t.Fatal("a contested item holds an unresolved disagreement")
		}
		it = addDisagreement(it)
	case StatusValidated:
		if unresolved {
			t.Fatal("a validated item holds no unresolved disagreement")
		}
		it = must(Decide(it, StatusValidated, testHuman, testReasonOK, at(10)))
	case StatusRejected:
		if unresolved {
			it = addDisagreement(it)
		}
		it = must(Decide(it, StatusRejected, testHuman, "not a rule of the product", at(10)))
	case StatusNeedsReview:
		it = must(Decide(it, StatusValidated, testHuman, testReasonOK, at(10)))
		if unresolved {
			it = addDisagreement(it)
		} else {
			it.Evidence[0].State = EvidenceStale
			it.Evidence[0].Reason = ReasonNotFound
			it = must(MarkNeedsReview(it, testRun2, "e1 no longer holds", at(20)))
		}
	default:
		t.Fatalf("unknown status %q", status)
	}
	if it.Status != status {
		t.Fatalf("built a %s item, want %s", it.Status, status)
	}
	if err := it.Validate(); err != nil {
		t.Fatalf("fixture %s is invalid: %v", status, err)
	}
	return it
}

// unchanged fails the test if the operation changed its input.
func unchanged(t *testing.T, before, after Item) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the operation changed its input:\nbefore %+v\nafter  %+v", before, after)
	}
}

// keepsGround fails the test if the operation rewrote what no operation may
// rewrite: title, content, support, evidence, earlier events and revision.
func keepsGround(t *testing.T, before, after Item) {
	t.Helper()
	if after.Title != before.Title || !reflect.DeepEqual(after.Requirement, before.Requirement) || !reflect.DeepEqual(after.Persona, before.Persona) {
		t.Error("title or type-specific content changed")
	}
	if !reflect.DeepEqual(after.Support[:len(before.Support)], before.Support) {
		t.Error("existing support changed")
	}
	if !reflect.DeepEqual(after.Evidence[:len(before.Evidence)], before.Evidence) {
		t.Error("existing evidence changed")
	}
	if len(after.History) < len(before.History) || !reflect.DeepEqual(after.History[:len(before.History)], before.History) {
		t.Error("existing history changed")
	}
	if after.Revision != before.Revision {
		t.Errorf("revision changed from %d to %d", before.Revision, after.Revision)
	}
}
