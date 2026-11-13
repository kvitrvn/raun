package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Entry is a forge identity named in the configuration: a forge and the
// stable identifier the forge gives a person or an organization, written
// forge:id, such as github:583231.
type Entry struct {
	Forge string
	ID    string
}

func (e Entry) String() string { return e.Forge + ":" + e.ID }

var entryForm = regexp.MustCompile(`^([a-z][a-z0-9]*):([A-Za-z0-9._-]{1,64})$`)

// ParseEntries reads a list of entries separated by commas, spaces around
// them ignored; an empty value is no entry. A login in place of an
// identifier passes the form on forges whose identifiers are not numbers:
// the documentation says how to find the identifier.
func ParseEntries(value string) ([]Entry, error) {
	var out []Entry
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		m := entryForm.FindStringSubmatch(raw)
		if m == nil {
			return nil, fmt.Errorf("%q is not of the form forge:identifier, such as github:583231", raw)
		}
		out = append(out, Entry{Forge: m[1], ID: m[2]})
	}
	return out, nil
}

// ErrNotAllowed: the sign-up rule refuses the person.
var ErrNotAllowed = errors.New("not allowed to sign up")

// Refusal is a refusal of the sign-up rule. Reason says which rule refused,
// for the person, and never lists the allowed organizations. It wraps
// ErrNotAllowed, and the error of the forge when a membership could not be
// checked.
type Refusal struct {
	Reason string
	cause  error
}

func (r *Refusal) Error() string {
	if r.cause != nil {
		return ErrNotAllowed.Error() + ": " + r.Reason + ": " + r.cause.Error()
	}
	return ErrNotAllowed.Error() + ": " + r.Reason
}

func (r *Refusal) Unwrap() []error {
	if r.cause != nil {
		return []error{ErrNotAllowed, r.cause}
	}
	return []error{ErrNotAllowed}
}

// Policy is the sign-up rule of the instance: a person may sign up and log
// in when their identity is listed in Users or Admins, or they belong to an
// organization listed in Orgs. Empty lists admit nobody.
type Policy struct {
	Users  []Entry
	Orgs   []Entry
	Admins []Entry
}

// Allow checks the identity id of the forge f, whose tokens are t, against
// p. A refusal is a *Refusal; a membership that cannot be established is a
// refusal too, wrapping the error of the forge, never an acceptance.
func (p Policy) Allow(ctx context.Context, f Forge, id Identity, t Tokens) error {
	me := Entry{Forge: id.Forge, ID: id.ID}
	for _, list := range [][]Entry{p.Users, p.Admins} {
		for _, e := range list {
			if e == me {
				return nil
			}
		}
	}
	var orgs []string
	for _, e := range p.Orgs {
		if e.Forge == id.Forge {
			orgs = append(orgs, e.ID)
		}
	}
	if len(orgs) > 0 {
		m, ok := MembershipsOf(f)
		if !ok {
			return &Refusal{Reason: fmt.Sprintf("Raun cannot tell the members of an organization on %s", f.Name())}
		}
		for _, org := range orgs {
			in, err := m.IsMember(ctx, t, org)
			if err != nil {
				return &Refusal{Reason: "the membership of your organizations could not be checked; try again in a moment", cause: err}
			}
			if in {
				return nil
			}
		}
	}
	return &Refusal{Reason: fmt.Sprintf("this %s account is not listed in RAUN_SIGNUP_USERS or RAUN_ADMINS, and is not a member of an organization listed in RAUN_SIGNUP_ORGS; ask the administrator of this instance to add it", f.Name())}
}
