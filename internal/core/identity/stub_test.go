package identity_test

import (
	"context"
	"fmt"
	"github.com/kvitrvn/raun/internal/core/identity"
	"net/url"
	"slices"
	"sync"
	"time"
)

// stubForge is a forge of the tests of this package, which cannot import the
// fake forge of internal/forgetest: it knows people, the organizations they
// belong to, and answers refreshes as told.
type stubForge struct {
	mu      sync.Mutex
	now     func() time.Time
	people  map[string]identity.Identity // login -> identity
	members map[string][]string          // organization -> identifiers
	codes   map[string]stubCode
	holders map[string]string // access token -> identifier
	// memberErr and refreshErr are returned by IsMember and Refresh.
	memberErr  error
	refreshErr error
	// refreshWait, when set, is waited on by Refresh, to hold callers.
	refreshWait chan struct{}
	refreshes   int
	seq         int
}

type stubCode struct {
	id        identity.Identity
	challenge string
}

var _ identity.Memberships = (*stubForge)(nil)

func newStubForge(now func() time.Time) *stubForge {
	return &stubForge{
		now:     now,
		people:  map[string]identity.Identity{"alice": {Forge: "stub", ID: "1001", Login: "alice", Name: "Alice"}, "bob": {Forge: "stub", ID: "1002", Login: "bob"}},
		members: map[string][]string{"3001": {"1001"}},
		codes:   map[string]stubCode{},
		holders: map[string]string{},
	}
}

func (f *stubForge) Name() string { return "stub" }

func (f *stubForge) AuthorizeURL(state, challenge string) string {
	return "https://stub.test/authorize?" + url.Values{"state": {state}, "challenge": {challenge}}.Encode()
}

// approve is login approving Raun at authorizeURL: the code and the state.
func (f *stubForge) approve(login, authorizeURL string) (code, state string) {
	u, _ := url.Parse(authorizeURL)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	code = fmt.Sprintf("code-%d", f.seq)
	f.codes[code] = stubCode{id: f.people[login], challenge: u.Query().Get("challenge")}
	return code, u.Query().Get("state")
}

func (f *stubForge) issue(person string) identity.Tokens {
	f.seq++
	now := f.now()
	f.holders[fmt.Sprintf("access-%d", f.seq)] = person
	return identity.Tokens{
		Access: identity.Secret(fmt.Sprintf("access-%d", f.seq)), AccessExpiry: now.Add(8 * time.Hour),
		Refresh: identity.Secret(fmt.Sprintf("refresh-%d", f.seq)), RefreshExpiry: now.Add(4000 * time.Hour),
	}
}

func (f *stubForge) Exchange(_ context.Context, code, verifier string) (identity.Identity, identity.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[code]
	delete(f.codes, code)
	if !ok || identity.Challenge(verifier) != c.challenge {
		return identity.Identity{}, identity.Tokens{}, identity.ErrInvalidCode
	}
	return c.id, f.issue(c.id.ID), nil
}

func (f *stubForge) Refresh(_ context.Context, t identity.Tokens) (identity.Tokens, error) {
	if f.refreshWait != nil {
		<-f.refreshWait
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	if f.refreshErr != nil {
		return identity.Tokens{}, f.refreshErr
	}
	return f.issue(f.holders[t.Access.Reveal()]), nil
}

func (f *stubForge) Repositories(context.Context, identity.Tokens) ([]identity.Repository, error) {
	return nil, nil
}

func (f *stubForge) Permission(context.Context, identity.Tokens, identity.Repository) (identity.Permission, error) {
	return identity.PermissionNone, nil
}

func (f *stubForge) IsMember(_ context.Context, t identity.Tokens, org string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memberErr != nil {
		return false, f.memberErr
	}
	return slices.Contains(f.members[org], f.holders[t.Access.Reveal()]), nil
}

// bareStub is stubForge without its membership capability.
type bareStub struct{ f *stubForge }

func (b bareStub) Name() string { return b.f.Name() }
func (b bareStub) AuthorizeURL(state, challenge string) string {
	return b.f.AuthorizeURL(state, challenge)
}
func (b bareStub) Exchange(ctx context.Context, code, verifier string) (identity.Identity, identity.Tokens, error) {
	return b.f.Exchange(ctx, code, verifier)
}
func (b bareStub) Refresh(ctx context.Context, t identity.Tokens) (identity.Tokens, error) {
	return b.f.Refresh(ctx, t)
}
func (b bareStub) Repositories(ctx context.Context, t identity.Tokens) ([]identity.Repository, error) {
	return b.f.Repositories(ctx, t)
}
func (b bareStub) Permission(ctx context.Context, t identity.Tokens, r identity.Repository) (identity.Permission, error) {
	return b.f.Permission(ctx, t, r)
}
