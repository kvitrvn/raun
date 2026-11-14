package github

import (
	"crypto/rand"
	"crypto/rsa"
	"os"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// testKey generates the private key of a test App.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// fixture is the scenario of the contract suite on a scripted GitHub.
func fixture(t *testing.T, key *rsa.PrivateKey) (*gitHub, forgetest.Fixture) {
	t.Helper()
	g := newGitHub(t, key)
	return g, forgetest.Fixture{
		Forge:    g.app(t, key, nil),
		Alice:    "1001",
		Org:      "3001",
		OtherOrg: "3002",
		Unreachable: []identity.Repository{
			{Forge: Name, ID: "2003", FullName: "acme/secret", DefaultBranch: "main", Private: true},
			{Forge: Name, ID: "2004", FullName: "other/lib", DefaultBranch: "trunk"},
		},
		Reached: []string{"2001", "2002", "2003"},
		Push: func(t *testing.T) (map[string]string, []byte) {
			t.Helper()
			body, err := os.ReadFile("testdata/hooks/push-branch.json")
			if err != nil {
				t.Fatal(err)
			}
			return map[string]string{headerEvent: "push", headerDelivery: "delivery-1", headerSignature: sign(webhookSecret, body)}, body
		},
		Approve: func(t *testing.T, url string) (string, string) {
			t.Helper()
			code, state, err := g.approve(1001, url)
			if err != nil {
				t.Fatal(err)
			}
			return code, state
		},
		ExpireCodes:   func(*testing.T) { g.clock.Advance(codeLifetime) },
		ExpireAccess:  func(*testing.T) { g.clock.Advance(accessLifetime + time.Second) },
		ExpireRefresh: func(*testing.T) { g.clock.Advance(refreshLifetime + time.Second) },
		Revoke:        func(*testing.T) { g.revoke(1001) },
		SetDown:       func(_ *testing.T, down bool) { g.change(func() { g.down = down }) },
		RenameAlice:   func(_ *testing.T, login string) { g.change(func() { g.people[1001].login = login }) },
	}
}

// TestContract runs the contract of the forge port on the adapter, against
// a scripted GitHub over TLS.
func TestContract(t *testing.T) {
	key := testKey(t)
	forgetest.Suite(t, func(t *testing.T) forgetest.Fixture {
		_, fx := fixture(t, key)
		return fx
	})
}
