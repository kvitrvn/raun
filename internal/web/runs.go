package web

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/run"
)

// Change filter values: what a run did to an item. They are derived from
// the run manifests and the knowledge files; no format records them per
// item (see docs/web.md).
const (
	changeNew           = "new"
	changeUpdated       = "updated"
	changeStale         = "stale"
	changeConflict      = "conflict"
	changeNeedsReview   = "needs-review"
	changeRejectedAgain = "rejected-again"
	changeSkipped       = "skipped"
)

// changeKinds lists the change filters in display order.
var changeKinds = []string{changeNew, changeUpdated, changeStale, changeConflict, changeNeedsReview, changeRejectedAgain, changeSkipped}

// sidebarRuns is how many recent runs the sidebar lists.
const sidebarRuns = 8

// runIndex holds the runs of the workspace, oldest first.
type runIndex struct {
	Runs []*run.Manifest
}

func (x *runIndex) get(id string) *run.Manifest {
	if x == nil {
		return nil
	}
	for _, m := range x.Runs {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// latest returns the latest succeeded run, or nil.
func (x *runIndex) latest() *run.Manifest {
	if x == nil {
		return nil
	}
	for _, m := range slices.Backward(x.Runs) {
		if m.Status == run.StatusSucceeded {
			return m
		}
	}
	return nil
}

// previous returns the latest succeeded run before m, or nil.
func (x *runIndex) previous(m *run.Manifest) *run.Manifest {
	if x == nil {
		return nil
	}
	i := slices.Index(x.Runs, m)
	for i--; i >= 0; i-- {
		if x.Runs[i].Status == run.StatusSucceeded {
			return x.Runs[i]
		}
	}
	return nil
}

// newestFirst returns at most limit runs (0: all), newest first.
func (x *runIndex) newestFirst(limit int) []*run.Manifest {
	if x == nil {
		return nil
	}
	out := slices.Clone(x.Runs)
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// runView is the run pages refer to, with what it changed in each item:
// the run named by the URL, or else the latest succeeded run.
type runView struct {
	Index *runIndex
	// Active is nil when the workspace has no succeeded run and the URL
	// names none.
	Active  *run.Manifest
	Changes map[string]itemChanges
}

// newRunView resolves the run named by the URL (empty: the latest
// succeeded run) and derives its changes.
func newRunView(index *runIndex, items []*knowledge.Item, id string) (*runView, error) {
	v := &runView{Index: index, Active: index.latest()}
	if id != "" {
		if v.Active = index.get(id); v.Active == nil {
			return nil, fmt.Errorf("run: unknown run %q", id)
		}
	}
	if v.Active != nil {
		v.Changes = make(map[string]itemChanges, len(items))
		for _, it := range items {
			v.Changes[it.ID] = changesOf(it, v.Active)
		}
	}
	return v, nil
}

// changes returns what the active run did to the item with this ID.
func (v *runView) changes(id string) itemChanges {
	if v == nil {
		return itemChanges{}
	}
	return v.Changes[id]
}

// count returns how many items the active run changed in this way.
func (v *runView) count(kind string) int {
	n := 0
	if v != nil {
		for _, c := range v.Changes {
			if c.has(kind) {
				n++
			}
		}
	}
	return n
}

// itemChanges is what one run did to one item.
type itemChanges struct {
	Run *run.Manifest
	// New: the run created the item. Updated: it changed an existing one.
	New, Updated bool
	// Support lists the interpretations the run added, and Agents their
	// agents in order.
	Support []knowledge.Support
	Agents  []string
	// Evidence lists the evidence first cited by the run.
	Evidence []string
	// Stale lists the stale evidence of an item the run updated: stale at
	// the run's commit or at a later one.
	Stale []string
	// Conflicts lists the disagreements the reconciler raised in this run:
	// their last position holds the run's content.
	Conflicts                  []string
	NeedsReview, RejectedAgain bool
	// Skipped is why the run left the item unchanged, if it did.
	Skipped string
	// Events are the status changes the run recorded.
	Events []knowledge.Event
}

func (c itemChanges) any() bool { return c.New || c.Updated || c.Skipped != "" }

func (c itemChanges) has(kind string) bool {
	switch kind {
	case changeNew:
		return c.New
	case changeUpdated:
		return c.Updated
	case changeStale:
		return len(c.Stale) > 0
	case changeConflict:
		return len(c.Conflicts) > 0
	case changeNeedsReview:
		return c.NeedsReview
	case changeRejectedAgain:
		return c.RejectedAgain
	case changeSkipped:
		return c.Skipped != ""
	}
	return false
}

func changesOf(it *knowledge.Item, m *run.Manifest) itemChanges {
	c := itemChanges{Run: m, New: slices.Contains(m.Items, it.ID), Updated: slices.Contains(m.Updated, it.ID)}
	if r := m.Reconciliation; r != nil {
		c.NeedsReview = slices.Contains(r.NeedsReview, it.ID)
		c.RejectedAgain = slices.Contains(r.RejectedSupported, it.ID)
		for _, s := range r.Skipped {
			if s.Item == it.ID {
				c.Skipped = s.Reason
			}
		}
	}
	if !c.any() {
		return c
	}
	for _, s := range it.Support {
		if s.Run == m.ID {
			c.Support = append(c.Support, s)
			if !slices.Contains(c.Agents, s.Agent) {
				c.Agents = append(c.Agents, s.Agent)
			}
		}
	}
	first := firstCitingRuns(it)
	for _, e := range it.Evidence {
		if first[e.ID] == m.ID {
			c.Evidence = append(c.Evidence, e.ID)
		}
		if c.Updated && e.State == knowledge.EvidenceStale {
			c.Stale = append(c.Stale, e.ID)
		}
	}
	for _, p := range it.OpenPoints {
		if p.Kind == knowledge.PointDisagreement && p.RaisedBy == "reconciler" && len(p.Positions) > 0 && p.Positions[len(p.Positions)-1].Run == m.ID {
			c.Conflicts = append(c.Conflicts, p.ID)
		}
	}
	for _, e := range it.History {
		if e.Actor == knowledge.ActorRun && e.By == m.ID {
			c.Events = append(c.Events, e)
		}
	}
	return c
}

// firstCitingRuns maps each evidence ID to the run that cited it first.
// Support is appended run after run, so its order is chronological;
// evidence only cited by disagreement positions takes their run.
func firstCitingRuns(it *knowledge.Item) map[string]string {
	first := map[string]string{}
	cite := func(ids []string, runID string) {
		for _, id := range ids {
			if _, ok := first[id]; !ok {
				first[id] = runID
			}
		}
	}
	for _, s := range it.Support {
		for _, o := range s.Observations {
			cite(o.Evidence, s.Run)
		}
	}
	for _, p := range it.OpenPoints {
		for _, pos := range p.Positions {
			cite(pos.Evidence, pos.Run)
		}
	}
	return first
}

// supportGroup holds the interpretations one run added to an item.
type supportGroup struct {
	RunID   string
	Run     *run.Manifest // nil when the run has no manifest
	Support []knowledge.Support
}

// supportGroups groups the item's interpretations by run, oldest first.
func supportGroups(it *knowledge.Item, index *runIndex) []supportGroup {
	var groups []supportGroup
	for _, s := range it.Support {
		i := slices.IndexFunc(groups, func(g supportGroup) bool { return g.RunID == s.Run })
		if i < 0 {
			groups = append(groups, supportGroup{RunID: s.Run, Run: index.get(s.Run)})
			i = len(groups) - 1
		}
		groups[i].Support = append(groups[i].Support, s)
	}
	return groups
}

// activity is one entry of an item's Activity: what a run did, a human
// decision, or the resolution of an open point.
type activity struct {
	At time.Time
	// Run is set for a run with a manifest; RunEvents for a run without one.
	Run       *itemChanges
	RunID     string
	RunEvents []knowledge.Event
	Human     *knowledge.Event
	Point     *knowledge.OpenPoint
}

// activities returns the item's Activity, newest first.
func activities(it *knowledge.Item, index *runIndex) []activity {
	var out []activity
	described := map[string]bool{}
	if index != nil {
		for _, m := range index.Runs {
			c := changesOf(it, m)
			if !c.any() {
				continue
			}
			at := m.FinishedAt
			if len(c.Events) > 0 {
				at = c.Events[len(c.Events)-1].At
			}
			out = append(out, activity{At: at, Run: &c, RunID: m.ID})
			described[m.ID] = true
		}
	}
	for _, e := range it.History {
		switch {
		case e.Actor == knowledge.ActorHuman:
			out = append(out, activity{At: e.At, Human: &e})
		case !described[e.By]:
			i := slices.IndexFunc(out, func(a activity) bool { return a.Run == nil && a.Human == nil && a.Point == nil && a.RunID == e.By })
			if i < 0 {
				out = append(out, activity{At: e.At, RunID: e.By})
				i = len(out) - 1
			}
			out[i].RunEvents = append(out[i].RunEvents, e)
			out[i].At = e.At
		}
	}
	for _, p := range it.OpenPoints {
		if p.Resolution != nil {
			out = append(out, activity{At: p.Resolution.At, Point: &p})
		}
	}
	// Newest first; at the same time, the later entry of the list first.
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b activity) int { return cmp.Compare(b.At.UnixNano(), a.At.UnixNano()) })
	return out
}
