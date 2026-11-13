package identity

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// Account is a person of Raun. ID is drawn at sign-up and never changes;
// Name is the person's name in the namespace, chosen at sign-up and
// permanent in epic 4.
type Account struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// accountIDBytes is the entropy of an account identifier.
const accountIDBytes = 16

// newAccountID draws the identifier of an account: 32 hexadecimal digits.
func newAccountID(random io.Reader) (string, error) {
	b := make([]byte, accountIDBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", fmt.Errorf("draw an account identifier: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MaxNameLength is the longest name, the bound GitHub sets on its logins.
const MaxNameLength = 39

var (
	// ErrInvalidName: the name breaks the rule of the namespace.
	ErrInvalidName = errors.New("invalid name")
	// ErrReservedName: the name is a path of the application or kept for it.
	ErrReservedName = errors.New("reserved name")
)

// reserved are the names no account takes: the first segment of every route
// of the application, and the names kept by the epic 4 record. #27 extends
// the list with its routes and adds the test that keeps it in step.
var reserved = []string{
	// Routes.
	"assets", "healthz", "instance", "login", "logout", "new", "readyz", "workspaces",
	// Kept.
	"about", "admin", "api", "help", "raun", "settings",
	// Forges.
	"forgejo", "gitea", "github", "gitlab",
}

// CheckName checks name against the rule of the namespace: 1 to 39
// characters among lowercase letters, digits and hyphens, neither starting
// nor ending with a hyphen, without two hyphens in a row, and not reserved.
// Its errors wrap ErrInvalidName or ErrReservedName and say what to change.
func CheckName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: a name has 1 character at least", ErrInvalidName)
	case len(name) > MaxNameLength:
		return fmt.Errorf("%w: a name has %d characters at most", ErrInvalidName, MaxNameLength)
	case strings.ContainsFunc(name, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' }):
		return fmt.Errorf("%w: a name holds lowercase letters, digits and hyphens only", ErrInvalidName)
	case name[0] == '-' || name[len(name)-1] == '-':
		return fmt.Errorf("%w: a name neither starts nor ends with a hyphen", ErrInvalidName)
	case strings.Contains(name, "--"):
		return fmt.Errorf("%w: a name holds no two hyphens in a row", ErrInvalidName)
	case slices.Contains(reserved, name):
		return fmt.Errorf("%w: %q is kept for Raun", ErrReservedName, name)
	}
	return nil
}

// ProposeName is the name proposed at sign-up for a forge login: the login
// lowercased, when it passes CheckName, or nothing.
func ProposeName(login string) string {
	name := strings.ToLower(login)
	if CheckName(name) != nil {
		return ""
	}
	return name
}
