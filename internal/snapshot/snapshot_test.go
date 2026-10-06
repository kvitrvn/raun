package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/gitx"
)

func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("a.txt", "a")
	fx.Write("b.txt", "b")
	fx.Write("c.txt", "c")
	fx.Write(".raun/knowledge/x.yaml", "x")
	c := fx.Commit("one")
	repo, err := gitx.Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Create(ctx, repo, c, "raun-test-", func(p string) bool { return p == ".raun/knowledge/x.yaml" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".raun", "knowledge", "x.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("skipped file present in snapshot")
	}

	if changes, err := s.Changes(); err != nil || len(changes) != 0 {
		t.Errorf("fresh snapshot changes = %v, %v", changes, err)
	}

	_ = os.WriteFile(filepath.Join(s.Dir, "a.txt"), []byte("A"), 0o644)
	_ = os.Remove(filepath.Join(s.Dir, "b.txt"))
	_ = os.WriteFile(filepath.Join(s.Dir, "d.txt"), []byte("d"), 0o644)
	_ = os.Chmod(s.Dir, 0o555) // removal must still succeed

	changes, err := s.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"modified: a.txt", "removed: b.txt", "added: d.txt"}; !slices.Equal(changes, want) {
		t.Errorf("Changes() = %q, want %q", changes, want)
	}

	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("snapshot directory still exists")
	}
}
