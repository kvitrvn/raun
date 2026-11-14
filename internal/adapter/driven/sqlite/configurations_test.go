package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// TestEventsBeforeConfigurations: a database at version 6 keeps its
// repositories and their event log through migration 7, which adds the
// configurations empty.
func TestEventsBeforeConfigurations(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, all[:6], true); err != nil {
		t.Fatal(err)
	}
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	owner := strings.Repeat("a", 32)
	addWorkspace(t, s, ws, owner)
	repos := s.Repositories()
	if err := repos.Activate(ctx, repository.Repository{Forge: "github", ID: "2001", Workspace: ws, FullName: "acme/api", DefaultBranch: "main", ChangedBy: owner, ChangedAt: when()}); err != nil {
		t.Fatal(err)
	}
	k := repository.Key{Forge: "github", Delivery: "d1", Digest: strings.Repeat("a", 64), ReceivedAt: when()}
	row := &repository.LoggedEvent{Forge: "github", Repository: "2001", Delivery: "d1", Event: "push", ReceivedAt: when(), Result: repository.ResultRecorded}
	if _, err := repos.Record(ctx, k, row); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, err := repos.Log(ctx, "github", "2001", 10); err != nil || len(got) != 1 {
		t.Errorf("the event log after migration 7 = %+v, %v", got, err)
	}
	if _, ok, err := repos.Configuration(ctx, "github", "2001"); err != nil || ok {
		t.Errorf("a configuration after migration 7: %v, %v", ok, err)
	}
}
