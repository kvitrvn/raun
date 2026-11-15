package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Checkout is a commit fetched by Fetch: a Reader of the repository it was
// fetched into, and the commit, as job.Snapshot names it.
type Checkout struct {
	*Reader
	commit string
}

var _ job.Snapshot = (*Checkout)(nil)

// Commit is the full hash of the commit fetched.
func (c *Checkout) Commit() string { return c.commit }

// FetchRequest is the one commit to fetch, and the credential that fetches
// it.
type FetchRequest struct {
	// URL is the address of the repository: https, or http on a loopback
	// address only, with no user, query or fragment.
	URL    string
	Commit string
	// Username and Password are sent in an HTTP Basic header, which the
	// environment of the fetch alone gives Git.
	Username string
	Password job.Secret
}

// Fetch fetches the one commit of req into dir, an empty directory, as a
// bare repository with that commit alone, and returns a Reader of it.
//
// Git runs as the Reader runs it, with one difference: it may reach the
// address of req, in https, or in http on a loopback address. The
// credential reaches Git through the environment of that process alone, as
// the configuration http.extraHeader given by GIT_CONFIG_COUNT: never on
// its command line, in a file or in the configuration of the repository.
// Hooks, templates, credential helpers, redirects and submodules are off,
// and the objects received are checked. The error of a failed fetch holds
// the address and what Git said, never the credential.
func Fetch(ctx context.Context, dir string, req FetchRequest, opts Options) (*Checkout, error) {
	scheme, err := checkFetchURL(req.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if !isHash(req.Commit) {
		return nil, fmt.Errorf("fetch: %q is not the full hash of a commit", req.Commit)
	}
	format := "sha1"
	if len(req.Commit) == 64 {
		format = "sha256"
	}
	gitPath := opts.Git
	if gitPath == "" {
		if gitPath, err = exec.LookPath("git"); err != nil {
			return nil, fmt.Errorf("fetch: %w", err)
		}
	}
	if !filepath.IsAbs(gitPath) {
		return nil, fmt.Errorf("fetch: git binary %q is not an absolute path", gitPath)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("fetch: %s is not empty", abs)
	}

	run := func(cmd *exec.Cmd, name string) error {
		stderr := &limitedBuffer{limit: maxStderr}
		cmd.Stdout, cmd.Stderr = nil, stderr
		if err := cmd.Run(); err != nil {
			return redactError(commandError(name, stderr, err), req)
		}
		return nil
	}
	// No template: no sample hook, no description, nothing Raun did not ask
	// for.
	initCmd := hardened(ctx, gitPath, abs, []string{"-c", "protocol.allow=never"},
		"init", "--quiet", "--bare", "--template=", "--object-format="+format, abs)
	initCmd.Env = append(initCmd.Env, "GIT_ALLOW_PROTOCOL=")
	if err := run(initCmd, "init"); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(req.Username+":"+req.Password.Reveal()))
	fetch := hardened(ctx, gitPath, abs, []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.version=2",
		"-c", "http.followRedirects=false",
		"-c", "credential.helper=",
		"-c", "fetch.fsckObjects=true",
		"-c", "transfer.fsckObjects=true",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
	}, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--depth", "1", req.URL, req.Commit)
	fetch.Env = append(fetch.Env,
		"GIT_DIR="+abs,
		// The one protocol of the address, over protocol.allow=never.
		"GIT_ALLOW_PROTOCOL="+scheme,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0="+header,
	)
	if err := run(fetch, "fetch"); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", req.URL, err)
	}

	r, err := Open(ctx, abs, Options{Git: gitPath, MaxFileSize: opts.MaxFileSize, MaxTreeEntries: opts.MaxTreeEntries})
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	got, err := r.ResolveRevision(ctx, req.Commit)
	if err != nil || got != req.Commit {
		return nil, fmt.Errorf("fetch %s: the commit %s is not in what the forge sent", req.URL, req.Commit)
	}
	return &Checkout{Reader: r, commit: req.Commit}, nil
}

// checkFetchURL accepts an address Git may fetch from, and returns its
// scheme: https, or http on a loopback address.
func checkFetchURL(raw string) (string, error) {
	if raw == "" || strings.ContainsFunc(raw, isControl) || strings.ContainsAny(raw, " ?#") {
		return "", fmt.Errorf("%q is not the address of a repository", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%q is not the address of a repository", raw)
	}
	switch u.Scheme {
	case "https":
		return "https", nil
	case "http":
		host := strings.TrimSuffix(u.Hostname(), ".")
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return "http", nil
		}
		return "", fmt.Errorf("%q: http is allowed on a loopback address only", raw)
	}
	return "", fmt.Errorf("%q is not an https address", raw)
}

// redactError replaces in err the credential of req, in every form a header
// could show it, though Git never writes it.
func redactError(err error, req FetchRequest) error {
	p := req.Password.Reveal()
	if p == "" {
		return err
	}
	msg := err.Error()
	for _, s := range []string{p, base64.StdEncoding.EncodeToString([]byte(req.Username + ":" + p))} {
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// Fetcher is the checkout of raun runner (job.Checkout): Fetch with the
// clone credential of a job.
type Fetcher struct {
	Options Options
}

var _ job.Checkout = Fetcher{}

// Fetch implements job.Checkout.
func (f Fetcher) Fetch(ctx context.Context, dir string, w job.Work) (job.Snapshot, error) {
	c, err := Fetch(ctx, dir, FetchRequest{URL: w.Clone.URL, Commit: w.Commit, Username: w.Clone.Username, Password: w.Clone.Password}, f.Options)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CheckVersion runs the git binary at path, the one in PATH when empty, and
// refuses it older than 2.32; it returns the version Git reports.
func CheckVersion(ctx context.Context, path string) (string, error) {
	if path == "" {
		p, err := exec.LookPath("git")
		if err != nil {
			return "", fmt.Errorf("find git: %w", err)
		}
		path = p
	}
	cmd := hardened(ctx, path, os.TempDir(), []string{"-c", "protocol.allow=never"}, "version")
	cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=")
	stdout := &limitedBuffer{limit: maxRecord}
	stderr := &limitedBuffer{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return "", commandError("version", stderr, err)
	}
	out := strings.TrimSpace(stdout.String())
	if err := checkVersion(out); err != nil {
		return out, err
	}
	return out, nil
}
