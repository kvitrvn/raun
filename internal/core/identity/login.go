package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// LoginStateLifetime is how long a login state waits for the forge.
const LoginStateLifetime = 10 * time.Minute

// stateBytes is the entropy of a login state.
const stateBytes = 32

// LoginState is what Raun keeps of a login in progress: the hash of the state
// it sent the forge, never the state, the PKCE verifier, the session the
// login belongs to and where to go back.
type LoginState struct {
	Hash       string
	Verifier   string
	Session    string
	ReturnPath string
	ExpiresAt  time.Time
}

// ErrInvalidState: the state of a callback is malformed, unknown, used,
// expired, or belongs to another session.
var ErrInvalidState = errors.New("invalid login state")

// NewLoginState draws a state and a verifier for a login of the session
// with the hash session at now. It returns the state, for the forge, and the
// row to store.
func NewLoginState(random io.Reader, now time.Time, session, returnPath string) (string, LoginState, error) {
	b := make([]byte, stateBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", LoginState{}, fmt.Errorf("draw a login state: %w", err)
	}
	verifier, err := NewVerifier(random)
	if err != nil {
		return "", LoginState{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	return state, LoginState{
		Hash:       hashState(state),
		Verifier:   verifier,
		Session:    session,
		ReturnPath: SafeReturnPath(returnPath),
		ExpiresAt:  now.UTC().Add(LoginStateLifetime),
	}, nil
}

// HashState checks that state is one NewLoginState draws and returns the
// hash it is stored by; ErrInvalidState otherwise.
func HashState(state string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil || len(raw) != stateBytes || base64.RawURLEncoding.EncodeToString(raw) != state {
		return "", ErrInvalidState
	}
	return hashState(state), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// SafeReturnPath returns p when it is a path of the instance, and / for
// anything else: p starts with /, its second character is neither / nor \,
// it holds no backslash and no control character, and parsed as a URL it
// has neither scheme nor host.
func SafeReturnPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsRune(p, '\\') ||
		strings.ContainsFunc(p, unicode.IsControl) {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return "/"
	}
	return p
}

// Login logs people in through a forge: it starts the authorization, checks
// the callback, applies the sign-up rule, and creates accounts. It is safe
// for concurrent use.
type Login struct {
	forge  Forge
	policy Policy
	store  Store
	random io.Reader
	now    func() time.Time
}

// NewLogin returns the login of forge, under policy, on store, drawing
// states and identifiers from random at the time now gives.
func NewLogin(forge Forge, policy Policy, store Store, random io.Reader, now func() time.Time) *Login {
	return &Login{forge: forge, policy: policy, store: store, random: random, now: now}
}

// Forge returns the forge of l.
func (l *Login) Forge() Forge { return l.forge }

// Begin starts a login of the session with the hash session, which goes back
// to returnPath once done, and returns the authorization URL of the forge.
func (l *Login) Begin(ctx context.Context, session, returnPath string) (string, error) {
	state, row, err := NewLoginState(l.random, l.now(), session, returnPath)
	if err != nil {
		return "", err
	}
	if err := l.store.PutLoginState(ctx, row); err != nil {
		return "", fmt.Errorf("store the login state: %w", err)
	}
	return l.forge.AuthorizeURL(state, Challenge(row.Verifier)), nil
}

// Outcome is where a callback leads: an account logged in, or a sign-up that
// waits for a name.
type Outcome struct {
	Account    Account
	Signup     bool
	ReturnPath string
}

// Complete checks the callback of the forge for the session with the hash
// session: the state is taken first, so it serves once whatever follows,
// then the code is exchanged and the sign-up rule applied, at every login. A
// known identity logs in to its account; a new one gets a sign-up of the
// session. ErrInvalidState for a state that is not this session's;
// ErrNotAllowed when the rule refuses; the errors of the forge otherwise.
func (l *Login) Complete(ctx context.Context, session, state, code string) (Outcome, error) {
	hash, err := HashState(state)
	if err != nil {
		return Outcome{}, err
	}
	now := l.now()
	row, err := l.store.TakeLoginState(ctx, hash, session, now)
	if errors.Is(err, ErrNotFound) {
		return Outcome{}, ErrInvalidState
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("take the login state: %w", err)
	}
	id, tokens, err := l.forge.Exchange(ctx, code, row.Verifier)
	if err != nil {
		return Outcome{}, err
	}
	if err := l.policy.Allow(ctx, l.forge, id, tokens); err != nil {
		return Outcome{}, err
	}
	account, err := l.store.AccountOf(ctx, id.Key())
	switch {
	case err == nil:
		if err := l.store.UpdateIdentity(ctx, id); err != nil {
			return Outcome{}, fmt.Errorf("update the identity: %w", err)
		}
		if err := l.store.PutTokens(ctx, id.Key(), tokens); err != nil {
			return Outcome{}, fmt.Errorf("store the tokens: %w", err)
		}
		return Outcome{Account: account, ReturnPath: row.ReturnPath}, nil
	case errors.Is(err, ErrNotFound):
		err := l.store.PutSignup(ctx, Signup{
			Session:    session,
			Identity:   id,
			Tokens:     tokens,
			Proposed:   ProposeName(id.Login),
			ReturnPath: row.ReturnPath,
			ExpiresAt:  now.UTC().Add(SignupLifetime),
		})
		if err != nil {
			return Outcome{}, fmt.Errorf("store the sign-up: %w", err)
		}
		return Outcome{Signup: true, ReturnPath: row.ReturnPath}, nil
	default:
		return Outcome{}, fmt.Errorf("find the account: %w", err)
	}
}

// Signup returns the sign-up of the session with the hash session;
// ErrNotFound if it has none or it expired.
func (l *Login) Signup(ctx context.Context, session string) (Signup, error) {
	return l.store.Signup(ctx, session, l.now())
}

// FinishSignup creates the account of the sign-up of the session with the
// hash session, under name, lowercased. It returns the account and where to
// go back. ErrInvalidName or ErrReservedName when the name breaks the rule,
// ErrNameTaken when another account has it, ErrNotFound when the sign-up
// expired, ErrIdentityTaken when the identity got an account meanwhile.
func (l *Login) FinishSignup(ctx context.Context, session, name string) (Account, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := CheckName(name); err != nil {
		return Account{}, "", err
	}
	id, err := newAccountID(l.random)
	if err != nil {
		return Account{}, "", err
	}
	now := l.now()
	account := Account{ID: id, Name: name, CreatedAt: now.UTC()}
	s, err := l.store.CompleteSignup(ctx, session, account, now)
	if err != nil {
		return Account{}, "", err
	}
	return account, s.ReturnPath, nil
}
