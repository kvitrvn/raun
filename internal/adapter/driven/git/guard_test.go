package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/gittest"
)

// hostileRemote returns a reader of a fixture whose origin remote runs a
// command through the ext transport, which the repository allows, and the file
// that command creates. The reader is opened before the remote is set.
func hostileRemote(t *testing.T) (*Reader, *gittest.Repo, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the remote command is a shell command")
	}
	repo := gittest.New(t)
	repo.WriteFile("README.md", []byte("# Fixture\n"))
	repo.Commit("first")
	r, err := Open(context.Background(), repo.Dir(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	// In an ext URL, "% " is a literal space: the remote runs sh -c 'touch <marker>'.
	repo.Git("config", "remote.origin.url", "ext::sh -c touch% "+marker)
	repo.Git("config", "protocol.ext.allow", "always")
	return r, repo, marker
}

// run starts git with the environment of the reader, less the variables named
// by drop, and reports whether the hostile command ran.
func run(t *testing.T, r *Reader, marker string, drop []string, args ...string) bool {
	t.Helper()
	cmd := r.command(context.Background(), r.gitDir, nil, args...)
	cmd.Env = slices.DeleteFunc(cmd.Env, func(v string) bool {
		name, _, _ := strings.Cut(v, "=")
		return slices.Contains(drop, name)
	})
	_ = cmd.Run() // The transport fails either way; only the marker counts.
	_, err := os.Stat(marker)
	ran := err == nil
	if ran {
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	}
	return ran
}

// TestEnvironmentDeniesEveryTransport proves the empty GIT_ALLOW_PROTOCOL on
// its own: a protocol.<name>.allow of the repository wins over protocol.allow,
// so without it the command of the remote runs.
func TestEnvironmentDeniesEveryTransport(t *testing.T) {
	r, _, marker := hostileRemote(t)

	if !run(t, r, marker, []string{"GIT_ALLOW_PROTOCOL"}, "fetch", "--quiet", "origin") {
		t.Fatal("control: without the empty allowlist, the remote command did not run, so the test proves nothing")
	}
	if run(t, r, marker, nil, "fetch", "--quiet", "origin") {
		t.Error("the remote command ran with the environment of the reader")
	}
}

// TestEnvironmentBlocksLazyFetching proves each guard against a lazy fetch on
// its own: GIT_NO_LAZY_FETCH where Git obeys it, the empty allowlist on the
// Git versions that ignore it.
func TestEnvironmentBlocksLazyFetching(t *testing.T) {
	r, repo, marker := hostileRemote(t)
	blob := repo.Git("rev-parse", "HEAD:README.md")
	if err := os.Remove(filepath.Join(repo.Dir(), ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatalf("remove the loose blob: %v", err)
	}
	repo.Git("config", "core.repositoryFormatVersion", "1")
	repo.Git("config", "extensions.partialClone", "origin")

	both := []string{"GIT_NO_LAZY_FETCH", "GIT_ALLOW_PROTOCOL"}
	if !run(t, r, marker, both, "cat-file", "-p", blob) {
		t.Fatal("control: without both guards, reading the missing blob did not fetch it, so the test proves nothing")
	}
	for _, drop := range [][]string{{"GIT_NO_LAZY_FETCH"}, {"GIT_ALLOW_PROTOCOL"}, nil} {
		if run(t, r, marker, drop, "cat-file", "-p", blob) {
			t.Errorf("reading the missing blob fetched it without %v", drop)
		}
	}
}
