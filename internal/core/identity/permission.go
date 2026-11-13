package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PermissionLifetime is how long a permission the forge gave is kept.
const PermissionLifetime = 5 * time.Minute

var (
	// ErrNoIdentity: the account has no identity, or more than one, on the
	// forge of the repository; nothing establishes its permission.
	ErrNoIdentity = errors.New("no single identity of the account on the forge of the repository")
	// errChanged: the permissions of the account were forgotten while the
	// forge answered, so the answer is not used.
	errChanged = errors.New("the permissions of the account changed during the check")
)

// IdentityLister lists the forge identities of an account.
type IdentityLister interface {
	IdentitiesOf(ctx context.Context, account string) ([]Identity, error)
}

// PermissionService decides what an account may do on a repository: it is
// the one place that does, for every screen and action on repository data.
// It asks the forge with the account's own tokens, and keeps the answer
// PermissionLifetime from that answer, in memory, per account and stable
// identifier of the repository. Callers that miss the same entry together
// share one question to the forge. A permission that cannot be established
// is a refusal: the service answers PermissionNone with the error. No role of
// the instance or of a workspace gives any permission. It is safe for
// concurrent use.
type PermissionService struct {
	forge      Forge
	tokens     *TokenService
	identities IdentityLister
	now        func() time.Time

	mu       sync.Mutex
	entries  map[permissionKey]permissionEntry
	inflight map[permissionKey]*permissionCall
	// The invalidations of each account, of each identity and of each
	// entry: an answer the forge gave across one is not kept.
	accountGen  map[string]uint64
	identityGen map[string]uint64
	keyGen      map[permissionKey]uint64
}

type permissionKey struct {
	account, forge, repository string
}

type permissionEntry struct {
	permission Permission
	expires    time.Time
	identity   string
}

// permissionCall is a question to the forge in progress, which the callers
// of the same entry wait on. The invalidation counts it was asked under are
// set under the lock of the service; its answer is set before done closes.
type permissionCall struct {
	done        chan struct{}
	permission  Permission
	err         error
	accountGen  uint64
	keyGen      uint64
	identity    string
	identityGen uint64
}

// NewPermissionService returns the permission service of forge, whose tokens
// tokens gives, for the accounts identities lists, at the time now gives. It
// forgets the permissions of an identity whose tokens tokens revokes.
func NewPermissionService(forge Forge, tokens *TokenService, identities IdentityLister, now func() time.Time) *PermissionService {
	p := &PermissionService{
		forge:       forge,
		tokens:      tokens,
		identities:  identities,
		now:         now,
		entries:     map[permissionKey]permissionEntry{},
		inflight:    map[permissionKey]*permissionCall{},
		accountGen:  map[string]uint64{},
		identityGen: map[string]uint64{},
		keyGen:      map[permissionKey]uint64{},
	}
	tokens.OnRevoke(p.revoked)
	return p
}

// Permission returns what the account with this identifier may do on repo.
// A permission kept less than PermissionLifetime ago is given without asking
// the forge, whatever the forge's state; past it, the forge is asked again,
// and a forge that cannot answer refuses. Any error comes with
// PermissionNone: ErrNoIdentity, the errors of the token service (ErrRevoked,
// ErrNotFound when the tokens are gone) and of the forge (ErrUnauthorized,
// ErrUnavailable, which also wraps a failure of the store, an answer the
// forge should not give, and an answer given across an invalidation). A
// PermissionNone the forge gives comes with no error.
func (p *PermissionService) Permission(ctx context.Context, account string, repo Repository) (Permission, error) {
	if account == "" || repo.ID == "" || repo.Forge != p.forge.Name() {
		return PermissionNone, ErrNoIdentity
	}
	key := permissionKey{account: account, forge: repo.Forge, repository: repo.ID}
	for {
		p.mu.Lock()
		if e, ok := p.entries[key]; ok && p.now().Before(e.expires) {
			p.mu.Unlock()
			return e.permission, nil
		}
		if c, ok := p.inflight[key]; ok {
			p.mu.Unlock()
			select {
			case <-c.done:
			case <-ctx.Done():
				return PermissionNone, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
			}
			// A question given up by its own caller is asked again.
			if c.err != nil && (errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return c.permission, c.err
		}
		c := &permissionCall{done: make(chan struct{}), accountGen: p.accountGen[account], keyGen: p.keyGen[key]}
		p.inflight[key] = c
		p.mu.Unlock()

		got, err := p.ask(ctx, key, repo, c)
		p.mu.Lock()
		// Keeping the answer and ending the question are one step: a caller
		// that comes after an invalidation never joins an answer it dropped.
		if err == nil {
			got, err = p.keep(key, c, got)
		}
		delete(p.inflight, key)
		p.prune(key, c.identity)
		p.mu.Unlock()
		c.permission, c.err = got, err
		close(c.done)
		return got, err
	}
}

// ask asks the forge the permission of key, for the question c.
func (p *PermissionService) ask(ctx context.Context, key permissionKey, repo Repository, c *permissionCall) (Permission, error) {
	identity, err := p.identityOf(ctx, key.account, repo.Forge)
	if err != nil {
		return PermissionNone, err
	}
	p.mu.Lock()
	c.identity, c.identityGen = identity, p.identityGen[identity]
	p.mu.Unlock()

	var got Permission
	err = p.tokens.With(ctx, identity, func(t Tokens) error {
		var err error
		got, err = p.forge.Permission(ctx, t, repo)
		return err
	})
	switch {
	case errors.Is(err, ErrRevoked) || errors.Is(err, ErrNotFound):
		// The tokens are gone: nothing of the account holds any more.
		p.Forget(key.account)
		return PermissionNone, err
	case errors.Is(err, ErrUnauthorized):
		// The forge refuses even a refreshed token for this repository.
		p.Refused(key.account, repo)
		return PermissionNone, err
	case errors.Is(err, ErrUnavailable):
		// A transient failure keeps what is kept: a permission still kept
		// was given above; an expired one stays refused.
		return PermissionNone, err
	case err != nil:
		// The store of the tokens failed, or the caller gave up: a refusal
		// to try again.
		return PermissionNone, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if got != PermissionNone && got != PermissionView && got != PermissionDecide {
		return PermissionNone, fmt.Errorf("%w: the forge gave the unknown permission %q", ErrUnavailable, got)
	}
	return got, nil
}

// keep keeps got, the answer to c, unless the account, the identity or the
// entry was invalidated while it was asked; the caller holds the lock.
func (p *PermissionService) keep(key permissionKey, c *permissionCall, got Permission) (Permission, error) {
	if p.accountGen[key.account] != c.accountGen || p.keyGen[key] != c.keyGen || p.identityGen[c.identity] != c.identityGen {
		return PermissionNone, fmt.Errorf("%w: %w", ErrUnavailable, errChanged)
	}
	// The lifetime counts from the answer, however long the question took.
	now := p.now()
	p.sweep(now)
	p.entries[key] = permissionEntry{permission: got, expires: now.Add(PermissionLifetime), identity: c.identity}
	return got, nil
}

// asking reports whether a question is in flight that match reports; the
// caller holds the lock.
func (p *PermissionService) asking(match func(permissionKey, *permissionCall) bool) bool {
	for k, c := range p.inflight {
		if match(k, c) {
			return true
		}
	}
	return false
}

// prune drops the invalidation counts no question in flight can compare
// with, once the question of key, through identity, ended: only questions in
// flight keep counts, so they never grow with the life of the server. The
// caller holds the lock.
func (p *PermissionService) prune(key permissionKey, identity string) {
	if !p.asking(func(k permissionKey, _ *permissionCall) bool { return k.account == key.account }) {
		delete(p.accountGen, key.account)
	}
	delete(p.keyGen, key)
	if identity != "" && !p.asking(func(_ permissionKey, c *permissionCall) bool { return c.identity == identity }) {
		delete(p.identityGen, identity)
	}
}

// identityOf returns the key of the one identity of account on forge.
func (p *PermissionService) identityOf(ctx context.Context, account, forge string) (string, error) {
	ids, err := p.identities.IdentitiesOf(ctx, account)
	if err != nil {
		return "", fmt.Errorf("%w: find the identity of the account: %w", ErrUnavailable, err)
	}
	var key string
	for _, id := range ids {
		if id.Forge != forge {
			continue
		}
		if key != "" {
			return "", ErrNoIdentity
		}
		key = id.Key()
	}
	if key == "" {
		return "", ErrNoIdentity
	}
	return key, nil
}

// sweep drops the entries expired at now; the caller holds the lock.
func (p *PermissionService) sweep(now time.Time) {
	for k, e := range p.entries {
		if !now.Before(e.expires) {
			delete(p.entries, k)
		}
	}
}

// Forget drops every permission of the account, at its logout or once its
// tokens are gone.
func (p *PermissionService) Forget(account string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.entries {
		if k.account == account {
			delete(p.entries, k)
		}
	}
	if p.asking(func(k permissionKey, _ *permissionCall) bool { return k.account == account }) {
		p.accountGen[account]++
	}
}

// Refused drops the permission of the account on repo, and only it: the
// forge refused a call about it, a call of the service or of a caller of
// epic 5 or later.
func (p *PermissionService) Refused(account string, repo Repository) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := permissionKey{account: account, forge: repo.Forge, repository: repo.ID}
	delete(p.entries, key)
	if _, ok := p.inflight[key]; ok {
		p.keyGen[key]++
	}
}

// revoked drops the permissions given through the identity with this key,
// whose tokens the token service revoked. It runs under the lock of the
// identity, and takes only the lock of p.
func (p *PermissionService) revoked(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.entries {
		if e.identity == key {
			delete(p.entries, k)
		}
	}
	if p.asking(func(_ permissionKey, c *permissionCall) bool { return c.identity == key }) {
		p.identityGen[key]++
	}
}
