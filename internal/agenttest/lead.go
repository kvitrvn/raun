package agenttest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/knowledge"
)

type shownReports struct {
	Reports []struct {
		Interpretations []struct {
			ID          string                    `json:"id"`
			Type        string                    `json:"type"`
			Title       string                    `json:"title"`
			Persona     *agent.PersonaContent     `json:"persona"`
			Requirement *agent.RequirementContent `json:"requirement"`
		} `json:"interpretations"`
	} `json:"reports"`
}

// consolidate reads the anonymized reports from the lead prompt and groups
// interpretations by type and title.
func consolidate(prompt, scenario string) (string, error) {
	_, rest, ok := strings.Cut(prompt, "````json\n")
	body, _, ok2 := strings.Cut(rest, "\n````")
	if !ok || !ok2 {
		return "", errors.New("no reports in prompt")
	}
	var shown shownReports
	if err := json.Unmarshal([]byte(body), &shown); err != nil {
		return "", err
	}

	type group struct {
		item       agent.PlannedItem
		statements map[string][]string // requirement statement -> refs
		order      []string
	}
	var groups []*group
	byKey := map[string]*group{}
	itemOf := map[string]string{} // interpretation ref -> item id
	for _, r := range shown.Reports {
		for _, in := range r.Interpretations {
			key := in.Type + "/" + in.Title
			g := byKey[key]
			if g == nil {
				g = &group{
					item: agent.PlannedItem{
						ID: fmt.Sprintf("k%d", len(groups)+1), Type: knowledge.Type(in.Type),
						Title: in.Title, Persona: in.Persona, Requirement: in.Requirement,
					},
					statements: map[string][]string{},
				}
				groups = append(groups, g)
				byKey[key] = g
			}
			g.item.Support = append(g.item.Support, in.ID)
			itemOf[in.ID] = g.item.ID
			if in.Requirement != nil {
				s := in.Requirement.Statement
				if _, seen := g.statements[s]; !seen {
					g.order = append(g.order, s)
				}
				g.statements[s] = append(g.statements[s], in.ID)
			}
		}
	}

	c := agent.Consolidation{Version: 1}
	for _, g := range groups {
		it := g.item
		it.Disagreements, it.Uncertainties, it.Questions = []agent.Disagreement{}, []string{}, []string{}
		if it.Requirement != nil {
			req := *it.Requirement
			var personas []string
			for _, ref := range req.Personas {
				personas = append(personas, itemOf[ref])
			}
			req.Personas = personas
			it.Requirement = &req
		}
		if len(g.order) > 1 {
			d := agent.Disagreement{Summary: "The interpretations state different rules."}
			for _, s := range g.order {
				d.Positions = append(d.Positions, agent.Position{Statement: s, Support: g.statements[s]})
			}
			it.Disagreements = append(it.Disagreements, d)
			it.Uncertainties = append(it.Uncertainties, "Code and documentation disagree.")
		}
		c.Items = append(c.Items, it)
	}

	switch scenario {
	case LeadUnknownRef:
		c.Items[0].Support = append(c.Items[0].Support, "Z.i9")
	case LeadDrop:
		last := &c.Items[len(c.Items)-1]
		last.Support = last.Support[:len(last.Support)-1]
	}
	out, err := json.MarshalIndent(c, "", "  ")
	return string(out), err
}
