package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/runnerapi"
	"github.com/kvitrvn/raun/internal/core/job"
)

// apiRunner is a runner of a runnerWorld with its token.
type apiRunner struct {
	w     *runnerWorld
	t     *testing.T
	token string
	id    string
}

func (w *runnerWorld) runner(t *testing.T, name, workspace string) *apiRunner {
	t.Helper()
	admin := strings.Repeat("e", 32)
	w.admins.set(admin, true, nil)
	r, token, err := w.registry.Create(context.Background(), admin, name, workspace, []job.Capability{job.CapabilityNoop})
	if err != nil {
		t.Fatal(err)
	}
	return &apiRunner{w: w, t: t, token: token, id: r.ID}
}

// enqueue queues a noop job of the repository 2001 of the workspace acme.
func (w *runnerWorld) enqueue(t *testing.T) job.Job {
	t.Helper()
	j, err := job.New(rand.Reader, job.Request{Kind: job.KindNoop, Workspace: strings.Repeat("a", 32), Forge: "github",
		Repository: "2001", Commit: strings.Repeat("c", 40), Ref: "refs/heads/main"}, w.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Enqueue(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

// send sends body to path below /api/runner/v1/ as the runner, with the
// lease when it is not empty, and returns the status and the body.
func (a *apiRunner) send(path, lease, body string, headers ...string) (int, string) {
	a.t.Helper()
	r := request(http.MethodPost, "/api/runner/v1/"+path, testHost, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Header.Set("Raun-Runner-Protocol", "1")
	r.Header.Set("User-Agent", "raun-runner/1.0.0")
	if lease != "" {
		r.Header.Set("Raun-Lease", lease)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			r.Header.Del(headers[i])
		} else {
			r.Header.Set(headers[i], headers[i+1])
		}
	}
	resp := serve(a.w, r)
	return resp.StatusCode, readBody(a.t, resp)
}

// claim claims a job, which there must be.
func (a *apiRunner) claim() runnerapi.Assignment {
	a.t.Helper()
	status, body := a.send("claim", "", "{}")
	if status != http.StatusOK {
		a.t.Fatalf("claim = %d %s", status, body)
	}
	var got runnerapi.Assignment
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		a.t.Fatal(err)
	}
	return got
}

func TestTheClaimGivesAJobAndItsCredential(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	j := w.enqueue(t)
	got := a.claim()
	if got.Run == "" || got.Lease.Token == "" || !got.Lease.Expires.Equal(w.clock.Now().Add(job.Lease)) || !got.Deadline.Equal(w.clock.Now().Add(5*time.Minute)) {
		t.Errorf("run, lease or deadline: %+v", got)
	}
	want := runnerapi.Job{Kind: "noop", Repository: runnerapi.Repository{Forge: "github", ID: "2001", FullName: "acme/api"},
		Commit: j.Commit, Ref: "refs/heads/main", Params: json.RawMessage(`{}`)}
	if got.Job.Kind != want.Kind || got.Job.Repository != want.Repository || got.Job.Commit != want.Commit || got.Job.Ref != want.Ref || string(got.Job.Params) != "{}" {
		t.Errorf("job %+v, want %+v", got.Job, want)
	}
	if got.Clone.URL != "https://github.test/acme/api.git" || got.Clone.Username == "" || got.Clone.Password == "" || got.Clone.Expires.IsZero() {
		t.Errorf("clone %+v", got.Clone)
	}
	if !w.clones.CloneReads(job.Credential{Password: job.Secret(got.Clone.Password)}, "2001") {
		t.Error("the forge refuses the credential given")
	}
	for _, secret := range []string{got.Clone.Password, got.Lease.Token, a.token} {
		if strings.Contains(w.logs.String(), secret) {
			t.Errorf("the log holds a secret: %s", w.logs)
		}
	}
	// The queue is empty now: 204 after the wait.
	if status, body := a.send("claim", "", "{}"); status != http.StatusNoContent || body != "" {
		t.Errorf("a claim of an empty queue = %d %q", status, body)
	}
}

func TestAClaimWaitsForAJob(t *testing.T) {
	w := newRunnerWorld(t, time.Minute, nil)
	a := w.runner(t, "builder", "")
	done := make(chan int, 1)
	go func() {
		status, _ := a.send("claim", "", "{}")
		done <- status
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case status := <-done:
		t.Fatalf("the claim answered %d before any job", status)
	default:
	}
	w.enqueue(t)
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Errorf("the woken claim = %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the enqueue did not wake the claim")
	}

	// The stop of the server ends a waiting claim at once.
	go func() {
		status, _ := a.send("claim", "", "{}")
		done <- status
	}()
	time.Sleep(20 * time.Millisecond)
	w.BeginShutdown()
	select {
	case status := <-done:
		if status != http.StatusNoContent {
			t.Errorf("a claim at the stop = %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stop did not end the claim")
	}
}

func TestAClaimFollowsTheScopeOfTheRunner(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	other := strings.Repeat("b", 32)
	w.store.AddWorkspace(job.Workspace{ID: other, Name: "beta"})
	beta := w.runner(t, "beta-runner", "beta")
	w.enqueue(t)
	if status, _ := beta.send("claim", "", "{}"); status != http.StatusNoContent {
		t.Errorf("a runner of beta claimed a job of acme: %d", status)
	}
	acme := w.runner(t, "acme-runner", "acme")
	acme.claim()
}

func TestTheRoutesOfARun(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	b := w.runner(t, "other", "")
	w.enqueue(t)
	got := a.claim()
	run, lease := "runs/"+got.Run, got.Lease.Token

	w.clock.Advance(10 * time.Second)
	status, body := a.send(run+"/heartbeat", lease, "{}")
	if status != http.StatusOK || body != fmt.Sprintf(`{"expires":%q,"cancel":false}`+"\n", w.clock.Now().Add(job.Lease).Format(time.RFC3339)) {
		t.Errorf("heartbeat = %d %s", status, body)
	}
	for _, tt := range []struct {
		name   string
		body   string
		status int
		answer string
	}{
		{"chunk 0", `{"seq": 0, "text": "fetch\n"}`, http.StatusNoContent, ""},
		{"chunk 0 again", `{"seq": 0, "text": "other\n"}`, http.StatusNoContent, ""},
		{"chunk 1", `{"seq": 1, "text": "noop\n"}`, http.StatusNoContent, ""},
		{"a skipped number", `{"seq": 3, "text": "x"}`, http.StatusConflict, `{"error":"chunk 2 is expected","expected":2}` + "\n"},
		{"no text", `{"seq": 2}`, http.StatusBadRequest, `{"error":"a chunk needs seq and text"}` + "\n"},
		{"a negative number", `{"seq": -1, "text": "x"}`, http.StatusBadRequest, ""},
		{"an unknown field", `{"seq": 2, "text": "x", "level": "info"}`, http.StatusBadRequest, ""},
		{"a lone surrogate, replaced", `{"seq": 2, "text": "\udc00"}`, http.StatusNoContent, ""},
		{"decoded text past its bound", `{"seq": 3, "text": "` + strings.Repeat(`é`, job.MaxLogChunk/2+1) + `"}`, http.StatusRequestEntityTooLarge, ""},
		{"a body past its bound", `{"seq": 3, "text": "` + strings.Repeat("x", runnerapi.MaxLogBody) + `"}`, http.StatusRequestEntityTooLarge, ""},
	} {
		status, body := a.send(run+"/log", lease, tt.body)
		if status != tt.status || (tt.answer != "" && body != tt.answer) {
			t.Errorf("log, %s = %d %s; want %d %s", tt.name, status, body, tt.status, tt.answer)
		}
	}
	if l, _ := w.store.Log(context.Background(), got.Run); !strings.HasPrefix(l.Text, "fetch\nnoop\n") || l.Next != 3 {
		t.Errorf("the log kept %+v", l)
	}

	// Another runner, another lease, no lease: refused.
	for name, try := range map[string]func() (int, string){
		"another runner": func() (int, string) { return b.send(run+"/heartbeat", lease, "{}") },
		"another lease":  func() (int, string) { return a.send(run+"/heartbeat", strings.Repeat("0", 64), "{}") },
		"an unknown run": func() (int, string) { return a.send("runs/"+strings.Repeat("f", 32)+"/heartbeat", lease, "{}") },
		"a foreign log":  func() (int, string) { return a.send(run+"/log", strings.Repeat("0", 64), `{"seq": 3, "text": "x"}`) },
		"a foreign result": func() (int, string) {
			return b.send(run+"/result", lease, `{"outcome": "succeeded", "summary": "x"}`)
		},
	} {
		if status, body := try(); status != http.StatusConflict || body != `{"error":"the run is no longer current"}`+"\n" {
			t.Errorf("%s = %d %s", name, status, body)
		}
	}
	if status, _ := a.send(run+"/heartbeat", "", "{}"); status != http.StatusBadRequest {
		t.Errorf("no lease = %d", status)
	}
	if status, _ := a.send(run+"/heartbeat", lease, `{"x": 1}`); status != http.StatusBadRequest {
		t.Errorf("a heartbeat with a field = %d", status)
	}
	if status, _ := a.send(run+"/heartbeat", lease, "{}"+strings.Repeat(" ", runnerapi.MaxHeartbeatBody)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("a heartbeat past its bound = %d", status)
	}

	// The result, once; the same again; another one refused.
	result := `{"outcome": "succeeded", "summary": "nothing to do", "data": {}}`
	if status, body := a.send(run+"/result", lease, result); status != http.StatusNoContent {
		t.Fatalf("result = %d %s", status, body)
	}
	if _, live := w.clones.Clones(); live != 0 {
		t.Error("the credential outlived the result")
	}
	if status, _ := a.send(run+"/result", lease, result); status != http.StatusNoContent {
		t.Errorf("the same result again = %d", status)
	}
	if status, body := a.send(run+"/result", lease, `{"outcome": "failed", "summary": "x"}`); status != http.StatusConflict || !strings.Contains(body, "another result") {
		t.Errorf("another result = %d %s", status, body)
	}
	if status, _ := a.send(run+"/heartbeat", lease, "{}"); status != http.StatusConflict {
		t.Errorf("a heartbeat after the result = %d", status)
	}
}

func TestAResultItsKindRefusesFailsTheJob(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	j := w.enqueue(t)
	got := a.claim()
	run, lease := "runs/"+got.Run, got.Lease.Token
	if status, _ := a.send(run+"/result", lease, `{"outcome": "succeeded", "summary": "x", "data": {"`+strings.Repeat("x", 4<<10+runnerapi.ResultEnvelope)+`": 1}}`); status != http.StatusRequestEntityTooLarge {
		t.Errorf("a result past its bound = %d", status)
	}
	if status, _ := a.send(run+"/result", lease, `{"outcome": "succeeded", "extra": 1}`); status != http.StatusBadRequest {
		t.Errorf("a result with an unknown field = %d", status)
	}
	if stored, _ := w.store.Job(context.Background(), j.ID); stored.State != job.StateRunning {
		t.Fatalf("a malformed body changed the job: %+v", stored)
	}
	status, body := a.send(run+"/result", lease, `{"outcome": "succeeded", "summary": "x", "data": {"found": 1}}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "noop result") {
		t.Errorf("a refused result = %d %s", status, body)
	}
	stored, _ := w.store.Job(context.Background(), j.ID)
	runs, _ := w.store.Runs(context.Background(), j.ID)
	if stored.State != job.StateFailed || stored.Reason != job.ReasonInvalidResult || len(runs) != 1 || runs[0].Result != nil {
		t.Errorf("after a refused result: %+v, runs %+v", stored, runs)
	}
	if _, live := w.clones.Clones(); live != 0 {
		t.Error("the credential outlived a refused result")
	}
}

func TestAHeartbeatTellsTheCancellation(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	j := w.enqueue(t)
	got := a.claim()
	if _, err := w.store.Cancel(context.Background(), j.ID, job.ReasonCancelled, w.clock.Now()); err != nil {
		t.Fatal(err)
	}
	run, lease := "runs/"+got.Run, got.Lease.Token
	if status, body := a.send(run+"/heartbeat", lease, "{}"); status != http.StatusOK || body != `{"cancel":true}`+"\n" {
		t.Errorf("heartbeat of a cancelled job = %d %s", status, body)
	}
	if status, body := a.send(run+"/result", lease, `{"outcome": "succeeded", "summary": "x"}`); status != http.StatusConflict || !strings.Contains(body, "cancelled") {
		t.Errorf("a result of a cancelled job = %d %s", status, body)
	}
	if _, live := w.clones.Clones(); live != 0 {
		t.Error("the credential outlived the cancellation")
	}
}

func TestAnExpiredLeaseIsRefused(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	w.enqueue(t)
	got := a.claim()
	w.clock.Advance(job.Lease)
	run, lease := "runs/"+got.Run, got.Lease.Token
	for _, path := range []string{"/heartbeat", "/log", "/result"} {
		body := "{}"
		switch path {
		case "/log":
			body = `{"seq": 0, "text": "x"}`
		case "/result":
			body = `{"outcome": "succeeded", "summary": "x"}`
		}
		if status, answer := a.send(run+path, lease, body); status != http.StatusConflict {
			t.Errorf("%s after the lease = %d %s", path, status, answer)
		}
	}
}

func TestTheProtocolVersion(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	send := func(path string, headers ...string) (int, string) {
		t.Helper()
		r := request(http.MethodPost, path, testHost, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+a.token)
		r.Header.Set("User-Agent", "raun-runner/1.0.0")
		r.Header.Set("Raun-Runner-Protocol", "1")
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Del(headers[i])
			if headers[i+1] != "" {
				r.Header.Add(headers[i], headers[i+1])
			}
		}
		resp := serve(w, r)
		if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusNoContent && ct != "application/json" {
			t.Errorf("%s answers %s", path, ct)
		}
		return resp.StatusCode, readBody(t, resp)
	}
	for _, tt := range []struct {
		name    string
		path    string
		headers []string
		status  int
		answer  string
	}{
		{"protocol 2 in the path", "/api/runner/v2/claim", []string{"Raun-Runner-Protocol", "2"}, http.StatusBadRequest,
			`{"error":"protocol 2 is not spoken here; this server speaks 1"}`},
		{"protocol 2 in the header", "/api/runner/v1/claim", []string{"Raun-Runner-Protocol", "2"}, http.StatusBadRequest,
			`{"error":"protocol 2 is not spoken here; this server speaks 1"}`},
		{"no header", "/api/runner/v1/claim", []string{"Raun-Runner-Protocol", ""}, http.StatusBadRequest, ""},
		{"a header that is not a version", "/api/runner/v1/claim", []string{"Raun-Runner-Protocol", "one"}, http.StatusBadRequest, ""},
		{"no version in the path", "/api/runner/claim", nil, http.StatusBadRequest,
			`{"error":"the path names no protocol; this server speaks 1"}`},
		{"the root of the API", "/api/runner/", nil, http.StatusBadRequest, ""},
		{"an unknown route", "/api/runner/v1/jobs", nil, http.StatusNotFound, `{"error":"no such route of the runner API"}`},
		{"not a runner", "/api/runner/v1/claim", []string{"User-Agent", "curl/8.0"}, http.StatusBadRequest, ""},
		{"protocol 1", "/api/runner/v1/claim", nil, http.StatusNoContent, ""},
	} {
		status, body := send(tt.path, tt.headers...)
		if status != tt.status || (tt.answer != "" && body != tt.answer+"\n") {
			t.Errorf("%s = %d %s; want %d %s", tt.name, status, body, tt.status, tt.answer)
		}
	}
	r := request(http.MethodGet, "/api/runner/v1/claim", testHost, nil)
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Header.Set("Raun-Runner-Protocol", "1")
	r.Header.Set("User-Agent", "raun-runner/1.0.0")
	if resp := serve(w, r); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Errorf("GET claim = %d", resp.StatusCode)
	}
	// The release the runner says is recorded.
	if got, _ := w.store.Runner(context.Background(), a.id); got.Release != "1.0.0" {
		t.Errorf("the release recorded: %q", got.Release)
	}
	// A body that is not an empty object is refused.
	for _, body := range []string{"", "null", `{"x": 1}`, "{} {}"} {
		r := request(http.MethodPost, "/api/runner/v1/claim", testHost, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+a.token)
		r.Header.Set("Raun-Runner-Protocol", "1")
		r.Header.Set("User-Agent", "raun-runner/1.0.0")
		if resp := serve(w, r); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a claim with %q = %d", body, resp.StatusCode)
		}
	}
}

// TestACredentialThatCannotBeMintedGivesNothing: the forge down, the claim
// gives no job, and the job waits MintDelay.
func TestACredentialThatCannotBeMintedGivesNothing(t *testing.T) {
	w := newRunnerWorld(t, 10*time.Millisecond, nil)
	a := w.runner(t, "builder", "")
	j := w.enqueue(t)
	w.clones.SetDown(true)
	if status, _ := a.send("claim", "", "{}"); status != http.StatusNoContent {
		t.Fatalf("a claim with the forge down = %d", status)
	}
	stored, _ := w.store.Job(context.Background(), j.ID)
	if stored.State != job.StateQueued || !stored.NotBefore.Equal(w.clock.Now().Add(job.MintDelay)) {
		t.Errorf("the job: %+v", stored)
	}
	w.clones.SetDown(false)
	w.clock.Advance(job.MintDelay)
	a.claim()
}
