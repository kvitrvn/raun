// Package reconcile matches the results of a run against an existing
// knowledge base, and updates existing items without rewriting them.
//
// The reconciler agent tells which existing item each planned item of the
// run describes (Input, agent.ParseReconciliation, Assign). The run builds
// its planned items as usual; Apply then attaches the ones matched to an
// existing item: their support, evidence and open points are appended, while
// the existing title, content and support never change. Apply also records
// the evidence that stopped holding at the run's commit (CheckFreshness),
// and moves the item to needs-review, or contested, when its ground changes.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/consolidate"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
)

// RaisedByReconciler marks open points raised for a conflict declared by
// the reconciler.
const RaisedByReconciler = "reconciler"

// shownItem is how the reconciler sees an item: its content and the
// analysts' statements behind it, never agents, runs, statuses or history.
type shownItem struct {
	ID              string                    `json:"id"`
	Type            knowledge.Type            `json:"type"`
	Title           string                    `json:"title"`
	Persona         *agent.PersonaContent     `json:"persona,omitempty"`
	Requirement     *agent.RequirementContent `json:"requirement,omitempty"`
	Interpretations []string                  `json:"interpretations"`
}

type input struct {
	Existing []shownItem `json:"existing"`
	New      []shownItem `json:"new"`
}

// Refs are the references shown to the reconciler, with their type.
type Refs struct {
	// Existing holds the IDs of the existing items.
	Existing map[string]knowledge.Type
	// Items holds the labels of the run's planned items (see Label).
	Items map[string]knowledge.Type
}

// Label returns the label the reconciler sees for the planned item at
// index i: "n1", "n2", …
func Label(i int) string { return fmt.Sprintf("n%d", i+1) }

// Input renders the existing items and the run's plan for the reconciler.
// Requirement persona links of planned items use labels. It returns the
// JSON and the references shown, for validating the reconciler's answer.
func Input(existing []*knowledge.Item, plan consolidate.Plan, reports []consolidate.CheckedReport) ([]byte, Refs, error) {
	refs := Refs{Existing: map[string]knowledge.Type{}, Items: map[string]knowledge.Type{}}
	in := input{Existing: []shownItem{}, New: []shownItem{}}

	for _, it := range existing {
		shown := shownItem{ID: it.ID, Type: it.Type, Title: it.Title, Interpretations: []string{}}
		for _, s := range it.Support {
			shown.Interpretations = appendNew(shown.Interpretations, s.Statement)
		}
		if p := it.Persona; p != nil {
			shown.Persona = &agent.PersonaContent{Description: p.Description, Goals: orEmpty(p.Goals), Capabilities: orEmpty(p.Capabilities)}
		}
		if q := it.Requirement; q != nil {
			shown.Requirement = &agent.RequirementContent{Statement: q.Statement, Kind: q.Kind, Rationale: q.Rationale, Personas: orEmpty(q.Personas)}
		}
		in.Existing = append(in.Existing, shown)
		refs.Existing[it.ID] = it.Type
	}

	labels := map[string]string{}
	for i, pi := range plan.Items {
		labels[pi.Key] = Label(i)
	}
	for i, pi := range plan.Items {
		shown := shownItem{ID: Label(i), Type: pi.Type, Title: pi.Title, Interpretations: []string{}}
		if p := pi.Persona; p != nil {
			shown.Persona = &agent.PersonaContent{Description: p.Description, Goals: orEmpty(p.Goals), Capabilities: orEmpty(p.Capabilities)}
		}
		if q := pi.Requirement; q != nil {
			req := *q
			req.Personas = make([]string, len(q.Personas))
			for j, key := range q.Personas {
				req.Personas[j] = labels[key]
			}
			shown.Requirement = &req
		}
		for _, ref := range pi.Support {
			if ref.Report < 0 || ref.Report >= len(reports) {
				return nil, Refs{}, fmt.Errorf("item %s: unknown report %d", pi.Key, ref.Report)
			}
			r := reports[ref.Report].Report
			k := slices.IndexFunc(r.Interpretations, func(in agent.Interpretation) bool { return in.ID == ref.Interpretation })
			if k < 0 {
				return nil, Refs{}, fmt.Errorf("item %s: no interpretation %q", pi.Key, ref.Interpretation)
			}
			shown.Interpretations = appendNew(shown.Interpretations, r.Interpretations[k].Statement)
		}
		in.New = append(in.New, shown)
		refs.Items[shown.ID] = pi.Type
	}

	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return nil, Refs{}, fmt.Errorf("render reconciler input: %w", err)
	}
	return data, refs, nil
}

// Match is a planned item of the run that describes an existing item.
type Match struct {
	// Item is the planned item's index in the plan.
	Item     int
	Existing string
	// Conflict is the reconciler's summary when the planned item
	// contradicts the existing item's content.
	Conflict string
}

// Assign gives each planned item matched by the reconciler the ID of its
// existing item, so that Build links new requirements to existing personas,
// and returns the matches in plan order. rec must have been validated
// against the references of Input.
func Assign(plan *consolidate.Plan, rec *agent.Reconciliation) []Match {
	index := map[string]int{}
	for i := range plan.Items {
		index[Label(i)] = i
	}
	var matches []Match
	for _, m := range rec.Items {
		if m.Match == "" {
			continue
		}
		i := index[m.ID]
		plan.Items[i].ID = m.Match
		matches = append(matches, Match{Item: i, Existing: m.Match, Conflict: strings.TrimSpace(m.Conflict)})
	}
	slices.SortFunc(matches, func(a, b Match) int { return a.Item - b.Item })
	return matches
}

// Attachment is an item the run built for an existing item.
type Attachment struct {
	Item     *knowledge.Item
	Conflict string
}

// Attachments groups the built items of the matches by existing item ID.
// built is the result of consolidate.Build, in plan order.
func Attachments(built []*knowledge.Item, matches []Match) map[string][]Attachment {
	out := map[string][]Attachment{}
	for _, m := range matches {
		out[m.Existing] = append(out[m.Existing], Attachment{Item: built[m.Item], Conflict: m.Conflict})
	}
	return out
}

// Checker verifies an evidence at a commit; *evidence.Verifier implements
// it.
type Checker interface {
	Verify(ctx context.Context, ev knowledge.Evidence, at string) (evidence.Result, error)
}

// Ancestry tells whether a commit descends from another; *gitx.Repo
// implements it. An unknown commit yields an error wrapping
// gitx.ErrUnknownCommit.
type Ancestry interface {
	IsAncestor(ctx context.Context, ancestor, commit string) (bool, error)
}

// Freshness holds, for existing evidence, the outcome of its check at a
// later commit.
type Freshness map[evidenceKey]evidence.Result

type evidenceKey struct {
	path, commit, sha256 string
	start, end           int
}

func keyOf(e knowledge.Evidence) evidenceKey {
	return evidenceKey{path: e.Path, commit: e.Commit, sha256: e.SHA256, start: e.StartLine, end: e.EndLine}
}

// CheckFreshness verifies at commit the evidence of items cited at one of
// its ancestors. Evidence cited at commit itself, at a commit that is not
// an ancestor of it or that the repository does not hold is left out, and
// so is a citation the check rejects outright (one longer than the current
// line limit, for instance).
func CheckFreshness(ctx context.Context, items []*knowledge.Item, commit string, checker Checker, ancestry Ancestry) (Freshness, error) {
	fresh := Freshness{}
	earlier := map[string]bool{}
	for _, it := range items {
		for _, e := range it.Evidence {
			if e.Commit == commit {
				continue
			}
			ok, known := earlier[e.Commit]
			if !known {
				var err error
				ok, err = ancestry.IsAncestor(ctx, e.Commit, commit)
				if errors.Is(err, gitx.ErrUnknownCommit) {
					ok, err = false, nil
				}
				if err != nil {
					return nil, err
				}
				earlier[e.Commit] = ok
			}
			if _, done := fresh[keyOf(e)]; !ok || done {
				continue
			}
			r, err := checker.Verify(ctx, e, commit)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", it.ID, e.ID, err)
			}
			if r.State != knowledge.EvidenceInvalid {
				fresh[keyOf(e)] = r
			}
		}
	}
	return fresh, nil
}

// Outcome tells what Apply did to an item.
type Outcome struct {
	// Changed is false when the item was left as it was.
	Changed bool
	// Stale lists the evidence that stopped holding.
	Stale []string
}

// Apply brings the results of run runID to an existing item. It never
// changes the item's title, content or existing support:
//   - each attachment adds its support, evidence and open points, with
//     renumbered IDs; evidence citing the same lines at the same commit is
//     shared, and known questions, uncertainties and disagreements are not
//     added again;
//   - a conflict adds a disagreement between the view the item was created
//     from and the attachment's view;
//   - evidence that no longer holds according to fresh becomes stale, with
//     an uncertainty saying why; stale evidence that holds again is verified.
//
// A validated item with a new disagreement or newly stale evidence moves to
// needs-review, and a proposed item with a new disagreement to contested.
// Other statuses are kept: a run never decides.
func Apply(it *knowledge.Item, attach []Attachment, fresh Freshness, runID string, at time.Time) (Outcome, error) {
	before, err := knowledge.Revision(it)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome

	for i := range it.Evidence {
		e := &it.Evidence[i]
		r, ok := fresh[keyOf(*e)]
		switch {
		case !ok:
		case r.State == knowledge.EvidenceStale && e.State != knowledge.EvidenceStale:
			e.State = knowledge.EvidenceStale
			out.Stale = append(out.Stale, e.ID)
			it.AddPoint(knowledge.OpenPoint{
				Kind:     knowledge.PointUncertainty,
				Summary:  fmt.Sprintf("Evidence %s (%s:%d-%d) no longer holds: %s.", e.ID, e.Path, e.StartLine, e.EndLine, r.Reason),
				RaisedBy: consolidate.RaisedByRaun,
			})
		case r.State != knowledge.EvidenceStale && e.State == knowledge.EvidenceStale:
			e.State = knowledge.EvidenceVerified
		}
	}

	from, hasOrigin := origin(it)
	var disagreements, conflicts []string
	for _, a := range attach {
		support, added := attachItem(it, a.Item)
		disagreements = append(disagreements, added...)
		if a.Conflict == "" {
			continue
		}
		if !hasOrigin {
			if id, ok := it.AddPoint(knowledge.OpenPoint{Kind: knowledge.PointUncertainty, Summary: a.Conflict, RaisedBy: RaisedByReconciler}); ok {
				conflicts = append(conflicts, id)
			}
			continue
		}
		p := knowledge.OpenPoint{
			Kind:      knowledge.PointDisagreement,
			Summary:   a.Conflict,
			RaisedBy:  RaisedByReconciler,
			Positions: []knowledge.Position{from, view(a.Item, support, runID)},
		}
		if id, ok := addDisagreement(it, p); ok {
			disagreements = append(disagreements, id)
		}
	}

	var reasons []string
	if len(out.Stale) > 0 {
		reasons = append(reasons, "stale evidence: "+strings.Join(out.Stale, ", "))
	}
	if len(disagreements) > 0 {
		reasons = append(reasons, "new disagreement: "+strings.Join(disagreements, ", "))
	}
	if len(conflicts) > 0 {
		reasons = append(reasons, "conflict: "+strings.Join(conflicts, ", "))
	}
	var to knowledge.Status
	switch {
	case it.Status == knowledge.StatusValidated && len(reasons) > 0:
		to = knowledge.StatusNeedsReview
	case it.Status == knowledge.StatusProposed && len(disagreements) > 0:
		to = knowledge.StatusContested
	}
	if to != "" {
		if err := it.Transition(to, knowledge.Actor{Kind: knowledge.ActorRun, Name: runID}, at, strings.Join(reasons, "; ")); err != nil {
			return Outcome{}, err
		}
	}

	after, err := knowledge.Revision(it)
	if err != nil {
		return Outcome{}, err
	}
	out.Changed = after != before
	return out, nil
}

// SameContent reports whether two versions of an item have the same title
// and content: what the reconciler matched.
func SameContent(a, b *knowledge.Item) bool {
	return a.Type == b.Type && a.Title == b.Title &&
		reflect.DeepEqual(a.Persona, b.Persona) && reflect.DeepEqual(a.Requirement, b.Requirement)
}

// attachItem appends a built item's support, evidence and open points to
// it, renumbering their local IDs, and returns the attached support and the
// IDs of the new disagreements. built is left unchanged.
func attachItem(it, built *knowledge.Item) ([]knowledge.Support, []string) {
	ids := map[string]string{}
	for _, e := range built.Evidence {
		ids[e.ID] = it.AddEvidence(e)
	}
	remap := func(refs []string) []string {
		if len(refs) == 0 {
			return nil
		}
		out := make([]string, len(refs))
		for i, ref := range refs {
			out[i] = ids[ref]
		}
		return out
	}

	var support []knowledge.Support
	for _, s := range built.Support {
		s.Observations = slices.Clone(s.Observations)
		for j := range s.Observations {
			s.Observations[j].Evidence = remap(s.Observations[j].Evidence)
		}
		it.Support = append(it.Support, s)
		support = append(support, s)
	}

	var added []string
	for _, p := range built.OpenPoints {
		p.Positions = slices.Clone(p.Positions)
		for j := range p.Positions {
			p.Positions[j].Agents = slices.Clone(p.Positions[j].Agents)
			p.Positions[j].Evidence = remap(p.Positions[j].Evidence)
		}
		if p.Kind != knowledge.PointDisagreement {
			it.AddPoint(p)
		} else if id, ok := addDisagreement(it, p); ok {
			added = append(added, id)
		}
	}
	return support, added
}

// addDisagreement adds a disagreement, unless the item already records one
// with the same summary and the same position statements, resolved or not:
// finding a known disagreement again changes nothing.
func addDisagreement(it *knowledge.Item, p knowledge.OpenPoint) (string, bool) {
	statements := func(p knowledge.OpenPoint) []string {
		s := make([]string, len(p.Positions))
		for i, pos := range p.Positions {
			s[i] = pos.Statement
		}
		return s
	}
	for _, x := range it.OpenPoints {
		if x.Kind == knowledge.PointDisagreement && x.Summary == p.Summary && slices.Equal(statements(x), statements(p)) {
			return x.ID, false
		}
	}
	return it.AddPoint(p)
}

// origin is the view an item was created from: its content, held by the
// agents and evidence of the run that created it. It is false when no
// support of that run is left.
func origin(it *knowledge.Item) (knowledge.Position, bool) {
	if len(it.History) == 0 || it.History[0].Actor != knowledge.ActorRun {
		return knowledge.Position{}, false
	}
	pos := knowledge.Position{Statement: statement(it), Run: it.History[0].By}
	for _, s := range it.Support {
		if s.Run == pos.Run {
			addBacking(&pos, s)
		}
	}
	return pos, len(pos.Agents) > 0
}

// view is the position of an item built by a run, held by its attached
// support.
func view(built *knowledge.Item, support []knowledge.Support, runID string) knowledge.Position {
	pos := knowledge.Position{Statement: statement(built), Run: runID}
	for _, s := range support {
		addBacking(&pos, s)
	}
	return pos
}

func addBacking(pos *knowledge.Position, s knowledge.Support) {
	pos.Agents = appendNew(pos.Agents, s.Agent)
	for _, o := range s.Observations {
		for _, id := range o.Evidence {
			pos.Evidence = appendNew(pos.Evidence, id)
		}
	}
}

// statement is what an item asserts: a requirement's statement or a
// persona's description.
func statement(it *knowledge.Item) string {
	switch {
	case it.Requirement != nil:
		return it.Requirement.Statement
	case it.Persona != nil:
		return it.Persona.Description
	}
	return it.Title
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
