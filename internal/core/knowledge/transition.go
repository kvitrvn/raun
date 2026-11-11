package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors of the operations on an item. They are wrapped with the details of
// the refused call; test them with errors.Is.
var (
	// ErrTransitionNotAllowed: the status change is not in the table of
	// docs/format.md, or not for this kind of actor.
	ErrTransitionNotAllowed = errors.New("transition not allowed")
	// ErrUnresolvedDisagreement: a disagreement must be resolved first.
	ErrUnresolvedDisagreement = errors.New("unresolved disagreement")
	// ErrReasonRequired: the status change needs a reason.
	ErrReasonRequired = errors.New("reason required")
	// ErrActorRequired: the actor of the change is not identified.
	ErrActorRequired = errors.New("actor required")
	// ErrTimeRequired: the time of the change is zero or not in UTC.
	ErrTimeRequired = errors.New("time required in UTC")
)

// transitionKey is one row of the transition table. From is empty for the
// creation of an item.
type transitionKey struct {
	from, to Status
}

// transitions is the table of docs/format.md: who may move an item from a
// status to another. A pair missing from it is refused.
var transitions = map[transitionKey]ActorKind{
	{"", StatusProposed}:                 ActorRun,
	{StatusProposed, StatusContested}:    ActorRun,
	{StatusProposed, StatusValidated}:    ActorHuman,
	{StatusProposed, StatusRejected}:     ActorHuman,
	{StatusContested, StatusProposed}:    ActorHuman,
	{StatusContested, StatusRejected}:    ActorHuman,
	{StatusValidated, StatusNeedsReview}: ActorRun,
	{StatusValidated, StatusRejected}:    ActorHuman,
	{StatusNeedsReview, StatusValidated}: ActorHuman,
	{StatusNeedsReview, StatusRejected}:  ActorHuman,
	{StatusRejected, StatusProposed}:     ActorHuman,
	{StatusRejected, StatusContested}:    ActorHuman,
}

// allowedTransition returns the kind of actor that may move an item from one
// status to another, and whether the move exists at all.
func allowedTransition(from, to Status) (ActorKind, bool) {
	actor, ok := transitions[transitionKey{from, to}]
	return actor, ok
}

// Actor is who causes a status change: a human identified by an opaque
// account identifier, or a run identified by its run identifier.
type Actor struct {
	Kind ActorKind
	By   string
}

// transition moves the item, a copy the caller owns, to the status to and
// appends one history event. It checks the table, the actor, the reason, the
// time and the rules on disagreements. It never touches the revision.
func transition(it *Item, to Status, actor Actor, reason string, at time.Time) error {
	from := it.Status
	allowed, ok := allowedTransition(from, to)
	if !ok || allowed != actor.Kind {
		return fmt.Errorf("%w: from %s to %s by a %s", ErrTransitionNotAllowed, from, to, actor.Kind)
	}
	if strings.TrimSpace(actor.By) == "" {
		return fmt.Errorf("%w: from %s to %s", ErrActorRequired, from, to)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: from %s to %s", ErrReasonRequired, from, to)
	}
	if at.IsZero() || at.Location() != time.UTC {
		return fmt.Errorf("%w: from %s to %s", ErrTimeRequired, from, to)
	}

	unresolved := it.HasUnresolvedDisagreement()
	switch {
	case to == StatusValidated && unresolved:
		return fmt.Errorf("%w: cannot validate %s", ErrUnresolvedDisagreement, it.ID)
	case from == StatusContested && to == StatusProposed && unresolved:
		return fmt.Errorf("%w: %s stays contested", ErrUnresolvedDisagreement, it.ID)
	case from == StatusRejected && to == StatusProposed && unresolved:
		return fmt.Errorf("%w: reopening %s makes it %s", ErrUnresolvedDisagreement, it.ID, StatusContested)
	case from == StatusRejected && to == StatusContested && !unresolved:
		return fmt.Errorf("%w: reopening %s without an unresolved disagreement makes it %s", ErrTransitionNotAllowed, it.ID, StatusProposed)
	}

	it.History = append(it.History, Event{
		At:     at,
		Actor:  actor.Kind,
		By:     actor.By,
		From:   from,
		To:     to,
		Reason: reason,
	})
	it.Status = to
	it.UpdatedAt = at
	return nil
}
