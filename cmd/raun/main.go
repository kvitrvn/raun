// Command raun is the single binary of Raun. It runs either the server or a
// runner; see docs/architecture.md.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// Exit codes of run.
const (
	exitOK             = 0
	exitFailure        = 1
	exitUsage          = 2
	exitNotImplemented = exitFailure
)

const usage = `Raun builds an evidence-backed knowledge base of a software project.

Usage:
  raun <command>

Commands:
  server    Run the Raun server (not implemented yet)
  runner    Run a Raun runner (not implemented yet)
  version   Print the version
  help      Print this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "version":
		fmt.Fprintln(stdout, versionLine(readBuildInfo()))
		return exitOK
	case "server", "runner":
		fmt.Fprintf(stderr, "raun %s: not implemented yet\n", args[0])
		return exitNotImplemented
	default:
		fmt.Fprintf(stderr, "raun: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// buildInfo is the subset of the build information that raun reports.
type buildInfo struct {
	version  string
	revision string
	time     string
	modified bool
}

func readBuildInfo() buildInfo {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildInfo{}
	}
	bi := buildInfo{version: info.Main.Version}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			bi.revision = setting.Value
		case "vcs.time":
			bi.time = setting.Value
		case "vcs.modified":
			bi.modified = setting.Value == "true"
		}
	}
	return bi
}

// versionLine formats the build information. A module version, tagged or a
// pseudo-version stamped from Git, already identifies the revision, and is what
// every build from a Git checkout reports. The "dev" form, with the revision
// appended when known, is for builds without a module version, such as
// `go build -buildvcs=false` or a build outside a repository.
func versionLine(bi buildInfo) string {
	if bi.version != "" && bi.version != "(devel)" {
		return "raun " + bi.version
	}
	line := "raun dev"
	if bi.revision == "" {
		return line
	}
	revision := bi.revision
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if bi.modified {
		revision += "-dirty"
	}
	if bi.time != "" {
		return fmt.Sprintf("%s (%s, %s)", line, revision, bi.time)
	}
	return fmt.Sprintf("%s (%s)", line, revision)
}
