package knowledgetest

import (
	"testing"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) knowledge.Store { return NewMemory() })
}
