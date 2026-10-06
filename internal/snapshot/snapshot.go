// Package snapshot gives an agent a disposable copy of the analyzed commit.
//
// The copy lives outside the repository and has no Git metadata, so an
// agent can neither read the knowledge base through history nor modify the
// repository. Any change the agent makes to the copy is detected.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/kvitrvn/raun/internal/gitx"
)

// Snapshot is an exported commit in a temporary directory.
type Snapshot struct {
	Dir    string
	before map[string]string
}

// Create exports commit into a new temporary directory whose name starts
// with prefix. Paths for which skip returns true are left out.
func Create(ctx context.Context, repo *gitx.Repo, commit, prefix string, skip func(string) bool) (*Snapshot, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	s := &Snapshot{Dir: dir}
	if err := repo.Export(ctx, commit, dir, skip); err != nil {
		return nil, fmt.Errorf("create snapshot: %w", errors.Join(err, s.Remove()))
	}
	if s.before, err = fingerprint(dir); err != nil {
		return nil, fmt.Errorf("create snapshot: %w", errors.Join(err, s.Remove()))
	}
	return s, nil
}

// Changes lists files added, modified or removed since Create, as
// "added: path", "modified: path" or "removed: path", sorted by path.
func (s *Snapshot) Changes() ([]string, error) {
	after, err := fingerprint(s.Dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(after))
	for p := range after {
		paths = append(paths, p)
	}
	for p := range s.before {
		if _, ok := after[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)

	var changes []string
	for _, p := range paths {
		was, existed := s.before[p]
		now, exists := after[p]
		switch {
		case !existed:
			changes = append(changes, "added: "+p)
		case !exists:
			changes = append(changes, "removed: "+p)
		case was != now:
			changes = append(changes, "modified: "+p)
		}
	}
	return changes, nil
}

// Remove deletes the snapshot directory.
func (s *Snapshot) Remove() error {
	// An agent may have removed write permissions; restore them so the
	// directory can be deleted.
	_ = filepath.WalkDir(s.Dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(s.Dir)
}

// fingerprint maps every file and symlink under dir to a content digest.
func fingerprint(dir string) (map[string]string, error) {
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			sums[rel] = "symlink:" + target
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		sums[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fingerprint snapshot: %w", err)
	}
	return sums, nil
}
