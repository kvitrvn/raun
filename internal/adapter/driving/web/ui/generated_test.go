package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests keep the committed generated files honest without the
// network or a frontend tool: templ is the tool pinned in go.mod, and the
// stylesheet is checked through the hash of its inputs it records.

// htmxSHA256 is the digest of the unchanged dist/htmx.min.js of the pinned
// HTMX release, as docs/web.md gives it.
const htmxSHA256 = "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de"

// moduleRoot returns the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package")
		}
		dir = parent
	}
}

func templFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.templ")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if len(files) == 0 {
		t.Fatal("no template")
	}
	return files
}

func TestGeneratedTemplatesAreCurrent(t *testing.T) {
	dir := t.TempDir()
	for _, f := range templFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "tool", "templ", "generate", "-path", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("templ generate: %v\n%s", err, out)
	}

	want, err := filepath.Glob(filepath.Join(dir, "*_templ.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.Glob("*_templ.go")
	if err != nil {
		t.Fatal(err)
	}
	var wantNames []string
	for _, w := range want {
		wantNames = append(wantNames, filepath.Base(w))
	}
	slices.Sort(wantNames)
	slices.Sort(got)
	if !slices.Equal(wantNames, got) {
		t.Fatalf("generated files %v, want %v: run scripts/generate-web.sh", got, wantNames)
	}
	for _, name := range wantNames {
		fresh, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(fresh, committed) {
			t.Errorf("%s is stale: run scripts/generate-web.sh", name)
		}
	}
}

// tailwindVersion reads the pinned Tailwind version from the generation
// script, so that the script stays the one place that names it.
func tailwindVersion(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(moduleRoot(t), "scripts", "generate-web.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^tailwind_version=(v[0-9.]+)$`).FindSubmatch(script)
	if m == nil {
		t.Fatal("scripts/generate-web.sh names no tailwind_version")
	}
	return string(m[1])
}

// stylesheetInputs is the hash scripts/generate-web.sh records in the first
// line of assets/app.css: the Tailwind version, then the SHA-256 and the
// path of each input, relative to the web directory, in byte order.
func stylesheetInputs(t *testing.T, version string) string {
	t.Helper()
	paths := []string{"styles.css"}
	paths = append(paths, templFiles(t)...)
	for i, p := range paths {
		paths[i] = "ui/" + p
	}
	slices.Sort(paths)
	var list strings.Builder
	fmt.Fprintf(&list, "tailwindcss %s\n", version)
	for _, p := range paths {
		body, err := os.ReadFile(strings.TrimPrefix(p, "ui/"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		fmt.Fprintf(&list, "%s %s\n", hex.EncodeToString(sum[:]), p)
	}
	sum := sha256.Sum256([]byte(list.String()))
	return hex.EncodeToString(sum[:])
}

func TestStylesheetIsCurrent(t *testing.T) {
	version := tailwindVersion(t)
	css, err := os.ReadFile(filepath.Join("..", "assets", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	first, rest, _ := strings.Cut(string(css), "\n")
	if want := "/* raun-inputs: " + stylesheetInputs(t, version) + " */"; first != want {
		t.Fatalf("assets/app.css records %q, want %q: run scripts/generate-web.sh", first, want)
	}
	if !strings.HasPrefix(rest, "/*! tailwindcss "+version+" ") {
		t.Errorf("assets/app.css was not produced by Tailwind %s", version)
	}
}

func TestHTMXIsPinned(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "assets", "htmx.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != htmxSHA256 {
		t.Fatalf("assets/htmx.min.js has SHA-256 %s, want %s", got, htmxSHA256)
	}
	doc, err := os.ReadFile(filepath.Join(moduleRoot(t), "docs", "web.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, []byte(htmxSHA256)) {
		t.Error("docs/web.md does not give the SHA-256 of htmx.min.js")
	}
}

// TestNoRawHTML refuses templ.Raw in every Go file of the module, generated
// ones included, whatever name templ is imported under: repository content
// must never reach a page unescaped. A dot import of templ is refused, since
// Raw would then need no qualifier.
func TestNoRawHTML(t *testing.T) {
	root := moduleRoot(t)
	var problems []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".tmp", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		problems = append(problems, rawUses(t, root, p)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// rawUses reports each use of templ.Raw in the Go file path.
func rawUses(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	rel, _ := filepath.Rel(root, path)
	var names []string
	var problems []string
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != "github.com/a-h/templ" {
			continue
		}
		switch {
		case imp.Name == nil:
			names = append(names, "templ")
		case imp.Name.Name == ".":
			problems = append(problems, fmt.Sprintf("%s: dot import of templ", rel))
		case imp.Name.Name != "_":
			names = append(names, imp.Name.Name)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Raw" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, x.Name) {
			problems = append(problems, fmt.Sprintf("%s:%d: templ.Raw renders unescaped HTML", rel, fset.Position(sel.Pos()).Line))
		}
		return true
	})
	return problems
}

func TestRawUsesAreFound(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]bool{
		"plain.go":   true,
		"alias.go":   true,
		"dot.go":     true,
		"clean.go":   false,
		"shadow.go":  false,
		"comment.go": false,
	}
	sources := map[string]string{
		"plain.go":   "package x\nimport \"github.com/a-h/templ\"\nvar _ = templ.Raw(\"<b>\")\n",
		"alias.go":   "package x\nimport t \"github.com/a-h/templ\"\nvar _ = t.Raw(\"<b>\")\n",
		"dot.go":     "package x\nimport . \"github.com/a-h/templ\"\nvar _ = URL(\"/\")\n",
		"clean.go":   "package x\nimport \"github.com/a-h/templ\"\nvar _ = templ.URL(\"/\")\n",
		"shadow.go":  "package x\ntype r struct{}\nfunc (r) Raw() {}\nvar templ r\nvar _ = templ.Raw\n",
		"comment.go": "package x\n// templ.Raw is refused.\n",
	}
	for name, want := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(sources[name]), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := len(rawUses(t, dir, path)) > 0; got != want {
			t.Errorf("%s: found = %v, want %v", name, got, want)
		}
	}
}
