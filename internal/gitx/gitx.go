// Package gitx runs the operations Raun needs on a Git repository through
// the `git` binary.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
)

var (
	// ErrUnknownCommit reports a revision that does not name a commit in
	// the repository (typo, shallow clone, rewritten history).
	ErrUnknownCommit = errors.New("unknown commit")
	// ErrNotAFile reports a path that exists at a commit but is not a
	// regular file (directory, symlink, submodule).
	ErrNotAFile = errors.New("not a regular file")
)

// Repo is a Git working tree.
type Repo struct {
	root string
}

// Open returns the repository containing dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a Git repository: %w", dir, err)
	}
	return &Repo{root: strings.TrimSpace(string(out))}, nil
}

// Root returns the absolute path of the working tree root.
func (r *Repo) Root() string { return r.root }

// ResolveCommit returns the full hash of the commit named by rev.
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%w: %q", ErrUnknownCommit, rev)
	}
	out, err := run(ctx, r.root, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%w: %q", ErrUnknownCommit, rev)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ReadFile returns the content of path (relative to the repository root)
// at commit. A missing path yields an error wrapping fs.ErrNotExist; a path
// that is not a regular file yields ErrNotAFile.
func (r *Repo) ReadFile(ctx context.Context, commit, path string) ([]byte, error) {
	hash, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return nil, err
	}

	// ls-tree tells blobs from trees, symlinks and submodules without
	// parsing error messages.
	out, err := run(ctx, r.root, "ls-tree", "-z", "--full-tree", hash, "--", path)
	if err != nil {
		return nil, err
	}
	entry, _, _ := bytes.Cut(out, []byte{0})
	if len(entry) == 0 {
		return nil, fmt.Errorf("%s at %s: %w", path, short(hash), fs.ErrNotExist)
	}
	meta, name, ok := bytes.Cut(entry, []byte{'\t'})
	fields := strings.Fields(string(meta))
	if !ok || len(fields) != 3 {
		return nil, fmt.Errorf("unexpected ls-tree output %q", entry)
	}
	if string(name) != path {
		return nil, fmt.Errorf("%s at %s: %w", path, short(hash), fs.ErrNotExist)
	}
	mode, typ, object := fields[0], fields[1], fields[2]
	if typ != "blob" || (mode != "100644" && mode != "100755") {
		return nil, fmt.Errorf("%s at %s: %w", path, short(hash), ErrNotAFile)
	}

	return run(ctx, r.root, "cat-file", "blob", object)
}

// run executes git in dir. Pathspecs are taken literally so that file
// names containing glob characters are not expanded.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("git %s: %s: %w", args[0], msg, err)
	}
	return out, nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
