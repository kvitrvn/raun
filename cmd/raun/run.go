package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	raunrun "github.com/kvitrvn/raun/internal/run"
	"github.com/kvitrvn/raun/internal/workspace"
)

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("run", stderr)
	dir := fs.String("dir", ".", "repository root")
	commit := fs.String("commit", "", "commit to analyze (default: HEAD, which requires a clean working tree)")
	agentID := fs.String("agent", "", "agent to run, when several are configured")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	m, err := raunrun.Execute(context.Background(), raunrun.Options{
		Dir:         *dir,
		Commit:      *commit,
		Agent:       *agentID,
		RaunVersion: buildVersion(),
		Progress:    stderr,
	})
	if m != nil {
		manifest := filepath.Join(workspace.RunsDir(*dir), m.ID, "manifest.yaml")
		defer fmt.Fprintf(stdout, "Manifest: %s\n", manifest)
	}
	if err != nil {
		return err
	}

	next := "raun list -status proposed"
	if r := m.Reconciliation; r != nil {
		fmt.Fprintf(stdout, "Run %s: %d knowledge item(s) proposed, %d updated (%d need review).\n", m.ID, len(m.Items), len(m.Updated), len(r.NeedsReview))
		if len(r.NeedsReview) > 0 || len(r.Skipped) > 0 {
			next = "raun review"
		}
	} else {
		fmt.Fprintf(stdout, "Run %s: %d knowledge item(s) proposed.\n", m.ID, len(m.Items))
	}
	fmt.Fprintln(stdout, "Review them with: "+next)
	return nil
}
