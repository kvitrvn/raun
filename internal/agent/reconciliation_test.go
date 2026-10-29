package agent

import (
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/knowledge"
)

var (
	existingItems = map[string]knowledge.Type{
		"persona-k3x9q2":     knowledge.TypePersona,
		"requirement-8fz2mc": knowledge.TypeRequirement,
	}
	runItems = map[string]knowledge.Type{
		"n1": knowledge.TypePersona,
		"n2": knowledge.TypeRequirement,
		"n3": knowledge.TypeRequirement,
	}
)

const validReconciliation = `{
  "version": 1,
  "items": [
    {"id": "n1", "match": "persona-k3x9q2", "conflict": ""},
    {"id": "n2", "match": "requirement-8fz2mc", "conflict": "The code now allows corrections."},
    {"id": "n3", "match": "", "conflict": ""}
  ]
}`

func TestParseReconciliationValid(t *testing.T) {
	r, _, err := ParseReconciliation([]byte(validReconciliation), existingItems, runItems, false)
	if err != nil {
		t.Fatalf("ParseReconciliation() error = %v", err)
	}
	if len(r.Items) != 3 || r.Items[1].Conflict == "" || r.Items[2].Match != "" {
		t.Errorf("unexpected reconciliation: %+v", r)
	}

	// Several run items may match the same existing item.
	same := strings.Replace(validReconciliation, `"id": "n3", "match": ""`, `"id": "n3", "match": "requirement-8fz2mc"`, 1)
	if _, _, err := ParseReconciliation([]byte(same), existingItems, runItems, false); err != nil {
		t.Errorf("two run items on one existing item: %v", err)
	}
}

func TestParseReconciliationInvalid(t *testing.T) {
	tests := []struct {
		name    string
		replace [2]string
		want    string
	}{
		{"wrong version", [2]string{`"version": 1`, `"version": 2`}, "version: must be 1, got 2"},
		{"content is not part of the contract", [2]string{`"conflict": ""}`, `"conflict": "", "title": "x"}`}, `unknown field "title"`},
		{"unknown run item", [2]string{`"id": "n3"`, `"id": "n9"`}, `items[2].id: unknown run item "n9"`},
		{"placed twice", [2]string{`"id": "n3"`, `"id": "n2"`}, `items[2].id: run item "n2" is already placed`},
		{"dropped run item", [2]string{`,
    {"id": "n3", "match": "", "conflict": ""}`, ``}, "every run item must be placed; missing: n3"},
		{"unknown existing item", [2]string{`"match": "persona-k3x9q2"`, `"match": "persona-zzzzzz"`}, `items[0].match: unknown existing item "persona-zzzzzz"`},
		{"type mismatch", [2]string{`"match": "persona-k3x9q2"`, `"match": "requirement-8fz2mc"`}, `existing item "requirement-8fz2mc" is a requirement, not a persona`},
		{"conflict without match", [2]string{`{"id": "n3", "match": "", "conflict": ""}`, `{"id": "n3", "match": "", "conflict": "Contradicts."}`}, "items[2].conflict: a conflict needs a match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.Replace(validReconciliation, tt.replace[0], tt.replace[1], 1)
			if in == validReconciliation {
				t.Fatal("replacement did not apply")
			}
			_, _, err := ParseReconciliation([]byte(in), existingItems, runItems, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
