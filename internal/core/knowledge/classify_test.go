package knowledge

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/guide/intro.md", true},
		{"**/*.md", "docs/guide/intro.mdx", false},
		{"docs/**", "docs", true},
		{"docs/**", "docs/a/b/c.go", true},
		{"docs/**", "documents/a.go", false},
		{"docs/**/*.go", "docs/a.go", true},
		{"docs/**/*.go", "docs/a/b/c.go", true},
		{"docs/**/*.go", "docs/a/b/c.txt", false},
		{"**", "any/path/at/all", true},
		{"**/**", "a", true},
		{"a/**/b/**/c", "a/b/c", true},
		{"a/**/b/**/c", "a/x/b/y/z/c", true},
		{"a/**/b/**/c", "a/x/c/y/b", false},
		{"vendor/**", "src/vendor/x.go", false},
		{"**/vendor/**", "src/vendor/x.go", true},
		{"cmd/*/main.go", "cmd/raun/main.go", true},
		{"cmd/*/main.go", "cmd/raun/sub/main.go", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[a-c].go", "b.go", true},
		{"[^a-c].go", "b.go", false},
		{"[^a-c].go", "d.go", true},
		{`\*.go`, "*.go", true},
		{`\*.go`, "a.go", false},
		{"a/b", "a/b/c", false},
		{"a/b/c", "a/b", false},
		{"[", "[", false},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestMatchStaysFastOnHostilePatterns(t *testing.T) {
	// A naive recursive ** takes time exponential in the number of ** in the
	// pattern; this one must answer at once.
	pattern := strings.Repeat("**/x/", 30) + "y"
	segments := make([]string, 2000)
	for i := range segments {
		segments[i] = "x"
	}
	if Match(pattern, strings.Join(segments, "/")) {
		t.Fatal("matched a path without y")
	}
}

func TestValidatePattern(t *testing.T) {
	tests := []struct {
		pattern string
		want    string // a fragment of the error, empty when the pattern is valid
	}{
		{"**/*.md", ""},
		{"docs/**", ""},
		{"vendor/*/[a-z]?.go", ""},
		{`\[literal\].go`, ""},
		{"", "empty pattern"},
		{"/docs/**", "relative to the repository root"},
		{"docs//*.md", "empty segment"},
		{"docs/", "empty segment"},
		{"./docs", `"." segment`},
		{"docs/../src", `".." segment`},
		{"[a-", "syntax error"},
		{`docs\`, "syntax error"},
	}
	for _, tt := range tests {
		err := ValidatePattern(tt.pattern)
		if tt.want == "" {
			if err != nil {
				t.Errorf("ValidatePattern(%q) = %v, want nil", tt.pattern, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidatePattern(%q) = %v, want an error about %q", tt.pattern, err, tt.want)
		}
	}
}

func TestClassify(t *testing.T) {
	sets := SourceSets{
		Context: []string{".raun/context.md", "TEAM.md"},
		Docs:    DefaultDocs(),
		Exclude: []string{"vendor/**", "node_modules/**", ".raun/**"},
	}
	c, err := NewClassifier(sets)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}

	tests := []struct {
		path     string
		want     Source
		excluded bool
	}{
		{"internal/billing/invoice.go", SourceCode, false},
		{"Makefile", SourceCode, false},
		{"README.md", SourceDoc, false},
		{"docs/architecture.png", SourceDoc, false},
		{"internal/billing/NOTES.txt", SourceDoc, false},
		{"guide.adoc", SourceDoc, false},
		{"TEAM.md", SourceHumanContext, false},
		// A context file wins over an exclusion of its directory.
		{".raun/context.md", SourceHumanContext, false},
		{".raun/other.md", "", true},
		{"vendor/lib/README.md", "", true},
		{"vendor", "", true},
		{"src/node_modules/x.js", SourceCode, false},
	}
	for _, tt := range tests {
		got, err := c.Classify(tt.path)
		if tt.excluded {
			if !errors.Is(err, ErrExcluded) {
				t.Errorf("Classify(%q) = %q, %v, want ErrExcluded", tt.path, got, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Classify(%q) = %q, %v, want %q", tt.path, got, err, tt.want)
		}
	}
}

func TestClassifyDoesNotDependOnOrder(t *testing.T) {
	sets := SourceSets{
		Context: []string{"a.md", "b/c.md"},
		Docs:    []string{"**/*.md", "docs/**"},
		Exclude: []string{"b/**", "gen/**"},
	}
	reversed := SourceSets{
		Context: slices.Clone(sets.Context),
		Docs:    slices.Clone(sets.Docs),
		Exclude: slices.Clone(sets.Exclude),
	}
	slices.Reverse(reversed.Context)
	slices.Reverse(reversed.Docs)
	slices.Reverse(reversed.Exclude)

	a, err := NewClassifier(sets)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewClassifier(reversed)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.md", "b/c.md", "b/d.md", "docs/x.go", "gen/a.md", "main.go"} {
		sa, ea := a.Classify(p)
		sb, eb := b.Classify(p)
		if sa != sb || errors.Is(ea, ErrExcluded) != errors.Is(eb, ErrExcluded) {
			t.Errorf("%s: %q, %v and %q, %v", p, sa, ea, sb, eb)
		}
	}
}

func TestClassifyWithoutDocs(t *testing.T) {
	c, err := NewClassifier(SourceSets{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Classify("README.md"); err != nil || got != SourceCode {
		t.Errorf("Classify(README.md) = %q, %v, want code: an empty set means no documentation", got, err)
	}
}

func TestClassifyRefusesInvalidPaths(t *testing.T) {
	c, err := NewClassifier(SourceSets{Docs: DefaultDocs()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "-rf", "../x.md", "/etc/passwd", "a/../b.md"} {
		if got, err := c.Classify(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Classify(%q) = %q, %v, want ErrInvalidPath", p, got, err)
		}
	}
}

func TestNewClassifierNamesEveryFaultyEntry(t *testing.T) {
	_, err := NewClassifier(SourceSets{
		Context: []string{"ok.md", "../up.md"},
		Docs:    []string{"**/*.md", ""},
		Exclude: []string{"[a-"},
	})
	if err == nil {
		t.Fatal("NewClassifier accepted faulty sets")
	}
	for _, want := range []string{"context[1]", "docs[1]", "exclude[0]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "context[0]") || strings.Contains(err.Error(), "docs[0]") {
		t.Errorf("error %q names a valid entry", err)
	}
}

func TestClassifierKeepsItsOwnSets(t *testing.T) {
	sets := SourceSets{Docs: []string{"**/*.md"}}
	c, err := NewClassifier(sets)
	if err != nil {
		t.Fatal(err)
	}
	sets.Docs[0] = "nothing"
	if got, _ := c.Classify("README.md"); got != SourceDoc {
		t.Errorf("changing the caller's slice changed the classifier: %q", got)
	}
}

func TestDefaultDocsIsANewSlice(t *testing.T) {
	d := DefaultDocs()
	d[0] = "changed"
	if DefaultDocs()[0] == "changed" {
		t.Error("DefaultDocs shares its slice")
	}
}
