// Command raun builds an evidence-backed knowledge base of a software project.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"

	"github.com/kvitrvn/raun/internal/config"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

const usage = `Usage: raun <command> [flags]

Commands:
  init      Create the .raun/ directory with a starter configuration
  check     Validate .raun/config.yaml and the knowledge files
  list      List knowledge items (-type, -status to filter)
  show      Show one knowledge item with its support, evidence and history
  version   Print the raun version
  help      Show this help

Planned (not implemented yet): run, verify, review, accept, reject,
resolve, report, diff. See docs/design.md.
`

var planned = []string{"run", "verify", "review", "accept", "reject", "resolve", "report", "diff"}

// errUsage marks errors caused by invalid invocation (exit code 2).
var errUsage = errors.New("usage error")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	err := dispatch(args, stdout, stderr)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "raun: %v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintf(stderr, "raun: %v\n", err)
		return 1
	}
}

func dispatch(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: missing command", errUsage)
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return cmdInit(rest, stdout, stderr)
	case "check":
		return cmdCheck(rest, stdout, stderr)
	case "list":
		return cmdList(rest, stdout, stderr)
	case "show":
		return cmdShow(rest, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "raun", buildVersion())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	if slices.Contains(planned, cmd) {
		return fmt.Errorf("%s: not implemented yet (see docs/design.md)", cmd)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, cmd)
}

func cmdInit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("init", stderr)
	dir := fs.String("dir", ".", "repository root")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	res, err := workspace.Init(*dir)
	if err != nil {
		return err
	}
	for _, p := range res.Created {
		fmt.Fprintln(stdout, "created", p)
	}
	for _, p := range res.Skipped {
		fmt.Fprintln(stdout, "exists, left unchanged:", p)
	}
	if len(res.Created) > 0 {
		fmt.Fprintf(stdout, "\nNext: edit %s (agent commands) and %s/context.md, then run \"raun check\".\n",
			workspace.ConfigPath(*dir), workspace.Dir)
	}
	return nil
}

func cmdCheck(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("check", stderr)
	dir := fs.String("dir", ".", "repository root")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	p := workspace.ConfigPath(*dir)
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s is valid: %d agent(s), quorum %d, types %v\n",
		p, len(cfg.Analysis.Agents), cfg.Analysis.Quorum, cfg.Types)

	items, err := knowledge.NewStore(workspace.KnowledgeDir(*dir)).List()
	if err != nil {
		return err
	}
	if errs := knowledge.CheckReferences(items); len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(stdout, "%s is valid: %d item(s)\n", workspace.KnowledgeDir(*dir), len(items))
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("raun "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseFlags parses args for a command that takes no positional argument.
func parseFlags(fs *flag.FlagSet, args []string) error {
	_, err := parseFlagsWithArgs(fs, args)
	return err
}

// parseFlagsWithArgs parses args for a command taking exactly the named
// positional arguments. Flags may appear before or after them.
func parseFlagsWithArgs(fs *flag.FlagSet, args []string, names ...string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) > len(names) {
		return nil, fmt.Errorf("%w: %s: unexpected argument %q", errUsage, fs.Name(), pos[len(names)])
	}
	if len(pos) < len(names) {
		return nil, fmt.Errorf("%w: %s: missing argument %s", errUsage, fs.Name(), names[len(pos)])
	}
	return pos, nil
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
