package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/adapter/runnerapi"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/forgetest"
)

var tokenShown = regexp.MustCompile(`data-slot="token"[^>]*>([0-9a-f]{64})</code>`)

// asRunner sends a request of the runner of token to the runner API.
func asRunner(t *testing.T, s *server, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+s.addr+"/api/runner/v1/claim", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Raun-Runner-Protocol", "1")
	req.Header.Set("User-Agent", "raun-runner/0.3.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestRunnersThroughTheServer: an administrator creates a runner on the
// Runners screen and gets its token once; the token opens the runner API
// until a new one replaces it, and a revocation refuses the runner and
// sends the job it runs back to the queue.
func TestRunnersThroughTheServer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	s := start(t, dir, append(app(keyFile), "RAUN_ADMINS=github:1001", "RAUN_SIGNUP_USERS=github:1002"),
		serverDeps{forge: fake, missing: allCapabilities, jobs: noopOnPush})
	alice := signedUp(t, s, fake, "1001", "alice")
	bob := signedUp(t, s, fake, "1002", "bob")
	if status, _, _ := bob.send(http.MethodGet, "/instance/runners", nil); status != http.StatusForbidden {
		t.Errorf("the Runners screen for Bob = %d", status)
	}

	status, _, page := alice.submit("/instance/runners", "/instance/runners", url.Values{"name": {"builder"}, "workspace": {"alice"}, "capability": {"noop"}})
	m := tokenShown.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil {
		t.Fatalf("a creation = %d %s", status, page)
	}
	token := m[1]
	if _, _, list := alice.send(http.MethodGet, "/instance/runners", nil); strings.Contains(list, token) || !strings.Contains(list, "workspace alice") {
		t.Errorf("the list after a creation: %s", list)
	}
	if strings.Contains(s.stderr.String(), token) {
		t.Error("the token was logged")
	}
	if got := asRunner(t, s, token); got != http.StatusNoContent {
		t.Errorf("the runner on the API = %d, want through to an empty queue", got)
	}
	if got := asRunner(t, s, strings.Repeat("0", 64)); got != http.StatusUnauthorized {
		t.Errorf("an unknown token = %d", got)
	}

	// A job of its workspace, taken by the runner.
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d %s", status, body)
	}
	h, b := fake.Deliver("d1", push("2001", strings.Repeat("b", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	store, err := sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	all, err := store.Runners().Runners(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("Runners = %+v, %v", all, err)
	}
	runner := all[0]
	if runner.LastContact.IsZero() || runner.Release != "0.3.0" {
		t.Errorf("the request of the runner left no last contact or release: %+v", runner)
	}
	c, err := store.Runners().ClaimFor(ctx, runner.ID, time.Now())
	if err != nil {
		t.Fatalf("ClaimFor: %v", err)
	}

	// A new token refuses the old one.
	_, _, list := alice.send(http.MethodGet, "/instance/runners", nil)
	status, _, page = alice.submit("/instance/runners", "/instance/runners/"+runner.ID+"/rotate", url.Values{})
	m = tokenShown.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil || m[1] == token || !strings.Contains(list, runner.ID) {
		t.Fatalf("a rotation = %d %s", status, page)
	}
	if got := asRunner(t, s, token); got != http.StatusUnauthorized {
		t.Errorf("the old token = %d", got)
	}
	fresh := m[1]
	if got := asRunner(t, s, fresh); got != http.StatusNoContent {
		t.Errorf("the new token = %d", got)
	}

	// A revocation refuses the runner and sends its job back.
	if status, _, _ := alice.submit("/instance/runners", "/instance/runners/"+runner.ID+"/revoke", url.Values{}); status != http.StatusSeeOther {
		t.Fatalf("a revocation = %d", status)
	}
	if got := asRunner(t, s, fresh); got != http.StatusUnauthorized {
		t.Errorf("a revoked runner = %d", got)
	}
	if j, err := store.Jobs().Job(ctx, c.Job.ID); err != nil || j.State != job.StateQueued {
		t.Errorf("the job of the revoked runner = %+v, %v", j, err)
	}
	if _, err := store.Runners().ClaimFor(ctx, runner.ID, time.Now()); !errors.Is(err, job.ErrUnauthenticated) {
		t.Errorf("ClaimFor of a revoked runner = %v", err)
	}
}

// runnerCall sends body to path below /api/runner/v1/ as the runner of
// token, with the lease when it is not empty, and returns the status and
// the body of the answer.
func runnerCall(t *testing.T, s *server, token, path, lease, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+s.addr+"/api/runner/v1/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Raun-Runner-Protocol", "1")
	req.Header.Set("User-Agent", "raun-runner/0.3.0")
	if lease != "" {
		req.Header.Set("Raun-Lease", lease)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// TestTheRunnerProtocolThroughTheServer: a runner claims the job of a push
// with its clone credential, renews its lease, sends its log and its
// result; a foreign lease and a result its kind refuses are refused; the
// credential is revoked once the run ends, and neither it nor the lease
// token reaches the log of the server or the database.
func TestTheRunnerProtocolThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	s := start(t, dir, append(app(keyFile), "RAUN_ADMINS=github:1001"), serverDeps{forge: fake, jobs: noopOnPush})
	alice := signedUp(t, s, fake, "1001", "alice")
	status, _, page := alice.submit("/instance/runners", "/instance/runners", url.Values{"name": {"builder"}, "workspace": {""}, "capability": {"noop"}})
	m := tokenShown.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil {
		t.Fatalf("a creation = %d %s", status, page)
	}
	token := m[1]
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d %s", status, body)
	}
	for i, after := range []string{strings.Repeat("b", 40), strings.Repeat("d", 40)} {
		h, b := fake.Deliver(fmt.Sprintf("d%d", i), push("2001", after))
		if got := post(t, s, h, b); got != http.StatusAccepted {
			t.Fatalf("a push = %d", got)
		}
	}

	claim := func() runnerapi.Assignment {
		t.Helper()
		status, body := runnerCall(t, s, token, "claim", "", "{}")
		var a runnerapi.Assignment
		if status != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil {
			t.Fatalf("claim = %d %s", status, body)
		}
		return a
	}
	a := claim()
	if a.Job.Kind != "noop" || a.Job.Repository.FullName != "acme/api" || a.Job.Commit != strings.Repeat("b", 40) || a.Clone.Password == "" {
		t.Fatalf("the claim gave %+v", a)
	}
	run, lease := "runs/"+a.Run, a.Lease.Token
	for _, step := range []struct {
		path, lease, body string
		status            int
	}{
		{"/heartbeat", lease, "{}", http.StatusOK},
		{"/log", lease, `{"seq": 0, "text": "fetch\n"}`, http.StatusNoContent},
		{"/log", lease, `{"seq": 0, "text": "fetch\n"}`, http.StatusNoContent},
		{"/log", lease, `{"seq": 2, "text": "x"}`, http.StatusConflict},
		{"/heartbeat", strings.Repeat("0", 64), "{}", http.StatusConflict},
		{"/result", lease, `{"outcome": "succeeded", "summary": "nothing to do", "data": {}}`, http.StatusNoContent},
		{"/result", lease, `{"outcome": "succeeded", "summary": "nothing to do", "data": {}}`, http.StatusNoContent},
		{"/result", lease, `{"outcome": "failed", "summary": "other"}`, http.StatusConflict},
	} {
		if got, body := runnerCall(t, s, token, run+step.path, step.lease, step.body); got != step.status {
			t.Errorf("%s %s = %d %s, want %d", step.path, step.body, got, body, step.status)
		}
	}

	// The second job: a result its kind refuses fails it.
	b := claim()
	if got, body := runnerCall(t, s, token, "runs/"+b.Run+"/result", b.Lease.Token, `{"outcome": "succeeded", "summary": "x", "data": {"x": 1}}`); got != http.StatusUnprocessableEntity {
		t.Errorf("a refused result = %d %s", got, body)
	}
	if minted, live := fake.Clones(); minted != 2 || live != 0 {
		t.Errorf("clone credentials: %d minted, %d live; want 2 and none", minted, live)
	}

	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if l, err := store.Jobs().Log(ctx, a.Run); err != nil || l.Text != "fetch\n" || l.Next != 1 {
		t.Errorf("the log of the run = %+v, %v", l, err)
	}
	refused, err := store.Jobs().Run(ctx, b.Run)
	if err != nil || refused.Outcome != job.OutcomeFailed || refused.Result != nil {
		t.Errorf("the run of the refused result = %+v, %v", refused, err)
	}
	db, err := os.ReadFile(filepath.Join(dir, "raun.db"))
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(filepath.Join(dir, "raun.db-wal"))
	for _, secret := range []string{a.Clone.Password, b.Clone.Password, lease, b.Lease.Token, token} {
		if strings.Contains(s.stderr.String(), secret) {
			t.Errorf("the log of the server holds a secret")
		}
		if strings.Contains(string(db), secret) || strings.Contains(string(wal), secret) {
			t.Errorf("the database holds a secret")
		}
	}
}
