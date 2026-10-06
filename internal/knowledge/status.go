package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// transitions lists every allowed status change and the only actor kind
// allowed to perform it. Validating and rejecting are human-only.
var transitions = map[Status]map[Status]ActorKind{
	StatusProposed: {
		StatusContested: ActorRun,
		StatusValidated: ActorHuman,
		StatusRejected:  ActorHuman,
	},
	StatusContested: {
		StatusProposed:  ActorHuman,
		StatusValidated: ActorHuman,
		StatusRejected:  ActorHuman,
	},
	StatusValidated: {
		StatusNeedsReview: ActorRun,
		StatusRejected:    ActorHuman,
	},
	StatusNeedsReview: {
		StatusValidated: ActorHuman,
		StatusRejected:  ActorHuman,
	},
	StatusRejected: {
		StatusProposed: ActorHuman,
	},
}

// ErrTransition reports a status change that is not allowed.
var ErrTransition = errors.New("status transition not allowed")

// Actor identifies who performs a change: a person (ActorHuman, by name) or
// a run (ActorRun, by run ID).
type Actor struct {
	Kind ActorKind
	Name string
}

// Transition moves the item to status `to` and records the change in its
// history. Human changes require a reason. Validating an item, or moving it
// back from contested to proposed, requires every disagreement to be
// resolved first; rejecting never does.
func (it *Item) Transition(to Status, by Actor, at time.Time, reason string) error {
	allowed, ok := transitions[it.Status][to]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, it.Status, to)
	}
	if by.Kind != allowed {
		return fmt.Errorf("%w: %s -> %s can only be done by a %s", ErrTransition, it.Status, to, allowed)
	}
	if strings.TrimSpace(by.Name) == "" {
		return fmt.Errorf("%w: actor name is required", ErrTransition)
	}
	if by.Kind == ActorHuman && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a human decision needs a reason", ErrTransition)
	}
	if (to == StatusValidated || it.Status == StatusContested && to == StatusProposed) && it.HasUnresolvedDisagreement() {
		return fmt.Errorf("%w: %s -> %s: resolve the open disagreements first", ErrTransition, it.Status, to)
	}

	it.History = append(it.History, Event{
		At:     at.UTC().Truncate(time.Second),
		Actor:  by.Kind,
		By:     by.Name,
		From:   it.Status,
		To:     to,
		Reason: reason,
	})
	it.Status = to
	return nil
}
