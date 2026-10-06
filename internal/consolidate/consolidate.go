// Package consolidate turns verified agent reports into knowledge items.
//
// A Plan says which interpretations form each item: one item per
// interpretation when a single report is available (Identity), or the
// lead's grouping (FromLead). Build then assembles items from the plan.
//
// Only evidence that Raun verified (or relocated) reaches the knowledge
// base. Observations left without valid evidence are dropped, and an
// interpretation that loses all its observations becomes a hypothesis; both
// are recorded as an open point so a human sees why.
package consolidate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/knowledge"
)

// RaisedByRaun marks open points raised by Raun's own checks.
const RaisedByRaun = "raun"

// RaisedByLead marks open points raised by the lead.
const RaisedByLead = "lead"

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

// Ref identifies one interpretation of one report.
type Ref struct {
	Report         int
	Interpretation string
}

// Plan groups interpretations into knowledge items.
type Plan struct {
	Items []PlannedItem
}

// PlannedItem is one knowledge item to build.
type PlannedItem struct {
	// Key identifies the item within the plan; requirement persona links
	// use keys.
	Key         string
	Type        knowledge.Type
	Title       string
	Persona     *agent.PersonaContent
	Requirement *agent.RequirementContent
	Support     []Ref
	// Disagreements, Uncertainties and Questions come from the lead.
	Disagreements []Disagreement
	Uncertainties []string
	Questions     []string
}

// Disagreement is a conflict between interpretations of one item.
type Disagreement struct {
	Summary   string
	Positions []Position
}

// Position is one side of a disagreement.
type Position struct {
	Statement string
	Support   []Ref
}

// AgentQuestion is an agent question not attached to any item.
type AgentQuestion struct {
	Agent    string
	ID       string
	Question string
}

// Result is the outcome of a consolidation.
type Result struct {
	Items     []*knowledge.Item
	Questions []AgentQuestion
}

// Identity plans one item per interpretation, for when a single report is
// available and there is nothing to merge.
func Identity(reports []CheckedReport) Plan {
	var p Plan
	for ri, r := range reports {
		key := func(id string) string { return fmt.Sprintf("%d/%s", ri, id) }
		for _, in := range r.Report.Interpretations {
			req := in.Requirement
			if req != nil {
				copied := *req
				copied.Personas = nil
				for _, ref := range req.Personas {
					copied.Personas = append(copied.Personas, key(ref))
				}
				req = &copied
			}
			p.Items = append(p.Items, PlannedItem{
				Key:         key(in.ID),
				Type:        in.Type,
				Title:       in.Title,
				Persona:     in.Persona,
				Requirement: req,
				Support:     []Ref{{Report: ri, Interpretation: in.ID}},
			})
		}
	}
	return p
}

// Build assembles the planned items. newID allocates item IDs. Items with
// a disagreement are moved to contested by the run.
func Build(runID, commit string, at time.Time, reports []CheckedReport, plan Plan, newID func(knowledge.Type) (string, error)) (Result, error) {
	at = at.UTC().Truncate(time.Second)

	ids := map[string]string{}
	for _, pi := range plan.Items {
		id, err := newID(pi.Type)
		if err != nil {
			return Result{}, err
		}
		ids[pi.Key] = id
	}

	var res Result
	for _, pi := range plan.Items {
		it, err := buildItem(runID, commit, at, reports, pi, ids)
		if err != nil {
			return Result{}, err
		}
		res.Items = append(res.Items, it)
	}
	for _, r := range reports {
		for _, q := range r.Report.Questions {
			if len(q.About) == 0 {
				res.Questions = append(res.Questions, AgentQuestion{Agent: r.Agent, ID: q.ID, Question: q.Question})
			}
		}
	}
	return res, nil
}

func buildItem(runID, commit string, at time.Time, reports []CheckedReport, pi PlannedItem, ids map[string]string) (*knowledge.Item, error) {
	it := &knowledge.Item{
		Version: knowledge.FormatVersion,
		ID:      ids[pi.Key],
		Type:    pi.Type,
		Title:   pi.Title,
		Status:  knowledge.StatusProposed,
		History: []knowledge.Event{{At: at, Actor: knowledge.ActorRun, By: runID, To: knowledge.StatusProposed}},
	}
	if p := pi.Persona; p != nil {
		it.Persona = &knowledge.Persona{Description: p.Description, Goals: p.Goals, Capabilities: p.Capabilities}
	}
	if q := pi.Requirement; q != nil {
		req := &knowledge.Requirement{Statement: q.Statement, Kind: q.Kind, Rationale: q.Rationale}
		for _, key := range q.Personas {
			id, ok := ids[key]
			if !ok {
				return nil, fmt.Errorf("item %s links to unknown persona %q", pi.Key, key)
			}
			req.Personas = append(req.Personas, id)
		}
		it.Requirement = req
	}

	ev := &evidenceSet{item: it, commit: commit}
	refEvidence := map[Ref][]string{}
	for _, ref := range pi.Support {
		if ref.Report < 0 || ref.Report >= len(reports) {
			return nil, fmt.Errorf("item %s: unknown report %d", pi.Key, ref.Report)
		}
		r := reports[ref.Report]
		i := slices.IndexFunc(r.Report.Interpretations, func(in agent.Interpretation) bool { return in.ID == ref.Interpretation })
		if i < 0 {
			return nil, fmt.Errorf("item %s: agent %s has no interpretation %q", pi.Key, r.Agent, ref.Interpretation)
		}
		in := r.Report.Interpretations[i]

		support, evIDs, dropped := buildSupport(runID, r, in, ev)
		it.Support = append(it.Support, support)
		refEvidence[ref] = evIDs
		if len(dropped) > 0 {
			summary := fmt.Sprintf("Agent %s cited %d of %d observation(s) whose evidence could not be verified: %s",
				r.Agent, len(dropped), len(in.Observations), strings.Join(dropped, "; "))
			if support.Nature != in.Nature {
				summary += ". Its interpretation was downgraded to a hypothesis."
			}
			addPoint(it, knowledge.OpenPoint{Kind: knowledge.PointUncertainty, Summary: summary, RaisedBy: RaisedByRaun})
		}
		for _, q := range r.Report.Questions {
			if slices.Contains(q.About, in.ID) {
				addPoint(it, knowledge.OpenPoint{Kind: knowledge.PointQuestion, Summary: q.Question, RaisedBy: r.Agent})
			}
		}
	}

	for _, d := range pi.Disagreements {
		point := knowledge.OpenPoint{Kind: knowledge.PointDisagreement, Summary: d.Summary, RaisedBy: RaisedByLead}
		for _, pos := range d.Positions {
			kp := knowledge.Position{Statement: pos.Statement, Run: runID}
			for _, ref := range pos.Support {
				if a := reports[ref.Report].Agent; !slices.Contains(kp.Agents, a) {
					kp.Agents = append(kp.Agents, a)
				}
				for _, id := range refEvidence[ref] {
					if !slices.Contains(kp.Evidence, id) {
						kp.Evidence = append(kp.Evidence, id)
					}
				}
			}
			point.Positions = append(point.Positions, kp)
		}
		addPoint(it, point)
	}
	for _, u := range pi.Uncertainties {
		addPoint(it, knowledge.OpenPoint{Kind: knowledge.PointUncertainty, Summary: u, RaisedBy: RaisedByLead})
	}
	for _, q := range pi.Questions {
		addPoint(it, knowledge.OpenPoint{Kind: knowledge.PointQuestion, Summary: q, RaisedBy: RaisedByLead})
	}

	if len(pi.Disagreements) > 0 {
		if err := it.Transition(knowledge.StatusContested, knowledge.Actor{Kind: knowledge.ActorRun, Name: runID}, at, ""); err != nil {
			return nil, err
		}
	}
	return it, nil
}

// buildSupport copies one interpretation into the item, keeping only
// observations with valid evidence. It returns the support entry, the
// evidence IDs it uses, and descriptions of the dropped observations.
func buildSupport(runID string, r CheckedReport, in agent.Interpretation, ev *evidenceSet) (knowledge.Support, []string, []string) {
	support := knowledge.Support{
		Run:            runID,
		Agent:          r.Agent,
		Interpretation: in.ID,
		Statement:      in.Statement,
		Nature:         in.Nature,
	}
	var all, dropped []string
	for _, ref := range in.Observations {
		i := slices.IndexFunc(r.Report.Observations, func(o agent.Observation) bool { return o.ID == ref })
		o := r.Report.Observations[i]
		var ids []string
		var reason string
		for _, c := range r.Citations[ref] {
			if !c.Valid() {
				if reason == "" {
					reason = c.Result.Reason
				}
				continue
			}
			id := ev.add(c)
			ids = append(ids, id)
			if !slices.Contains(all, id) {
				all = append(all, id)
			}
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
	return support, all, dropped
}

// addPoint appends an open point, numbering it and skipping exact
// duplicates (the same question asked by several agents, for instance).
func addPoint(it *knowledge.Item, p knowledge.OpenPoint) {
	for _, existing := range it.OpenPoints {
		if existing.Kind == p.Kind && existing.Summary == p.Summary && p.Kind != knowledge.PointDisagreement {
			return
		}
	}
	p.ID = fmt.Sprintf("p%d", len(it.OpenPoints)+1)
	it.OpenPoints = append(it.OpenPoints, p)
}

// evidenceSet adds evidence to an item, reusing the entry when two
// citations land on the same lines.
type evidenceSet struct {
	item   *knowledge.Item
	commit string
}

func (s *evidenceSet) add(c CheckedCitation) string {
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
