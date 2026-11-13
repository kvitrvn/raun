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

// PermissionService decides what an account may do on a repository: it is
// the one place that does, for every screen and action on repository data.
// It asks the forge with the account's own tokens, and keeps the answer
// PermissionLifetime, in memory, per account and stable identifier of the
// repository. A permission that cannot be established is a refusal: the
// service answers PermissionNone with the error. No role of the instance or
// of a workspace gives any permission. It is safe for concurrent use.
type PermissionService struct {
	forge  Forge
	tokens *TokenService
	store  Store
	now    func() time.Time

	mu      sync.Mutex
	entries map[permissionKey]permissionEntry
	// accounts and identities count the invalidations of each account and
	// of each identity: an answer the forge gave across one is not kept.
	accounts   map[string]uint64
	identities map[string]uint64
}

type permissionKey struct {
	account, forge, repository string
}

type permissionEntry struct {
	permission Permission
	expires    time.Time
	identity   string
}

// NewPermissionService returns the permission service of forge, whose tokens
// tokens gives, for the accounts of store, at the time now gives. It forgets
// the permissions of an identity whose tokens tokens revokes.
func NewPermissionService(forge Forge, tokens *TokenService, store Store, now func() time.Time) *PermissionService {
	p := &PermissionService{
		forge:      forge,
		tokens:     tokens,
		store:      store,
		now:        now,
		entries:    map[permissionKey]permissionEntry{},
		accounts:   map[string]uint64{},
		identities: map[string]uint64{},
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
// ErrUnavailable). A PermissionNone the forge gives comes with no error.
func (p *PermissionService) Permission(ctx context.Context, account string, repo Repository) (Permission, error) {
	if account == "" || repo.ID == "" || repo.Forge != p.forge.Name() {
		return PermissionNone, ErrNoIdentity
	}
	key := permissionKey{account: account, forge: repo.Forge, repository: repo.ID}
	now := p.now()
	p.mu.Lock()
	if e, ok := p.entries[key]; ok && now.Before(e.expires) {
		p.mu.Unlock()
		return e.permission, nil
	}
	accountGen := p.accounts[account]
	p.mu.Unlock()

	identity, err := p.identityOf(ctx, account, repo.Forge)
	if err != nil {
		return PermissionNone, err
	}
	p.mu.Lock()
	identityGen := p.identities[identity]
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
		p.Forget(account)
		return PermissionNone, err
	case errors.Is(err, ErrUnauthorized):
		// The forge refuses even a refreshed token for this repository.
		p.Refused(account, repo)
		return PermissionNone, err
	case err != nil:
		// A transient failure keeps what is kept: a permission still kept
		// was given above; an expired one stays refused.
		return PermissionNone, err
	}
	if got != PermissionNone && got != PermissionView && got != PermissionDecide {
		return PermissionNone, fmt.Errorf("the forge gave the unknown permission %q", got)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.accounts[account] != accountGen || p.identities[identity] != identityGen {
		return PermissionNone, fmt.Errorf("%w: %w", ErrUnavailable, errChanged)
	}
	p.sweep(now)
	p.entries[key] = permissionEntry{permission: got, expires: now.Add(PermissionLifetime), identity: identity}
	return got, nil
}

// identityOf returns the key of the one identity of account on forge.
func (p *PermissionService) identityOf(ctx context.Context, account, forge string) (string, error) {
	ids, err := p.store.IdentitiesOf(ctx, account)
	if err != nil {
		return "", fmt.Errorf("find the identity of the account: %w", err)
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
	p.accounts[account]++
}

// Refused drops the permission of the account on repo: the forge refused a
// call about it, a call of the service or of a caller of epic 5 or later.
func (p *PermissionService) Refused(account string, repo Repository) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, permissionKey{account: account, forge: repo.Forge, repository: repo.ID})
	p.accounts[account]++
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
	p.identities[key]++
}
