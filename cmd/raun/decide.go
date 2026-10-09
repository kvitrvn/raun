package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

// decisionFlags are shared by the commands through which a human decides.
type decisionFlags struct {
	dir, author *string
}

func newDecisionFlags(name string, stderr io.Writer) (*flag.FlagSet, decisionFlags) {
	fs := newFlagSet(name, stderr)
	return fs, decisionFlags{
		dir:    fs.String("dir", ".", "repository root"),
		author: fs.String("author", "", "who decides (default: git config user.name)"),
	}
}

// resolveAuthor returns the decision's author. Decisions are signed: an
// anonymous decision is refused.
func resolveAuthor(ctx context.Context, f decisionFlags) (string, error) {
	if a := strings.TrimSpace(*f.author); a != "" {
		return a, nil
	}
	repo, err := gitx.Open(ctx, *f.dir)
	if err != nil {
		return "", err
	}
	name, err := repo.ConfigValue(ctx, "user.name")
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("no author: set git config user.name or pass -author")
	}
	return name, nil
}

// transitionCommand builds accept, reject and reopen.
func transitionCommand(name string, to knowledge.Status) func([]string, io.Writer, io.Writer) error {
	return func(args []string, stdout, stderr io.Writer) error {
		fs, f := newDecisionFlags(name, stderr)
		reason := fs.String("reason", "", "why (required)")
		pos, err := parseFlagsWithArgs(fs, args, "<id>")
		if err != nil {
			return err
		}
		ctx := context.Background()
		author, err := resolveAuthor(ctx, f)
		if err != nil {
			return err
		}
		store := knowledge.NewStore(workspace.KnowledgeDir(*f.dir))
		it, err := store.Get(pos[0])
		if err != nil {
			return err
		}

		revision, err := knowledge.Revision(it)
		if err != nil {
			return err
		}
		from := it.Status
		err = store.Update(it.ID, revision, func(current *knowledge.Item) error {
			if err := current.Transition(to, knowledge.Actor{Kind: knowledge.ActorHuman, Name: author}, time.Now(), *reason); err != nil {
				if current.HasUnresolvedDisagreement() && to == knowledge.StatusValidated {
					return fmt.Errorf("%w (see raun review, then raun resolve %s <point> -note ...)", err, current.ID)
				}
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: %s -> %s by %s\n", it.ID, from, to, author)
		return nil
	}
}

var (
	cmdAccept = transitionCommand("accept", knowledge.StatusValidated)
	cmdReject = transitionCommand("reject", knowledge.StatusRejected)
	cmdReopen = transitionCommand("reopen", knowledge.StatusProposed)
)

func cmdResolve(args []string, stdout, stderr io.Writer) error {
	fs, f := newDecisionFlags("resolve", stderr)
	note := fs.String("note", "", "how the point was settled (required)")
	position := fs.Int("position", 0, "for a disagreement: number of the retained position, as shown by raun show")
	pos, err := parseFlagsWithArgs(fs, args, "<id>", "<point>")
	if err != nil {
		return err
	}
	if *position < 0 {
		return fmt.Errorf("%w: -position must be a position number", errUsage)
	}
	ctx := context.Background()
	author, err := resolveAuthor(ctx, f)
	if err != nil {
		return err
	}
	store := knowledge.NewStore(workspace.KnowledgeDir(*f.dir))
	it, err := store.Get(pos[0])
	if err != nil {
		return err
	}

	var retained *int
	if *position > 0 {
		p := *position - 1
		retained = &p
	}
	revision, err := knowledge.Revision(it)
	if err != nil {
		return err
	}
	from := it.Status
	err = store.Update(it.ID, revision, func(current *knowledge.Item) error {
		if err := current.Resolve(pos[1], author, time.Now(), retained, *note); err != nil {
			return err
		}
		it = current
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s %s: resolved by %s\n", it.ID, pos[1], author)
	if it.Status != from {
		fmt.Fprintf(stdout, "%s: %s -> %s (no disagreement left)\n", it.ID, from, it.Status)
	}
	return nil
}

// awaiting lists the statuses that wait for a human decision, in the
// order review shows them.
var awaiting = []knowledge.Status{knowledge.StatusNeedsReview, knowledge.StatusContested, knowledge.StatusProposed}

func cmdReview(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("review", stderr)
	dir := fs.String("dir", ".", "repository root")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	items, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).List()
	if err != nil {
		return err
	}

	var pending []*knowledge.Item
	for _, status := range awaiting {
		for _, it := range items {
			if it.Status == status {
				pending = append(pending, it)
			}
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(stdout, "Nothing awaits a decision.")
		return nil
	}

	fmt.Fprintf(stdout, "%d item(s) await a decision.\n", len(pending))
	for _, it := range pending {
		fmt.Fprintf(stdout, "\n%s  %s  %s\n", it.ID, it.Status, it.Title)
		var disagreements []string
		for _, p := range it.OpenPoints {
			if !p.Unresolved() {
				continue
			}
			by := ""
			if p.RaisedBy != "" {
				by = " (raised by " + p.RaisedBy + ")"
			}
			fmt.Fprintf(stdout, "  %s  %s%s: %s\n", p.ID, p.Kind, by, p.Summary)
			if p.Kind == knowledge.PointDisagreement {
				disagreements = append(disagreements, p.ID)
				for i, pos := range p.Positions {
					fmt.Fprintf(stdout, "      %d. %s  (%s)\n", i+1, pos.Statement, strings.Join(pos.Agents, ", "))
				}
			}
		}
		fmt.Fprintf(stdout, "  next: %s\n", nextStep(it, disagreements))
	}
	return nil
}

func nextStep(it *knowledge.Item, disagreements []string) string {
	if len(disagreements) > 0 {
		return fmt.Sprintf("raun resolve %s %s -note \"...\" [-position N], then accept or reject", it.ID, disagreements[0])
	}
	verb := "accept"
	if it.Status == knowledge.StatusNeedsReview {
		verb = "accept again"
	}
	return fmt.Sprintf("raun show %s, then %s or reject (-reason \"...\")", it.ID, verb)
}
