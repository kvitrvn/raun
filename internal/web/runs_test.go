package web

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/run"
	"github.com/kvitrvn/raun/internal/workspace"
)

// The knowledge fixtures were created by run1. run2 reran the analysis at
// a later commit; run3 was interrupted.
const (
	run1    = "run-20261007-a1"
	run2    = "run-20261008-b2"
	run3    = "run-20261009-c3"
	commit2 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var (
	run1At = time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	run2At = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
)

// rerunItems returns the knowledge base after run2:
//   - persona-k3x9q2: attached by run2 (+support, +e9), e1 stale;
//   - requirement-8fz2mc: a conflict raised by the reconciler in run2;
//   - persona-new222: created by run2;
//   - persona-val333: validated, then sent back to review by run2 (stale e1);
//   - persona-rej444: rejected, found again by run2;
//   - persona-skp555: skipped by run2;
//   - persona-old666: untouched by run2, with a resolved question.
func rerunItems(t *testing.T) []*knowledge.Item {
	t.Helper()
	support := func(runID, agent, evidence string) knowledge.Support {
		return knowledge.Support{Run: runID, Agent: agent, Interpretation: "i1", Statement: "Seen again.", Nature: knowledge.NatureSupported,
			Observations: []knowledge.Observation{{Statement: "Observed.", Evidence: []string{evidence}}}}
	}
	newEvidence := func(it *knowledge.Item, id string) {
		e := it.Evidence[0]
		e.ID, e.Commit = id, commit2
		it.Evidence = append(it.Evidence, e)
	}
	persona := func(id string) *knowledge.Item {
		it := fixture(t, "persona")
		it.ID = id
		return it
	}

	attached := fixture(t, "persona")
	attached.Support = append(attached.Support, support(run2, "gamma", "e9"))
	newEvidence(attached, "e9")
	attached.Evidence[0].State = knowledge.EvidenceStale

	conflict := fixture(t, "requirement")
	conflict.Support = append(conflict.Support, support(run2, "gamma", "e1"))
	conflict.OpenPoints = append(conflict.OpenPoints, knowledge.OpenPoint{ID: "p2", Kind: knowledge.PointDisagreement, Summary: "The run contradicts it.", RaisedBy: "reconciler",
		Positions: []knowledge.Position{
			{Statement: "Never editable.", Run: run1, Agents: []string{"alpha"}, Evidence: []string{"e1"}},
			{Statement: "Editable by admins.", Run: run2, Agents: []string{"gamma"}, Evidence: []string{"e1"}},
		}})

	created := persona("persona-new222")
	created.Support = []knowledge.Support{support(run2, "gamma", "e1")}
	created.Evidence[0].Commit = commit2
	created.History = []knowledge.Event{{At: run2At, Actor: knowledge.ActorRun, By: run2, To: knowledge.StatusProposed}}

	review := persona("persona-val333")
	review.Evidence[0].State = knowledge.EvidenceStale
	review.Status = knowledge.StatusNeedsReview
	review.History = append(review.History,
		knowledge.Event{At: run1At.Add(time.Hour), Actor: knowledge.ActorHuman, By: "Ada Lovelace", From: knowledge.StatusProposed, To: knowledge.StatusValidated, Reason: "checked"},
		knowledge.Event{At: run2At, Actor: knowledge.ActorRun, By: run2, From: knowledge.StatusValidated, To: knowledge.StatusNeedsReview, Reason: "evidence e1 is stale"})

	rejected := persona("persona-rej444")
	rejected.Support = append(rejected.Support, support(run2, "gamma", "e1"))
	rejected.Status = knowledge.StatusRejected
	rejected.History = append(rejected.History, knowledge.Event{At: run1At.Add(time.Hour), Actor: knowledge.ActorHuman, By: "Ada Lovelace", From: knowledge.StatusProposed, To: knowledge.StatusRejected, Reason: "wrong"})

	skipped := persona("persona-skp555")

	old := persona("persona-old666")
	old.OpenPoints = append(old.OpenPoints, knowledge.OpenPoint{ID: "p9", Kind: knowledge.PointQuestion, Summary: "Who pays?",
		Resolution: &knowledge.Resolution{At: run1At.Add(2 * time.Hour), By: "Grace Hopper", Note: "The tenant pays."}})

	return []*knowledge.Item{attached, conflict, created, review, rejected, skipped, old}
}

func rerunManifests() []*run.Manifest {
	agent := func(id string) run.AgentRecord {
		return run.AgentRecord{ID: id, Role: run.RoleAnalyst, Status: run.AgentOK, Duration: "1s"}
	}
	return []*run.Manifest{
		{Version: 3, ID: run1, Status: run.StatusSucceeded, StartedAt: run1At, FinishedAt: run1At.Add(time.Minute),
			Commit: "3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b", Agents: []run.AgentRecord{agent("alpha"), agent("beta")},
			Items: []string{"persona-k3x9q2", "requirement-8fz2mc", "persona-val333", "persona-rej444", "persona-skp555", "persona-old666"}},
		{Version: 3, ID: run2, Status: run.StatusSucceeded, StartedAt: run2At, FinishedAt: run2At.Add(3 * time.Minute), Commit: commit2,
			Agents:     []run.AgentRecord{agent("gamma"), {ID: "delta", Role: run.RoleAnalyst, Status: run.AgentFailed, Error: "exited <b>badly</b>", ReportRetries: 1}},
			Reconciler: &run.AgentRecord{ID: "reconciler", Role: run.RoleReconciler, Status: run.AgentOK},
			Reconciliation: &run.ReconciliationSummary{Candidates: 6, Matched: 3, Conflicts: 1, StaleEvidence: 2,
				NeedsReview: []string{"persona-val333"}, RejectedSupported: []string{"persona-rej444"},
				Skipped: []run.SkippedItem{{Item: "persona-skp555", Reason: "it changed during the run"}}},
			Items:     []string{"persona-new222"},
			Updated:   []string{"persona-k3x9q2", "requirement-8fz2mc", "persona-val333", "persona-rej444"},
			Questions: []run.QuestionRecord{{Agent: "gamma", ID: "q1", Question: "Is <script>alert(1)</script> billed?"}}},
		{Version: 3, ID: run3, Status: run.StatusFailed, Error: "run failed: interrupted", StartedAt: run2At.Add(24 * time.Hour),
			FinishedAt: run2At.Add(24*time.Hour + time.Minute), Commit: commit2, Agents: []run.AgentRecord{{ID: "gamma", Role: run.RoleAnalyst, Status: run.AgentFailed, Error: "interrupted"}}},
	}
}

// setupRuns serves the knowledge base and runs of a rerun.
func setupRuns(t *testing.T) (string, http.Handler) {
	t.Helper()
	root, store, h := setup(t, false)
	for _, it := range rerunItems(t) {
		if err := store.Save(it); err != nil {
			t.Fatalf("save %s: %v", it.ID, err)
		}
	}
	for _, m := range rerunManifests() {
		writeManifest(t, root, m.ID, m)
	}
	// A run stopped before writing its manifest is not listed.
	if err := os.MkdirAll(filepath.Join(workspace.RunsDir(root), "run-20261010-gone", "raw"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, h
}

func writeManifest(t *testing.T, root, id string, m any) {
	t.Helper()
	data, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.RunsDir(root), id, "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func byID(items []*knowledge.Item, id string) *knowledge.Item {
	i := slices.IndexFunc(items, func(it *knowledge.Item) bool { return it.ID == id })
	return items[i]
}

func TestRunChanges(t *testing.T) {
	items := rerunItems(t)
	index := &runIndex{Runs: rerunManifests()}
	if got := index.latest(); got == nil || got.ID != run2 {
		t.Fatalf("latest succeeded run = %v, want %s", got, run2)
	}
	if got := index.previous(index.get(run2)); got == nil || got.ID != run1 {
		t.Errorf("previous run of run2 = %v", got)
	}
	v, err := newRunView(index, items, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id       string
		kinds    []string
		support  int
		evidence []string
		stale    []string
	}{
		{"persona-k3x9q2", []string{changeUpdated, changeStale}, 1, []string{"e9"}, []string{"e1"}},
		{"requirement-8fz2mc", []string{changeUpdated, changeConflict}, 1, nil, nil},
		{"persona-new222", []string{changeNew}, 1, []string{"e1"}, nil},
		{"persona-val333", []string{changeUpdated, changeStale, changeNeedsReview}, 0, nil, []string{"e1"}},
		{"persona-rej444", []string{changeUpdated, changeRejectedAgain}, 1, nil, nil},
		{"persona-skp555", []string{changeSkipped}, 0, nil, nil},
		{"persona-old666", nil, 0, nil, nil},
	} {
		c := v.changes(tt.id)
		var kinds []string
		for _, kind := range changeKinds {
			if c.has(kind) {
				kinds = append(kinds, kind)
			}
		}
		if !slices.Equal(kinds, tt.kinds) || len(c.Support) != tt.support || !slices.Equal(c.Evidence, tt.evidence) || !slices.Equal(c.Stale, tt.stale) {
			t.Errorf("%s: kinds %v, support %d, evidence %v, stale %v", tt.id, kinds, len(c.Support), c.Evidence, c.Stale)
		}
	}
	if got := v.changes("requirement-8fz2mc").Conflicts; !slices.Equal(got, []string{"p2"}) {
		t.Errorf("conflicts = %v", got)
	}
	if got := v.changes("persona-skp555").Skipped; got != "it changed during the run" {
		t.Errorf("skipped = %q", got)
	}
	if n := v.count(changeUpdated); n != 4 {
		t.Errorf("updated count = %d", n)
	}
	if got := v.noChanges(); got != "" {
		t.Errorf("no changes = %q, want every kind present", got)
	}

	// The first run created most items; changes are relative to the run.
	v1, err := newRunView(index, items, run1)
	if err != nil {
		t.Fatal(err)
	}
	if c := v1.changes("persona-k3x9q2"); !c.New || !slices.Equal(c.Evidence, []string{"e1"}) || len(c.Stale) != 0 {
		t.Errorf("run1 changes = %+v", c)
	}
	// The reconciler's conflict belongs to the run of its last position.
	if got := v1.noChanges(); got != "No stale evidence, conflict, needs review, found again, not updated" {
		t.Errorf("run1 no changes = %q", got)
	}
	// A failed run changed nothing.
	if v3, err := newRunView(index, items, run3); err != nil || v3.count(changeNew)+v3.count(changeUpdated) != 0 {
		t.Errorf("failed run view = %+v, %v", v3, err)
	}
	if _, err := newRunView(index, items, "run-unknown"); err == nil || err.Error() != `run: unknown run "run-unknown"` {
		t.Errorf("unknown run error = %v", err)
	}
	// Without runs, nothing is attributed.
	if v, err := newRunView(&runIndex{}, items, ""); err != nil || v.Active != nil || v.changes("persona-k3x9q2").any() {
		t.Errorf("view without runs = %+v, %v", v, err)
	}

	// Run filters list what the run changed, whatever the status.
	f := filters{Status: statusAll, Run: run2, Page: 1}
	var ids []string
	for _, it := range filtered(items, v, f) {
		ids = append(ids, it.ID)
	}
	// List order: type, then status, then ID.
	if want := []string{"persona-val333", "persona-k3x9q2", "persona-new222", "persona-skp555", "persona-rej444", "requirement-8fz2mc"}; !slices.Equal(ids, want) {
		t.Errorf("run filter = %v, want %v", ids, want)
	}
	f.Change = changeNeedsReview
	if got := filtered(items, v, f); len(got) != 1 || got[0].ID != "persona-val333" {
		t.Errorf("needs-review filter = %v", got)
	}
}

func TestActivities(t *testing.T) {
	items := rerunItems(t)
	index := &runIndex{Runs: rerunManifests()}
	describe := func(a activity) string {
		switch {
		case a.Human != nil:
			return "human " + string(a.Human.To)
		case a.Point != nil:
			return "resolved " + a.Point.ID
		case a.Run != nil:
			return "run " + a.RunID
		}
		return "events " + a.RunID
	}
	for _, tt := range []struct {
		id    string
		index *runIndex
		want  []string
	}{
		{"persona-val333", index, []string{"run " + run2, "human validated", "run " + run1}},
		{"persona-old666", index, []string{"resolved p9", "run " + run1}},
		// Without manifests, run history events stand alone.
		{"persona-val333", nil, []string{"events " + run2, "human validated", "events " + run1}},
	} {
		var got []string
		for _, a := range activities(byID(items, tt.id), tt.index) {
			got = append(got, describe(a))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s activity = %v, want %v", tt.id, got, tt.want)
		}
	}
	if a := activities(byID(items, "persona-val333"), index)[0]; len(a.Run.Events) != 1 || a.Run.Events[0].To != knowledge.StatusNeedsReview || !a.At.Equal(run2At) {
		t.Errorf("run2 activity = %+v", a)
	}
}

func TestRunPages(t *testing.T) {
	root, h := setupRuns(t)

	// The default view marks the changes of the latest succeeded run.
	w := request(h, http.MethodGet, "/knowledge?status=all", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /knowledge: %d %s", w.Code, body)
	}
	assertContains(t, body, `aria-labelledby="nav-runs"`, "Oct 8 · 09:30", "1 new · 4 updated", "failed: interrupted",
		"Changed by the run of Oct 8 · 09:30", "+1 support", "Stale evidence", "Not updated", `?run=`+run2+`&amp;status=all"`)
	if strings.Contains(body, "run-20261010-gone") {
		t.Error("a run without manifest is listed")
	}

	// The run view: its card, its items and its change filters.
	w = request(h, http.MethodGet, "/knowledge?run="+run2+"&status=all", nil)
	body = w.Body.String()
	assertContains(t, body, "Run of Oct 8 · 09:30", run2, "3f1c2a9 → aaaaaaa", "3 results matched among 6 existing items · 2 evidence went stale",
		"persona-skp555</a> not updated: it changed during the run", "1 project question", "Is &lt;script&gt;alert(1)&lt;/script&gt; billed?",
		"exited &lt;b&gt;badly&lt;/b&gt;", "↻1", `change=needs-review&amp;run=`+run2, "persona-new222", "persona-val333")
	if strings.Contains(body, `data-row="persona-old666"`) || strings.Contains(body, "<script>alert") {
		t.Error("run view lists an untouched item or leaks markup")
	}
	w = request(h, http.MethodGet, "/knowledge?run="+run2+"&status=all&change=needs-review", nil)
	if body = w.Body.String(); !strings.Contains(body, `data-row="persona-val333"`) || strings.Contains(body, `data-row="persona-new222"`) || !strings.Contains(body, `aria-current="true"`) {
		t.Errorf("needs-review view:\n%s", body)
	}

	// A failed run is shown with its error and changed nothing.
	w = request(h, http.MethodGet, "/knowledge?run="+run3+"&status=all", nil)
	assertContains(t, w.Body.String(), "Failed: interrupted. A failed run writes nothing")

	// The detail: runs that saw the item, the banner, grouped interpretations and Activity.
	w = request(h, http.MethodGet, "/knowledge/persona-val333?run="+run2+"&status=all", nil)
	body = w.Body.String()
	assertContains(t, body, "Seen in 1 run", "Changed by the run of", "Validated →", "evidence e1 is stale", ">Activity<",
		"Run of Oct 7 · 09:30", "Ada Lovelace", "Stale evidence: held at its commit")
	w = request(h, http.MethodGet, "/knowledge/persona-k3x9q2", map[string]string{"HX-Request": "true"})
	body = w.Body.String()
	assertContains(t, body, "Seen in 2 runs", "This run", "+1 interpretation from gamma", `href="#evidence-e9"`, "+1 evidence at aaaaaaa")
	if strings.Contains(body, "<html") {
		t.Error("HTMX request received a full document")
	}
	for _, path := range []string{"/knowledge", "/knowledge?run=" + run2 + "&status=all", "/knowledge/requirement-8fz2mc?run=" + run2} {
		if body := request(h, http.MethodGet, path, nil).Body.String(); strings.Contains(body, " style=") {
			t.Errorf("%s has an inline style", path)
		}
	}

	for path, want := range map[string]string{
		"/knowledge?run=run-unknown":                     `run: unknown run &#34;run-unknown&#34;`,
		"/knowledge/persona-k3x9q2?run=run-unknown":      `run: unknown run &#34;run-unknown&#34;`,
		"/knowledge?change=stale":                        "change: requires run",
		"/knowledge?run=" + run2 + "&change=nonexistent": `change: unknown change &#34;nonexistent&#34;`,
	} {
		if w := request(h, http.MethodGet, path, nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
			t.Errorf("GET %s = %d, want 400 with %q", path, w.Code, want)
		}
	}

	// An invalid manifest fails the page and names its file.
	writeManifest(t, root, "run-bad", map[string]any{"version": 3, "id": "run-bad", "confidence": 1})
	w = request(h, http.MethodGet, "/knowledge", nil)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), filepath.Join("run-bad", "manifest.yaml")) {
		t.Errorf("invalid manifest: %d %s", w.Code, w.Body.String())
	}
}

func TestPagesWithoutRuns(t *testing.T) {
	_, _, h := setup(t, true)
	w := request(h, http.MethodGet, "/knowledge/persona-k3x9q2", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, `aria-labelledby="nav-runs"`) || strings.Contains(body, "Changed by the run") {
		t.Errorf("without manifests: %d\n%s", w.Code, body)
	}
	// Runs named by the knowledge files still appear, without links.
	assertContains(t, body, "Seen in 1 run", `title="This run has no manifest"`, ">Activity<", run1)
}
