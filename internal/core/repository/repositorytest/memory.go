// Package repositorytest holds the contract suite of the store port of the
// repository context and an in-memory store that passes it. Every store runs
// Suite, so the SQLite adapter and later ones keep the same rules.
package repositorytest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// Memory is a store in memory. Its zero value is ready; AddWorkspace makes
// a workspace known, as the workspace store of the same data would.
type Memory struct {
	mu         sync.Mutex
	workspaces map[string]string // identifier -> name
	repos      map[key]repository.Repository
	deliveries map[key]time.Time // forge and delivery identifier -> received
	digests    map[key]time.Time // forge and digest -> received
	events     []repository.LoggedEvent
}

type key struct{ forge, id string }

var (
	_ repository.Store      = (*Memory)(nil)
	_ repository.EventStore = (*Memory)(nil)
)

func (m *Memory) init() {
	if m.repos == nil {
		m.workspaces = map[string]string{}
		m.repos = map[key]repository.Repository{}
		m.deliveries = map[key]time.Time{}
		m.digests = map[key]time.Time{}
	}
}

// AddWorkspace makes the workspace w known.
func (m *Memory) AddWorkspace(w repository.Workspace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.workspaces[w.ID] = w.Name
}

// get returns r with the name of its workspace; the caller holds the lock.
func (m *Memory) get(r repository.Repository) repository.Repository {
	r.Workspace.Name = m.workspaces[r.Workspace.ID]
	return r
}

func (m *Memory) Known(_ context.Context, forge string, ids []string) ([]repository.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []repository.Repository
	for _, id := range ids {
		if r, ok := m.repos[key{forge, id}]; ok && !slices.ContainsFunc(out, func(o repository.Repository) bool { return o.ID == id }) {
			out = append(out, m.get(r))
		}
	}
	slices.SortFunc(out, func(a, b repository.Repository) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *Memory) Find(_ context.Context, workspace, forge, fullName string) (repository.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	for k, r := range m.repos {
		if k.forge == forge && fullName != "" && r.FullName == fullName && m.workspaces[r.Workspace.ID] == workspace {
			return m.get(r), nil
		}
	}
	return repository.Repository{}, repository.ErrNotFound
}

func (m *Memory) InWorkspace(_ context.Context, workspace string) ([]repository.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []repository.Repository
	for _, r := range m.repos {
		if r.Workspace.ID == workspace {
			out = append(out, m.get(r))
		}
	}
	slices.SortFunc(out, func(a, b repository.Repository) int {
		if c := strings.Compare(a.FullName, b.FullName); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// takeName empties the full name of every other repository of forge that
// holds name; the caller holds the lock.
func (m *Memory) takeName(forge, id, name string) {
	for k, r := range m.repos {
		if k.forge == forge && k.id != id && r.FullName == name {
			r.FullName = ""
			m.repos[k] = r
		}
	}
}

func (m *Memory) Activate(_ context.Context, r repository.Repository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	k := key{r.Forge, r.ID}
	if old, ok := m.repos[k]; ok && old.Workspace.ID != r.Workspace.ID {
		return repository.ErrElsewhere
	}
	m.takeName(r.Forge, r.ID, r.FullName)
	r.Workspace.Name = ""
	r.Active, r.Reachable = true, true
	r.ChangedAt = r.ChangedAt.UTC()
	m.repos[k] = r
	return nil
}

func (m *Memory) Deactivate(_ context.Context, workspace, forge, id, account string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	k := key{forge, id}
	r, ok := m.repos[k]
	if !ok || r.Workspace.ID != workspace {
		return repository.ErrNotFound
	}
	r.Active, r.ChangedBy, r.ChangedAt = false, account, at.UTC()
	m.repos[k] = r
	return nil
}

func (m *Memory) Refresh(_ context.Context, listed []repository.Listed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	for _, l := range listed {
		k := key{l.Forge, l.ID}
		r, ok := m.repos[k]
		if !ok {
			continue
		}
		m.takeName(l.Forge, l.ID, l.FullName)
		r.FullName, r.DefaultBranch, r.Private = l.FullName, l.DefaultBranch, l.Private
		m.repos[k] = r
	}
	return nil
}

func (m *Memory) SetReach(_ context.Context, forge string, reached []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var lost []string
	for k, r := range m.repos {
		if k.forge != forge {
			continue
		}
		if slices.Contains(reached, k.id) {
			r.Reachable = true
		} else {
			if r.Reachable {
				lost = append(lost, k.id)
			}
			r.Reachable, r.Active = false, false
		}
		m.repos[k] = r
	}
	slices.Sort(lost)
	return lost, nil
}

func (m *Memory) Record(_ context.Context, k repository.Key, row *repository.LoggedEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if err := checkKey(k, row); err != nil {
		return false, err
	}
	if _, ok := m.deliveries[key{k.Forge, k.Delivery}]; ok {
		return false, repository.ErrDuplicate
	}
	if _, ok := m.digests[key{k.Forge, k.Digest}]; ok {
		return false, repository.ErrDuplicate
	}
	m.deliveries[key{k.Forge, k.Delivery}] = k.ReceivedAt.UTC()
	m.digests[key{k.Forge, k.Digest}] = k.ReceivedAt.UTC()
	if row == nil {
		return false, nil
	}
	if r, ok := m.repos[key{row.Forge, row.Repository}]; !ok || !r.Active {
		return false, nil
	}
	e := *row
	e.ReceivedAt = e.ReceivedAt.UTC()
	m.events = append(m.events, e)
	return true, nil
}

// checkKey refuses what a store refuses: a key without its parts, a row of
// another delivery or with an unknown result.
func checkKey(k repository.Key, row *repository.LoggedEvent) error {
	if k.Forge == "" || k.Delivery == "" || len(k.Digest) != 64 {
		return fmt.Errorf("record: incomplete key %+v", k)
	}
	if row != nil && (row.Forge != k.Forge || row.Delivery != k.Delivery || row.Repository == "" ||
		(row.Result != repository.ResultRecorded && row.Result != repository.ResultIgnored)) {
		return fmt.Errorf("record: invalid row %+v", *row)
	}
	return nil
}

func (m *Memory) Log(_ context.Context, forge, repo string, limit int) ([]repository.LoggedEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []repository.LoggedEvent
	for _, e := range m.events {
		if e.Forge == forge && e.Repository == repo {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b repository.LoggedEvent) int {
		if c := b.ReceivedAt.Compare(a.ReceivedAt); c != 0 {
			return c
		}
		return strings.Compare(b.Delivery, a.Delivery)
	})
	if len(out) > limit {
		out = out[:max(limit, 0)]
	}
	return out, nil
}

func (m *Memory) PurgeEvents(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	n := 0
	for _, keys := range []map[key]time.Time{m.deliveries, m.digests} {
		for k, at := range keys {
			if at.Before(before) {
				delete(keys, k)
				n++
			}
		}
	}
	// A delivery is one key whatever it holds: its two entries count once.
	n /= 2
	kept := m.events[:0]
	for _, e := range m.events {
		if e.ReceivedAt.Before(before) {
			n++
			continue
		}
		kept = append(kept, e)
	}
	m.events = kept
	return n, nil
}
