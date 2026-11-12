package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors of a Store. They are wrapped with the details of the operation:
// test them with errors.Is.
var (
	// ErrNotFound: no item has this identifier in the scope.
	ErrNotFound = errors.New("item not found")
	// ErrIDTaken: an item already has this identifier in the scope. The
	// caller draws another identifier.
	ErrIDTaken = errors.New("item identifier taken")
	// ErrStaleRevision: the stored revision is not the one the caller read.
	ErrStaleRevision = errors.New("stale revision")
	// ErrUnknownReference: a requirement names a persona that does not exist
	// in the scope.
	ErrUnknownReference = errors.New("unknown reference")
	// ErrHistoryChanged: an update changes or removes an event of the
	// history, which is append-only.
	ErrHistoryChanged = errors.New("history changed")
)

// Scope is the repository of a workspace that every store operation works in.
// Repository identifiers are not assumed unique across workspaces, so both are
// needed.
type Scope struct {
	Workspace  string
	Repository string
}

// Validate reports an empty identifier of the scope.
func (s Scope) Validate() error {
	switch {
	case strings.TrimSpace(s.Workspace) == "":
		return errors.New("scope: workspace is required")
	case strings.TrimSpace(s.Repository) == "":
		return errors.New("scope: repository is required")
	}
	return nil
}

// Filter selects items in List. A zero field matches any value.
type Filter struct {
	Type   Type
	Status Status
}

// Validate reports a field that holds an unknown value.
func (f Filter) Validate() error {
	if f.Type != "" && !f.Type.Valid() {
		return fmt.Errorf("filter: unknown type %q", f.Type)
	}
	if f.Status != "" && !f.Status.Valid() {
		return fmt.Errorf("filter: unknown status %q", f.Status)
	}
	return nil
}

// Matches reports whether it is selected by the filter.
func (f Filter) Matches(it Item) bool {
	return (f.Type == "" || it.Type == f.Type) && (f.Status == "" || it.Status == f.Status)
}

// Mutation turns the stored item into its next version, usually through one of
// the operations of this package (AppendFromRun, Decide, Reopen,
// ResolveOpenPoint, MarkNeedsReview). It gets a copy and may return it
// changed. An error it returns cancels the update and comes back from Update
// as it is.
type Mutation func(Item) (Item, error)

// Store keeps the knowledge items of every repository. There is no operation
// to replace an item without a revision check, and none to delete one: an
// item is rejected, never deleted.
//
// Every operation takes a scope and only ever sees the items of that scope.
// Items handed to and returned by a store are copies: changing them changes
// nothing stored.
type Store interface {
	// List returns the items of the scope that match the filter, in order of
	// their identifiers.
	List(ctx context.Context, scope Scope, filter Filter) ([]Item, error)

	// Get returns the item, or ErrNotFound.
	Get(ctx context.Context, scope Scope, id string) (Item, error)

	// Create stores a new item, which CheckCreate must accept: revision 1 and
	// its history as New makes it. ErrIDTaken if the identifier exists in the
	// scope, ErrUnknownReference if a persona it names does not.
	Create(ctx context.Context, scope Scope, it Item) error

	// Update runs mutate on the stored item inside the write transaction and
	// stores the result at revision + 1 with its new events, then returns it.
	// The revision is the one the caller read: ErrStaleRevision if the stored
	// one differs, in which case mutate is not called. ErrNotFound,
	// ErrUnknownReference, ErrHistoryChanged or a validation error refuse the
	// result. Nothing is written when an error is returned.
	//
	// mutate runs once, while the store holds its write lock: it must be
	// short and must not call the store.
	Update(ctx context.Context, scope Scope, id string, revision int64, mutate Mutation) (Item, error)
}

// CheckCreate refuses an item a store may not create: an invalid one, or one
// whose revision is not 1. Every store calls it.
func CheckCreate(it Item) error {
	if it.Revision != 1 {
		return fmt.Errorf("create %s: revision must be 1, got %d", it.ID, it.Revision)
	}
	return it.Validate()
}

// CheckUpdate refuses next as the update of stored: a change of identifier,
// type or creation time, an event of the stored history changed or removed
// (ErrHistoryChanged), an update time before the stored one, or an invalid
// item. The revision is not checked: the store sets it. Every store calls it.
func CheckUpdate(stored, next Item) error {
	switch {
	case next.ID != stored.ID:
		return fmt.Errorf("update %s: identifier changed to %q", stored.ID, next.ID)
	case next.Type != stored.Type:
		return fmt.Errorf("update %s: type changed to %q", stored.ID, next.Type)
	case !next.CreatedAt.Equal(stored.CreatedAt):
		return fmt.Errorf("update %s: creation time changed", stored.ID)
	case next.UpdatedAt.Before(stored.UpdatedAt):
		return fmt.Errorf("update %s: update time before the stored one", stored.ID)
	}
	if len(next.History) < len(stored.History) {
		return fmt.Errorf("update %s: %w: %d events removed", stored.ID, ErrHistoryChanged, len(stored.History)-len(next.History))
	}
	for i, e := range stored.History {
		if !sameEvent(e, next.History[i]) {
			return fmt.Errorf("update %s: %w: event %d", stored.ID, ErrHistoryChanged, i)
		}
	}
	return next.Validate()
}

func sameEvent(a, b Event) bool {
	return a.At.Equal(b.At) && a.Actor == b.Actor && a.By == b.By && a.From == b.From && a.To == b.To && a.Reason == b.Reason
}

// PersonaReferences returns the identifiers of the personas the item names,
// sorted and without duplicates: a store checks that each exists in the scope.
func (it Item) PersonaReferences() []string {
	if it.Requirement == nil || len(it.Requirement.Personas) == 0 {
		return nil
	}
	refs := slices.Clone(it.Requirement.Personas)
	slices.Sort(refs)
	return slices.Compact(refs)
}

// Clone returns a deep copy of the item: changing one never changes the other.
// A store hands out clones.
func (it Item) Clone() Item { return it.clone() }
