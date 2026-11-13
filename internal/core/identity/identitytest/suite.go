package identitytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Fixture is a store under test, empty, and a way to give an account a
// session of the session store RevokeTokens ends sessions in.
type Fixture struct {
	Store identity.Store
	// Workspaces is the workspace store of the same data as Store.
	Workspaces identity.WorkspaceStore
	// StartSession gives account a live session and returns a function
	// that tells whether that session still lives.
	StartSession func(t *testing.T, account string) func() bool
}

// t0Unix is the time of the suite, in Unix seconds.
const t0Unix = 1794564000

func t0Time() time.Time { return time.Unix(t0Unix, 0).UTC() }

// hash returns a session hash of 64 hexadecimal digits for n.
func hash(n byte) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = digits[(int(n)+i)%16]
	}
	return string(b)
}

func alice() identity.Identity {
	return identity.Identity{Forge: "github", ID: "1001", Login: "alice", Name: "Alice Martin"}
}

func tokens(n string) identity.Tokens {
	return identity.Tokens{
		Access: identity.Secret("access-" + n), AccessExpiry: t0Time().Add(8 * time.Hour),
		Refresh: identity.Secret("refresh-" + n), RefreshExpiry: t0Time().Add(4000 * time.Hour),
	}
}

func signup(session string, id identity.Identity, t identity.Tokens) identity.Signup {
	return identity.Signup{Session: session, Identity: id, Tokens: t, Proposed: id.Login, ReturnPath: "/back", ExpiresAt: t0Time().Add(identity.SignupLifetime)}
}

// accountID gives the short name n the 32 hexadecimal digits of an account
// identifier.
func accountID(n string) string {
	return strings.Repeat("0", 32-len(n)) + n
}

func account(n, name string) identity.Account {
	return identity.Account{ID: accountID(n), Name: name, CreatedAt: t0Time()}
}

func sameTokens(a, b identity.Tokens) bool {
	return a.Access.Reveal() == b.Access.Reveal() && a.Refresh.Reveal() == b.Refresh.Reveal() &&
		a.AccessExpiry.Equal(b.AccessExpiry) && a.RefreshExpiry.Equal(b.RefreshExpiry)
}

// signedUp gives the store an account for id, with tokens t.
func signedUp(t *testing.T, s identity.Store, session string, id identity.Identity, a identity.Account, tk identity.Tokens) {
	t.Helper()
	ctx := context.Background()
	if err := s.PutSignup(ctx, signup(session, id, tk)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteSignup(ctx, session, a, t0Time()); err != nil {
		t.Fatal(err)
	}
}

// Suite runs the contract of the store port on the fixtures newFixture
// builds, one per case.
func Suite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()

	t.Run("a login state serves once, to its session, before it expires", func(t *testing.T) {
		s := newFixture(t).Store
		st := identity.LoginState{Hash: hash(1), Verifier: "verifier", Session: hash(2), ReturnPath: "/r", ExpiresAt: t0Time().Add(identity.LoginStateLifetime)}
		if err := s.PutLoginState(ctx, st); err != nil {
			t.Fatal(err)
		}
		got, err := s.TakeLoginState(ctx, st.Hash, st.Session, t0Time())
		if err != nil || got != st {
			t.Fatalf("TakeLoginState = %+v, %v", got, err)
		}
		if _, err := s.TakeLoginState(ctx, st.Hash, st.Session, t0Time()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("second TakeLoginState = %v, want ErrNotFound", err)
		}
		for name, take := range map[string]func() error{
			"another session": func() error { _, err := s.TakeLoginState(ctx, st.Hash, hash(3), t0Time()); return err },
			"expired": func() error {
				_, err := s.TakeLoginState(ctx, st.Hash, st.Session, st.ExpiresAt)
				return err
			},
		} {
			if err := s.PutLoginState(ctx, st); err != nil {
				t.Fatal(err)
			}
			if err := take(); !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("%s: TakeLoginState = %v, want ErrNotFound", name, err)
			}
			// Refused or not, the state is gone.
			if _, err := s.TakeLoginState(ctx, st.Hash, st.Session, t0Time()); !errors.Is(err, identity.ErrNotFound) {
				t.Errorf("%s: the state survived a refused take: %v", name, err)
			}
		}
	})

	t.Run("expired login states and sign-ups are deleted", func(t *testing.T) {
		s := newFixture(t).Store
		for i, d := range []time.Duration{time.Minute, 2 * time.Minute, time.Hour} {
			if err := s.PutLoginState(ctx, identity.LoginState{Hash: hash(byte(10 + i)), Verifier: "v", Session: hash(2), ReturnPath: "/", ExpiresAt: t0Time().Add(d)}); err != nil {
				t.Fatal(err)
			}
			sg := signup(hash(byte(20+i)), alice(), tokens("x"))
			sg.ExpiresAt = t0Time().Add(d)
			if err := s.PutSignup(ctx, sg); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := s.DeleteExpiredLoginStates(ctx, t0Time().Add(2*time.Minute)); err != nil || n != 2 {
			t.Errorf("DeleteExpiredLoginStates = %d, %v; want 2", n, err)
		}
		if n, err := s.DeleteExpiredSignups(ctx, t0Time().Add(2*time.Minute)); err != nil || n != 2 {
			t.Errorf("DeleteExpiredSignups = %d, %v; want 2", n, err)
		}
		if _, err := s.TakeLoginState(ctx, hash(12), hash(2), t0Time()); err != nil {
			t.Errorf("a live login state was deleted: %v", err)
		}
		if _, err := s.Signup(ctx, hash(22), t0Time()); err != nil {
			t.Errorf("a live sign-up was deleted: %v", err)
		}
	})

	t.Run("a sign-up belongs to its session and expires", func(t *testing.T) {
		s := newFixture(t).Store
		sg := signup(hash(1), alice(), tokens("1"))
		if err := s.PutSignup(ctx, sg); err != nil {
			t.Fatal(err)
		}
		got, err := s.Signup(ctx, hash(1), t0Time())
		if err != nil || got.Identity != sg.Identity || got.Proposed != "alice" || got.ReturnPath != "/back" || !sameTokens(got.Tokens, sg.Tokens) || !got.ExpiresAt.Equal(sg.ExpiresAt) {
			t.Fatalf("Signup = %+v, %v", got, err)
		}
		if _, err := s.Signup(ctx, hash(2), t0Time()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("Signup of another session = %v", err)
		}
		if _, err := s.Signup(ctx, hash(1), sg.ExpiresAt); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("Signup once expired = %v", err)
		}
		// A new callback of the same session replaces it.
		again := signup(hash(1), alice(), tokens("2"))
		again.Proposed = "alice-2"
		if err := s.PutSignup(ctx, again); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Signup(ctx, hash(1), t0Time()); got.Proposed != "alice-2" || !sameTokens(got.Tokens, tokens("2")) {
			t.Errorf("Signup after a replacement = %+v", got)
		}
	})

	t.Run("completing a sign-up creates the account once", func(t *testing.T) {
		s := newFixture(t).Store
		if err := s.PutSignup(ctx, signup(hash(1), alice(), tokens("1"))); err != nil {
			t.Fatal(err)
		}
		a := account("a1", "alice")
		got, err := s.CompleteSignup(ctx, hash(1), a, t0Time())
		if err != nil || got.ReturnPath != "/back" {
			t.Fatalf("CompleteSignup = %+v, %v", got, err)
		}
		if _, err := s.CompleteSignup(ctx, hash(1), account("a2", "alice-again"), t0Time()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("second CompleteSignup = %v, want ErrNotFound", err)
		}
		if got, err := s.AccountOf(ctx, alice().Key()); err != nil || got.ID != accountID("a1") || got.Name != "alice" || !got.CreatedAt.Equal(t0Time()) {
			t.Errorf("AccountOf = %+v, %v", got, err)
		}
		if got, err := s.Account(ctx, accountID("a1")); err != nil || got.Name != "alice" {
			t.Errorf("Account = %+v, %v", got, err)
		}
		if got, err := s.Tokens(ctx, alice().Key()); err != nil || !sameTokens(got, tokens("1")) {
			t.Errorf("Tokens = %+v, %v", got, err)
		}
		if _, err := s.Signup(ctx, hash(1), t0Time()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("the sign-up outlived its completion: %v", err)
		}
		if _, err := s.CompleteSignup(ctx, hash(9), account("a3", "nobody"), t0Time()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("CompleteSignup without a sign-up = %v", err)
		}
		if _, err := s.Account(ctx, accountID("a3")); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("Account of nobody = %v", err)
		}
		if _, err := s.AccountOf(ctx, "github:9999"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("AccountOf an unknown identity = %v", err)
		}
	})

	t.Run("an expired sign-up is not completed", func(t *testing.T) {
		s := newFixture(t).Store
		sg := signup(hash(1), alice(), tokens("1"))
		if err := s.PutSignup(ctx, sg); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteSignup(ctx, hash(1), account("a1", "alice"), sg.ExpiresAt); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("CompleteSignup once expired = %v", err)
		}
		if _, err := s.AccountOf(ctx, alice().Key()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("an expired sign-up created an account: %v", err)
		}
	})

	t.Run("a name and an identity belong to one account", func(t *testing.T) {
		s := newFixture(t).Store
		signedUp(t, s, hash(1), alice(), account("a1", "alice"), tokens("1"))
		bob := identity.Identity{Forge: "github", ID: "1002", Login: "bob"}
		if err := s.PutSignup(ctx, signup(hash(2), bob, tokens("2"))); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteSignup(ctx, hash(2), account("a2", "alice"), t0Time()); !errors.Is(err, identity.ErrNameTaken) {
			t.Errorf("CompleteSignup with a taken name = %v, want ErrNameTaken", err)
		}
		// Nothing changed: Bob can still pick another name.
		if _, err := s.CompleteSignup(ctx, hash(2), account("a2", "bob"), t0Time()); err != nil {
			t.Errorf("CompleteSignup after ErrNameTaken = %v", err)
		}
		// A second sign-up of Alice, from another browser.
		if err := s.PutSignup(ctx, signup(hash(3), alice(), tokens("3"))); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteSignup(ctx, hash(3), account("a3", "alice-two"), t0Time()); !errors.Is(err, identity.ErrIdentityTaken) {
			t.Errorf("CompleteSignup of an identity with an account = %v, want ErrIdentityTaken", err)
		}
		if _, err := s.Account(ctx, accountID("a3")); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("a refused sign-up created an account: %v", err)
		}
		if got, _ := s.Tokens(ctx, alice().Key()); !sameTokens(got, tokens("1")) {
			t.Errorf("a refused sign-up replaced the tokens: %+v", got)
		}
	})

	t.Run("the identities of an account", func(t *testing.T) {
		s := newFixture(t).Store
		signedUp(t, s, hash(1), alice(), account("a1", "alice"), tokens("1"))
		bob := identity.Identity{Forge: "github", ID: "1002", Login: "bob", Name: "Bob"}
		signedUp(t, s, hash(2), bob, account("a2", "bob"), tokens("2"))
		if got, err := s.IdentitiesOf(ctx, accountID("a1")); err != nil || len(got) != 1 || got[0] != alice() {
			t.Errorf("IdentitiesOf Alice = %+v, %v", got, err)
		}
		if got, err := s.IdentitiesOf(ctx, accountID("a2")); err != nil || len(got) != 1 || got[0] != bob {
			t.Errorf("IdentitiesOf Bob = %+v, %v", got, err)
		}
		if got, err := s.IdentitiesOf(ctx, accountID("zz")); err != nil || len(got) != 0 {
			t.Errorf("IdentitiesOf nobody = %+v, %v", got, err)
		}
	})

	t.Run("an identity keeps its account across logins", func(t *testing.T) {
		s := newFixture(t).Store
		signedUp(t, s, hash(1), alice(), account("a1", "alice"), tokens("1"))
		renamed := alice()
		renamed.Login, renamed.Name = "alice-renamed", "Alice M."
		if err := s.UpdateIdentity(ctx, renamed); err != nil {
			t.Fatal(err)
		}
		if got, err := s.AccountOf(ctx, renamed.Key()); err != nil || got.Name != "alice" {
			t.Errorf("AccountOf after a rename = %+v, %v; the account keeps its name", got, err)
		}
		if err := s.UpdateIdentity(ctx, identity.Identity{Forge: "github", ID: "9999", Login: "x"}); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("UpdateIdentity of an unknown identity = %v", err)
		}
	})

	workspaceSuite(t, newFixture)

	t.Run("tokens are stored, replaced and revoked", func(t *testing.T) {
		fx := newFixture(t)
		s := fx.Store
		signedUp(t, s, hash(1), alice(), account("a1", "alice"), tokens("1"))
		bob := identity.Identity{Forge: "github", ID: "1002", Login: "bob"}
		signedUp(t, s, hash(2), bob, account("a2", "bob"), tokens("2"))
		if err := s.PutTokens(ctx, alice().Key(), tokens("3")); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Tokens(ctx, alice().Key()); !sameTokens(got, tokens("3")) {
			t.Errorf("Tokens after PutTokens = %+v", got)
		}
		if err := s.PutTokens(ctx, "github:9999", tokens("x")); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("PutTokens of an identity without an account = %v", err)
		}
		if _, err := s.Tokens(ctx, "github:9999"); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("Tokens of an unknown identity = %v", err)
		}

		aliceSessions := []func() bool{fx.StartSession(t, accountID("a1")), fx.StartSession(t, accountID("a1"))}
		bobSession := fx.StartSession(t, accountID("a2"))
		if err := s.RevokeTokens(ctx, alice().Key()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Tokens(ctx, alice().Key()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("Tokens after RevokeTokens = %v", err)
		}
		for i, alive := range aliceSessions {
			if alive() {
				t.Errorf("session %d of the account outlived the revocation", i)
			}
		}
		if !bobSession() {
			t.Error("RevokeTokens ended the session of another account")
		}
		if got, err := s.AccountOf(ctx, alice().Key()); err != nil || got.ID != accountID("a1") {
			t.Errorf("the account did not outlive the revocation: %+v, %v", got, err)
		}
		if got, _ := s.Tokens(ctx, bob.Key()); !sameTokens(got, tokens("2")) {
			t.Errorf("RevokeTokens touched the tokens of another identity: %+v", got)
		}
		// A login stores tokens again.
		if err := s.PutTokens(ctx, alice().Key(), tokens("4")); err != nil {
			t.Errorf("PutTokens after a revocation = %v", err)
		}
		if err := s.RevokeTokens(ctx, "github:9999"); err != nil {
			t.Errorf("RevokeTokens of an unknown identity = %v", err)
		}
	})
}
