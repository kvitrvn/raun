package knowledge

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestScopeValidate(t *testing.T) {
	tests := []struct {
		scope Scope
		want  string
	}{
		{Scope{Workspace: "w", Repository: "r"}, ""},
		{Scope{Repository: "r"}, "workspace"},
		{Scope{Workspace: " ", Repository: "r"}, "workspace"},
		{Scope{Workspace: "w"}, "repository"},
		{Scope{}, "workspace"},
	}
	for _, tt := range tests {
		err := tt.scope.Validate()
		if tt.want == "" {
			if err != nil {
				t.Errorf("%+v: %v", tt.scope, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v: %v, want an error about %s", tt.scope, err, tt.want)
		}
	}
}

func TestFilter(t *testing.T) {
	it := newRequirement(t)
	tests := []struct {
		filter  Filter
		valid   bool
		matches bool
	}{
		{Filter{}, true, true},
		{Filter{Type: TypeRequirement}, true, true},
		{Filter{Type: TypePersona}, true, false},
		{Filter{Status: StatusProposed}, true, true},
		{Filter{Status: StatusValidated}, true, false},
		{Filter{Type: TypeRequirement, Status: StatusProposed}, true, true},
		{Filter{Type: "rule"}, false, false},
		{Filter{Status: "done"}, false, false},
	}
	for _, tt := range tests {
		if err := tt.filter.Validate(); (err == nil) != tt.valid {
			t.Errorf("%+v: Validate = %v", tt.filter, err)
		}
		if tt.valid && tt.filter.Matches(it) != tt.matches {
			t.Errorf("%+v: Matches = %v", tt.filter, !tt.matches)
		}
	}
}

func TestCheckCreate(t *testing.T) {
	it := newRequirement(t)
	if err := CheckCreate(it); err != nil {
		t.Fatalf("CheckCreate: %v", err)
	}
	for _, rev := range []int64{0, 2} {
		c := it.Clone()
		c.Revision = rev
		if err := CheckCreate(c); err == nil || !strings.Contains(err.Error(), "revision") {
			t.Errorf("revision %d: %v", rev, err)
		}
	}
	invalid := it.Clone()
	invalid.Title = ""
	if err := CheckCreate(invalid); err == nil {
		t.Error("an invalid item was accepted")
	}
}

func TestCheckUpdate(t *testing.T) {
	stored := newRequirement(t)
	decided, err := Decide(stored, StatusValidated, testHuman, testReasonOK, at(10))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(Item) Item
		want   error  // a sentinel, or nil
		text   string // a fragment of the error, when there is no sentinel
	}{
		{"an operation", func(Item) Item { return decided.Clone() }, nil, ""},
		{"no change", func(it Item) Item { return it }, nil, ""},
		{"identifier", func(it Item) Item { it.ID = "requirement-zzzzzz"; return it }, nil, "identifier"},
		{"type", func(it Item) Item { it.Type = TypePersona; return it }, nil, "type"},
		{"creation time", func(it Item) Item { it.CreatedAt = at(-1); return it }, nil, "creation time"},
		{"update time backwards", func(Item) Item {
			c := decided.Clone()
			c.UpdatedAt = at(-1)
			return c
		}, nil, "update time"},
		{"event removed", func(it Item) Item { it.History = nil; return it }, ErrHistoryChanged, ""},
		{"event changed", func(Item) Item {
			c := decided.Clone()
			c.History[0].Reason = "rewritten"
			return c
		}, ErrHistoryChanged, ""},
		{"event time changed", func(Item) Item {
			c := decided.Clone()
			c.History[0].At = at(1)
			return c
		}, ErrHistoryChanged, ""},
		{"invalid result", func(it Item) Item { it.Title = ""; return it }, nil, "title"},
	}
	for _, tt := range tests {
		err := CheckUpdate(stored, tt.change(stored.Clone()))
		switch {
		case tt.want == nil && tt.text == "":
			if err != nil {
				t.Errorf("%s: %v", tt.name, err)
			}
		case tt.want != nil:
			if !errors.Is(err, tt.want) {
				t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
			}
		default:
			if err == nil || !strings.Contains(err.Error(), tt.text) {
				t.Errorf("%s: %v, want an error about %s", tt.name, err, tt.text)
			}
		}
	}
}

func TestPersonaReferences(t *testing.T) {
	it := newRequirement(t)
	if refs := it.PersonaReferences(); refs != nil {
		t.Errorf("no persona: %v", refs)
	}
	it.Requirement.Personas = []string{"persona-zzzzzz", "persona-aaaaaa"}
	if refs := it.PersonaReferences(); !slices.Equal(refs, []string{"persona-aaaaaa", "persona-zzzzzz"}) {
		t.Errorf("refs = %v", refs)
	}
	if it.Requirement.Personas[0] != "persona-zzzzzz" {
		t.Error("PersonaReferences sorted the item's own slice")
	}
}

func TestClone(t *testing.T) {
	it := fullItem(t)
	c := it.Clone()
	c.Support[0].Observations[0].Evidence[0] = "changed"
	c.Evidence[0].Excerpt = "changed"
	c.History[0].Reason = "changed"
	c.OpenPoints[0].Positions[0].Agents[0] = "changed"
	c.Requirement.Statement = "changed"
	if it.Support[0].Observations[0].Evidence[0] == "changed" || it.Evidence[0].Excerpt == "changed" ||
		it.History[0].Reason == "changed" || it.OpenPoints[0].Positions[0].Agents[0] == "changed" ||
		it.Requirement.Statement == "changed" {
		t.Error("changing the clone changed the original")
	}
}
