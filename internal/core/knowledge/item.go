package knowledge

import "time"

// Type is the type of a knowledge item.
type Type string

// Item types.
const (
	TypePersona     Type = "persona"
	TypeRequirement Type = "requirement"
)

// Valid reports whether t is a known item type.
func (t Type) Valid() bool {
	switch t {
	case TypePersona, TypeRequirement:
		return true
	}
	return false
}

// Kind is the kind of a requirement.
type Kind string

// Requirement kinds.
const (
	KindBusinessRule Kind = "business-rule"
	KindFunctional   Kind = "functional"
	KindConstraint   Kind = "constraint"
)

// Valid reports whether k is a known requirement kind.
func (k Kind) Valid() bool {
	switch k {
	case KindBusinessRule, KindFunctional, KindConstraint:
		return true
	}
	return false
}

// Status is the status of a knowledge item.
type Status string

// Item statuses.
const (
	StatusProposed    Status = "proposed"
	StatusContested   Status = "contested"
	StatusValidated   Status = "validated"
	StatusRejected    Status = "rejected"
	StatusNeedsReview Status = "needs-review"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusProposed, StatusContested, StatusValidated, StatusRejected, StatusNeedsReview:
		return true
	}
	return false
}

// Nature tells whether a support entry rests on observations.
type Nature string

// Support natures.
const (
	NatureSupported  Nature = "supported"
	NatureHypothesis Nature = "hypothesis"
)

// Valid reports whether n is a known nature.
func (n Nature) Valid() bool {
	switch n {
	case NatureSupported, NatureHypothesis:
		return true
	}
	return false
}

// Source is the kind of file an evidence cites.
type Source string

// Evidence sources.
const (
	SourceCode         Source = "code"
	SourceDoc          Source = "doc"
	SourceHumanContext Source = "human-context"
)

// Valid reports whether s is a known evidence source.
func (s Source) Valid() bool {
	switch s {
	case SourceCode, SourceDoc, SourceHumanContext:
		return true
	}
	return false
}

// EvidenceState is the state of an evidence, set by Raun.
type EvidenceState string

// Evidence states.
const (
	EvidenceVerified  EvidenceState = "verified"
	EvidenceRelocated EvidenceState = "relocated"
	EvidenceInvalid   EvidenceState = "invalid"
	EvidenceStale     EvidenceState = "stale"
)

// Valid reports whether s is a known evidence state.
func (s EvidenceState) Valid() bool {
	switch s {
	case EvidenceVerified, EvidenceRelocated, EvidenceInvalid, EvidenceStale:
		return true
	}
	return false
}

// Reason says why an evidence is invalid or stale.
type Reason string

// Evidence reasons.
const (
	ReasonNotFound    Reason = "not-found"
	ReasonAmbiguous   Reason = "ambiguous"
	ReasonMissingFile Reason = "missing-file"
	ReasonNotRegular  Reason = "not-regular"
	ReasonBinary      Reason = "binary"
	ReasonTooLong     Reason = "too-long"
	ReasonBadRange    Reason = "bad-range"
)

// Valid reports whether r is a known reason.
func (r Reason) Valid() bool {
	switch r {
	case ReasonNotFound, ReasonAmbiguous, ReasonMissingFile, ReasonNotRegular,
		ReasonBinary, ReasonTooLong, ReasonBadRange:
		return true
	}
	return false
}

// allows reports whether the reason may go with the state: too-long and
// bad-range describe a citation, so they apply at the cited commit only.
func (r Reason) allows(s EvidenceState) bool {
	switch s {
	case EvidenceInvalid:
		return r.Valid()
	case EvidenceStale:
		return r.Valid() && r != ReasonTooLong && r != ReasonBadRange
	}
	return false
}

// PointKind is the kind of an open point.
type PointKind string

// Open point kinds.
const (
	PointDisagreement PointKind = "disagreement"
	PointUncertainty  PointKind = "uncertainty"
	PointQuestion     PointKind = "question"
)

// Valid reports whether k is a known open point kind.
func (k PointKind) Valid() bool {
	switch k {
	case PointDisagreement, PointUncertainty, PointQuestion:
		return true
	}
	return false
}

// ActorKind tells who caused a status change.
type ActorKind string

// Actor kinds.
const (
	ActorHuman ActorKind = "human"
	ActorRun   ActorKind = "run"
)

// Valid reports whether k is a known actor kind.
func (k ActorKind) Valid() bool {
	switch k {
	case ActorHuman, ActorRun:
		return true
	}
	return false
}

// Item is a knowledge item. See docs/format.md for the meaning of each field.
type Item struct {
	ID          string       `json:"id"`
	Type        Type         `json:"type"`
	Title       string       `json:"title"`
	Status      Status       `json:"status"`
	Revision    int64        `json:"revision"`
	Persona     *Persona     `json:"persona,omitempty"`
	Requirement *Requirement `json:"requirement,omitempty"`
	Support     []Support    `json:"support,omitempty"`
	Evidence    []Evidence   `json:"evidence,omitempty"`
	OpenPoints  []OpenPoint  `json:"open_points,omitempty"`
	History     []Event      `json:"history"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// Persona is the content of an item of type persona.
type Persona struct {
	Description  string   `json:"description"`
	Goals        []string `json:"goals,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Requirement is the content of an item of type requirement.
type Requirement struct {
	Statement string   `json:"statement"`
	Kind      Kind     `json:"kind,omitempty"`
	Rationale string   `json:"rationale,omitempty"`
	Personas  []string `json:"personas,omitempty"`
}

// Support is an agent interpretation backing an item.
type Support struct {
	Run            string        `json:"run"`
	Agent          string        `json:"agent"`
	Interpretation string        `json:"interpretation"`
	Statement      string        `json:"statement"`
	Nature         Nature        `json:"nature"`
	Observations   []Observation `json:"observations,omitempty"`
}

// Observation is a statement grounded in evidence of the same item.
type Observation struct {
	Statement string   `json:"statement"`
	Evidence  []string `json:"evidence"`
}

// Evidence is a citation: lines of a file at a commit.
type Evidence struct {
	ID            string        `json:"id"`
	Source        Source        `json:"source"`
	Path          string        `json:"path"`
	Commit        string        `json:"commit"`
	StartLine     int           `json:"start_line"`
	EndLine       int           `json:"end_line"`
	Excerpt       string        `json:"excerpt"`
	SHA256        string        `json:"sha256"`
	State         EvidenceState `json:"state"`
	Reason        Reason        `json:"reason,omitempty"`
	CheckedAt     time.Time     `json:"checked_at,omitzero"`
	CheckedCommit string        `json:"checked_commit,omitempty"`
}

// OpenPoint is a disagreement, an uncertainty or a question about an item.
type OpenPoint struct {
	ID         string      `json:"id"`
	Kind       PointKind   `json:"kind"`
	Summary    string      `json:"summary"`
	RaisedBy   string      `json:"raised_by,omitempty"`
	Positions  []Position  `json:"positions,omitempty"`
	Resolution *Resolution `json:"resolution,omitempty"`
}

// Unresolved reports whether p is a disagreement nobody has resolved yet.
// It is the single definition of an unresolved disagreement.
func (p OpenPoint) Unresolved() bool {
	return p.Kind == PointDisagreement && p.Resolution == nil
}

// Position is one side of a disagreement, kept as written.
type Position struct {
	Statement string   `json:"statement"`
	Run       string   `json:"run"`
	Agents    []string `json:"agents,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
}

// Resolution is a human decision on an open point.
type Resolution struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
	// Position is the 0-based index of the retained position, or nil when
	// none is retained.
	Position *int   `json:"position,omitempty"`
	Note     string `json:"note"`
}

// Event is one status change in the history of an item.
type Event struct {
	At    time.Time `json:"at"`
	Actor ActorKind `json:"actor"`
	By    string    `json:"by"`
	// From is empty for the creation event.
	From   Status `json:"from,omitempty"`
	To     Status `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// HasUnresolvedDisagreement reports whether the item holds a disagreement
// nobody has resolved yet.
func (it Item) HasUnresolvedDisagreement() bool {
	for _, p := range it.OpenPoints {
		if p.Unresolved() {
			return true
		}
	}
	return false
}

// clone returns a deep copy of the item, so that an operation can change the
// copy without touching the slices and pointers the caller still holds.
func (it Item) clone() Item {
	c := it
	if it.Persona != nil {
		p := *it.Persona
		p.Goals = cloneStrings(p.Goals)
		p.Capabilities = cloneStrings(p.Capabilities)
		c.Persona = &p
	}
	if it.Requirement != nil {
		r := *it.Requirement
		r.Personas = cloneStrings(r.Personas)
		c.Requirement = &r
	}
	c.Support = cloneSupport(it.Support)
	c.Evidence = cloneSlice(it.Evidence)
	c.OpenPoints = cloneOpenPoints(it.OpenPoints)
	c.History = cloneSlice(it.History)
	return c
}

func cloneSupport(in []Support) []Support {
	if in == nil {
		return nil
	}
	out := make([]Support, len(in))
	for i, s := range in {
		s.Observations = cloneSlice(s.Observations)
		for j := range s.Observations {
			s.Observations[j].Evidence = cloneStrings(s.Observations[j].Evidence)
		}
		out[i] = s
	}
	return out
}

func cloneOpenPoints(in []OpenPoint) []OpenPoint {
	if in == nil {
		return nil
	}
	out := make([]OpenPoint, len(in))
	for i, p := range in {
		p.Positions = cloneSlice(p.Positions)
		for j := range p.Positions {
			p.Positions[j].Agents = cloneStrings(p.Positions[j].Agents)
			p.Positions[j].Evidence = cloneStrings(p.Positions[j].Evidence)
		}
		if p.Resolution != nil {
			r := *p.Resolution
			if r.Position != nil {
				idx := *r.Position
				r.Position = &idx
			}
			p.Resolution = &r
		}
		out[i] = p
	}
	return out
}

func cloneStrings(in []string) []string { return cloneSlice(in) }

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	return append(make([]T, 0, len(in)), in...)
}
