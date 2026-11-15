package jobtest

import (
	"sync/atomic"
	"testing"

	"github.com/kvitrvn/raun/internal/core/job"
)

func memoryFixture(*testing.T) Fixture {
	var queued atomic.Int64
	m := &Memory{Queued: func() { queued.Add(1) }}
	return Fixture{
		Store: m, Runners: m,
		AddWorkspace:  func(_ *testing.T, w job.Workspace) { m.AddWorkspace(w) },
		AddRepository: func(*testing.T, string, string, string) {},
		Queued:        func() int { return int(queued.Load()) },
		Reads:         m,
	}
}

func TestMemory(t *testing.T) { Suite(t, memoryFixture) }

func TestMemoryRunners(t *testing.T) { RunnerSuite(t, memoryFixture) }
