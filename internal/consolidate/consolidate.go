// Package consolidate turns verified agent reports into knowledge items.
//
// Only evidence that Raun verified (or relocated) reaches the knowledge
// base. Observations left without valid evidence are dropped, and an
// interpretation that loses all its observations becomes a hypothesis; both
// are recorded as an open point so a human sees why.
package consolidate

import (
	"fmt"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/knowledge"
)

// CheckedCitation is an agent citation after Raun's verification.
type CheckedCitation struct {
	agent.Citation
	// Source is empty when the path is excluded from sources.
	Source knowledge.SourceKind
	Result evidence.Result
}

// Valid reports whether the citation can back knowledge.
func (c CheckedCitation) Valid() bool {
	return c.Result.State == knowledge.EvidenceVerified || c.Result.State == knowledge.EvidenceRelocated
}

// CheckedReport is a valid agent report whose citations were all checked.
type CheckedReport struct {
	Agent  string
	Report *agent.Report
	// Citations holds the checked citations of each observation, by
	// observation ID, in report order.
	Citations map[string][]CheckedCitation
}

// Result is the outcome of a consolidation.
type Result struct {
	Items []*knowledge.Item
	// Questions are those not attached to any interpretation.
	Questions []agent.Question
}

// Single consolidates the report of a single agent: each interpretation
// becomes one proposed knowledge item. newID allocates item IDs.
func Single(runID, commit string, at time.Time, r CheckedReport, newID func(knowledge.Type) (string, error)) (Result, error) {
	at = at.UTC().Truncate(time.Second)
	observations := map[string]agent.Observation{}
	for _, o := range r.Report.Observations {
		observations[o.ID] = o
	}

	itemIDs := map[string]string{} // interpretation ID -> item ID
	for _, in := range r.Report.Interpretations {
		id, err := newID(in.Type)
		if err != nil {
			return Result{}, err
		}
		itemIDs[in.ID] = id
	}

	var res Result
	for _, in := range r.Report.Interpretations {
		it := &knowledge.Item{
			Version: knowledge.FormatVersion,
			ID:      itemIDs[in.ID],
			Type:    in.Type,
			Title:   in.Title,
			Status:  knowledge.StatusProposed,
			History: []knowledge.Event{{At: at, Actor: knowledge.ActorRun, By: runID, To: knowledge.StatusProposed}},
		}
		if p := in.Persona; p != nil {
			it.Persona = &knowledge.Persona{Description: p.Description, Goals: p.Goals, Capabilities: p.Capabilities}
		}
		if q := in.Requirement; q != nil {
			req := &knowledge.Requirement{Statement: q.Statement, Kind: q.Kind, Rationale: q.Rationale}
			for _, ref := range q.Personas {
				req.Personas = append(req.Personas, itemIDs[ref])
			}
			it.Requirement = req
		}

		support := knowledge.Support{
			Run:            runID,
			Agent:          r.Agent,
			Interpretation: in.ID,
			Statement:      in.Statement,
			Nature:         in.Nature,
		}
		ev := evidenceSet{item: it, commit: commit}
		var dropped []string
		for _, ref := range in.Observations {
			o := observations[ref]
			var ids []string
			var reason string
			for _, c := range r.Citations[ref] {
				if !c.Valid() {
					if reason == "" {
						reason = c.Result.Reason
					}
					continue
				}
				ids = append(ids, ev.add(c))
			}
			if len(ids) == 0 {
				dropped = append(dropped, fmt.Sprintf("%q (%s)", o.Statement, reason))
				continue
			}
			support.Observations = append(support.Observations, knowledge.Observation{Statement: o.Statement, Evidence: ids})
		}
		if support.Nature == knowledge.NatureSupported && len(support.Observations) == 0 {
			support.Nature = knowledge.NatureHypothesis
		}
		it.Support = []knowledge.Support{support}

		if len(dropped) > 0 {
			summary := fmt.Sprintf("Agent %s cited %d of %d observation(s) whose evidence could not be verified: %s",
				r.Agent, len(dropped), len(in.Observations), strings.Join(dropped, "; "))
			if support.Nature != in.Nature {
				summary += ". The interpretation was downgraded to a hypothesis."
			}
			addPoint(it, knowledge.PointUncertainty, summary)
		}
		for _, q := range r.Report.Questions {
			for _, about := range q.About {
				if about == in.ID {
					addPoint(it, knowledge.PointQuestion, q.Question)
				}
			}
		}
		res.Items = append(res.Items, it)
	}

	for _, q := range r.Report.Questions {
		if len(q.About) == 0 {
			res.Questions = append(res.Questions, q)
		}
	}
	return res, nil
}

func addPoint(it *knowledge.Item, kind knowledge.PointKind, summary string) {
	it.OpenPoints = append(it.OpenPoints, knowledge.OpenPoint{
		ID:      fmt.Sprintf("p%d", len(it.OpenPoints)+1),
		Kind:    kind,
		Summary: summary,
	})
}

// evidenceSet adds evidence to an item, reusing the entry when two
// citations land on the same lines.
type evidenceSet struct {
	item   *knowledge.Item
	commit string
}

func (s evidenceSet) add(c CheckedCitation) string {
	for _, e := range s.item.Evidence {
		if e.Path == c.Path && e.StartLine == c.Result.StartLine && e.EndLine == c.Result.EndLine {
			return e.ID
		}
	}
	id := fmt.Sprintf("e%d", len(s.item.Evidence)+1)
	s.item.Evidence = append(s.item.Evidence, knowledge.Evidence{
		ID:        id,
		Source:    c.Source,
		Path:      c.Path,
		Commit:    s.commit,
		StartLine: c.Result.StartLine,
		EndLine:   c.Result.EndLine,
		Excerpt:   c.Result.Excerpt,
		SHA256:    knowledge.HashExcerpt(c.Result.Excerpt),
		State:     c.Result.State,
	})
	return id
}
