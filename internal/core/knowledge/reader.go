package knowledge

import (
	"bytes"
	"context"
	"errors"
)

// ErrUnknownRevision reports a revision or a commit hash that does not name a
// commit of the repository: a typo, a shallow clone, a rewritten history.
var ErrUnknownRevision = errors.New("unknown revision")

// ErrTreeTooLarge reports a commit whose tree holds more entries than the
// reader lists.
var ErrTreeTooLarge = errors.New("tree has too many entries")

// CommitReader reads one repository at a commit, without a checkout. It is
// bound to its repository when it is built; no operation takes a repository.
//
// Paths are slash-separated and relative to the repository root. A path given
// to ReadFiles must pass ValidatePath, and an invalid one fails the call
// before anything is read. A commit given to ListTree or ReadFiles is a full
// hash, as ResolveRevision returns it.
type CommitReader interface {
	// ResolveRevision returns the full hash of the commit a revision names: a
	// hash, a branch, a tag. An unknown revision gives ErrUnknownRevision.
	ResolveRevision(ctx context.Context, revision string) (string, error)

	// ListTree returns every entry of the commit, directories included, in
	// byte order of their paths. An unknown commit gives ErrUnknownRevision,
	// a tree above the bound of the reader ErrTreeTooLarge.
	ListTree(ctx context.Context, commit string) ([]TreeEntry, error)

	// ReadFiles calls visit once for each distinct path, in byte order, with
	// what the commit holds there. Many paths cost the same small number of
	// processes as one, so a caller reads the files of a commit in one call.
	// An error returned by visit stops the reading and is returned as it is.
	// An unknown commit and a tree above the bound fail as for ListTree.
	ReadFiles(ctx context.Context, commit string, paths []string, visit func(File) error) error
}

// PathKind says what a commit holds at a path.
type PathKind string

// Path kinds. Only PathFile has content; the others give the evidence reasons
// missing-file and not-regular.
const (
	// PathFile is a regular file, executable or not.
	PathFile PathKind = "file"
	// PathDirectory is a directory.
	PathDirectory PathKind = "directory"
	// PathLink is a symbolic link. Its target is never followed.
	PathLink PathKind = "link"
	// PathSubmodule is a submodule: a reference to a commit of another
	// repository, never read.
	PathSubmodule PathKind = "submodule"
	// PathMissing is a path the commit does not hold, including a path below a
	// file, a link or a submodule.
	PathMissing PathKind = "missing"
)

// TreeEntry is one entry of the tree of a commit.
type TreeEntry struct {
	Path string
	Kind PathKind
	// Size is the size in bytes of a PathFile or of the target of a PathLink,
	// and zero for the other kinds.
	Size int64
}

// File is what a commit holds at a requested path.
type File struct {
	Path string
	Kind PathKind
	// Size is the size in bytes of a PathFile, zero for the other kinds.
	Size int64
	// TooLarge reports a PathFile larger than the reader reads. Its Content is
	// nil and Binary is false: the reader did not look.
	TooLarge bool
	// Content is the content of a PathFile that is not TooLarge, and nil
	// otherwise. It is not shared with any other File.
	Content []byte
	// Binary reports a PathFile whose Content IsBinary.
	Binary bool
}

// binaryPrefix is how many leading bytes IsBinary looks at, as Git does.
const binaryPrefix = 8000

// IsBinary reports whether content is not text: a NUL byte in its first 8000
// bytes. It is the rule Git uses to decide whether to show a diff.
func IsBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), binaryPrefix)], 0) >= 0
}
