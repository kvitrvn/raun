package sessiontest

import (
	"testing"

	"github.com/kvitrvn/raun/internal/core/session"
)

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) session.Store { return &Memory{} })
}
