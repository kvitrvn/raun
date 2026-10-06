package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kvitrvn/raun/internal/agenttest"
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

		if _, err := execute(t, fx.Dir, Options{}); err == nil || !strings.Contains(err.Error(), "select one with -agent") {
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
