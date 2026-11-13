package identity

import (
	"context"
	"errors"
	"time"
)

// Errors of the stores of this package.
var (
	// ErrNotFound: no such login state, sign-up, account or tokens.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken: an account or a workspace already has the name: they
	// share one namespace.
	ErrNameTaken = errors.New("name taken")
	// ErrIdentityTaken: the forge identity already belongs to an account.
	ErrIdentityTaken = errors.New("identity already has an account")
)

// Signup is a sign-up in progress: a person the forge identified and the
// rule allowed, who has not chosen a name yet. It belongs to one session and
// lives SignupLifetime.
type Signup struct {
	// Session is the hash of the session the sign-up belongs to.
	Session    string
	Identity   Identity
	Tokens     Tokens
	Proposed   string
	ReturnPath string
	ExpiresAt  time.Time
}

// SignupLifetime is how long a person has to choose a name.
const SignupLifetime = 10 * time.Minute

// Store keeps accounts, forge identities, their tokens, login states and
// sign-ups. Tokens are stored encrypted. Every store passes the contract
// suite of package identitytest.
type Store interface {
	// PutLoginState stores s.
	PutLoginState(ctx context.Context, s LoginState) error
	// TakeLoginState removes the login state with this hash and returns it
	// when it belongs to the session with the hash session and has not
	// expired at now; ErrNotFound otherwise. Whatever the answer, a state
	// is never returned twice.
	TakeLoginState(ctx context.Context, hash, session string, now time.Time) (LoginState, error)
	// DeleteExpiredLoginStates removes the login states expired at now.
	DeleteExpiredLoginStates(ctx context.Context, now time.Time) (int, error)

	// PutSignup stores s, replacing a sign-up of the same session.
	PutSignup(ctx context.Context, s Signup) error
	// Signup returns the sign-up of the session with the hash session, if
	// it has not expired at now; ErrNotFound otherwise.
	Signup(ctx context.Context, session string, now time.Time) (Signup, error)
	// CompleteSignup removes the sign-up of the session, if it has not
	// expired at now, and creates account with its identity, its tokens and
	// its personal workspace (the identifier and the name of the account,
	// the account its owner), in one operation. ErrNotFound without a
	// sign-up, ErrNameTaken when an account or a workspace has the name,
	// ErrIdentityTaken when another account has the identity; nothing
	// changes then.
	CompleteSignup(ctx context.Context, session string, account Account, now time.Time) (Signup, error)
	// DeleteExpiredSignups removes the sign-ups expired at now.
	DeleteExpiredSignups(ctx context.Context, now time.Time) (int, error)

	// AccountOf returns the account of the forge identity with this key,
	// as Identity.Key gives it; ErrNotFound if none.
	AccountOf(ctx context.Context, key string) (Account, error)
	// Account returns the account with this identifier; ErrNotFound if
	// none.
	Account(ctx context.Context, id string) (Account, error)
	// IdentitiesOf returns the forge identities of the account with this
	// identifier, sorted by forge then identifier; none for an account
	// without one.
	IdentitiesOf(ctx context.Context, account string) ([]Identity, error)
	// UpdateIdentity stores the login and the name the forge gave id at a
	// login; ErrNotFound if the identity has no account.
	UpdateIdentity(ctx context.Context, id Identity) error

	// Tokens returns the tokens of the identity with this key; ErrNotFound
	// if none.
	Tokens(ctx context.Context, key string) (Tokens, error)
	// PutTokens stores the tokens of an identity that has an account;
	// ErrNotFound otherwise.
	PutTokens(ctx context.Context, key string, t Tokens) error
	// RevokeTokens deletes the tokens of the identity with this key and ends
	// every session of its account, in one operation: Raun can no longer
	// check what the person may do. The account and the identity stay.
	RevokeTokens(ctx context.Context, key string) error
}
