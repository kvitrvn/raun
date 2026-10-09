package knowledge

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrNotFound reports a missing knowledge item.
var ErrNotFound = errors.New("knowledge item not found")

// ErrConflict reports a busy item or a revision that changed since it was read.
var ErrConflict = errors.New("knowledge update conflict")

// Revision fingerprints the canonical content without adding a persisted field.
func Revision(it *Item) (string, error) {
	data, err := Encode(it)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// Store reads and writes knowledge items as `<dir>/<type>/<id>.yaml`.
type Store struct {
	dir string
}

// NewStore returns a store rooted at dir (usually `.raun/knowledge`).
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Path returns the file path of the item with the given type and ID.
func (s *Store) Path(t Type, id string) string {
	return filepath.Join(s.dir, string(t), id+".yaml")
}

// List loads every item, sorted by type (in Types order) then ID.
// Any invalid file makes List fail, naming the file.
func (s *Store) List() ([]*Item, error) {
	var items []*Item
	for _, t := range Types {
		entries, err := os.ReadDir(filepath.Join(s.dir, string(t)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", t, err)
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".yaml")
			if e.IsDir() || !ok {
				continue
			}
			it, err := s.load(t, id)
			if err != nil {
				return nil, err
			}
			items = append(items, it)
		}
	}
	return items, nil
}

// Get loads one item by ID.
func (s *Store) Get(id string) (*Item, error) {
	t, ok := TypeOfID(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a knowledge ID", ErrNotFound, id)
	}
	return s.load(t, id)
}

func (s *Store) load(t Type, id string) (*Item, error) {
	p := s.Path(t, id)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	it, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if it.Type != t || it.ID != id {
		return nil, fmt.Errorf("%s: file holds %s %q, expected %s %q", p, it.Type, it.ID, t, id)
	}
	return it, nil
}

// Save validates and atomically writes an item under the shared item lock.
// Read-modify-write callers must use Update to also check their read revision.
func (s *Store) Save(it *Item) (err error) {
	if err := it.Validate(); err != nil {
		return fmt.Errorf("save %s: %w", it.ID, err)
	}
	p := s.Path(it.Type, it.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("save %s: %w", p, err)
	}
	unlock, err := lockItem(p)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	return s.save(it)
}

// Update locks and reloads an item, checks expectedRevision, then applies mutate
// and saves atomically. Conflicts never retry or invoke mutate. The callback must
// not change the item's identity or call Save/Update for this item.
func (s *Store) Update(id, expectedRevision string, mutate func(*Item) error) (err error) {
	t, ok := TypeOfID(id)
	if !ok {
		return fmt.Errorf("%w: %q is not a knowledge ID", ErrNotFound, id)
	}
	p := s.Path(t, id)
	unlock, err := lockItem(p)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	it, err := s.load(t, id)
	if err != nil {
		return err
	}
	revision, err := Revision(it)
	if err != nil {
		return err
	}
	if revision != expectedRevision {
		return fmt.Errorf("%w: %s changed; reload and review the item before deciding again", ErrConflict, p)
	}
	if err := mutate(it); err != nil {
		return err
	}
	if it.ID != id || it.Type != t {
		return fmt.Errorf("update %s: item identity cannot change", p)
	}
	return s.save(it)
}

// A fixed adjacent name makes the exclusive create coordinate separate processes.
// Never remove an existing lock: its owner may still be writing.
func lockItem(path string) (func() error, error) {
	p := path + ".lock"
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: lock %s is busy; reload and review before retrying (see docs/web.md for abandoned locks)", ErrConflict, p)
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close lock %s: %w", p, errors.Join(err, os.Remove(p)))
	}
	return func() error {
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("remove lock %s: %w", p, err)
		}
		return nil
	}, nil
}

func (s *Store) save(it *Item) error {
	if err := it.Validate(); err != nil {
		return fmt.Errorf("save %s: %w", it.ID, err)
	}
	data, err := Encode(it)
	if err != nil {
		return err
	}

	p := s.Path(it.Type, it.ID)
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+it.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("save %s: %w", p, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once renamed

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("save %s: %w", p, errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save %s: %w", p, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("save %s: %w", p, err)
	}
	return nil
}

// NewID returns an unused random ID for type t.
func (s *Store) NewID(t Type) (string, error) {
	if !slices.Contains(Types, t) {
		return "", fmt.Errorf("unknown knowledge type %q", t)
	}
	for range 100 {
		id := newID(t)
		_, err := os.Stat(s.Path(t, id))
		if errors.Is(err, fs.ErrNotExist) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("new id: %w", err)
		}
	}
	return "", fmt.Errorf("new id: no free %s ID after 100 attempts", t)
}

// CheckReferences reports links to items that do not exist, such as a
// requirement naming an unknown persona.
func CheckReferences(items []*Item) []error {
	known := map[string]bool{}
	for _, it := range items {
		known[it.ID] = true
	}
	var errs []error
	for _, it := range items {
		if it.Requirement == nil {
			continue
		}
		for _, ref := range it.Requirement.Personas {
			if !known[ref] {
				errs = append(errs, fmt.Errorf("%s: requirement.personas references unknown persona %q", it.ID, ref))
			}
		}
	}
	return errs
}
