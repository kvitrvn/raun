package golden

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	Equal(r, path, []byte("other\n"))
	r.failed(t, "line 1")
	assertFile(t, path, "written\n")
}

func TestUpdating(t *testing.T) {
	// The flags of the binary hold -update when the tests run with it.
	given := flag.Lookup(flagName).Value.String() == "true"
	if update, err := updating(flag.CommandLine); err != nil || update != given {
		t.Errorf("updating(the flags of the binary) = %v, %v, want %v", update, err, given)
	}

	set := flag.NewFlagSet("set", flag.ContinueOnError)
	set.Bool(flagName, false, "")
	if err := set.Parse([]string{"-" + flagName}); err != nil {
		t.Fatal(err)
	}
	if update, err := updating(set); err != nil || !update {
		t.Errorf("updating(-update given) = %v, %v, want true", update, err)
	}

	foreign := []struct {
		name   string
		define func(*flag.FlagSet)
	}{
		{"not defined", func(*flag.FlagSet) {}},
		{"a string", func(f *flag.FlagSet) { f.String(flagName, "true", "") }},
		{"a boolean that defaults to true", func(f *flag.FlagSet) { f.Bool(flagName, true, "") }},
		{"an integer", func(f *flag.FlagSet) { f.Int(flagName, 1, "") }},
	}
	for _, tt := range foreign {
		set := flag.NewFlagSet(tt.name, flag.ContinueOnError)
		tt.define(set)
		if update, err := updating(set); err == nil || update {
			t.Errorf("%s: updating = %v, %v, want an error", tt.name, update, err)
		}
	}
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

func TestCompare(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("testdata", "x.golden")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
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
	assertFile(t, path, "one\ntwo\n")

	missing := filepath.Join("testdata", "missing.golden")
	r = &recorder{TB: t}
	equal(r, missing, []byte("x"), false)
	r.failed(t, missing, "does not exist", "-update")
	if _, err := os.Stat(missing); err == nil {
		t.Error("comparing created the missing golden file")
	}

	r = &recorder{TB: t}
	equal(r, filepath.Join("testdata", "sub"), []byte("x"), false)
	r.failed(t, "does not exist")

	// Forward slashes work on every system, as in the documented example.
	r = &recorder{TB: t}
	equal(r, "testdata/x.golden", []byte("one\ntwo\n"), false)
	r.passed(t)
}

func TestCompareReportsAReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not deny reading on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join("testdata", "locked.golden")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	r := &recorder{TB: t}
	equal(r, path, []byte("x"), false)
	r.failed(t, "read "+path, "permission denied")
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

	// No temporary file is left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "x.golden" {
		t.Errorf("the directory holds %v, want x.golden only", entries)
	}

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

func TestRefusedPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("testdata", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want string
	}{
		{"x.golden", "testdata directory"},
		{filepath.Join("data", "x.golden"), "testdata directory"},
		{"testdata", "testdata directory"},
		{filepath.Join("..", "testdata", "x.golden"), "clean local path"},
		{"testdata/../x.golden", "clean local path"},
		{"testdata//x.golden", "clean local path"},
		{"./testdata/x.golden", "clean local path"},
		{filepath.Join(t.TempDir(), "testdata", "x.golden"), "clean local path"},
		{filepath.Join("testdata", "dir"), "not a regular file"},
	}
	for _, update := range []bool{false, true} {
		for _, tt := range tests {
			r := &recorder{TB: t}
			equal(r, tt.path, []byte("x"), update)
			r.failed(t, tt.path, tt.want)
		}
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("refused paths wrote into testdata: %v", entries)
	}
	if entries, _ := os.ReadDir("."); len(entries) != 1 {
		t.Errorf("refused paths wrote %v", entries)
	}
}

func TestRefusesSymbolicLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links needs a privilege on Windows")
	}
	t.Chdir(t.TempDir())
	outside := t.TempDir()
	target := filepath.Join(outside, "source.go")
	if err := os.WriteFile(target, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("pkg", "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join("pkg", "testdata", "x.golden"): target,  // the golden file
		filepath.Join("pkg", "testdata", "sub"):      outside, // a directory below testdata
		filepath.Join("other", "testdata"):           outside, // testdata itself
	}
	for link, to := range links {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}

	paths := []string{
		filepath.Join("pkg", "testdata", "x.golden"),
		filepath.Join("pkg", "testdata", "sub", "source.go"),
		filepath.Join("pkg", "testdata", "sub", "new.golden"),
		filepath.Join("other", "testdata", "source.go"),
		filepath.Join("other", "testdata", "new.golden"),
	}
	for _, update := range []bool{false, true} {
		for _, p := range paths {
			r := &recorder{TB: t}
			equal(r, p, []byte("overwritten\n"), update)
			r.failed(t, p, "symbolic link")
		}
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package outside\n" {
		t.Errorf("the file the link points to was changed: %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the mode of the file the link points to changed to %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("updates wrote through a link: %v", entries)
	}
}

func TestWriteReportsAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	// A regular file where a directory should be fails on every system,
	// whatever the permissions of the user.
	if err := os.WriteFile("testdata", []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "x.golden")
	if err := write(path, []byte("x")); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("write = %v, want an error naming %s", err, path)
	}

	r := &recorder{TB: t}
	equal(r, path, []byte("x"), true)
	r.failed(t, path)
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
