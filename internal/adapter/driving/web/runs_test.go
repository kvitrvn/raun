package web

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// runsWorld is a login server whose repository acme/api, github/2001 of
// the workspace alice, has a job running and one queued, on a memory store.
type runsWorld struct {
	*loginServer
	store   *jobtest.Memory
	running job.Claimed
	queued  job.Job
	other   job.Job
	base    string
}

func newRunsWorld(t *testing.T, permission repository.Permission) *runsWorld {
	t.Helper()
	ctx := context.Background()
	store := &jobtest.Memory{}
	ws := strings.Repeat("a", 32)
	store.AddWorkspace(job.Workspace{ID: ws, Name: "alice"})
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	board := job.NewBoard(job.BoardOptions{Store: store, Reads: store, Runners: store, Now: func() time.Time { return now.Add(time.Minute) }})
	w := &runsWorld{store: store, base: "/alice/acme/api/runs"}
	w.loginServer = newLoginServer(t, func(o *Options) { o.Runs = board })
	w.repos.view = repository.View{
		Repository: repository.Repository{Forge: "github", ID: "2001", FullName: "acme/api", Workspace: repository.Workspace{Name: "alice"}, Active: true, Reachable: true},
		Permission: permission, Member: true,
	}
	enqueue := func(repo, ref string, at time.Time) job.Job {
		j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: ws, Forge: "github", Repository: repo, Commit: strings.Repeat("c", 40), Ref: ref, Delivery: "d-" + ref}, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	enqueue("2001", "refs/heads/main", now)
	c, err := store.Claim(ctx, job.Claimant{Runner: strings.Repeat("e", 32), Capabilities: []job.Capability{job.CapabilityNoop}}, now)
	if err != nil {
		t.Fatal(err)
	}
	w.running = c
	w.queued = enqueue("2001", "refs/heads/dev", now.Add(time.Second))
	// A job of another repository, which no URL of acme/api reaches.
	w.other = enqueue("3001", "refs/heads/secret", now.Add(2*time.Second))
	return w
}

func (w *runsWorld) appendLog(t *testing.T, seq int, text string) {
	t.Helper()
	if err := w.store.AppendLog(context.Background(), w.running.Run.ID, w.running.Token, seq, text, w.running.Run.StartedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestTheRunPagesNeedAnAccount(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionView)
	b := w.browser(t)
	for _, path := range []string{w.base, w.base + "/" + w.running.Job.ID, w.base + "/" + w.running.Job.ID + "/log?run=" + w.running.Run.ID + "&offset=0&cut=0"} {
		if got := location(t, b.do(http.MethodGet, path, nil), http.StatusSeeOther); !strings.HasPrefix(got, "/login?return=") {
			t.Errorf("GET %s without an account went to %q", path, got)
		}
	}
}

func TestTheRunPagesFollowTheForge(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionView)
	alice := w.signUp(t, "1001", "alice")
	jobPath := w.base + "/" + w.running.Job.ID
	logPath := jobPath + "/log?run=" + w.running.Run.ID + "&offset=0&cut=0"

	// A reader sees the list, the job and its log, and no cancel.
	w.appendLog(t, 0, "fetching\n<script>alert(1)</script>\n")
	resp := alice.do(http.MethodGet, w.base, nil)
	list := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(list, "refs/heads/main") || !strings.Contains(list, "refs/heads/dev") ||
		strings.Contains(list, "refs/heads/secret") || !strings.Contains(list, "No runner of its workspace has the capability noop") {
		t.Fatalf("the Runs list = %d %s", resp.StatusCode, list)
	}
	resp = alice.do(http.MethodGet, jobPath, nil)
	page := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "fetching\n&lt;script&gt;") || strings.Contains(page, "<script>alert") ||
		strings.Contains(page, "/cancel") || !strings.Contains(page, `hx-trigger="every 2s"`) {
		t.Fatalf("the page of the job = %d %s", resp.StatusCode, page)
	}
	if resp := alice.post(w.loginServer, jobPath+"/cancel", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a cancel by a reader = %d", resp.StatusCode)
	}
	if j, _ := w.store.Job(context.Background(), w.running.Job.ID); j.State != job.StateRunning {
		t.Errorf("a reader cancelled the job: %+v", j)
	}

	// A job, or a run, of another repository through the URL of acme/api.
	for _, path := range []string{
		w.base + "/" + w.other.ID,
		w.base + "/" + w.other.ID + "/log?run=" + w.running.Run.ID + "&offset=0&cut=0",
		w.base + "/" + w.queued.ID + "/log?run=" + w.running.Run.ID + "&offset=0&cut=0",
		jobPath + "?run=" + strings.Repeat("f", 32),
		w.base + "/not-an-id",
	} {
		if resp := alice.do(http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}

	// The forge no longer lets her read it: nothing, as for no repository.
	w.repos.err = repository.ErrNotFound
	for _, path := range []string{w.base, jobPath, logPath} {
		resp := alice.do(http.MethodGet, path, nil)
		if body := readBody(t, resp); resp.StatusCode != http.StatusNotFound || strings.Contains(body, "acme/api") || strings.Contains(body, "fetching") {
			t.Errorf("GET %s without access = %d %s", path, resp.StatusCode, body)
		}
	}
	if resp := alice.post(w.loginServer, jobPath+"/cancel", url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a cancel without access = %d", resp.StatusCode)
	}
	w.repos.err = repository.ErrUnavailable
	if resp := alice.do(http.MethodGet, w.base, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the Runs list with the forge down = %d", resp.StatusCode)
	}
}

func TestTheLogIsPolledFromTheLastOffset(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionView)
	alice := w.signUp(t, "1001", "alice")
	jobPath := w.base + "/" + w.running.Job.ID
	poll := func(offset int, cut string) (int, string) {
		t.Helper()
		resp := alice.do(http.MethodGet, jobPath+"/log?run="+w.running.Run.ID+"&offset="+strconv.Itoa(offset)+"&cut="+cut, nil)
		return resp.StatusCode, readBody(t, resp)
	}
	w.appendLog(t, 0, "one\n")
	status, body := poll(0, "0")
	if status != http.StatusOK || !strings.HasPrefix(body, "one\n<span") || !strings.Contains(body, "offset=4") {
		t.Fatalf("the first poll = %d %q", status, body)
	}
	w.appendLog(t, 1, "two <b>&</b>\n")
	status, body = poll(4, "0")
	if status != http.StatusOK || !strings.HasPrefix(body, "two &lt;b&gt;&amp;&lt;/b&gt;\n<span") || strings.Contains(body, "one") {
		t.Errorf("the next poll = %d %q", status, body)
	}
	for _, q := range []string{"offset=-1&cut=0", "offset=x&cut=0", "offset=999&cut=0", "offset=0&cut=2", "offset=0", "offset=99999999&cut=0"} {
		resp := alice.do(http.MethodGet, jobPath+"/log?run="+w.running.Run.ID+"&"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a poll with %s = %d", q, resp.StatusCode)
		}
	}

	// The run ends: the last part, and no more polling.
	if err := w.store.Report(context.Background(), w.running.Run.ID, w.running.Token, job.Result{Outcome: job.OutcomeSucceeded, Summary: "ok"}, w.running.Run.StartedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, body = poll(len("one\ntwo <b>&</b>\n"), "0")
	if status != http.StatusOK || strings.Contains(body, "hx-get") {
		t.Errorf("the poll of an ended run = %d %q", status, body)
	}
	if page := readBody(t, alice.do(http.MethodGet, jobPath, nil)); strings.Contains(page, "hx-get") {
		t.Error("the page of an ended run polls")
	}
}

func TestTheLogIsResetWhenCut(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionView)
	alice := w.signUp(t, "1001", "alice")
	chunk := strings.Repeat("x", job.MaxLogChunk)
	n := 0
	for ; n < job.MaxLog/job.MaxLogChunk; n++ {
		w.appendLog(t, n, chunk)
	}
	w.appendLog(t, n, chunk)
	jobPath := w.base + "/" + w.running.Job.ID
	resp := alice.do(http.MethodGet, jobPath+"/log?run="+w.running.Run.ID+"&offset="+strconv.Itoa(job.MaxLog)+"&cut=0", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, `<pre id="log"`) || !strings.Contains(body, `hx-swap-oob="true"`) ||
		!strings.Contains(body, "(truncated)") || !strings.Contains(body, "cut=1") {
		t.Errorf("the poll after the cut = %d %.200q", resp.StatusCode, body)
	}
}

func TestADeciderCancelsAJob(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionDecide)
	alice := w.signUp(t, "1001", "alice")
	jobPath := w.base + "/" + w.running.Job.ID
	page := readBody(t, alice.do(http.MethodGet, jobPath, nil))
	if !strings.Contains(page, `action="`+jobPath+`/cancel"`) {
		t.Fatalf("the page of the job has no cancel: %s", page)
	}
	if resp := alice.do(http.MethodPost, jobPath+"/cancel", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a cancel without its CSRF token = %d", resp.StatusCode)
	}
	for _, id := range []string{w.running.Job.ID, w.queued.ID} {
		if got := location(t, alice.post(w.loginServer, w.base+"/"+id+"/cancel", url.Values{}), http.StatusSeeOther); got != w.base+"/"+id {
			t.Errorf("a cancel went to %q", got)
		}
		if j, _ := w.store.Job(context.Background(), id); j.State != job.StateCancelled || j.Reason != job.ReasonCancelled {
			t.Errorf("the job after a cancel: %+v", j)
		}
	}
	// Ended already: back to the page, which says how.
	if got := location(t, alice.post(w.loginServer, jobPath+"/cancel", url.Values{}), http.StatusSeeOther); got != jobPath {
		t.Errorf("a cancel of an ended job went to %q", got)
	}
	if page := readBody(t, alice.do(http.MethodGet, jobPath, nil)); strings.Contains(page, "/cancel") || !strings.Contains(page, "Cancelled by hand.") {
		t.Errorf("the page of a cancelled job: %s", page)
	}
	if resp := alice.post(w.loginServer, w.base+"/"+w.other.ID+"/cancel", url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a cancel of a job of another repository = %d", resp.StatusCode)
	}
	if j, _ := w.store.Job(context.Background(), w.other.ID); j.State != job.StateQueued {
		t.Errorf("the job of another repository: %+v", j)
	}
	if !strings.Contains(w.logs.String(), `"msg":"job cancelled"`) {
		t.Error("the cancel was not logged")
	}
}

func TestTheRepositoryPageLinksItsRuns(t *testing.T) {
	w := newRunsWorld(t, repository.PermissionView)
	alice := w.signUp(t, "1001", "alice")
	w.repos.view.Events = []repository.LoggedEvent{{Forge: "github", Repository: "2001", Delivery: "d-refs/heads/main", Event: "push", Ref: "refs/heads/main", Result: repository.ResultRecorded}}
	page := readBody(t, alice.do(http.MethodGet, "/alice/acme/api", nil))
	if !strings.Contains(page, `href="`+w.base+`"`) || !strings.Contains(page, "See the runs") || !strings.Contains(page, `href="/alice/acme/api/runs">1</a>`) {
		t.Errorf("the repository page: %s", page)
	}
}
