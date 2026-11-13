package sqlite

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/identity/identitytest"
	"github.com/kvitrvn/raun/internal/core/session"
)

// startSession gives account a session in s and returns whether it lives.
func startSession(t *testing.T, s *Store, account string) func() bool {
	t.Helper()
	_, sess, err := session.New(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sess.Account = account
	if err := s.Sessions().Create(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		_, err := s.Sessions().Get(context.Background(), sess.Hash)
		return err == nil
	}
}

func TestAccountsContract(t *testing.T) {
	identitytest.Suite(t, func(t *testing.T) identitytest.Fixture {
		s, _ := openMigrated(t)
		a := s.Accounts(testKeyring(t, 1, 0))
		return identitytest.Fixture{
			Store:        a,
			Workspaces:   a,
			StartSession: func(t *testing.T, account string) func() bool { return startSession(t, s, account) },
		}
	})
}

// when() is the time of these tests.
func when() time.Time { return time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC) }

// signedUpAlice gives the store an account for Alice whose tokens hold the
// sentinels.
func signedUpAlice(t *testing.T, a Accounts) identity.Tokens {
	t.Helper()
	ctx := context.Background()
	tokens := identity.Tokens{
		Access: "gha_access-sentinel", AccessExpiry: when().Add(8 * time.Hour),
		Refresh: "ghr_refresh-sentinel", RefreshExpiry: when().Add(4000 * time.Hour),
	}
	alice := identity.Identity{Forge: "github", ID: "1001", Login: "alice"}
	if err := a.PutSignup(ctx, identity.Signup{Session: "s1", Identity: alice, Tokens: tokens, Proposed: "alice", ReturnPath: "/", ExpiresAt: when().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteSignup(ctx, "s1", identity.Account{ID: strings.Repeat("a", 32), Name: "alice", CreatedAt: when()}, when()); err != nil {
		t.Fatal(err)
	}
	return tokens
}

// TestTokensAreNeverStoredInClear: no byte of the database file or its
// write-ahead log holds a token, whether it waits in a sign-up or belongs
// to an account.
func TestTokensAreNeverStoredInClear(t *testing.T) {
	ctx := context.Background()
	s, path := openMigrated(t)
	a := s.Accounts(testKeyring(t, 1, 0))
	signedUpAlice(t, a)
	pending := identity.Tokens{Access: "gha_pending-sentinel", Refresh: "ghr_pending-sentinel", AccessExpiry: when(), RefreshExpiry: when()}
	if err := a.PutSignup(ctx, identity.Signup{Session: "s2", Identity: identity.Identity{Forge: "github", ID: "1002"}, Tokens: pending, ExpiresAt: when().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Tokens(ctx, "github:1001"); err != nil || got.Access.Reveal() != "gha_access-sentinel" {
		t.Fatalf("Tokens = %v, %v", got.Access.Reveal(), err)
	}
	for _, f := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			t.Fatal(err)
		}
		for _, secret := range []string{"access-sentinel", "refresh-sentinel", "pending-sentinel", "redacted"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds %q", filepath.Base(f), secret)
			}
		}
	}
}

func TestRotateSecrets(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	old := s.Accounts(testKeyring(t, 1, 0))
	tokens := signedUpAlice(t, old)
	if err := old.PutSignup(ctx, identity.Signup{Session: "s2", Identity: identity.Identity{Forge: "github", ID: "1002"}, Tokens: tokens, ExpiresAt: when().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// Nothing to do under the same key.
	if n, err := old.RotateSecrets(ctx); err != nil || n != 0 {
		t.Errorf("RotateSecrets with one key = %d, %v", n, err)
	}
	// A new key, the old one as previous: both secrets are sealed again.
	rotating := s.Accounts(testKeyring(t, 2, 1))
	if n, err := rotating.RotateSecrets(ctx); err != nil || n != 2 {
		t.Fatalf("RotateSecrets = %d, %v; want 2", n, err)
	}
	// The previous key can go.
	fresh := s.Accounts(testKeyring(t, 2, 0))
	if got, err := fresh.Tokens(ctx, "github:1001"); err != nil || got.Refresh.Reveal() != "ghr_refresh-sentinel" || !got.RefreshExpiry.Equal(tokens.RefreshExpiry) {
		t.Errorf("Tokens under the new key = %v, %v", got.Refresh.Reveal(), err)
	}
	if got, err := fresh.Signup(ctx, "s2", when()); err != nil || got.Tokens.Access.Reveal() != "gha_access-sentinel" {
		t.Errorf("Signup under the new key = %v", err)
	}
	if _, err := old.Tokens(ctx, "github:1001"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Tokens under the old key alone = %v, want ErrUnknownKey", err)
	}
}

func TestRotateSecretsStopsOnAnUnknownKey(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	a := s.Accounts(testKeyring(t, 1, 0))
	signedUpAlice(t, a)
	var before []byte
	if err := s.reader.QueryRowContext(ctx, `SELECT sealed FROM tokens`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for name, keys := range map[string]*Keyring{"another key": testKeyring(t, 3, 4), "no key": nil} {
		_, err := s.Accounts(keys).RotateSecrets(ctx)
		want := ErrUnknownKey
		if keys == nil {
			want = ErrNoKey
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: RotateSecrets = %v, want %v", name, err, want)
		}
		if err != nil && !strings.Contains(err.Error(), "RAUN_SECRET_KEY") {
			t.Errorf("%s: the error %q does not name the variable", name, err)
		}
	}
	var after []byte
	if err := s.reader.QueryRowContext(ctx, `SELECT sealed FROM tokens`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a failed rotation changed a secret")
	}
	if n, err := s.Accounts(nil).RotateSecrets(ctx); err == nil || n != 0 {
		t.Errorf("RotateSecrets without a key = %d, %v", n, err)
	}
}

// TestOversizedTokensAreRefused: tokens too large to read back are never
// stored, and the store is unchanged.
func TestOversizedTokensAreRefused(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	a := s.Accounts(testKeyring(t, 1, 0))
	tokens := signedUpAlice(t, a)
	huge := identity.Tokens{Access: identity.Secret(strings.Repeat("x", maxSealed)), AccessExpiry: when(), RefreshExpiry: when()}
	if err := a.PutTokens(ctx, "github:1001", huge); !errors.Is(err, ErrTooLarge) {
		t.Errorf("PutTokens of oversized tokens = %v", err)
	}
	if err := a.PutSignup(ctx, identity.Signup{Session: "s9", Identity: identity.Identity{Forge: "github", ID: "1009"}, Tokens: huge, ExpiresAt: when().Add(time.Hour)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("PutSignup of oversized tokens = %v", err)
	}
	if got, err := a.Tokens(ctx, "github:1001"); err != nil || got.Access != tokens.Access {
		t.Errorf("the refused tokens changed the stored ones: %v", err)
	}
	if n, err := a.RotateSecrets(ctx); err != nil || n != 0 {
		t.Errorf("RotateSecrets after a refused write = %d, %v", n, err)
	}
}

func TestRotateSecretsOnAnEmptyDatabase(t *testing.T) {
	s, _ := openMigrated(t)
	if n, err := s.Accounts(nil).RotateSecrets(context.Background()); err != nil || n != 0 {
		t.Errorf("RotateSecrets without secrets nor a key = %d, %v", n, err)
	}
}

// TestSessionsBeforeAccounts: sessions created before migration 3 stay, with
// no account.
func TestSessionsBeforeAccounts(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, all[:2], true); err != nil {
		t.Fatal(err)
	}
	_, sess, _ := session.New(rand.Reader, time.Now())
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO sessions (hash, csrf, created_at, last_seen_at) VALUES (?, ?, ?, ?)`,
		sess.Hash, sess.CSRF, formatTime(sess.CreatedAt), formatTime(sess.LastSeenAt)); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sessions().Get(ctx, sess.Hash); err != nil || got.Account != "" {
		t.Errorf("a session of version 2 after the migration = %+v, %v", got, err)
	}
}
