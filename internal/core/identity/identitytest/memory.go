// Package identitytest holds the contract suite of the store port of the
// identity context and an in-memory store that passes it. Every store runs
// Suite, so the SQLite adapter and later ones keep the same rules.
package identitytest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Memory is a store in memory, for the tests of the packages that log people
// in. It keeps the sessions of accounts as hashes only, to tell which ones
// RevokeTokens ended. Its zero value is ready.
type Memory struct {
	mu         sync.Mutex
	states     map[string]identity.LoginState
	signups    map[string]identity.Signup
	accounts   map[string]identity.Account
	identities map[string]memIdentity // key -> identity
	tokens     map[string]identity.Tokens
	sessions   map[string]string                   // session -> account
	workspaces map[string]identity.Workspace       // identifier -> workspace
	names      map[string]string                   // name -> workspace identifier
	members    map[string]map[string]identity.Role // workspace -> account -> role
	seq        int
}

type memIdentity struct {
	id      identity.Identity
	account string
}

var (
	_ identity.Store          = (*Memory)(nil)
	_ identity.WorkspaceStore = (*Memory)(nil)
)

func (m *Memory) init() {
	if m.states == nil {
		m.states = map[string]identity.LoginState{}
		m.signups = map[string]identity.Signup{}
		m.accounts = map[string]identity.Account{}
		m.identities = map[string]memIdentity{}
		m.tokens = map[string]identity.Tokens{}
		m.sessions = map[string]string{}
		m.workspaces = map[string]identity.Workspace{}
		m.names = map[string]string{}
		m.members = map[string]map[string]identity.Role{}
	}
}

func (m *Memory) PutLoginState(_ context.Context, s identity.LoginState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.states[s.Hash] = s
	return nil
}

func (m *Memory) TakeLoginState(_ context.Context, hash, session string, now time.Time) (identity.LoginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	s, ok := m.states[hash]
	delete(m.states, hash)
	if !ok || s.Session != session || !now.Before(s.ExpiresAt) {
		return identity.LoginState{}, identity.ErrNotFound
	}
	return s, nil
}

func (m *Memory) DeleteExpiredLoginStates(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	n := 0
	for k, s := range m.states {
		if !now.Before(s.ExpiresAt) {
			delete(m.states, k)
			n++
		}
	}
	return n, nil
}

func (m *Memory) PutSignup(_ context.Context, s identity.Signup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.signups[s.Session] = s
	return nil
}

func (m *Memory) Signup(_ context.Context, session string, now time.Time) (identity.Signup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	s, ok := m.signups[session]
	if !ok || !now.Before(s.ExpiresAt) {
		return identity.Signup{}, identity.ErrNotFound
	}
	return s, nil
}

func (m *Memory) CompleteSignup(_ context.Context, session string, a identity.Account, now time.Time) (identity.Signup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	s, ok := m.signups[session]
	if !ok || !now.Before(s.ExpiresAt) {
		return identity.Signup{}, identity.ErrNotFound
	}
	if _, taken := m.names[a.Name]; taken {
		return identity.Signup{}, identity.ErrNameTaken
	}
	key := s.Identity.Key()
	if _, ok := m.identities[key]; ok {
		return identity.Signup{}, identity.ErrIdentityTaken
	}
	delete(m.signups, session)
	m.accounts[a.ID] = a
	m.identities[key] = memIdentity{id: s.Identity, account: a.ID}
	m.tokens[key] = s.Tokens
	m.workspaces[a.ID] = identity.Workspace{ID: a.ID, Name: a.Name, Personal: true, CreatedAt: a.CreatedAt}
	m.names[a.Name] = a.ID
	m.members[a.ID] = map[string]identity.Role{a.ID: identity.RoleOwner}
	return s, nil
}

func (m *Memory) CreateWorkspace(_ context.Context, w identity.Workspace, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if _, taken := m.names[w.Name]; taken {
		return identity.ErrNameTaken
	}
	if _, ok := m.accounts[owner]; !ok {
		return identity.ErrNotFound
	}
	w.Personal = false
	m.workspaces[w.ID] = w
	m.names[w.Name] = w.ID
	m.members[w.ID] = map[string]identity.Role{owner: identity.RoleOwner}
	return nil
}

func (m *Memory) WorkspacesOf(_ context.Context, account string) ([]identity.Membership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []identity.Membership
	for id, roles := range m.members {
		if role, ok := roles[account]; ok {
			out = append(out, identity.Membership{Workspace: m.workspaces[id], Role: role})
		}
	}
	slices.SortFunc(out, func(a, b identity.Membership) int { return strings.Compare(a.Workspace.Name, b.Workspace.Name) })
	return out, nil
}

func (m *Memory) WorkspaceOf(_ context.Context, name, account string) (identity.Membership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	id, ok := m.names[name]
	if !ok {
		return identity.Membership{}, identity.ErrNotFound
	}
	role, ok := m.members[id][account]
	if !ok {
		return identity.Membership{}, identity.ErrNotFound
	}
	return identity.Membership{Workspace: m.workspaces[id], Role: role}, nil
}

func (m *Memory) Members(_ context.Context, workspace string) ([]identity.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []identity.Member
	for account, role := range m.members[workspace] {
		out = append(out, identity.Member{Account: account, Name: m.accounts[account].Name, Role: role})
	}
	slices.SortFunc(out, func(a, b identity.Member) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *Memory) AddMember(_ context.Context, actor, workspace, name string, role identity.Role) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if !role.Valid() {
		return identity.ErrInvalidRole
	}
	roles, ok := m.members[workspace]
	if !ok {
		return identity.ErrNotFound
	}
	switch roles[actor] {
	case identity.RoleOwner:
	case identity.RoleMember:
		return identity.ErrNotOwner
	default:
		return identity.ErrNotFound
	}
	if m.workspaces[workspace].Personal {
		return identity.ErrPersonalWorkspace
	}
	var account string
	for id, a := range m.accounts {
		if a.Name == name {
			account = id
		}
	}
	if account == "" {
		return identity.ErrNotFound
	}
	if _, ok := roles[account]; ok {
		return identity.ErrAlreadyMember
	}
	roles[account] = role
	return nil
}

func (m *Memory) DeleteExpiredSignups(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	n := 0
	for k, s := range m.signups {
		if !now.Before(s.ExpiresAt) {
			delete(m.signups, k)
			n++
		}
	}
	return n, nil
}

func (m *Memory) AccountOf(_ context.Context, key string) (identity.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	id, ok := m.identities[key]
	if !ok {
		return identity.Account{}, identity.ErrNotFound
	}
	return m.accounts[id.account], nil
}

func (m *Memory) Account(_ context.Context, id string) (identity.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	a, ok := m.accounts[id]
	if !ok {
		return identity.Account{}, identity.ErrNotFound
	}
	return a, nil
}

func (m *Memory) IdentitiesOf(_ context.Context, account string) ([]identity.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	var out []identity.Identity
	for _, id := range m.identities {
		if id.account == account {
			out = append(out, id.id)
		}
	}
	slices.SortFunc(out, func(a, b identity.Identity) int {
		if c := strings.Compare(a.Forge, b.Forge); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (m *Memory) UpdateIdentity(_ context.Context, id identity.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	known, ok := m.identities[id.Key()]
	if !ok {
		return identity.ErrNotFound
	}
	known.id = id
	m.identities[id.Key()] = known
	return nil
}

func (m *Memory) Tokens(_ context.Context, key string) (identity.Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	t, ok := m.tokens[key]
	if !ok {
		return identity.Tokens{}, identity.ErrNotFound
	}
	return t, nil
}

func (m *Memory) PutTokens(_ context.Context, key string, t identity.Tokens) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if _, ok := m.identities[key]; !ok {
		return identity.ErrNotFound
	}
	m.tokens[key] = t
	return nil
}

func (m *Memory) RevokeTokens(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	delete(m.tokens, key)
	if id, ok := m.identities[key]; ok {
		for s, account := range m.sessions {
			if account == id.account {
				delete(m.sessions, s)
			}
		}
	}
	return nil
}

// StartSession gives account a session and returns whether it still lives,
// for Fixture.
func (m *Memory) StartSession(_ *testing.T, account string) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.seq++
	hash := fmt.Sprintf("session-%d", m.seq)
	m.sessions[hash] = account
	return func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.sessions[hash]
		return ok
	}
}
