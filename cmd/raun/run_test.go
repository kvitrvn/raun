package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/agenttest"
)

func TestMain(m *testing.M) {
	agenttest.MaybeRun()
	os.Exit(m.Run())
}

func TestRunCommand(t *testing.T) {
	fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Valid))

	code, out, errOut := runCLI(t, "run", "-dir", fx.Dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	for _, want := range []string{"knowledge item(s) proposed.", "Manifest: ", "raun list -status proposed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	for _, want := range []string{"alpha: analyzing", "evidence 2 verified, 1 relocated, 2 invalid"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("progress lacks %q:\n%s", want, errOut)
		}
	}

	code, out, _ = runCLI(t, "list", "-dir", fx.Dir)
	if code != 0 || strings.Count(out, "proposed") != 3 {
		t.Errorf("list after run:\n%s", out)
	}
	if code, _, errOut := runCLI(t, "verify", "-dir", fx.Dir); code != 0 {
		t.Errorf("verify after run: exit = %d\n%s", code, errOut)
	}
}

func TestRunCommandFailure(t *testing.T) {
	fx := agenttest.NewProject(t, agenttest.Argv(agenttest.Garbage))
	code, out, errOut := runCLI(t, "run", "-dir", fx.Dir)
	if code != 1 || !strings.Contains(errOut, "quorum not met") || !strings.Contains(out, "Manifest: ") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestRunCommandTeam(t *testing.T) {
	cfg := agenttest.TeamConfig("10s", agenttest.Argv(agenttest.Lead),
		agenttest.Agent{ID: "alpha", Argv: agenttest.Argv(agenttest.Valid)},
		agenttest.Agent{ID: "beta", Argv: agenttest.Argv(agenttest.Dissent)})
	fx := agenttest.NewTeamProject(t, cfg)

	code, _, errOut := runCLI(t, "run", "-dir", fx.Dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	for _, want := range []string{"with 2 agent(s)", "lead: consolidating 2 reports", "3 knowledge item(s) proposed, 2 merged from several agents, 1 contested"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("progress lacks %q:\n%s", want, errOut)
		}
	}

	_, out, _ := runCLI(t, "list", "-dir", fx.Dir, "-status", "contested")
	fields := strings.Fields(strings.Split(out, "\n")[1])
	if len(fields) == 0 {
		t.Fatalf("no contested item:\n%s", out)
	}
	_, out, _ = runCLI(t, "show", fields[0], "-dir", fx.Dir)
	for _, want := range []string{"disagreement (raised by lead)", "(alpha)", "(beta)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
}
