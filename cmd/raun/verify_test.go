package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

const invoiceGo = "package billing\n\nfunc Update(inv *Invoice) error {\n\tif inv.Issued {\n\t\treturn ErrLocked\n\t}\n\treturn nil\n}\n"

func evidenceAt(id, commit, path string, start, end int, excerpt string) knowledge.Evidence {
	return knowledge.Evidence{
		ID: id, Source: knowledge.SourceCode, Path: path, Commit: commit,
		StartLine: start, EndLine: end, Excerpt: excerpt,
		SHA256: knowledge.HashExcerpt(excerpt), State: knowledge.EvidenceVerified,
	}
}

func saveRequirement(t *testing.T, dir, id string, evs ...knowledge.Evidence) {
	t.Helper()
	it := &knowledge.Item{
		Version: knowledge.FormatVersion,
		ID:      id,
		Type:    knowledge.TypeRequirement,
		Title:   "Issued invoices are locked",
		Status:  knowledge.StatusProposed,
		Requirement: &knowledge.Requirement{
			Statement: "An issued invoice cannot be updated.",
			Kind:      knowledge.KindBusinessRule,
		},
		Evidence: evs,
		History: []knowledge.Event{{
			At: time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC), Actor: knowledge.ActorRun, By: "run-test", To: knowledge.StatusProposed,
		}},
	}
	if err := knowledge.NewStore(workspace.KnowledgeDir(dir)).Save(it); err != nil {
		t.Fatal(err)
	}
}

func TestVerify(t *testing.T) {
	fx := gittest.New(t)
	fx.Write("billing/invoice.go", invoiceGo)
	fx.Write("docs/billing.md", "Issued invoices are locked.\n")
	c1 := fx.Commit("one")
	fx.Write("billing/invoice.go", "// Package billing.\n"+invoiceGo)
	fx.Remove("docs/billing.md")
	fx.Commit("two")
	if code, _, errOut := runCLI(t, "init", "-dir", fx.Dir); code != 0 {
		t.Fatal(errOut)
	}

	guard := "if inv.Issued {\n\treturn ErrLocked\n}"
	saveRequirement(t, fx.Dir, "requirement-aaaaaa",
		evidenceAt("e1", c1, "billing/invoice.go", 4, 6, guard),
		evidenceAt("e2", c1, "docs/billing.md", 1, 1, "Issued invoices are locked."),
		evidenceAt("e3", c1, "billing/invoice.go", 4, 4, "if inv.Paid {"),
		evidenceAt("e4", strings.Repeat("ab", 20), "billing/invoice.go", 4, 6, guard),
	)

	code, out, errOut := runCLI(t, "verify", "-dir", fx.Dir)
	if code != 1 || !strings.Contains(errOut, "verify: 3 evidence need attention") {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{
		"requirement-aaaaaa  e1        billing/invoice.go:4-6  verified",
		"relocated 5-7",
		"e2        docs/billing.md:1-1     verified         stale",
		"does not exist at this commit",
		"e3        billing/invoice.go:4-4  invalid",
		"excerpt not found in billing/invoice.go",
		"e4        billing/invoice.go:4-6  unavailable",
		"4 evidence checked against",
		"0 verified, 1 relocated, 1 stale, 1 invalid, 1 unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// At the cited commit, the healthy evidence verify cleanly.
	saveRequirement(t, fx.Dir, "requirement-aaaaaa",
		evidenceAt("e1", c1, "billing/invoice.go", 4, 6, guard),
		evidenceAt("e2", c1, "docs/billing.md", 1, 1, "Issued invoices are locked."),
	)
	code, out, errOut = runCLI(t, "verify", "-dir", fx.Dir, "-at", c1)
	if code != 0 || !strings.Contains(out, "2 verified, 0 relocated, 0 stale, 0 invalid, 0 unavailable") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestVerifyErrors(t *testing.T) {
	fx := gittest.New(t)
	fx.Commit("empty")

	// No configuration yet.
	if code, _, errOut := runCLI(t, "verify", "-dir", fx.Dir); code != 1 || !strings.Contains(errOut, "read config") {
		t.Errorf("no config: exit = %d, stderr = %q", code, errOut)
	}

	if code, _, errOut := runCLI(t, "init", "-dir", fx.Dir); code != 0 {
		t.Fatal(errOut)
	}
	if code, out, _ := runCLI(t, "verify", "-dir", fx.Dir); code != 0 || !strings.Contains(out, "No evidence to verify.") {
		t.Errorf("empty base: exit = %d, stdout = %q", code, out)
	}
	if code, _, errOut := runCLI(t, "verify", "-dir", fx.Dir, "-at", "nope"); code != 1 || !strings.Contains(errOut, "unknown commit") {
		t.Errorf("bad -at: exit = %d, stderr = %q", code, errOut)
	}

	plain := t.TempDir()
	runCLI(t, "init", "-dir", plain)
	if code, _, errOut := runCLI(t, "verify", "-dir", plain); code != 1 || !strings.Contains(errOut, "not inside a Git repository") {
		t.Errorf("not a repo: exit = %d, stderr = %q", code, errOut)
	}
}
