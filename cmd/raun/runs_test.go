package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

// regexpJobLink finds the link to the page of a job on the Runs list.
var regexpJobLink = regexp.MustCompile(`href="(/alice/acme/api/runs/[0-9a-f]{32})"`)

// TestTheRunsOfARepositoryThroughTheServer: the job of a push shows on the
// Runs list of its repository; its page shows the log a runner sends,
// escaped, polled from the last offset; a person who decides cancels it,
// which its next heartbeat tells the runner; and the purge deletes the log
// of a run ended 30 days ago, keeping the job and its runs.
func TestTheRunsOfARepositoryThroughTheServer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	ticks := make(chan time.Time)
	s := start(t, dir, append(app(keyFile), "RAUN_ADMINS=github:1001"), serverDeps{forge: fake, jobs: noopOnPush, purge: ticks})
	alice := signedUp(t, s, fake, "1001", "alice")
	_, _, page := alice.submit("/instance/runners", "/instance/runners", url.Values{"name": {"builder"}, "workspace": {""}, "capability": {"noop"}})
	m := tokenShown.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no token shown")
	}
	token := m[1]
	if status, _, _ := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d", status)
	}
	h, b := fake.Deliver("d1", push("2001", strings.Repeat("b", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	status, body := runnerCall(t, s, token, "claim", "", "{}")
	var a runnerapi.Assignment
	if status != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil {
		t.Fatalf("claim = %d %s", status, body)
	}
	run, lease := "runs/"+a.Run, a.Lease.Token
	if got, body := runnerCall(t, s, token, run+"/log", lease, `{"seq": 0, "text": "fetching <b>x</b>\n"}`); got != http.StatusNoContent {
		t.Fatalf("log = %d %s", got, body)
	}

	runs := "/alice/acme/api/runs"
	_, _, list := alice.send(http.MethodGet, runs, nil)
	jobs := regexpJobLink.FindStringSubmatch(list)
	if jobs == nil || !strings.Contains(list, "refs/heads/main") {
		t.Fatalf("the Runs list: %s", list)
	}
	jobPath := jobs[1]
	status, _, page = alice.send(http.MethodGet, jobPath, nil)
	if status != http.StatusOK || !strings.Contains(page, "fetching &lt;b&gt;x&lt;/b&gt;") || strings.Contains(page, "<b>x</b>") {
		t.Fatalf("the page of the job = %d %s", status, page)
	}
	if got, body := runnerCall(t, s, token, run+"/log", lease, `{"seq": 1, "text": "running noop\n"}`); got != http.StatusNoContent {
		t.Fatalf("log = %d %s", got, body)
	}
	poll := jobPath + "/log?" + url.Values{"run": {a.Run}, "offset": {"18"}, "cut": {"0"}}.Encode()
	status, _, part := alice.send(http.MethodGet, poll, nil)
	if status != http.StatusOK || !strings.HasPrefix(part, "running noop\n<span") || strings.Contains(part, "fetching") {
		t.Errorf("a poll = %d %q", status, part)
	}

	if status, _, _ := alice.submit(jobPath, jobPath+"/cancel", url.Values{}); status != http.StatusSeeOther {
		t.Fatalf("a cancel = %d", status)
	}
	if got, body := runnerCall(t, s, token, run+"/heartbeat", lease, "{}"); got != http.StatusOK || !strings.Contains(body, `"cancel":true`) {
		t.Errorf("the heartbeat after the cancel = %d %s", got, body)
	}
	if _, _, page := alice.send(http.MethodGet, jobPath, nil); !strings.Contains(page, "Cancelled by hand.") || strings.Contains(page, "hx-get") {
		t.Errorf("the page of the cancelled job: %s", page)
	}

	// 31 days later, the log is purged; the job and its run stay.
	ticks <- time.Now().Add(job.LogRetention + 24*time.Hour)
	deadline := time.Now().Add(wait)
	for !strings.Contains(s.stderr.String(), `"msg":"expired logs of runs deleted","count":1`) {
		if time.Now().After(deadline) {
			t.Fatalf("the purge did not delete the log:\n%s", s.stderr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	store, err := sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if l, _ := store.Jobs().Log(ctx, a.Run); l.Text != "" {
		t.Errorf("the log after the purge: %q", l.Text)
	}
	if r, err := store.Jobs().Run(ctx, a.Run); err != nil || r.Outcome != job.OutcomeCancelled {
		t.Errorf("the run after the purge: %+v, %v", r, err)
	}
}
