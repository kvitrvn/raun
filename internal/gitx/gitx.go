// Package gitx runs the operations Raun needs on a Git repository through
// the `git` binary.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// Export writes the files of commit into dest, without any Git metadata.
// Paths for which skip returns true are left out. Symlinks are recreated
// only when they point inside dest; submodules become empty directories.
// Unlike `git archive`, export-ignore and export-subst attributes are not
// applied, so exported files are byte-identical to the commit.
func (r *Repo) Export(ctx context.Context, commit, dest string, skip func(path string) bool) error {
	hash, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return err
	}
	out, err := run(ctx, r.root, "ls-tree", "-r", "-z", "--full-tree", hash)
	if err != nil {
		return err
	}

	type entry struct{ mode, object, path string }
	var blobs []entry
	for raw := range bytes.SplitSeq(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		meta, name, ok := bytes.Cut(raw, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 {
			return fmt.Errorf("unexpected ls-tree output %q", raw)
		}
		e := entry{mode: fields[0], object: fields[2], path: string(name)}
		if skip != nil && skip(e.path) {
			continue
		}
		target, err := safeJoin(dest, e.path)
		if err != nil {
			return err
		}
		if e.mode == "160000" {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		blobs = append(blobs, e)
	}

	ids := make([]string, len(blobs))
	for i, e := range blobs {
		ids[i] = e.object
	}
	contents, err := r.catBlobs(ctx, ids)
	if err != nil {
		return err
	}
	for i, e := range blobs {
		target, _ := safeJoin(dest, e.path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data := contents[i]
		switch e.mode {
		case "120000":
			link := string(data)
			resolved := filepath.Join(filepath.Dir(target), link)
			if filepath.IsAbs(link) || !within(dest, resolved) {
				continue
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		case "100755":
			if err := os.WriteFile(target, data, 0o755); err != nil {
				return err
			}
		default:
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// catBlobs reads many objects through one `git cat-file --batch` process.
func (r *Repo) catBlobs(ctx context.Context, ids []string) ([][]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}

	writeErr := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, id := range ids {
			if _, err := w.WriteString(id + "\n"); err != nil {
				writeErr <- err
				return
			}
		}
		err := w.Flush()
		writeErr <- errors.Join(err, stdin.Close())
	}()

	br := bufio.NewReader(stdout)
	contents := make([][]byte, 0, len(ids))
	var readErr error
	for range ids {
		header, err := br.ReadString('\n')
		if err != nil {
			readErr = fmt.Errorf("git cat-file: %w", err)
			break
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			readErr = fmt.Errorf("git cat-file: unexpected header %q", header)
			break
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			readErr = fmt.Errorf("git cat-file: unexpected header %q", header)
			break
		}
		data := make([]byte, size+1) // content + trailing LF
		if _, err := io.ReadFull(br, data); err != nil {
			readErr = fmt.Errorf("git cat-file: %w", err)
			break
		}
		contents = append(contents, data[:size])
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err := errors.Join(readErr, <-writeErr); err != nil {
		return nil, err
	}
	if waitErr != nil {
		return nil, fmt.Errorf("git cat-file: %s: %w", strings.TrimSpace(stderr.String()), waitErr)
	}
	return contents, nil
}

// ChangedPaths lists working tree paths that differ from HEAD, including
// untracked files that are not ignored.
func (r *Repo) ChangedPaths(ctx context.Context) ([]string, error) {
	out, err := run(ctx, r.root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	records := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 {
			continue
		}
		paths = append(paths, string(rec[3:]))
		if rec[0] == 'R' || rec[0] == 'C' {
			i++ // the next record is the original path
		}
	}
	return paths, nil
}

// safeJoin joins a repository path to dir, refusing paths that escape it.
func safeJoin(dir, p string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(p))
	if !within(dir, target) {
		return "", fmt.Errorf("refusing path outside the export directory: %q", p)
	}
	return target, nil
}

func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ConfigValue returns a Git configuration value, or "" when it is unset.
func (r *Repo) ConfigValue(ctx context.Context, key string) (string, error) {
	out, err := run(ctx, r.root, "config", "--get", key)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
