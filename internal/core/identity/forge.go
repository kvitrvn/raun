package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// Errors of a forge. An adapter wraps the cause in one of them, so a caller
// tells what to do without knowing the forge.
var (
	// ErrUnauthorized: the forge refuses the access token, expired or not;
	// the caller refreshes the tokens and tries again.
	ErrUnauthorized = errors.New("forge refuses the access token")
	// ErrRevoked: the forge refuses the refresh token or the authorization
	// for good: the person must log in again.
	ErrRevoked = errors.New("forge revoked the authorization")
	// ErrUnavailable: the forge cannot answer now (network, server error,
	// rate limit); the same call may succeed later.
	ErrUnavailable = errors.New("forge unavailable")
	// ErrInvalidCode: the code of an authorization is unknown, used,
	// expired, or does not match the PKCE verifier.
	ErrInvalidCode = errors.New("invalid authorization code")
)

// Identity is a person as a forge knows them. ID is the stable identifier the
// forge gives the person, which never changes; Login and Name are what the
// forge said at the last login.
type Identity struct {
	Forge string
	ID    string
	Login string
	Name  string
}

// Key is the form of the identity in the configuration: forge:id, such as
// github:583231.
func (i Identity) Key() string {
	return i.Forge + ":" + i.ID
}

// Secret is a value that must never be printed: a token. Every format, log
// value and text encoding of it gives [redacted], without an error; Reveal
// gives the value, for the adapter that sends it and the store that encrypts
// it. A store encrypts Reveal, never an encoding of Tokens, which would keep
// [redacted] instead of the token.
type Secret string

const redacted = "[redacted]"

// Reveal returns the value of the secret.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter, so no verb prints the value.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, so JSON and other text
// encodings print no value either.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Tokens are what a forge gives a person who authorizes Raun.
type Tokens struct {
	Access        Secret
	AccessExpiry  time.Time
	Refresh       Secret
	RefreshExpiry time.Time
}

// Repository is a repository as a forge knows it. ID is its stable
// identifier; FullName is owner/name, which can change.
type Repository struct {
	Forge         string
	ID            string
	FullName      string
	DefaultBranch string
	Private       bool
}

// Permission is what a person may do on a repository in Raun.
type Permission string

const (
	// PermissionNone: the person may not see the repository's knowledge.
	PermissionNone Permission = "none"
	// PermissionView: the person may see it.
	PermissionView Permission = "view"
	// PermissionDecide: the person may also decide on it.
	PermissionDecide Permission = "decide"
)

// Forge is the port every forge adapter implements.
type Forge interface {
	// Name is the name the server gives this forge, the prefix of the
	// identities of the configuration: github.
	Name() string
	// AuthorizeURL is where a person approves Raun on the forge, carrying
	// state and the S256 PKCE challenge, and coming back to the callback
	// of the server.
	AuthorizeURL(state, challenge string) string
	// Exchange trades the code the forge sent back for the person's
	// identity and tokens, proving the PKCE verifier. ErrInvalidCode if the
	// forge refuses the code.
	Exchange(ctx context.Context, code, verifier string) (Identity, Tokens, error)
	// Refresh gives new tokens for the refresh token of t. ErrRevoked if the
	// forge refuses it for good, ErrUnavailable if it cannot answer now.
	Refresh(ctx context.Context, t Tokens) (Tokens, error)
	// Repositories are the repositories the person of t can reach through
	// Raun, sorted by full name. ErrUnauthorized if the forge refuses the
	// access token, ErrUnavailable if it cannot answer now.
	Repositories(ctx context.Context, t Tokens) ([]Repository, error)
	// Permission is what the person of t may do on repo, a repository of
	// this forge: the caller routes by repo.Forge. It is mapped by the
	// table of docs/forge.md; a repository the person cannot reach through
	// Raun is PermissionNone, not an error. ErrUnauthorized if the forge
	// refuses the access token, ErrUnavailable if it cannot answer now.
	Permission(ctx context.Context, t Tokens, repo Repository) (Permission, error)
}

// Memberships is the optional capability of a forge that tells whether a
// person belongs to an organization, for RAUN_SIGNUP_ORGS.
type Memberships interface {
	// IsMember reports whether the person of t belongs to the organization
	// whose stable identifier is org. ErrUnauthorized if the forge refuses
	// the access token, ErrUnavailable if it cannot answer now.
	IsMember(ctx context.Context, t Tokens, org string) (bool, error)
}

// MembershipsOf returns the membership capability of f, if it has one.
func MembershipsOf(f Forge) (Memberships, bool) {
	m, ok := f.(Memberships)
	return m, ok
}

// verifierBytes is the entropy of a PKCE verifier: 32 bytes give 43
// characters, the minimum of RFC 7636.
const verifierBytes = 32

// NewVerifier draws a PKCE code verifier from random.
func NewVerifier(random io.Reader) (string, error) {
	b := make([]byte, verifierBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", fmt.Errorf("draw a PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge is the S256 PKCE challenge of verifier (RFC 7636).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
