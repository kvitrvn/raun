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
// to the package directory, where go test runs, uses forward slashes or the
// separator of the system, and must lie in a testdata directory, such as
// "testdata/listing.golden".
func Equal(t testing.TB, path string, got []byte) {
	t.Helper()
	equalWith(t, flag.CommandLine, path, got)
}

// equalWith is Equal with the flags it reads -update from. A flag it cannot
// trust fails the test before anything is compared or written.
func equalWith(t testing.TB, flags *flag.FlagSet, path string, got []byte) {
	t.Helper()
	update, err := updating(flags)
	if err != nil {
		t.Errorf("golden: %v", err)
		return
	}
	equal(t, path, got, update)
}

// updating reports whether -update is set in flags. A flag of that name that
// another package defined is accepted only if it is a boolean that defaults
// to false: anything else could rewrite every golden file on a plain run, or
// never.
func updating(flags *flag.FlagSet) (bool, error) {
	f := flags.Lookup(flagName)
	if f == nil {
		return false, fmt.Errorf("the -%s flag is not defined", flagName)
	}
	getter, ok := f.Value.(flag.Getter)
	if !ok {
		return false, fmt.Errorf("the -%s flag of this test binary is not a boolean flag", flagName)
	}
	value, ok := getter.Get().(bool)
	if !ok || f.DefValue != "false" {
		return false, fmt.Errorf("the -%s flag of this test binary is not a boolean flag that defaults to false", flagName)
	}
	return value, nil
}

func equal(t testing.TB, path string, got []byte, update bool) {
	t.Helper()
	path = filepath.FromSlash(path)
	if err := checkPath(path); err != nil {
		t.Errorf("golden: %v", err)
		return
	}
	if update {
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

// checkPath refuses a golden path that -update could not write where it
// says: one that is not clean, leaves the package directory, lies outside a
// testdata directory, goes through a symbolic link, or names something other
// than a regular file. Comparing applies the same rule, so a file that can be
// read can always be refreshed.
func checkPath(path string) error {
	if !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s: must be a clean local path below the package directory", path)
	}
	parts := strings.Split(path, string(filepath.Separator))
	if !slices.Contains(parts[:len(parts)-1], "testdata") {
		return fmt.Errorf("%s: must lie in a testdata directory", path)
	}
	// A link would send the write elsewhere, so none is followed. Checking
	// stops at the first component that does not exist: the write creates
	// it and everything below.
	for i := range parts {
		prefix := filepath.Join(parts[:i+1]...)
		info, err := os.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %s is a symbolic link", path, prefix)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", path)
		}
	}
	return nil
}

// write replaces the file at path through a temporary file in the same
// directory, so an interrupted update never leaves a partial golden file.
func write(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".golden-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // Fails once renamed, as it should.
	_, err = tmp.Write(content)
	// The mode is set here: the umask does not apply, and the mode of a file
	// being replaced is not kept.
	err = errors.Join(err, tmp.Chmod(0o644), tmp.Close())
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
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
