package knowledge

import (
	"errors"
	"testing"
)

func TestValidateSamples(t *testing.T) {
	for _, it := range []*Item{samplePersona(), sampleRequirement()} {
		if err := it.Validate(); err != nil {
			t.Errorf("%s: Validate() error = %v", it.ID, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Item)
		field  string
	}{
		{"version", func(it *Item) { it.Version = 2 }, "version"},
		{"unknown type", func(it *Item) { it.Type = "epic" }, "type"},
		{"id of other type", func(it *Item) { it.ID = "requirement-k3x9q2" }, "id"},
		{"id bad suffix", func(it *Item) { it.ID = "persona-K3X9Q2" }, "id"},
		{"empty title", func(it *Item) { it.Title = " " }, "title"},
		{"unknown status", func(it *Item) { it.Status = "done"; it.History[0].To = "done" }, "status"},
		{"missing content block", func(it *Item) { it.Persona = nil }, "persona"},
		{"extra content block", func(it *Item) {
			it.Requirement = &Requirement{Statement: "x", Kind: KindFunctional}
		}, "requirement"},
		{"empty description", func(it *Item) { it.Persona.Description = "" }, "persona.description"},
		{"empty goal", func(it *Item) { it.Persona.Goals = []string{""} }, "persona.goals[0]"},
		{"bad nature", func(it *Item) { it.Support[0].Nature = "certain" }, "support[0].nature"},
		{"supported without observation", func(it *Item) { it.Support[0].Observations = nil }, "support[0].observations"},
		{"observation without evidence", func(it *Item) { it.Support[0].Observations[0].Evidence = nil }, "support[0].observations[0].evidence"},
		{"dangling evidence ref", func(it *Item) { it.Support[0].Observations[0].Evidence = []string{"e9"} }, "support[0].observations[0].evidence[0]"},
		{"evidence bad id", func(it *Item) { it.Evidence[0].ID = "E1" }, "evidence[0].id"},
		{"evidence absolute path", func(it *Item) { it.Evidence[0].Path = "/etc/passwd" }, "evidence[0].path"},
		{"evidence escaping path", func(it *Item) { it.Evidence[0].Path = "../x.go" }, "evidence[0].path"},
		{"evidence unclean path", func(it *Item) { it.Evidence[0].Path = "a/./b.go" }, "evidence[0].path"},
		{"evidence short commit", func(it *Item) { it.Evidence[0].Commit = "3f1c2a9" }, "evidence[0].commit"},
		{"evidence reversed lines", func(it *Item) { it.Evidence[0].StartLine = 20 }, "evidence[0].start_line"},
		{"evidence tampered excerpt", func(it *Item) { it.Evidence[0].Excerpt = "changed" }, "evidence[0].sha256"},
		{"evidence unknown state", func(it *Item) { it.Evidence[0].State = "trusted" }, "evidence[0].state"},
		{"evidence unknown source", func(it *Item) { it.Evidence[0].Source = "llm" }, "evidence[0].source"},
		{"point unknown kind", func(it *Item) { it.OpenPoints[0].Kind = "risk" }, "open_points[0].kind"},
		{"point duplicate id", func(it *Item) {
			it.OpenPoints = append(it.OpenPoints, it.OpenPoints[0])
		}, "open_points[1].id"},
		{"no history", func(it *Item) { it.History = nil }, "history"},
		{"history unknown actor", func(it *Item) { it.History[0].Actor = "llm" }, "history[0].actor"},
		{"status differs from history", func(it *Item) { it.Status = StatusRejected }, "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := samplePersona()
			tt.mutate(it)
			assertFieldError(t, it.Validate(), tt.field)
		})
	}
}

func TestValidateRequirementRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Item)
		field  string
	}{
		{"bad kind", func(it *Item) { it.Requirement.Kind = "nice-to-have" }, "requirement.kind"},
		{"persona ref not a persona id", func(it *Item) { it.Requirement.Personas = []string{"requirement-8fz2mc"} }, "requirement.personas[0]"},
		{"disagreement with one position", func(it *Item) {
			it.OpenPoints[0].Positions = it.OpenPoints[0].Positions[:1]
		}, "open_points[0].positions"},
		{"position without agents", func(it *Item) { it.OpenPoints[0].Positions[1].Agents = nil }, "open_points[0].positions[1].agents"},
		{"position dangling evidence", func(it *Item) { it.OpenPoints[0].Positions[1].Evidence = []string{"e7"} }, "open_points[0].positions[1].evidence[0]"},
		{"resolution out of range", func(it *Item) {
			resolveAll(it)
			bad := 5
			it.OpenPoints[0].Resolution.Position = &bad
		}, "open_points[0].resolution.position"},
		{"resolution without note", func(it *Item) {
			resolveAll(it)
			it.OpenPoints[0].Resolution.Note = ""
		}, "open_points[0].resolution.note"},
		{"validated with open disagreement", func(it *Item) {
			it.Status = StatusValidated
			it.History = append(it.History, Event{At: testTime, Actor: ActorHuman, By: testPerson, From: StatusContested, To: StatusValidated})
		}, "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := sampleRequirement()
			tt.mutate(it)
			assertFieldError(t, it.Validate(), tt.field)
		})
	}
}

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Validate() = nil, want error on %q", field)
	}
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		var fe *FieldError
		if errors.As(e, &fe) && fe.Field == field {
			return
		}
	}
	t.Errorf("no error on field %q; got: %v", field, err)
}
