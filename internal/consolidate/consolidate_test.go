package consolidate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/evidence"
	"github.com/kvitrvn/raun/internal/knowledge"
)

const commit = "3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b"

var at = time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)

func checked(path string, start, end int, state knowledge.EvidenceState, reason string) CheckedCitation {
	c := CheckedCitation{
		Citation: agent.Citation{Path: path, StartLine: start, EndLine: end, Excerpt: "quoted"},
		Source:   knowledge.SourceCode,
		Result:   evidence.Result{State: state, Reason: reason},
	}
	if c.Valid() {
		c.Result.StartLine, c.Result.EndLine = start, end
		c.Result.Excerpt = fmt.Sprintf("text of %s:%d-%d", path, start, end)
	}
	return c
}

func sequentialIDs() func(knowledge.Type) (string, error) {
	n := 0
	return func(t knowledge.Type) (string, error) {
		n++
		return fmt.Sprintf("%s-aaaaa%d", t, n), nil
	}
}

func TestSingle(t *testing.T) {
	report := &agent.Report{
		Version: 1,
		Observations: []agent.Observation{
			{ID: "o1", Statement: "Admins delete accounts."},
			{ID: "o2", Statement: "Admin menu exists."},
			{ID: "o3", Statement: "Invented fact."},
		},
		Interpretations: []agent.Interpretation{
			{
				ID: "i1", Type: knowledge.TypePersona, Title: "Administrator", Statement: "Admins run the platform.",
				Nature: knowledge.NatureSupported, Observations: []string{"o1", "o2", "o3"},
				Persona: &agent.PersonaContent{Description: "Runs the platform."},
			},
			{
				ID: "i2", Type: knowledge.TypeRequirement, Title: "Restricted deletion", Statement: "Only admins delete.",
				Nature: knowledge.NatureSupported, Observations: []string{"o3"},
				Requirement: &agent.RequirementContent{Statement: "Only admins can delete accounts.", Kind: knowledge.KindBusinessRule, Personas: []string{"i1"}},
			},
		},
		Questions: []agent.Question{
			{ID: "q1", Question: "Can admins restore accounts?", About: []string{"i1", "i2"}},
			{ID: "q2", Question: "Is there a mobile app?"},
		},
	}
	cr := CheckedReport{
		Agent:  "alpha",
		Report: report,
		Citations: map[string][]CheckedCitation{
			"o1": {checked("auth/roles.go", 3, 4, knowledge.EvidenceVerified, ""), checked("auth/roles.go", 30, 31, knowledge.EvidenceInvalid, "excerpt not found")},
			"o2": {checked("ui/menu.go", 7, 7, knowledge.EvidenceRelocated, "excerpt found at lines 7-7"), checked("auth/roles.go", 3, 4, knowledge.EvidenceVerified, "")},
			"o3": {checked("auth/none.go", 1, 1, knowledge.EvidenceInvalid, "auth/none.go does not exist at this commit")},
		},
	}

	res, err := Single("run-1", commit, at, cr, sequentialIDs())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || len(res.Questions) != 1 || res.Questions[0].ID != "q2" {
		t.Fatalf("got %d items, questions %+v", len(res.Items), res.Questions)
	}
	for _, it := range res.Items {
		if err := it.Validate(); err != nil {
			t.Errorf("%s invalid: %v", it.ID, err)
		}
	}

	persona, req := res.Items[0], res.Items[1]

	// Shared lines are stored once; invalid citations never reach the item.
	if len(persona.Evidence) != 2 {
		t.Errorf("persona evidence = %+v, want 2 entries", persona.Evidence)
	}
	s := persona.Support[0]
	if s.Nature != knowledge.NatureSupported || len(s.Observations) != 2 || s.Observations[1].Evidence[1] != "e1" {
		t.Errorf("persona support = %+v", s)
	}
	if persona.Evidence[1].State != knowledge.EvidenceRelocated || persona.Evidence[0].Excerpt != "text of auth/roles.go:3-4" {
		t.Errorf("persona evidence = %+v", persona.Evidence)
	}
	if got := persona.OpenPoints; len(got) != 2 || got[0].Kind != knowledge.PointUncertainty ||
		!strings.Contains(got[0].Summary, `1 of 3 observation(s)`) || strings.Contains(got[0].Summary, "downgraded") ||
		got[1].Kind != knowledge.PointQuestion {
		t.Errorf("persona open points = %+v", got)
	}

	// The requirement lost its only observation: downgraded and flagged.
	if rs := req.Support[0]; rs.Nature != knowledge.NatureHypothesis || len(rs.Observations) != 0 || len(req.Evidence) != 0 {
		t.Errorf("requirement support = %+v, evidence = %+v", rs, req.Evidence)
	}
	if !strings.Contains(req.OpenPoints[0].Summary, "downgraded to a hypothesis") ||
		!strings.Contains(req.OpenPoints[0].Summary, "does not exist") {
		t.Errorf("requirement open point = %q", req.OpenPoints[0].Summary)
	}
	if req.Requirement.Personas[0] != persona.ID {
		t.Errorf("persona link = %v, want %s", req.Requirement.Personas, persona.ID)
	}
	if h := req.History[0]; h.By != "run-1" || h.Actor != knowledge.ActorRun || h.To != knowledge.StatusProposed {
		t.Errorf("history = %+v", h)
	}
}
