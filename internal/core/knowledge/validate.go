package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// FieldError is one problem of an item, at a field path such as
// support[1].observations[0].evidence[2].
type FieldError struct {
	Path    string
	Message string
}

// ValidationError lists every problem Validate found, in the order of the
// fields.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		lines[i] = fe.Path + ": " + fe.Message
	}
	return "invalid knowledge item:\n" + strings.Join(lines, "\n")
}

// validator collects field errors.
type validator struct {
	errs []FieldError
}

func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) required(path, value string) {
	if strings.TrimSpace(value) == "" {
		v.add(path, "required")
	}
}

func (v *validator) utc(path string, t time.Time) {
	switch {
	case t.IsZero():
		v.add(path, "required")
	case t.Location() != time.UTC:
		v.add(path, "must be in UTC")
	}
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

// ExcerptSHA256 returns the hex SHA-256 of an excerpt, as the sha256 field of
// an evidence holds it.
func ExcerptSHA256(excerpt string) string {
	sum := sha256.Sum256([]byte(excerpt))
	return hex.EncodeToString(sum[:])
}

// Validate checks the item against docs/format.md and returns a
// *ValidationError listing every problem, or nil. It checks the references
// inside the item; that the personas a requirement names exist is checked by
// the store.
func (it Item) Validate() error {
	var v validator
	it.validateHeader(&v)
	it.validateContent(&v)
	evidence := it.validateEvidence(&v)
	it.validateSupport(&v, evidence)
	it.validateOpenPoints(&v, evidence)
	it.validateHistory(&v)
	it.validateStatus(&v)
	return v.err()
}

func (it Item) validateHeader(v *validator) {
	if t, ok := idType(it.ID); !ok {
		v.add("id", "must be <type>-<6 characters of %s>, got %q", idAlphabet, it.ID)
	} else if t != it.Type {
		v.add("id", "type prefix %q does not match type %q", t, it.Type)
	}
	if !it.Type.Valid() {
		v.add("type", "unknown type %q", it.Type)
	}
	v.required("title", it.Title)
	if !it.Status.Valid() {
		v.add("status", "unknown status %q", it.Status)
	}
	if it.Revision < 1 {
		v.add("revision", "must be at least 1, got %d", it.Revision)
	}
	v.utc("created_at", it.CreatedAt)
	v.utc("updated_at", it.UpdatedAt)
	if !it.CreatedAt.IsZero() && it.UpdatedAt.Before(it.CreatedAt) {
		v.add("updated_at", "is before created_at")
	}
}

func (it Item) validateContent(v *validator) {
	switch it.Type {
	case TypePersona:
		if it.Persona == nil {
			v.add("persona", "required for type persona")
		} else {
			v.required("persona.description", it.Persona.Description)
		}
		if it.Requirement != nil {
			v.add("requirement", "only for type requirement")
		}
	case TypeRequirement:
		if it.Requirement == nil {
			v.add("requirement", "required for type requirement")
		} else {
			r := it.Requirement
			v.required("requirement.statement", r.Statement)
			if r.Kind != "" && !r.Kind.Valid() {
				v.add("requirement.kind", "unknown kind %q", r.Kind)
			}
			seen := map[string]bool{}
			for i, p := range r.Personas {
				at := fmt.Sprintf("requirement.personas[%d]", i)
				if t, ok := idType(p); !ok || t != TypePersona {
					v.add(at, "must be a persona identifier, got %q", p)
				} else if seen[p] {
					v.add(at, "duplicate persona %q", p)
				}
				seen[p] = true
			}
		}
		if it.Persona != nil {
			v.add("persona", "only for type persona")
		}
	}
}

// validateEvidence checks the evidence and returns the set of its
// identifiers, for the references of support and positions.
func (it Item) validateEvidence(v *validator) map[string]bool {
	ids := map[string]bool{}
	for i, e := range it.Evidence {
		at := fmt.Sprintf("evidence[%d]", i)
		if _, ok := localID(e.ID, "e"); !ok {
			v.add(at+".id", "must be e<number>, got %q", e.ID)
		} else if ids[e.ID] {
			v.add(at+".id", "duplicate identifier %q", e.ID)
		} else {
			ids[e.ID] = true
		}

		if !e.Source.Valid() {
			v.add(at+".source", "unknown source %q", e.Source)
		}
		if msg := checkPath(e.Path); msg != "" {
			v.add(at+".path", "%s", msg)
		}
		if !isCommit(e.Commit) {
			v.add(at+".commit", "must be a full commit hash, got %q", e.Commit)
		}
		if e.Excerpt == "" {
			v.add(at+".excerpt", "required")
		} else if e.SHA256 != ExcerptSHA256(e.Excerpt) {
			v.add(at+".sha256", "does not match the excerpt")
		}

		switch {
		case !e.State.Valid():
			v.add(at+".state", "unknown state %q", e.State)
		case e.State == EvidenceVerified || e.State == EvidenceRelocated:
			if e.Reason != "" {
				v.add(at+".reason", "must be empty for a %s evidence", e.State)
			}
		case e.Reason == "":
			v.add(at+".reason", "required for a %s evidence", e.State)
		case !e.Reason.allows(e.State):
			v.add(at+".reason", "%q does not apply to a %s evidence", e.Reason, e.State)
		}

		// A bad-range citation is kept as the agent gave it, whatever its range.
		if !(e.State == EvidenceInvalid && e.Reason == ReasonBadRange) {
			if e.StartLine < 1 {
				v.add(at+".start_line", "must be at least 1, got %d", e.StartLine)
			}
			if e.EndLine < e.StartLine {
				v.add(at+".end_line", "must not be before start_line")
			}
		}

		if !e.CheckedAt.IsZero() && e.CheckedAt.Location() != time.UTC {
			v.add(at+".checked_at", "must be in UTC")
		}
		if e.CheckedCommit != "" && !isCommit(e.CheckedCommit) {
			v.add(at+".checked_commit", "must be a full commit hash, got %q", e.CheckedCommit)
		}
	}
	return ids
}

func (it Item) validateSupport(v *validator, evidence map[string]bool) {
	for i, s := range it.Support {
		at := fmt.Sprintf("support[%d]", i)
		v.required(at+".run", s.Run)
		v.required(at+".agent", s.Agent)
		v.required(at+".interpretation", s.Interpretation)
		v.required(at+".statement", s.Statement)
		switch {
		case !s.Nature.Valid():
			v.add(at+".nature", "unknown nature %q", s.Nature)
		case s.Nature == NatureSupported && len(s.Observations) == 0:
			v.add(at+".observations", "a supported interpretation needs at least one observation")
		}
		for j, o := range s.Observations {
			oat := fmt.Sprintf("%s.observations[%d]", at, j)
			v.required(oat+".statement", o.Statement)
			if len(o.Evidence) == 0 {
				v.add(oat+".evidence", "needs at least one evidence")
			}
			checkRefs(v, oat+".evidence", o.Evidence, evidence)
		}
	}
}

func (it Item) validateOpenPoints(v *validator, evidence map[string]bool) {
	ids := map[string]bool{}
	for i, p := range it.OpenPoints {
		at := fmt.Sprintf("open_points[%d]", i)
		if _, ok := localID(p.ID, "p"); !ok {
			v.add(at+".id", "must be p<number>, got %q", p.ID)
		} else if ids[p.ID] {
			v.add(at+".id", "duplicate identifier %q", p.ID)
		} else {
			ids[p.ID] = true
		}

		if !p.Kind.Valid() {
			v.add(at+".kind", "unknown kind %q", p.Kind)
		}
		v.required(at+".summary", p.Summary)
		if p.Kind == PointDisagreement && len(p.Positions) < 2 {
			v.add(at+".positions", "a disagreement needs at least two positions")
		}
		for j, pos := range p.Positions {
			pat := fmt.Sprintf("%s.positions[%d]", at, j)
			v.required(pat+".statement", pos.Statement)
			v.required(pat+".run", pos.Run)
			checkRefs(v, pat+".evidence", pos.Evidence, evidence)
		}
		if r := p.Resolution; r != nil {
			rat := at + ".resolution"
			v.utc(rat+".at", r.At)
			v.required(rat+".by", r.By)
			v.required(rat+".note", r.Note)
			if r.Position != nil && (*r.Position < 0 || *r.Position >= len(p.Positions)) {
				v.add(rat+".position", "no position %d among %d", *r.Position, len(p.Positions))
			}
		}
	}
}

func (it Item) validateHistory(v *validator) {
	if len(it.History) == 0 {
		v.add("history", "required: an item has at least its creation event")
		return
	}
	for i, e := range it.History {
		at := fmt.Sprintf("history[%d]", i)
		v.utc(at+".at", e.At)
		if !e.Actor.Valid() {
			v.add(at+".actor", "unknown actor %q", e.Actor)
		}
		v.required(at+".by", e.By)
		if !e.To.Valid() {
			v.add(at+".to", "unknown status %q", e.To)
		}

		if i > 0 && e.At.Before(it.History[i-1].At) {
			v.add(at+".at", "is before the previous event")
		}
		if i == 0 {
			if e.From != "" || e.To != StatusProposed || e.Actor != ActorRun {
				v.add(at, "the first event must be the creation of the item by a run, to %s", StatusProposed)
			}
			continue
		}
		if prev := it.History[i-1].To; e.From != prev {
			v.add(at+".from", "must be %q, the status of the previous event, got %q", prev, e.From)
		}
		if actor, ok := allowedTransition(e.From, e.To); !ok {
			v.add(at, "transition from %q to %q is not allowed", e.From, e.To)
		} else if e.Actor.Valid() && actor != e.Actor {
			v.add(at+".actor", "transition from %q to %q is made by a %s, got %s", e.From, e.To, actor, e.Actor)
		}
		v.required(at+".reason", e.Reason)
	}
	last := it.History[len(it.History)-1]
	if it.Status.Valid() && it.Status != last.To {
		v.add("status", "must be %q, the status of the last history event, got %q", last.To, it.Status)
	}
	if !it.UpdatedAt.IsZero() && it.UpdatedAt.Before(last.At) {
		v.add("updated_at", "is before the last history event")
	}
}

// validateStatus checks what a status says about open disagreements.
func (it Item) validateStatus(v *validator) {
	unresolved := it.HasUnresolvedDisagreement()
	switch it.Status {
	case StatusProposed, StatusValidated:
		if unresolved {
			v.add("status", "a %s item holds no unresolved disagreement", it.Status)
		}
	case StatusContested:
		if !unresolved {
			v.add("status", "a contested item holds at least one unresolved disagreement")
		}
	}
}

func checkRefs(v *validator, at string, refs []string, evidence map[string]bool) {
	for k, ref := range refs {
		if !evidence[ref] {
			v.add(fmt.Sprintf("%s[%d]", at, k), "no evidence %q in this item", ref)
		}
	}
}

// isCommit reports whether s is a full SHA-1 or SHA-256 commit hash in
// lowercase hexadecimal.
func isCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
