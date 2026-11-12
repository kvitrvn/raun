package knowledgetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) knowledge.Store { return NewMemory() })
}

func TestZeroMemoryRefuses(t *testing.T) {
	var m Memory
	if err := m.Create(context.Background(), scopeA(), Persona(t, personaID)); err == nil || !strings.Contains(err.Error(), "NewMemory") {
		t.Errorf("a zero Memory: %v, want an error naming NewMemory", err)
	}
}

// A mutation must not call the store. If it does, the call waits for the lock
// until its context ends, and never forever.
func TestMemoryCallFromAMutationEnds(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	if err := m.Create(ctx, scopeA(), Persona(t, personaID)); err != nil {
		t.Fatal(err)
	}
	_, err := m.Update(ctx, scopeA(), personaID, 1, func(it knowledge.Item) (knowledge.Item, error) {
		inner, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, err := m.Get(inner, scopeA(), personaID)
		return it, err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call from a mutation: %v, want the end of its context", err)
	}
	if it, err := m.Get(ctx, scopeA(), personaID); err != nil || it.Revision != 1 {
		t.Errorf("after the refused update: %+v, %v", it.Revision, err)
	}
}
