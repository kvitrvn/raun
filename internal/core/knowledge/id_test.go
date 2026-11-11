package knowledge

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
)

var idPattern = regexp.MustCompile(`^(persona|requirement)-[23456789abcdefghjkmnpqrstuvwxyz]{6}$`)

func TestNewIDShape(t *testing.T) {
	src := rand.NewChaCha8([32]byte{1})
	seen := map[string]bool{}
	for i := range 1000 {
		typ := TypePersona
		if i%2 == 0 {
			typ = TypeRequirement
		}
		id, err := NewID(typ, src)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !idPattern.MatchString(id) || !strings.HasPrefix(id, string(typ)+"-") {
			t.Fatalf("NewID(%s) = %q", typ, id)
		}
		if got, ok := idType(id); !ok || got != typ {
			t.Fatalf("idType(%q) = %q, %v", id, got, ok)
		}
		seen[id] = true
	}
	if len(seen) < 990 {
		t.Errorf("only %d distinct identifiers out of 1000", len(seen))
	}
}

func TestNewIDIsDeterministicForASeed(t *testing.T) {
	a, _ := NewID(TypePersona, rand.NewChaCha8([32]byte{7}))
	b, _ := NewID(TypePersona, rand.NewChaCha8([32]byte{7}))
	if a != b {
		t.Errorf("%q != %q for the same seed", a, b)
	}
}

func TestNewIDRejectsBiasedBytes(t *testing.T) {
	// 31 characters: bytes 248 to 255 would favour the first eight. They are
	// skipped, and the next usable bytes are used in order.
	src := append(bytes.Repeat([]byte{255}, 10), 0, 1, 2, 3, 4, 30)
	src = append(src, make([]byte, 16)...)
	id, err := NewID(TypePersona, bytes.NewReader(src))
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if want := "persona-23456z"; id != want {
		t.Errorf("NewID = %q, want %q", id, want)
	}
}

func TestNewIDErrors(t *testing.T) {
	if _, err := NewID("epic", rand.NewChaCha8([32]byte{})); err == nil {
		t.Error("unknown type accepted")
	}
	if _, err := NewID(TypePersona, nil); err == nil {
		t.Error("nil source accepted")
	}
	boom := errors.New("boom")
	if _, err := NewID(TypePersona, iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("read error: err = %v", err)
	}
	if _, err := NewID(TypePersona, bytes.NewReader([]byte{1, 2})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: err = %v", err)
	}
	unusable := bytes.NewReader(bytes.Repeat([]byte{250}, 16*maxRandomReads))
	if _, err := NewID(TypePersona, unusable); err == nil || !strings.Contains(err.Error(), "no usable byte") {
		t.Errorf("source of rejected bytes: err = %v", err)
	}
}

func TestIDType(t *testing.T) {
	tests := []struct {
		id   string
		want Type
		ok   bool
	}{
		{"requirement-8fz2mc", TypeRequirement, true},
		{"persona-k3x9q2", TypePersona, true},
		{"persona-k3x9q", "", false},
		{"persona-k3x9q22", "", false},
		{"persona-k3x9q0", "", false},
		{"persona-K3X9Q2", "", false},
		{"epic-k3x9q2", "", false},
		{"personak3x9q2", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := idType(tt.id)
		if got != tt.want || ok != tt.ok {
			t.Errorf("idType(%q) = %q, %v; want %q, %v", tt.id, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFreeLocalIDs(t *testing.T) {
	it := Item{
		Evidence:   []Evidence{{ID: "e1"}, {ID: "e3"}},
		OpenPoints: []OpenPoint{{ID: "p2"}},
	}
	if got := FreeEvidenceIDs(it, 2); strings.Join(got, ",") != "e4,e5" {
		t.Errorf("FreeEvidenceIDs = %v", got)
	}
	if got := FreeOpenPointIDs(it, 1); strings.Join(got, ",") != "p3" {
		t.Errorf("FreeOpenPointIDs = %v", got)
	}
	if got := FreeEvidenceIDs(Item{}, 1); got[0] != "e1" {
		t.Errorf("first evidence = %v", got)
	}
	if got := FreeOpenPointIDs(Item{}, 0); got != nil {
		t.Errorf("none = %v", got)
	}
	if got := FreeEvidenceIDs(it, -3); got != nil {
		t.Errorf("negative count = %v", got)
	}
}

func TestLocalID(t *testing.T) {
	for _, s := range []string{"e", "e0", "e01", "ex", "p1", "e-1", "e1a"} {
		if _, ok := localID(s, "e"); ok {
			t.Errorf("localID(%q) accepted", s)
		}
	}
	if n, ok := localID("e12", "e"); !ok || n != 12 {
		t.Errorf("localID(e12) = %d, %v", n, ok)
	}
}
