package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/agenttest"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

// teamBase runs the fake team and returns the repository and the IDs of
// the items by title.
func teamBase(t *testing.T) (string, map[string]string) {
	t.Helper()
	// Decisions read git config user.name: keep the user's own config out.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	cfg := agenttest.TeamConfig("10s", agenttest.Argv(agenttest.Lead),
		agenttest.Agent{ID: "alpha", Argv: agenttest.Argv(agenttest.Valid)},
		agenttest.Agent{ID: "beta", Argv: agenttest.Argv(agenttest.Dissent)})
	fx := agenttest.NewTeamProject(t, cfg)
	if code, _, errOut := runCLI(t, "run", "-dir", fx.Dir); code != 0 {
		t.Fatalf("run: %s", errOut)
	}
	items, err := knowledge.NewStore(workspace.KnowledgeDir(fx.Dir)).List()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, it := range items {
		ids[it.Title] = it.ID
	}
	return fx.Dir, ids
}

func get(t *testing.T, dir, id string) *knowledge.Item {
	t.Helper()
	it, err := knowledge.NewStore(workspace.KnowledgeDir(dir)).Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestDecisionWorkflow(t *testing.T) {
	dir, ids := teamBase(t)
	immutable := ids["Issued invoices are immutable"]
	accountant := ids["Accountant"]
	window := ids["Correction window"]

	// Lead input order is anonymized, so locate the code position in the
	// stored disagreement instead of assuming it is always position one.
	codePosition := 0
	for _, point := range get(t, dir, immutable).OpenPoints {
		for i, position := range point.Positions {
			if position.Statement == "An issued invoice cannot be updated." {
				codePosition = i + 1
			}
		}
	}
	if codePosition == 0 {
		t.Fatal("missing code position")
	}

	// review lists every pending item, contested first, with the next step.
	code, out, _ := runCLI(t, "review", "-dir", dir)
	if code != 0 {
		t.Fatal(out)
	}
	for _, want := range []string{
		"3 item(s) await a decision.",
		immutable + "  contested  Issued invoices are immutable",
		"disagreement (raised by lead)",
		fmt.Sprintf("%d. An issued invoice cannot be updated.  (alpha)", codePosition),
		"next: raun resolve " + immutable + " p",
		"next: raun show " + accountant + ", then accept or reject",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, immutable) > strings.Index(out, accountant) {
		t.Error("contested items must come before proposed ones")
	}

	// Decisions are signed.
	code, _, errOut := runCLI(t, "accept", accountant, "-dir", dir, "-reason", "ok")
	if code != 1 || !strings.Contains(errOut, "no author") {
		t.Errorf("unsigned decision: exit = %d, stderr = %q", code, errOut)
	}

	// A contested item cannot be accepted before its disagreement is resolved.
	code, _, errOut = runCLI(t, "accept", immutable, "-dir", dir, "-author", "PO", "-reason", "ok")
	if code != 1 || !strings.Contains(errOut, "resolve the open disagreements first") || !strings.Contains(errOut, "raun resolve "+immutable) {
		t.Errorf("accept contested: exit = %d, stderr = %q", code, errOut)
	}

	point := disagreementID(t, get(t, dir, immutable))
	code, _, errOut = runCLI(t, "resolve", immutable, point, "-dir", dir, "-author", "PO", "-position", "3", "-note", "x")
	if code != 1 || !strings.Contains(errOut, "has 2 positions") {
		t.Errorf("bad position: exit = %d, stderr = %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "resolve", immutable, point, "-dir", dir, "-author", "PO"); code != 1 || !strings.Contains(errOut, "needs a note") {
		t.Errorf("missing note: exit = %d, stderr = %q", code, errOut)
	}
	code, out, errOut = runCLI(t, "resolve", immutable, point, "-dir", dir, "-author", "PO", "-position", strconv.Itoa(codePosition), "-note", "The code is authoritative.")
	if code != 0 || !strings.Contains(out, "contested -> proposed (no disagreement left)") {
		t.Fatalf("resolve: exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}

	code, out, _ = runCLI(t, "accept", immutable, "-dir", dir, "-author", "PO", "-reason", "Confirmed with accounting.")
	if code != 0 || !strings.Contains(out, "proposed -> validated by PO") {
		t.Errorf("accept: exit = %d, stdout = %q", code, out)
	}
	it := get(t, dir, immutable)
	last := it.History[len(it.History)-1]
	if it.Status != knowledge.StatusValidated || last.Actor != knowledge.ActorHuman || last.By != "PO" || last.Reason != "Confirmed with accounting." {
		t.Errorf("status = %s, last event = %+v", it.Status, last)
	}

	// The author defaults to git config user.name.
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[user]\n\tname = Ada\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "reject", window, "-dir", dir); code != 1 || !strings.Contains(errOut, "needs a reason") {
		t.Errorf("reject without reason: exit = %d, stderr = %q", code, errOut)
	}
	code, out, _ = runCLI(t, "reject", window, "-dir", dir, "-reason", "No such window exists.")
	if code != 0 || !strings.Contains(out, "proposed -> rejected by Ada") {
		t.Errorf("reject: exit = %d, stdout = %q", code, out)
	}
	code, out, _ = runCLI(t, "reopen", window, "-dir", dir, "-reason", "Support confirmed a manual process.")
	if code != 0 || !strings.Contains(out, "rejected -> proposed by Ada") {
		t.Errorf("reopen: exit = %d, stdout = %q", code, out)
	}
	if code, _, errOut := runCLI(t, "reopen", accountant, "-dir", dir, "-reason", "x"); code != 1 || !strings.Contains(errOut, "proposed -> proposed") {
		t.Errorf("reopen a proposed item: exit = %d, stderr = %q", code, errOut)
	}

	code, out, _ = runCLI(t, "review", "-dir", dir)
	if code != 0 || !strings.Contains(out, "2 item(s) await a decision.") || strings.Contains(out, immutable) {
		t.Errorf("review after decisions:\n%s", out)
	}
	if code, _, errOut := runCLI(t, "check", "-dir", dir); code != 0 {
		t.Errorf("the base must stay valid: %s", errOut)
	}
}

func TestReportCommand(t *testing.T) {
	dir, ids := teamBase(t)
	window := ids["Correction window"]
	if code, _, errOut := runCLI(t, "reject", window, "-dir", dir, "-author", "PO", "-reason", "Not a rule."); code != 0 {
		t.Fatal(errOut)
	}

	code, out, _ := runCLI(t, "report", "-dir", dir)
	if code != 0 || !strings.Contains(out, "2 item(s): 1 contested, 1 proposed.") || strings.Contains(out, "Correction window") {
		t.Errorf("default report:\n%s", out)
	}
	_, out, _ = runCLI(t, "report", "-dir", dir, "-all")
	if !strings.Contains(out, "Correction window") {
		t.Errorf("-all must include rejected items:\n%s", out)
	}
	_, out, _ = runCLI(t, "report", "-dir", dir, "-status", "contested")
	if !strings.Contains(out, "1 item(s): 1 contested.") {
		t.Errorf("-status contested:\n%s", out)
	}

	file := filepath.Join(t.TempDir(), "knowledge.md")
	code, out, _ = runCLI(t, "report", "-dir", dir, "-o", file)
	data, err := os.ReadFile(file)
	if code != 0 || err != nil || !strings.HasPrefix(string(data), "# Knowledge report") || !strings.Contains(out, "Wrote ") {
		t.Errorf("-o: exit = %d, stdout = %q, err = %v", code, out, err)
	}
	if code, _, errOut := runCLI(t, "report", "-dir", dir, "-status", "done"); code != 2 || !strings.Contains(errOut, `unknown status "done"`) {
		t.Errorf("bad status: exit = %d, stderr = %q", code, errOut)
	}
}

func disagreementID(t *testing.T, it *knowledge.Item) string {
	t.Helper()
	for _, p := range it.OpenPoints {
		if p.Kind == knowledge.PointDisagreement {
			return p.ID
		}
	}
	t.Fatalf("%s has no disagreement", it.ID)
	return ""
}

func TestDecisionCommandsRespectItemLock(t *testing.T) {
	dir, ids := teamBase(t)
	id := ids["Issued invoices are immutable"]
	it := get(t, dir, id)
	store := knowledge.NewStore(workspace.KnowledgeDir(dir))
	p := store.Path(it.Type, id)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"accept", "reject", "reopen", "resolve"} {
		args := []string{verb, id, "-dir", dir, "-author", "Reviewer"}
		if verb == "resolve" {
			args = append(args, disagreementID(t, it), "-note", "Confirmed")
		} else {
			args = append(args, "-reason", "Confirmed")
		}
		code, _, errOut := runCLI(t, args...)
		if code != 1 || !strings.Contains(errOut, "knowledge update conflict") {
			t.Errorf("%s: code=%d stderr=%s", verb, code, errOut)
		}
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("busy item changed")
	}
	if _, err := os.Stat(p + ".lock"); err != nil {
		t.Fatal("CLI removed another process's lock")
	}
}
