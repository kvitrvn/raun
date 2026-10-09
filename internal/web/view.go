package web

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kvitrvn/raun/internal/knowledge"
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
	return fmt.Sprintf("%d interp · %d ev", len(it.Support), len(it.Evidence))
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

func newestFirst(events []knowledge.Event) []knowledge.Event {
	out := slices.Clone(events)
	slices.Reverse(out)
	return out
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
