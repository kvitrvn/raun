package identity

import (
	"context"
	"fmt"
	"slices"
)

// Administrators tells the administrators of the instance: the accounts one
// of whose forge identities is listed in RAUN_ADMINS. The role is worked out
// at every call from the identities of the account, never stored, so an
// account removed from the list loses it at its next request. It grants
// configuring the instance and nothing on repository data: the forge decides
// that, through the PermissionService. It is safe for concurrent use.
type Administrators struct {
	entries    []Entry
	identities IdentityLister
}

// NewAdministrators returns the administrators named by entries, looking up
// the identities of accounts in identities.
func NewAdministrators(entries []Entry, identities IdentityLister) *Administrators {
	return &Administrators{entries: slices.Clone(entries), identities: identities}
}

// IsAdministrator reports whether the account with this identifier is an
// administrator. An empty list makes nobody one. An error of the store is
// returned with false: a role that cannot be established is not granted.
func (a *Administrators) IsAdministrator(ctx context.Context, account string) (bool, error) {
	if account == "" || len(a.entries) == 0 {
		return false, nil
	}
	ids, err := a.identities.IdentitiesOf(ctx, account)
	if err != nil {
		return false, fmt.Errorf("read the identities of the account: %w", err)
	}
	for _, id := range ids {
		if slices.Contains(a.entries, Entry{Forge: id.Forge, ID: id.ID}) {
			return true, nil
		}
	}
	return false, nil
}
