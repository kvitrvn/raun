package git

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		out          string
		major, minor int
		err          bool
	}{
		{"git version 2.56.0\n", 2, 56, false},
		{"git version 2.39.3 (Apple Git-145)", 2, 39, false},
		{"git version 2.45.2.windows.1", 2, 45, false},
		{"git version 3.0", 3, 0, false},
		{"git version 2", 0, 0, true},
		{"git version two.forty", 0, 0, true},
		{"version 2.56.0", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, tt := range tests {
		major, minor, err := parseVersion(tt.out)
		if (err != nil) != tt.err || major != tt.major || minor != tt.minor {
			t.Errorf("parseVersion(%q) = %d, %d, %v", tt.out, major, minor, err)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	for out, ok := range map[string]bool{
		"git version 2.32.0": true,
		"git version 2.44.1": true,
		"git version 3.1.0":  true,
		"git version 2.31.8": false,
		"git version 1.99.0": false,
	} {
		err := checkVersion(out)
		if (err == nil) != ok {
			t.Errorf("checkVersion(%q) = %v", out, err)
		}
		if err != nil && !strings.Contains(err.Error(), "older than 2.32") {
			t.Errorf("checkVersion(%q) = %v, does not name the minimum", out, err)
		}
	}
}
