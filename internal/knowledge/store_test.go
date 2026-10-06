package knowledge

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStoreSaveListGet(t *testing.T) {
	s := NewStore(t.TempDir())

	items, err := s.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("List() on empty store = %v, %v", items, err)
	}

	// Saved out of order: List must sort by type, then ID.
	req, per := sampleRequirement(), samplePersona()
	per2 := samplePersona()
	per2.ID = "persona-22aaaa"
	for _, it := range []*Item{req, per, per2} {
		if err := s.Save(it); err != nil {
			t.Fatalf("Save(%s) error = %v", it.ID, err)
		}
	}

	items, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if want := []string{"persona-22aaaa", "persona-k3x9q2", "requirement-8fz2mc"}; !slices.Equal(ids, want) {
		t.Errorf("List() IDs = %v, want %v", ids, want)
	}

	got, err := s.Get("requirement-8fz2mc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Requirement.Statement != req.Requirement.Statement {
		t.Errorf("Get() = %+v", got.Requirement)
	}

	for _, id := range []string{"persona-zzzzzz", "nothing", ""} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestStoreSaveIsByteStable(t *testing.T) {
	s := NewStore(t.TempDir())
	it := samplePersona()
	if err := s.Save(it); err != nil {
		t.Fatal(err)
	}
	p := s.Path(it.Type, it.ID)
	first, _ := os.ReadFile(p)

	loaded, err := s.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(loaded); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Errorf("load+save changed the file:\n%s\n---\n%s", first, second)
	}

	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestStoreRefusesInvalid(t *testing.T) {
	s := NewStore(t.TempDir())
	it := samplePersona()
	it.Title = ""
	if err := s.Save(it); err == nil {
		t.Fatal("Save() accepted an invalid item")
	}
	if _, err := os.Stat(s.Path(it.Type, it.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("invalid item was written")
	}
}

func TestStoreListReportsBadFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(samplePersona()); err != nil {
		t.Fatal(err)
	}

	// A file whose name does not match its content.
	data, _ := os.ReadFile(s.Path(TypePersona, "persona-k3x9q2"))
	if err := os.WriteFile(s.Path(TypePersona, "persona-mmmmmm"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.List()
	if err == nil || !strings.Contains(err.Error(), "persona-mmmmmm.yaml") {
		t.Errorf("List() error = %v, want it to name the bad file", err)
	}
}

func TestNewID(t *testing.T) {
	s := NewStore(t.TempDir())
	seen := map[string]bool{}
	for range 200 {
		id, err := s.NewID(TypeRequirement)
		if err != nil {
			t.Fatal(err)
		}
		if !ValidID(TypeRequirement, id) || strings.ContainsAny(id[len("requirement-"):], "01ilo") {
			t.Errorf("bad id %q", id)
		}
		seen[id] = true
	}
	if len(seen) < 190 {
		t.Errorf("only %d distinct IDs out of 200", len(seen))
	}
	if _, err := s.NewID("epic"); err == nil {
		t.Error("NewID(unknown type) succeeded")
	}
}

func TestCheckReferences(t *testing.T) {
	req := sampleRequirement()
	if errs := CheckReferences([]*Item{req}); len(errs) != 1 {
		t.Errorf("missing persona: got %v, want 1 error", errs)
	}
	if errs := CheckReferences([]*Item{req, samplePersona()}); len(errs) != 0 {
		t.Errorf("complete base: got %v, want none", errs)
	}
}
