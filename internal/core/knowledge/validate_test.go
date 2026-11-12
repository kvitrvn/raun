package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fullItem returns a valid needs-review requirement that uses every field:
// support, evidence of each state, a resolved and an unresolved disagreement,
// a question, and a long history.
func fullItem(t *testing.T) Item {
	t.Helper()
	it := itemIn(t, StatusNeedsReview, true)
	stale := evidence("e2")
	stale.State, stale.Reason = EvidenceStale, ReasonAmbiguous
	stale.CheckedAt, stale.CheckedCommit = at(90), strings.Repeat("b", 64)
	badRange := evidence("e3")
	badRange.State, badRange.Reason, badRange.StartLine, badRange.EndLine = EvidenceInvalid, ReasonBadRange, 9, 2
	relocated := evidence("e4")
	relocated.State = EvidenceRelocated
	hypothesis := Support{Run: testRun2, Agent: "beta", Interpretation: "i3", Statement: "Probably legal.", Nature: NatureHypothesis}
	it, err := AppendFromRun(it, RunAdditions{
		Run:        testRun2,
		At:         at(90),
		Support:    []Support{hypothesis},
		Evidence:   []Evidence{stale, badRange, relocated},
		OpenPoints: []OpenPoint{disagreement("p2", testRun2), question("p3")},
	})
	if err != nil {
		t.Fatalf("fullItem: %v", err)
	}
	zero := 0
	it, err = ResolveOpenPoint(it, "p1", Resolution{At: at(91), By: testHuman, Position: &zero, Note: "the code wins"})
	if err != nil {
		t.Fatalf("fullItem: %v", err)
	}
	return it
}

func TestValidateAcceptsValidItems(t *testing.T) {
	for _, it := range []Item{fullItem(t), itemIn(t, StatusContested, true), itemIn(t, StatusRejected, true)} {
		if err := it.Validate(); err != nil {
			t.Errorf("%s item: %v", it.Status, err)
		}
	}
}

func TestValidateReportsFieldPaths(t *testing.T) {
	tests := []struct {
		name   string
		change func(it *Item)
		path   string
	}{
		{"malformed identifier", func(it *Item) { it.ID = "requirement-8FZ2MC" }, "id"},
		{"identifier of another type", func(it *Item) { it.ID = testPerID }, "id"},
		{"unknown type", func(it *Item) { it.Type = "epic" }, "type"},
		{"missing title", func(it *Item) { it.Title = " " }, "title"},
		{"unknown status", func(it *Item) { it.Status = "draft" }, "status"},
		{"revision zero", func(it *Item) { it.Revision = 0 }, "revision"},
		{"local time", func(it *Item) { it.CreatedAt = it.CreatedAt.In(time.FixedZone("CET", 3600)) }, "created_at"},
		{"updated before created", func(it *Item) { it.UpdatedAt = it.CreatedAt.Add(-time.Second) }, "updated_at"},
		{"missing requirement", func(it *Item) { it.Requirement = nil }, "requirement"},
		{"persona on a requirement", func(it *Item) { it.Persona = &Persona{Description: "d"} }, "persona"},
		{"missing statement", func(it *Item) { it.Requirement.Statement = "" }, "requirement.statement"},
		{"unknown kind", func(it *Item) { it.Requirement.Kind = "wish" }, "requirement.kind"},
		{"persona reference of another type", func(it *Item) { it.Requirement.Personas = []string{testReqID} }, "requirement.personas[0]"},
		{"duplicate persona reference", func(it *Item) { it.Requirement.Personas = []string{testPerID, testPerID} }, "requirement.personas[1]"},

		{"support without run", func(it *Item) { it.Support[0].Run = "" }, "support[0].run"},
		{"unknown nature", func(it *Item) { it.Support[0].Nature = "guess" }, "support[0].nature"},
		{"supported without observation", func(it *Item) { it.Support[0].Observations = nil }, "support[0].observations"},
		{"observation without evidence", func(it *Item) { it.Support[0].Observations[0].Evidence = nil }, "support[0].observations[0].evidence"},
		{"dangling observation evidence", func(it *Item) { it.Support[0].Observations[0].Evidence = []string{"e9"} }, "support[0].observations[0].evidence[0]"},

		{"malformed evidence identifier", func(it *Item) { it.Evidence[0].ID = "E1" }, "evidence[0].id"},
		{"duplicate evidence identifier", func(it *Item) { it.Evidence[1].ID = "e1" }, "evidence[1].id"},
		{"unknown source", func(it *Item) { it.Evidence[0].Source = "chat" }, "evidence[0].source"},
		{"absolute path", func(it *Item) { it.Evidence[0].Path = "/etc/passwd" }, "evidence[0].path"},
		{"path leaving the repository", func(it *Item) { it.Evidence[0].Path = "../secrets" }, "evidence[0].path"},
		{"unclean path", func(it *Item) { it.Evidence[0].Path = "billing//invoice.go" }, "evidence[0].path"},
		{"backslash path", func(it *Item) { it.Evidence[0].Path = `billing\invoice.go` }, "evidence[0].path"},
		{"dot path", func(it *Item) { it.Evidence[0].Path = "." }, "evidence[0].path"},
		{"path read as an option", func(it *Item) { it.Evidence[0].Path = "-c" }, "evidence[0].path"},
		{"short commit", func(it *Item) { it.Evidence[0].Commit = "3f1c2a9" }, "evidence[0].commit"},
		{"uppercase commit", func(it *Item) { it.Evidence[0].Commit = strings.ToUpper(testCommit) }, "evidence[0].commit"},
		{"empty excerpt", func(it *Item) { it.Evidence[0].Excerpt = "" }, "evidence[0].excerpt"},
		{"sha256 of another excerpt", func(it *Item) { it.Evidence[0].SHA256 = ExcerptSHA256("other") }, "evidence[0].sha256"},
		{"unknown state", func(it *Item) { it.Evidence[0].State = "fine" }, "evidence[0].state"},
		{"reason on verified evidence", func(it *Item) { it.Evidence[0].Reason = ReasonNotFound }, "evidence[0].reason"},
		{"stale without reason", func(it *Item) { it.Evidence[1].Reason = "" }, "evidence[1].reason"},
		{"stale too long", func(it *Item) { it.Evidence[1].Reason = ReasonTooLong }, "evidence[1].reason"},
		{"unknown reason", func(it *Item) { it.Evidence[1].Reason = "gone" }, "evidence[1].reason"},
		{"line zero", func(it *Item) { it.Evidence[0].StartLine = 0 }, "evidence[0].start_line"},
		{"reversed range", func(it *Item) { it.Evidence[0].EndLine = 39 }, "evidence[0].end_line"},
		{"local checked time", func(it *Item) { it.Evidence[1].CheckedAt = at(90).In(time.FixedZone("CET", 3600)) }, "evidence[1].checked_at"},
		{"short checked commit", func(it *Item) { it.Evidence[1].CheckedCommit = "abc" }, "evidence[1].checked_commit"},

		{"malformed point identifier", func(it *Item) { it.OpenPoints[0].ID = "q1" }, "open_points[0].id"},
		{"duplicate point identifier", func(it *Item) { it.OpenPoints[1].ID = "p1" }, "open_points[1].id"},
		{"unknown point kind", func(it *Item) { it.OpenPoints[2].Kind = "remark" }, "open_points[2].kind"},
		{"missing summary", func(it *Item) { it.OpenPoints[0].Summary = "" }, "open_points[0].summary"},
		{"single position", func(it *Item) { it.OpenPoints[1].Positions = it.OpenPoints[1].Positions[:1] }, "open_points[1].positions"},
		{"position without run", func(it *Item) { it.OpenPoints[1].Positions[0].Run = "" }, "open_points[1].positions[0].run"},
		{"dangling position evidence", func(it *Item) { it.OpenPoints[1].Positions[0].Evidence = []string{"e9"} }, "open_points[1].positions[0].evidence[0]"},
		{"resolution without note", func(it *Item) { it.OpenPoints[0].Resolution.Note = "" }, "open_points[0].resolution.note"},
		{"resolution without author", func(it *Item) { it.OpenPoints[0].Resolution.By = "" }, "open_points[0].resolution.by"},
		{"negative position", func(it *Item) { n := -1; it.OpenPoints[0].Resolution.Position = &n }, "open_points[0].resolution.position"},

		{"no history", func(it *Item) { it.History = nil }, "history"},
		{"creation by a human", func(it *Item) { it.History[0].Actor = ActorHuman }, "history[0]"},
		{"creation into another status", func(it *Item) { it.History[0].To = StatusValidated }, "history[0]"},
		{"broken chain", func(it *Item) { it.History[2].From = StatusRejected }, "history[2].from"},
		{"transition missing from the table", func(it *Item) {
			it.History = append(it.History, Event{At: at(95), Actor: ActorHuman, By: testHuman, From: StatusNeedsReview, To: StatusContested, Reason: "r"})
		}, "history[3]"},
		{"transition by the wrong actor", func(it *Item) { it.History[1].Actor = ActorRun }, "history[1].actor"},
		{"event without author", func(it *Item) { it.History[1].By = "" }, "history[1].by"},
		{"event without reason", func(it *Item) { it.History[1].Reason = "" }, "history[1].reason"},
		{"status other than the last event", func(it *Item) { it.Status = StatusValidated }, "status"},
		{"event before the previous one", func(it *Item) { it.History[2].At = it.History[1].At.Add(-time.Second) }, "history[2].at"},
		{"updated before the last event", func(it *Item) { it.UpdatedAt = it.History[2].At.Add(-time.Second) }, "updated_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := fullItem(t)
			tt.change(&it)
			assertFieldError(t, it.Validate(), tt.path)
		})
	}
}

func TestValidateStatusAndDisagreements(t *testing.T) {
	contested := itemIn(t, StatusContested, true)
	contested.OpenPoints[0].Resolution = &Resolution{At: at(6), By: testHuman, Note: "n"}
	assertFieldError(t, contested.Validate(), "status")

	proposed := itemIn(t, StatusProposed, false)
	proposed.OpenPoints = []OpenPoint{disagreement("p1", testRun)}
	assertFieldError(t, proposed.Validate(), "status")
}

// A malformed identifier is reported once for its shape, and never counts as
// an evidence or open point that a reference may name.
func TestValidateMalformedLocalIDs(t *testing.T) {
	it := fullItem(t)
	it.Evidence[0].ID = "E1"
	it.Evidence[1].ID = "E1"
	err := it.Validate()
	assertFieldError(t, err, "evidence[0].id")
	assertFieldError(t, err, "evidence[1].id")
	assertFieldError(t, err, "support[0].observations[0].evidence[0]")
	for _, fe := range err.(*ValidationError).Errors {
		if strings.Contains(fe.Message, "duplicate") {
			t.Errorf("malformed identifier reported as a duplicate: %v", fe)
		}
	}
}

func TestValidatePersona(t *testing.T) {
	it, err := New(Draft{ID: testPerID, Type: TypePersona, Title: "Accountant", Persona: &Persona{Description: "Issues invoices."}}, testRun, "", at(0))
	if err != nil {
		t.Fatal(err)
	}
	it.Persona.Description = ""
	assertFieldError(t, it.Validate(), "persona.description")
	it.Persona = nil
	assertFieldError(t, it.Validate(), "persona")
}

func TestValidationErrorListsEveryProblem(t *testing.T) {
	it := fullItem(t)
	it.Title = ""
	it.Evidence[0].Source = "chat"
	err := it.Validate()
	var ve *ValidationError
	assertFieldError(t, err, "title")
	assertFieldError(t, err, "evidence[0].source")
	if ve = err.(*ValidationError); len(ve.Errors) != 2 {
		t.Errorf("%d errors, want 2: %v", len(ve.Errors), ve)
	}
	if msg := err.Error(); !strings.Contains(msg, "title: required") || !strings.Contains(msg, `evidence[0].source: unknown source "chat"`) {
		t.Errorf("Error() = %q", msg)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	it := fullItem(t)
	data, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	var back Item
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(it, back) {
		t.Errorf("round trip changed the item:\n%s", data)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("decoded item: %v", err)
	}
	again, _ := json.Marshal(back)
	if string(again) != string(data) {
		t.Error("encoding is not deterministic")
	}
}

func TestExcerptSHA256(t *testing.T) {
	if got := ExcerptSHA256("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("ExcerptSHA256(abc) = %s", got)
	}
}

func TestReasonGoesWithState(t *testing.T) {
	// Reasons that describe the citation itself hold at the cited commit only.
	atCitedCommitOnly := map[Reason]bool{ReasonTooLong: true, ReasonBadRange: true}
	reasons := []Reason{ReasonNotFound, ReasonAmbiguous, ReasonMissingFile, ReasonNotRegular,
		ReasonBinary, ReasonTooLarge, ReasonTooLong, ReasonBadRange}
	for _, r := range reasons {
		if !r.Valid() {
			t.Errorf("%s is not valid", r)
		}
		if !r.allows(EvidenceInvalid) {
			t.Errorf("%s refused with invalid", r)
		}
		if r.allows(EvidenceStale) == atCitedCommitOnly[r] {
			t.Errorf("%s with stale: allowed = %v", r, r.allows(EvidenceStale))
		}
		for _, s := range []EvidenceState{EvidenceVerified, EvidenceRelocated} {
			if r.allows(s) {
				t.Errorf("%s allowed with %s", r, s)
			}
		}
	}
	if Reason("too-big").Valid() {
		t.Error("an unknown reason is valid")
	}
}
