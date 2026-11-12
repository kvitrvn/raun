package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// memoryReader is a CommitReader over files held in memory, keyed by commit
// then path. It follows the contract of ReadFiles and records each call.
type memoryReader struct {
	commits map[string]map[string]File
	calls   []readCall
	fail    error
}

type readCall struct {
	commit string
	paths  []string
}

func (m *memoryReader) ResolveRevision(context.Context, string) (string, error) {
	return "", errors.New("not used")
}

func (m *memoryReader) ListTree(context.Context, string) ([]TreeEntry, error) {
	return nil, errors.New("not used")
}

func (m *memoryReader) ReadFiles(ctx context.Context, commit string, paths []string, visit func(File) error) error {
	m.calls = append(m.calls, readCall{commit: commit, paths: slices.Clone(paths)})
	for _, p := range paths {
		if err := ValidatePath(p); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.fail != nil {
		return m.fail
	}
	files, ok := m.commits[commit]
	if !ok {
		return fmt.Errorf("commit %s: %w", commit, ErrUnknownRevision)
	}
	wanted := slices.Clone(paths)
	slices.Sort(wanted)
	for _, p := range slices.Compact(wanted) {
		f, ok := files[p]
		if !ok {
			f = File{Kind: PathMissing}
		}
		f.Path = p
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}

func text(content string) File {
	return File{Kind: PathFile, Size: int64(len(content)), Content: []byte(content), Binary: IsBinary([]byte(content))}
}

var (
	commitA = strings.Repeat("a", 40)
	commitB = strings.Repeat("b", 40)
)

// lines returns "line 1\nline 2\n...line n\n".
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

const invoice = "package billing\n" + // 1
	"\n" + // 2
	"func Update(inv Invoice) error {\n" + // 3
	"\tif inv.Status == StatusIssued {\n" + // 4
	"\t\treturn ErrInvoiceLocked\n" + // 5
	"\t}\n" + // 6
	"\treturn nil\n" + // 7
	"}\n" // 8

const twice = "a()\nb()\n\na()\nb()\n" // a()/b() at 1-2 and 4-5

func cite(path string, start, end int, excerpt string) Evidence {
	e := evidence("e1")
	e.Path, e.Commit, e.StartLine, e.EndLine, e.Excerpt = path, commitA, start, end, excerpt
	e.SHA256 = ExcerptSHA256(excerpt)
	e.State, e.Reason = "", ""
	return e
}

func newTestVerifier(t *testing.T, r CommitReader, maxLines int) *Verifier {
	t.Helper()
	v, err := NewVerifier(r, maxLines)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func checkedAt() time.Time { return at(100) }

func TestIntegrity(t *testing.T) {
	reader := &memoryReader{commits: map[string]map[string]File{commitA: {
		"billing/invoice.go":    text(invoice),
		"billing/invoice_crlf":  text(strings.ReplaceAll(invoice, "\n", "\r\n")),
		"billing/no_newline.go": text("first\nlast"),
		"twice.go":              text(twice),
		"overlap.go":            text("x\nx\nx\n"),
		"blank_range.go":        text("a\n\n\n\nb\n"),
		"long.txt":              text(lines(60)),
		"empty.txt":             text(""),
		"docs":                  {Kind: PathDirectory},
		"current":               {Kind: PathLink},
		"vendor/lib":            {Kind: PathSubmodule},
		"logo.png":              text("PNG\x00\x01\n"),
		"big.sql":               {Kind: PathFile, Size: 9 << 20, TooLarge: true},
		"latin1.txt":            text("caf\xe9\n"),
	}}}
	v := newTestVerifier(t, reader, 0)

	tests := []struct {
		name       string
		evidence   Evidence
		state      EvidenceState
		reason     Reason
		start, end int // the lines expected after the check
	}{
		{"verified", cite("billing/invoice.go", 4, 6, "\tif inv.Status == StatusIssued {\n\t\treturn ErrInvoiceLocked\n\t}"), EvidenceVerified, "", 4, 6},
		{"whitespace runs, tabs and edges", cite("billing/invoice.go", 4, 5, "if   inv.Status ==\tStatusIssued {   \n  return    ErrInvoiceLocked"), EvidenceVerified, "", 4, 5},
		{"CRLF in the file", cite("billing/invoice_crlf", 3, 3, "func Update(inv Invoice) error {"), EvidenceVerified, "", 3, 3},
		{"CRLF in the excerpt", cite("billing/invoice.go", 4, 5, "if inv.Status == StatusIssued {\r\nreturn ErrInvoiceLocked\r\n"), EvidenceVerified, "", 4, 5},
		{"blank lines at both ends of the excerpt", cite("billing/invoice.go", 3, 3, "\n\n  func Update(inv Invoice) error {\n\n"), EvidenceVerified, "", 3, 3},
		{"blank lines at both ends of the range", cite("billing/invoice.go", 2, 3, "func Update(inv Invoice) error {"), EvidenceVerified, "", 2, 3},
		{"last line without a newline", cite("billing/no_newline.go", 2, 2, "last"), EvidenceVerified, "", 2, 2},
		{"at the cited lines and elsewhere", cite("twice.go", 4, 5, "a()\nb()"), EvidenceVerified, "", 4, 5},
		{"relocated", cite("billing/invoice.go", 1, 2, "return ErrInvoiceLocked"), EvidenceRelocated, "", 5, 5},
		{"relocated without the blank ends", cite("billing/invoice.go", 1, 1, "\nreturn nil\n}\n\n"), EvidenceRelocated, "", 7, 8},
		{"interior blank line kept", cite("blank_range.go", 1, 1, "a\n\nb"), EvidenceInvalid, ReasonNotFound, 1, 1},
		{"interior blank lines matched", cite("blank_range.go", 2, 2, "a\n\n\n\nb"), EvidenceRelocated, "", 1, 5},
		{"ambiguous", cite("twice.go", 3, 3, "a()\nb()"), EvidenceInvalid, ReasonAmbiguous, 3, 3},
		{"ambiguous overlapping", cite("overlap.go", 3, 3, "x\nx"), EvidenceInvalid, ReasonAmbiguous, 3, 3},
		{"not found", cite("billing/invoice.go", 4, 4, "if inv.Status == StatusDraft {"), EvidenceInvalid, ReasonNotFound, 4, 4},
		{"a fragment of a line", cite("billing/invoice.go", 4, 4, "inv.Status == StatusIssued"), EvidenceInvalid, ReasonNotFound, 4, 4},
		{"blank excerpt", cite("billing/invoice.go", 2, 2, " \t\n\n"), EvidenceInvalid, ReasonNotFound, 2, 2},
		{"missing file", cite("billing/gone.go", 1, 1, "x"), EvidenceInvalid, ReasonMissingFile, 1, 1},
		{"directory", cite("docs", 1, 1, "x"), EvidenceInvalid, ReasonNotRegular, 1, 1},
		{"link", cite("current", 1, 1, "x"), EvidenceInvalid, ReasonNotRegular, 1, 1},
		{"submodule", cite("vendor/lib", 1, 1, "x"), EvidenceInvalid, ReasonNotRegular, 1, 1},
		{"binary", cite("logo.png", 1, 1, "PNG"), EvidenceInvalid, ReasonBinary, 1, 1},
		{"too large", cite("big.sql", 1, 1, "x"), EvidenceInvalid, ReasonTooLarge, 1, 1},
		{"range past the end", cite("billing/invoice.go", 8, 9, "}"), EvidenceInvalid, ReasonBadRange, 8, 9},
		{"range starting at 0", cite("billing/invoice.go", 0, 1, "package billing"), EvidenceInvalid, ReasonBadRange, 0, 1},
		{"reversed range", cite("billing/invoice.go", 5, 4, "return ErrInvoiceLocked"), EvidenceInvalid, ReasonBadRange, 5, 4},
		{"range in an empty file", cite("empty.txt", 1, 1, "x"), EvidenceInvalid, ReasonBadRange, 1, 1},
		{"extreme lines", cite("billing/invoice.go", math.MinInt, math.MinInt+1, "x"), EvidenceInvalid, ReasonBadRange, math.MinInt, math.MinInt + 1},
		{"40 lines", cite("long.txt", 1, 40, lines(40)), EvidenceVerified, "", 1, 40},
		{"41 lines of range", cite("long.txt", 1, 41, lines(40)), EvidenceInvalid, ReasonTooLong, 1, 41},
		{"41 lines of excerpt", cite("long.txt", 1, 40, lines(41)), EvidenceInvalid, ReasonTooLong, 1, 40},
		{"the longest range", cite("long.txt", 1, math.MaxInt, "x"), EvidenceInvalid, ReasonTooLong, 1, math.MaxInt},
		{"a range across every int", cite("long.txt", math.MinInt, math.MaxInt, "x"), EvidenceInvalid, ReasonBadRange, math.MinInt, math.MaxInt},
		{"a bad range before the file is looked at", cite("billing/gone.go", 0, 1, "x"), EvidenceInvalid, ReasonBadRange, 0, 1},
		{"a reversed range is not too long", cite("long.txt", 50, 1, lines(3)), EvidenceInvalid, ReasonBadRange, 50, 1},
		{"too long before the file is looked at", cite("billing/gone.go", 1, 41, "x"), EvidenceInvalid, ReasonTooLong, 1, 41},
		{"invalid UTF-8 compared as it is", cite("latin1.txt", 1, 1, "caf\xe9"), EvidenceVerified, "", 1, 1},
		{"another invalid byte", cite("latin1.txt", 1, 1, "caf\xe8"), EvidenceInvalid, ReasonNotFound, 1, 1},
		{"the replacement character is not an invalid byte", cite("latin1.txt", 1, 1, "caf\uFFFD"), EvidenceInvalid, ReasonNotFound, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Integrity(context.Background(), []Evidence{tt.evidence}, checkedAt())
			if err != nil {
				t.Fatalf("Integrity: %v", err)
			}
			e := got[0]
			if e.State != tt.state || e.Reason != tt.reason || e.StartLine != tt.start || e.EndLine != tt.end {
				t.Errorf("got %s/%q at %d-%d, want %s/%q at %d-%d", e.State, e.Reason, e.StartLine, e.EndLine, tt.state, tt.reason, tt.start, tt.end)
			}
			if e.CheckedAt != checkedAt() || e.CheckedCommit != commitA {
				t.Errorf("checked at %v on %q", e.CheckedAt, e.CheckedCommit)
			}
			if e.Excerpt != tt.evidence.Excerpt || e.SHA256 != tt.evidence.SHA256 || e.Commit != tt.evidence.Commit || e.Path != tt.evidence.Path {
				t.Errorf("the citation changed: %+v", e)
			}
			if e.State != EvidenceInvalid || e.Reason != ReasonBadRange {
				assertValidEvidence(t, e)
			}
		})
	}
}

func TestIntegrityWithAnotherCap(t *testing.T) {
	reader := &memoryReader{commits: map[string]map[string]File{commitA: {"long.txt": text(lines(60))}}}
	v := newTestVerifier(t, reader, 3)
	got, err := v.Integrity(context.Background(), []Evidence{
		cite("long.txt", 1, 3, lines(3)),
		cite("long.txt", 1, 4, lines(4)),
	}, checkedAt())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != EvidenceVerified || got[1].Reason != ReasonTooLong {
		t.Errorf("cap 3: %s/%q and %s/%q", got[0].State, got[0].Reason, got[1].State, got[1].Reason)
	}
}

func stored(path string, start, end int, excerpt string, state EvidenceState, reason Reason) Evidence {
	e := cite(path, start, end, excerpt)
	e.State, e.Reason = state, reason
	e.CheckedAt, e.CheckedCommit = at(50), commitA
	return e
}

func TestFreshness(t *testing.T) {
	reader := &memoryReader{commits: map[string]map[string]File{commitB: {
		"billing/invoice.go": text(invoice),
		"moved.go":           text("// header\n" + invoice),
		"twice.go":           text(twice),
		"short.go":           text("one\n"),
		"docs":               {Kind: PathDirectory},
		"logo.png":           text("\x00"),
		"big.sql":            {Kind: PathFile, Size: 9 << 20, TooLarge: true},
		"long.txt":           text(lines(60)),
		"latin1.txt":         text("caf\xe8\n"),
	}}}
	v := newTestVerifier(t, reader, 3)

	locked := "if inv.Status == StatusIssued {\nreturn ErrInvoiceLocked"
	tests := []struct {
		name     string
		evidence Evidence
		state    EvidenceState
		reason   Reason
	}{
		{"verified at the stored lines", stored("billing/invoice.go", 4, 5, locked, EvidenceVerified, ""), EvidenceVerified, ""},
		{"relocated becomes verified", stored("billing/invoice.go", 4, 5, locked, EvidenceRelocated, ""), EvidenceVerified, ""},
		{"stale holds again", stored("billing/invoice.go", 4, 5, locked, EvidenceStale, ReasonNotFound), EvidenceVerified, ""},
		{"found at one other place", stored("moved.go", 4, 5, locked, EvidenceVerified, ""), EvidenceVerified, ""},
		{"found at several places", stored("twice.go", 3, 3, "a()\nb()", EvidenceVerified, ""), EvidenceStale, ReasonAmbiguous},
		{"at the stored lines and elsewhere", stored("twice.go", 1, 2, "a()\nb()", EvidenceVerified, ""), EvidenceVerified, ""},
		{"not found", stored("billing/invoice.go", 4, 4, "if inv.Status == StatusDraft {", EvidenceVerified, ""), EvidenceStale, ReasonNotFound},
		{"stored range past a shorter file", stored("short.go", 5, 6, "five\nsix", EvidenceVerified, ""), EvidenceStale, ReasonNotFound},
		{"missing file", stored("gone.go", 1, 1, "x", EvidenceVerified, ""), EvidenceStale, ReasonMissingFile},
		{"not regular", stored("docs", 1, 1, "x", EvidenceRelocated, ""), EvidenceStale, ReasonNotRegular},
		{"binary", stored("logo.png", 1, 1, "x", EvidenceVerified, ""), EvidenceStale, ReasonBinary},
		{"too large", stored("big.sql", 1, 1, "x", EvidenceVerified, ""), EvidenceStale, ReasonTooLarge},
		{"no cap at freshness", stored("long.txt", 1, 10, lines(10), EvidenceVerified, ""), EvidenceVerified, ""},
		{"an invalid byte that changed", stored("latin1.txt", 1, 1, "caf\xe9", EvidenceVerified, ""), EvidenceStale, ReasonNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Freshness(context.Background(), []Evidence{tt.evidence}, commitB, checkedAt())
			if err != nil {
				t.Fatalf("Freshness: %v", err)
			}
			e := got[0]
			if e.State != tt.state || e.Reason != tt.reason {
				t.Errorf("got %s/%q, want %s/%q", e.State, e.Reason, tt.state, tt.reason)
			}
			if e.CheckedAt != checkedAt() || e.CheckedCommit != commitB {
				t.Errorf("checked at %v on %q", e.CheckedAt, e.CheckedCommit)
			}
			want := tt.evidence
			if e.Commit != want.Commit || e.StartLine != want.StartLine || e.EndLine != want.EndLine || e.Excerpt != want.Excerpt || e.SHA256 != want.SHA256 {
				t.Errorf("the stored citation changed: %+v", e)
			}
			assertValidEvidence(t, e)
		})
	}
}

func TestFreshnessSkipsInvalidEvidence(t *testing.T) {
	reader := &memoryReader{commits: map[string]map[string]File{commitB: {"a.go": text("a\n")}}}
	v := newTestVerifier(t, reader, 0)
	invalid := stored("a.go", 1, 1, "a", EvidenceInvalid, ReasonNotFound)
	badRange := stored("b.go", 9, 2, "a", EvidenceInvalid, ReasonBadRange)

	got, err := v.Freshness(context.Background(), []Evidence{invalid, badRange}, commitB, checkedAt())
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != invalid || got[1] != badRange {
		t.Errorf("invalid evidence changed: %+v", got)
	}
	if len(reader.calls) != 0 {
		t.Errorf("invalid evidence was read: %v", reader.calls)
	}
}

func TestFreshnessRefusesUncheckedEvidence(t *testing.T) {
	v := newTestVerifier(t, &memoryReader{}, 0)
	e := cite("a.go", 1, 1, "a") // no state yet
	if _, err := v.Freshness(context.Background(), []Evidence{e}, commitB, checkedAt()); err == nil || !strings.Contains(err.Error(), "evidence[0]") {
		t.Errorf("Freshness = %v, want an error naming evidence[0]", err)
	}
}

func TestVerifierBatchesReads(t *testing.T) {
	commitC := strings.Repeat("c", 40)
	reader := &memoryReader{commits: map[string]map[string]File{
		commitA: {"a.go": text("a\nb\n"), "b.go": text("b\n")},
		commitC: {"a.go": text("c\n")},
	}}
	v := newTestVerifier(t, reader, 0)
	onC := cite("a.go", 1, 1, "c")
	onC.Commit = commitC
	input := []Evidence{
		cite("b.go", 1, 1, "b"),
		onC,
		cite("a.go", 1, 1, "a"),
		cite("a.go", 2, 2, "b"),
		cite("b.go", 1, 1, "x"),
	}
	before := slices.Clone(input)

	got, err := v.Integrity(context.Background(), input, checkedAt())
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []readCall{{commitA, []string{"a.go", "b.go"}}, {commitC, []string{"a.go"}}}
	if len(reader.calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", reader.calls, wantCalls)
	}
	for i, c := range wantCalls {
		if reader.calls[i].commit != c.commit || !slices.Equal(reader.calls[i].paths, c.paths) {
			t.Errorf("call %d = %v, want %v", i, reader.calls[i], c)
		}
	}
	states := []EvidenceState{EvidenceVerified, EvidenceVerified, EvidenceVerified, EvidenceVerified, EvidenceInvalid}
	for i, e := range got {
		if e.State != states[i] || e.Path != input[i].Path || e.Commit != input[i].Commit {
			t.Errorf("result %d = %s %s@%s, want %s for %s@%s", i, e.State, e.Path, e.CheckedCommit, states[i], input[i].Path, input[i].Commit)
		}
	}
	if !slices.Equal(input, before) {
		t.Error("Integrity changed its input")
	}

	reader.calls = nil
	fresh, err := v.Freshness(context.Background(), got, commitA, checkedAt())
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.calls) != 1 || !slices.Equal(reader.calls[0].paths, []string{"a.go", "b.go"}) {
		t.Errorf("freshness calls = %v, want one read of a.go and b.go", reader.calls)
	}
	if fresh[4] != got[4] {
		t.Error("freshness changed an invalid evidence")
	}
}

func TestVerifierRefusesBadInput(t *testing.T) {
	reader := &memoryReader{commits: map[string]map[string]File{commitA: {"a.go": text("a\n")}}}
	v := newTestVerifier(t, reader, 0)
	ok := cite("a.go", 1, 1, "a")
	badPath := cite("-a.go", 1, 1, "a")
	badCommit := cite("a.go", 1, 1, "a")
	badCommit.Commit = "main"

	tests := []struct {
		name  string
		check func() error
		want  string
	}{
		{"path", func() error {
			_, err := v.Integrity(context.Background(), []Evidence{ok, badPath}, checkedAt())
			return err
		}, "evidence[1]: path"},
		{"commit", func() error {
			_, err := v.Integrity(context.Background(), []Evidence{badCommit}, checkedAt())
			return err
		}, "evidence[0]: commit"},
		{"zero time", func() error { _, err := v.Integrity(context.Background(), []Evidence{ok}, time.Time{}); return err }, "UTC"},
		{"local time", func() error {
			_, err := v.Integrity(context.Background(), []Evidence{ok}, checkedAt().In(time.FixedZone("CET", 3600)))
			return err
		}, "UTC"},
		{"freshness commit", func() error {
			_, err := v.Freshness(context.Background(), []Evidence{ok}, "HEAD", checkedAt())
			return err
		}, "full commit hash"},
	}
	for _, tt := range tests {
		reader.calls = nil
		if err := tt.check(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
		if len(reader.calls) != 0 {
			t.Errorf("%s: the reader was called: %v", tt.name, reader.calls)
		}
	}
}

func TestVerifierPassesReaderErrorsOn(t *testing.T) {
	unknown := cite("a.go", 1, 1, "a")
	unknown.Commit = strings.Repeat("d", 40)
	v := newTestVerifier(t, &memoryReader{commits: map[string]map[string]File{}}, 0)
	if _, err := v.Integrity(context.Background(), []Evidence{unknown}, checkedAt()); !errors.Is(err, ErrUnknownRevision) {
		t.Errorf("unknown commit: %v, want ErrUnknownRevision", err)
	}

	v = newTestVerifier(t, &memoryReader{fail: fmt.Errorf("listing: %w", ErrTreeTooLarge)}, 0)
	if _, err := v.Freshness(context.Background(), []Evidence{stored("a.go", 1, 1, "a", EvidenceVerified, "")}, commitB, checkedAt()); !errors.Is(err, ErrTreeTooLarge) {
		t.Errorf("tree too large: %v, want ErrTreeTooLarge", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v = newTestVerifier(t, &memoryReader{commits: map[string]map[string]File{commitA: {}}}, 0)
	if _, err := v.Integrity(ctx, []Evidence{cite("a.go", 1, 1, "a")}, checkedAt()); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v, want context.Canceled", err)
	}
}

// liarReader gives a file that was not asked for, or skips one.
type liarReader struct {
	memoryReader
	give []string
}

func (l *liarReader) ReadFiles(_ context.Context, _ string, _ []string, visit func(File) error) error {
	for _, p := range l.give {
		if err := visit(File{Path: p, Kind: PathMissing}); err != nil {
			return err
		}
	}
	return nil
}

func TestVerifierRefusesAReaderThatBreaksItsContract(t *testing.T) {
	for _, give := range [][]string{{}, {"other.go"}, {"a.go", "a.go"}} {
		v := newTestVerifier(t, &liarReader{give: give}, 0)
		if _, err := v.Integrity(context.Background(), []Evidence{cite("a.go", 1, 1, "a")}, checkedAt()); err == nil {
			t.Errorf("reader giving %q: no error", give)
		}
	}
}

func TestEmptyBatches(t *testing.T) {
	reader := &memoryReader{}
	v := newTestVerifier(t, reader, 0)
	if got, err := v.Integrity(context.Background(), nil, checkedAt()); err != nil || len(got) != 0 {
		t.Errorf("Integrity(nil) = %v, %v", got, err)
	}
	if got, err := v.Freshness(context.Background(), nil, commitB, checkedAt()); err != nil || len(got) != 0 {
		t.Errorf("Freshness(nil) = %v, %v", got, err)
	}
	if len(reader.calls) != 0 {
		t.Errorf("empty batches read: %v", reader.calls)
	}
}

func TestNewVerifier(t *testing.T) {
	if _, err := NewVerifier(nil, 0); err == nil || !strings.Contains(err.Error(), "reader") {
		t.Errorf("nil reader: %v", err)
	}
	if _, err := NewVerifier(&memoryReader{}, -1); err == nil || !strings.Contains(err.Error(), "maxLines") {
		t.Errorf("negative maxLines: %v", err)
	}
	v, err := NewVerifier(&memoryReader{}, 0)
	if err != nil || v.maxLines != DefaultMaxCitationLines {
		t.Errorf("NewVerifier(0) = %+v, %v, want the default of %d lines", v, err, DefaultMaxCitationLines)
	}
}

func TestNormalizeLine(t *testing.T) {
	tests := map[string]string{
		"":                  "",
		" \t ":              "",
		"a":                 "a",
		"  a  b\t\tc  ":     "a b c",
		"\ta \t b":          "a b",
		"a b":               "a b", // only spaces and tabs are blank
		"a\rb":              "a\rb",
		"caf\xe9  \xff":     "caf\xe9 \xff", // bytes are kept, valid UTF-8 or not
		"\xe9":              "\xe9",
		"tab\there and  so": "tab here and so",
	}
	for in, want := range tests {
		if got := normalizeLine(in); got != want {
			t.Errorf("normalizeLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// assertValidEvidence checks that e is a valid evidence of an item: its state
// and its reason go together.
func assertValidEvidence(t *testing.T, e Evidence) {
	t.Helper()
	it := newRequirement(t)
	e.ID = it.Evidence[0].ID
	it.Evidence[0] = e
	if err := it.Validate(); err != nil {
		t.Errorf("the result is not a valid evidence: %v", err)
	}
}
