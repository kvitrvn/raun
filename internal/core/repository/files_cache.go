package repository

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// The bounds of the cache of files: a file at a commit never changes, so the
// lifetime bounds what a missing file and memory cost.
const (
	FileCacheLifetime = 5 * time.Minute
	FileCacheEntries  = 1024
)

// fileKey is what a file is at: the forge, the stable identifier of the
// repository, the commit, the path and the bound of the read.
type fileKey struct {
	forge, repository, commit, path string
	limit                           int64
}

type fileEntry struct {
	content []byte
	err     error // nil or ErrNoFile or ErrFileTooLarge
	expires time.Time
	added   uint64
}

// fileCache keeps the answers of Read, by repository and commit, for
// FileCacheLifetime: a file, its absence, or that it is too large. A failure
// of the forge is never kept. Head is never kept: a branch moves. It is safe
// for concurrent use.
type fileCache struct {
	files Files
	forge string
	now   func() time.Time

	mu      sync.Mutex
	entries map[fileKey]fileEntry
	seq     uint64
}

func newFileCache(files Files, forge string, now func() time.Time) *fileCache {
	return &fileCache{files: files, forge: forge, now: now, entries: map[fileKey]fileEntry{}}
}

func (c *fileCache) Head(ctx context.Context, repository, branch string) (string, error) {
	return c.files.Head(ctx, repository, branch)
}

func (c *fileCache) Read(ctx context.Context, repository, commit, path string, limit int64) ([]byte, error) {
	k := fileKey{forge: c.forge, repository: repository, commit: commit, path: path, limit: limit}
	c.mu.Lock()
	if e, ok := c.entries[k]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return slices.Clone(e.content), e.err
	}
	c.mu.Unlock()

	content, err := c.files.Read(ctx, repository, commit, path, limit)
	if err != nil && !errors.Is(err, ErrNoFile) && !errors.Is(err, ErrFileTooLarge) {
		return nil, err
	}
	if err == nil && int64(len(content)) > limit {
		// An adapter that overreads is refused, as a larger file.
		content, err = nil, ErrFileTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for key, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) >= FileCacheEntries {
		c.evictOldest()
	}
	c.seq++
	c.entries[k] = fileEntry{content: slices.Clone(content), err: err, expires: now.Add(FileCacheLifetime), added: c.seq}
	return slices.Clone(content), err
}

// evictOldest drops the entry added first; the caller holds the lock.
func (c *fileCache) evictOldest() {
	var oldest fileKey
	first := true
	var at uint64
	for k, e := range c.entries {
		if first || e.added < at {
			oldest, at, first = k, e.added, false
		}
	}
	delete(c.entries, oldest)
}
