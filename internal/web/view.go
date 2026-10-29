package web

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/run"
)

// Presentation helpers for the templates. Labels follow the browser design.

var statusLabels = map[knowledge.Status]string{
	knowledge.StatusValidated:   "Validated",
	knowledge.StatusNeedsReview: "Needs review",
	knowledge.StatusContested:   "Contested",
	knowledge.StatusProposed:    "Proposed",
	knowledge.StatusRejected:    "Rejected",
}

func statusLabel(s knowledge.Status) string { return statusLabels[s] }

var viewTitles = map[string]string{
	statusPending:                       "Awaiting decision",
	statusAll:                           "All knowledge",
	string(knowledge.StatusValidated):   "Validated",
	string(knowledge.StatusRejected):    "Rejected",
	string(knowledge.StatusContested):   "Contested",
	string(knowledge.StatusNeedsReview): "Needs review",
	string(knowledge.StatusProposed):    "Proposed",
	"":                                  "Awaiting decision",
}

func viewTitle(status string) string { return viewTitles[status] }

func (m pageModel) listSubtitle() string {
	var parts []string
	switch m.Filters.Type {
	case string(knowledge.TypePersona):
		parts = append(parts, "Personas")
	case string(knowledge.TypeRequirement):
		parts = append(parts, "Requirements")
	}
	return strings.Join(append(parts, plural(m.Total, "item")), " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// emptyList returns the title and text of an empty list, and whether to offer
// every status.
func (m pageModel) emptyList() (string, string, bool) {
	switch {
	case m.Empty:
		return "Nothing here yet", "Run an analysis from the CLI to create knowledge.", false
	case m.Filters.Query != "":
		return "No matching knowledge", "Try another search, or include every status.", true
	case m.Filters.Status == statusPending:
		return "Nothing awaits a decision", "Every proposal has a human decision. New runs will add proposals here.", true
	}
	return "Nothing here yet", "Run an analysis from the CLI to create knowledge.", true
}

// selectedID is the item kept by filter links.
func (m pageModel) selectedID() string {
	if m.Item != nil {
		return m.Item.ID
	}
	return ""
}

func typeLabel(t knowledge.Type) string {
	if t == knowledge.TypePersona {
		return "Persona"
	}
	return "Requirement"
}

func typePlural(t knowledge.Type) string { return typeLabel(t) + "s" }

func typeIcon(t knowledge.Type) string {
	if t == knowledge.TypePersona {
		return "user"
	}
	return "scroll-text"
}

func itemMeta(it *knowledge.Item) string {
	meta := fmt.Sprintf("%d interp · %d ev", len(it.Support), len(it.Evidence))
	if n := len(seenRuns(it)); n > 1 {
		meta = plural(n, "run") + " · " + meta
	}
	return meta
}

// seenRuns lists the runs whose analysis backs the item, oldest first.
func seenRuns(it *knowledge.Item) []string {
	var runs []string
	for _, s := range it.Support {
		if !slices.Contains(runs, s.Run) {
			runs = append(runs, s.Run)
		}
	}
	return runs
}

func yamlPath(it *knowledge.Item) string {
	return ".raun/knowledge/" + string(it.Type) + "/" + it.ID + ".yaml"
}

func agentCount(it *knowledge.Item) string {
	var agents []string
	for _, s := range it.Support {
		if !slices.Contains(agents, s.Agent) {
			agents = append(agents, s.Agent)
		}
	}
	return fmt.Sprintf("%d from %s", len(it.Support), plural(len(agents), "agent"))
}

func unresolvedPoints(it *knowledge.Item) int {
	n := 0
	for _, p := range it.OpenPoints {
		if p.Unresolved() {
			n++
		}
	}
	return n
}

// blockingPoints lists the unresolved disagreements, which block validation.
func blockingPoints(it *knowledge.Item) []string {
	var ids []string
	for _, p := range it.OpenPoints {
		if p.Kind == knowledge.PointDisagreement && p.Unresolved() {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func evidenceRange(e knowledge.Evidence) string {
	if e.StartLine == e.EndLine {
		return strconv.Itoa(e.StartLine)
	}
	return strconv.Itoa(e.StartLine) + "–" + strconv.Itoa(e.EndLine)
}

func shortCommit(commit string) string { return commit[:min(7, len(commit))] }

type excerptLine struct {
	Number int
	Text   string
}

func excerptLines(e knowledge.Evidence, limit int) []excerptLine {
	var lines []excerptLine
	for i, text := range strings.Split(e.Excerpt, "\n") {
		if limit > 0 && i == limit {
			break
		}
		if text == "" {
			text = " "
		}
		lines = append(lines, excerptLine{e.StartLine + i, text})
	}
	return lines
}

var evidenceHelp = map[knowledge.EvidenceState]string{
	knowledge.EvidenceVerified:  "The excerpt is at the cited lines.",
	knowledge.EvidenceRelocated: "Found at other lines; Raun corrected them.",
	knowledge.EvidenceStale:     "Held at the cited commit, no longer at a later one.",
	knowledge.EvidenceInvalid:   "Not in the file at the cited commit.",
}

var sourceIcons = map[knowledge.SourceKind]string{
	knowledge.SourceCode:         "file-code",
	knowledge.SourceDoc:          "book-open",
	knowledge.SourceHumanContext: "user-pen",
}

var pointLabels = map[knowledge.PointKind]string{
	knowledge.PointDisagreement: "Disagreement",
	knowledge.PointUncertainty:  "Uncertainty",
	knowledge.PointQuestion:     "Question",
}

var pointIcons = map[knowledge.PointKind]string{
	knowledge.PointDisagreement: "split",
	knowledge.PointUncertainty:  "circle-question-mark",
	knowledge.PointQuestion:     "message-circle-question-mark",
}

func blocks(p knowledge.OpenPoint) bool {
	return p.Kind == knowledge.PointDisagreement && p.Unresolved()
}

func raisedBy(p knowledge.OpenPoint) string {
	if p.RaisedBy == "raun" {
		return "raun (verification)"
	}
	return p.RaisedBy
}

// resolveCommand is the CLI command that settles an open point.
func resolveCommand(it *knowledge.Item, p knowledge.OpenPoint) string {
	cmd := fmt.Sprintf("raun resolve %s %s -note \"...\"", it.ID, p.ID)
	if p.Kind == knowledge.PointDisagreement {
		cmd += " [-position N]"
	}
	return cmd
}

func reopenCommand(it *knowledge.Item) string {
	return fmt.Sprintf("raun reopen %s -reason \"...\"", it.ID)
}

func resolutionHead(r *knowledge.Resolution) string {
	head := "Resolved by " + r.By + " · " + formatTime(r.At)
	if r.Position != nil {
		head += " · Kept position " + strconv.Itoa(*r.Position+1)
	}
	return head
}

// formatTime renders "Oct 7, 2026 · 09:30 UTC".
func formatTime(t time.Time) string {
	return t.UTC().Format("Jan 2, 2006 · 15:04 UTC")
}

func initials(name string) string {
	var b strings.Builder
	for _, word := range strings.Fields(name) {
		r, _ := utf8.DecodeRuneInString(word)
		b.WriteRune(r)
	}
	s := b.String()
	if utf8.RuneCountInString(s) > 2 {
		_, first := utf8.DecodeRuneInString(s)
		_, second := utf8.DecodeRuneInString(s[first:])
		s = s[:first+second]
	}
	return s
}

// lastDecision returns the human event that set the current status.
func lastDecision(it *knowledge.Item) *knowledge.Event {
	for i := len(it.History) - 1; i >= 0; i-- {
		if e := it.History[i]; e.Actor == knowledge.ActorHuman && e.To == it.Status {
			return &e
		}
	}
	return nil
}

func terminalTitle(it *knowledge.Item) string {
	if e := lastDecision(it); e != nil {
		return statusLabel(it.Status) + " by " + e.By + " · " + formatTime(e.At)
	}
	return statusLabel(it.Status)
}

func terminalText(it *knowledge.Item) string {
	if it.Status == knowledge.StatusRejected {
		return "No decision actions for rejected items. Reopen from the CLI to review again."
	}
	return "No decision actions for validated items. A new run marks it Needs review if its evidence changes."
}

func blockedLabel(ids []string) string {
	return "Resolve " + strings.Join(ids, ", ") + " to validate"
}

// runLabel names a run by its start time, "Oct 29 · 12:54".
func runLabel(m *run.Manifest) string { return m.StartedAt.UTC().Format("Jan 2 · 15:04") }

// runRef names a run by its manifest's start time, or by its ID when its
// manifest is missing.
func runRef(index *runIndex, id string) string {
	if m := index.get(id); m != nil {
		return runLabel(m)
	}
	return id
}

func runDuration(m *run.Manifest) string {
	return m.FinishedAt.Sub(m.StartedAt).Round(time.Second).String()
}

// runError is the reason a run failed, without the "run failed" prefix.
func runError(m *run.Manifest) string {
	reason, _, _ := strings.Cut(m.Error, "\n")
	return strings.TrimPrefix(reason, "run failed: ")
}

// runDigest summarizes a run in the sidebar.
func runDigest(m *run.Manifest) string {
	if m.Status != run.StatusSucceeded {
		return "failed: " + runError(m)
	}
	var parts []string
	if n := len(m.Items); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" new")
	}
	if n := len(m.Updated); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" updated")
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, " · ")
}

func runTone(m *run.Manifest) string {
	if m.Status == run.StatusSucceeded {
		return "tone-verified"
	}
	return "tone-invalid"
}

// commitRange shows the commit a run analyzed after the previous one.
func commitRange(previous, m *run.Manifest) string {
	if previous == nil || previous.Commit == m.Commit {
		return shortCommit(m.Commit)
	}
	return shortCommit(previous.Commit) + " → " + shortCommit(m.Commit)
}

// runAgents lists every agent of a run: analysts, then the lead and the
// reconciler.
func runAgents(m *run.Manifest) []run.AgentRecord {
	agents := slices.Clone(m.Agents)
	for _, rec := range []*run.AgentRecord{m.Lead, m.Reconciler} {
		if rec != nil {
			agents = append(agents, *rec)
		}
	}
	return agents
}

func agentTitle(rec run.AgentRecord) string {
	title := rec.Role + " · " + rec.Status
	if rec.ReportRetries > 0 {
		title += " · asked again " + plural(rec.ReportRetries, "time")
	}
	if rec.Salvaged {
		title += " · salvaged after its timeout"
	}
	if rec.Error != "" {
		title += " · " + rec.Error
	}
	return title
}

var changeLabels = map[string]string{
	changeNew:           "New",
	changeUpdated:       "Updated",
	changeStale:         "Stale evidence",
	changeConflict:      "Conflict",
	changeNeedsReview:   "Needs review",
	changeRejectedAgain: "Found again",
	changeSkipped:       "Not updated",
}

var changeTones = map[string]string{
	changeNew:           "new",
	changeUpdated:       "support",
	changeStale:         "stale",
	changeConflict:      "contested",
	changeNeedsReview:   "needs-review",
	changeRejectedAgain: "rejected",
	changeSkipped:       "rejected",
}

type changeBadge struct{ Label, Tone string }

// changeBadges summarizes what the active run did to an item in its row.
func changeBadges(c itemChanges) []changeBadge {
	var badges []changeBadge
	add := func(kind, label string) {
		badges = append(badges, changeBadge{Label: label, Tone: changeTones[kind]})
	}
	switch {
	case c.New:
		add(changeNew, changeLabels[changeNew])
	case c.Updated && len(c.Support) > 0:
		add(changeUpdated, "+"+strconv.Itoa(len(c.Support))+" support")
	case c.Updated && len(c.Stale) == 0:
		add(changeUpdated, changeLabels[changeUpdated])
	}
	for _, kind := range changeKinds[2:] {
		if c.has(kind) {
			add(kind, changeLabels[kind])
		}
	}
	return badges
}

// newestGroupsFirst orders support groups from the latest run.
func newestGroupsFirst(groups []supportGroup) []supportGroup {
	out := slices.Clone(groups)
	slices.Reverse(out)
	return out
}

// evidenceOrder lists stale evidence first, the rest in file order.
func evidenceOrder(it *knowledge.Item) []knowledge.Evidence {
	out := slices.Clone(it.Evidence)
	slices.SortStableFunc(out, func(a, b knowledge.Evidence) int {
		return cmpBool(b.State == knowledge.EvidenceStale, a.State == knowledge.EvidenceStale)
	})
	return out
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// creation returns the event that created the item, if the run recorded it.
func creation(events []knowledge.Event) *knowledge.Event {
	for _, e := range events {
		if e.From == "" {
			return &e
		}
	}
	return nil
}

func positionLabel(r *knowledge.Resolution) string {
	if r.Position == nil {
		return ""
	}
	return "Kept position " + strconv.Itoa(*r.Position+1)
}

// runList returns at most limit runs (0: all), newest first.
func (m pageModel) runList(limit int) []*run.Manifest {
	if m.Runs == nil || m.Counts == nil {
		return nil
	}
	return m.Runs.Index.newestFirst(limit)
}

// listTitle names the list: its status view, or the run it shows.
func (m pageModel) listTitle() string {
	if m.Filters.Run != "" && m.Runs != nil && m.Runs.Active != nil {
		title := "Run of " + runLabel(m.Runs.Active)
		if m.Filters.Change != "" {
			title += " · " + changeLabels[m.Filters.Change]
		}
		return title
	}
	return viewTitle(m.Filters.Status)
}

func agentTone(rec run.AgentRecord) string {
	if rec.Status == run.AgentOK {
		return "verified"
	}
	return "invalid"
}

// runNote states the run-level numbers that no item filter shows.
func runNote(m *run.Manifest) string {
	r := m.Reconciliation
	if r == nil {
		return ""
	}
	note := fmt.Sprintf("%s matched among %s", plural(r.Matched, "result"), plural(r.Candidates, "existing item"))
	if r.StaleEvidence > 0 {
		note += fmt.Sprintf(" · %d evidence went stale", r.StaleEvidence)
	}
	return note
}

// runs returns the workspace's runs, or nil.
func (m pageModel) runs() *runIndex {
	if m.Runs == nil {
		return nil
	}
	return m.Runs.Index
}

// noChanges names the kinds of change the active run did not make, which
// get no filter chip: "No conflict · needs review · …".
func (v *runView) noChanges() string {
	var none []string
	for _, kind := range changeKinds[2:] {
		if v.count(kind) == 0 {
			none = append(none, strings.ToLower(changeLabels[kind]))
		}
	}
	if len(none) == 0 {
		return ""
	}
	return "No " + strings.Join(none, ", ")
}

// maxCitations is how many evidence a change list links before counting.
const maxCitations = 8
