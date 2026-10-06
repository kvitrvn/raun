package gitx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestExport(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("src/main.go", "package main\n")
	fx.Write("run.sh", "#!/bin/sh\n")
	fx.Write(".gitattributes", "src/main.go export-ignore\n")
	fx.Write(".raun/knowledge/persona/persona-aaaaaa.yaml", "secret\n")
	fx.Write(".raun/context.md", "context\n")
	if err := os.Chmod(filepath.Join(fx.Dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/main.go", filepath.Join(fx.Dir, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(fx.Dir, "outside")); err != nil {
		t.Fatal(err)
	}
	c := fx.Commit("one")
	r, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	skip := func(p string) bool { return p == ".raun/knowledge" || strings.HasPrefix(p, ".raun/knowledge/") }
	if err := r.Export(ctx, c, dest, skip); err != nil {
		t.Fatal(err)
	}

	read := func(p string) string {
		data, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(data)
	}
	if got := read("src/main.go"); got != "package main\n" {
		t.Errorf("export-ignore must not apply: src/main.go = %q", got)
	}
	if got := read(".raun/context.md"); got != "context\n" {
		t.Errorf(".raun/context.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, ".raun", "knowledge")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("skipped path was exported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".git was exported")
	}
	if info, err := os.Stat(filepath.Join(dest, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh lost its executable bit: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "inside")); err != nil || target != "src/main.go" {
		t.Errorf("inside symlink = %q, %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "outside")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("symlink escaping the export was created")
	}
}

func TestChangedPaths(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	fx.Write("a.txt", "1")
	fx.Write("b.txt", "1")
	fx.Write(".gitignore", "ignored/\n")
	fx.Commit("one")
	r, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	if paths, err := r.ChangedPaths(ctx); err != nil || len(paths) != 0 {
		t.Fatalf("clean tree: %v, %v", paths, err)
	}

	fx.Write("a.txt", "2")
	fx.Write("new dir/c.txt", "new")
	fx.Write("ignored/x", "x")
	fx.Git("mv", "b.txt", "renamed.txt")
	paths, err := r.ChangedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	if want := []string{"a.txt", "new dir/c.txt", "renamed.txt"}; !slices.Equal(paths, want) {
		t.Errorf("ChangedPaths() = %q, want %q", paths, want)
	}
}

func TestConfigValue(t *testing.T) {
	ctx := context.Background()
	fx := gittest.New(t)
	r, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	fx.Git("config", "raun.test", "Ada Lovelace")
	if v, err := r.ConfigValue(ctx, "raun.test"); err != nil || v != "Ada Lovelace" {
		t.Errorf("ConfigValue() = %q, %v", v, err)
	}
	if v, err := r.ConfigValue(ctx, "raun.unset"); err != nil || v != "" {
		t.Errorf("unset key = %q, %v", v, err)
	}
}
