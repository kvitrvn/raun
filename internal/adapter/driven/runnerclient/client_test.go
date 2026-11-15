package runnerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
)

const (
	token = "runner-token-sentinel"
	lease = "lease-token-sentinel"
)

var run = strings.Repeat("a", 32)

// seen is a request the scripted server received.
type seen struct {
	path    string
	headers http.Header
	body    string
}

// script serves answers by path, and records the requests.
type script struct {
	mu      sync.Mutex
	answers map[string]func(w http.ResponseWriter)
	seen    []seen
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.seen = append(s.seen, seen{path: r.URL.Path, headers: r.Header.Clone(), body: string(b)})
	answer := s.answers[r.URL.Path]
	s.mu.Unlock()
	if answer == nil {
		http.Error(w, "no", http.StatusNotFound)
		return
	}
	answer(w)
}

func reply(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func newClient(t *testing.T, answers map[string]func(http.ResponseWriter)) (*Client, *script) {
	t.Helper()
	s := &script{answers: answers}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := New(Options{Server: srv.URL, Token: token, Release: "1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

const assignment = `{"run":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","lease":{"token":"lease-token-sentinel","expires":"2026-11-14T15:01:00Z"},
"deadline":"2026-11-14T15:05:00Z","job":{"kind":"noop","repository":{"forge":"github","id":"2001","full_name":"acme/api"},
"commit":"9fceb02d0ae598e95dc970b74767f19372d61af8","ref":"refs/heads/main","params":{}},
"clone":{"url":"https://github.com/acme/api.git","username":"x-access-token","password":"clone-sentinel","expires":"2026-11-14T16:00:00Z"}}`

func TestClaim(t *testing.T) {
	ctx := context.Background()
	c, s := newClient(t, map[string]func(http.ResponseWriter){"/api/runner/v1/claim": reply(http.StatusOK, assignment)})
	w, err := c.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w.Run != run || w.Lease.Reveal() != lease || w.Kind != job.KindNoop || w.Repository != "2001" || w.FullName != "acme/api" ||
		w.Commit != "9fceb02d0ae598e95dc970b74767f19372d61af8" || w.Clone.Password.Reveal() != "clone-sentinel" ||
		w.Clone.URL != "https://github.com/acme/api.git" || !w.Deadline.Equal(time.Date(2026, 11, 14, 15, 5, 0, 0, time.UTC)) {
		t.Errorf("Claim = %+v", w)
	}
	got := s.seen[0]
	if got.headers.Get("Authorization") != "Bearer "+token || got.headers.Get("Raun-Runner-Protocol") != "1" ||
		got.headers.Get("User-Agent") != "raun-runner/1.2.0" || got.headers.Get("Raun-Lease") != "" || got.body != "{}" {
		t.Errorf("the claim sent %+v", got)
	}

	for name, tt := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"no job":          {http.StatusNoContent, "", job.ErrNoJob},
		"revoked":         {http.StatusUnauthorized, `{"error":"unauthorized"}`, job.ErrUnauthenticated},
		"another version": {http.StatusBadRequest, `{"error":"protocol 2 is not spoken here; this server speaks 1"}`, job.ErrProtocol},
	} {
		c, _ := newClient(t, map[string]func(http.ResponseWriter){"/api/runner/v1/claim": reply(tt.status, tt.body)})
		if _, err := c.Claim(ctx); !errors.Is(err, tt.want) {
			t.Errorf("%s: Claim = %v, want %v", name, err, tt.want)
		}
	}
	for name, body := range map[string]string{
		"an unknown field":  strings.Replace(assignment, `"run"`, `"other":1,"run"`, 1),
		"no run":            strings.Replace(assignment, `"run":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",`, "", 1),
		"no clone password": strings.Replace(assignment, `"password":"clone-sentinel",`, "", 1),
		"not JSON":          "<html>",
		"too large":         `{"run":"` + strings.Repeat("a", maxAnswer) + `"}`,
	} {
		c, _ := newClient(t, map[string]func(http.ResponseWriter){"/api/runner/v1/claim": reply(http.StatusOK, body)})
		_, err := c.Claim(ctx)
		if err == nil || errors.Is(err, job.ErrNoJob) {
			t.Errorf("%s: Claim = %v", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "clone-sentinel")) {
			t.Errorf("%s: the error holds a secret: %v", name, err)
		}
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusNotFound} {
		c, _ := newClient(t, map[string]func(http.ResponseWriter){"/api/runner/v1/claim": reply(status, "")})
		if _, err := c.Claim(ctx); err == nil || errors.Is(err, job.ErrProtocol) || errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("a claim answered %d = %v, want a transient failure", status, err)
		}
	}
}

func TestTheRoutesOfARun(t *testing.T) {
	ctx := context.Background()
	base := "/api/runner/v1/runs/" + run
	c, s := newClient(t, map[string]func(http.ResponseWriter){
		base + "/heartbeat": reply(http.StatusOK, `{"expires":"2026-11-14T15:02:00Z","cancel":false}`),
		base + "/log":       reply(http.StatusNoContent, ""),
		base + "/result":    reply(http.StatusNoContent, ""),
	})
	r, err := c.Heartbeat(ctx, run, lease)
	if err != nil || r.Cancel || !r.Expires.Equal(time.Date(2026, 11, 14, 15, 2, 0, 0, time.UTC)) {
		t.Errorf("Heartbeat = %+v, %v", r, err)
	}
	if err := c.AppendLog(ctx, run, lease, 3, "fetching\n"); err != nil {
		t.Error(err)
	}
	res := job.Result{Outcome: job.OutcomeSucceeded, Summary: "nothing to do"}
	for range 2 {
		if err := c.Report(ctx, run, lease, res); err != nil {
			t.Error(err)
		}
	}
	if len(s.seen) != 4 {
		t.Fatalf("%d requests", len(s.seen))
	}
	for _, r := range s.seen {
		if r.headers.Get("Raun-Lease") != lease {
			t.Errorf("%s without its lease", r.path)
		}
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(s.seen[1].body), &chunk); err != nil || chunk["seq"] != 3.0 || chunk["text"] != "fetching\n" {
		t.Errorf("the chunk sent %s", s.seen[1].body)
	}
	if s.seen[2].body != s.seen[3].body || s.seen[2].body != `{"outcome":"succeeded","summary":"nothing to do"}` {
		t.Errorf("results sent %q and %q", s.seen[2].body, s.seen[3].body)
	}
	if _, err := c.Heartbeat(ctx, "../claim", lease); err == nil {
		t.Error("a run that is not an identifier was sent")
	}
}

func TestTheRefusalsOfARun(t *testing.T) {
	ctx := context.Background()
	base := "/api/runner/v1/runs/" + run
	for name, tt := range map[string]struct {
		path, body string
		status     int
		want       error
	}{
		"cancelled heartbeat": {"/heartbeat", `{"cancel":true}`, http.StatusOK, nil},
		"stale heartbeat":     {"/heartbeat", `{"error":"the run is no longer current"}`, http.StatusConflict, job.ErrStaleLease},
		"a gap":               {"/log", `{"error":"chunk 2 is expected","expected":2}`, http.StatusConflict, job.ErrLogSequence},
		"cancelled log":       {"/log", `{"error":"the job was cancelled"}`, http.StatusConflict, job.ErrCancelled},
		"stale log":           {"/log", `{"error":"the run is no longer current"}`, http.StatusConflict, job.ErrStaleLease},
		"another result":      {"/result", `{"error":"the run already reported another result"}`, http.StatusConflict, job.ErrReported},
		"a refused result":    {"/result", `{"error":"invalid result: x"}`, http.StatusUnprocessableEntity, job.ErrInvalidResult},
		"revoked":             {"/result", `{"error":"unauthorized"}`, http.StatusUnauthorized, job.ErrUnauthenticated},
	} {
		c, _ := newClient(t, map[string]func(http.ResponseWriter){base + tt.path: reply(tt.status, tt.body)})
		var err error
		switch tt.path {
		case "/heartbeat":
			var r job.Renewal
			r, err = c.Heartbeat(ctx, run, lease)
			if tt.want == nil && !r.Cancel {
				t.Errorf("%s: not cancelled", name)
			}
		case "/log":
			err = c.AppendLog(ctx, run, lease, 3, "x")
		default:
			err = c.Report(ctx, run, lease, job.Result{Outcome: job.OutcomeSucceeded})
		}
		if !errors.Is(err, tt.want) && !(tt.want == nil && err == nil) {
			t.Errorf("%s = %v, want %v", name, err, tt.want)
		}
		var gap *job.SequenceError
		if tt.want == job.ErrLogSequence && (!errors.As(err, &gap) || gap.Expected != 2) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), lease) {
			t.Errorf("%s: the error holds the lease", name)
		}
	}
}

func TestNoRedirectIsFollowed(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer elsewhere.Close()
	c, _ := newClient(t, map[string]func(http.ResponseWriter){"/api/runner/v1/claim": func(w http.ResponseWriter) {
		w.Header().Set("Location", elsewhere.URL+"/api/runner/v1/claim")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}})
	if _, err := c.Claim(context.Background()); err == nil || errors.Is(err, job.ErrNoJob) {
		t.Errorf("Claim after a redirect = %v", err)
	}
	if reached {
		t.Error("the token followed a redirect")
	}
}

func TestNew(t *testing.T) {
	for name, o := range map[string]Options{
		"no server":   {Token: token, Release: "1"},
		"ftp":         {Server: "ftp://x", Token: token, Release: "1"},
		"a user":      {Server: "https://u:p@x", Token: token, Release: "1"},
		"no token":    {Server: "https://x", Release: "1"},
		"bad release": {Server: "https://x", Token: token, Release: "1 2"},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("New with %s succeeded", name)
		}
	}
}
