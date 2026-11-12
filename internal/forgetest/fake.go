// Package forgetest holds a fake forge for tests and the contract suite every
// forge adapter passes. It is test code: no package of the binary imports it.
//
// The fake keeps scripted people, organizations, repositories and
// permissions in memory, runs the authorization flow with PKCE as a forge
// does, issues tokens that expire on its clock, and fails on request. It
// implements the required port and every optional capability that exists;
// Bare returns the same forge without the optional ones.
package forgetest

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Lifetimes the fake gives what it issues, the ones of GitHub.
const (
	AccessLifetime  = 8 * time.Hour
	RefreshLifetime = 183 * 24 * time.Hour
	CodeLifetime    = 10 * time.Minute
)

// Person is someone the fake forge knows.
type Person struct {
	ID    string
	Login string
	Name  string
}

type grant struct {
	person    string
	challenge string
	expires   time.Time
}

type token struct {
	person  string
	expires time.Time
}

type repository struct {
	repo      identity.Repository
	installed bool
}

// Fake is a forge in memory. Its zero value is not ready: use New.
type Fake struct {
	mu      sync.Mutex
	name    string
	now     func() time.Time
	people  map[string]Person
	repos   map[string]repository
	grants  map[string]map[string]identity.Permission // person -> repository -> permission
	orgs    map[string]map[string]bool                // organization -> members
	codes   map[string]grant
	access  map[string]token
	refresh map[string]token
	down    bool
	seq     int
}

var (
	_ identity.Forge       = (*Fake)(nil)
	_ identity.Memberships = (*Fake)(nil)
)

// New returns an empty fake forge called name, whose clock is now.
func New(name string, now func() time.Time) *Fake {
	return &Fake{
		name:    name,
		now:     now,
		people:  map[string]Person{},
		repos:   map[string]repository{},
		grants:  map[string]map[string]identity.Permission{},
		orgs:    map[string]map[string]bool{},
		codes:   map[string]grant{},
		access:  map[string]token{},
		refresh: map[string]token{},
	}
}

// AddPerson adds p.
func (f *Fake) AddPerson(p Person) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.people[p.ID] = p
}

// AddRepository adds r. A repository that is not installed exists on the
// forge, but Raun cannot reach it.
func (f *Fake) AddRepository(r identity.Repository, installed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.Forge = f.name
	f.repos[r.ID] = repository{repo: r, installed: installed}
}

// Grant gives person the permission p on repository repo.
func (f *Fake) Grant(person, repo string, p identity.Permission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grants[person] == nil {
		f.grants[person] = map[string]identity.Permission{}
	}
	f.grants[person][repo] = p
}

// AddMember makes person a member of the organization org.
func (f *Fake) AddMember(org, person string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgs[org] == nil {
		f.orgs[org] = map[string]bool{}
	}
	f.orgs[org][person] = true
}

// Rename gives person a new login, as a forge rename does; the identifier
// stays. It reports whether the person exists.
func (f *Fake) Rename(person, login string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.people[person]
	if ok {
		p.Login = login
		f.people[person] = p
	}
	return ok
}

// SetDown makes every call fail with identity.ErrUnavailable, or work again.
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Revoke withdraws every token of person, as a person who revokes the App.
func (f *Fake) Revoke(person string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.access {
		if t.person == person {
			delete(f.access, k)
		}
	}
	for k, t := range f.refresh {
		if t.person == person {
			delete(f.refresh, k)
		}
	}
}

// Approve is person approving Raun at authorizeURL: it returns the code and
// the state the forge sends back to the callback.
func (f *Fake) Approve(person, authorizeURL string) (code, state string, err error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		return "", "", fmt.Errorf("authorization URL %q lacks state or an S256 challenge", authorizeURL)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.people[person]; !ok {
		return "", "", fmt.Errorf("unknown person %q", person)
	}
	code = f.next("code")
	f.codes[code] = grant{person: person, challenge: q.Get("code_challenge"), expires: f.now().Add(CodeLifetime)}
	return code, q.Get("state"), nil
}

// next returns a new opaque value; the caller holds the lock.
func (f *Fake) next(kind string) string {
	f.seq++
	return fmt.Sprintf("%s-%s-%d", f.name, kind, f.seq)
}

func (f *Fake) Name() string { return f.name }

func (f *Fake) AuthorizeURL(state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", "raun")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return "https://" + f.name + ".forge.test/login/oauth/authorize?" + q.Encode()
}

func (f *Fake) Exchange(_ context.Context, code, verifier string) (identity.Identity, identity.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return identity.Identity{}, identity.Tokens{}, identity.ErrUnavailable
	}
	g, ok := f.codes[code]
	delete(f.codes, code)
	if !ok || !f.now().Before(g.expires) || identity.Challenge(verifier) != g.challenge {
		return identity.Identity{}, identity.Tokens{}, identity.ErrInvalidCode
	}
	p := f.people[g.person]
	return identity.Identity{Forge: f.name, ID: p.ID, Login: p.Login, Name: p.Name}, f.issue(g.person), nil
}

// issue gives person a new pair of tokens; the caller holds the lock.
func (f *Fake) issue(person string) identity.Tokens {
	now := f.now()
	a, r := f.next("access"), f.next("refresh")
	f.access[a] = token{person: person, expires: now.Add(AccessLifetime)}
	f.refresh[r] = token{person: person, expires: now.Add(RefreshLifetime)}
	return identity.Tokens{
		Access:        identity.Secret(a),
		AccessExpiry:  now.Add(AccessLifetime),
		Refresh:       identity.Secret(r),
		RefreshExpiry: now.Add(RefreshLifetime),
	}
}

func (f *Fake) Refresh(_ context.Context, t identity.Tokens) (identity.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return identity.Tokens{}, identity.ErrUnavailable
	}
	r, ok := f.refresh[t.Refresh.Reveal()]
	if !ok || !f.now().Before(r.expires) {
		return identity.Tokens{}, identity.ErrRevoked
	}
	// A refresh token serves once: the forge rotates both.
	delete(f.refresh, t.Refresh.Reveal())
	delete(f.access, t.Access.Reveal())
	return f.issue(r.person), nil
}

// holder returns the person of a valid access token; the caller holds the
// lock.
func (f *Fake) holder(t identity.Tokens) (string, error) {
	if f.down {
		return "", identity.ErrUnavailable
	}
	a, ok := f.access[t.Access.Reveal()]
	if !ok || !f.now().Before(a.expires) {
		return "", identity.ErrUnauthorized
	}
	return a.person, nil
}

func (f *Fake) Repositories(_ context.Context, t identity.Tokens) ([]identity.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	person, err := f.holder(t)
	if err != nil {
		return nil, err
	}
	var out []identity.Repository
	for id, p := range f.grants[person] {
		r, ok := f.repos[id]
		if ok && r.installed && p != identity.PermissionNone {
			out = append(out, r.repo)
		}
	}
	slices.SortFunc(out, func(a, b identity.Repository) int { return strings.Compare(a.FullName, b.FullName) })
	return out, nil
}

func (f *Fake) Permission(_ context.Context, t identity.Tokens, repo identity.Repository) (identity.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	person, err := f.holder(t)
	if err != nil {
		return "", err
	}
	r, ok := f.repos[repo.ID]
	if !ok || !r.installed {
		return identity.PermissionNone, nil
	}
	if p, ok := f.grants[person][repo.ID]; ok {
		return p, nil
	}
	return identity.PermissionNone, nil
}

func (f *Fake) IsMember(_ context.Context, t identity.Tokens, org string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	person, err := f.holder(t)
	if err != nil {
		return false, err
	}
	return f.orgs[org][person], nil
}

// Bare returns f without its optional capabilities, as a forge that offers
// only the required port.
func (f *Fake) Bare() identity.Forge {
	return bare{f: f}
}

type bare struct{ f *Fake }

func (b bare) Name() string { return b.f.Name() }

func (b bare) AuthorizeURL(state, challenge string) string { return b.f.AuthorizeURL(state, challenge) }

func (b bare) Exchange(ctx context.Context, code, verifier string) (identity.Identity, identity.Tokens, error) {
	return b.f.Exchange(ctx, code, verifier)
}

func (b bare) Refresh(ctx context.Context, t identity.Tokens) (identity.Tokens, error) {
	return b.f.Refresh(ctx, t)
}

func (b bare) Repositories(ctx context.Context, t identity.Tokens) ([]identity.Repository, error) {
	return b.f.Repositories(ctx, t)
}

func (b bare) Permission(ctx context.Context, t identity.Tokens, repo identity.Repository) (identity.Permission, error) {
	return b.f.Permission(ctx, t, repo)
}
