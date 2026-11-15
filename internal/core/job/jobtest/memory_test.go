package jobtest

import (
	"testing"

	"github.com/kvitrvn/raun/internal/core/job"
)

func memoryFixture(*testing.T) Fixture {
	m := &Memory{}
	return Fixture{
		Store: m, Runners: m,
		AddWorkspace:  func(_ *testing.T, w job.Workspace) { m.AddWorkspace(w) },
		AddRepository: func(*testing.T, string, string, string) {},
	}
}

func TestMemory(t *testing.T) { Suite(t, memoryFixture) }

func TestMemoryRunners(t *testing.T) { RunnerSuite(t, memoryFixture) }
