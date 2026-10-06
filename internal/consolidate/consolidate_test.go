package consolidate

import (
	"encoding/json"
	"fmt"
	"slices"
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

// alphaReport has a persona and two requirements, with every evidence
// outcome; it also asks one item question and one project-wide question.
func alphaReport() CheckedReport {
	return CheckedReport{
		Agent: "alpha",
		Report: &agent.Report{
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
		},
		Citations: map[string][]CheckedCitation{
			"o1": {checked("auth/roles.go", 3, 4, knowledge.EvidenceVerified, ""), checked("auth/roles.go", 30, 31, knowledge.EvidenceInvalid, "excerpt not found")},
			"o2": {checked("ui/menu.go", 7, 7, knowledge.EvidenceRelocated, "excerpt found at lines 7-7"), checked("auth/roles.go", 3, 4, knowledge.EvidenceVerified, "")},
			"o3": {checked("auth/none.go", 1, 1, knowledge.EvidenceInvalid, "auth/none.go does not exist at this commit")},
		},
	}
}

// betaReport agrees on the persona and contradicts the requirement.
func betaReport() CheckedReport {
	return CheckedReport{
		Agent: "beta",
		Report: &agent.Report{
			Version: 1,
			Observations: []agent.Observation{
				{ID: "o1", Statement: "Support staff can delete accounts."},
			},
			Interpretations: []agent.Interpretation{
				{
					ID: "i1", Type: knowledge.TypePersona, Title: "Admin", Statement: "Admins manage accounts.",
					Nature: knowledge.NatureSupported, Observations: []string{"o1"},
					Persona: &agent.PersonaContent{Description: "Manages accounts."},
				},
				{
					ID: "i2", Type: knowledge.TypeRequirement, Title: "Support can delete", Statement: "Support staff delete accounts too.",
					Nature: knowledge.NatureSupported, Observations: []string{"o1"},
					Requirement: &agent.RequirementContent{Statement: "Support staff can delete accounts.", Kind: knowledge.KindBusinessRule},
				},
			},
			Questions: []agent.Question{{ID: "q1", Question: "Can admins restore accounts?", About: []string{"i1"}}},
		},
		Citations: map[string][]CheckedCitation{
			"o1": {checked("auth/roles.go", 3, 4, knowledge.EvidenceVerified, ""), checked("support/delete.go", 10, 12, knowledge.EvidenceVerified, "")},
		},
	}
}

func validateAll(t *testing.T, items []*knowledge.Item) {
	t.Helper()
	for _, it := range items {
		if err := it.Validate(); err != nil {
			t.Errorf("%s invalid: %v", it.ID, err)
		}
	}
}

func TestBuildIdentity(t *testing.T) {
	reports := []CheckedReport{alphaReport()}
	res, err := Build("run-1", commit, at, reports, Identity(reports), sequentialIDs())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || len(res.Questions) != 1 || res.Questions[0] != (AgentQuestion{Agent: "alpha", ID: "q2", Question: "Is there a mobile app?"}) {
		t.Fatalf("got %d items, questions %+v", len(res.Items), res.Questions)
	}
	validateAll(t, res.Items)
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
	if got := persona.OpenPoints; len(got) != 2 ||
		got[0].Kind != knowledge.PointUncertainty || got[0].RaisedBy != RaisedByRaun ||
		!strings.Contains(got[0].Summary, `1 of 3 observation(s)`) || strings.Contains(got[0].Summary, "downgraded") ||
		got[1].Kind != knowledge.PointQuestion || got[1].RaisedBy != "alpha" {
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
	if req.Status != knowledge.StatusProposed || len(req.History) != 1 {
		t.Errorf("status = %s, history = %+v", req.Status, req.History)
	}
}

func TestLeadInputIsAnonymous(t *testing.T) {
	reports := []CheckedReport{alphaReport(), betaReport()}
	labels := Labels([]int{1, 0}) // beta gets A, alpha gets B
	if !slices.Equal(labels, []string{"B", "A"}) {
		t.Fatalf("Labels() = %v", labels)
	}

	data, refs, err := LeadInput(reports, labels)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if strings.Contains(string(data), name) {
			t.Errorf("lead input reveals agent %q", name)
		}
	}
	var in leadInput
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if in.Reports[0].Label != "A" || in.Reports[0].Interpretations[0].Title != "Admin" {
		t.Errorf("reports must be in label order: %+v", in.Reports[0])
	}
	b := in.Reports[1]
	if b.Interpretations[1].Requirement.Personas[0] != "B.i1" || b.Questions[0].About[0] != "B.i1" {
		t.Errorf("references must be prefixed: %+v", b)
	}
	if e := b.Observations[0].Evidence[1]; e.Verification != "invalid" || e.Reason != "excerpt not found" {
		t.Errorf("verification outcome missing: %+v", e)
	}
	if e := b.Observations[1].Evidence[0]; e.Excerpt != "text of ui/menu.go:7-7" {
		t.Errorf("valid evidence must show the file's text: %+v", e)
	}
	want := map[string]knowledge.Type{"A.i1": "persona", "A.i2": "requirement", "B.i1": "persona", "B.i2": "requirement"}
	if len(refs) != len(want) {
		t.Errorf("refs = %v", refs)
	}
	for k, v := range want {
		if refs[k] != v {
			t.Errorf("refs[%s] = %q, want %q", k, refs[k], v)
		}
	}
}

func TestBuildFromLead(t *testing.T) {
	reports := []CheckedReport{alphaReport(), betaReport()}
	labels := Labels([]int{1, 0}) // alpha = B, beta = A
	c := &agent.Consolidation{
		Version: 1,
		Items: []agent.PlannedItem{
			{
				ID: "k1", Type: knowledge.TypePersona, Title: "Administrator",
				Persona: &agent.PersonaContent{Description: "Runs and manages the platform."},
				Support: []string{"B.i1", "A.i1"},
			},
			{
				ID: "k2", Type: knowledge.TypeRequirement, Title: "Account deletion",
				Requirement: &agent.RequirementContent{Statement: "Account deletion is restricted.", Kind: knowledge.KindBusinessRule, Personas: []string{"k1"}},
				Support:     []string{"B.i2", "A.i2"},
				Disagreements: []agent.Disagreement{{
					Summary: "Who may delete accounts?",
					Positions: []agent.Position{
						{Statement: "Only admins.", Support: []string{"B.i2"}},
						{Statement: "Support staff too.", Support: []string{"A.i2"}},
					},
				}},
				Uncertainties: []string{"The support module may be dead code."},
			},
		},
	}
	plan, err := FromLead(c, labels)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build("run-2", commit, at, reports, plan, sequentialIDs())
	if err != nil {
		t.Fatal(err)
	}
	validateAll(t, res.Items)
	persona, req := res.Items[0], res.Items[1]

	// Merged persona: two agents, shared evidence stored once, and the same
	// question asked by both agents recorded once.
	if len(persona.Support) != 2 || persona.Support[0].Agent != "alpha" || persona.Support[1].Agent != "beta" {
		t.Errorf("persona support = %+v", persona.Support)
	}
	var questions int
	for _, p := range persona.OpenPoints {
		if p.Kind == knowledge.PointQuestion {
			questions++
		}
	}
	if questions != 1 {
		t.Errorf("duplicate question not merged: %+v", persona.OpenPoints)
	}
	if req.Requirement.Personas[0] != persona.ID {
		t.Errorf("persona link = %v", req.Requirement.Personas)
	}

	// Contested requirement with both positions and their evidence.
	if req.Status != knowledge.StatusContested || req.History[1].From != knowledge.StatusProposed || req.History[1].By != "run-2" {
		t.Errorf("status = %s, history = %+v", req.Status, req.History)
	}
	i := slices.IndexFunc(req.OpenPoints, func(p knowledge.OpenPoint) bool { return p.Kind == knowledge.PointDisagreement })
	d := req.OpenPoints[i]
	if d.RaisedBy != RaisedByLead || len(d.Positions) != 2 {
		t.Fatalf("disagreement = %+v", d)
	}
	if p := d.Positions[0]; !slices.Equal(p.Agents, []string{"alpha"}) || len(p.Evidence) != 0 {
		t.Errorf("alpha position = %+v (its only evidence was invalid)", p)
	}
	if p := d.Positions[1]; !slices.Equal(p.Agents, []string{"beta"}) || len(p.Evidence) != 2 || p.Run != "run-2" {
		t.Errorf("beta position = %+v", p)
	}
	if !slices.ContainsFunc(req.OpenPoints, func(p knowledge.OpenPoint) bool {
		return p.Kind == knowledge.PointUncertainty && p.RaisedBy == RaisedByLead
	}) {
		t.Errorf("lead uncertainty missing: %+v", req.OpenPoints)
	}
	if len(res.Questions) != 1 {
		t.Errorf("project-wide questions = %+v", res.Questions)
	}
}

func TestFromLeadUnknownLabel(t *testing.T) {
	c := &agent.Consolidation{Items: []agent.PlannedItem{{ID: "k1", Support: []string{"C.i1"}}}}
	if _, err := FromLead(c, []string{"A", "B"}); err == nil {
		t.Error("FromLead accepted an unknown label")
	}
}
