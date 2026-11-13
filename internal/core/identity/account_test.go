package identity

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	for name, want := range map[string]error{
		"a":                     nil,
		"alice":                 nil,
		"alice-martin":          nil,
		"a1-b2-c3":              nil,
		"0":                     nil,
		strings.Repeat("a", 39): nil,
		strings.Repeat("a", 40): ErrInvalidName,
		"":                      ErrInvalidName,
		"Alice":                 ErrInvalidName,
		"al ice":                ErrInvalidName,
		"al_ice":                ErrInvalidName,
		"alice.m":               ErrInvalidName,
		"élise":                 ErrInvalidName,
		"-alice":                ErrInvalidName,
		"alice-":                ErrInvalidName,
		"al--ice":               ErrInvalidName,
		"login":                 ErrReservedName,
		"logout":                ErrReservedName,
		"assets":                ErrReservedName,
		"admin":                 ErrReservedName,
		"github":                ErrReservedName,
		"workspaces":            ErrReservedName,
	} {
		err := CheckName(name)
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("CheckName(%q) = %v, want %v", name, err, want)
		}
	}
}

// TestEveryReservedNameIsRefused: each name kept for Raun is refused, and
// passes the rule of names otherwise, so it could not be taken by mistake.
func TestEveryReservedNameIsRefused(t *testing.T) {
	for _, name := range reserved {
		if err := CheckName(name); !errors.Is(err, ErrReservedName) {
			t.Errorf("CheckName(%q) = %v, want ErrReservedName", name, err)
		}
	}
}

func TestProposeName(t *testing.T) {
	for login, want := range map[string]string{
		"Alice":       "alice",
		"alice-m":     "alice-m",
		"admin":       "",
		"al--ice":     "",
		"Mona_Lisa":   "",
		"OctoCat-007": "octocat-007",
	} {
		if got := ProposeName(login); got != want {
			t.Errorf("ProposeName(%q) = %q, want %q", login, got, want)
		}
	}
}

func TestNewAccountID(t *testing.T) {
	id, err := newAccountID(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)))
	if err != nil || id != strings.Repeat("ab", 16) {
		t.Errorf("newAccountID = %q, %v", id, err)
	}
	if _, err := newAccountID(bytes.NewReader(nil)); err == nil {
		t.Error("newAccountID without randomness succeeded")
	}
}
