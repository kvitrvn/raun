package golden

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a testing.TB that keeps the failures it is given instead of
// failing the test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) failed(t *testing.T, want ...string) {
	t.Helper()
	if len(r.errors) != 1 {
		t.Fatalf("got %d failures, want 1: %q", len(r.errors), r.errors)
	}
	for _, w := range want {
		if !strings.Contains(r.errors[0], w) {
			t.Errorf("failure %q does not mention %q", r.errors[0], w)
		}
	}
}

func (r *recorder) passed(t *testing.T) {
	t.Helper()
	if len(r.errors) != 0 {
		t.Fatalf("unexpected failures: %q", r.errors)
	}
}

const example = "kind path\nfile README.md\ndirectory docs\n"

func TestEqualAgainstACommittedFile(t *testing.T) {
	Equal(t, "testdata/example.golden", []byte(example))
}

func TestUpdateFlag(t *testing.T) {
	f := flag.Lookup(flagName)
	if f == nil {
		t.Fatal("-update is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("-update defaults to %q", f.DefValue)
	}
}

func TestEqualHonoursTheFlag(t *testing.T) {
	// The tests of this package do not run in parallel, so setting the flag
	// of the binary for one of them is safe.
	initial := flag.Lookup(flagName).Value.String()
	t.Cleanup(func() {
		if err := flag.Set(flagName, initial); err != nil {
			t.Error(err)
		}
	})
	t.Chdir(t.TempDir())
	path := filepath.Join("testdata", "flag.golden")

	if err := flag.Set(flagName, "true"); err != nil {
		t.Fatal(err)
	}
	Equal(t, path, []byte("written\n"))
	assertFile(t, path, "written\n")

	if err := flag.Set(flagName, "false"); err != nil {
		t.Fatal(err)
	}
	r := &recorder{TB: t}
	equal(r, path, []byte("other\n"), updating())
	r.failed(t, "line 1")
	assertFile(t, path, "written\n")
}

func TestCompare(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testdata", "x.golden")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &recorder{TB: t}
	equal(r, path, []byte("one\ntwo\n"), false)
	r.passed(t)

	r = &recorder{TB: t}
	equal(r, path, []byte("one\nTWO\n"), false)
	r.failed(t, path, "line 2", `"two\n"`, `"TWO\n"`, "-update")
	if got, _ := os.ReadFile(path); string(got) != "one\ntwo\n" {
		t.Errorf("a mismatch changed the golden file: %q", got)
	}

	missing := filepath.Join(dir, "testdata", "missing.golden")
	r = &recorder{TB: t}
	equal(r, missing, []byte("x"), false)
	r.failed(t, missing, "does not exist", "-update")
	if _, err := os.Stat(missing); err == nil {
		t.Error("comparing created the missing golden file")
	}

	// A directory where the file should be cannot be read.
	r = &recorder{TB: t}
	equal(r, filepath.Join(dir, "testdata"), []byte("x"), false)
	r.failed(t, "read")
}

func TestUpdate(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("testdata", "nested", "x.golden")

	r := &recorder{TB: t}
	equal(r, path, []byte("first\n"), true)
	r.passed(t)
	assertFile(t, path, "first\n")

	// An existing file with another mode and content is replaced exactly.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	r = &recorder{TB: t}
	equal(r, path, []byte("second, longer\n"), true)
	r.passed(t)
	assertFile(t, path, "second, longer\n")

	// Comparing after an update passes.
	r = &recorder{TB: t}
	equal(r, path, []byte("second, longer\n"), false)
	r.passed(t)
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("%s has mode %v, want 0644", path, info.Mode().Perm())
	}
}

func TestUpdateRefusesPathsOutsideTestdata(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, path := range []string{
		"x.golden",
		"data/x.golden",
		"testdata",
		"../testdata/x.golden",
		"testdata/../x.golden",
		filepath.Join(t.TempDir(), "testdata", "x.golden"),
	} {
		r := &recorder{TB: t}
		equal(r, path, []byte("x"), true)
		r.failed(t, path)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s was written", path)
		}
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Errorf("refused updates wrote %v", entries)
	}
}

func TestUpdateReportsAWriteError(t *testing.T) {
	t.Chdir(t.TempDir())
	// A regular file where a directory should be fails on every system,
	// whatever the permissions of the user.
	if err := os.WriteFile("testdata", []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recorder{TB: t}
	equal(r, filepath.Join("testdata", "x.golden"), []byte("x"), true)
	r.failed(t, "write", "x.golden")
}

func TestDiff(t *testing.T) {
	long := strings.Repeat("a", 300)
	tests := []struct {
		name      string
		want, got string
		parts     []string // fragments of the result, none when equal
	}{
		{"equal", "a\nb\n", "a\nb\n", nil},
		{"both empty", "", "", nil},
		{"one line differs", "a\nb\nc\n", "a\nB\nc\n", []string{"line 2", `want: "b\n"`, `got: "B\n"`, "want 6 bytes, 3 lines; got 6 bytes, 3 lines"}},
		{"a line more", "a\n", "a\nb\n", []string{"line 2", "want: (end of file)", `got: "b\n"`, "want 2 bytes, 1 lines; got 4 bytes, 2 lines"}},
		{"a line less", "a\nb\n", "a\n", []string{"line 2", `want: "b\n"`, "got: (end of file)"}},
		{"missing final newline", "a\nb\n", "a\nb", []string{"line 2", `want: "b\n"`, `got: "b"`, "got 3 bytes, 2 lines"}},
		{"line endings", "a\nb\n", "a\r\nb\r\n", []string{"line 1", `want: "a\n"`, `got: "a\r\n"`}},
		{"empty against content", "", "x\n", []string{"line 1", "want: (end of file)", `got: "x\n"`, "want 0 bytes, 0 lines"}},
		{"binary", "\x00\x01\n", "\x00\x02\n", []string{"line 1", `"\x00\x01\n"`, `"\x00\x02\n"`}},
		{"long line", long + "\n", long + "b\n", []string{"line 1", "... (301 bytes)", "... (302 bytes)"}},
	}
	for _, tt := range tests {
		got := diff([]byte(tt.want), []byte(tt.got))
		if len(tt.parts) == 0 {
			if got != "" {
				t.Errorf("%s: diff = %q, want none", tt.name, got)
			}
			continue
		}
		for _, part := range tt.parts {
			if !strings.Contains(got, part) {
				t.Errorf("%s: diff\n%s\ndoes not contain %q", tt.name, got, part)
			}
		}
		if strings.Contains(got, long) {
			t.Errorf("%s: diff quotes a long line whole", tt.name)
		}
	}
}

func TestDiffIsBoundedWhateverTheInput(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 1<<20)
	if d := diff(big, append(big, 'y')); len(d) > 4*maxQuoted+200 {
		t.Errorf("diff of a megabyte is %d bytes long", len(d))
	}
}
