package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
)

// ConsolidationVersion is the lead consolidation contract version.
const ConsolidationVersion = 1

// Consolidation is what the lead returns: the analysts' interpretations
// grouped into knowledge items. The lead groups and arbitrates; it cites no
// evidence of its own and discards no interpretation. See docs/agents.md.
type Consolidation struct {
	Version int           `json:"version"`
	Items   []PlannedItem `json:"items"`
}

// PlannedItem is one knowledge item proposed by the lead.
type PlannedItem struct {
	ID    string         `json:"id"`
	Type  knowledge.Type `json:"type"`
	Title string         `json:"title"`
	// Requirement.Personas holds IDs of persona items of this consolidation.
	Persona     *PersonaContent     `json:"persona,omitempty"`
	Requirement *RequirementContent `json:"requirement,omitempty"`
	// Support lists the interpretations merged into this item, as
	// anonymized references such as "A.i1".
	Support       []string       `json:"support"`
	Disagreements []Disagreement `json:"disagreements"`
	Uncertainties []string       `json:"uncertainties"`
	Questions     []string       `json:"questions"`
}

// Disagreement records interpretations of one item that conflict.
type Disagreement struct {
	Summary   string     `json:"summary"`
	Positions []Position `json:"positions"`
}

// Position is one side of a disagreement, backed by interpretations.
type Position struct {
	Statement string   `json:"statement"`
	Support   []string `json:"support"`
}

// ParseConsolidation extracts, strictly decodes and validates a lead
// consolidation. interpretations maps every reference shown to the lead
// (such as "A.i1") to its knowledge type.
func ParseConsolidation(out []byte, allowed []knowledge.Type, interpretations map[string]knowledge.Type) (*Consolidation, []byte, error) {
	data, err := ExtractJSON(out)
	if err != nil {
		return nil, nil, err
	}
	var c Consolidation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, data, fmt.Errorf("decode consolidation: %w", err)
	}
	if err := c.Validate(allowed, interpretations); err != nil {
		return nil, data, err
	}
	return &c, data, nil
}

// Validate checks the consolidation against the interpretations the lead
// was given. Every interpretation must be placed in exactly one item.
func (c *Consolidation) Validate(allowed []knowledge.Type, interpretations map[string]knowledge.Type) error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	nonEmpty := func(field, s string) {
		if strings.TrimSpace(s) == "" {
			add(field, "must not be empty")
		}
	}

	if c.Version != ConsolidationVersion {
		add("version", "must be %d, got %d", ConsolidationVersion, c.Version)
	}

	itemTypes := map[string]knowledge.Type{}
	for i, it := range c.Items {
		f := fmt.Sprintf("items[%d].id", i)
		switch {
		case !reportIDPattern.MatchString(it.ID):
			add(f, "must be a short identifier such as \"k1\", got %q", it.ID)
		case itemTypes[it.ID] != "":
			add(f, "duplicate id %q", it.ID)
		default:
			itemTypes[it.ID] = it.Type
		}
	}

	placed := map[string]string{} // interpretation -> item field
	for i, it := range c.Items {
		f := fmt.Sprintf("items[%d]", i)
		if !slices.Contains(allowed, it.Type) {
			add(f+".type", "must be one of %v, got %q", allowed, it.Type)
		}
		nonEmpty(f+".title", it.Title)
		validateContent(f, it.Type, it.Persona, it.Requirement, add, func(ref string) bool {
			return itemTypes[ref] == knowledge.TypePersona
		}, "unknown persona item %q")

		if len(it.Support) == 0 {
			add(f+".support", "must list at least one interpretation")
		}
		for j, ref := range it.Support {
			sf := fmt.Sprintf("%s.support[%d]", f, j)
			t, ok := interpretations[ref]
			switch {
			case !ok:
				add(sf, "unknown interpretation %q", ref)
			case t != it.Type:
				add(sf, "interpretation %q is a %s, not a %s", ref, t, it.Type)
			case placed[ref] != "":
				add(sf, "interpretation %q is already placed in %s", ref, placed[ref])
			default:
				placed[ref] = f
			}
		}

		for j, d := range it.Disagreements {
			df := fmt.Sprintf("%s.disagreements[%d]", f, j)
			nonEmpty(df+".summary", d.Summary)
			if len(d.Positions) < 2 {
				add(df+".positions", "a disagreement needs at least two positions")
			}
			seen := map[string]bool{}
			for k, p := range d.Positions {
				pf := fmt.Sprintf("%s.positions[%d]", df, k)
				nonEmpty(pf+".statement", p.Statement)
				if len(p.Support) == 0 {
					add(pf+".support", "must list at least one interpretation")
				}
				for l, ref := range p.Support {
					switch {
					case !slices.Contains(it.Support, ref):
						add(fmt.Sprintf("%s.support[%d]", pf, l), "interpretation %q is not in the item's support", ref)
					case seen[ref]:
						add(fmt.Sprintf("%s.support[%d]", pf, l), "interpretation %q backs two positions", ref)
					}
					seen[ref] = true
				}
			}
		}
		for j, u := range it.Uncertainties {
			nonEmpty(fmt.Sprintf("%s.uncertainties[%d]", f, j), u)
		}
		for j, q := range it.Questions {
			nonEmpty(fmt.Sprintf("%s.questions[%d]", f, j), q)
		}
	}

	var missing []string
	for ref := range interpretations {
		if placed[ref] == "" {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		add("items", "every interpretation must be placed in an item; missing: %s", strings.Join(missing, ", "))
	}

	return errors.Join(errs...)
}
