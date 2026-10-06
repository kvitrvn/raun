package main

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/report"
	"github.com/kvitrvn/raun/internal/workspace"
)

func cmdReport(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("report", stderr)
	dir := fs.String("dir", ".", "repository root")
	out := fs.String("o", "", "write to this file instead of stdout")
	status := fs.String("status", "", "only this status")
	all := fs.Bool("all", false, "include rejected items")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	statuses := slices.DeleteFunc(slices.Clone(report.Order), func(s knowledge.Status) bool {
		return s == knowledge.StatusRejected && !*all
	})
	if *status != "" {
		s := knowledge.Status(*status)
		if !slices.Contains(knowledge.Statuses, s) {
			return fmt.Errorf("%w: unknown status %q", errUsage, *status)
		}
		statuses = []knowledge.Status{s}
	}

	items, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).List()
	if err != nil {
		return err
	}
	if *out == "" {
		return report.Render(stdout, items, statuses)
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := report.Render(f, items, statuses); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s\n", *out)
	return nil
}
