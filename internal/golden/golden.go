package golden

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// flagName is the flag that makes Equal rewrite golden files.
const flagName = "update"

// maxQuoted bounds each line a mismatch quotes, so binary content stays
// readable.
const maxQuoted = 200

func init() {
	// The flag is looked up when Equal runs, so the package keeps no variable.
	// Another package of the same test binary may already own the name.
	if flag.Lookup(flagName) == nil {
		flag.Bool(flagName, false, "rewrite golden files instead of comparing them")
	}
}

// Equal compares got with the golden file at path, byte for byte, and fails
// the test on a difference or when the file is missing. With -update it
// rewrites the file instead, creating its directories. The path is relative
// to the package directory, where go test runs, and must lie in a testdata
// directory, such as "testdata/listing.golden".
func Equal(t testing.TB, path string, got []byte) {
	t.Helper()
	equal(t, path, got, updating())
}

// updating reports whether the test binary runs with -update.
func updating() bool {
	f := flag.Lookup(flagName)
	return f != nil && f.Value.String() == "true"
}

func equal(t testing.TB, path string, got []byte, update bool) {
	t.Helper()
	if update {
		if err := checkPath(path); err != nil {
			t.Errorf("golden: %v", err)
			return
		}
		if err := write(path, got); err != nil {
			t.Errorf("golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("golden: %s does not exist; run go test with -update in this package to create it", path)
		return
	}
	if err != nil {
		t.Errorf("golden: read %s: %v", path, err)
		return
	}
	if d := diff(want, got); d != "" {
		t.Errorf("golden: output differs from %s (run go test with -update in this package to accept it):\n%s", path, d)
	}
}

// checkPath refuses a path -update may not write: one that leaves the package
// directory or lies outside a testdata directory.
func checkPath(path string) error {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("%s: must be a local path below the package directory", path)
	}
	if !slices.Contains(strings.Split(filepath.Dir(path), string(filepath.Separator)), "testdata") {
		return fmt.Errorf("%s: must lie in a testdata directory", path)
	}
	return nil
}

func write(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile keeps the mode of a file that exists, and the umask applies
	// to a new one.
	if err := os.Chmod(path, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// diff describes how got differs from want: the first line that differs, both
// versions of it, and the size of each side. It is empty when they are equal.
func diff(want, got []byte) string {
	if bytes.Equal(want, got) {
		return ""
	}
	wantLines := bytes.SplitAfter(want, []byte("\n"))
	gotLines := bytes.SplitAfter(got, []byte("\n"))
	line := 0
	for line < len(wantLines) && line < len(gotLines) && bytes.Equal(wantLines[line], gotLines[line]) {
		line++
	}
	return fmt.Sprintf("first difference at line %d\nwant: %s\n got: %s\nwant %d bytes, %d lines; got %d bytes, %d lines",
		line+1, quote(wantLines, line), quote(gotLines, line),
		len(want), countLines(want), len(got), countLines(got))
}

// quote returns line i of lines, quoted and bounded, or a mark past the end.
func quote(lines [][]byte, i int) string {
	if i >= len(lines) || len(lines[i]) == 0 {
		return "(end of file)"
	}
	if l := lines[i]; len(l) > maxQuoted {
		return fmt.Sprintf("%q... (%d bytes)", l[:maxQuoted], len(l))
	}
	return fmt.Sprintf("%q", lines[i])
}

// countLines counts lines, a last line without a newline included.
func countLines(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}
