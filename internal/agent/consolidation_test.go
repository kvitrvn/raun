package agent

import (
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/knowledge"
)

var shown = map[string]knowledge.Type{
	"A.i1": knowledge.TypePersona,
	"A.i2": knowledge.TypeRequirement,
	"B.i1": knowledge.TypePersona,
	"B.i2": knowledge.TypeRequirement,
}

const validConsolidation = `{
  "version": 1,
  "items": [
    {"id": "k1", "type": "persona", "title": "Accountant",
     "persona": {"description": "Issues invoices.", "goals": [], "capabilities": []},
     "support": ["A.i1", "B.i1"], "disagreements": [], "uncertainties": [], "questions": []},
    {"id": "k2", "type": "requirement", "title": "Invoice corrections",
     "requirement": {"statement": "Issued invoices are immutable.", "kind": "business-rule", "rationale": "", "personas": ["k1"]},
     "support": ["A.i2", "B.i2"],
     "disagreements": [{"summary": "Code forbids edits, docs allow 24h.",
       "positions": [{"statement": "Never editable.", "support": ["A.i2"]}, {"statement": "Editable for 24h.", "support": ["B.i2"]}]}],
     "uncertainties": ["Which source is authoritative is unclear."],
     "questions": ["Should the docs be fixed?"]}
  ]
}`

func TestParseConsolidationValid(t *testing.T) {
	c, _, err := ParseConsolidation([]byte(validConsolidation), allTypes, shown)
	if err != nil {
		t.Fatalf("ParseConsolidation() error = %v", err)
	}
	if len(c.Items) != 2 || len(c.Items[1].Disagreements[0].Positions) != 2 {
		t.Errorf("unexpected consolidation: %+v", c)
	}
}

func TestParseConsolidationInvalid(t *testing.T) {
	tests := []struct {
		name    string
		replace [2]string
		want    string
	}{
		{"evidence is not part of the contract", [2]string{`"uncertainties": [],`, `"uncertainties": [], "evidence": [],`}, `unknown field "evidence"`},
		{"unknown interpretation", [2]string{`["A.i1", "B.i1"]`, `["A.i1", "Z.i9"]`}, `items[0].support[1]: unknown interpretation "Z.i9"`},
		{"dropped interpretation", [2]string{`["A.i1", "B.i1"]`, `["A.i1"]`}, "missing: B.i1"},
		{"placed twice", [2]string{`["A.i1", "B.i1"]`, `["A.i1", "B.i1", "A.i1"]`}, `items[0].support[2]: interpretation "A.i1" is already placed in items[0]`},
		{"type mismatch", [2]string{`["A.i1", "B.i1"]`, `["A.i1", "B.i2"]`}, `is a requirement, not a persona`},
		{"one-sided disagreement", [2]string{`, {"statement": "Editable for 24h.", "support": ["B.i2"]}`, ``}, "positions: a disagreement needs at least two positions"},
		{"position outside support", [2]string{`"support": ["B.i2"]}]`, `"support": ["B.i1"]}]`}, `"B.i1" is not in the item's support`},
		{"same interpretation on both sides", [2]string{`"support": ["B.i2"]}]`, `"support": ["A.i2"]}]`}, `"A.i2" backs two positions`},
		{"persona link to unknown item", [2]string{`"personas": ["k1"]`, `"personas": ["k7"]`}, `unknown persona item "k7"`},
		{"duplicate item id", [2]string{`"id": "k2"`, `"id": "k1"`}, `duplicate id "k1"`},
		{"empty uncertainty", [2]string{`["Which source is authoritative is unclear."]`, `[" "]`}, "uncertainties[0]: must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.Replace(validConsolidation, tt.replace[0], tt.replace[1], 1)
			if in == validConsolidation {
				t.Fatal("replacement did not apply")
			}
			_, _, err := ParseConsolidation([]byte(in), allTypes, shown)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
