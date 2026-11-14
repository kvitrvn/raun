package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// TestDuplicatesAfterARestart: the keys of a delivery outlive the process,
// so a delivery sent again after a restart has no effect.
func TestDuplicatesAfterARestart(t *testing.T) {
	ctx := context.Background()
	s, path := openMigrated(t)
	k := repository.Key{Forge: "github", Delivery: "d1", Digest: strings.Repeat("a", 64), ReceivedAt: when()}
	if _, err := s.Repositories().Record(ctx, k, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	for _, dup := range []repository.Key{{Forge: "github", Delivery: "d1", Digest: strings.Repeat("b", 64), ReceivedAt: when()}, {Forge: "github", Delivery: "d2", Digest: k.Digest, ReceivedAt: when()}} {
		if _, err := again.Repositories().Record(ctx, dup, nil); !errors.Is(err, repository.ErrDuplicate) {
			t.Errorf("after a restart, Record(%s, %s) = %v, want ErrDuplicate", dup.Delivery, dup.Digest[:4], err)
		}
	}
}

// TestRepositoriesBeforeEvents: a database at version 5 keeps its
// repositories through migration 6, which adds the deliveries and the event
// log empty.
func TestRepositoriesBeforeEvents(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, all[:5], true); err != nil {
		t.Fatal(err)
	}
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	owner := strings.Repeat("a", 32)
	addWorkspace(t, s, ws, owner)
	r := repository.Repository{Forge: "github", ID: "2001", Workspace: ws, FullName: "acme/api", DefaultBranch: "main", Active: true, Reachable: true, ChangedBy: owner, ChangedAt: when()}
	if err := s.Repositories().Activate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Repositories().Find(ctx, "acme", "github", "acme/api"); err != nil || got != r {
		t.Errorf("the repository after migration 6 = %+v, %v", got, err)
	}
	if got, err := s.Repositories().Log(ctx, "github", "2001", 10); err != nil || len(got) != 0 {
		t.Errorf("the event log after migration 6 = %+v, %v", got, err)
	}
}
