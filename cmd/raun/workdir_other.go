//go:build !unix

package main

import (
	"fmt"
	"os"
)

// lockWorkDir creates the work directory dir when missing; outside Unix,
// two runners on one directory are not detected.
func lockWorkDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the work directory: %w", err)
	}
	return func() {}, nil
}
