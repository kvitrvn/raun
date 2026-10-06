// Package evidence checks, without any LLM, that a cited excerpt really is
// in the cited file at the cited lines.
//
// An excerpt matches when it equals whole lines of the file after whitespace
// normalization: CRLF is read as LF, runs of spaces and tabs count as one
// space, line edges are trimmed, and blank lines at the start and end of the
// excerpt or range are ignored. Fragments of a line do not match.
package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
)

// Source reads a file at a commit. *gitx.Repo implements it. A missing file
// must yield an error wrapping fs.ErrNotExist, and a non-regular file one
// wrapping gitx.ErrNotAFile.
type Source interface {
	ReadFile(ctx context.Context, commit, path string) ([]byte, error)
}

// Result is the outcome of verifying one evidence at one commit.
type Result struct {
	State knowledge.EvidenceState
	// StartLine and EndLine locate the excerpt at the checked commit;
	// both are 0 when it was not found.
	StartLine, EndLine int
	// Excerpt is the exact text of the located lines, as in the file.
	Excerpt string
	// Reason explains any state other than verified.
	Reason string
}

// Verifier checks evidence against a Source, caching file contents.
type Verifier struct {
	src      Source
	maxLines int
	files    map[fileKey]fileEntry
}

type fileKey struct{ commit, path string }

type fileEntry struct {
	raw   []string // lines as in the file, without line terminators
	lines []string // normalized
	err   error    // content-level problem (missing, not a file, binary)
}

// NewVerifier returns a verifier rejecting citations longer than maxLines.
func NewVerifier(src Source, maxLines int) *Verifier {
	return &Verifier{src: src, maxLines: maxLines, files: map[fileKey]fileEntry{}}
}

// Verify checks ev at commit `at`.
//
// At the evidence's own commit (integrity), the outcome is verified,
// relocated (the excerpt is in the file but at other lines) or invalid.
// At any other commit (freshness), it is verified, relocated or stale.
// An error is returned only when the check itself cannot run, such as an
// unknown commit or a failing git command.
func (v *Verifier) Verify(ctx context.Context, ev knowledge.Evidence, at string) (Result, error) {
	notFound := knowledge.EvidenceStale
	if at == ev.Commit {
		notFound = knowledge.EvidenceInvalid
	}

	if n := ev.EndLine - ev.StartLine + 1; n > v.maxLines {
		return invalid("cites %d lines, the limit is %d", n, v.maxLines), nil
	}
	want := trimBlank(normalize(ev.Excerpt))
	if len(want) == 0 {
		return invalid("excerpt is blank"), nil
	}
	if len(want) > v.maxLines {
		return invalid("excerpt has %d lines, the limit is %d", len(want), v.maxLines), nil
	}

	file, err := v.file(ctx, at, ev.Path)
	lines := file.lines
	var content *contentError
	if errors.As(err, &content) {
		return Result{State: notFound, Reason: content.msg}, nil
	}
	if err != nil {
		return Result{}, err
	}

	if ev.StartLine >= 1 && ev.EndLine <= len(lines) && slices.Equal(trimBlank(lines[ev.StartLine-1:ev.EndLine]), want) {
		return Result{
			State:     knowledge.EvidenceVerified,
			StartLine: ev.StartLine,
			EndLine:   ev.EndLine,
			Excerpt:   strings.Join(file.raw[ev.StartLine-1:ev.EndLine], "\n"),
		}, nil
	}

	start, ok := nearest(find(lines, want), ev.StartLine-1)
	if !ok {
		return Result{State: notFound, Reason: "excerpt not found in " + ev.Path}, nil
	}
	return Result{
		State:     knowledge.EvidenceRelocated,
		StartLine: start + 1,
		EndLine:   start + len(want),
		Excerpt:   strings.Join(file.raw[start:start+len(want)], "\n"),
		Reason:    fmt.Sprintf("excerpt found at lines %d-%d", start+1, start+len(want)),
	}, nil
}

func invalid(format string, args ...any) Result {
	return Result{State: knowledge.EvidenceInvalid, Reason: fmt.Sprintf(format, args...)}
}

// contentError marks problems with the file itself, which are verification
// outcomes rather than failures of the check.
type contentError struct{ msg string }

func (e *contentError) Error() string { return e.msg }

func (v *Verifier) file(ctx context.Context, commit, path string) (fileEntry, error) {
	key := fileKey{commit, path}
	if e, ok := v.files[key]; ok {
		return e, e.err
	}

	data, err := v.src.ReadFile(ctx, commit, path)
	var entry fileEntry
	switch {
	case errors.Is(err, fs.ErrNotExist):
		entry.err = &contentError{path + " does not exist at this commit"}
	case errors.Is(err, gitx.ErrNotAFile):
		entry.err = &contentError{path + " is not a regular file"}
	case err != nil:
		return fileEntry{}, err // not cached: may be transient
	case isBinary(data):
		entry.err = &contentError{path + " is a binary file"}
	default:
		entry.raw = splitLines(string(data))
		entry.lines = make([]string, len(entry.raw))
		for i, l := range entry.raw {
			entry.lines[i] = normalizeLine(l)
		}
	}
	v.files[key] = entry
	return entry, entry.err
}

// isBinary uses Git's heuristic: a NUL byte in the first 8000 bytes.
func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// normalize splits text into lines with whitespace normalized.
func normalize(text string) []string {
	lines := splitLines(text)
	for i, l := range lines {
		lines[i] = normalizeLine(l)
	}
	return lines
}

func normalizeLine(l string) string {
	return strings.Join(strings.Fields(l), " ")
}

// splitLines splits text on LF or CRLF, ignoring a final line terminator.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// trimBlank drops blank lines at both ends.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// find returns every 0-based index where want starts in lines.
func find(lines, want []string) []int {
	var at []int
	for i := 0; i+len(want) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(want)], want) {
			at = append(at, i)
		}
	}
	return at
}

// nearest returns the candidate closest to target; ties go to the first.
func nearest(candidates []int, target int) (int, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if abs(c-target) < abs(best-target) {
			best = c
		}
	}
	return best, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
