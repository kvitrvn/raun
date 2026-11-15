package git

import (
	"context"
	"encoding/base64"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/knowledge"
	"github.com/kvitrvn/raun/internal/gittest"
)

const (
	cloneUser     = "x-access-token"
	clonePassword = job.Secret("ghs_clone-secret-sentinel")
)

// basic is the header a fetch with the clone credential sends.
var basic = "Basic " + base64.StdEncoding.EncodeToString([]byte(cloneUser+":"+clonePassword.Reveal()))

// served is a fixture with two commits, and a hook that would leave a mark,
// served behind the clone credential.
func served(t *testing.T) (*gittest.Repo, *gittest.Server, string, string) {
	t.Helper()
	repo := gittest.New(t)
	repo.WriteFile("README.md", []byte("first\n"))
	first := repo.Commit("first")
	repo.WriteFile("README.md", []byte("second\n"))
	repo.WriteExecutable(".git/hooks/post-checkout", []byte("#!/bin/sh\ntouch /tmp/raun-hook-ran\n"))
	second := repo.Commit("second")
	srv := repo.Serve(gittest.Unless(basic))
	return repo, srv, first, second
}

func request(url, commit string) FetchRequest {
	return FetchRequest{URL: url, Commit: commit, Username: cloneUser, Password: clonePassword}
}

func TestFetchOneCommit(t *testing.T) {
	ctx := context.Background()
	_, srv, first, second := served(t)
	dir := t.TempDir()
	c, err := Fetch(ctx, dir, request(srv.URL, first), Options{})
	if err != nil {
		t.Fatalf("Fetch = %v", err)
	}
	if c.Commit() != first {
		t.Errorf("Commit = %s", c.Commit())
	}
	var got string
	if err := c.ReadFiles(ctx, first, []string{"README.md"}, func(f knowledge.File) error {
		got = string(f.Content)
		return nil
	}); err != nil || got != "first\n" {
		t.Errorf("README.md at the commit = %q, %v", got, err)
	}
	// One commit only: its parent, and the later one, are not there.
	if _, err := c.ResolveRevision(ctx, second); err == nil {
		t.Error("the later commit was fetched too")
	}
	for _, a := range srv.Authorizations() {
		if a != basic {
			t.Errorf("a request went without the credential: %q", a)
		}
	}

	// Nothing of the credential under the directory, its configuration
	// included, and no hook ran.
	encoded := base64.StdEncoding.EncodeToString([]byte(cloneUser + ":" + clonePassword.Reveal()))
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), clonePassword.Reveal()) || strings.Contains(string(b), encoded) || strings.Contains(string(b), "extraHeader") {
			t.Errorf("%s holds the credential", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "hooks")); len(entries) != 0 {
		t.Errorf("hooks in the checkout: %v", entries)
	}
	gitPath, _ := exec.LookPath("git")
	list := exec.Command(gitPath, "config", "--list", "--local")
	list.Dir, list.Env = dir, []string{"GIT_DIR=" + dir, "GIT_CONFIG_NOSYSTEM=1", "HOME=" + os.DevNull}
	out, err := list.Output()
	if err != nil || strings.Contains(string(out), "extraheader") || strings.Contains(string(out), "url") {
		t.Errorf("the configuration of the checkout: %s, %v", out, err)
	}
}

func TestFetchRefusals(t *testing.T) {
	ctx := context.Background()
	_, srv, first, _ := served(t)
	for name, req := range map[string]FetchRequest{
		"no credential":       {URL: srv.URL, Commit: first},
		"another password":    {URL: srv.URL, Commit: first, Username: cloneUser, Password: "other"},
		"an unknown commit":   request(srv.URL, strings.Repeat("d", 40)),
		"an abbreviated hash": request(srv.URL, first[:12]),
		"a branch":            request(srv.URL, "main"),
		"an option":           request(srv.URL, "--upload-pack=touch"),
		"http elsewhere":      request("http://example.com/acme/api.git", first),
		"ssh":                 request("ssh://git@example.com/acme/api.git", first),
		"a file":              request("file:///tmp/x.git", first),
		"a user in the URL":   request(strings.Replace(srv.URL, "http://", "http://x:y@", 1), first),
		"a query":             request(srv.URL+"?x=1", first),
		"an ext transport":    request("ext::sh -c touch% /tmp/x", first),
		"no address":          request("", first),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := Fetch(ctx, dir, req, Options{})
			if err == nil {
				t.Fatal("Fetch succeeded")
			}
			if strings.Contains(err.Error(), clonePassword.Reveal()) || strings.Contains(err.Error(), "Basic") {
				t.Errorf("the error holds the credential: %v", err)
			}
		})
	}
	// A directory that is not empty is refused.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(ctx, dir, request(srv.URL, first), Options{}); err == nil {
		t.Error("a fetch into a directory that is not empty succeeded")
	}
}

func TestFetchFollowsNoRedirect(t *testing.T) {
	ctx := context.Background()
	_, srv, first, _ := served(t)
	// Another server that would serve the commit: a redirect to it is not
	// followed, so the credential never leaves for another address.
	repo := gittest.New(t)
	repo.WriteFile("README.md", []byte("first\n"))
	repo.Commit("first")
	target := repo.Serve(nil)
	srv.SetGate(func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, target.URL+"/info/refs?"+r.URL.RawQuery, http.StatusFound)
		return true
	})
	if _, err := Fetch(ctx, t.TempDir(), request(srv.URL, first), Options{}); err == nil {
		t.Error("a fetch followed a redirect")
	}
	if got := target.Authorizations(); len(got) != 0 {
		t.Errorf("the credential reached another server: %d requests", len(got))
	}
}

func TestFetchSHA256(t *testing.T) {
	repo := gittest.NewSHA256(t)
	repo.WriteFile("a.txt", []byte("x"))
	commit := repo.Commit("a")
	srv := repo.Serve(nil)
	c, err := Fetch(context.Background(), t.TempDir(), request(srv.URL, commit), Options{})
	if err != nil || c.Commit() != commit {
		t.Fatalf("Fetch of a SHA-256 commit = %v", err)
	}
}

func TestFetcherIsACheckout(t *testing.T) {
	_, srv, first, _ := served(t)
	s, err := Fetcher{}.Fetch(context.Background(), t.TempDir(), job.Work{
		Commit: first, Clone: job.Credential{URL: srv.URL, Username: cloneUser, Password: clonePassword},
	})
	if err != nil || s.Commit() != first {
		t.Fatalf("Fetcher.Fetch = %v", err)
	}
	if _, ok := s.(knowledge.CommitReader); !ok {
		t.Error("the snapshot is not a commit reader")
	}
}

func TestCheckVersionOfTheBinary(t *testing.T) {
	if v, err := CheckVersion(context.Background(), ""); err != nil || !strings.HasPrefix(v, "git version") {
		t.Errorf("CheckVersion = %q, %v", v, err)
	}
	old := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(old, []byte("#!/bin/sh\necho git version 2.31.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckVersion(context.Background(), old); err == nil || !strings.Contains(err.Error(), "2.32") {
		t.Errorf("CheckVersion of Git 2.31 = %v", err)
	}
	if _, err := CheckVersion(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("CheckVersion of a missing git succeeded")
	}
}
