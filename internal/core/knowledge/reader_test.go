package knowledge

import (
	"bytes"
	"testing"
)

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"empty", nil, false},
		{"text", []byte("package main\n"), false},
		{"utf-8", []byte("répertoire ✓\n"), false},
		{"nul byte", []byte("PNG\x00\x01"), true},
		{"nul at the last byte looked at", append(bytes.Repeat([]byte("a"), binaryPrefix-1), 0), true},
		{"nul after the bytes looked at", append(bytes.Repeat([]byte("a"), binaryPrefix), 0), false},
	}
	for _, tt := range tests {
		if got := IsBinary(tt.content); got != tt.want {
			t.Errorf("%s: IsBinary = %v, want %v", tt.name, got, tt.want)
		}
	}
}
