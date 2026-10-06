package knowledge

import (
	"strings"
	"time"
)

const (
	testCommit = "3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b"
	testRun    = "run-20261007-a1"
	testPerson = "PO"
)

var testTime = time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)

func evidence(id string, source SourceKind, path string, start, end int, excerpt string) Evidence {
	return Evidence{
		ID:        id,
		Source:    source,
		Path:      path,
		Commit:    testCommit,
		StartLine: start,
		EndLine:   end,
		Excerpt:   excerpt,
		SHA256:    HashExcerpt(excerpt),
		State:     EvidenceVerified,
	}
}

func created() []Event {
	return []Event{{At: testTime, Actor: ActorRun, By: testRun, To: StatusProposed}}
}

func samplePersona() *Item {
	return &Item{
		Version: FormatVersion,
		ID:      "persona-k3x9q2",
		Type:    TypePersona,
		Title:   "Platform administrator",
		Status:  StatusProposed,
		Persona: &Persona{
			Description:  "Operates the platform for all tenants.",
			Goals:        []string{"Keep tenant accounts healthy"},
			Capabilities: []string{"Delete any user account", "Change billing plans"},
		},
		Support: []Support{
			{
				Run:            testRun,
				Agent:          "alpha",
				Interpretation: "i1",
				Statement:      "Administrators manage every tenant account.",
				Nature:         NatureSupported,
				Observations: []Observation{
					{Statement: "The admin role can delete any account.", Evidence: []string{"e1"}},
				},
			},
			{
				Run:            testRun,
				Agent:          "beta",
				Interpretation: "i4",
				Statement:      "Administrators are internal support staff.",
				Nature:         NatureHypothesis,
			},
		},
		Evidence: []Evidence{
			evidence("e1", SourceCode, "internal/auth/roles.go", 12, 14,
				"case RoleAdmin:\n\treturn []Permission{DeleteAccount, ChangePlan}\n}"),
		},
		OpenPoints: []OpenPoint{
			{ID: "p1", Kind: PointQuestion, Summary: "Are administrators employees or customers?"},
		},
		History: created(),
	}
}

func sampleRequirement() *Item {
	return &Item{
		Version: FormatVersion,
		ID:      "requirement-8fz2mc",
		Type:    TypeRequirement,
		Title:   "Issued invoices are immutable",
		Status:  StatusContested,
		Requirement: &Requirement{
			Statement: "An invoice cannot be edited once issued.",
			Kind:      KindBusinessRule,
			Rationale: "Legal immutability of issued invoices.",
			Personas:  []string{"persona-k3x9q2"},
		},
		Support: []Support{
			{
				Run:            testRun,
				Agent:          "alpha",
				Interpretation: "i2",
				Statement:      "Issued invoices are read-only.",
				Nature:         NatureSupported,
				Observations: []Observation{
					{Statement: "Update rejects invoices with status issued.", Evidence: []string{"e1"}},
				},
			},
		},
		Evidence: []Evidence{
			evidence("e1", SourceCode, "billing/invoice.go", 40, 42,
				"if inv.Status == StatusIssued {\n\treturn ErrInvoiceLocked\n}"),
			evidence("e2", SourceDoc, "docs/billing.md", 8, 8,
				"Admins may correct an issued invoice within 24 hours."),
		},
		OpenPoints: []OpenPoint{
			{
				ID:      "p1",
				Kind:    PointDisagreement,
				Summary: "Code forbids edits, docs allow a 24h correction window.",
				Positions: []Position{
					{Statement: "Issued invoices are never editable.", Run: testRun, Agents: []string{"alpha"}, Evidence: []string{"e1"}},
					{Statement: "Admins can edit within 24 hours.", Run: testRun, Agents: []string{"beta"}, Evidence: []string{"e2"}},
				},
			},
		},
		History: []Event{
			{At: testTime, Actor: ActorRun, By: testRun, To: StatusProposed},
			{At: testTime, Actor: ActorRun, By: testRun, From: StatusProposed, To: StatusContested},
		},
	}
}

// resolveAll resolves every open point of it.
func resolveAll(it *Item) {
	pos := 0
	for i := range it.OpenPoints {
		it.OpenPoints[i].Resolution = &Resolution{At: testTime, By: testPerson, Position: &pos, Note: "checked with " + strings.ToLower(testPerson)}
	}
}
