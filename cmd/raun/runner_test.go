package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/forgetest"
	"github.com/kvitrvn/raun/internal/gittest"
)

func TestLoadRunnerConfig(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadRunnerConfig([]string{"RAUN_RUNNER_SERVER=https://raun.example.com", "RAUN_RUNNER_TOKEN_FILE=" + tokenFile,
		"RAUN_HTTP_ADDR=:9999", "RAUN_SOMETHING_ELSE=x"})
	if err != nil {
		t.Fatal(err)
	}
	if c.server != "https://raun.example.com" || c.token.Reveal() != strings.Repeat("a", 64) || c.capacity != 1 ||
		c.workDir != filepath.Join(os.TempDir(), "raun-runner") {
		t.Errorf("defaults: %+v", c)
	}
	c, err = loadRunnerConfig([]string{"RAUN_RUNNER_SERVER=http://127.0.0.1:8080", "RAUN_RUNNER_TOKEN=t0ken", "RAUN_RUNNER_CAPACITY=64",
		"RAUN_RUNNER_WORKDIR=rel/work"})
	if err != nil || c.capacity != 64 || !filepath.IsAbs(c.workDir) || c.server != "http://127.0.0.1:8080" {
		t.Errorf("loopback: %+v, %v", c, err)
	}
	for _, s := range c.logAttrs() {
		if strings.Contains(s.Value.String(), "t0ken") {
			t.Error("the configuration log holds the token")
		}
	}

	for name, tt := range map[string]struct {
		env  []string
		want string
	}{
		"nothing":            {nil, "RAUN_RUNNER_SERVER: required"},
		"no token":           {[]string{"RAUN_RUNNER_SERVER=https://x"}, "RAUN_RUNNER_TOKEN or RAUN_RUNNER_TOKEN_FILE: required"},
		"http elsewhere":     {[]string{"RAUN_RUNNER_SERVER=http://raun.example.com", "RAUN_RUNNER_TOKEN=s3cret"}, "RAUN_RUNNER_SERVER"},
		"a path":             {[]string{"RAUN_RUNNER_SERVER=https://x/raun", "RAUN_RUNNER_TOKEN=s3cret"}, "RAUN_RUNNER_SERVER"},
		"both token forms":   {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3cret", "RAUN_RUNNER_TOKEN_FILE=" + tokenFile}, "set one or neither"},
		"a missing file":     {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN_FILE=" + filepath.Join(dir, "none")}, "RAUN_RUNNER_TOKEN_FILE"},
		"an empty token":     {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN="}, "RAUN_RUNNER_TOKEN: empty"},
		"a token with space": {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3c ret"}, "RAUN_RUNNER_TOKEN: not the token"},
		"capacity 0":         {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3cret", "RAUN_RUNNER_CAPACITY=0"}, "RAUN_RUNNER_CAPACITY"},
		"capacity 65":        {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3cret", "RAUN_RUNNER_CAPACITY=65"}, "RAUN_RUNNER_CAPACITY"},
		"an empty workdir":   {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3cret", "RAUN_RUNNER_WORKDIR="}, "RAUN_RUNNER_WORKDIR"},
		"an unknown one":     {[]string{"RAUN_RUNNER_SERVER=https://x", "RAUN_RUNNER_TOKEN=s3cret", "RAUN_RUNNER_LABELS=gpu"}, "RAUN_RUNNER_LABELS: unknown variable"},
	} {
		_, err := loadRunnerConfig(tt.env)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", name, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), "s3c") {
			t.Errorf("%s: the error holds the token: %v", name, err)
		}
	}
}

func TestRunnerRelease(t *testing.T) {
	for _, tt := range []struct {
		version, want string
	}{
		{"v0.3.0", "v0.3.0"},
		{"v0.0.0-20261115213601-71b0f83312ee+dirty", "v0.0.0-20261115213601-71b0f83312ee+dirty"},
		{"(devel)", "dev"},
		{"", "dev"},
		{"v1 beta", "dev"},
	} {
		if got := runnerRelease(buildInfo{version: tt.version}); got != tt.want || !job.ValidRelease(got) {
			t.Errorf("runnerRelease(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestTheRunnerRefusesToStart(t *testing.T) {
	var stderr syncBuffer
	if code := runRunner([]string{"RAUN_RUNNER_TOKEN=s3cret-token"}, &stderr, runnerDeps{}); code != exitFailure ||
		!strings.Contains(stderr.String(), "raun runner: invalid configuration") || strings.Contains(stderr.String(), "s3cret-token") {
		t.Errorf("an invalid configuration = %d %s", code, stderr.String())
	}
	old := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(old, []byte("#!/bin/sh\necho git version 2.31.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stderr = syncBuffer{}
	env := []string{"RAUN_RUNNER_SERVER=http://127.0.0.1:1", "RAUN_RUNNER_TOKEN=s3cret-token", "RAUN_RUNNER_WORKDIR=" + t.TempDir()}
	if code := runRunner(env, &stderr, runnerDeps{git: old}); code != exitFailure || !strings.Contains(stderr.String(), "Git 2.32") {
		t.Errorf("an old Git = %d %s", code, stderr.String())
	}
}

// fixture is a repository served behind the clone credentials the fake
// forge mints for the repository 2001.
func fixture(t *testing.T, fake *forgetest.Fake) (string, *gittest.Server) {
	t.Helper()
	repo := gittest.New(t)
	repo.WriteFile("README.md", []byte("acme api\n"))
	commit := repo.Commit("first")
	srv := repo.Serve(func(w http.ResponseWriter, r *http.Request) bool {
		user, password, ok := r.BasicAuth()
		if ok && user == "x-access-token" && fake.ClonePassword(password, "2001") {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	})
	fake.SetCloneURL(func(string) string { return srv.URL })
	return commit, srv
}

// TestTheRunnerRunsAJob: a runner started against the server claims the job
// of a push, fetches its commit from the forge with the clone credential,
// runs its handler and reports; the credential is in neither the checkout,
// its Git configuration, the environment of the handler nor any log, and the
// work directory is left empty. A signal stops it with 0.
func TestTheRunnerRunsAJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	commit, gitServer := fixture(t, fake)
	s := start(t, dir, append(app(keyFile), "RAUN_ADMINS=github:1001"), serverDeps{forge: fake, jobs: noopOnPush})
	alice := signedUp(t, s, fake, "1001", "alice")
	status, _, page := alice.submit("/instance/runners", "/instance/runners", url.Values{"name": {"builder"}, "workspace": {""}, "capability": {"noop"}})
	m := tokenShown.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil {
		t.Fatalf("a creation = %d", status)
	}
	token := m[1]
	tokenFile := filepath.Join(dir, "runner-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d", status)
	}

	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(workDir, job.RunPrefix+"left-by-a-killed-runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The handler looks at what it can reach while it runs.
	type seen struct {
		run     string
		files   map[string]string
		environ []string
		task    string
	}
	saw := make(chan seen, 1)
	handlers := job.Handlers{job.KindNoop: job.HandlerFunc(func(_ context.Context, task job.Task) (job.Result, error) {
		files := map[string]string{}
		_ = filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := os.ReadFile(path)
				files[path] = string(b)
			}
			return nil
		})
		fmt.Fprintf(task.Log, "handled %s\n", task.Commit)
		saw <- seen{run: task.Run, files: files, environ: os.Environ(), task: fmt.Sprintf("%#v", task)}
		return job.Result{Outcome: job.OutcomeSucceeded, Summary: "nothing to do"}, nil
	})}

	signals := make(chan os.Signal, 2)
	var runnerLog syncBuffer
	exited := make(chan int, 1)
	env := []string{"RAUN_RUNNER_SERVER=http://" + s.addr, "RAUN_RUNNER_TOKEN_FILE=" + tokenFile, "RAUN_RUNNER_WORKDIR=" + workDir}
	go func() {
		exited <- runRunner(env, &runnerLog, runnerDeps{signals: signals, release: "0.3.0", handlers: handlers})
	}()

	h, b := fake.Deliver("d1", push("2001", commit))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	var got seen
	select {
	case got = <-saw:
	case <-time.After(30 * time.Second):
		t.Fatalf("the handler did not run:\n%s", runnerLog.String())
	}

	store, err := sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var j job.Job
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, err := store.Jobs().Run(ctx, got.run)
		if err != nil {
			t.Fatal(err)
		}
		if j, err = store.Jobs().Job(ctx, r.Job); err != nil {
			t.Fatal(err)
		}
		if j.State.Final() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job did not end: %+v\n%s", j, runnerLog.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if j.State != job.StateSucceeded {
		t.Fatalf("the job is %s:\n%s", j.State, runnerLog.String())
	}
	runs, err := store.Jobs().Runs(ctx, j.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %+v, %v", runs, err)
	}
	log, err := store.Jobs().Log(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"fetching " + commit, "fetched", "running noop", "handled " + commit, "succeeded: nothing to do"} {
		if !strings.Contains(log.Text, step+"\n") {
			t.Errorf("the log of the run lacks %q: %q", step, log.Text)
		}
	}

	// The credential the forge minted for the run: the one the Git server
	// received.
	var password string
	for _, a := range gitServer.Authorizations() {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(a, "Basic "))
		if err == nil {
			_, password, _ = strings.Cut(string(raw), ":")
		}
	}
	if password == "" {
		t.Fatal("the fetch sent no credential")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + password))
	if len(got.files) == 0 {
		t.Fatal("the handler saw no checkout")
	}
	for path, content := range got.files {
		if strings.Contains(content, password) || strings.Contains(content, encoded) {
			t.Errorf("%s holds the credential", path)
		}
		if strings.HasSuffix(path, "/config") && (strings.Contains(content, "extraheader") || strings.Contains(content, "url")) {
			t.Errorf("the Git configuration of the checkout: %s", content)
		}
	}
	for _, kv := range got.environ {
		if strings.Contains(kv, password) || strings.Contains(kv, encoded) || strings.HasPrefix(kv, "GIT_CONFIG_") {
			t.Errorf("the environment of the handler holds %q", kv)
		}
	}
	for name, text := range map[string]string{"the task": got.task, "the runner log": runnerLog.String(), "the server log": s.stderr.String(), "the log of the run": log.Text} {
		for _, secret := range []string{password, encoded, token} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds a secret", name)
			}
		}
	}
	if fake.ClonePassword(password, "2001") {
		t.Error("the credential was not revoked when the run ended")
	}

	signals <- syscall.SIGTERM
	select {
	case code := <-exited:
		if code != exitOK {
			t.Errorf("the runner exited with %d:\n%s", code, runnerLog.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the runner did not stop")
	}
	if entries, _ := os.ReadDir(workDir); len(entries) != 0 {
		t.Errorf("the work directory holds %v", entries)
	}
	runner, err := store.Runners().Runners(ctx)
	if err != nil || len(runner) != 1 || runner[0].Release != "0.3.0" {
		t.Errorf("the runner %+v, %v", runner, err)
	}
}

// TestTheRunnerStopsWhenRevoked: a runner whose token is revoked exits with
// 1, at once.
func TestTheRunnerStopsWhenRevoked(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	s := start(t, dir, append(app(keyFile), "RAUN_ADMINS=github:1001"), serverDeps{forge: fake, claimWait: time.Minute})
	alice := signedUp(t, s, fake, "1001", "alice")
	_, _, page := alice.submit("/instance/runners", "/instance/runners", url.Values{"name": {"builder"}, "workspace": {""}, "capability": {"noop"}})
	m := tokenShown.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no token shown")
	}
	var runnerLog syncBuffer
	exited := make(chan int, 1)
	started := make(chan struct{})
	env := []string{"RAUN_RUNNER_SERVER=http://" + s.addr, "RAUN_RUNNER_TOKEN=" + m[1], "RAUN_RUNNER_WORKDIR=" + t.TempDir()}
	go func() {
		exited <- runRunner(env, &runnerLog, runnerDeps{signals: make(chan os.Signal), release: "0.3.0", started: func() { close(started) }})
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	_, _, list := alice.send(http.MethodGet, "/instance/runners", nil)
	ids := regexp.MustCompile(`action="/instance/runners/([0-9a-f]{32})/rotate"`).FindStringSubmatch(list)
	if ids == nil {
		t.Fatal("no runner listed")
	}
	id := ids[1]
	if status, _, _ := alice.submit("/instance/runners", "/instance/runners/"+id+"/revoke", url.Values{}); status != http.StatusSeeOther {
		t.Fatalf("a revocation = %d", status)
	}
	select {
	case code := <-exited:
		if code != exitFailure || !strings.Contains(runnerLog.String(), "refuses this runner") {
			t.Errorf("a revoked runner exited with %d:\n%s", code, runnerLog.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a revoked runner did not stop")
	}
	if strings.Contains(runnerLog.String(), m[1]) {
		t.Error("the runner logged its token")
	}
}
