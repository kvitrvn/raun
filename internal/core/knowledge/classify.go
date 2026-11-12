package knowledge

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrExcluded reports a path the source sets exclude: nothing may cite it.
var ErrExcluded = errors.New("path is excluded from sources")

// DefaultDocs returns the patterns that classify a path as documentation when
// the repository configuration sets none, in a new slice.
func DefaultDocs() []string {
	return []string{"**/*.md", "**/*.rst", "**/*.adoc", "**/*.txt", "docs/**"}
}

// SourceSets says which paths of a repository are human context, which are
// documentation and which may not be cited at all. Everything else is code.
type SourceSets struct {
	// Context lists exact paths of files written by the team for Raun. They
	// are cited as human context, never as facts derived from the code.
	Context []string
	// Docs lists patterns of documentation paths. The classifier applies
	// exactly this list, so an empty one means no documentation; DefaultDocs
	// is for the caller that builds the sets from a configuration.
	Docs []string
	// Exclude lists patterns of paths that no evidence may cite.
	Exclude []string
}

// Classifier gives the source of a repository path. It is built once from
// validated sets and never changes.
type Classifier struct {
	context map[string]bool
	docs    []string
	exclude []string
}

// NewClassifier validates the sets and builds a classifier. A context entry
// must pass ValidatePath; a pattern must be valid, see Match. The error names
// every faulty entry.
func NewClassifier(sets SourceSets) (*Classifier, error) {
	var errs []error
	c := &Classifier{context: make(map[string]bool, len(sets.Context))}
	for i, p := range sets.Context {
		if err := ValidatePath(p); err != nil {
			errs = append(errs, fmt.Errorf("context[%d]: %w", i, err))
			continue
		}
		c.context[p] = true
	}
	for i, pattern := range sets.Docs {
		if err := ValidatePattern(pattern); err != nil {
			errs = append(errs, fmt.Errorf("docs[%d]: %w", i, err))
		}
	}
	for i, pattern := range sets.Exclude {
		if err := ValidatePattern(pattern); err != nil {
			errs = append(errs, fmt.Errorf("exclude[%d]: %w", i, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("source sets: %w", err)
	}
	c.docs = cloneStrings(sets.Docs)
	c.exclude = cloneStrings(sets.Exclude)
	return c, nil
}

// Classify returns the source of the file at p. The first rule that holds
// wins: a context path is human context, even inside an excluded directory; an
// excluded path gives ErrExcluded; a documentation path is documentation;
// anything else is code. A path that fails ValidatePath gives its error. The
// order of the entries in the sets never changes the result.
func (c *Classifier) Classify(p string) (Source, error) {
	if err := ValidatePath(p); err != nil {
		return "", err
	}
	switch {
	case c.context[p]:
		return SourceHumanContext, nil
	case matchAny(c.exclude, p):
		return "", fmt.Errorf("%s: %w", p, ErrExcluded)
	case matchAny(c.docs, p):
		return SourceDoc, nil
	}
	return SourceCode, nil
}

func matchAny(patterns []string, p string) bool {
	for _, pattern := range patterns {
		if Match(pattern, p) {
			return true
		}
	}
	return false
}

// Match reports whether the path p matches pattern, as a whole. Both are split
// on slashes. A segment of the pattern matches one segment of the path with
// the syntax of path.Match (*, ?, [...] and \ to escape); a segment that is
// exactly ** matches any number of segments, none included. So *.md matches
// at the root only, **/*.md anywhere, and docs/** matches docs itself and
// everything below it. Unlike .gitignore, a pattern without a slash is not
// matched at every depth, and there is no negation. An invalid pattern
// matches nothing.
func Match(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

// matchSegments matches like a wildcard over segments: ** is the wildcard and
// every other segment matches exactly one. On a mismatch it only goes back to
// the last ** seen, which is enough, so the work stays within the product of
// both lengths whatever the pattern: a pattern from the repository cannot make
// it explode.
func matchSegments(pattern, segments []string) bool {
	pi, si := 0, 0
	star, resume := -1, 0
	for si < len(segments) {
		switch {
		case pi < len(pattern) && pattern[pi] == "**":
			star, resume = pi, si
			pi++
		case pi < len(pattern) && matchSegment(pattern[pi], segments[si]):
			pi++
			si++
		case star >= 0:
			// Let the last ** take one more segment and retry after it.
			resume++
			pi, si = star+1, resume
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == "**" {
		pi++
	}
	return pi == len(pattern)
}

func matchSegment(pattern, segment string) bool {
	ok, err := path.Match(pattern, segment)
	return err == nil && ok
}

// ValidatePattern reports why pattern can never be used: empty, absolute, with
// an empty, "." or ".." segment, or with a segment path.Match refuses. Such a
// pattern would match no clean path, so a typo would go unnoticed.
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return errors.New("empty pattern")
	}
	if strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("pattern %q must be relative to the repository root", pattern)
	}
	for segment := range strings.SplitSeq(pattern, "/") {
		switch segment {
		case "":
			return fmt.Errorf("pattern %q has an empty segment", pattern)
		case ".", "..":
			return fmt.Errorf("pattern %q has a %q segment", pattern, segment)
		case "**":
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("pattern %q: %w", pattern, err)
		}
	}
	return nil
}
