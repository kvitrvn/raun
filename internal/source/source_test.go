package source

import (
	"errors"
	"testing"

	"github.com/kvitrvn/raun/internal/knowledge"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"*.md", "README.md", true},
		{"*.md", "docs/a.md", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/deep/a.md", true},
		{"**/*.md", "docs/a.go", false},
		{"docs/**", "docs/a.go", true},
		{"docs/**", "docs/x/y/z.png", true},
		{"docs/**", "src/docs/a.go", false},
		{"vendor/**", "vendor/github.com/x/y.go", true},
		{"**/testdata/**", "internal/knowledge/testdata/a.yaml", true},
		{"src/*/main.go", "src/cmd/main.go", true},
		{"src/*/main.go", "src/a/b/main.go", false},
		{".raun/**", ".raun/context.md", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestValidPattern(t *testing.T) {
	for _, p := range []string{"**/*.md", "docs/**", "a/[bc]/d"} {
		if err := ValidPattern(p); err != nil {
			t.Errorf("ValidPattern(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "a/[bc", "[x"} {
		if err := ValidPattern(p); err == nil {
			t.Errorf("ValidPattern(%q) = nil, want error", p)
		}
	}
}

func TestClassify(t *testing.T) {
	c := NewClassifier([]string{".raun/context.md"}, DefaultDocs, []string{".raun/**", "vendor/**"})
	tests := []struct {
		path    string
		want    knowledge.SourceKind
		wantErr bool
	}{
		{".raun/context.md", knowledge.SourceHumanContext, false},
		{".raun/config.yaml", "", true},
		{"vendor/lib/README.md", "", true},
		{"README.md", knowledge.SourceDoc, false},
		{"docs/api/schema.json", knowledge.SourceDoc, false},
		{"internal/billing/notes.txt", knowledge.SourceDoc, false},
		{"internal/billing/invoice.go", knowledge.SourceCode, false},
	}
	for _, tt := range tests {
		got, err := c.Classify(tt.path)
		if tt.wantErr {
			if !errors.Is(err, ErrExcluded) {
				t.Errorf("Classify(%q) error = %v, want ErrExcluded", tt.path, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Classify(%q) = %q, %v; want %q", tt.path, got, err, tt.want)
		}
	}
}
