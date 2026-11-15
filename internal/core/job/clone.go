package job

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// redacted replaces a secret wherever it would be shown.
const redacted = "[redacted]"

// Secret is a value no log, error or rendering may hold: it prints as
// [redacted] in every format, as a log value and as text, as the secrets of
// the identity context do. Only Reveal gives its value, to the one place
// that sends it: the answer to a claim.
type Secret string

// Reveal returns the value of s.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer: [redacted].
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer: [redacted].
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter: [redacted] for every verb.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue implements slog.LogValuer: [redacted].
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, which JSON uses too:
// [redacted].
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Credential is what fetches the repository of one job: the clone
// credential of epic 6, minted for one run, held in memory only and revoked
// when the run ends.
type Credential struct {
	// URL is the address Git fetches from, holding no secret.
	URL string
	// FullName is the full name of the repository on the forge, owner/name.
	FullName string
	// Username and Password are the HTTP credentials of the fetch.
	Username string
	Password Secret
	// Expires is when the forge stops accepting it.
	Expires time.Time
}

// Clone mints and revokes clone credentials: the optional Clone capability
// of a forge, which the job context consumes (epic 6 record, Clone
// credential). Every forge that has it passes the contract suite of package
// forgetest.
type Clone interface {
	// MintClone returns a new credential that reads the repository with
	// this stable identifier, and nothing else, for one hour at most. Each
	// call mints a credential of its own, apart from any the forge adapter
	// keeps for the server.
	MintClone(ctx context.Context, repository string) (Credential, error)
	// RevokeClone makes the forge refuse c from now on. A credential the
	// forge already refuses, expired or revoked, is revoked: nil.
	RevokeClone(ctx context.Context, c Credential) error
}
