package git_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/git"
	"github.com/kvitrvn/raun/internal/core/knowledge"
	"github.com/kvitrvn/raun/internal/gittest"
)

// counter is a git binary that logs every start before running the real one.
type counter struct {
	path string
	log  string
}

func newCounter(t *testing.T) *counter {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the counting wrapper is a shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found: %v", err)
	}
	dir := t.TempDir()
	c := &counter{path: filepath.Join(dir, "git"), log: filepath.Join(dir, "starts.log")}
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> '%s'\nexec '%s' \"$@\"\n", c.log, real)
	if err := os.WriteFile(c.path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// starts returns the commands started so far and forgets them.
func (c *counter) starts(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(c.log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(c.log); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func open(t *testing.T, dir string, opts git.Options) *git.Reader {
	t.Helper()
	r, err := git.Open(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

// fixture holds every kind of path at its first commit.
type fixture struct {
	repo   *gittest.Repo
	first  string
	second string
}

const (
	logo    = "\x89PNG\r\n\x00\x1a"
	invoice = "package billing\r\n\r\nconst Due = 30\r\n"
)

func newFixture(t *testing.T) fixture {
	t.Helper()
	r := gittest.New(t)
	r.WriteFile("README.md", []byte("# Billing\n\nInvoices are sent monthly.\n"))
	r.WriteFile("internal/billing/invoice.go", []byte(invoice))
	r.WriteFile("docs/empty.txt", nil)
	r.WriteFile("assets/logo.png", []byte(logo))
	r.WriteFile("-dash.txt", []byte("a file whose name starts with a dash\n"))
	r.WriteFile("dir with spaces/a\tb.txt", []byte("tab\n"))
	r.WriteExecutable("scripts/run.sh", []byte("#!/bin/sh\n"))
	r.Symlink("current", "internal/billing")
	r.Symlink("escape", "../../etc/passwd")
	r.Submodule("vendor/lib", strings.Repeat("1", 40))
	first := r.Commit("first")
	r.WriteFile("README.md", []byte("# Billing\n"))
	r.Remove("assets")
	second := r.Commit("second")
	r.Git("tag", "light", first)
	r.Git("tag", "--annotate", "--message", "release", "v1", first)
	r.Git("branch", "old", first)
	return fixture{repo: r, first: first, second: second}
}

func TestOpen(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(f.repo.Dir(), "README.md")
	notRepo := t.TempDir()

	tests := []struct {
		name string
		dir  string
		opts git.Options
		want string
	}{
		{"empty directory", "", git.Options{}, "empty directory"},
		{"missing directory", filepath.Join(notRepo, "nope"), git.Options{}, "no such file"},
		{"a file", file, git.Options{}, "not a directory"},
		{"not a repository", notRepo, git.Options{}, "not the root of a Git repository"},
		{"below the root", filepath.Join(f.repo.Dir(), "internal"), git.Options{}, "not the root of a Git repository"},
		{"relative git binary", f.repo.Dir(), git.Options{Git: "git"}, "not an absolute path"},
		{"missing git binary", f.repo.Dir(), git.Options{Git: filepath.Join(notRepo, "git")}, "git version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := git.Open(context.Background(), tt.dir, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open(%q) = %v, want an error about %q", tt.dir, err, tt.want)
			}
		})
	}
}

func TestOpenThroughALink(t *testing.T) {
	f := newFixture(t)
	links := t.TempDir()
	below := filepath.Join(links, "below")
	root := filepath.Join(links, "root")
	if err := os.Symlink(filepath.Join(f.repo.Dir(), "internal"), below); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.repo.Dir(), root); err != nil {
		t.Fatal(err)
	}

	// A link to a directory below the root must not let Git reach the root.
	if _, err := git.Open(context.Background(), below, git.Options{}); err == nil || !strings.Contains(err.Error(), "not the root of a Git repository") {
		t.Errorf("Open(link below the root) = %v, want a refusal", err)
	}
	r := open(t, root, git.Options{})
	if got, err := r.ResolveRevision(context.Background(), "main"); err != nil || got != f.second {
		t.Errorf("ResolveRevision(main) through a link to the root = %q, %v", got, err)
	}
}

func TestOpenRefusesAnOldGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	fake := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'git version 2.31.8'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := git.Open(context.Background(), t.TempDir(), git.Options{Git: fake})
	if err == nil || !strings.Contains(err.Error(), "older than 2.32") {
		t.Errorf("Open = %v, want a refusal of git 2.31", err)
	}
}

func TestOpenBareRepository(t *testing.T) {
	f := newFixture(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	f.repo.Git("clone", "--quiet", "--bare", "--no-local", f.repo.Dir(), bare)

	r := open(t, bare, git.Options{})
	got, err := r.ResolveRevision(context.Background(), "main")
	if err != nil || got != f.second {
		t.Errorf("ResolveRevision(main) = %q, %v, want %s", got, err, f.second)
	}
}

func TestResolveRevision(t *testing.T) {
	f := newFixture(t)
	c := newCounter(t)
	r := open(t, f.repo.Dir(), git.Options{Git: c.path})
	ctx := context.Background()

	resolved := []struct {
		revision string
		want     string
	}{
		{"main", f.second},
		{"HEAD", f.second},
		{"old", f.first},
		{"light", f.first},
		{"v1", f.first},
		{"refs/tags/v1", f.first},
		{"main~1", f.first},
		{f.first, f.first},
		{f.first[:12], f.first},
	}
	for _, tt := range resolved {
		got, err := r.ResolveRevision(ctx, tt.revision)
		if err != nil || got != tt.want {
			t.Errorf("ResolveRevision(%q) = %q, %v, want %s", tt.revision, got, err, tt.want)
		}
	}

	unknown := []string{
		"nope",
		strings.Repeat("0", 40),
		"main:README.md",
		"main^{tree}",
		"main..old",
	}
	for _, revision := range unknown {
		if got, err := r.ResolveRevision(ctx, revision); !errors.Is(err, knowledge.ErrUnknownRevision) {
			t.Errorf("ResolveRevision(%q) = %q, %v, want ErrUnknownRevision", revision, got, err)
		}
	}
	c.starts(t)

	refused := []string{"", "-n", "--all", "--output=/tmp/x", "main\nold", "main\x00"}
	for _, revision := range refused {
		if got, err := r.ResolveRevision(ctx, revision); !errors.Is(err, knowledge.ErrUnknownRevision) {
			t.Errorf("ResolveRevision(%q) = %q, %v, want ErrUnknownRevision", revision, got, err)
		}
	}
	if starts := c.starts(t); len(starts) != 0 {
		t.Errorf("refused revisions started git: %q", starts)
	}
}

func TestListTree(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})

	got, err := r.ListTree(context.Background(), f.first)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	want := []knowledge.TreeEntry{
		{Path: "-dash.txt", Kind: knowledge.PathFile, Size: 37},
		{Path: "README.md", Kind: knowledge.PathFile, Size: 38},
		{Path: "assets", Kind: knowledge.PathDirectory},
		{Path: "assets/logo.png", Kind: knowledge.PathFile, Size: int64(len(logo))},
		{Path: "current", Kind: knowledge.PathLink, Size: int64(len("internal/billing"))},
		{Path: "dir with spaces", Kind: knowledge.PathDirectory},
		{Path: "dir with spaces/a\tb.txt", Kind: knowledge.PathFile, Size: 4},
		{Path: "docs", Kind: knowledge.PathDirectory},
		{Path: "docs/empty.txt", Kind: knowledge.PathFile},
		{Path: "escape", Kind: knowledge.PathLink, Size: int64(len("../../etc/passwd"))},
		{Path: "internal", Kind: knowledge.PathDirectory},
		{Path: "internal/billing", Kind: knowledge.PathDirectory},
		{Path: "internal/billing/invoice.go", Kind: knowledge.PathFile, Size: int64(len(invoice))},
		{Path: "scripts", Kind: knowledge.PathDirectory},
		{Path: "scripts/run.sh", Kind: knowledge.PathFile, Size: 10},
		{Path: "vendor", Kind: knowledge.PathDirectory},
		{Path: "vendor/lib", Kind: knowledge.PathSubmodule},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListTree:\n%v\nwant:\n%v", got, want)
	}
}

func TestListTreeRefusesWhatIsNotACommit(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})
	tree := f.repo.Git("rev-parse", f.first+"^{tree}")
	tag := f.repo.Git("rev-parse", "v1")

	for _, commit := range []string{"main", f.first[:12], strings.ToUpper(f.first), strings.Repeat("0", 40), tree, tag, "-" + f.first[1:]} {
		if _, err := r.ListTree(context.Background(), commit); !errors.Is(err, knowledge.ErrUnknownRevision) {
			t.Errorf("ListTree(%q) = %v, want ErrUnknownRevision", commit, err)
		}
	}
}

func TestListTreeBound(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{MaxTreeEntries: 5})
	if _, err := r.ListTree(context.Background(), f.first); !errors.Is(err, knowledge.ErrTreeTooLarge) {
		t.Errorf("ListTree = %v, want ErrTreeTooLarge", err)
	}
	err := r.ReadFiles(context.Background(), f.first, []string{"README.md"}, func(knowledge.File) error { return nil })
	if !errors.Is(err, knowledge.ErrTreeTooLarge) {
		t.Errorf("ReadFiles = %v, want ErrTreeTooLarge", err)
	}
}

// readAll reads paths and returns the files in the order they were visited.
func readAll(t *testing.T, r *git.Reader, commit string, paths ...string) []knowledge.File {
	t.Helper()
	var files []knowledge.File
	err := r.ReadFiles(context.Background(), commit, paths, func(f knowledge.File) error {
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	return files
}

func TestReadFiles(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})

	got := readAll(t, r, f.first,
		"internal/billing/invoice.go",
		"README.md",
		"assets/logo.png",
		"docs/empty.txt",
		"scripts/run.sh",
		"dir with spaces/a\tb.txt",
		"current",
		"escape",
		"vendor/lib",
		"internal",
		"missing.go",
		"README.md/below-a-file",
		"current/invoice.go",
		"vendor/lib/inside.go",
		"README.md",
	)
	text := func(s string) []byte { return []byte(s) }
	want := []knowledge.File{
		{Path: "README.md", Kind: knowledge.PathFile, Size: 38, Content: text("# Billing\n\nInvoices are sent monthly.\n")},
		{Path: "README.md/below-a-file", Kind: knowledge.PathMissing},
		{Path: "assets/logo.png", Kind: knowledge.PathFile, Size: int64(len(logo)), Content: []byte(logo), Binary: true},
		{Path: "current", Kind: knowledge.PathLink},
		{Path: "current/invoice.go", Kind: knowledge.PathMissing},
		{Path: "dir with spaces/a\tb.txt", Kind: knowledge.PathFile, Size: 4, Content: text("tab\n")},
		{Path: "docs/empty.txt", Kind: knowledge.PathFile, Content: []byte{}},
		{Path: "escape", Kind: knowledge.PathLink},
		{Path: "internal", Kind: knowledge.PathDirectory},
		{Path: "internal/billing/invoice.go", Kind: knowledge.PathFile, Size: int64(len(invoice)), Content: []byte(invoice)},
		{Path: "missing.go", Kind: knowledge.PathMissing},
		{Path: "scripts/run.sh", Kind: knowledge.PathFile, Size: 10, Content: text("#!/bin/sh\n")},
		{Path: "vendor/lib", Kind: knowledge.PathSubmodule},
		{Path: "vendor/lib/inside.go", Kind: knowledge.PathMissing},
	}
	if len(got) != len(want) {
		t.Fatalf("visited %d files, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !sameFile(got[i], want[i]) {
			t.Errorf("file %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func sameFile(a, b knowledge.File) bool {
	return a.Path == b.Path && a.Kind == b.Kind && a.Size == b.Size && a.TooLarge == b.TooLarge &&
		a.Binary == b.Binary && bytes.Equal(a.Content, b.Content) && (a.Content == nil) == (b.Content == nil)
}

func TestReadFilesAtEachCommit(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})

	first := readAll(t, r, f.first, "README.md", "assets/logo.png")
	second := readAll(t, r, f.second, "README.md", "assets/logo.png")
	if string(first[0].Content) == string(second[0].Content) {
		t.Errorf("README.md is the same at both commits: %q", first[0].Content)
	}
	if string(second[0].Content) != "# Billing\n" {
		t.Errorf("README.md at the second commit = %q", second[0].Content)
	}
	if first[1].Kind != knowledge.PathFile || second[1].Kind != knowledge.PathMissing {
		t.Errorf("assets/logo.png: %s then %s, want file then missing", first[1].Kind, second[1].Kind)
	}
}

func TestReadFilesTooLarge(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{MaxFileSize: 10})

	got := readAll(t, r, f.first, "README.md", "scripts/run.sh")
	want := []knowledge.File{
		{Path: "README.md", Kind: knowledge.PathFile, Size: 38, TooLarge: true},
		{Path: "scripts/run.sh", Kind: knowledge.PathFile, Size: 10, Content: []byte("#!/bin/sh\n")},
	}
	for i := range want {
		if !sameFile(got[i], want[i]) {
			t.Errorf("file %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestReadFilesRefusesInvalidPaths(t *testing.T) {
	f := newFixture(t)
	c := newCounter(t)
	r := open(t, f.repo.Dir(), git.Options{Git: c.path})
	c.starts(t)

	// -dash.txt exists in the fixture: a path starting with a dash is refused
	// all the same.
	for _, p := range []string{"-dash.txt", "--output=x", "../outside", "/etc/passwd", "a/../../b", "./README.md", "", `a\b`} {
		visited := false
		err := r.ReadFiles(context.Background(), f.first, []string{"README.md", p}, func(knowledge.File) error {
			visited = true
			return nil
		})
		if !errors.Is(err, knowledge.ErrInvalidPath) {
			t.Errorf("ReadFiles(%q) = %v, want ErrInvalidPath", p, err)
		}
		if visited {
			t.Errorf("ReadFiles(%q) visited a file before refusing", p)
		}
	}
	if starts := c.starts(t); len(starts) != 0 {
		t.Errorf("refused paths started git: %q", starts)
	}
}

func TestReadFilesRefusesWhatIsNotACommit(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})
	for _, commit := range []string{"main", strings.Repeat("0", 40), "--all"} {
		err := r.ReadFiles(context.Background(), commit, []string{"README.md"}, func(knowledge.File) error { return nil })
		if !errors.Is(err, knowledge.ErrUnknownRevision) {
			t.Errorf("ReadFiles at %q = %v, want ErrUnknownRevision", commit, err)
		}
	}
}

func TestReadFilesStartsABoundedNumberOfProcesses(t *testing.T) {
	repo := gittest.New(t)
	var paths []string
	contents := map[string]string{}
	for i := range 400 {
		p := fmt.Sprintf("src/pkg%02d/file%03d.go", i%20, i)
		contents[p] = fmt.Sprintf("package pkg%02d\n\n// file %d\n", i%20, i)
		repo.WriteFile(p, []byte(contents[p]))
		paths = append(paths, p)
	}
	commit := repo.Commit("many files")
	c := newCounter(t)
	r := open(t, repo.Dir(), git.Options{Git: c.path})
	c.starts(t)

	for _, n := range []int{1, 10, 400} {
		count := 0
		err := r.ReadFiles(context.Background(), commit, paths[:n], func(f knowledge.File) error {
			if f.Kind != knowledge.PathFile || string(f.Content) != contents[f.Path] {
				return fmt.Errorf("unexpected %s: %s %q", f.Path, f.Kind, f.Content)
			}
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("ReadFiles(%d paths): %v", n, err)
		}
		if count != n {
			t.Errorf("visited %d files, want %d", count, n)
		}
		starts := c.starts(t)
		if len(starts) != 3 {
			t.Errorf("reading %d files started %d processes, want 3: %q", n, len(starts), starts)
		}
	}

	// Only links, directories and missing paths: nothing to read, no cat-file.
	readAll(t, r, commit, "src", "nope")
	if starts := c.starts(t); len(starts) != 2 {
		t.Errorf("reading no content started %d processes, want 2: %q", len(starts), starts)
	}
}

func TestReadFilesStopsWhenVisitFails(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})
	stop := errors.New("stop")

	visited := 0
	err := r.ReadFiles(context.Background(), f.first, []string{"README.md", "docs/empty.txt", "scripts/run.sh"}, func(knowledge.File) error {
		visited++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Errorf("ReadFiles = %v, want the error of visit", err)
	}
	if visited != 1 {
		t.Errorf("visited %d files after the error, want 1", visited)
	}
}

func TestReadFilesHonoursTheContext(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.repo.Dir(), git.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.ReadFiles(ctx, f.first, []string{"README.md"}, func(knowledge.File) error { return nil })
	if err == nil {
		t.Error("ReadFiles ran with a canceled context")
	}
}

func TestSHA256Repository(t *testing.T) {
	repo := gittest.NewSHA256(t)
	repo.WriteFile("a.txt", []byte("a\n"))
	commit := repo.Commit("first")
	r := open(t, repo.Dir(), git.Options{})

	got, err := r.ResolveRevision(context.Background(), "main")
	if err != nil || got != commit || len(got) != 64 {
		t.Fatalf("ResolveRevision(main) = %q, %v, want %s", got, err, commit)
	}
	files := readAll(t, r, commit, "a.txt")
	if len(files) != 1 || string(files[0].Content) != "a\n" {
		t.Errorf("ReadFiles = %+v", files)
	}
}

// TestIgnoresHostileConfiguration gives Git every chance to run a command or
// show other content: a caller environment and a repository configuration
// that would run a program, and a replacement object.
func TestIgnoresHostileConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hostile commands are shell scripts")
	}
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hostile := filepath.Join(t.TempDir(), "hostile.sh")
	if err := os.WriteFile(hostile, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	config := strings.Join([]string{
		"[core]",
		"\tfsmonitor = " + hostile,
		"\tpager = " + hostile,
		"\thooksPath = " + filepath.Dir(hostile),
		"\tsshCommand = " + hostile,
		"[uploadpack]",
		"\tpackObjectsHook = " + hostile,
		"",
	}, "\n")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.pager'='"+hostile+"'")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_PAGER", hostile)
	t.Setenv("GIT_EXTERNAL_DIFF", hostile)

	local, err := os.OpenFile(filepath.Join(f.repo.Dir(), ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.WriteString(config); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	// A replacement makes plain Git show other content for README.md.
	original := f.repo.Git("rev-parse", f.first+":README.md")
	other := f.repo.Git("rev-parse", f.second+":README.md")
	f.repo.Git("replace", original, other)
	if got := f.repo.Git("cat-file", "blob", original); got != "# Billing" {
		t.Fatalf("the replacement does not apply to plain Git: %q", got)
	}

	r := open(t, f.repo.Dir(), git.Options{})
	if _, err := r.ListTree(context.Background(), f.first); err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	files := readAll(t, r, f.first, "README.md")
	if string(files[0].Content) != "# Billing\n\nInvoices are sent monthly.\n" {
		t.Errorf("README.md = %q: the replacement object was read", files[0].Content)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a configured command ran")
	}
}

// TestReadsDoNotWrite compares every file of the fixture, its .git directory
// included, before and after reading.
func TestReadsDoNotWrite(t *testing.T) {
	f := newFixture(t)
	before := snapshot(t, f.repo.Dir())
	// File times have a coarse resolution on some file systems.
	time.Sleep(10 * time.Millisecond)

	r := open(t, f.repo.Dir(), git.Options{})
	ctx := context.Background()
	for _, revision := range []string{"main", "v1", "nope"} {
		_, _ = r.ResolveRevision(ctx, revision)
	}
	if _, err := r.ListTree(ctx, f.first); err != nil {
		t.Fatal(err)
	}
	readAll(t, r, f.first, "README.md", "assets/logo.png", "current", "vendor/lib", "missing")

	if after := snapshot(t, f.repo.Dir()); after != before {
		t.Errorf("the repository changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(&b, "%s %s %d %d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
