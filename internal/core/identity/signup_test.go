package identity_test

import (
	"context"
	"errors"
	"github.com/kvitrvn/raun/internal/core/identity"
	"strings"
	"testing"
	"time"
)

func TestParseEntries(t *testing.T) {
	got, err := identity.ParseEntries(" github:583231 , gitea:alice.m,github:1")
	if err != nil || len(got) != 3 || got[0] != (identity.Entry{"github", "583231"}) || got[1] != (identity.Entry{"gitea", "alice.m"}) || got[2].String() != "github:1" {
		t.Errorf("ParseEntries = %v, %v", got, err)
	}
	if got, err := identity.ParseEntries("  "); err != nil || got != nil {
		t.Errorf("ParseEntries of an empty value = %v, %v", got, err)
	}
	for _, bad := range []string{"583231", "github:", ":1", "GitHub:1", "github:1,", "github:1;github:2", "github:a b", "github:" + strings.Repeat("1", 65)} {
		if _, err := identity.ParseEntries(bad); err == nil {
			t.Errorf("ParseEntries(%q) accepted", bad)
		}
	}
}

func TestPolicy(t *testing.T) {
	ctx := context.Background()
	f := newStubForge(time.Now)
	alice, bob := f.people["alice"], f.people["bob"]
	aliceTokens, bobTokens := f.issue(alice.ID), f.issue(bob.ID)
	for _, tc := range []struct {
		name   string
		policy identity.Policy
		forge  identity.Forge
		id     identity.Identity
		tokens identity.Tokens
		ok     bool
	}{
		{"empty lists admit nobody", identity.Policy{}, f, alice, aliceTokens, false},
		{"a listed user", identity.Policy{Users: []identity.Entry{{"stub", "1001"}}}, f, alice, aliceTokens, true},
		{"an administrator", identity.Policy{Admins: []identity.Entry{{"stub", "1001"}}}, f, alice, aliceTokens, true},
		{"a user of another forge", identity.Policy{Users: []identity.Entry{{"other", "1001"}}}, f, alice, aliceTokens, false},
		{"a member of a listed organization", identity.Policy{Orgs: []identity.Entry{{"stub", "9999"}, {"stub", "3001"}}}, f, alice, aliceTokens, true},
		{"someone outside the organization", identity.Policy{Orgs: []identity.Entry{{"stub", "3001"}}}, f, bob, bobTokens, false},
		{"an organization of another forge", identity.Policy{Orgs: []identity.Entry{{"other", "3001"}}}, f, alice, aliceTokens, false},
		{"a forge without memberships", identity.Policy{Orgs: []identity.Entry{{"stub", "3001"}}}, bareStub{f}, alice, aliceTokens, false},
	} {
		err := tc.policy.Allow(ctx, tc.forge, tc.id, tc.tokens)
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, identity.ErrNotAllowed) {
			t.Errorf("%s: Allow = %v, want ok %v", tc.name, err, tc.ok)
		}
		if err != nil && strings.Contains(err.Error(), "3001") {
			t.Errorf("%s: the refusal %q lists an organization", tc.name, err)
		}
	}

	f.memberErr = identity.ErrUnavailable
	err := identity.Policy{Orgs: []identity.Entry{{"stub", "3001"}}}.Allow(ctx, f, alice, aliceTokens)
	if !errors.Is(err, identity.ErrNotAllowed) || !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("Allow while the forge is down = %v, want a refusal that wraps identity.ErrUnavailable", err)
	}
}
