package gittest

import (
	"regexp"
	"strings"
	"testing"
)

var (
	sha1Name   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Name = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// build makes the same fixture every time: text, binary, executable, nested
// directories, a link and a submodule, over two commits.
func build(r *Repo) []string {
	r.WriteFile("README.md", []byte("# Fixture\n"))
	r.WriteFile("docs/guide/intro.md", []byte("Intro\n"))
	r.WriteFile("assets/logo.bin", []byte{0x89, 'P', 'N', 'G', 0x00, 0x01})
	r.WriteExecutable("bin/run.sh", []byte("#!/bin/sh\n"))
	r.Symlink("link", "README.md")
	r.Submodule("vendor/lib", strings.Repeat("1", 40))
	first := r.Commit("first")
	r.WriteFile("README.md", []byte("# Fixture, second\n"))
	r.Remove("docs")
	second := r.Commit("second")
	return []string{first, second}
}

func TestCommitIdentifiersAreStable(t *testing.T) {
	a := build(New(t))
	b := build(New(t))
	if strings.Join(a, " ") != strings.Join(b, " ") {
		t.Fatalf("two runs of one fixture differ: %v and %v", a, b)
	}
	for _, c := range a {
		if !sha1Name.MatchString(c) {
			t.Errorf("commit %q is not a full SHA-1 name", c)
		}
	}
	if a[0] == a[1] {
		t.Errorf("both commits are %s", a[0])
	}
}

func TestSHA256(t *testing.T) {
	r := NewSHA256(t)
	r.WriteFile("a.txt", []byte("a\n"))
	c := r.Commit("first")
	if !sha256Name.MatchString(c) {
		t.Fatalf("commit %q is not a full SHA-256 name", c)
	}
	r2 := NewSHA256(t)
	r2.WriteFile("a.txt", []byte("a\n"))
	if c2 := r2.Commit("first"); c2 != c {
		t.Errorf("two runs differ: %s and %s", c, c2)
	}
}

func TestTreeHoldsEveryKind(t *testing.T) {
	r := New(t)
	commits := build(r)

	tests := []struct {
		commit string
		want   string
	}{
		{commits[0], strings.Join([]string{
			"100644 blob README.md",
			"100644 blob assets/logo.bin",
			"100755 blob bin/run.sh",
			"100644 blob docs/guide/intro.md",
			"120000 blob link",
			"160000 commit vendor/lib",
		}, "\n")},
		{commits[1], strings.Join([]string{
			"100644 blob README.md",
			"100644 blob assets/logo.bin",
			"100755 blob bin/run.sh",
			"120000 blob link",
			"160000 commit vendor/lib",
		}, "\n")},
	}
	for _, tt := range tests {
		if got := tree(r, tt.commit); got != tt.want {
			t.Errorf("tree of %s:\n%s\nwant:\n%s", tt.commit, got, tt.want)
		}
	}

	if got := r.Git("cat-file", "blob", commits[0]+":link"); got != "README.md" {
		t.Errorf("link target = %q, want README.md", got)
	}
	if got := r.Git("log", "--format=%aI %s", "main"); got != "2026-01-01T00:01:00Z second\n2026-01-01T00:00:00Z first" {
		t.Errorf("history:\n%s", got)
	}
}

func TestRewriteChangesTheMode(t *testing.T) {
	r := New(t)
	r.WriteExecutable("a.sh", []byte("x\n"))
	r.WriteFile("a.sh", []byte("x\n"))
	c := r.Commit("first")
	if got := tree(r, c); got != "100644 blob a.sh" {
		t.Errorf("tree = %s, want a.sh in mode 100644", got)
	}
}

func TestEmptyCommit(t *testing.T) {
	r := New(t)
	if got := tree(r, r.Commit("empty")); got != "" {
		t.Errorf("tree of an empty commit = %q", got)
	}
}

func TestSubmoduleInSHA256(t *testing.T) {
	r := NewSHA256(t)
	r.Submodule("lib", strings.Repeat("2", 64))
	c := r.Commit("first")
	if got := tree(r, c); got != "160000 commit lib" {
		t.Errorf("tree = %s", got)
	}
}

// tree lists the tree of commit as "<mode> <type> <path>" lines, without the
// object names, with the output format every supported Git has.
func tree(r *Repo, commit string) string {
	r.t.Helper()
	var lines []string
	for line := range strings.SplitSeq(r.Git("ls-tree", "-r", commit), "\n") {
		if line == "" {
			continue
		}
		meta, path, _ := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			r.t.Fatalf("unexpected ls-tree line %q", line)
		}
		lines = append(lines, fields[0]+" "+fields[1]+" "+path)
	}
	return strings.Join(lines, "\n")
}

func TestEnvironmentIgnoresTheCaller(t *testing.T) {
	// A GIT_DIR inherited from the caller would make git work on another
	// repository; the fixture must not see it.
	other := New(t)
	t.Setenv("GIT_DIR", other.Dir()+"/.git")
	t.Setenv("GIT_AUTHOR_NAME", "Someone Else")

	r := New(t)
	r.WriteFile("a.txt", []byte("a\n"))
	r.Commit("first")
	if got := r.Git("log", "--format=%an"); got != "Raun Test" {
		t.Errorf("author = %q", got)
	}
	if got := other.Git("rev-list", "--all"); got != "" {
		t.Errorf("the other repository got commits: %q", got)
	}
}
