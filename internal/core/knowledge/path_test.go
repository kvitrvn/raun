package knowledge

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path string
		want string // a fragment of the error, empty when the path is valid
	}{
		{"README.md", ""},
		{"internal/billing/invoice.go", ""},
		{"dir/-x", ""},
		{"a b/c.go", ""},
		{".github/workflows/ci.yaml", ""},
		{"..hidden", ""},
		{"", "required"},
		{"/etc/passwd", "relative to the repository root"},
		{"-rf", "must not start with a dash"},
		{"--output=x", "must not start with a dash"},
		{"-", "must not start with a dash"},
		{`a\b`, "forward slashes only"},
		{"a\x00b", "forward slashes only"},
		{"a/../b", "clean path"},
		{"a/./b", "clean path"},
		{"./a", "clean path"},
		{"a//b", "clean path"},
		{"a/", "clean path"},
		{".", "clean path"},
		{"..", "stay inside the repository"},
		{"../secrets", "stay inside the repository"},
		{"../../etc/passwd", "stay inside the repository"},
	}
	for _, tt := range tests {
		err := ValidatePath(tt.path)
		if tt.want == "" {
			if err != nil {
				t.Errorf("ValidatePath(%q) = %v, want nil", tt.path, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ValidatePath(%q) = nil, want an error about %q", tt.path, tt.want)
			continue
		}
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("ValidatePath(%q) = %v, does not wrap ErrInvalidPath", tt.path, err)
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidatePath(%q) = %v, want %q in it", tt.path, err, tt.want)
		}
	}
}
