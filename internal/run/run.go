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
	"sync"
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
	// Agent restricts the run to one agent, by ID. Empty runs them all.
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
	agents, err := selectAgents(cfg, opts.Agent)
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
	fmt.Fprintf(progress, "run %s on commit %s with %d agent(s)\n", m.ID, commit[:12], len(agents))

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
		hidden:   hiddenFromAgents(cfg.Sources.Context),
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
	hidden   func(path string) bool
	mu       sync.Mutex // serializes progress output
}

func (r *runner) execute(ctx context.Context, m *Manifest, agents []config.Agent, store *knowledge.Store, now func() time.Time) error {
	// Analysts work in parallel and independently.
	recs := make([]AgentRecord, len(agents))
	outputs := make([][]byte, len(agents))
	errs := make([]error, len(agents))
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Go(func() {
			recs[i], outputs[i], errs[i] = r.analyze(ctx, a)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		m.Agents = recs
		return err
	}

	// Contract and evidence checks run sequentially (the verifier caches).
	var reports []consolidate.CheckedReport
	for i := range agents {
		if outputs[i] == nil {
			continue
		}
		report, ok, err := r.checkReport(ctx, &recs[i], outputs[i])
		if err != nil {
			m.Agents = recs
			return err
		}
		if ok {
			reports = append(reports, report)
		}
	}
	m.Agents = recs
	if len(reports) < m.Quorum {
		return fmt.Errorf("%w: quorum not met: %d valid report(s), %d required", ErrRunFailed, len(reports), m.Quorum)
	}

	var plan consolidate.Plan
	if len(reports) == 1 {
		plan = consolidate.Identity(reports)
		m.Consolidation = &ConsolidationSummary{Method: MethodIdentity}
	} else {
		var err error
		if plan, err = r.lead(ctx, m, reports); err != nil {
			return err
		}
		m.Consolidation = &ConsolidationSummary{Method: MethodLead}
	}

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
	res, err := consolidate.Build(r.runID, r.commit, now(), reports, plan, newID)
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
		if len(it.Support) > 1 {
			m.Consolidation.Merged++
		}
		if it.Status == knowledge.StatusContested {
			m.Consolidation.Contested++
		}
	}
	m.Consolidation.Items = len(res.Items)
	for _, q := range res.Questions {
		m.Questions = append(m.Questions, QuestionRecord{Agent: q.Agent, ID: q.ID, Question: q.Question})
	}
	r.logf("%d knowledge item(s) proposed, %d merged from several agents, %d contested",
		len(res.Items), m.Consolidation.Merged, m.Consolidation.Contested)
	return nil
}

// analyze runs one analyst. It returns the agent's stdout, or nil if the
// agent failed (the record says why). Only infrastructure problems return
// an error.
func (r *runner) analyze(ctx context.Context, a config.Agent) (AgentRecord, []byte, error) {
	rec := AgentRecord{ID: a.ID, Role: RoleAnalyst, Instructions: a.Instructions, Argv: a.Runner.Argv, Status: AgentFailed}
	instructions, err := r.instructions(a.Instructions)
	if err != nil {
		return rec, nil, err
	}
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
	r.logf("%s: analyzing", a.ID)
	out, err := r.invoke(ctx, &rec, instructions, text, a.Runner)
	return rec, out, err
}

// checkReport validates an analyst's output against the contract, then
// classifies and verifies its citations. ok is false when the report is
// invalid (recorded as an agent failure).
func (r *runner) checkReport(ctx context.Context, rec *AgentRecord, stdout []byte) (consolidate.CheckedReport, bool, error) {
	report, data, err := agent.ParseReport(stdout, r.types)
	if data != nil {
		if err := os.WriteFile(filepath.Join(r.rawDir, rec.ID, "report.json"), data, 0o644); err != nil {
			return consolidate.CheckedReport{}, false, err
		}
	}
	if err != nil {
		r.fail(rec, "invalid report: %v", err)
		return consolidate.CheckedReport{}, false, nil
	}

	checked, summary, err := r.check(ctx, report)
	if err != nil {
		return consolidate.CheckedReport{}, false, err
	}
	rec.Status = AgentOK
	rec.Report = summary
	r.logf("%s: %d interpretation(s), evidence %d verified, %d relocated, %d invalid",
		rec.ID, summary.Interpretations, summary.Evidence.Verified, summary.Evidence.Relocated, summary.Evidence.Invalid)
	return consolidate.CheckedReport{Agent: rec.ID, Report: report, Citations: checked}, true, nil
}

// lead asks the lead to consolidate the reports, anonymized. Any lead
// failure fails the run: the knowledge base never receives unconsolidated
// results.
func (r *runner) lead(ctx context.Context, m *Manifest, reports []consolidate.CheckedReport) (consolidate.Plan, error) {
	l := r.cfg.Lead
	rec := AgentRecord{ID: RoleLead, Role: RoleLead, Instructions: l.Instructions, Argv: l.Runner.Argv, Status: AgentFailed}
	defer func() { m.Lead = &rec }()

	labels := consolidate.Labels(rand.Perm(len(reports)))
	m.Anonymization = map[string]string{}
	for i, label := range labels {
		m.Anonymization[label] = reports[i].Agent
	}
	input, refs, err := consolidate.LeadInput(reports, labels)
	if err != nil {
		return consolidate.Plan{}, err
	}
	instructions, err := r.instructions(l.Instructions)
	if err != nil {
		return consolidate.Plan{}, err
	}
	text, err := prompt.Lead{
		Instructions: instructions,
		Commit:       r.commit,
		Language:     r.cfg.Language,
		Types:        r.types,
		Reports:      string(input),
	}.Render()
	if err != nil {
		return consolidate.Plan{}, err
	}

	r.logf("lead: consolidating %d reports", len(reports))
	out, err := r.invoke(ctx, &rec, instructions, text, l.Runner)
	if err != nil {
		return consolidate.Plan{}, err
	}
	if out == nil {
		return consolidate.Plan{}, fmt.Errorf("%w: lead failed: %s", ErrRunFailed, rec.Error)
	}
	c, data, err := agent.ParseConsolidation(out, r.types, refs)
	if data != nil {
		if err := os.WriteFile(filepath.Join(r.rawDir, rec.ID, "consolidation.json"), data, 0o644); err != nil {
			return consolidate.Plan{}, err
		}
	}
	if err != nil {
		r.fail(&rec, "invalid consolidation: %v", err)
		return consolidate.Plan{}, fmt.Errorf("%w: lead failed: %s", ErrRunFailed, rec.Error)
	}
	plan, err := consolidate.FromLead(c, labels)
	if err != nil {
		return consolidate.Plan{}, err
	}
	rec.Status = AgentOK
	return plan, nil
}

// invoke runs one agent on a fresh snapshot, saving its prompt and output
// under raw/<rec.ID>/. It returns stdout, or nil if the agent failed (the
// record says why). Only infrastructure problems return an error.
func (r *runner) invoke(ctx context.Context, rec *AgentRecord, instructions, text string, cfg config.Runner) ([]byte, error) {
	rec.InstructionsSHA256 = digest([]byte(instructions))
	rec.PromptSHA256 = digest([]byte(text))

	raw := filepath.Join(r.rawDir, rec.ID)
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(raw, "prompt.md"), []byte(text), 0o644); err != nil {
		return nil, err
	}

	snap, err := snapshot.Create(ctx, r.repo, r.commit, "raun-"+r.runID+"-"+rec.ID+"-", r.hidden)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snap.Remove() }()

	ag, err := newRunner(cfg)
	if err != nil {
		return nil, err
	}
	out, runErr := ag.Run(ctx, agent.Request{
		RunID:   r.runID,
		Role:    rec.Role,
		Prompt:  text,
		Dir:     snap.Dir,
		Timeout: time.Duration(cfg.Timeout),
	})
	rec.ExitCode = out.ExitCode
	rec.Duration = out.Duration.Round(time.Millisecond).String()
	if err := errors.Join(
		os.WriteFile(filepath.Join(raw, "stdout.txt"), out.Stdout, 0o644),
		os.WriteFile(filepath.Join(raw, "stderr.txt"), out.Stderr, 0o644),
	); err != nil {
		return nil, err
	}
	if rec.SnapshotChanges, err = snap.Changes(); err != nil {
		return nil, err
	}
	if len(rec.SnapshotChanges) > 0 {
		r.logf("%s: warning: the agent changed %d file(s) in its copy", rec.ID, len(rec.SnapshotChanges))
	}

	switch {
	case runErr != nil:
		r.fail(rec, "%v", runErr)
		return nil, nil
	case out.ExitCode != 0:
		r.fail(rec, "exited with code %d", out.ExitCode)
		return nil, nil
	}
	return out.Stdout, nil
}

func (r *runner) fail(rec *AgentRecord, format string, args ...any) {
	rec.Status = AgentFailed
	rec.Error = fmt.Sprintf(format, args...)
	r.logf("%s: failed: %s", rec.ID, firstLine(rec.Error))
}

func (r *runner) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.progress, format+"\n", args...)
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

// hiddenFromAgents keeps `.raun/` out of agent snapshots, except the
// context files: agents must not see earlier conclusions (knowledge, runs)
// nor which agents and models take part (configuration).
func hiddenFromAgents(contextFiles []string) func(string) bool {
	return func(p string) bool {
		return strings.HasPrefix(p, workspace.Dir+"/") && !slices.Contains(contextFiles, p)
	}
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

// selectAgents returns the agent named id, or all agents. Several agents
// need a lead to consolidate their reports.
func selectAgents(cfg *config.Config, id string) ([]config.Agent, error) {
	all := cfg.Analysis.Agents
	if id != "" {
		i := slices.IndexFunc(all, func(a config.Agent) bool { return a.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("no agent %q in the configuration", id)
		}
		return all[i : i+1], nil
	}
	if len(all) > 1 && cfg.Lead == nil {
		return nil, fmt.Errorf("%d agents are configured but no lead: add a `lead` section to consolidate their reports, or run one agent with -agent", len(all))
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
