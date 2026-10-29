package run

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kvitrvn/raun/internal/agenttest"
	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

// reconciledConfig has a single analyst running scenario and the fake
// reconciler running reconciler.
func reconciledConfig(scenario, reconciler string) string {
	return agenttest.WithReconciler(agenttest.Config(agenttest.Argv(scenario), "10s"), agenttest.Argv(reconciler))
}

// firstRun commits the fixture project with a single Valid analyst and a
// reconciler, and runs it once.
func firstRun(t *testing.T) (*gittest.Repo, *Manifest) {
	t.Helper()
	fx := agenttest.NewTeamProject(t, reconciledConfig(agenttest.Valid, agenttest.Reconciler))
	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	return fx, m
}

func byTitle(t *testing.T, dir string) map[string]*knowledge.Item {
	t.Helper()
	items, err := knowledge.NewStore(workspace.KnowledgeDir(dir)).List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*knowledge.Item{}
	for _, it := range items {
		out[it.Title] = it
	}
	return out
}

// decide records a human decision, as raun accept and reject do.
func decide(t *testing.T, dir, id string, to knowledge.Status) {
	t.Helper()
	store := knowledge.NewStore(workspace.KnowledgeDir(dir))
	it, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := knowledge.Revision(it)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(id, revision, func(it *knowledge.Item) error {
		return it.Transition(to, knowledge.Actor{Kind: knowledge.ActorHuman, Name: "PO"}, fixedNow(), "checked")
	})
	if err != nil {
		t.Fatal(err)
	}
}

// knowledgeFiles returns the content of every knowledge file.
func knowledgeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(workspace.KnowledgeDir(dir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		files[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func disagreements(it *knowledge.Item) []knowledge.OpenPoint {
	var out []knowledge.OpenPoint
	for _, p := range it.OpenPoints {
		if p.Kind == knowledge.PointDisagreement {
			out = append(out, p)
		}
	}
	return out
}

func TestRerunSameCommit(t *testing.T) {
	fx, first := firstRun(t)
	before := byTitle(t, fx.Dir)
	decide(t, fx.Dir, before["Accountant"].ID, knowledge.StatusValidated)
	decide(t, fx.Dir, before["Correction window"].ID, knowledge.StatusRejected)
	before = byTitle(t, fx.Dir)

	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if len(m.Items) != 0 || len(m.Updated) != 3 {
		t.Errorf("created %v, updated %v", m.Items, m.Updated)
	}
	if m.Reconciler == nil || m.Reconciler.Status != AgentOK || m.Reconciler.Role != RoleReconciler {
		t.Errorf("reconciler record = %+v", m.Reconciler)
	}
	window := before["Correction window"].ID
	want := ReconciliationSummary{Candidates: 3, Matched: 3, RejectedSupported: []string{window}}
	if !reflect.DeepEqual(*m.Reconciliation, want) {
		t.Errorf("reconciliation = %+v, want %+v", *m.Reconciliation, want)
	}

	after := byTitle(t, fx.Dir)
	for title, old := range before {
		it := after[title]
		if it.Status != old.Status || !reflect.DeepEqual(it.History, old.History) {
			t.Errorf("%s: status %s, history %+v", title, it.Status, it.History)
		}
		if it.Title != old.Title || !reflect.DeepEqual(it.Persona, old.Persona) || !reflect.DeepEqual(it.Requirement, old.Requirement) ||
			!reflect.DeepEqual(it.Support[:len(old.Support)], old.Support) || !reflect.DeepEqual(it.Evidence[:len(old.Evidence)], old.Evidence) {
			t.Errorf("%s: existing content or support changed", title)
		}
		if len(it.Support) != 2 || it.Support[1].Run != m.ID {
			t.Errorf("%s: support = %+v", title, it.Support)
		}
		// The same citations at the same commit, and the same questions,
		// are not added again.
		if len(it.Evidence) != len(old.Evidence) || len(it.OpenPoints) != len(old.OpenPoints) {
			t.Errorf("%s: evidence %d -> %d, open points %d -> %d", title, len(old.Evidence), len(it.Evidence), len(old.OpenPoints), len(it.OpenPoints))
		}
	}

	// The reconciler sees neither agents, runs nor statuses.
	raw := filepath.Join(workspace.RunsDir(fx.Dir), m.ID, "raw", "reconciler")
	prompt, err := os.ReadFile(filepath.Join(raw, "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"alpha", first.ID, `"status"`, "validated", "rejected", "history"} {
		if strings.Contains(string(prompt), hidden) {
			t.Errorf("reconciler prompt reveals %q", hidden)
		}
	}
	if _, err := os.Stat(filepath.Join(raw, "reconciliation.json")); err != nil {
		t.Errorf("reconciliation not kept: %v", err)
	}
}

func TestRerunStaleEvidence(t *testing.T) {
	fx, _ := firstRun(t)
	before := byTitle(t, fx.Dir)
	immutable := before["Issued invoices are immutable"].ID
	decide(t, fx.Dir, immutable, knowledge.StatusValidated)

	fx.Write("billing/invoice.go", "package billing\n\nfunc (s *Service) Update(inv *Invoice) error {\n\treturn s.repo.Save(inv)\n}\n")
	fx.Commit("allow updates")
	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !slices.Equal(m.Reconciliation.NeedsReview, []string{immutable}) || m.Reconciliation.StaleEvidence != 1 {
		t.Errorf("reconciliation = %+v", m.Reconciliation)
	}

	after := byTitle(t, fx.Dir)
	it := after["Issued invoices are immutable"]
	if it.Status != knowledge.StatusNeedsReview || it.Evidence[0].State != knowledge.EvidenceStale {
		t.Fatalf("status %s, evidence %+v", it.Status, it.Evidence[0])
	}
	last := it.History[len(it.History)-1]
	if last.Actor != knowledge.ActorRun || last.By != m.ID || !strings.Contains(last.Reason, "stale evidence: e1") {
		t.Errorf("last event = %+v", last)
	}
	if !slices.ContainsFunc(it.OpenPoints, func(p knowledge.OpenPoint) bool {
		return p.Kind == knowledge.PointUncertainty && p.RaisedBy == "raun" && strings.HasPrefix(p.Summary, "Evidence e1 (billing/invoice.go:4-6) no longer holds")
	}) {
		t.Errorf("no uncertainty for the stale evidence: %+v", it.OpenPoints)
	}
	if a := after["Accountant"]; a.Status != knowledge.StatusProposed || a.Evidence[0].State != knowledge.EvidenceRelocated {
		t.Errorf("accountant: status %s, evidence %+v", a.Status, a.Evidence[0])
	}

	// Once the excerpt is back, the evidence holds again; only a human
	// validates the item again.
	fx.Write("billing/invoice.go", agenttest.Invoice)
	fx.Commit("forbid updates again")
	if _, err := execute(t, fx.Dir, Options{}); err != nil {
		t.Fatalf("third run: %v", err)
	}
	it = byTitle(t, fx.Dir)["Issued invoices are immutable"]
	if it.Status != knowledge.StatusNeedsReview || it.Evidence[0].State != knowledge.EvidenceVerified {
		t.Errorf("status %s, evidence %+v", it.Status, it.Evidence[0])
	}
}

func TestRerunConflict(t *testing.T) {
	for _, tt := range []struct {
		name     string
		accepted bool
		want     knowledge.Status
	}{
		{"validated item", true, knowledge.StatusNeedsReview},
		{"proposed item", false, knowledge.StatusContested},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fx, first := firstRun(t)
			immutable := byTitle(t, fx.Dir)["Issued invoices are immutable"].ID
			if tt.accepted {
				decide(t, fx.Dir, immutable, knowledge.StatusValidated)
			}
			fx.Write(".raun/config.yaml", reconciledConfig(agenttest.Dissent, agenttest.Reconciler))

			m, err := execute(t, fx.Dir, Options{})
			if err != nil {
				t.Fatalf("rerun: %v", err)
			}
			if m.Reconciliation.Matched != 2 || m.Reconciliation.Conflicts != 1 || len(m.Items) != 0 {
				t.Errorf("reconciliation = %+v, created %v", m.Reconciliation, m.Items)
			}
			it := byTitle(t, fx.Dir)["Issued invoices are immutable"]
			if it.Status != tt.want || it.Requirement.Statement != "An issued invoice cannot be updated." {
				t.Errorf("status %s, statement %q", it.Status, it.Requirement.Statement)
			}
			d := disagreements(it)
			if len(d) != 1 || d[0].RaisedBy != "reconciler" || len(d[0].Positions) != 2 {
				t.Fatalf("disagreements = %+v", d)
			}
			old, new := d[0].Positions[0], d[0].Positions[1]
			if old.Statement != "An issued invoice cannot be updated." || old.Run != first.ID || !slices.Equal(old.Agents, []string{"alpha"}) || !slices.Equal(old.Evidence, []string{"e1"}) {
				t.Errorf("existing position = %+v", old)
			}
			if new.Statement != "Admins can correct an issued invoice within 24 hours." || new.Run != m.ID || len(new.Evidence) != 1 {
				t.Errorf("new position = %+v", new)
			}
			if e := it.Evidence[slices.IndexFunc(it.Evidence, func(e knowledge.Evidence) bool { return e.ID == new.Evidence[0] })]; e.Path != "docs/billing.md" {
				t.Errorf("new position backed by %s", e.Path)
			}

			// Finding the same conflict again changes nothing.
			if _, err := execute(t, fx.Dir, Options{}); err != nil {
				t.Fatalf("third run: %v", err)
			}
			again := byTitle(t, fx.Dir)["Issued invoices are immutable"]
			if len(disagreements(again)) != 1 || again.Status != tt.want || len(again.History) != len(it.History) {
				t.Errorf("third run: status %s, %d disagreements", again.Status, len(disagreements(again)))
			}
		})
	}
}

func TestRerunLinksExistingPersona(t *testing.T) {
	fx, _ := firstRun(t)
	before := byTitle(t, fx.Dir)
	store := knowledge.NewStore(workspace.KnowledgeDir(fx.Dir))
	for _, title := range []string{"Issued invoices are immutable", "Correction window"} {
		it := before[title]
		if err := os.Remove(store.Path(it.Type, it.ID)); err != nil {
			t.Fatal(err)
		}
	}

	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if len(m.Items) != 2 || m.Reconciliation.Candidates != 1 || m.Reconciliation.Matched != 1 {
		t.Errorf("created %v, reconciliation %+v", m.Items, m.Reconciliation)
	}
	after := byTitle(t, fx.Dir)
	if got := after["Issued invoices are immutable"].Requirement.Personas; !slices.Equal(got, []string{before["Accountant"].ID}) {
		t.Errorf("persona link = %v, want the existing persona %s", got, before["Accountant"].ID)
	}
}

func TestRerunReconcilerFailures(t *testing.T) {
	tests := []struct {
		reconciler string
		wantErr    string
	}{
		{agenttest.ReconcilerUnknownMatch, `unknown existing item "persona-zzzzzz"`},
		{agenttest.ReconcilerDrop, "every run item must be placed; missing: n3"},
		{agenttest.Crash, "exited with code 2"},
	}
	for _, tt := range tests {
		t.Run(tt.reconciler, func(t *testing.T) {
			fx, _ := firstRun(t)
			files := knowledgeFiles(t, fx.Dir)
			fx.Write(".raun/config.yaml", reconciledConfig(agenttest.Valid, tt.reconciler))

			m, err := execute(t, fx.Dir, Options{})
			if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "reconciler failed") {
				t.Fatalf("rerun error = %v", err)
			}
			if m.Reconciler == nil || m.Reconciler.Status != AgentFailed || !strings.Contains(m.Reconciler.Error, tt.wantErr) {
				t.Errorf("reconciler record = %+v", m.Reconciler)
			}
			if !reflect.DeepEqual(knowledgeFiles(t, fx.Dir), files) {
				t.Error("a run whose reconciler failed changed the knowledge base")
			}
		})
	}
}

// onLine calls do once, the first time a progress line contains prefix.
type onLine struct {
	prefix string
	do     func()
	once   sync.Once
}

func (w *onLine) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.prefix) {
		w.once.Do(w.do)
	}
	return len(p), nil
}

var _ io.Writer = (*onLine)(nil)

func TestRerunKeepsChangesMadeDuringTheRun(t *testing.T) {
	fx, _ := firstRun(t)
	before := byTitle(t, fx.Dir)
	accountant, window := before["Accountant"].ID, before["Correction window"].ID
	store := knowledge.NewStore(workspace.KnowledgeDir(fx.Dir))

	// While the reconciler works, a human validates one item and edits the
	// title of another by hand.
	progress := &onLine{prefix: "reconciler: matching", do: func() {
		decide(t, fx.Dir, accountant, knowledge.StatusValidated)
		it, err := store.Get(window)
		if err != nil {
			t.Fatal(err)
		}
		it.Title = "Correction period"
		if err := store.Save(it); err != nil {
			t.Fatal(err)
		}
	}}
	m, err := execute(t, fx.Dir, Options{Progress: progress})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !slices.Contains(m.Updated, accountant) || slices.Contains(m.Updated, window) {
		t.Errorf("updated = %v", m.Updated)
	}
	if want := []SkippedItem{{Item: window, Reason: "its content changed during the run"}}; !reflect.DeepEqual(m.Reconciliation.Skipped, want) {
		t.Errorf("skipped = %+v", m.Reconciliation.Skipped)
	}

	after := byTitle(t, fx.Dir)
	if a := after["Accountant"]; a.Status != knowledge.StatusValidated || len(a.Support) != 2 {
		t.Errorf("accountant: status %s, %d supports", a.Status, len(a.Support))
	}
	if w := after["Correction period"]; len(w.Support) != 1 {
		t.Errorf("the edited item was updated: %+v", w.Support)
	}
}
