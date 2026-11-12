package knowledgetest

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

// Memory is a knowledge.Store held in memory. One lock covers every
// operation, so an update reads, checks, mutates and publishes an item
// without any other operation in between. Waiting for the lock honours the
// context, as waiting for the SQLite writer does.
type Memory struct {
	lock  chan struct{}
	items map[key]knowledge.Item
}

type key struct {
	scope knowledge.Scope
	id    string
}

var _ knowledge.Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{lock: make(chan struct{}, 1), items: map[key]knowledge.Item{}}
}

// acquire takes the lock, or gives up when ctx ends: a mutation that calls
// the store waits for it, never forever.
func (m *Memory) acquire(ctx context.Context) error {
	select {
	case m.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Memory) release() { <-m.lock }

// List returns the items of the scope that match the filter, by identifier.
func (m *Memory) List(ctx context.Context, scope knowledge.Scope, filter knowledge.Filter) ([]knowledge.Item, error) {
	if err := check(ctx, scope); err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	if err := filter.Validate(); err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	if err := m.acquire(ctx); err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	defer m.release()
	var out []knowledge.Item
	for k, it := range m.items {
		if k.scope == scope && filter.Matches(it) {
			out = append(out, it.Clone())
		}
	}
	slices.SortFunc(out, func(a, b knowledge.Item) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get returns the item, or knowledge.ErrNotFound.
func (m *Memory) Get(ctx context.Context, scope knowledge.Scope, id string) (knowledge.Item, error) {
	if err := check(ctx, scope); err != nil {
		return knowledge.Item{}, fmt.Errorf("get %s: %w", id, err)
	}
	if err := m.acquire(ctx); err != nil {
		return knowledge.Item{}, fmt.Errorf("get %s: %w", id, err)
	}
	defer m.release()
	it, ok := m.items[key{scope, id}]
	if !ok {
		return knowledge.Item{}, fmt.Errorf("get %s: %w", id, knowledge.ErrNotFound)
	}
	return it.Clone(), nil
}

// Create stores a new item.
func (m *Memory) Create(ctx context.Context, scope knowledge.Scope, it knowledge.Item) error {
	if err := check(ctx, scope); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	if err := knowledge.CheckCreate(it); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	if err := m.acquire(ctx); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	defer m.release()
	if _, ok := m.items[key{scope, it.ID}]; ok {
		return fmt.Errorf("create %s: %w", it.ID, knowledge.ErrIDTaken)
	}
	if err := m.checkReferences(scope, it); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	m.items[key{scope, it.ID}] = it.Clone()
	return nil
}

// Update runs mutate on the stored item and stores the result at the next
// revision.
func (m *Memory) Update(ctx context.Context, scope knowledge.Scope, id string, revision int64, mutate knowledge.Mutation) (knowledge.Item, error) {
	if err := check(ctx, scope); err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	if err := m.acquire(ctx); err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	defer m.release()
	stored, ok := m.items[key{scope, id}]
	if !ok {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, knowledge.ErrNotFound)
	}
	if stored.Revision != revision {
		return knowledge.Item{}, fmt.Errorf("update %s: %w: read %d, stored %d", id, knowledge.ErrStaleRevision, revision, stored.Revision)
	}
	if stored.Revision == math.MaxInt64 {
		return knowledge.Item{}, fmt.Errorf("update %s: revision %d cannot grow", id, stored.Revision)
	}
	next, err := mutate(stored.Clone())
	if err != nil {
		return knowledge.Item{}, err
	}
	if err := knowledge.CheckUpdate(stored, next); err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	if err := m.checkReferences(scope, next); err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	next = next.Clone()
	next.Revision = stored.Revision + 1
	m.items[key{scope, id}] = next
	return next.Clone(), nil
}

func (m *Memory) checkReferences(scope knowledge.Scope, it knowledge.Item) error {
	var missing []string
	for _, ref := range it.PersonaReferences() {
		if p, ok := m.items[key{scope, ref}]; !ok || p.Type != knowledge.TypePersona {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: personas %s", knowledge.ErrUnknownReference, strings.Join(missing, ", "))
	}
	return nil
}

func check(ctx context.Context, scope knowledge.Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return scope.Validate()
}
