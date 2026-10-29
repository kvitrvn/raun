package agenttest

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/kvitrvn/raun/internal/agent"
)

type shownItems struct {
	Existing []shownItem `json:"existing"`
	New      []shownItem `json:"new"`
}

type shownItem struct {
	ID          string                    `json:"id"`
	Type        string                    `json:"type"`
	Title       string                    `json:"title"`
	Requirement *agent.RequirementContent `json:"requirement"`
}

// reconcile reads the items from the reconciler prompt and matches new
// items to existing items with the same type and title. A requirement whose
// statement differs is declared in conflict.
func reconcile(prompt, scenario string) (string, error) {
	_, rest, ok := strings.Cut(prompt, "````json\n")
	body, _, ok2 := strings.Cut(rest, "\n````")
	if !ok || !ok2 {
		return "", errors.New("no items in prompt")
	}
	var shown shownItems
	if err := json.Unmarshal([]byte(body), &shown); err != nil {
		return "", err
	}

	r := agent.Reconciliation{Version: 1, Items: []agent.Match{}}
	for _, n := range shown.New {
		m := agent.Match{ID: n.ID}
		for _, e := range shown.Existing {
			if e.Type != n.Type || e.Title != n.Title {
				continue
			}
			m.Match = e.ID
			if n.Requirement != nil && e.Requirement != nil && n.Requirement.Statement != e.Requirement.Statement {
				m.Conflict = "The new analysis states a different rule."
			}
			break
		}
		r.Items = append(r.Items, m)
	}

	switch scenario {
	case ReconcilerUnknownMatch:
		r.Items[0].Match = "persona-zzzzzz"
	case ReconcilerDrop:
		r.Items = r.Items[:len(r.Items)-1]
	}
	out, err := json.MarshalIndent(r, "", "  ")
	return string(out), err
}
