// Package run executes a Raun analysis: snapshot, independent agent
// analyses, contract validation, evidence verification, consolidation and
// writing of the knowledge base and run manifest.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/config"
	"github.com/kvitrvn/raun/internal/consolidate"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/prompt"
	"github.com/kvitrvn/raun/internal/snapshot"
	"github.com/kvitrvn/raun/internal/source"
	"github.com/kvitrvn/raun/internal/workspace"
)

// Options configures a run.
type Options struct {
	// Dir is the repository root holding `.raun/`.
	Dir string
	// Commit to analyze. Empty means HEAD, which requires a clean working
	// tree (changes under `.raun/` are ignored, except context files).
	Commit string
	// Agent selects one agent by ID. Until multi-agent consolidation
	// exists, it is required when several agents are configured.
	Agent string
	// RaunVersion is recorded in the manifest.
	RaunVersion string
	// Progress receives one line per notable event. May be nil.
	Progress io.Writer
	// Now defaults to time.Now.
	Now func() time.Time
}

// ErrRunFailed reports a run that completed its bookkeeping but produced no
// knowledge (for instance, no valid agent report). The manifest says why.
var ErrRunFailed = errors.New("run failed")

// Execute performs one analysis run. It returns the manifest, which is also
// written to `.raun/runs/<id>/manifest.yaml` once the run has started.
func Execute(ctx context.Context, opts Options) (*Manifest, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}

	configPath := workspace.ConfigPath(opts.Dir)
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	repo, err := openRoot(ctx, opts.Dir)
	if err != nil {
		return nil, err
	}
	commit, err := resolveCommit(ctx, repo, opts.Commit, cfg.Sources.Context)
	if err != nil {
		return nil, err
	}
	store := knowledge.NewStore(workspace.KnowledgeDir(opts.Dir))
	existing, err := store.List()
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, fmt.Errorf("the knowledge base already holds %d item(s); running again on an existing base needs reconciliation, which is not implemented yet", len(existing))
	}
	agents, err := selectAgents(cfg.Analysis.Agents, opts.Agent)
	if err != nil {
		return nil, err
	}
	types := make([]knowledge.Type, len(cfg.Types))
	for i, t := range cfg.Types {
		types[i] = knowledge.Type(t)
	}

	start := now().UTC().Truncate(time.Second)
	m := &Manifest{
		Version:      ManifestVersion,
		ID:           newRunID(start),
		Status:       StatusFailed,
		StartedAt:    start,
		Commit:       commit,
		RaunVersion:  opts.RaunVersion,
		ConfigSHA256: digest(configData),
		Language:     cfg.Language,
		Types:        cfg.Types,
		Quorum:       min(cfg.Analysis.Quorum, len(agents)),
	}
	runDir := filepath.Join(workspace.RunsDir(opts.Dir), m.ID)
	fmt.Fprintf(progress, "run %s on commit %s\n", m.ID, commit[:12])

	r := &runner{
		repo:     repo,
		cfg:      cfg,
		commit:   commit,
		types:    types,
		runID:    m.ID,
		rawDir:   filepath.Join(runDir, "raw"),
		dir:      opts.Dir,
		progress: progress,
		verifier: evidence.NewVerifier(repo, cfg.Evidence.MaxLines),
		classify: source.NewClassifier(cfg.Sources.Context, cfg.Sources.Docs, cfg.Sources.Exclude),
	}

	runErr := r.execute(ctx, m, agents, store, now)
	m.FinishedAt = now().UTC().Truncate(time.Second)
	if runErr != nil {
		m.Status = StatusFailed
		m.Error = runErr.Error()
	} else {
		m.Status = StatusSucceeded
	}
	if err := m.write(filepath.Join(runDir, "manifest.yaml")); err != nil {
		return m, errors.Join(runErr, fmt.Errorf("write manifest: %w", err))
	}
	return m, runErr
}

type runner struct {
	repo     *gitx.Repo
	cfg      *config.Config
	commit   string
	types    []knowledge.Type
	runID    string
	rawDir   string
	dir      string
	progress io.Writer
	verifier *evidence.Verifier
	classify *source.Classifier
}

func (r *runner) execute(ctx context.Context, m *Manifest, agents []config.Agent, store *knowledge.Store, now func() time.Time) error {
	var reports []consolidate.CheckedReport
	for _, a := range agents {
		rec, report, err := r.analyze(ctx, a)
		if err != nil {
			return err
		}
		m.Agents = append(m.Agents, rec)
		if report != nil {
			reports = append(reports, *report)
		}
	}
	if len(reports) < m.Quorum {
		return fmt.Errorf("%w: quorum not met: %d valid report(s), %d required", ErrRunFailed, len(reports), m.Quorum)
	}

	// Until multi-agent consolidation exists, exactly one report reaches here.
	used := map[string]bool{}
	newID := func(t knowledge.Type) (string, error) {
		for {
			id, err := store.NewID(t)
			if err != nil || !used[id] {
				used[id] = true
				return id, err
			}
		}
	}
	res, err := consolidate.Single(r.runID, r.commit, now(), reports[0], newID)
	if err != nil {
		return err
	}
	for _, it := range res.Items {
		if err := it.Validate(); err != nil {
			return fmt.Errorf("consolidated item %s is invalid: %w", it.ID, err)
		}
	}
	for _, it := range res.Items {
		if err := store.Save(it); err != nil {
			return err
		}
		m.Items = append(m.Items, it.ID)
	}
	for _, q := range res.Questions {
		m.Questions = append(m.Questions, QuestionRecord{Agent: reports[0].Agent, ID: q.ID, Question: q.Question})
	}
	fmt.Fprintf(r.progress, "%d knowledge item(s) proposed\n", len(res.Items))
	return nil
}

// analyze runs one analyst agent. An agent failure is recorded and returns
// a nil report; only infrastructure problems return an error.
func (r *runner) analyze(ctx context.Context, a config.Agent) (AgentRecord, *consolidate.CheckedReport, error) {
	rec := AgentRecord{ID: a.ID, Role: "analyst", Instructions: a.Instructions, Argv: a.Runner.Argv, Status: AgentFailed}
	fail := func(format string, args ...any) (AgentRecord, *consolidate.CheckedReport, error) {
		rec.Error = fmt.Sprintf(format, args...)
		fmt.Fprintf(r.progress, "%s: failed: %s\n", a.ID, firstLine(rec.Error))
		return rec, nil, nil
	}

	instructions, err := r.instructions(a.Instructions)
	if err != nil {
		return rec, nil, err
	}
	rec.InstructionsSHA256 = digest([]byte(instructions))
	text, err := prompt.Analyst{
		Instructions: instructions,
		Commit:       r.commit,
		Language:     r.cfg.Language,
		Types:        r.types,
		ContextFiles: r.cfg.Sources.Context,
		Exclude:      r.cfg.Sources.Exclude,
		MaxLines:     r.cfg.Evidence.MaxLines,
	}.Render()
	if err != nil {
		return rec, nil, err
	}
	rec.PromptSHA256 = digest([]byte(text))

	raw := filepath.Join(r.rawDir, a.ID)
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return rec, nil, err
	}
	if err := os.WriteFile(filepath.Join(raw, "prompt.md"), []byte(text), 0o644); err != nil {
		return rec, nil, err
	}

	snap, err := snapshot.Create(ctx, r.repo, r.commit, "raun-"+r.runID+"-"+a.ID+"-", hiddenFromAgents)
	if err != nil {
		return rec, nil, err
	}
	defer func() { _ = snap.Remove() }()

	fmt.Fprintf(r.progress, "%s: analyzing\n", a.ID)
	ag, err := newRunner(a.Runner)
	if err != nil {
		return rec, nil, err
	}
	out, runErr := ag.Run(ctx, agent.Request{
		RunID:   r.runID,
		Role:    "analyst",
		Prompt:  text,
		Dir:     snap.Dir,
		Timeout: time.Duration(a.Runner.Timeout),
	})
	rec.ExitCode = out.ExitCode
	rec.Duration = out.Duration.Round(time.Millisecond).String()
	if err := errors.Join(
		os.WriteFile(filepath.Join(raw, "stdout.txt"), out.Stdout, 0o644),
		os.WriteFile(filepath.Join(raw, "stderr.txt"), out.Stderr, 0o644),
	); err != nil {
		return rec, nil, err
	}
	if rec.SnapshotChanges, err = snap.Changes(); err != nil {
		return rec, nil, err
	}
	if len(rec.SnapshotChanges) > 0 {
		fmt.Fprintf(r.progress, "%s: warning: the agent changed %d file(s) in its copy\n", a.ID, len(rec.SnapshotChanges))
	}

	if runErr != nil {
		return fail("%v", runErr)
	}
	if out.ExitCode != 0 {
		return fail("exited with code %d", out.ExitCode)
	}
	report, data, err := agent.ParseReport(out.Stdout, r.types)
	if data != nil {
		if err := os.WriteFile(filepath.Join(raw, "report.json"), data, 0o644); err != nil {
			return rec, nil, err
		}
	}
	if err != nil {
		return fail("invalid report: %v", err)
	}

	checked, summary, err := r.check(ctx, report)
	if err != nil {
		return rec, nil, err
	}
	rec.Status = AgentOK
	rec.Report = summary
	fmt.Fprintf(r.progress, "%s: %d interpretation(s), evidence %d verified, %d relocated, %d invalid\n",
		a.ID, summary.Interpretations, summary.Evidence.Verified, summary.Evidence.Relocated, summary.Evidence.Invalid)
	return rec, &consolidate.CheckedReport{Agent: a.ID, Report: report, Citations: checked}, nil
}

// check classifies and verifies every citation of a report.
func (r *runner) check(ctx context.Context, report *agent.Report) (map[string][]consolidate.CheckedCitation, *ReportSummary, error) {
	summary := &ReportSummary{
		Observations:    len(report.Observations),
		Interpretations: len(report.Interpretations),
		Questions:       len(report.Questions),
	}
	checked := map[string][]consolidate.CheckedCitation{}
	for _, o := range report.Observations {
		for _, c := range o.Evidence {
			cc := consolidate.CheckedCitation{Citation: c}
			kind, err := r.classify.Classify(c.Path)
			if err != nil {
				cc.Result = evidence.Result{State: knowledge.EvidenceInvalid, Reason: err.Error()}
			} else {
				cc.Source = kind
				cc.Result, err = r.verifier.Verify(ctx, knowledge.Evidence{
					Path: c.Path, Commit: r.commit, StartLine: c.StartLine, EndLine: c.EndLine, Excerpt: c.Excerpt,
				}, r.commit)
				if err != nil {
					return nil, nil, err
				}
			}

			switch cc.Result.State {
			case knowledge.EvidenceVerified:
				summary.Evidence.Verified++
			case knowledge.EvidenceRelocated:
				summary.Evidence.Relocated++
			default:
				summary.Evidence.Invalid++
				summary.Rejected = append(summary.Rejected, RejectedEvidence{
					Observation: o.ID,
					Path:        c.Path,
					Lines:       fmt.Sprintf("%d-%d", c.StartLine, c.EndLine),
					Reason:      cc.Result.Reason,
				})
			}
			checked[o.ID] = append(checked[o.ID], cc)
		}
	}
	return checked, summary, nil
}

// instructions resolves "builtin:<name>" or a file path relative to the
// repository root.
func (r *runner) instructions(ref string) (string, error) {
	if name, ok := strings.CutPrefix(ref, "builtin:"); ok {
		return prompt.Builtin(name)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(ref)))
	if err != nil {
		return "", fmt.Errorf("read instructions: %w", err)
	}
	return string(data), nil
}

func newRunner(cfg config.Runner) (agent.Runner, error) {
	switch cfg.Kind {
	case config.RunnerCommand:
		return agent.CommandRunner{Argv: cfg.Argv}, nil
	}
	return nil, fmt.Errorf("unsupported runner kind %q", cfg.Kind)
}

// hiddenFromAgents keeps the knowledge base and past runs out of agent
// snapshots, so analyses stay independent of earlier conclusions.
func hiddenFromAgents(p string) bool {
	for _, dir := range []string{workspace.Dir + "/knowledge", workspace.Dir + "/runs"} {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func openRoot(ctx context.Context, dir string) (*gitx.Repo, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	a, errA := filepath.EvalSymlinks(abs)
	b, errB := filepath.EvalSymlinks(repo.Root())
	if err := errors.Join(errA, errB); err != nil {
		return nil, err
	}
	if a != b {
		return nil, fmt.Errorf("%s is not the repository root (%s); run raun from the root or pass -dir", dir, repo.Root())
	}
	return repo, nil
}

func resolveCommit(ctx context.Context, repo *gitx.Repo, rev string, contextFiles []string) (string, error) {
	if rev != "" {
		return repo.ResolveCommit(ctx, rev)
	}
	changed, err := repo.ChangedPaths(ctx)
	if err != nil {
		return "", err
	}
	var dirty []string
	for _, p := range changed {
		if strings.HasPrefix(p, workspace.Dir+"/") && !slices.Contains(contextFiles, p) {
			continue
		}
		dirty = append(dirty, p)
	}
	if len(dirty) > 0 {
		if len(dirty) > 5 {
			dirty = append(dirty[:5], "…")
		}
		return "", fmt.Errorf("the working tree has uncommitted changes (%s); agents analyze committed files only: commit them, or pass -commit to analyze a specific commit", strings.Join(dirty, ", "))
	}
	return repo.ResolveCommit(ctx, "HEAD")
}

func selectAgents(all []config.Agent, id string) ([]config.Agent, error) {
	if id != "" {
		i := slices.IndexFunc(all, func(a config.Agent) bool { return a.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("no agent %q in the configuration", id)
		}
		return all[i : i+1], nil
	}
	if len(all) > 1 {
		return nil, errors.New("several agents are configured but multi-agent consolidation is not implemented yet; select one with -agent")
	}
	return all, nil
}

const runIDAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// newRunID returns a sortable, readable run ID such as 20261007-093000-k3x9.
func newRunID(t time.Time) string {
	b := make([]byte, 4)
	for i := range b {
		b[i] = runIDAlphabet[rand.IntN(len(runIDAlphabet))]
	}
	return t.UTC().Format("20060102-150405") + "-" + string(b)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
