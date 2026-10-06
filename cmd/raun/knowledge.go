package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("list", stderr)
	dir := fs.String("dir", ".", "repository root")
	typ := fs.String("type", "", "only this knowledge type")
	status := fs.String("status", "", "only this status")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *typ != "" && !slices.Contains(knowledge.Types, knowledge.Type(*typ)) {
		return fmt.Errorf("%w: unknown type %q", errUsage, *typ)
	}
	if *status != "" && !slices.Contains(knowledge.Statuses, knowledge.Status(*status)) {
		return fmt.Errorf("%w: unknown status %q", errUsage, *status)
	}

	items, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).List()
	if err != nil {
		return err
	}
	for _, err := range knowledge.CheckReferences(items) {
		fmt.Fprintln(stderr, "warning:", err)
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tAGENTS\tEVIDENCE\tOPEN\tTITLE")
	n := 0
	for _, it := range items {
		if (*typ != "" && it.Type != knowledge.Type(*typ)) || (*status != "" && it.Status != knowledge.Status(*status)) {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
			it.ID, it.Status, len(supportingAgents(it)), len(it.Evidence), unresolved(it), it.Title)
		n++
	}
	if n == 0 {
		fmt.Fprintln(stdout, "No knowledge items.")
		return nil
	}
	return tw.Flush()
}

func cmdShow(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("show", stderr)
	dir := fs.String("dir", ".", "repository root")
	pos, err := parseFlagsWithArgs(fs, args, "<id>")
	if err != nil {
		return err
	}

	it, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).Get(pos[0])
	if err != nil {
		return err
	}
	renderItem(stdout, it)
	return nil
}

// supportingAgents returns the distinct agent IDs backing an item.
func supportingAgents(it *knowledge.Item) []string {
	var agents []string
	for _, s := range it.Support {
		if !slices.Contains(agents, s.Agent) {
			agents = append(agents, s.Agent)
		}
	}
	return agents
}

func unresolved(it *knowledge.Item) int {
	n := 0
	for _, p := range it.OpenPoints {
		if p.Unresolved() {
			n++
		}
	}
	return n
}

// renderItem prints an item for humans. It is a view: the YAML file remains
// the source of truth.
func renderItem(w io.Writer, it *knowledge.Item) {
	fmt.Fprintf(w, "%s  %s\n", it.ID, it.Title)
	fmt.Fprintf(w, "Type:    %s\nStatus:  %s\n", it.Type, it.Status)

	if p := it.Persona; p != nil {
		fmt.Fprintf(w, "\nDescription: %s\n", p.Description)
		renderList(w, "Goals", p.Goals)
		renderList(w, "Capabilities", p.Capabilities)
	}
	if r := it.Requirement; r != nil {
		fmt.Fprintf(w, "\nStatement: %s\nKind:      %s\n", r.Statement, r.Kind)
		if r.Rationale != "" {
			fmt.Fprintf(w, "Rationale: %s\n", r.Rationale)
		}
		if len(r.Personas) > 0 {
			fmt.Fprintf(w, "Personas:  %s\n", strings.Join(r.Personas, ", "))
		}
	}

	if len(it.Support) > 0 {
		fmt.Fprintf(w, "\nSupport (%d interpretations, %d agents):\n", len(it.Support), len(supportingAgents(it)))
		for _, s := range it.Support {
			fmt.Fprintf(w, "  [%s] %s  (agent %s, run %s, %s)\n", s.Nature, s.Statement, s.Agent, s.Run, s.Interpretation)
			for _, o := range s.Observations {
				fmt.Fprintf(w, "    - %s  [%s]\n", o.Statement, strings.Join(o.Evidence, ", "))
			}
		}
	}

	if len(it.Evidence) > 0 {
		fmt.Fprintf(w, "\nEvidence:\n")
		for _, e := range it.Evidence {
			fmt.Fprintf(w, "  %s  %s:%d-%d @ %s  (%s, %s)\n", e.ID, e.Path, e.StartLine, e.EndLine, shortCommit(e.Commit), e.Source, e.State)
			for line := range strings.SplitSeq(e.Excerpt, "\n") {
				fmt.Fprintf(w, "      | %s\n", line)
			}
		}
	}

	if len(it.OpenPoints) > 0 {
		fmt.Fprintf(w, "\nOpen points:\n")
		for _, p := range it.OpenPoints {
			by := ""
			if p.RaisedBy != "" {
				by = " (raised by " + p.RaisedBy + ")"
			}
			fmt.Fprintf(w, "  %s  %s%s: %s\n", p.ID, p.Kind, by, p.Summary)
			for i, pos := range p.Positions {
				ev := ""
				if len(pos.Evidence) > 0 {
					ev = "  [" + strings.Join(pos.Evidence, ", ") + "]"
				}
				fmt.Fprintf(w, "      %d. %s  (%s)%s\n", i+1, pos.Statement, strings.Join(pos.Agents, ", "), ev)
			}
			if r := p.Resolution; r != nil {
				chosen := ""
				if r.Position != nil {
					chosen = fmt.Sprintf(", kept position %d", *r.Position+1)
				}
				fmt.Fprintf(w, "      resolved by %s on %s%s: %s\n", r.By, r.At.Format("2006-01-02"), chosen, r.Note)
			} else {
				fmt.Fprintf(w, "      unresolved\n")
			}
		}
	}

	fmt.Fprintf(w, "\nHistory:\n")
	for _, e := range it.History {
		from := ""
		if e.From != "" {
			from = string(e.From) + " -> "
		}
		reason := ""
		if e.Reason != "" {
			reason = fmt.Sprintf("  %q", e.Reason)
		}
		fmt.Fprintf(w, "  %s  %s %s: %s%s%s\n", e.At.Format("2006-01-02 15:04"), e.Actor, e.By, from, e.To, reason)
	}
}

func renderList(w io.Writer, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, s := range items {
		fmt.Fprintf(w, "  - %s\n", s)
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
