package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/kvitrvn/raun/internal/config"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

// cmdVerify re-checks every evidence of the knowledge base. It writes
// nothing: evidence states in files only change through runs.
func cmdVerify(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("verify", stderr)
	dir := fs.String("dir", ".", "repository root")
	at := fs.String("at", "HEAD", "commit to check freshness against")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	ctx := context.Background()

	cfg, err := config.Load(workspace.ConfigPath(*dir))
	if err != nil {
		return err
	}
	repo, err := gitx.Open(ctx, *dir)
	if err != nil {
		return err
	}
	target, err := repo.ResolveCommit(ctx, *at)
	if err != nil {
		return err
	}
	items, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).List()
	if err != nil {
		return err
	}

	v := evidence.NewVerifier(repo, cfg.Evidence.MaxLines)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ITEM\tEVIDENCE\tCITED\tAT CITED COMMIT\tAT %s\tDETAIL\n", shortCommit(target))

	var total, relocated, stale, invalid, unavailable int
	for _, it := range items {
		for _, ev := range it.Evidence {
			total++
			cited := fmt.Sprintf("%s:%d-%d", ev.Path, ev.StartLine, ev.EndLine)
			row := func(integrity, fresh, detail string) {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, ev.ID, cited, integrity, fresh, detail)
			}

			integ, err := v.Verify(ctx, ev, ev.Commit)
			if errors.Is(err, gitx.ErrUnknownCommit) {
				unavailable++
				row("unavailable", "-", "commit "+shortCommit(ev.Commit)+" is not in this repository")
				continue
			}
			if err != nil {
				return fmt.Errorf("%s %s: %w", it.ID, ev.ID, err)
			}
			if integ.State == knowledge.EvidenceInvalid {
				invalid++
				row(describe(integ), "-", integ.Reason)
				continue
			}

			fresh := integ
			if target != ev.Commit {
				if fresh, err = v.Verify(ctx, ev, target); err != nil {
					return fmt.Errorf("%s %s: %w", it.ID, ev.ID, err)
				}
			}
			switch {
			case fresh.State == knowledge.EvidenceStale:
				stale++
			case integ.State == knowledge.EvidenceRelocated || fresh.State == knowledge.EvidenceRelocated:
				relocated++
			}
			detail := fresh.Reason
			if detail == "" {
				detail = integ.Reason
			}
			row(describe(integ), describe(fresh), detail)
		}
	}

	if total == 0 {
		fmt.Fprintln(stdout, "No evidence to verify.")
		return nil
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	ok := total - relocated - stale - invalid - unavailable
	fmt.Fprintf(stdout, "\n%d evidence checked against %s: %d verified, %d relocated, %d stale, %d invalid, %d unavailable\n",
		total, shortCommit(target), ok, relocated, stale, invalid, unavailable)

	if problems := stale + invalid + unavailable; problems > 0 {
		return fmt.Errorf("verify: %d evidence need attention", problems)
	}
	return nil
}

func describe(r evidence.Result) string {
	if r.State == knowledge.EvidenceRelocated {
		return fmt.Sprintf("relocated %d-%d", r.StartLine, r.EndLine)
	}
	return string(r.State)
}
