package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// start is the date of the first commit of every fixture, 2026-01-01T00:00:00Z
// in seconds since the epoch. Each later commit is one minute after the
// previous one, so a fixture always gets the same identifiers and its history
// is ordered.
const start = 1767225600

// Repo is a fixture repository in a temporary directory, removed when the test
// ends.
type Repo struct {
	t       testing.TB
	git     string
	dir     string
	env     []string
	commits int
}

// New creates an empty repository with a main branch and SHA-1 object names.
func New(t testing.TB) *Repo {
	t.Helper()
	return newRepo(t, "sha1")
}

// NewSHA256 creates an empty repository with a main branch and SHA-256 object
// names.
func NewSHA256(t testing.TB) *Repo {
	t.Helper()
	return newRepo(t, "sha256")
}

func newRepo(t testing.TB, format string) *Repo {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("gittest: git not found: %v", err)
	}
	r := &Repo{
		t:   t,
		git: git,
		dir: t.TempDir(),
		// The environment is built from nothing: a variable such as GIT_DIR
		// inherited from the caller would point git at another repository,
		// and the user or system configuration would change identifiers.
		env: []string{
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"HOME=" + t.TempDir(),
			"LC_ALL=C",
			"TZ=UTC",
			"GIT_AUTHOR_NAME=Raun Test",
			"GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Raun Test",
			"GIT_COMMITTER_EMAIL=test@example.invalid",
		},
	}
	r.Git("init", "--quiet", "--initial-branch=main", "--object-format="+format, "--template=")
	return r
}

// Dir returns the root of the working tree.
func (r *Repo) Dir() string { return r.dir }

// WriteFile creates or replaces a regular file, creating its parent
// directories. Directories exist in a commit through the files they hold.
func (r *Repo) WriteFile(name string, content []byte) {
	r.t.Helper()
	r.write(name, content, 0o644)
}

// WriteExecutable creates or replaces an executable regular file.
func (r *Repo) WriteExecutable(name string, content []byte) {
	r.t.Helper()
	r.write(name, content, 0o755)
}

func (r *Repo) write(name string, content []byte, mode os.FileMode) {
	r.t.Helper()
	p := r.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
	if err := os.WriteFile(p, content, mode); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
	// WriteFile keeps the mode of a file that already exists.
	if err := os.Chmod(p, mode); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
}

// Symlink creates a symbolic link at name pointing to target, which is stored
// as it is and may point anywhere, outside the repository included.
func (r *Repo) Symlink(name, target string) {
	r.t.Helper()
	p := r.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
	if err := os.Symlink(target, p); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
}

// Submodule records a submodule at name pointing to commit, without cloning
// anything: the commit only has to look like an object name.
func (r *Repo) Submodule(name, commit string) {
	r.t.Helper()
	// An empty directory keeps the next Commit from staging the submodule as
	// deleted.
	if err := os.MkdirAll(r.path(name), 0o755); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
	r.Git("update-index", "--add", "--cacheinfo", "160000,"+commit+","+name)
}

// Remove deletes a file, a link or a directory and everything below it.
func (r *Repo) Remove(name string) {
	r.t.Helper()
	if err := os.RemoveAll(r.path(name)); err != nil {
		r.t.Fatalf("gittest: %v", err)
	}
}

// Commit stages every change and commits it, and returns the full object name
// of the commit.
func (r *Repo) Commit(message string) string {
	r.t.Helper()
	date := fmt.Sprintf("%d +0000", start+60*r.commits)
	r.commits++
	r.Git("add", "--all")
	r.gitEnv([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
		"commit", "--quiet", "--allow-empty", "--no-verify", "--no-gpg-sign", "--message", message)
	return r.Git("rev-parse", "--verify", "HEAD^{commit}")
}

// Git runs git in the working tree with the fixture environment and returns
// its output without the trailing newline. A failure fails the test.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return r.gitEnv(nil, args...)
}

func (r *Repo) gitEnv(extra []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, args...)
	cmd.Dir = r.dir
	cmd.Env = append(append([]string(nil), r.env...), extra...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("gittest: git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSuffix(stdout.String(), "\n")
}

// path returns the absolute path of name, which must be a local slash-separated
// path such as "docs/a.md".
func (r *Repo) path(name string) string {
	r.t.Helper()
	if !filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, `\`) {
		r.t.Fatalf("gittest: %q is not a local slash-separated path", name)
	}
	return filepath.Join(r.dir, filepath.FromSlash(name))
}
