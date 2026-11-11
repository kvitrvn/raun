package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// More errors of the operations on an item.
var (
	// ErrNothingToAppend: a run appended no support, evidence or open point.
	ErrNothingToAppend = errors.New("nothing to append")
	// ErrForeignRun: an appended support entry names another run than the one
	// appending it, or a position names a run unknown to the item.
	ErrForeignRun = errors.New("element of another run")
	// ErrResolvedByRun: a run tried to append an open point already resolved;
	// only a human resolves.
	ErrResolvedByRun = errors.New("open point resolved by a run")
	// ErrUnknownOpenPoint: no open point has this identifier.
	ErrUnknownOpenPoint = errors.New("unknown open point")
	// ErrAlreadyResolved: the open point already has a resolution.
	ErrAlreadyResolved = errors.New("open point already resolved")
	// ErrNoteRequired: a resolution needs a note.
	ErrNoteRequired = errors.New("note required")
	// ErrNoStaleEvidence: an item moves to needs-review on stale evidence only.
	ErrNoStaleEvidence = errors.New("no stale evidence")
)

// ReasonDisagreementsResolved is the reason of the event that moves a
// contested item back to proposed once its last disagreement is resolved.
const ReasonDisagreementsResolved = "disagreements resolved"

// Draft is the content of an item a run is about to create. Support, evidence
// and open points are added afterwards with AppendFromRun.
type Draft struct {
	ID          string
	Type        Type
	Title       string
	Persona     *Persona
	Requirement *Requirement
}

// New creates an item found by the run identified by run, at revision 1 and
// in status proposed, with its creation event. The reason of the creation
// event is optional.
func New(d Draft, run, reason string, at time.Time) (Item, error) {
	if strings.TrimSpace(run) == "" {
		return Item{}, fmt.Errorf("new item: %w", ErrActorRequired)
	}
	if at.IsZero() || at.Location() != time.UTC {
		return Item{}, fmt.Errorf("new item: %w", ErrTimeRequired)
	}
	it := Item{
		ID:          d.ID,
		Type:        d.Type,
		Title:       d.Title,
		Status:      StatusProposed,
		Revision:    1,
		Persona:     d.Persona,
		Requirement: d.Requirement,
		History: []Event{{
			At:     at,
			Actor:  ActorRun,
			By:     run,
			To:     StatusProposed,
			Reason: reason,
		}},
		CreatedAt: at,
		UpdatedAt: at,
	}
	// The draft's pointers belong to the caller: keep a copy of their content.
	it = it.clone()
	if err := it.Validate(); err != nil {
		return Item{}, err
	}
	return it, nil
}

// RunAdditions is what a run appends to an existing item.
type RunAdditions struct {
	// Run identifies the run. Every appended support entry must name it. A
	// position names it, or an earlier run of the item whose content the
	// position stands for.
	Run string
	// Reason says why the ground of the item changed. It is required when the
	// additions change the status.
	Reason string
	At     time.Time

	Support    []Support
	Evidence   []Evidence
	OpenPoints []OpenPoint
}

// AppendFromRun appends support, evidence and open points found by a run, in
// any status. It never changes an existing element. New evidence and open
// points carry identifiers the item does not use yet (see FreeEvidenceIDs and
// FreeOpenPointIDs), and may refer to existing evidence.
//
// A new unresolved disagreement makes a proposed item contested and a
// validated item needs-review; on a contested, needs-review or rejected item
// it changes no status.
func AppendFromRun(it Item, add RunAdditions) (Item, error) {
	if strings.TrimSpace(add.Run) == "" {
		return Item{}, fmt.Errorf("append to %s: %w", it.ID, ErrActorRequired)
	}
	if err := checkTime(it, add.At); err != nil {
		return Item{}, fmt.Errorf("append to %s: %w", it.ID, err)
	}
	if len(add.Support) == 0 && len(add.Evidence) == 0 && len(add.OpenPoints) == 0 {
		return Item{}, fmt.Errorf("append to %s: %w", it.ID, ErrNothingToAppend)
	}

	disagreement := false
	for i, s := range add.Support {
		if s.Run != add.Run {
			return Item{}, fmt.Errorf("append to %s: support[%d] names run %q: %w", it.ID, i, s.Run, ErrForeignRun)
		}
	}
	// A position keeps the run that wrote it: a conflict with existing content
	// sets a position of an earlier run of the item against one of this run.
	runs := it.runs()
	runs[add.Run] = true
	for i, p := range add.OpenPoints {
		if p.Resolution != nil {
			return Item{}, fmt.Errorf("append to %s: open_points[%d]: %w", it.ID, i, ErrResolvedByRun)
		}
		for j, pos := range p.Positions {
			if !runs[pos.Run] {
				return Item{}, fmt.Errorf("append to %s: open_points[%d].positions[%d] names run %q, unknown to the item: %w", it.ID, i, j, pos.Run, ErrForeignRun)
			}
		}
		if p.Unresolved() {
			disagreement = true
		}
	}

	c := it.clone()
	batch := Item{Support: add.Support, Evidence: add.Evidence, OpenPoints: add.OpenPoints}.clone()
	c.Support = append(c.Support, batch.Support...)
	c.Evidence = append(c.Evidence, batch.Evidence...)
	c.OpenPoints = append(c.OpenPoints, batch.OpenPoints...)
	c.UpdatedAt = add.At

	if disagreement {
		run := Actor{Kind: ActorRun, By: add.Run}
		var err error
		switch c.Status {
		case StatusProposed:
			err = transition(&c, StatusContested, run, add.Reason, add.At)
		case StatusValidated:
			err = transition(&c, StatusNeedsReview, run, add.Reason, add.At)
		}
		if err != nil {
			return Item{}, fmt.Errorf("append to %s: %w", it.ID, err)
		}
	}

	if err := c.Validate(); err != nil {
		return Item{}, err
	}
	return c, nil
}

// MarkNeedsReview moves a validated item to needs-review because one of its
// evidence became stale. Computing that an evidence is stale is the job of
// evidence verification; this only records the consequence.
func MarkNeedsReview(it Item, run, reason string, at time.Time) (Item, error) {
	stale := false
	for _, e := range it.Evidence {
		if e.State == EvidenceStale {
			stale = true
			break
		}
	}
	if !stale {
		return Item{}, fmt.Errorf("mark %s for review: %w", it.ID, ErrNoStaleEvidence)
	}
	c := it.clone()
	if err := transition(&c, StatusNeedsReview, Actor{Kind: ActorRun, By: run}, reason, at); err != nil {
		return Item{}, fmt.Errorf("mark %s for review: %w", it.ID, err)
	}
	if err := c.Validate(); err != nil {
		return Item{}, err
	}
	return c, nil
}

// Decide records the human decision to validate or reject an item. Reopening
// a rejected item is Reopen, and a contested item becomes proposed only by
// resolving its last disagreement.
func Decide(it Item, to Status, by, reason string, at time.Time) (Item, error) {
	if to != StatusValidated && to != StatusRejected {
		return Item{}, fmt.Errorf("decide on %s: %w: a decision validates or rejects, got %s", it.ID, ErrTransitionNotAllowed, to)
	}
	return humanTransition(it, to, by, reason, at)
}

// Reopen records the human decision to reopen a rejected item. It becomes
// contested if it holds an unresolved disagreement, proposed otherwise.
func Reopen(it Item, by, reason string, at time.Time) (Item, error) {
	if it.Status != StatusRejected {
		return Item{}, fmt.Errorf("reopen %s: %w: only a rejected item is reopened, got %s", it.ID, ErrTransitionNotAllowed, it.Status)
	}
	to := StatusProposed
	if it.HasUnresolvedDisagreement() {
		to = StatusContested
	}
	return humanTransition(it, to, by, reason, at)
}

func humanTransition(it Item, to Status, by, reason string, at time.Time) (Item, error) {
	c := it.clone()
	if err := transition(&c, to, Actor{Kind: ActorHuman, By: by}, reason, at); err != nil {
		return Item{}, fmt.Errorf("decide on %s: %w", it.ID, err)
	}
	if err := c.Validate(); err != nil {
		return Item{}, err
	}
	return c, nil
}

// ResolveOpenPoint records the human resolution of an open point. It changes
// nothing else of the item, except that resolving the last disagreement of a
// contested item makes it proposed, with an event that takes the time and the
// author of the resolution.
func ResolveOpenPoint(it Item, pointID string, res Resolution) (Item, error) {
	if strings.TrimSpace(res.By) == "" {
		return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, ErrActorRequired)
	}
	if strings.TrimSpace(res.Note) == "" {
		return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, ErrNoteRequired)
	}
	if err := checkTime(it, res.At); err != nil {
		return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, err)
	}

	c := it.clone()
	idx := -1
	for i, p := range c.OpenPoints {
		if p.ID == pointID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, ErrUnknownOpenPoint)
	}
	if c.OpenPoints[idx].Resolution != nil {
		return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, ErrAlreadyResolved)
	}

	r := res
	if res.Position != nil {
		pos := *res.Position
		r.Position = &pos
	}
	c.OpenPoints[idx].Resolution = &r
	c.UpdatedAt = res.At

	if c.Status == StatusContested && !c.HasUnresolvedDisagreement() {
		human := Actor{Kind: ActorHuman, By: res.By}
		if err := transition(&c, StatusProposed, human, ReasonDisagreementsResolved, res.At); err != nil {
			return Item{}, fmt.Errorf("resolve %s of %s: %w", pointID, it.ID, err)
		}
	}

	if err := c.Validate(); err != nil {
		return Item{}, err
	}
	return c, nil
}
