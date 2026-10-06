// Package source decides which repository paths agents may cite and what
// kind of source each one is. Raun classifies sources itself; agents never
// declare a source kind.
package source

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/knowledge"
)

// DefaultDocs are the patterns classifying a path as documentation when the
// configuration does not set `sources.docs`.
var DefaultDocs = []string{"**/*.md", "**/*.rst", "**/*.adoc", "**/*.txt", "docs/**"}

// ErrExcluded reports a path that matches `sources.exclude`.
var ErrExcluded = errors.New("path is excluded from sources")

// Classifier maps repository paths to source kinds.
type Classifier struct {
	context []string
	docs    []string
	exclude []string
}

// NewClassifier builds a classifier. Context paths are exact repository
// paths; docs and exclude are glob patterns (see Match).
func NewClassifier(context, docs, exclude []string) *Classifier {
	return &Classifier{context: context, docs: docs, exclude: exclude}
}

// Classify returns the kind of the source at p. Precedence: context files
// (even inside an excluded directory), then exclusions, then docs, then code.
func (c *Classifier) Classify(p string) (knowledge.SourceKind, error) {
	switch {
	case slices.Contains(c.context, p):
		return knowledge.SourceHumanContext, nil
	case matchAny(c.exclude, p):
		return "", fmt.Errorf("%s: %w", p, ErrExcluded)
	case matchAny(c.docs, p):
		return knowledge.SourceDoc, nil
	}
	return knowledge.SourceCode, nil
}

func matchAny(patterns []string, p string) bool {
	return slices.ContainsFunc(patterns, func(pat string) bool { return Match(pat, p) })
}

// Match reports whether the slash-separated path p matches pattern. The
// pattern is matched against the whole path; within a segment, the syntax
// is that of path.Match, and a `**` segment matches any number of segments,
// including none. So `*.md` only matches at the root, `**/*.md` anywhere.
func Match(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// ValidPattern reports a syntax error in pattern, if any.
func ValidPattern(pattern string) error {
	if pattern == "" {
		return errors.New("empty pattern")
	}
	for seg := range strings.SplitSeq(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("invalid pattern %q", pattern)
		}
	}
	return nil
}
