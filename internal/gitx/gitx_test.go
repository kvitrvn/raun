package gitx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kvitrvn/raun/internal/gittest"
)

func TestOpen(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("a/b/c.txt", "x")

	r, err := Open(ctx, filepath.Join(fx.Dir, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(fx.Dir)
	got, _ := filepath.EvalSymlinks(r.Root())
	if got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}

	if _, err := Open(ctx, t.TempDir()); err == nil {
		t.Error("Open(non-repo) succeeded")
	}
}

func TestResolveCommit(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("f.txt", "1")
	c1 := fx.Commit("one")
	r, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, rev := range []string{"HEAD", "main", c1, c1[:8]} {
		got, err := r.ResolveCommit(ctx, rev)
		if err != nil || got != c1 {
			t.Errorf("ResolveCommit(%q) = %q, %v; want %q", rev, got, err, c1)
		}
	}
	for _, rev := range []string{"", "nope", "--all", "0000000000000000000000000000000000000000"} {
		if _, err := r.ResolveCommit(ctx, rev); !errors.Is(err, ErrUnknownCommit) {
			t.Errorf("ResolveCommit(%q) error = %v, want ErrUnknownCommit", rev, err)
		}
	}
}

func TestReadFile(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("src/main.go", "package main\n")
	fx.Write("docs/[draft].md", "draft\n")
	fx.Write("docs/guide.md", "guide\n")
	if err := os.Symlink("main.go", filepath.Join(fx.Dir, "src", "link.go")); err != nil {
		t.Fatal(err)
	}
	c1 := fx.Commit("one")
	fx.Write("src/main.go", "package main // v2\n")
	fx.Remove("docs/guide.md")
	c2 := fx.Commit("two")

	r, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		commit  string
		path    string
		want    string
		wantErr error
	}{
		{"file at first commit", c1, "src/main.go", "package main\n", nil},
		{"file at second commit", c2, "src/main.go", "package main // v2\n", nil},
		{"glob characters taken literally", c1, "docs/[draft].md", "draft\n", nil},
		{"deleted file", c2, "docs/guide.md", "", fs.ErrNotExist},
		{"never existed", c1, "nope.go", "", fs.ErrNotExist},
		{"prefix of a file name", c1, "src/main", "", fs.ErrNotExist},
		{"directory", c1, "src", "", ErrNotAFile},
		{"symlink", c1, "src/link.go", "", ErrNotAFile},
		{"unknown commit", "deadbeef", "src/main.go", "", ErrUnknownCommit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.ReadFile(ctx, tt.commit, tt.path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Errorf("ReadFile() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
