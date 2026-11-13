package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// TestAccountsBeforeWorkspaces: the accounts of a database at version 3 get
// their personal workspace from migration 4, and keep their identities,
// tokens and sessions.
func TestAccountsBeforeWorkspaces(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, all[:3], true); err != nil {
		t.Fatal(err)
	}
	a := s.Accounts(testKeyring(t, 1, 0))
	ids := map[string]string{}
	for i, name := range []string{"alice", "bob"} {
		id := strings.Repeat(string(rune('a'+i)), 32)
		ids[name] = id
		for _, stmt := range []string{
			`INSERT INTO accounts (id, name, created_at) VALUES ('` + id + `', '` + name + `', '` + formatTime(when()) + `')`,
			`INSERT INTO identities (forge, forge_id, account, login, name) VALUES ('github', '` + name + `-id', '` + id + `', '` + name + `', '')`,
		} {
			if _, err := s.writer.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.PutTokens(ctx, "github:"+name+"-id", identity.Tokens{Access: "a", Refresh: "r", AccessExpiry: when(), RefreshExpiry: when()}); err != nil {
			t.Fatal(err)
		}
	}
	alive := startSession(t, s, ids["alice"])
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	for name, id := range ids {
		got, err := a.WorkspacesOf(ctx, id)
		if err != nil || len(got) != 1 || got[0].Workspace.ID != id || got[0].Workspace.Name != name || !got[0].Workspace.Personal || got[0].Role != identity.RoleOwner {
			t.Errorf("WorkspacesOf %s after migration 4 = %+v, %v", name, got, err)
		}
		if _, err := a.Tokens(ctx, "github:"+name+"-id"); err != nil {
			t.Errorf("the tokens of %s did not survive the migration: %v", name, err)
		}
	}
	if !alive() {
		t.Error("a session did not survive the migration")
	}
	if err := a.CreateWorkspace(ctx, identity.Workspace{ID: strings.Repeat("c", 32), Name: "alice", CreatedAt: when()}, ids["bob"]); !errors.Is(err, identity.ErrNameTaken) {
		t.Errorf("CreateWorkspace with the name of an account of version 3 = %v", err)
	}
	// Migrating again changes nothing.
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM workspaces`).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d workspaces after a second migration, %v", n, err)
	}
}

// TestOneNameOneOwner: a sign-up and a workspace that want the same name at
// the same time end with one winner.
func TestOneNameOneOwner(t *testing.T) {
	ctx := context.Background()
	for range 10 {
		s, _ := openMigrated(t)
		a := s.Accounts(testKeyring(t, 1, 0))
		signedUpAlice(t, a)
		if err := a.PutSignup(ctx, identity.Signup{Session: "s2", Identity: identity.Identity{Forge: "github", ID: "1002"}, Tokens: identity.Tokens{Access: "a", Refresh: "r"}, ExpiresAt: when().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = a.CompleteSignup(ctx, "s2", identity.Account{ID: strings.Repeat("b", 32), Name: "acme", CreatedAt: when()}, when())
		}()
		go func() {
			defer wg.Done()
			errs[1] = a.CreateWorkspace(ctx, identity.Workspace{ID: strings.Repeat("c", 32), Name: "acme", CreatedAt: when()}, strings.Repeat("a", 32))
		}()
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, identity.ErrNameTaken):
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d winners for one name: %v", won, errs)
		}
	}
}
