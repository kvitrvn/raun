// Package gittest builds throwaway Git repositories for tests. It ignores
// the user's Git configuration so tests behave the same everywhere.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a fixture repository in a temporary directory.
type Repo struct {
	t   testing.TB
	Dir string
}

// New creates an empty repository with deterministic identity settings.
func New(t testing.TB) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir()}
	r.Git("init", "--quiet", "--initial-branch=main")
	return r
}

// Write creates or replaces a file in the working tree.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	p := filepath.Join(r.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Remove deletes a file from the working tree.
func (r *Repo) Remove(path string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.Dir, filepath.FromSlash(path))); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits it, returning the full hash.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.Git("add", "--all")
	r.Git("commit", "--quiet", "--allow-empty", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}

// Git runs a git command in the repository and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Raun Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Raun Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-10-07T09:30:00Z",
		"GIT_COMMITTER_DATE=2026-10-07T09:30:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
