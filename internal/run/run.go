// Package run executes a Raun analysis: snapshot, independent agent
// analyses, contract validation, evidence verification, consolidation and
// writing of the knowledge base and run manifest.
package run

import (
	"bytes"
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
	"github.com/kvitrvn/raun/internal/reconcile"
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
	agents, err := selectAgents(cfg, opts.Agent)
	if err != nil {
		return nil, err
	}
	types := make([]knowledge.Type, len(cfg.Types))
	for i, t := range cfg.Types {
		types[i] = knowledge.Type(t)
	}
	store := knowledge.NewStore(workspace.KnowledgeDir(opts.Dir))
	existing, err := store.List()
	if err != nil {
		return nil, err
	}
	if n := len(candidates(existing, types)); n > 0 && cfg.Reconciler == nil {
		return nil, fmt.Errorf("the knowledge base holds %d item(s) of the configured types: add a `reconciler` section to match new results against them (see docs/agents.md)", n)
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

	runErr := r.execute(ctx, m, agents, store, existing, now)
	if runErr != nil && ctx.Err() != nil {
		// Whatever failed (a quorum, the lead), the interruption caused it.
		runErr = fmt.Errorf("%w: %w", ErrRunFailed, agent.ErrInterrupted)
	}
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

func (r *runner) execute(ctx context.Context, m *Manifest, agents []config.Agent, store *knowledge.Store, existing []*knowledge.Item, now func() time.Time) error {
	// Analysts work in parallel and independently.
	recs := make([]AgentRecord, len(agents))
	parsed := make([]*agent.Report, len(agents))
	errs := make([]error, len(agents))
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Go(func() {
			recs[i], parsed[i], errs[i] = r.analyze(ctx, a)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		m.Agents = recs
		return err
	}

	// Evidence checks run sequentially (the verifier caches).
	var reports []consolidate.CheckedReport
	for i := range agents {
		if parsed[i] == nil {
			continue
		}
		report, err := r.checkReport(ctx, &recs[i], parsed[i])
		if err != nil {
			m.Agents = recs
			return err
		}
		reports = append(reports, report)
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

	// Planned items describing an existing item take its ID.
	var matches []reconcile.Match
	if shown := candidates(existing, r.types); len(shown) > 0 && len(plan.Items) > 0 {
		rec, err := r.reconcile(ctx, m, shown, plan, reports)
		if err != nil {
			return err
		}
		matches = reconcile.Assign(&plan, rec)
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
	at := now()
	res, err := consolidate.Build(r.runID, r.commit, at, reports, plan, newID)
	if err != nil {
		return err
	}
	var created []*knowledge.Item
	for i, it := range res.Items {
		if plan.Items[i].ID != "" {
			continue
		}
		if err := it.Validate(); err != nil {
			return fmt.Errorf("consolidated item %s is invalid: %w", it.ID, err)
		}
		created = append(created, it)
	}
	m.Consolidation.Items = len(res.Items)
	for _, it := range res.Items {
		if len(it.Support) > 1 {
			m.Consolidation.Merged++
		}
		if it.Status == knowledge.StatusContested {
			m.Consolidation.Contested++
		}
	}
	r.logf("%d knowledge item(s) proposed, %d merged from several agents, %d contested",
		len(res.Items), m.Consolidation.Merged, m.Consolidation.Contested)

	if len(existing) > 0 {
		if err := r.update(ctx, m, store, existing, reconcile.Attachments(res.Items, matches), at); err != nil {
			return err
		}
		m.Reconciliation.Matched = len(matches)
		for _, match := range matches {
			if match.Conflict != "" {
				m.Reconciliation.Conflicts++
			}
		}
	}
	for _, it := range created {
		if err := store.Save(it); err != nil {
			return err
		}
		m.Items = append(m.Items, it.ID)
	}
	for _, q := range res.Questions {
		m.Questions = append(m.Questions, QuestionRecord{Agent: q.Agent, ID: q.ID, Question: q.Question})
	}
	if m.Reconciliation != nil {
		r.logf("%d item(s) created, %d existing item(s) updated, %d need review, %d evidence stale",
			len(m.Items), len(m.Updated), len(m.Reconciliation.NeedsReview), m.Reconciliation.StaleEvidence)
	}
	return nil
}

// update brings the run's results to the existing items: the attachments of
// the reconciliation, and the freshness of their evidence at the analyzed
// commit. Every change is computed and validated before anything is
// written. Each item is then reread and updated under its lock, so that a
// human decision taken during the run is kept; an item whose matched
// content changed in the meantime is skipped.
func (r *runner) update(ctx context.Context, m *Manifest, store *knowledge.Store, existing []*knowledge.Item, attach map[string][]reconcile.Attachment, at time.Time) error {
	if m.Reconciliation == nil {
		m.Reconciliation = &ReconciliationSummary{}
	}
	fresh, err := reconcile.CheckFreshness(ctx, existing, r.commit, r.verifier, r.repo)
	if err != nil {
		return err
	}

	// Dry run on the listed items, which are not saved. Apply never changes
	// their content, which stays what the reconciler was shown.
	var changed []*knowledge.Item
	for _, it := range existing {
		out, err := reconcile.Apply(it, attach[it.ID], fresh, r.runID, at)
		if err != nil {
			return fmt.Errorf("reconcile %s: %w", it.ID, err)
		}
		if !out.Changed {
			continue
		}
		if err := it.Validate(); err != nil {
			return fmt.Errorf("reconciled item %s is invalid: %w", it.ID, err)
		}
		changed = append(changed, it)
	}

	for _, listed := range changed {
		skip := func(reason string) {
			m.Reconciliation.Skipped = append(m.Reconciliation.Skipped, SkippedItem{Item: listed.ID, Reason: reason})
			r.logf("%s: warning: not updated: %s", listed.ID, reason)
		}
		current, err := store.Get(listed.ID)
		if errors.Is(err, knowledge.ErrNotFound) {
			skip("it was removed during the run")
			continue
		}
		if err != nil {
			return err
		}
		if len(attach[listed.ID]) > 0 && !reconcile.SameContent(listed, current) {
			skip("its content changed during the run")
			continue
		}
		revision, err := knowledge.Revision(current)
		if err != nil {
			return err
		}
		from := current.Status
		var out reconcile.Outcome
		var status knowledge.Status
		err = store.Update(listed.ID, revision, func(it *knowledge.Item) error {
			var err error
			out, err = reconcile.Apply(it, attach[it.ID], fresh, r.runID, at)
			status = it.Status
			return err
		})
		if errors.Is(err, knowledge.ErrConflict) {
			skip("it changed during the run")
			continue
		}
		if err != nil {
			return err
		}
		m.Updated = append(m.Updated, listed.ID)
		m.Reconciliation.StaleEvidence += len(out.Stale)
		if from == knowledge.StatusValidated && status == knowledge.StatusNeedsReview {
			m.Reconciliation.NeedsReview = append(m.Reconciliation.NeedsReview, listed.ID)
		}
		if status == knowledge.StatusRejected && len(attach[listed.ID]) > 0 {
			m.Reconciliation.RejectedSupported = append(m.Reconciliation.RejectedSupported, listed.ID)
		}
	}
	return nil
}

// reconcile asks the reconciler which existing item each planned item
// describes. Any reconciler failure fails the run: the knowledge base never
// receives unreconciled results.
func (r *runner) reconcile(ctx context.Context, m *Manifest, shown []*knowledge.Item, plan consolidate.Plan, reports []consolidate.CheckedReport) (*agent.Reconciliation, error) {
	c := r.cfg.Reconciler
	rec := AgentRecord{ID: RoleReconciler, Role: RoleReconciler, Instructions: c.Instructions, Argv: c.Runner.Argv, Status: AgentFailed}
	defer func() { m.Reconciler = &rec }()
	m.Reconciliation = &ReconciliationSummary{Candidates: len(shown)}

	input, refs, err := reconcile.Input(shown, plan, reports)
	if err != nil {
		return nil, err
	}
	instructions, err := r.instructions(c.Instructions)
	if err != nil {
		return nil, err
	}
	text, err := prompt.Reconciler{
		Instructions: instructions,
		Commit:       r.commit,
		Language:     r.cfg.Language,
		Types:        r.types,
		Items:        string(input),
	}.Render()
	if err != nil {
		return nil, err
	}

	r.logf("reconciler: matching %d item(s) against %d existing", len(plan.Items), len(shown))
	var out *agent.Reconciliation
	ok, err := r.invoke(ctx, &rec, instructions, text, c.Runner, func(stdout []byte, salvage bool) error {
		var data []byte
		var err error
		out, data, err = agent.ParseReconciliation(stdout, refs.Existing, refs.Items, salvage)
		if data != nil {
			if err := os.WriteFile(filepath.Join(r.rawDir, rec.ID, "reconciliation.json"), data, 0o644); err != nil {
				return &infraError{err}
			}
		}
		if err != nil {
			return fmt.Errorf("invalid reconciliation: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: reconciler failed: %s", ErrRunFailed, rec.Error)
	}
	rec.Status = AgentOK
	return out, nil
}

// candidates returns the existing items of the given types: those the
// reconciler matches the run's results against.
func candidates(items []*knowledge.Item, types []knowledge.Type) []*knowledge.Item {
	var out []*knowledge.Item
	for _, it := range items {
		if slices.Contains(types, it.Type) {
			out = append(out, it)
		}
	}
	return out
}

// analyze runs one analyst. It returns the agent's stdout, or nil if the
// agent failed (the record says why). Only infrastructure problems return
// an error.
func (r *runner) analyze(ctx context.Context, a config.Agent) (AgentRecord, *agent.Report, error) {
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
	var report *agent.Report
	ok, err := r.invoke(ctx, &rec, instructions, text, a.Runner, func(out []byte, salvage bool) error {
		var data []byte
		var err error
		report, data, err = agent.ParseReport(out, r.types, salvage)
		if data != nil {
			if err := os.WriteFile(filepath.Join(r.rawDir, rec.ID, "report.json"), data, 0o644); err != nil {
				return &infraError{err}
			}
		}
		if err != nil {
			return fmt.Errorf("invalid report: %w", err)
		}
		return nil
	})
	if !ok {
		report = nil
	}
	return rec, report, err
}

// checkReport classifies and verifies the citations of a valid report.
func (r *runner) checkReport(ctx context.Context, rec *AgentRecord, report *agent.Report) (consolidate.CheckedReport, error) {
	checked, summary, err := r.check(ctx, report)
	if err != nil {
		return consolidate.CheckedReport{}, err
	}
	rec.Status = AgentOK
	rec.Report = summary
	r.logf("%s: %d interpretation(s), evidence %d verified, %d relocated, %d invalid",
		rec.ID, summary.Interpretations, summary.Evidence.Verified, summary.Evidence.Relocated, summary.Evidence.Invalid)
	return consolidate.CheckedReport{Agent: rec.ID, Report: report, Citations: checked}, nil
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
	var c *agent.Consolidation
	ok, err := r.invoke(ctx, &rec, instructions, text, l.Runner, func(out []byte, salvage bool) error {
		var data []byte
		var err error
		c, data, err = agent.ParseConsolidation(out, r.types, refs, salvage)
		if data != nil {
			if err := os.WriteFile(filepath.Join(r.rawDir, rec.ID, "consolidation.json"), data, 0o644); err != nil {
				return &infraError{err}
			}
		}
		if err != nil {
			return fmt.Errorf("invalid consolidation: %w", err)
		}
		return nil
	})
	if err != nil {
		return consolidate.Plan{}, err
	}
	if !ok {
		return consolidate.Plan{}, fmt.Errorf("%w: lead failed: %s", ErrRunFailed, rec.Error)
	}
	plan, err := consolidate.FromLead(c, labels)
	if err != nil {
		return consolidate.Plan{}, err
	}
	rec.Status = AgentOK
	return plan, nil
}

// minRetryOutput is the least an agent must have printed to be asked again
// for its answer: below it, there is nothing to build an answer from.
const minRetryOutput = 64

// infraError wraps an infrastructure failure met while parsing an answer
// (writing a raw artifact), so that it aborts the run instead of failing
// the agent.
type infraError struct{ err error }

func (e *infraError) Error() string { return e.err.Error() }
func (e *infraError) Unwrap() error { return e.err }

// invoke runs one agent on a fresh snapshot, saving its prompts and
// outputs under raw/<rec.ID>/. parse checks an output against the contract;
// salvage is set for an agent stopped by its timeout. An agent that printed
// something but no valid answer is asked again for it, up to
// cfg.ReportRetries times, on the same snapshot (see docs/agents.md).
//
// ok is false when the agent failed; the record says why. Only
// infrastructure problems return an error.
func (r *runner) invoke(ctx context.Context, rec *AgentRecord, instructions, text string, cfg config.Runner, parse func(out []byte, salvage bool) error) (ok bool, err error) {
	rec.InstructionsSHA256 = digest([]byte(instructions))
	rec.PromptSHA256 = digest([]byte(text))

	raw := filepath.Join(r.rawDir, rec.ID)
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return false, err
	}

	snap, err := snapshot.Create(ctx, r.repo, r.commit, "raun-"+r.runID+"-"+rec.ID+"-", r.hidden)
	if err != nil {
		return false, err
	}
	defer func() { _ = snap.Remove() }()

	ag, err := newRunner(cfg)
	if err != nil {
		return false, err
	}

	ok, err = r.attempts(ctx, rec, ag, raw, snap.Dir, text, cfg, parse)
	if err != nil {
		return false, err
	}
	if rec.SnapshotChanges, err = snap.Changes(); err != nil {
		return false, err
	}
	if len(rec.SnapshotChanges) > 0 {
		r.logf("%s: warning: the agent changed %d file(s) in its copy", rec.ID, len(rec.SnapshotChanges))
	}
	return ok, nil
}

// attempts runs the first attempt and the retries of invoke.
func (r *runner) attempts(ctx context.Context, rec *AgentRecord, ag agent.Runner, raw, dir, text string, cfg config.Runner, parse func([]byte, bool) error) (bool, error) {
	task := text
	timeout := time.Duration(cfg.Timeout)
	var total time.Duration
	for n := 0; ; n++ {
		suffix := ""
		if n > 0 {
			suffix = fmt.Sprintf(".retry%d", n)
		}
		if err := os.WriteFile(filepath.Join(raw, "prompt"+suffix+".md"), []byte(text), 0o644); err != nil {
			return false, err
		}
		out, runErr := ag.Run(ctx, agent.Request{
			RunID:          r.runID,
			Role:           rec.Role,
			Prompt:         text,
			Dir:            dir,
			Timeout:        timeout,
			MaxOutputBytes: *cfg.MaxOutputBytes,
		})
		total += out.Duration
		rec.ExitCode = out.ExitCode
		rec.Duration = total.Round(time.Millisecond).String()
		if err := errors.Join(
			os.WriteFile(filepath.Join(raw, "stdout"+suffix+".txt"), out.Stdout, 0o644),
			os.WriteFile(filepath.Join(raw, "stderr"+suffix+".txt"), out.Stderr, 0o644),
		); err != nil {
			return false, err
		}

		timedOut := errors.Is(runErr, agent.ErrTimeout)
		switch {
		case runErr != nil && !timedOut:
			r.fail(rec, "%v", runErr)
			return false, nil
		case !timedOut && out.ExitCode != 0:
			r.fail(rec, "exited with code %d", out.ExitCode)
			return false, nil
		}

		perr := parse(out.Stdout, timedOut)
		if infra, ok := errors.AsType[*infraError](perr); ok {
			return false, infra.err
		}
		if perr == nil {
			if timedOut {
				rec.Salvaged = true
				r.logf("%s: warning: %v; keeping the answer it printed before it was stopped", rec.ID, runErr)
			}
			return true, nil
		}

		problem := perr
		if timedOut {
			problem = fmt.Errorf("%w, with no valid answer: %w", runErr, perr)
		}
		size := len(bytes.TrimSpace(out.Stdout))
		switch {
		case size < minRetryOutput:
			r.fail(rec, "printed almost nothing (%d bytes): %v", size, problem)
			return false, nil
		case n >= *cfg.ReportRetries, timedOut && *cfg.ReportGrace == 0, ctx.Err() != nil:
			r.fail(rec, "%v", problem)
			return false, nil
		}

		if timedOut {
			timeout = time.Duration(*cfg.ReportGrace)
		} else {
			timeout = time.Duration(cfg.Timeout)
		}
		var err error
		text, err = prompt.Retry{Task: task, Problems: perr.Error(), Output: out.Stdout}.Render()
		if err != nil {
			return false, err
		}
		rec.ReportRetries++
		r.logf("%s: no valid answer (%s); asking again", rec.ID, firstLine(problem.Error()))
	}
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
