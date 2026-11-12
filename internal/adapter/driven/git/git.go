package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

// Default bounds of a Reader.
const (
	// DefaultMaxFileSize is the largest file a Reader reads: 8 MiB.
	DefaultMaxFileSize int64 = 8 << 20
	// DefaultMaxTreeEntries is the largest tree a Reader lists.
	DefaultMaxTreeEntries = 1_000_000
)

// The oldest Git a Reader accepts: GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM,
// which keep the user and system configuration out, exist since 2.32.
const (
	minMajor = 2
	minMinor = 32
)

// maxRecord bounds one line of the output of git: a tree entry, an object
// header, a hash.
const maxRecord = 64 << 10

// maxStderr bounds what is kept of the error output of git.
const maxStderr = 4 << 10

// Options tunes a Reader. A zero field takes its default.
type Options struct {
	// Git is the path of the git binary; empty means the git found in PATH.
	Git string
	// MaxFileSize is the size in bytes above which a file is reported as
	// TooLarge instead of being read.
	MaxFileSize int64
	// MaxTreeEntries is the number of entries above which listing or reading
	// a commit fails with knowledge.ErrTreeTooLarge.
	MaxTreeEntries int
}

// Reader reads one repository through the git binary. It holds no state that
// changes, so one Reader serves concurrent calls.
type Reader struct {
	git            string
	gitDir         string
	maxFileSize    int64
	maxTreeEntries int
}

var _ knowledge.CommitReader = (*Reader)(nil)

// Open returns a Reader of the repository whose root is dir: the top of a
// working tree, or a bare repository. A directory below the root is refused,
// so that Git never looks above dir for a repository. Open checks the version
// of Git once.
func Open(ctx context.Context, dir string, opts Options) (*Reader, error) {
	if dir == "" {
		return nil, errors.New("open repository: empty directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("open repository %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("open repository %s: not a directory", abs)
	}
	// Git walks up from the physical directory: the ceiling must be computed
	// on it, or a link to a directory below a root would let Git reach that
	// root. Messages keep naming the directory the caller gave.
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}

	r := &Reader{
		git:            opts.Git,
		maxFileSize:    opts.MaxFileSize,
		maxTreeEntries: opts.MaxTreeEntries,
	}
	if r.git == "" {
		if r.git, err = exec.LookPath("git"); err != nil {
			return nil, fmt.Errorf("open repository: %w", err)
		}
	}
	if !filepath.IsAbs(r.git) {
		return nil, fmt.Errorf("open repository: git binary %q is not an absolute path", r.git)
	}
	if r.maxFileSize <= 0 {
		r.maxFileSize = DefaultMaxFileSize
	}
	if r.maxTreeEntries <= 0 {
		r.maxTreeEntries = DefaultMaxTreeEntries
	}

	out, err := r.output(ctx, physical, nil, "version")
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	if err := checkVersion(string(out)); err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}

	// The ceiling keeps Git from finding a repository above dir.
	ceiling := []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(physical)}
	out, err = r.output(ctx, physical, ceiling, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("open repository %s: not the root of a Git repository: %w", abs, err)
	}
	r.gitDir = strings.TrimSuffix(string(out), "\n")
	if !filepath.IsAbs(r.gitDir) {
		return nil, fmt.Errorf("open repository %s: unexpected Git directory %q", abs, r.gitDir)
	}
	return r, nil
}

// ResolveRevision returns the full hash of the commit revision names. A
// revision that starts with a dash or holds a control character is refused
// before Git runs.
func (r *Reader) ResolveRevision(ctx context.Context, revision string) (string, error) {
	if revision == "" || strings.HasPrefix(revision, "-") || strings.ContainsFunc(revision, isControl) {
		return "", fmt.Errorf("resolve revision %q: %w", revision, knowledge.ErrUnknownRevision)
	}
	out, err := r.output(ctx, r.gitDir, nil,
		"rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", fmt.Errorf("resolve revision %q: %w", revision, knowledge.ErrUnknownRevision)
		}
		return "", fmt.Errorf("resolve revision %q: %w", revision, err)
	}
	hash := strings.TrimSuffix(string(out), "\n")
	if !isHash(hash) {
		return "", fmt.Errorf("resolve revision %q: unexpected output %q", revision, hash)
	}
	return hash, nil
}

// ListTree returns every entry of the tree of commit, directories included,
// in byte order of their paths.
func (r *Reader) ListTree(ctx context.Context, commit string) ([]knowledge.TreeEntry, error) {
	if err := r.checkCommit(ctx, commit); err != nil {
		return nil, fmt.Errorf("list tree: %w", err)
	}
	var entries []knowledge.TreeEntry
	err := r.walkTree(ctx, commit, func(e entry) {
		entries = append(entries, knowledge.TreeEntry{Path: e.path, Kind: e.kind, Size: e.size})
	})
	if err != nil {
		return nil, fmt.Errorf("list tree of %s: %w", commit, err)
	}
	slices.SortFunc(entries, func(a, b knowledge.TreeEntry) int { return strings.Compare(a.Path, b.Path) })
	return entries, nil
}

// ReadFiles calls visit once for each distinct path, in byte order. It starts
// three processes whatever the number of paths: one to check the commit, one
// to list its tree and one to read the files, the last only when there is a
// file to read. Only object names and the commit hash reach Git; a path never
// does.
func (r *Reader) ReadFiles(ctx context.Context, commit string, paths []string, visit func(knowledge.File) error) error {
	for _, p := range paths {
		if err := knowledge.ValidatePath(p); err != nil {
			return fmt.Errorf("read files: %w", err)
		}
	}
	wanted := slices.Clone(paths)
	slices.Sort(wanted)
	wanted = slices.Compact(wanted)
	if len(wanted) == 0 {
		return nil
	}
	if err := r.checkCommit(ctx, commit); err != nil {
		return fmt.Errorf("read files: %w", err)
	}

	found := make(map[string]entry, len(wanted))
	for _, p := range wanted {
		found[p] = entry{}
	}
	err := r.walkTree(ctx, commit, func(e entry) {
		if _, ok := found[e.path]; ok {
			found[e.path] = e
		}
	})
	if err != nil {
		return fmt.Errorf("read files at %s: %w", commit, err)
	}

	var objects []string
	for _, p := range wanted {
		if e := found[p]; e.kind == knowledge.PathFile && e.size <= r.maxFileSize {
			objects = append(objects, e.object)
		}
	}
	var batch *catFile
	if len(objects) > 0 {
		if batch, err = r.startCatFile(ctx, objects); err != nil {
			return fmt.Errorf("read files at %s: %w", commit, err)
		}
	}

	for _, p := range wanted {
		e := found[p]
		f := knowledge.File{Path: p, Kind: e.kind}
		switch {
		case e.kind == "":
			f.Kind = knowledge.PathMissing
		case e.kind != knowledge.PathFile:
		case e.size > r.maxFileSize:
			f.Size, f.TooLarge = e.size, true
		default:
			content, err := batch.read(e.object, e.size)
			if err != nil {
				batch.abort()
				return fmt.Errorf("read %s at %s: %w", p, commit, err)
			}
			f.Size, f.Content, f.Binary = e.size, content, knowledge.IsBinary(content)
		}
		if err := visit(f); err != nil {
			batch.abort()
			return err
		}
	}
	if err := batch.close(); err != nil {
		return fmt.Errorf("read files at %s: %w", commit, err)
	}
	return nil
}

// checkCommit refuses anything but the full hash of a commit of the
// repository. The syntax is checked first, so a value starting with a dash
// never reaches Git.
func (r *Reader) checkCommit(ctx context.Context, commit string) error {
	if !isHash(commit) {
		return fmt.Errorf("commit %q is not a full commit hash: %w", commit, knowledge.ErrUnknownRevision)
	}
	resolved, err := r.ResolveRevision(ctx, commit)
	if err != nil {
		return err
	}
	if resolved != commit {
		return fmt.Errorf("commit %s resolves to %s: %w", commit, resolved, knowledge.ErrUnknownRevision)
	}
	return nil
}

// entry is one entry of a tree as Git lists it.
type entry struct {
	path   string
	kind   knowledge.PathKind
	object string
	size   int64
}

// walkTree calls fn for each entry of the tree of commit, in the order of Git.
func (r *Reader) walkTree(ctx context.Context, commit string, fn func(entry)) error {
	cmd := r.command(ctx, r.gitDir, nil, "ls-tree", "-r", "-t", "-z", "--long", "--full-tree", commit)
	stderr := &limitedBuffer{limit: maxStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git ls-tree: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git ls-tree: %w", err)
	}

	walkErr := func() error {
		br := bufio.NewReaderSize(stdout, maxRecord)
		for count := 0; ; count++ {
			record, err := br.ReadSlice(0)
			if errors.Is(err, io.EOF) && len(record) == 0 {
				return nil
			}
			if err != nil {
				return fmt.Errorf("git ls-tree: read entry: %w", err)
			}
			if count >= r.maxTreeEntries {
				return fmt.Errorf("more than %d entries: %w", r.maxTreeEntries, knowledge.ErrTreeTooLarge)
			}
			e, err := parseEntry(record[:len(record)-1])
			if err != nil {
				return fmt.Errorf("git ls-tree: %w", err)
			}
			fn(e)
		}
	}()
	if walkErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return walkErr
	}
	if err := cmd.Wait(); err != nil {
		return commandError("ls-tree", stderr, err)
	}
	return nil
}

// parseEntry reads "<mode> SP <type> SP <object> SP+ <size> TAB <path>", the
// size being "-" for anything but a blob.
func parseEntry(record []byte) (entry, error) {
	meta, name, ok := bytes.Cut(record, []byte{'\t'})
	fields := strings.Fields(string(meta))
	if !ok || len(fields) != 4 || len(name) == 0 {
		return entry{}, fmt.Errorf("unexpected tree entry %q", record)
	}
	mode, typ, object, size := fields[0], fields[1], fields[2], fields[3]
	e := entry{path: string(name), object: object}
	switch {
	case typ == "tree" && mode == "040000":
		e.kind = knowledge.PathDirectory
	case typ == "commit" && mode == "160000":
		e.kind = knowledge.PathSubmodule
	case typ == "blob" && mode == "120000":
		e.kind = knowledge.PathLink
	case typ == "blob" && strings.HasPrefix(mode, "100"):
		e.kind = knowledge.PathFile
	default:
		return entry{}, fmt.Errorf("unexpected mode %s and type %s for %q", mode, typ, name)
	}
	if !isHash(object) {
		return entry{}, fmt.Errorf("unexpected object name %q for %q", object, name)
	}
	if typ == "blob" {
		n, err := strconv.ParseInt(size, 10, 64)
		if err != nil || n < 0 {
			return entry{}, fmt.Errorf("unexpected size %q for %q", size, name)
		}
		e.size = n
	}
	return e, nil
}

// catFile is a running "git cat-file --batch" that answers the objects it was
// given, in order.
type catFile struct {
	cmd    *exec.Cmd
	out    *bufio.Reader
	stderr *limitedBuffer
	wrote  chan error
}

func (r *Reader) startCatFile(ctx context.Context, objects []string) (*catFile, error) {
	cmd := r.command(ctx, r.gitDir, nil, "cat-file", "--batch")
	stderr := &limitedBuffer{limit: maxStderr}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}

	// Writing runs apart from reading: Git answers while it reads, and both
	// pipes are bounded.
	wrote := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, object := range objects {
			if _, err := w.WriteString(object + "\n"); err != nil {
				wrote <- errors.Join(err, stdin.Close())
				return
			}
		}
		wrote <- errors.Join(w.Flush(), stdin.Close())
	}()
	return &catFile{cmd: cmd, out: bufio.NewReaderSize(stdout, maxRecord), stderr: stderr, wrote: wrote}, nil
}

// read returns the content of the next object, which must be the blob object
// of the given size.
func (c *catFile) read(object string, size int64) ([]byte, error) {
	header, err := c.out.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file: read header: %w", err)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 || fields[0] != object || fields[1] != "blob" || fields[2] != strconv.FormatInt(size, 10) {
		return nil, fmt.Errorf("git cat-file: unexpected header %q for blob %s of %d bytes", bytes.TrimSpace(header), object, size)
	}
	content := make([]byte, size+1)
	if _, err := io.ReadFull(c.out, content); err != nil {
		return nil, fmt.Errorf("git cat-file: read blob %s: %w", object, err)
	}
	if content[size] != '\n' {
		return nil, fmt.Errorf("git cat-file: blob %s does not end where its header says", object)
	}
	return content[:size:size], nil
}

// close waits for Git to end once every object was read. A nil catFile has
// nothing to close.
func (c *catFile) close() error {
	if c == nil {
		return nil
	}
	writeErr := <-c.wrote
	if err := c.cmd.Wait(); err != nil {
		return commandError("cat-file", c.stderr, err)
	}
	if writeErr != nil {
		return fmt.Errorf("git cat-file: write object names: %w", writeErr)
	}
	return nil
}

// abort stops Git before the end of the batch.
func (c *catFile) abort() {
	if c == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
	<-c.wrote
}

// output runs git and returns its standard output, bounded.
func (r *Reader) output(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := r.command(ctx, dir, extraEnv, args...)
	stdout := &limitedBuffer{limit: maxRecord}
	stderr := &limitedBuffer{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, commandError(args[0], stderr, err)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("git %s: output longer than %d bytes", args[0], maxRecord)
	}
	return stdout.Bytes(), nil
}

// command builds a git process with an environment made from nothing: the
// caller's variables, the user and the system configuration never reach it,
// and the options before the subcommand override what the configuration of
// the repository could make Git run or fetch.
func (r *Reader) command(ctx context.Context, dir string, extraEnv []string, args ...string) *exec.Cmd {
	full := []string{
		"--no-optional-locks",
		"--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "protocol.allow=never",
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.git, full...)
	cmd.Dir = dir
	cmd.Env = []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_PROTOCOL_FROM_USER=0",
		"HOME=" + os.DevNull,
		"XDG_CONFIG_HOME=" + os.DevNull,
		"LC_ALL=C",
	}
	if r.gitDir != "" {
		cmd.Env = append(cmd.Env, "GIT_DIR="+r.gitDir)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd
}

func commandError(name string, stderr *limitedBuffer, err error) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("git %s: %s: %w", name, msg, err)
	}
	return fmt.Errorf("git %s: %w", name, err)
}

// checkVersion refuses a Git older than the minimum, from the output of
// "git version", such as "git version 2.39.3 (Apple Git-145)".
func checkVersion(out string) error {
	version, ok := strings.CutPrefix(strings.TrimSpace(out), "git version ")
	if !ok {
		return fmt.Errorf("unexpected git version %q", out)
	}
	version, _, _ = strings.Cut(version, " ")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("unexpected git version %q", version)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("unexpected git version %q", version)
	}
	if major < minMajor || major == minMajor && minor < minMinor {
		return fmt.Errorf("git %s is older than %d.%d, the oldest supported", version, minMajor, minMinor)
	}
	return nil
}

// isHash reports whether s is a full SHA-1 or SHA-256 object name in lowercase
// hexadecimal.
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// limitedBuffer keeps the first limit bytes written to it and drops the rest.
type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room < len(p) {
		b.overflow = true
		b.Buffer.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
