package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kvitrvn/raun/internal/agenttest"
	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

func TestMain(m *testing.M) {
	agenttest.MaybeRun()
	os.Exit(m.Run())
}

var fixedNow = func() time.Time { return time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC) }

func execute(t *testing.T, dir string, opts Options) (*Manifest, error) {
	t.Helper()
	opts.Dir = dir
	opts.Now = fixedNow
	opts.RaunVersion = "test"
	return Execute(context.Background(), opts)
}

func TestRunValid(t *testing.T) {
	fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
	head := fx.Git("rev-parse", "HEAD")

	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Manifest.
	if m.Status != StatusSucceeded || m.Commit != head || m.Quorum != 1 || len(m.Items) != 3 {
		t.Errorf("manifest = %+v", m)
	}
	if !strings.HasPrefix(m.ID, "20261007-093000-") {
		t.Errorf("run ID = %q", m.ID)
	}
	a := m.Agents[0]
	if a.Status != AgentOK || a.ExitCode != 0 || a.PromptSHA256 == "" || a.InstructionsSHA256 == "" {
		t.Errorf("agent record = %+v", a)
	}
	if got := a.Report.Evidence; got != (EvidenceCounts{Verified: 2, Relocated: 1, Invalid: 2}) {
		t.Errorf("evidence counts = %+v", got)
	}
	if len(a.Report.Rejected) != 2 || !strings.Contains(a.Report.Rejected[0].Reason, "excluded") {
		t.Errorf("rejected evidence = %+v", a.Report.Rejected)
	}
	if len(m.Questions) != 1 || m.Questions[0].ID != "q2" {
		t.Errorf("run-level questions = %+v", m.Questions)
	}

	// The manifest on disk matches.
	runDir := filepath.Join(workspace.RunsDir(fx.Dir), m.ID)
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Manifest
	if err := yaml.Unmarshal(data, &onDisk); err != nil || onDisk.ID != m.ID || onDisk.Status != StatusSucceeded {
		t.Errorf("manifest on disk = %+v, %v", onDisk, err)
	}
	for _, f := range []string{"prompt.md", "stdout.txt", "stderr.txt", "report.json"} {
		if _, err := os.Stat(filepath.Join(runDir, "raw", "alpha", f)); err != nil {
			t.Errorf("raw artifact missing: %v", err)
		}
	}
	if out := fx.Git("status", "--porcelain", "--untracked-files=all"); strings.Contains(out, "raw/") {
		t.Errorf("raw artifacts are not ignored by Git:\n%s", out)
	}

	// Knowledge.
	items, err := knowledge.NewStore(workspace.KnowledgeDir(fx.Dir)).List()
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]*knowledge.Item{}
	for _, it := range items {
		byTitle[it.Title] = it
		if it.Status != knowledge.StatusProposed || it.History[0].By != m.ID {
			t.Errorf("%s: status %s, history %+v", it.ID, it.Status, it.History)
		}
	}

	persona := byTitle["Accountant"]
	if e := persona.Evidence[0]; e.Source != knowledge.SourceHumanContext || e.State != knowledge.EvidenceRelocated ||
		e.StartLine != 3 || e.Excerpt != "The product is used by accountants." || e.Commit != head {
		t.Errorf("persona evidence = %+v", e)
	}

	immutable := byTitle["Issued invoices are immutable"]
	if e := immutable.Evidence; len(e) != 1 || e[0].Excerpt != "\tif inv.Status == StatusIssued {\n\t\treturn ErrInvoiceLocked\n\t}" {
		t.Errorf("stored excerpt must be the file's own text: %+v", e)
	}
	if immutable.Requirement.Personas[0] != persona.ID {
		t.Errorf("persona link = %v", immutable.Requirement.Personas)
	}
	kinds := func(it *knowledge.Item) []knowledge.PointKind {
		var k []knowledge.PointKind
		for _, p := range it.OpenPoints {
			k = append(k, p.Kind)
		}
		return k
	}
	if got := kinds(immutable); !slices.Equal(got, []knowledge.PointKind{knowledge.PointUncertainty, knowledge.PointQuestion}) {
		t.Errorf("open points = %v", got)
	}

	window := byTitle["Correction window"]
	if window.Support[0].Nature != knowledge.NatureHypothesis || len(window.Evidence) != 0 {
		t.Errorf("correction window must be downgraded: %+v", window.Support[0])
	}

	// Snapshots are removed.
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "raun-"+m.ID+"-*")); len(left) != 0 {
		t.Errorf("snapshots left behind: %v", left)
	}
}

func TestRunChattyAgent(t *testing.T) {
	fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Chatty))
	if m, err := execute(t, fx.Dir, Options{}); err != nil || len(m.Items) != 3 {
		t.Fatalf("Execute() = %+v, %v", m, err)
	}
}

func TestRunTamperingAgent(t *testing.T) {
	fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Tamper))
	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Agents[0].SnapshotChanges; !slices.Equal(got, []string{"modified: billing/invoice.go"}) {
		t.Errorf("snapshot changes = %v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(fx.Dir, "billing", "invoice.go")); string(data) != agenttest.Invoice {
		t.Error("the agent modified the repository")
	}
}

func TestRunAgentFailures(t *testing.T) {
	tests := []struct {
		scenario string
		timeout  string
		wantErr  string
	}{
		{agenttest.Garbage, "10s", "invalid report: no JSON object"},
		{agenttest.BadContract, "10s", `unknown field "confidence"`},
		{agenttest.Crash, "10s", "exited with code 2"},
		{agenttest.Hang, "500ms", "agent timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			fx := agenttest.NewProject(t, agenttest.Argv(tt.scenario))
			fx.Write(".raun/config.yaml", agenttest.Config(agenttest.Argv(tt.scenario), tt.timeout))

			m, err := execute(t, fx.Dir, Options{})
			if !errors.Is(err, ErrRunFailed) {
				t.Fatalf("Execute() error = %v, want ErrRunFailed", err)
			}
			if m.Status != StatusFailed || !strings.Contains(m.Error, "quorum not met") {
				t.Errorf("manifest status = %s, error = %q", m.Status, m.Error)
			}
			if a := m.Agents[0]; a.Status != AgentFailed || !strings.Contains(a.Error, tt.wantErr) {
				t.Errorf("agent record = %+v", a)
			}
			if _, err := os.Stat(filepath.Join(workspace.RunsDir(fx.Dir), m.ID, "manifest.yaml")); err != nil {
				t.Errorf("failed run has no manifest: %v", err)
			}
			if _, err := os.Stat(workspace.KnowledgeDir(fx.Dir)); !errors.Is(err, os.ErrNotExist) {
				t.Error("a failed run wrote knowledge")
			}
		})
	}
}

func TestRunPreconditions(t *testing.T) {
	t.Run("dirty tree", func(t *testing.T) {
		fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
		fx.Write("billing/invoice.go", "package billing // wip\n")
		fx.Write(".raun/config.yaml", agenttest.Config(agenttest.Argv(agenttest.Valid), "10s")) // ignored

		_, err := execute(t, fx.Dir, Options{})
		if err == nil || !strings.Contains(err.Error(), "uncommitted changes (billing/invoice.go)") {
			t.Fatalf("error = %v", err)
		}
		// An explicit commit is analyzed regardless of the working tree.
		if _, err := execute(t, fx.Dir, Options{Commit: "HEAD"}); err != nil {
			t.Errorf("with Commit: %v", err)
		}
	})

	t.Run("uncommitted context file", func(t *testing.T) {
		fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
		fx.Write(".raun/context.md", "changed\n")
		if _, err := execute(t, fx.Dir, Options{}); err == nil || !strings.Contains(err.Error(), ".raun/context.md") {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("existing knowledge base", func(t *testing.T) {
		fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
		if _, err := execute(t, fx.Dir, Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, fx.Dir, Options{}); err == nil || !strings.Contains(err.Error(), "already holds 3 item(s)") {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("agent selection", func(t *testing.T) {
		fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
		cfg := agenttest.Config(agenttest.Argv(agenttest.Valid), "10s") +
			"    - id: beta\n      runner: { argv: [\"unused\"] }\n"
		fx.Write(".raun/config.yaml", cfg)

		if _, err := execute(t, fx.Dir, Options{}); err == nil || !strings.Contains(err.Error(), "2 agents are configured but no lead") {
			t.Errorf("error = %v", err)
		}
		if _, err := execute(t, fx.Dir, Options{Agent: "gamma"}); err == nil || !strings.Contains(err.Error(), `no agent "gamma"`) {
			t.Errorf("error = %v", err)
		}
		m, err := execute(t, fx.Dir, Options{Agent: "alpha"})
		if err != nil || len(m.Agents) != 1 || m.Agents[0].ID != "alpha" {
			t.Errorf("Execute(alpha) = %+v, %v", m, err)
		}
	})

	t.Run("not the repository root", func(t *testing.T) {
		fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))
		sub := filepath.Join(fx.Dir, "billing")
		if err := os.MkdirAll(filepath.Join(sub, ".raun"), 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(fx.Dir, ".raun", "config.yaml"))
		_ = os.WriteFile(filepath.Join(sub, ".raun", "config.yaml"), data, 0o644)
		if _, err := execute(t, sub, Options{}); err == nil || !strings.Contains(err.Error(), "not the repository root") {
			t.Errorf("error = %v", err)
		}
	})
}

func team(t *testing.T, quorum int, lead string, alpha, beta string) *gittest.Repo {
	t.Helper()
	cfg := agenttest.TeamConfig("10s", agenttest.Argv(lead),
		agenttest.Agent{ID: "alpha", Argv: agenttest.Argv(alpha)},
		agenttest.Agent{ID: "beta", Argv: agenttest.Argv(beta)})
	cfg = strings.Replace(cfg, "quorum: 1", fmt.Sprintf("quorum: %d", quorum), 1)
	return agenttest.NewTeamProject(t, cfg)
}

func TestRunTeam(t *testing.T) {
	fx := team(t, 2, agenttest.Lead, agenttest.Valid, agenttest.Dissent)

	m, err := execute(t, fx.Dir, Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := *m.Consolidation; got != (ConsolidationSummary{Method: MethodLead, Items: 3, Merged: 2, Contested: 1}) {
		t.Errorf("consolidation = %+v", got)
	}
	if m.Lead == nil || m.Lead.Status != AgentOK || m.Lead.Role != RoleLead {
		t.Errorf("lead record = %+v", m.Lead)
	}
	agents := []string{m.Anonymization["A"], m.Anonymization["B"]}
	slices.Sort(agents)
	if len(m.Anonymization) != 2 || !slices.Equal(agents, []string{"alpha", "beta"}) {
		t.Errorf("anonymization = %v", m.Anonymization)
	}

	// The lead never learns who the analysts are.
	leadPrompt, err := os.ReadFile(filepath.Join(workspace.RunsDir(fx.Dir), m.ID, "raw", "lead", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if strings.Contains(string(leadPrompt), name) {
			t.Errorf("lead prompt reveals agent %q", name)
		}
	}

	items, err := knowledge.NewStore(workspace.KnowledgeDir(fx.Dir)).List()
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]*knowledge.Item{}
	for _, it := range items {
		byTitle[it.Title] = it
	}

	accountant := byTitle["Accountant"]
	if len(accountant.Support) != 2 || accountant.Status != knowledge.StatusProposed {
		t.Errorf("accountant = %s with %d supports", accountant.Status, len(accountant.Support))
	}
	if len(accountant.Evidence) != 1 {
		t.Errorf("both agents cite the same context line, stored once: %+v", accountant.Evidence)
	}

	immutable := byTitle["Issued invoices are immutable"]
	if immutable.Status != knowledge.StatusContested {
		t.Fatalf("status = %s, want contested", immutable.Status)
	}
	i := slices.IndexFunc(immutable.OpenPoints, func(p knowledge.OpenPoint) bool { return p.Kind == knowledge.PointDisagreement })
	if i < 0 {
		t.Fatalf("no disagreement: %+v", immutable.OpenPoints)
	}
	d := immutable.OpenPoints[i]
	if len(d.Positions) != 2 || d.RaisedBy != "lead" {
		t.Fatalf("disagreement = %+v", d)
	}
	for _, p := range d.Positions {
		if len(p.Agents) != 1 || len(p.Evidence) != 1 {
			t.Errorf("position %q: agents %v, evidence %v", p.Statement, p.Agents, p.Evidence)
		}
		e := immutable.Evidence[slices.IndexFunc(immutable.Evidence, func(e knowledge.Evidence) bool { return e.ID == p.Evidence[0] })]
		wantPath := map[string]string{"alpha": "billing/invoice.go", "beta": "docs/billing.md"}[p.Agents[0]]
		if e.Path != wantPath {
			t.Errorf("position of %s backed by %s, want %s", p.Agents[0], e.Path, wantPath)
		}
	}
	if immutable.Requirement.Personas[0] != accountant.ID {
		t.Errorf("persona link = %v", immutable.Requirement.Personas)
	}

	// A contested item cannot be validated before its disagreement is resolved.
	err = immutable.Transition(knowledge.StatusValidated, knowledge.Actor{Kind: knowledge.ActorHuman, Name: "PO"}, fixedNow(), "ok")
	if !errors.Is(err, knowledge.ErrTransition) {
		t.Errorf("validating a contested item: %v", err)
	}
}

func TestRunTeamLeadFailures(t *testing.T) {
	tests := []struct {
		lead    string
		wantErr string
	}{
		{agenttest.LeadUnknownRef, `unknown interpretation "Z.i9"`},
		{agenttest.LeadDrop, "every interpretation must be placed"},
		{agenttest.Crash, "exited with code 2"},
	}
	for _, tt := range tests {
		t.Run(tt.lead, func(t *testing.T) {
			fx := team(t, 2, tt.lead, agenttest.Valid, agenttest.Dissent)
			m, err := execute(t, fx.Dir, Options{})
			if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "lead failed") {
				t.Fatalf("Execute() error = %v", err)
			}
			if m.Lead == nil || m.Lead.Status != AgentFailed || !strings.Contains(m.Lead.Error, tt.wantErr) {
				t.Errorf("lead record = %+v", m.Lead)
			}
			if _, err := os.Stat(workspace.KnowledgeDir(fx.Dir)); !errors.Is(err, os.ErrNotExist) {
				t.Error("a run whose lead failed wrote knowledge")
			}
		})
	}
}

func TestRunTeamWithFailingAnalyst(t *testing.T) {
	t.Run("quorum met by one report", func(t *testing.T) {
		fx := team(t, 1, agenttest.Lead, agenttest.Valid, agenttest.Garbage)
		m, err := execute(t, fx.Dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if m.Consolidation.Method != MethodIdentity || m.Lead != nil || len(m.Items) != 3 {
			t.Errorf("one valid report needs no lead: %+v, lead %+v", m.Consolidation, m.Lead)
		}
		if m.Agents[1].Status != AgentFailed {
			t.Errorf("beta = %+v", m.Agents[1])
		}
	})
	t.Run("quorum not met", func(t *testing.T) {
		fx := team(t, 2, agenttest.Lead, agenttest.Valid, agenttest.Garbage)
		m, err := execute(t, fx.Dir, Options{})
		if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "1 valid report(s), 2 required") {
			t.Fatalf("Execute() error = %v", err)
		}
		if m.Lead != nil {
			t.Error("the lead ran although the quorum was not met")
		}
	})
}
