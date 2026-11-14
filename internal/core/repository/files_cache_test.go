package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// countingFiles serves files by repository, commit and path, counts its
// reads, and fails with err when it is set.
type countingFiles struct {
	mu    sync.Mutex
	files map[string]string
	reads int
	err   error
}

func (f *countingFiles) Head(context.Context, string, string) (string, error) { return "", nil }

func (f *countingFiles) Read(_ context.Context, repo, commit, path string, limit int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	content, ok := f.files[repo+"@"+commit+":"+path]
	switch {
	case !ok:
		return nil, ErrNoFile
	case int64(len(content)) > limit:
		return nil, ErrFileTooLarge
	}
	return []byte(content), nil
}

func (f *countingFiles) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func TestFileCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	files := &countingFiles{files: map[string]string{
		"2001@c1:.raun.yaml": "api c1",
		"2001@c2:.raun.yaml": "api c2",
		"2002@c1:.raun.yaml": "web c1",
		"2001@c1:other":      "other",
		"2001@c1:big":        "0123456789",
	}}
	c := newFileCache(files, "github", func() time.Time { return now })
	read := func(repo, commit, path string, limit int64) (string, error) {
		t.Helper()
		b, err := c.Read(ctx, repo, commit, path, limit)
		return string(b), err
	}
	for _, tc := range []struct{ repo, commit, path, want string }{
		{"2001", "c1", ".raun.yaml", "api c1"},
		{"2001", "c2", ".raun.yaml", "api c2"}, // another commit
		{"2002", "c1", ".raun.yaml", "web c1"}, // another repository at the same commit
		{"2001", "c1", "other", "other"},       // another path
	} {
		for range 2 {
			if got, err := read(tc.repo, tc.commit, tc.path, 100); err != nil || got != tc.want {
				t.Errorf("Read(%s, %s, %s) = %q, %v; want %q", tc.repo, tc.commit, tc.path, got, err, tc.want)
			}
		}
	}
	if n := files.count(); n != 4 {
		t.Errorf("%d reads of the forge, want 4", n)
	}
	// An absence and a file too large are kept; a smaller bound is another
	// read.
	for range 2 {
		if _, err := read("2001", "c1", "none", 100); !errors.Is(err, ErrNoFile) {
			t.Errorf("a missing file = %v", err)
		}
		if _, err := read("2001", "c1", "big", 5); !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("a file too large = %v", err)
		}
	}
	if got, err := read("2001", "c1", "big", 10); err != nil || got != "0123456789" {
		t.Errorf("the file at its bound = %q, %v", got, err)
	}
	if n := files.count(); n != 7 {
		t.Errorf("%d reads of the forge, want 7", n)
	}
	// What a caller does to the bytes never reaches the cache.
	b, _ := c.Read(ctx, "2001", "c1", ".raun.yaml", 100)
	b[0] = 'X'
	if got, _ := read("2001", "c1", ".raun.yaml", 100); got != "api c1" {
		t.Errorf("after a change of the bytes = %q", got)
	}
	// A failure of the forge is never kept.
	files.err = fmt.Errorf("%w: down", ErrUnavailable)
	if _, err := read("2003", "c1", ".raun.yaml", 100); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a failure = %v", err)
	}
	files.err = nil
	before := files.count()
	if _, err := read("2003", "c1", ".raun.yaml", 100); !errors.Is(err, ErrNoFile) || files.count() != before+1 {
		t.Errorf("after a failure: %v, %d reads", err, files.count()-before)
	}
	// Past its lifetime, an entry is read again.
	now = now.Add(FileCacheLifetime)
	before = files.count()
	if got, _ := read("2001", "c1", ".raun.yaml", 100); got != "api c1" || files.count() != before+1 {
		t.Errorf("past the lifetime: %q, %d reads", got, files.count()-before)
	}
}

func TestFileCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	files := &countingFiles{files: map[string]string{}}
	c := newFileCache(files, "github", func() time.Time { return now })
	for i := range FileCacheEntries + 10 {
		_, _ = c.Read(ctx, "2001", fmt.Sprintf("c%d", i), ".raun.yaml", 100)
	}
	if n := len(c.entries); n != FileCacheEntries {
		t.Errorf("%d entries, want %d", n, FileCacheEntries)
	}
	// The oldest went first.
	before := files.count()
	_, _ = c.Read(ctx, "2001", "c0", ".raun.yaml", 100)
	_, _ = c.Read(ctx, "2001", fmt.Sprintf("c%d", FileCacheEntries+9), ".raun.yaml", 100)
	if files.count() != before+1 {
		t.Errorf("%d reads, want the oldest only", files.count()-before)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Read(ctx, fmt.Sprintf("r%d", i%4), "c", ".raun.yaml", 100)
		}()
	}
	wg.Wait()
}
