package consolidate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/knowledge"
)

// Labels returns anonymous labels ("A", "B", …) for n reports, assigned in
// the order given by perm (a permutation of 0..n-1), so that the label
// order does not reveal the configuration order. labels[i] is report i's.
func Labels(perm []int) []string {
	labels := make([]string, len(perm))
	for pos, report := range perm {
		labels[report] = string(rune('A' + pos))
	}
	return labels
}

// leadInput is what the lead sees: anonymized reports, each citation
// annotated with Raun's verification outcome.
type leadInput struct {
	Reports []leadReport `json:"reports"`
}

type leadReport struct {
	Label           string               `json:"label"`
	Observations    []leadObservation    `json:"observations"`
	Interpretations []leadInterpretation `json:"interpretations"`
	Questions       []leadQuestion       `json:"questions"`
}

type leadObservation struct {
	ID        string         `json:"id"`
	Statement string         `json:"statement"`
	Evidence  []leadEvidence `json:"evidence"`
}

type leadEvidence struct {
	Path         string `json:"path"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Excerpt      string `json:"excerpt"`
	Verification string `json:"verification"`
	Reason       string `json:"reason,omitempty"`
}

type leadInterpretation struct {
	ID           string                    `json:"id"`
	Type         knowledge.Type            `json:"type"`
	Title        string                    `json:"title"`
	Statement    string                    `json:"statement"`
	Nature       knowledge.Nature          `json:"nature"`
	Observations []string                  `json:"observations"`
	Persona      *agent.PersonaContent     `json:"persona,omitempty"`
	Requirement  *agent.RequirementContent `json:"requirement,omitempty"`
}

type leadQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	About    []string `json:"about"`
}

// LeadInput renders the reports for the lead, in label order, with every
// ID prefixed by its report's label ("A.i1"). It returns the JSON and the
// type of every interpretation reference, for validating the lead's answer.
func LeadInput(reports []CheckedReport, labels []string) ([]byte, map[string]knowledge.Type, error) {
	refs := map[string]knowledge.Type{}
	in := leadInput{Reports: make([]leadReport, len(reports))}
	for ri, r := range reports {
		label := labels[ri]
		prefix := func(id string) string { return label + "." + id }
		prefixAll := func(ids []string) []string {
			out := make([]string, len(ids))
			for i, id := range ids {
				out[i] = prefix(id)
			}
			return out
		}

		lr := leadReport{Label: label}
		for _, o := range r.Report.Observations {
			lo := leadObservation{ID: prefix(o.ID), Statement: o.Statement}
			for _, c := range r.Citations[o.ID] {
				le := leadEvidence{Path: c.Path, StartLine: c.StartLine, EndLine: c.EndLine, Excerpt: c.Excerpt, Verification: string(c.Result.State)}
				if c.Valid() {
					le.StartLine, le.EndLine, le.Excerpt = c.Result.StartLine, c.Result.EndLine, c.Result.Excerpt
				} else {
					le.Reason = c.Result.Reason
				}
				lo.Evidence = append(lo.Evidence, le)
			}
			lr.Observations = append(lr.Observations, lo)
		}
		for _, it := range r.Report.Interpretations {
			li := leadInterpretation{
				ID: prefix(it.ID), Type: it.Type, Title: it.Title, Statement: it.Statement, Nature: it.Nature,
				Observations: prefixAll(it.Observations), Persona: it.Persona,
			}
			if it.Requirement != nil {
				req := *it.Requirement
				req.Personas = prefixAll(req.Personas)
				li.Requirement = &req
			}
			lr.Interpretations = append(lr.Interpretations, li)
			refs[li.ID] = it.Type
		}
		for _, q := range r.Report.Questions {
			lr.Questions = append(lr.Questions, leadQuestion{ID: prefix(q.ID), Question: q.Question, About: prefixAll(q.About)})
		}
		in.Reports[label[0]-'A'] = lr
	}

	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("render lead input: %w", err)
	}
	return data, refs, nil
}

// FromLead converts a validated lead consolidation into a plan, resolving
// anonymous references with labels.
func FromLead(c *agent.Consolidation, labels []string) (Plan, error) {
	resolve := func(ref string) (Ref, error) {
		label, id, ok := strings.Cut(ref, ".")
		for ri, l := range labels {
			if ok && l == label {
				return Ref{Report: ri, Interpretation: id}, nil
			}
		}
		return Ref{}, fmt.Errorf("unknown interpretation reference %q", ref)
	}
	resolveAll := func(refs []string) ([]Ref, error) {
		out := make([]Ref, len(refs))
		for i, r := range refs {
			var err error
			if out[i], err = resolve(r); err != nil {
				return nil, err
			}
		}
		return out, nil
	}

	var p Plan
	for _, it := range c.Items {
		support, err := resolveAll(it.Support)
		if err != nil {
			return Plan{}, err
		}
		pi := PlannedItem{
			Key: it.ID, Type: it.Type, Title: it.Title,
			Persona: it.Persona, Requirement: it.Requirement,
			Support: support, Uncertainties: it.Uncertainties, Questions: it.Questions,
		}
		for _, d := range it.Disagreements {
			pd := Disagreement{Summary: d.Summary}
			for _, pos := range d.Positions {
				refs, err := resolveAll(pos.Support)
				if err != nil {
					return Plan{}, err
				}
				pd.Positions = append(pd.Positions, Position{Statement: pos.Statement, Support: refs})
			}
			pi.Disagreements = append(pi.Disagreements, pd)
		}
		p.Items = append(p.Items, pi)
	}
	return p, nil
}
