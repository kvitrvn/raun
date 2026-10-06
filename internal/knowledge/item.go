// Package knowledge defines Raun's knowledge model and its on-disk store.
//
// A knowledge Item never asserts truth on its own: it carries the agent
// interpretations that support it, the evidence they cite, the open points
// (disagreements, uncertainties, questions) and the history of every status
// change. See docs/format.md for the file format.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FormatVersion is the knowledge file format version written by this build.
const FormatVersion = 1

// Type is a knowledge type.
type Type string

// Built-in knowledge types.
const (
	TypePersona     Type = "persona"
	TypeRequirement Type = "requirement"
)

// Types lists the built-in knowledge types, in display order.
var Types = []Type{TypePersona, TypeRequirement}

// Status is the lifecycle status of a knowledge item.
type Status string

// Knowledge statuses.
const (
	StatusProposed    Status = "proposed"
	StatusContested   Status = "contested"
	StatusValidated   Status = "validated"
	StatusRejected    Status = "rejected"
	StatusNeedsReview Status = "needs-review"
)

// Statuses lists all statuses, in lifecycle order.
var Statuses = []Status{StatusProposed, StatusContested, StatusValidated, StatusRejected, StatusNeedsReview}

// Nature tells whether an interpretation rests on observations or is an
// inference the agent declared as such.
type Nature string

// Interpretation natures.
const (
	NatureSupported  Nature = "supported"
	NatureHypothesis Nature = "hypothesis"
)

// SourceKind is the nature of the source an evidence points to.
type SourceKind string

// Source kinds.
const (
	SourceCode         SourceKind = "code"
	SourceDoc          SourceKind = "doc"
	SourceHumanContext SourceKind = "human-context"
)

// EvidenceState is computed by Raun when verifying evidence, never declared
// by an agent.
type EvidenceState string

// Evidence states.
const (
	EvidenceVerified  EvidenceState = "verified"
	EvidenceRelocated EvidenceState = "relocated"
	EvidenceInvalid   EvidenceState = "invalid"
	EvidenceStale     EvidenceState = "stale"
)

// PointKind is the kind of an open point.
type PointKind string

// Open point kinds.
const (
	PointDisagreement PointKind = "disagreement"
	PointUncertainty  PointKind = "uncertainty"
	PointQuestion     PointKind = "question"
)

// RequirementKind classifies a requirement.
type RequirementKind string

// Requirement kinds.
const (
	KindBusinessRule RequirementKind = "business-rule"
	KindFunctional   RequirementKind = "functional"
	KindConstraint   RequirementKind = "constraint"
)

// ActorKind identifies who changed a status.
type ActorKind string

// Actor kinds.
const (
	// ActorHuman is a person using a Raun command.
	ActorHuman ActorKind = "human"
	// ActorRun is an analysis run; it can never validate or reject.
	ActorRun ActorKind = "run"
)

// Item is one knowledge item, stored as one YAML file.
type Item struct {
	Version int    `yaml:"version"`
	ID      string `yaml:"id"`
	Type    Type   `yaml:"type"`
	Title   string `yaml:"title"`
	Status  Status `yaml:"status"`

	// Exactly one type-specific block is set, matching Type.
	Persona     *Persona     `yaml:"persona,omitempty"`
	Requirement *Requirement `yaml:"requirement,omitempty"`

	Support    []Support   `yaml:"support,omitempty"`
	Evidence   []Evidence  `yaml:"evidence,omitempty"`
	OpenPoints []OpenPoint `yaml:"open_points,omitempty"`
	History    []Event     `yaml:"history"`
}

// Persona describes a kind of user of the product.
type Persona struct {
	Description string   `yaml:"description"`
	Goals       []string `yaml:"goals,omitempty"`
	// Capabilities are what the persona can do in the product.
	Capabilities []string `yaml:"capabilities,omitempty"`
}

// Requirement describes a business requirement.
type Requirement struct {
	Statement string          `yaml:"statement"`
	Kind      RequirementKind `yaml:"kind"`
	Rationale string          `yaml:"rationale,omitempty"`
	// Personas holds the IDs of the personas concerned.
	Personas []string `yaml:"personas,omitempty"`
}

// Support is one agent interpretation backing the item, copied from the
// agent report so the justification chain survives without raw artifacts.
type Support struct {
	Run   string `yaml:"run"`
	Agent string `yaml:"agent"`
	// Interpretation is the interpretation ID inside the agent report.
	Interpretation string        `yaml:"interpretation"`
	Statement      string        `yaml:"statement"`
	Nature         Nature        `yaml:"nature"`
	Observations   []Observation `yaml:"observations,omitempty"`
}

// Observation is a factual statement backed by evidence.
type Observation struct {
	Statement string `yaml:"statement"`
	// Evidence holds IDs of entries in Item.Evidence.
	Evidence []string `yaml:"evidence"`
}

// Evidence anchors a statement to exact lines of a source at a commit.
type Evidence struct {
	ID        string        `yaml:"id"`
	Source    SourceKind    `yaml:"source"`
	Path      string        `yaml:"path"`
	Commit    string        `yaml:"commit"`
	StartLine int           `yaml:"start_line"`
	EndLine   int           `yaml:"end_line"`
	Excerpt   string        `yaml:"excerpt"`
	SHA256    string        `yaml:"sha256"`
	State     EvidenceState `yaml:"state"`
}

// OpenPoint is a disagreement, uncertainty or question attached to an item.
type OpenPoint struct {
	ID      string    `yaml:"id"`
	Kind    PointKind `yaml:"kind"`
	Summary string    `yaml:"summary"`
	// Positions are the divergent views of a disagreement, kept as-is.
	Positions  []Position  `yaml:"positions,omitempty"`
	Resolution *Resolution `yaml:"resolution,omitempty"`
}

// Position is one side of a disagreement.
type Position struct {
	Statement string   `yaml:"statement"`
	Run       string   `yaml:"run"`
	Agents    []string `yaml:"agents"`
	Evidence  []string `yaml:"evidence,omitempty"`
}

// Resolution records how a human closed an open point.
type Resolution struct {
	At time.Time `yaml:"at"`
	By string    `yaml:"by"`
	// Position is the index of the retained position, if any.
	Position *int   `yaml:"position,omitempty"`
	Note     string `yaml:"note"`
}

// Event records one status change.
type Event struct {
	At    time.Time `yaml:"at"`
	Actor ActorKind `yaml:"actor"`
	// By is a person's name for ActorHuman, a run ID for ActorRun.
	By     string `yaml:"by"`
	From   Status `yaml:"from,omitempty"`
	To     Status `yaml:"to"`
	Reason string `yaml:"reason,omitempty"`
}

// Unresolved reports whether the point has no resolution yet.
func (p OpenPoint) Unresolved() bool { return p.Resolution == nil }

// HasUnresolvedDisagreement reports whether any disagreement is still open.
func (it *Item) HasUnresolvedDisagreement() bool {
	return slices.ContainsFunc(it.OpenPoints, func(p OpenPoint) bool {
		return p.Kind == PointDisagreement && p.Unresolved()
	})
}

// HashExcerpt returns the hex SHA-256 of an evidence excerpt.
func HashExcerpt(excerpt string) string {
	sum := sha256.Sum256([]byte(excerpt))
	return hex.EncodeToString(sum[:])
}

var (
	localIDPattern = regexp.MustCompile(`^[a-z][0-9]+$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// FieldError reports an invalid value at a field path inside an item.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

// Validate checks the item's internal consistency. All problems are
// reported at once, joined, each as a *FieldError.
func (it *Item) Validate() error {
	v := &validator{}

	if it.Version != FormatVersion {
		v.add("version", "must be %d, got %d", FormatVersion, it.Version)
	}
	if !slices.Contains(Types, it.Type) {
		v.add("type", "unknown type %q", it.Type)
	} else if !ValidID(it.Type, it.ID) {
		v.add("id", "must look like %q, got %q", string(it.Type)+"-xxxxxx", it.ID)
	}
	if strings.TrimSpace(it.Title) == "" {
		v.add("title", "must not be empty")
	}
	if !slices.Contains(Statuses, it.Status) {
		v.add("status", "unknown status %q", it.Status)
	}

	it.validateContent(v)

	evidenceIDs := map[string]bool{}
	for i, e := range it.Evidence {
		f := fmt.Sprintf("evidence[%d]", i)
		v.localID(f+".id", e.ID, evidenceIDs)
		e.validate(v, f)
	}
	refs := func(field string, ids []string) {
		for j, id := range ids {
			if !evidenceIDs[id] {
				v.add(fmt.Sprintf("%s[%d]", field, j), "unknown evidence %q", id)
			}
		}
	}

	for i, s := range it.Support {
		f := fmt.Sprintf("support[%d]", i)
		v.nonEmpty(f+".run", s.Run)
		v.nonEmpty(f+".agent", s.Agent)
		v.nonEmpty(f+".interpretation", s.Interpretation)
		v.nonEmpty(f+".statement", s.Statement)
		if s.Nature != NatureSupported && s.Nature != NatureHypothesis {
			v.add(f+".nature", "must be %q or %q, got %q", NatureSupported, NatureHypothesis, s.Nature)
		}
		if s.Nature == NatureSupported && len(s.Observations) == 0 {
			v.add(f+".observations", "a supported interpretation needs at least one observation")
		}
		for j, o := range s.Observations {
			of := fmt.Sprintf("%s.observations[%d]", f, j)
			v.nonEmpty(of+".statement", o.Statement)
			if len(o.Evidence) == 0 {
				v.add(of+".evidence", "an observation needs at least one evidence")
			}
			refs(of+".evidence", o.Evidence)
		}
	}

	pointIDs := map[string]bool{}
	for i, p := range it.OpenPoints {
		f := fmt.Sprintf("open_points[%d]", i)
		v.localID(f+".id", p.ID, pointIDs)
		if !slices.Contains([]PointKind{PointDisagreement, PointUncertainty, PointQuestion}, p.Kind) {
			v.add(f+".kind", "unknown kind %q", p.Kind)
		}
		v.nonEmpty(f+".summary", p.Summary)
		if p.Kind == PointDisagreement && len(p.Positions) < 2 {
			v.add(f+".positions", "a disagreement needs at least two positions")
		}
		for j, pos := range p.Positions {
			pf := fmt.Sprintf("%s.positions[%d]", f, j)
			v.nonEmpty(pf+".statement", pos.Statement)
			v.nonEmpty(pf+".run", pos.Run)
			if len(pos.Agents) == 0 {
				v.add(pf+".agents", "must name at least one agent")
			}
			refs(pf+".evidence", pos.Evidence)
		}
		if r := p.Resolution; r != nil {
			v.nonEmpty(f+".resolution.by", r.By)
			v.nonEmpty(f+".resolution.note", r.Note)
			if r.At.IsZero() {
				v.add(f+".resolution.at", "must be set")
			}
			if r.Position != nil && (*r.Position < 0 || *r.Position >= len(p.Positions)) {
				v.add(f+".resolution.position", "must index one of the %d positions", len(p.Positions))
			}
		}
	}
	if it.Status == StatusValidated && it.HasUnresolvedDisagreement() {
		v.add("status", "a validated item cannot have an unresolved disagreement")
	}

	if len(it.History) == 0 {
		v.add("history", "must record at least the creation event")
	}
	for i, e := range it.History {
		e.validate(v, fmt.Sprintf("history[%d]", i))
	}
	if n := len(it.History); n > 0 && it.History[n-1].To != it.Status {
		v.add("status", "must equal the last history event (%q), got %q", it.History[n-1].To, it.Status)
	}

	return errors.Join(v.errs...)
}

func (it *Item) validateContent(v *validator) {
	if (it.Persona != nil) != (it.Type == TypePersona) {
		v.add("persona", "must be set if and only if type is %q", TypePersona)
	}
	if (it.Requirement != nil) != (it.Type == TypeRequirement) {
		v.add("requirement", "must be set if and only if type is %q", TypeRequirement)
	}

	if p := it.Persona; p != nil {
		v.nonEmpty("persona.description", p.Description)
		for i, g := range p.Goals {
			v.nonEmpty(fmt.Sprintf("persona.goals[%d]", i), g)
		}
		for i, c := range p.Capabilities {
			v.nonEmpty(fmt.Sprintf("persona.capabilities[%d]", i), c)
		}
	}
	if r := it.Requirement; r != nil {
		v.nonEmpty("requirement.statement", r.Statement)
		if !slices.Contains([]RequirementKind{KindBusinessRule, KindFunctional, KindConstraint}, r.Kind) {
			v.add("requirement.kind", "must be %q, %q or %q, got %q", KindBusinessRule, KindFunctional, KindConstraint, r.Kind)
		}
		for i, id := range r.Personas {
			if !ValidID(TypePersona, id) {
				v.add(fmt.Sprintf("requirement.personas[%d]", i), "not a persona ID: %q", id)
			}
		}
	}
}

func (e Evidence) validate(v *validator, f string) {
	if !slices.Contains([]SourceKind{SourceCode, SourceDoc, SourceHumanContext}, e.Source) {
		v.add(f+".source", "unknown source kind %q", e.Source)
	}
	switch clean := path.Clean(e.Path); {
	case e.Path == "":
		v.add(f+".path", "must not be empty")
	case path.IsAbs(e.Path) || clean == ".." || strings.HasPrefix(clean, "../") || clean != e.Path:
		v.add(f+".path", "must be a clean path relative to the repository root, got %q", e.Path)
	}
	if !commitPattern.MatchString(e.Commit) {
		v.add(f+".commit", "must be a full commit hash, got %q", e.Commit)
	}
	if e.StartLine < 1 || e.EndLine < e.StartLine {
		v.add(f+".start_line", "invalid line range %d-%d", e.StartLine, e.EndLine)
	}
	if e.Excerpt == "" {
		v.add(f+".excerpt", "must not be empty")
	}
	switch {
	case !sha256Pattern.MatchString(e.SHA256):
		v.add(f+".sha256", "must be a hex SHA-256")
	case e.SHA256 != HashExcerpt(e.Excerpt):
		v.add(f+".sha256", "does not match the excerpt")
	}
	if !slices.Contains([]EvidenceState{EvidenceVerified, EvidenceRelocated, EvidenceInvalid, EvidenceStale}, e.State) {
		v.add(f+".state", "unknown state %q", e.State)
	}
}

func (e Event) validate(v *validator, f string) {
	if e.At.IsZero() {
		v.add(f+".at", "must be set")
	}
	if e.Actor != ActorHuman && e.Actor != ActorRun {
		v.add(f+".actor", "must be %q or %q, got %q", ActorHuman, ActorRun, e.Actor)
	}
	v.nonEmpty(f+".by", e.By)
	if e.From != "" && !slices.Contains(Statuses, e.From) {
		v.add(f+".from", "unknown status %q", e.From)
	}
	if !slices.Contains(Statuses, e.To) {
		v.add(f+".to", "unknown status %q", e.To)
	}
}

type validator struct{ errs []error }

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) nonEmpty(field, s string) {
	if strings.TrimSpace(s) == "" {
		v.add(field, "must not be empty")
	}
}

func (v *validator) localID(field, id string, seen map[string]bool) {
	switch {
	case !localIDPattern.MatchString(id):
		v.add(field, "must look like \"e1\" or \"p1\", got %q", id)
	case seen[id]:
		v.add(field, "duplicate id %q", id)
	}
	seen[id] = true
}
