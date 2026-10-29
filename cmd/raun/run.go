package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

	// Ctrl+C or SIGTERM stops the agents, which run in their own process
	// groups and do not get the terminal's signal. A second one exits at
	// once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)

	m, err := raunrun.Execute(ctx, raunrun.Options{
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
