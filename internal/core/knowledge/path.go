package knowledge

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrInvalidPath reports a repository path that is not clean, leaves the
// repository or could be read by Git as an option.
var ErrInvalidPath = errors.New("invalid repository path")

// ValidatePath checks a path that comes from outside Raun, such as an agent
// citation or the repository configuration, before anything reads it. The
// path must be relative to the repository root, use forward slashes, be clean
// (no empty, "." or ".." segment, no trailing slash), stay inside the
// repository and not start with a dash. The error wraps ErrInvalidPath.
//
// A path that passes is still only a name: whether it exists, and what it is,
// is for the commit reader to say.
func ValidatePath(p string) error {
	if msg := checkPath(p); msg != "" {
		return fmt.Errorf("%w: %s", ErrInvalidPath, msg)
	}
	return nil
}

// checkPath returns why p is not a valid repository path, or an empty string.
// Validate reports the same messages at the field of the evidence.
func checkPath(p string) string {
	switch {
	case p == "":
		return "required"
	case strings.HasPrefix(p, "/"):
		return fmt.Sprintf("must be relative to the repository root, got %q", p)
	case strings.HasPrefix(p, "-"):
		// Git would read it as an option wherever it is passed as an argument.
		return fmt.Sprintf("must not start with a dash, got %q", p)
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Sprintf("must use forward slashes only, got %q", p)
	case path.Clean(p) != p || p == ".":
		return fmt.Sprintf("must be a clean path, got %q", p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Sprintf("must stay inside the repository, got %q", p)
	}
	return ""
}
