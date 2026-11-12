package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// DefaultMaxCitationLines is the longest citation, in lines, that integrity
// accepts when the verifier is built with no other bound.
const DefaultMaxCitationLines = 40

// Verifier checks evidence against the content of a repository, without any
// LLM, as docs/format.md defines it. It reads files through a CommitReader
// only, never starts a process and holds no state that changes.
type Verifier struct {
	reader   CommitReader
	maxLines int
}

// NewVerifier returns a verifier that reads through reader and refuses, at
// integrity, a citation longer than maxLines lines. A maxLines of 0 stands for
// DefaultMaxCitationLines.
func NewVerifier(reader CommitReader, maxLines int) (*Verifier, error) {
	if reader == nil {
		return nil, errors.New("new verifier: reader is nil")
	}
	if maxLines < 0 {
		return nil, fmt.Errorf("new verifier: maxLines must not be negative, got %d", maxLines)
	}
	if maxLines == 0 {
		maxLines = DefaultMaxCitationLines
	}
	return &Verifier{reader: reader, maxLines: maxLines}, nil
}

// Integrity checks each evidence at the commit it cites and returns the
// evidence with its state, reason, checked time and checked commit set, in the
// order given. The state is verified, relocated (the excerpt is at one other
// place only, and the lines are corrected) or invalid. The excerpt and its
// hash are never changed. at is the time of the check, in UTC.
//
// An error means the check could not run: an invalid input, or a failure of
// the reader, such as an unknown commit. It never stands for a state.
func (v *Verifier) Integrity(ctx context.Context, evidence []Evidence, at time.Time) ([]Evidence, error) {
	if err := checkUTC(at); err != nil {
		return nil, fmt.Errorf("check integrity: %w", err)
	}
	byCommit := map[string][]int{}
	for i, e := range evidence {
		if err := checkCitation(e); err != nil {
			return nil, fmt.Errorf("check integrity: evidence[%d]: %w", i, err)
		}
		byCommit[e.Commit] = append(byCommit[e.Commit], i)
	}

	out := cloneSlice(evidence)
	for _, commit := range sortedKeys(byCommit) {
		err := v.read(ctx, commit, evidence, byCommit[commit], func(i int, f File) {
			out[i] = v.integrity(evidence[i], f, at)
		})
		if err != nil {
			return nil, fmt.Errorf("check integrity at %s: %w", commit, err)
		}
	}
	return out, nil
}

// Freshness checks each evidence at commit, a later commit than the one it
// cites, and returns the evidence with its state, reason, checked time and
// checked commit set, in the order given. A verified, relocated or stale
// evidence becomes verified or stale. An invalid evidence had no ground to
// begin with: it is returned as it is and its file is not read. The commit,
// the lines, the excerpt and its hash are never changed, even when the excerpt
// is found at another place. at is the time of the check, in UTC.
//
// An error means the check could not run, as for Integrity.
func (v *Verifier) Freshness(ctx context.Context, evidence []Evidence, commit string, at time.Time) ([]Evidence, error) {
	if !isCommit(commit) {
		return nil, fmt.Errorf("check freshness: commit must be a full commit hash, got %q", commit)
	}
	if err := checkUTC(at); err != nil {
		return nil, fmt.Errorf("check freshness: %w", err)
	}
	var checked []int
	for i, e := range evidence {
		switch e.State {
		case EvidenceInvalid:
			continue
		case EvidenceVerified, EvidenceRelocated, EvidenceStale:
		default:
			return nil, fmt.Errorf("check freshness: evidence[%d]: state %q cannot be checked for freshness", i, e.State)
		}
		if err := checkCitation(e); err != nil {
			return nil, fmt.Errorf("check freshness: evidence[%d]: %w", i, err)
		}
		checked = append(checked, i)
	}

	out := cloneSlice(evidence)
	if len(checked) == 0 {
		return out, nil
	}
	err := v.read(ctx, commit, evidence, checked, func(i int, f File) {
		out[i] = freshness(evidence[i], f, commit, at)
	})
	if err != nil {
		return nil, fmt.Errorf("check freshness at %s: %w", commit, err)
	}
	return out, nil
}

// read reads, in one call, the files the evidence at indexes cite at commit,
// and calls decide for each of them with its file. Only one file is held at a
// time.
func (v *Verifier) read(ctx context.Context, commit string, evidence []Evidence, indexes []int, decide func(int, File)) error {
	byPath := map[string][]int{}
	for _, i := range indexes {
		byPath[evidence[i].Path] = append(byPath[evidence[i].Path], i)
	}
	paths := sortedKeys(byPath)
	visited := map[string]bool{}
	err := v.reader.ReadFiles(ctx, commit, paths, func(f File) error {
		at, ok := byPath[f.Path]
		if !ok || visited[f.Path] {
			return fmt.Errorf("reader gave %q, which was not asked for or was given twice", f.Path)
		}
		visited[f.Path] = true
		for _, i := range at {
			decide(i, f)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range paths {
		if !visited[p] {
			return fmt.Errorf("reader did not give %q", p)
		}
	}
	return nil
}

// integrity decides the state of e at its own commit, from what the commit
// holds at its path.
func (v *Verifier) integrity(e Evidence, f File, at time.Time) Evidence {
	block := excerptBlock(e.Excerpt)
	set := func(state EvidenceState, reason Reason) Evidence {
		e.State, e.Reason, e.CheckedAt, e.CheckedCommit = state, reason, at, e.Commit
		return e
	}

	// A range that is empty or reversed, and the length of a citation, are
	// properties of the citation: they are checked before the content.
	if e.StartLine < 1 || e.EndLine < e.StartLine {
		return set(EvidenceInvalid, ReasonBadRange)
	}
	if rangeLonger(e.StartLine, e.EndLine, v.maxLines) || len(block) > v.maxLines {
		return set(EvidenceInvalid, ReasonTooLong)
	}
	if reason, ok := fileReason(f); !ok {
		return set(EvidenceInvalid, reason)
	}
	lines := fileLines(f.Content)
	if e.EndLine > len(lines) {
		return set(EvidenceInvalid, ReasonBadRange)
	}
	if len(block) == 0 {
		return set(EvidenceInvalid, ReasonNotFound)
	}
	if matchesAt(lines, block, e.StartLine, e.EndLine) {
		return set(EvidenceVerified, "")
	}
	switch starts := occurrences(lines, block); len(starts) {
	case 0:
		return set(EvidenceInvalid, ReasonNotFound)
	case 1:
		e.StartLine, e.EndLine = starts[0]+1, starts[0]+len(block)
		return set(EvidenceRelocated, "")
	default:
		return set(EvidenceInvalid, ReasonAmbiguous)
	}
}

// freshness decides the state of e at commit, a later commit than its own,
// from what commit holds at its path. The cap on the length of a citation
// does not apply: it was checked at integrity, and may have changed since.
func freshness(e Evidence, f File, commit string, at time.Time) Evidence {
	set := func(state EvidenceState, reason Reason) Evidence {
		e.State, e.Reason, e.CheckedAt, e.CheckedCommit = state, reason, at, commit
		return e
	}

	if reason, ok := fileReason(f); !ok {
		return set(EvidenceStale, reason)
	}
	block := excerptBlock(e.Excerpt)
	if len(block) == 0 {
		return set(EvidenceStale, ReasonNotFound)
	}
	lines := fileLines(f.Content)
	if e.StartLine >= 1 && e.EndLine >= e.StartLine && e.EndLine <= len(lines) && matchesAt(lines, block, e.StartLine, e.EndLine) {
		return set(EvidenceVerified, "")
	}
	switch len(occurrences(lines, block)) {
	case 0:
		return set(EvidenceStale, ReasonNotFound)
	case 1:
		return set(EvidenceVerified, "")
	default:
		return set(EvidenceStale, ReasonAmbiguous)
	}
}

// rangeLonger reports whether the lines start to end span more than max
// lines. A reversed range spans none. The difference is taken unsigned, where
// it is exact for end >= start whatever the values, so no input overflows.
func rangeLonger(start, end, max int) bool {
	return end >= start && uint64(end)-uint64(start) >= uint64(max)
}

// fileReason returns why the file f cannot ground a citation, if it cannot.
func fileReason(f File) (Reason, bool) {
	switch {
	case f.Kind == PathMissing:
		return ReasonMissingFile, false
	case f.Kind != PathFile:
		return ReasonNotRegular, false
	case f.TooLarge:
		return ReasonTooLarge, false
	case f.Binary || IsBinary(f.Content):
		return ReasonBinary, false
	}
	return "", true
}

// fileLines returns the lines of content, normalized. CRLF is read as LF, and
// a last line without a newline is a line.
func fileLines(content []byte) []string {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = normalizeLine(l)
	}
	return lines
}

// excerptBlock returns the lines of an excerpt, normalized, without the blank
// lines at both ends. Blank lines inside the excerpt are kept.
func excerptBlock(excerpt string) []string {
	return trimBlank(fileLines([]byte(excerpt)))
}

// normalizeLine turns each run of spaces and tabs into one space and trims
// both edges. It works on bytes: every other byte is kept as it is, so two
// lines that differ in an invalid UTF-8 byte still differ.
func normalizeLine(l string) string {
	l = strings.Trim(l, " \t")
	var b strings.Builder
	b.Grow(len(l))
	space := false
	for i := 0; i < len(l); i++ {
		c := l[i]
		if c == ' ' || c == '\t' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// trimBlank drops the blank lines at both ends.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// matchesAt reports whether the lines start to end, 1-based and inclusive,
// within the bounds of lines, equal block once their blank ends are dropped.
func matchesAt(lines, block []string, start, end int) bool {
	return slices.Equal(trimBlank(lines[start-1:end]), block)
}

// occurrences returns the 0-based indexes where block starts in lines,
// overlapping ones included, stopping at the second: one is a relocation, two
// are as many as several.
func occurrences(lines, block []string) []int {
	var starts []int
	for i := 0; i+len(block) <= len(lines) && len(starts) < 2; i++ {
		if slices.Equal(lines[i:i+len(block)], block) {
			starts = append(starts, i)
		}
	}
	return starts
}

// checkCitation refuses an evidence the verifier cannot even look up: a path
// that is not a valid repository path, or a commit that is not a full hash.
func checkCitation(e Evidence) error {
	if err := ValidatePath(e.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if !isCommit(e.Commit) {
		return fmt.Errorf("commit must be a full commit hash, got %q", e.Commit)
	}
	return nil
}

func checkUTC(at time.Time) error {
	if at.IsZero() || at.Location() != time.UTC {
		return fmt.Errorf("the time of the check must be set and in UTC, got %v", at)
	}
	return nil
}

func sortedKeys(m map[string][]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
