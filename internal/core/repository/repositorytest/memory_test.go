package repositorytest

import (
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) Fixture {
		m := &Memory{}
		return Fixture{Store: m, AddWorkspace: func(_ *testing.T, w repository.Workspace, _ string) { m.AddWorkspace(w) }}
	})
}
