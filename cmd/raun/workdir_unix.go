//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockWorkDir takes the lock of the work directory dir, created when
// missing, for as long as the runner runs, and returns its release. The
// directory must be the user's own, not a link; it is closed to other
// users. A
// second runner on the same directory is refused, since its start would
// remove the runs of the first. The lock goes with the process, so a killed
// runner leaves none behind.
func lockWorkDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the work directory: %w", err)
	}
	// The default is a known path in a shared temporary directory: one
	// that another user made, or a link to one, would receive the code of
	// the repositories.
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("read the work directory: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return nil, fmt.Errorf("%s is not a directory, or is a link", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return nil, fmt.Errorf("%s belongs to another user", dir)
	case info.Mode().Perm()&0o077 != 0:
		// The runner's own directory: closed to other users, since the
		// code of the repositories goes there.
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("close the work directory to other users: %w", err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, workDirLock), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the lock of the work directory: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s is the work directory of another runner that runs: give each runner its own", dir)
		}
		return nil, fmt.Errorf("lock the work directory: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
