package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/knowledge"
	"github.com/kvitrvn/raun/internal/core/knowledge/knowledgetest"
	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/core/repository/repositorytest"
)

// addWorkspace creates the workspace w owned by the account owner, and the
// account when it does not exist, as sign-up and the workspace screen would.
func addWorkspace(t *testing.T, s *Store, w repository.Workspace, owner string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.writer.ExecContext(ctx, `INSERT OR IGNORE INTO accounts (id, name, created_at) VALUES (?, ?, ?)`,
		owner, "u"+owner[len(owner)-6:], formatTime(when())); err != nil {
		t.Fatal(err)
	}
	if err := s.Accounts(nil).CreateWorkspace(ctx, identity.Workspace{ID: w.ID, Name: w.Name, CreatedAt: when()}, owner); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoriesContract(t *testing.T) {
	repositorytest.Suite(t, func(t *testing.T) repositorytest.Fixture {
		s, _ := openMigrated(t)
		return repositorytest.Fixture{
			Store:        s.Repositories(),
			Events:       s.Repositories(),
			AddWorkspace: func(t *testing.T, w repository.Workspace, owner string) { addWorkspace(t, s, w, owner) },
		}
	})
}

func TestConfigurationsContract(t *testing.T) {
	repositorytest.ConfigurationSuite(t, func(t *testing.T) repositorytest.Fixture {
		s, _ := openMigrated(t)
		return repositorytest.Fixture{
			Store:          s.Repositories(),
			Events:         s.Repositories(),
			Configurations: s.Repositories(),
			AddWorkspace:   func(t *testing.T, w repository.Workspace, owner string) { addWorkspace(t, s, w, owner) },
		}
	})
}

func TestEventsContract(t *testing.T) {
	repositorytest.EventSuite(t, func(t *testing.T) repositorytest.Fixture {
		s, _ := openMigrated(t)
		return repositorytest.Fixture{
			Store:        s.Repositories(),
			Events:       s.Repositories(),
			AddWorkspace: func(t *testing.T, w repository.Workspace, owner string) { addWorkspace(t, s, w, owner) },
		}
	})
}

// TestRepositoriesSurviveAReopen: what is stored is read again by another
// process.
func TestRepositoriesSurviveAReopen(t *testing.T) {
	ctx := context.Background()
	s, path := openMigrated(t)
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	addWorkspace(t, s, ws, strings.Repeat("a", 32))
	r := repository.Repository{
		Forge: "github", ID: "2001", Workspace: ws, FullName: "acme/api", DefaultBranch: "main", Private: true,
		Active: true, Reachable: true, ChangedBy: strings.Repeat("a", 32), ChangedAt: when(),
	}
	if err := s.Repositories().Activate(ctx, r); err != nil {
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
	if got, err := again.Repositories().Find(ctx, "acme", "github", "acme/api"); err != nil || got != r {
		t.Errorf("after a reopen: %+v, %v; want %+v", got, err, r)
	}
}

// TestRepositoriesRefuseWhatTheRulesForbid: the table itself refuses an
// active repository the App does not reach, and an unknown workspace.
func TestRepositoriesRefuseWhatTheRulesForbid(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	owner := strings.Repeat("a", 32)
	addWorkspace(t, s, ws, owner)
	for name, stmt := range map[string]string{
		"active and unreachable": `INSERT INTO repositories VALUES ('github', '1', '` + ws.ID + `', 'a/b', 'main', 0, 1, 0, '` + owner + `', '` + formatTime(when()) + `')`,
		"unknown workspace":      `INSERT INTO repositories VALUES ('github', '1', '` + strings.Repeat("9", 32) + `', 'a/b', 'main', 0, 1, 1, '` + owner + `', '` + formatTime(when()) + `')`,
		"empty full name":        `INSERT INTO repositories VALUES ('github', '1', '` + ws.ID + `', '', 'main', 0, 1, 1, '` + owner + `', '` + formatTime(when()) + `')`,
	} {
		if _, err := s.writer.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
}

// TestRenamedRepositoryKeepsItsKnowledge: the knowledge of a repository is
// scoped by its stable identifier, so a rename on the forge leaves it where
// it was.
func TestRenamedRepositoryKeepsItsKnowledge(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	owner := strings.Repeat("a", 32)
	addWorkspace(t, s, ws, owner)
	repos := s.Repositories()
	if err := repos.Activate(ctx, repository.Repository{Forge: "github", ID: "2001", Workspace: ws, FullName: "acme/api", DefaultBranch: "main", ChangedBy: owner, ChangedAt: when()}); err != nil {
		t.Fatal(err)
	}
	scopeOf := func(r repository.Repository) knowledge.Scope {
		return knowledge.Scope{Workspace: r.Workspace.ID, Repository: r.Forge + ":" + r.ID}
	}
	before, err := repos.Find(ctx, "acme", "github", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	item := knowledgetest.Persona(t, "persona-k3x9q2")
	if err := s.Create(ctx, scopeOf(before), item); err != nil {
		t.Fatal(err)
	}
	if err := repos.Refresh(ctx, []repository.Listed{{Forge: "github", ID: "2001", FullName: "acme/backend", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Find(ctx, "acme", "github", "acme/api"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the old name still answers: %v", err)
	}
	after, err := repos.Find(ctx, "acme", "github", "acme/backend")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, scopeOf(after), item.ID); err != nil || got.ID != item.ID {
		t.Errorf("the knowledge after a rename: %+v, %v", got, err)
	}
}

// TestWorkspacesBeforeRepositories: a database at version 4 keeps its
// workspaces and its knowledge through migration 5, which adds the table of
// repositories empty.
func TestWorkspacesBeforeRepositories(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, all[:4], true); err != nil {
		t.Fatal(err)
	}
	ws := repository.Workspace{ID: strings.Repeat("1", 32), Name: "acme"}
	addWorkspace(t, s, ws, strings.Repeat("a", 32))
	item := knowledgetest.Persona(t, "persona-k3x9q2")
	if err := s.Create(ctx, testScope(), item); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, testScope(), item.ID); err != nil || got.ID != item.ID {
		t.Errorf("the knowledge after migration 5: %+v, %v", got, err)
	}
	if m, err := s.Accounts(nil).WorkspaceOf(ctx, "acme", strings.Repeat("a", 32)); err != nil || m.Workspace.ID != ws.ID {
		t.Errorf("the workspace after migration 5: %+v, %v", m, err)
	}
	if rows, err := s.Repositories().InWorkspace(ctx, ws.ID); err != nil || len(rows) != 0 {
		t.Errorf("repositories after migration 5: %+v, %v", rows, err)
	}
}
