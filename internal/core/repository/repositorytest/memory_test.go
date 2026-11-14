package repositorytest

import (
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) Fixture {
		m := &Memory{}
		return Fixture{Store: m, Events: m, AddWorkspace: func(_ *testing.T, w repository.Workspace, _ string) { m.AddWorkspace(w) }}
	})
}

func TestMemoryEvents(t *testing.T) {
	EventSuite(t, func(*testing.T) Fixture {
		m := &Memory{}
		return Fixture{Store: m, Events: m, AddWorkspace: func(_ *testing.T, w repository.Workspace, _ string) { m.AddWorkspace(w) }}
	})
}
