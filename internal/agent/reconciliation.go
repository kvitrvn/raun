package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
)

// ReconciliationVersion is the reconciler contract version.
const ReconciliationVersion = 1

// Reconciliation is what the reconciler returns: for each item of a run,
// the existing knowledge item it describes, if any. The reconciler only
// matches; it cites no evidence and changes no content. See docs/agents.md.
type Reconciliation struct {
	Version int     `json:"version"`
	Items   []Match `json:"items"`
}

// Match places one item of the run.
type Match struct {
	// ID is the run item's reference, such as "n1".
	ID string `json:"id"`
	// Match is the ID of the existing item the run item describes, or ""
	// for something the knowledge base does not hold yet.
	Match string `json:"match"`
	// Conflict summarizes how the run item contradicts the content of the
	// existing item it matches; "" when they agree.
	Conflict string `json:"conflict"`
}

// ParseReconciliation extracts, strictly decodes and validates a
// reconciliation. existing maps the IDs of the existing items shown to the
// reconciler to their type, and items the references of the run's items
// (such as "n1"). salvage is set for the output of a reconciler stopped
// before it finished (see Extract).
func ParseReconciliation(out []byte, existing, items map[string]knowledge.Type, salvage bool) (*Reconciliation, []byte, error) {
	data, err := Extract(out, salvage)
	if err != nil {
		return nil, nil, err
	}
	var r Reconciliation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, data, fmt.Errorf("decode reconciliation: %w", err)
	}
	if err := r.Validate(existing, items); err != nil {
		return nil, data, err
	}
	return &r, data, nil
}

// Validate checks the reconciliation against the items the reconciler was
// given. Every run item must be placed exactly once, and a match must name
// an existing item of the same type.
func (r *Reconciliation) Validate(existing, items map[string]knowledge.Type) error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}

	if r.Version != ReconciliationVersion {
		add("version", "must be %d, got %d", ReconciliationVersion, r.Version)
	}

	placed := map[string]bool{}
	for i, m := range r.Items {
		f := fmt.Sprintf("items[%d]", i)
		t, ok := items[m.ID]
		switch {
		case !ok:
			add(f+".id", "unknown run item %q", m.ID)
		case placed[m.ID]:
			add(f+".id", "run item %q is already placed", m.ID)
		}
		placed[m.ID] = true

		if m.Match != "" {
			et, known := existing[m.Match]
			switch {
			case !known:
				add(f+".match", "unknown existing item %q", m.Match)
			case ok && et != t:
				add(f+".match", "existing item %q is a %s, not a %s", m.Match, et, t)
			}
		}
		if m.Match == "" && strings.TrimSpace(m.Conflict) != "" {
			add(f+".conflict", "a conflict needs a match")
		}
	}

	var missing []string
	for id := range items {
		if !placed[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		add("items", "every run item must be placed; missing: %s", strings.Join(missing, ", "))
	}

	return errors.Join(errs...)
}
