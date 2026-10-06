// Package agenttest provides a fake analyst agent and a matching fixture
// project, so runs can be tested end to end without any LLM or network.
//
// The fake agent is the test binary itself: a package's TestMain calls
// MaybeRun, and configurations use Argv(scenario) as the agent command.
package agenttest

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/gittest"
)

const marker = "raun-fake-agent"

// Scenarios understood by the fake agent.
const (
	// Valid prints Report after checking its isolation.
	Valid = "valid"
	// Chatty prints logs and a Markdown fence around Report.
	Chatty = "chatty"
	// Tamper modifies a file of its copy, then behaves like Valid.
	Tamper = "tamper"
	// Garbage prints no JSON.
	Garbage = "garbage"
	// BadContract prints JSON that breaks the report contract.
	BadContract = "bad-contract"
	// Crash exits with code 2.
	Crash = "crash"
	// Hang sleeps longer than any test timeout.
	Hang = "hang"
	// Dissent prints DissentReport: it shares the persona of Report and
	// contradicts its immutability requirement.
	Dissent = "dissent"

	// Lead consolidates like a careful lead: interpretations with the same
	// type and title are merged, and conflicting requirement statements
	// become a disagreement.
	Lead = "lead"
	// LeadUnknownRef cites an interpretation that was never shown.
	LeadUnknownRef = "lead-unknown-ref"
	// LeadDrop leaves an interpretation out.
	LeadDrop = "lead-drop"
)

// Argv returns the agent command running the fake agent with a scenario.
func Argv(scenario string) []string {
	return []string{os.Args[0], marker, scenario}
}

// MaybeRun turns the current process into the fake agent when it was
// started through Argv. Call it first thing in TestMain.
func MaybeRun() {
	if len(os.Args) == 3 && os.Args[1] == marker {
		os.Exit(fake(os.Args[2]))
	}
}

func fake(scenario string) int {
	data, _ := io.ReadAll(os.Stdin)
	prompt := string(data)
	task := strings.Contains(prompt, "# Raun analysis task") || strings.Contains(prompt, "# Raun consolidation task")
	if strings.HasPrefix(scenario, Lead) {
		task = strings.Contains(prompt, "# Raun consolidation task")
	}
	if !task || os.Getenv("RAUN_RUN_ID") == "" {
		fmt.Fprintln(os.Stderr, "fake agent: unexpected prompt or environment")
		return 4
	}
	// Isolation: no Git metadata, no knowledge base, no past runs, no
	// configuration (it would reveal which agents take part).
	for _, p := range []string{".git", ".raun/knowledge", ".raun/runs", ".raun/config.yaml"} {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(os.Stderr, "fake agent: %s is visible\n", p)
			return 3
		}
	}

	switch scenario {
	case Dissent:
		fmt.Println(DissentReport)
	case Lead, LeadUnknownRef, LeadDrop:
		out, err := consolidate(prompt, scenario)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake lead:", err)
			return 6
		}
		fmt.Println(out)
	case Valid:
		fmt.Println(Report)
	case Chatty:
		fmt.Printf("Reading the repository...\nDone. Here is my report:\n```json\n%s\n```\n", Report)
	case Tamper:
		_ = os.WriteFile("billing/invoice.go", []byte("package hacked\n"), 0o644)
		fmt.Println(Report)
	case Garbage:
		fmt.Println("I could not finish the analysis.")
	case BadContract:
		fmt.Println(`{"version": 1, "observations": [], "interpretations": [], "questions": [], "confidence": 0.9}`)
	case Crash:
		fmt.Fprintln(os.Stderr, "fake agent: crashing")
		return 2
	case Hang:
		time.Sleep(time.Minute)
	default:
		fmt.Fprintf(os.Stderr, "fake agent: unknown scenario %q\n", scenario)
		return 5
	}
	return 0
}

// Invoice is billing/invoice.go in the fixture project.
const Invoice = `package billing

func (s *Service) Update(inv *Invoice) error {
	if inv.Status == StatusIssued {
		return ErrInvoiceLocked
	}
	return s.repo.Save(inv)
}
`

// Report is the fake agent's report on the fixture project. Its citations
// cover every verification outcome:
//   - o1: verified (code), o2: verified (doc);
//   - o3: relocated (human context, wrong line numbers);
//   - o4: invalid (excluded vendor path);
//   - o5: invalid (invented excerpt).
//
// i3 rests only on o4, so it is downgraded to a hypothesis.
const Report = `{
  "version": 1,
  "observations": [
    {"id": "o1", "statement": "Update rejects issued invoices.",
     "evidence": [{"path": "billing/invoice.go", "start_line": 4, "end_line": 6,
                   "excerpt": "  if inv.Status == StatusIssued {\n    return ErrInvoiceLocked\n  }"}]},
    {"id": "o2", "statement": "The docs allow correcting an issued invoice within 24 hours.",
     "evidence": [{"path": "docs/billing.md", "start_line": 3, "end_line": 3,
                   "excerpt": "Admins may correct an issued invoice within 24 hours."}]},
    {"id": "o3", "statement": "The team says accountants use the product.",
     "evidence": [{"path": ".raun/context.md", "start_line": 9, "end_line": 9,
                   "excerpt": "The product is used by accountants."}]},
    {"id": "o4", "statement": "A vendored library handles corrections.",
     "evidence": [{"path": "vendor/lib/lib.go", "start_line": 1, "end_line": 1, "excerpt": "package lib"}]},
    {"id": "o5", "statement": "Invoices live in their own package.",
     "evidence": [{"path": "billing/invoice.go", "start_line": 1, "end_line": 1, "excerpt": "package invoices"}]}
  ],
  "interpretations": [
    {"id": "i1", "type": "persona", "title": "Accountant", "statement": "Accountants issue and correct invoices.",
     "nature": "supported", "observations": ["o3"],
     "persona": {"description": "Issues invoices for customers.", "goals": ["Bill customers correctly"], "capabilities": ["Issue invoices"]}},
    {"id": "i2", "type": "requirement", "title": "Issued invoices are immutable", "statement": "Once issued, an invoice cannot be updated.",
     "nature": "supported", "observations": ["o1", "o5"],
     "requirement": {"statement": "An issued invoice cannot be updated.", "kind": "business-rule", "rationale": "", "personas": ["i1"]}},
    {"id": "i3", "type": "requirement", "title": "Correction window", "statement": "Issued invoices can be corrected for 24 hours.",
     "nature": "supported", "observations": ["o4"],
     "requirement": {"statement": "Admins can correct an issued invoice within 24 hours.", "kind": "business-rule", "rationale": "", "personas": []}}
  ],
  "questions": [
    {"id": "q1", "question": "The docs allow a correction window that the code forbids: which one is right?", "about": ["i2"]},
    {"id": "q2", "question": "Is there an API for external accounting tools?", "about": []}
  ]
}`

// DissentReport agrees with Report on the Accountant persona and claims,
// from the documentation, that issued invoices can be corrected.
const DissentReport = `{
  "version": 1,
  "observations": [
    {"id": "o1", "statement": "The docs allow correcting an issued invoice within 24 hours.",
     "evidence": [{"path": "docs/billing.md", "start_line": 3, "end_line": 3,
                   "excerpt": "Admins may correct an issued invoice within 24 hours."}]},
    {"id": "o2", "statement": "The team says accountants use the product.",
     "evidence": [{"path": ".raun/context.md", "start_line": 3, "end_line": 3,
                   "excerpt": "The product is used by accountants."}]}
  ],
  "interpretations": [
    {"id": "i1", "type": "persona", "title": "Accountant", "statement": "The product serves accountants.",
     "nature": "supported", "observations": ["o2"],
     "persona": {"description": "Uses the product for invoicing.", "goals": [], "capabilities": []}},
    {"id": "i2", "type": "requirement", "title": "Issued invoices are immutable", "statement": "Issued invoices stay editable for 24 hours.",
     "nature": "supported", "observations": ["o1"],
     "requirement": {"statement": "Admins can correct an issued invoice within 24 hours.", "kind": "business-rule", "rationale": "", "personas": ["i1"]}}
  ],
  "questions": [
    {"id": "q1", "question": "Do accountants also manage customers?", "about": ["i1"]}
  ]
}`

// Agent is one analyst of a fixture configuration.
type Agent struct {
	ID   string
	Argv []string
}

// Config returns a configuration with a single agent running argv.
func Config(argv []string, timeout string) string {
	return TeamConfig(timeout, nil, Agent{ID: "alpha", Argv: argv})
}

// TeamConfig returns a configuration with the given analysts and, if
// leadArgv is not nil, a lead.
func TeamConfig(timeout string, leadArgv []string, agents ...Agent) string {
	quote := func(argv []string) string {
		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = fmt.Sprintf("%q", a)
		}
		return strings.Join(quoted, ", ")
	}
	var b strings.Builder
	b.WriteString(`version: 1
language: en
sources:
  exclude: ["vendor/**", ".raun/**"]
  context: [".raun/context.md"]
types: [persona, requirement]
analysis:
  quorum: 1
  agents:
`)
	for _, a := range agents {
		fmt.Fprintf(&b, "    - id: %s\n      runner: { argv: [%s], timeout: %s }\n", a.ID, quote(a.Argv), timeout)
	}
	if leadArgv != nil {
		fmt.Fprintf(&b, "lead:\n  runner: { argv: [%s], timeout: %s }\n", quote(leadArgv), timeout)
	}
	return b.String()
}

// NewProject commits the fixture project, configured with a single agent
// running argv.
func NewProject(t testing.TB, argv []string) *gittest.Repo {
	t.Helper()
	return NewTeamProject(t, Config(argv, "10s"))
}

// NewTeamProject commits the fixture project with the given configuration.
func NewTeamProject(t testing.TB, config string) *gittest.Repo {
	t.Helper()
	fx := gittest.New(t)
	fx.Write("billing/invoice.go", Invoice)
	fx.Write("docs/billing.md", "# Billing\n\nAdmins may correct an issued invoice within 24 hours.\n")
	fx.Write("vendor/lib/lib.go", "package lib\n")
	fx.Write(".raun/context.md", "# Project context\n\nThe product is used by accountants.\n")
	fx.Write(".raun/config.yaml", config)
	fx.Write(".raun/.gitignore", "runs/*/raw/\n")
	fx.Commit("fixture project")
	return fx
}
