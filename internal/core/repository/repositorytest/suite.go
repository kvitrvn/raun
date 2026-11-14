package repositorytest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// Fixture is a store under test, empty, and a way to create a workspace in
// the data it reads, with owner, the identifier of an account, which the
// fixture creates when it does not exist.
type Fixture struct {
	Store        repository.Store
	AddWorkspace func(t *testing.T, w repository.Workspace, owner string)
}

// Identifiers of the suite: 32 hexadecimal digits, as accounts and
// workspaces have.
func id32(n string) string { return strings.Repeat("0", 32-len(n)) + n }

var (
	alice = id32("a1")
	bob   = id32("a2")
	acme  = repository.Workspace{ID: id32("w1"), Name: "acme"}
	beta  = repository.Workspace{ID: id32("w2"), Name: "beta"}
	t0    = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
)

func repo(id, fullName string, ws repository.Workspace, by string) repository.Repository {
	return repository.Repository{
		Forge: "github", ID: id, Workspace: ws, FullName: fullName, DefaultBranch: "main",
		Active: true, Reachable: true, ChangedBy: by, ChangedAt: t0,
	}
}

func listed(id, fullName string) repository.Listed {
	return repository.Listed{Forge: "github", ID: id, FullName: fullName, DefaultBranch: "main"}
}

// Suite runs the contract of repository.Store on the stores newFixture
// gives, each with the workspaces acme (owned by Alice) and beta (owned by
// Bob).
func Suite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	setup := func(t *testing.T) repository.Store {
		t.Helper()
		f := newFixture(t)
		f.AddWorkspace(t, acme, alice)
		f.AddWorkspace(t, beta, bob)
		return f.Store
	}
	ctx := context.Background()

	t.Run("activate then read", func(t *testing.T) {
		s := setup(t)
		want := repo("2001", "acme/api", acme, alice)
		want.Private = true
		mustActivate(t, s, want)
		got := known(t, s, "2001")
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Known = %+v, want %+v", got, want)
		}
		if r, err := s.Find(ctx, "acme", "github", "acme/api"); err != nil || r != want {
			t.Errorf("Find = %+v, %v", r, err)
		}
		for _, tc := range []struct{ ws, forge, name string }{
			{"beta", "github", "acme/api"}, {"acme", "gitea", "acme/api"}, {"acme", "github", "acme/API"}, {"acme", "github", ""},
		} {
			if _, err := s.Find(ctx, tc.ws, tc.forge, tc.name); !errors.Is(err, repository.ErrNotFound) {
				t.Errorf("Find(%q, %q, %q) = %v, want ErrNotFound", tc.ws, tc.forge, tc.name, err)
			}
		}
		if rows, err := s.InWorkspace(ctx, acme.ID); err != nil || len(rows) != 1 || rows[0] != want {
			t.Errorf("InWorkspace(acme) = %+v, %v", rows, err)
		}
		if rows, err := s.InWorkspace(ctx, beta.ID); err != nil || len(rows) != 0 {
			t.Errorf("InWorkspace(beta) = %+v, %v", rows, err)
		}
	})

	t.Run("known", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2002", "acme/web", acme, alice))
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		got := known(t, s, "2002", "9999", "2001", "2002")
		if len(got) != 2 || got[0].ID != "2001" || got[1].ID != "2002" {
			t.Errorf("Known = %+v, want 2001 and 2002, sorted", got)
		}
		if rows, err := s.Known(ctx, "gitea", []string{"2001"}); err != nil || len(rows) != 0 {
			t.Errorf("Known of another forge = %+v, %v", rows, err)
		}
		if rows, err := s.Known(ctx, "github", nil); err != nil || len(rows) != 0 {
			t.Errorf("Known of nothing = %+v, %v", rows, err)
		}
	})

	t.Run("one workspace for good", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		if err := s.Activate(ctx, repo("2001", "acme/api", beta, bob)); !errors.Is(err, repository.ErrElsewhere) {
			t.Fatalf("activation in another workspace: %v, want ErrElsewhere", err)
		}
		if err := s.Deactivate(ctx, acme.ID, "github", "2001", bob, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.Activate(ctx, repo("2001", "acme/api", beta, bob)); !errors.Is(err, repository.ErrElsewhere) {
			t.Fatalf("activation of a deactivated repository in another workspace: %v, want ErrElsewhere", err)
		}
		got := known(t, s, "2001")
		if len(got) != 1 || got[0].Workspace != acme || got[0].Active || got[0].ChangedBy != bob || !got[0].ChangedAt.Equal(t0.Add(time.Hour)) {
			t.Errorf("after the refusals: %+v", got)
		}
		// The same identifier on another forge is another repository.
		other := repo("2001", "acme/api", beta, bob)
		other.Forge = "gitea"
		mustActivate(t, s, other)
	})

	t.Run("deactivate and reactivate", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		for _, tc := range []struct{ ws, forge, id string }{
			{beta.ID, "github", "2001"}, {acme.ID, "gitea", "2001"}, {acme.ID, "github", "2002"},
		} {
			if err := s.Deactivate(ctx, tc.ws, tc.forge, tc.id, alice, t0); !errors.Is(err, repository.ErrNotFound) {
				t.Errorf("Deactivate(%s, %s, %s) = %v, want ErrNotFound", tc.ws, tc.forge, tc.id, err)
			}
		}
		at := t0.Add(time.Hour).In(time.FixedZone("CET", 3600))
		if err := s.Deactivate(ctx, acme.ID, "github", "2001", alice, at); err != nil {
			t.Fatal(err)
		}
		got := known(t, s, "2001")[0]
		if got.Active || !got.Reachable || got.State() != repository.StateDeactivated || got.ChangedAt.Location() != time.UTC || !got.ChangedAt.Equal(at) {
			t.Errorf("deactivated: %+v", got)
		}
		// The page stays where it was.
		if _, err := s.Find(ctx, "acme", "github", "acme/api"); err != nil {
			t.Errorf("Find of a deactivated repository: %v", err)
		}
		again := repo("2001", "acme/api", acme, bob)
		again.ChangedAt = t0.Add(2 * time.Hour)
		mustActivate(t, s, again)
		if got := known(t, s, "2001")[0]; got != again {
			t.Errorf("reactivated: %+v, want %+v", got, again)
		}
	})

	t.Run("refresh keeps the identity", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		renamed := listed("2001", "acme/backend")
		renamed.DefaultBranch, renamed.Private = "trunk", true
		if err := s.Refresh(ctx, []repository.Listed{renamed, listed("9999", "acme/unknown")}); err != nil {
			t.Fatal(err)
		}
		got := known(t, s, "2001", "9999")
		if len(got) != 1 || got[0].FullName != "acme/backend" || got[0].DefaultBranch != "trunk" || !got[0].Private || !got[0].Active || got[0].ChangedBy != alice {
			t.Fatalf("after a rename: %+v", got)
		}
		if _, err := s.Find(ctx, "acme", "github", "acme/api"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("Find at the old name = %v, want ErrNotFound", err)
		}
		if r, err := s.Find(ctx, "acme", "github", "acme/backend"); err != nil || r.ID != "2001" {
			t.Errorf("Find at the new name = %+v, %v", r, err)
		}
	})

	t.Run("a name the forge gave to another repository", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		mustActivate(t, s, repo("2002", "acme/web", beta, bob))
		// 2001 was renamed on the forge, and 2002 took its name, in
		// another workspace: the newer snapshot wins.
		if err := s.Refresh(ctx, []repository.Listed{listed("2002", "acme/api")}); err != nil {
			t.Fatal(err)
		}
		got := known(t, s, "2001", "2002")
		if got[0].FullName != "" || got[1].FullName != "acme/api" {
			t.Fatalf("after the name moved: %+v", got)
		}
		if _, err := s.Find(ctx, "acme", "github", "acme/api"); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("Find in the workspace of the old holder = %v", err)
		}
		if r, err := s.Find(ctx, "beta", "github", "acme/api"); err != nil || r.ID != "2002" {
			t.Errorf("Find of the new holder = %+v, %v", r, err)
		}
		// Two repositories swapping their names in one listing.
		if err := s.Refresh(ctx, []repository.Listed{listed("2001", "acme/api"), listed("2002", "acme/web")}); err != nil {
			t.Fatal(err)
		}
		if err := s.Refresh(ctx, []repository.Listed{listed("2001", "acme/web"), listed("2002", "acme/api")}); err != nil {
			t.Fatal(err)
		}
		got = known(t, s, "2001", "2002")
		if got[0].FullName != "acme/web" || got[1].FullName != "acme/api" {
			t.Errorf("after a swap: %+v", got)
		}
		// An activation takes the name too.
		mustActivate(t, s, repo("2003", "acme/web", beta, bob))
		got = known(t, s, "2001", "2003")
		if got[0].FullName != "" || got[1].FullName != "acme/web" {
			t.Errorf("after an activation under a held name: %+v", got)
		}
	})

	t.Run("reach", func(t *testing.T) {
		s := setup(t)
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		mustActivate(t, s, repo("2002", "acme/web", acme, alice))
		mustActivate(t, s, repo("2003", "acme/lib", beta, bob))
		other := repo("2001", "other/api", beta, bob)
		other.Forge = "gitea"
		mustActivate(t, s, other)
		lost, err := s.SetReach(ctx, "github", []string{"2002", "9999"})
		if err != nil || strings.Join(lost, " ") != "2001 2003" {
			t.Fatalf("SetReach = %v, %v; want 2001 2003", lost, err)
		}
		got := known(t, s, "2001", "2002", "2003")
		if got[0].State() != repository.StateUnreachable || got[0].Active || got[1].State() != repository.StateActive || got[2].State() != repository.StateUnreachable {
			t.Errorf("after the loss: %+v", got)
		}
		if g, _ := s.Known(ctx, "gitea", []string{"2001"}); len(g) != 1 || !g[0].Active {
			t.Errorf("a repository of another forge changed: %+v", g)
		}
		// Lost again: not lost twice. Reached again: reachable, not active.
		if lost, err := s.SetReach(ctx, "github", []string{"2002"}); err != nil || len(lost) != 0 {
			t.Errorf("SetReach again = %v, %v; want none lost", lost, err)
		}
		if lost, err := s.SetReach(ctx, "github", []string{"2001", "2002", "2003"}); err != nil || len(lost) != 0 {
			t.Errorf("SetReach of all = %v, %v", lost, err)
		}
		got = known(t, s, "2001", "2003")
		if got[0].State() != repository.StateDeactivated || got[1].State() != repository.StateDeactivated {
			t.Errorf("reached again: %+v, want deactivated", got)
		}
		mustActivate(t, s, repo("2001", "acme/api", acme, alice))
		if got := known(t, s, "2001"); got[0].State() != repository.StateActive {
			t.Errorf("activated again: %+v", got)
		}
	})

	t.Run("concurrent activations", func(t *testing.T) {
		s := setup(t)
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ws, by := acme, alice
				if i%2 == 1 {
					ws, by = beta, bob
				}
				errs[i] = s.Activate(ctx, repo("2001", "acme/api", ws, by))
			}()
		}
		wg.Wait()
		got := known(t, s, "2001")
		if len(got) != 1 {
			t.Fatalf("Known = %+v", got)
		}
		for i, err := range errs {
			inWinner := (i%2 == 1) == (got[0].Workspace == beta)
			if inWinner && err != nil || !inWinner && !errors.Is(err, repository.ErrElsewhere) {
				t.Errorf("activation %d in the %s workspace: %v", i, map[bool]string{true: "winning", false: "losing"}[inWinner], err)
			}
		}
	})
}

func mustActivate(t *testing.T, s repository.Store, r repository.Repository) {
	t.Helper()
	if err := s.Activate(context.Background(), r); err != nil {
		t.Fatalf("Activate(%s in %s): %v", r.ID, r.Workspace.Name, err)
	}
}

func known(t *testing.T, s repository.Store, ids ...string) []repository.Repository {
	t.Helper()
	got, err := s.Known(context.Background(), "github", ids)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
