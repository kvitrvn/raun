package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// scripted is a TLS server that answers each request with the next of its
// answers, the last one repeated.
type scripted struct {
	srv   *httptest.Server
	mu    sync.Mutex
	steps []func(w http.ResponseWriter, r *http.Request)
	calls int
}

func newScripted(t *testing.T, steps ...func(w http.ResponseWriter, r *http.Request)) *scripted {
	t.Helper()
	s := &scripted{steps: steps}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		step := s.steps[min(s.calls, len(s.steps)-1)]
		s.calls++
		s.mu.Unlock()
		step(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// answer writes status, the headers of kv pairs and body.
func answer(status int, body string, kv ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(kv); i += 2 {
			w.Header().Set(kv[i], kv[i+1])
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// startUnix is the time of a scripted App, in Unix seconds.
const startUnix = 1793095200

func startTime() time.Time { return time.Unix(startUnix, 0).UTC() }

// scriptedApp is an App on s, at startTime, that records its waits.
func scriptedApp(t *testing.T, s *scripted, waits *[]time.Duration) *App {
	t.Helper()
	a, err := New(Options{
		WebURL: s.srv.URL, AppID: 42, ClientID: "Iv1", ClientSecret: "client-secret-sentinel",
		PrivateKey: testKey(t), WebhookSecret: "webhook-secret-sentinel", CallbackURL: "https://raun.example/login/callback",
		HTTPClient: s.srv.Client(),
		Now:        startTime,
		Wait: func(ctx context.Context, d time.Duration) error {
			*waits = append(*waits, d)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDoRetries(t *testing.T) {
	ok := answer(http.StatusOK, `{}`)
	reset := strconv.FormatInt(startTime().Add(5*time.Second).Unix(), 10)
	late := strconv.FormatInt(startTime().Add(time.Hour).Unix(), 10)
	for _, tc := range []struct {
		name  string
		steps []func(http.ResponseWriter, *http.Request)
		retry bool
		calls int
		waits []time.Duration
		err   error // nil: a 200 comes back
	}{
		{"429 with Retry-After", []func(http.ResponseWriter, *http.Request){answer(429, "", "Retry-After", "2"), ok}, true, 2, []time.Duration{2 * time.Second}, nil},
		{"spent quota until the reset", []func(http.ResponseWriter, *http.Request){answer(403, "", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset), ok}, true, 2, []time.Duration{6 * time.Second}, nil},
		{"secondary limit with Retry-After", []func(http.ResponseWriter, *http.Request){answer(403, "", "Retry-After", "3"), ok}, true, 2, []time.Duration{3 * time.Second}, nil},
		{"reset beyond the budget", []func(http.ResponseWriter, *http.Request){answer(403, "", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", late)}, true, 1, nil, identity.ErrUnavailable},
		{"secondary limit without Retry-After", []func(http.ResponseWriter, *http.Request){answer(429, "")}, true, 1, nil, identity.ErrUnavailable},
		{"server error, then success", []func(http.ResponseWriter, *http.Request){answer(503, ""), ok}, true, 2, []time.Duration{backoff}, nil},
		{"server errors until the attempts are spent", []func(http.ResponseWriter, *http.Request){answer(502, "")}, true, maxAttempts, []time.Duration{backoff, 2 * backoff}, identity.ErrUnavailable},
		{"waits over the budget", []func(http.ResponseWriter, *http.Request){answer(429, "", "Retry-After", "9"), answer(429, "", "Retry-After", "9")}, true, 2, []time.Duration{9 * time.Second}, identity.ErrUnavailable},
		{"a request that consumes a value is sent once", []func(http.ResponseWriter, *http.Request){answer(503, ""), ok}, false, 1, nil, identity.ErrUnavailable},
		{"a rate limit of such a request is not retried either", []func(http.ResponseWriter, *http.Request){answer(429, "", "Retry-After", "1"), ok}, false, 1, nil, identity.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScripted(t, tc.steps...)
			var waits []time.Duration
			a := scriptedApp(t, s, &waits)
			req := a.apiGet("/x", nil, "Bearer token-sentinel")
			req.retry = tc.retry
			r, err := a.do(context.Background(), req)
			if tc.err == nil && (err != nil || r.status != http.StatusOK) {
				t.Fatalf("do = %+v, %v; want a 200", r, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("do = %+v, %v; want %v", r, err, tc.err)
			}
			if s.count() != tc.calls || !slices.Equal(waits, tc.waits) {
				t.Errorf("%d calls and waits %v, want %d and %v", s.count(), waits, tc.calls, tc.waits)
			}
			if err != nil && strings.Contains(err.Error(), "sentinel") {
				t.Errorf("error %q holds a secret", err)
			}
		})
	}
}

func TestDoGivesBackOtherStatuses(t *testing.T) {
	for _, code := range []int{401, 403, 404, 422} {
		s := newScripted(t, answer(code, `{"message":"token-sentinel"}`))
		var waits []time.Duration
		a := scriptedApp(t, s, &waits)
		req := a.apiGet("/x", nil, "")
		r, err := a.do(context.Background(), req)
		if err != nil || r.status != code || s.count() != 1 {
			t.Errorf("do with %d = %+v, %v after %d calls", code, r, err, s.count())
			continue
		}
		want := identity.ErrUnavailable
		if code == 401 {
			want = identity.ErrUnauthorized
		}
		if err := status(req, r); !errors.Is(err, want) || strings.Contains(err.Error(), "sentinel") {
			t.Errorf("status(%d) = %v, want %v without the body", code, err, want)
		}
	}
}

func TestDoStopsWithItsContext(t *testing.T) {
	s := newScripted(t, answer(503, ""))
	var waits []time.Duration
	a := scriptedApp(t, s, &waits)
	ctx, cancel := context.WithCancel(context.Background())
	a.wait = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	_, err := a.do(ctx, a.apiGet("/x", nil, ""))
	if !errors.Is(err, identity.ErrUnavailable) || !errors.Is(err, context.Canceled) || s.count() != 1 {
		t.Errorf("do = %v after %d calls, want ErrUnavailable and context.Canceled after 1", err, s.count())
	}
}

func TestDoRefusesARedirect(t *testing.T) {
	other := newScripted(t, answer(http.StatusOK, `{}`))
	s := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/stolen", http.StatusFound)
	})
	var waits []time.Duration
	a := scriptedApp(t, s, &waits)
	req := a.apiGet("/x", nil, "Bearer token-sentinel")
	r, err := a.do(context.Background(), req)
	if err != nil || r.status != http.StatusFound || other.count() != 0 {
		t.Fatalf("do = %+v, %v; the redirect reached %d calls", r, err, other.count())
	}
	if err := status(req, r); !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("status(302) = %v", err)
	}
}

func TestDoBoundsTheBody(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxBody+1)))
	})
	var waits []time.Duration
	a := scriptedApp(t, s, &waits)
	if _, err := a.do(context.Background(), a.apiGet("/x", nil, "")); !errors.Is(err, identity.ErrUnavailable) || s.count() != 1 {
		t.Errorf("do of an oversized body = %v after %d calls", err, s.count())
	}
}

func TestDecode(t *testing.T) {
	req := request{method: "GET", path: "/x"}
	var v struct {
		ID int64 `json:"id"`
	}
	for body, ok := range map[string]bool{
		`{"id": 9007199254740993, "added": "ignored"}`: true,
		`{"id": 1}  `:  true,
		`{"id": 1} {}`: false,
		`{"id": 1`:     false,
		`{"id": "1"}`:  false,
		`{"id": 1.5}`:  false,
		``:             false,
	} {
		err := decode(req, &response{body: []byte(body)}, &v)
		if (err == nil) != ok {
			t.Errorf("decode(%q) = %v, want ok %v", body, err, ok)
		}
		if err != nil && !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("decode(%q) = %v, want ErrUnavailable", body, err)
		}
	}
	if err := decode(req, &response{body: []byte(`{"id": 9007199254740993}`)}, &v); err != nil || v.ID != 9007199254740993 {
		t.Errorf("a large identifier decodes to %d, %v", v.ID, err)
	}
}

func TestPagesStopsAtTheBound(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != strconv.Itoa(perPage) {
			http.Error(w, "per_page", http.StatusBadRequest)
			return
		}
		items := make([]string, perPage)
		for i := range items {
			items[i] = fmt.Sprintf("%q", "x")
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	})
	var waits []time.Duration
	a := scriptedApp(t, s, &waits)
	err := a.pages(context.Background(), "/x", url.Values{"state": {"active"}}, "", func(req request, r *response, _ bool) (int, error) {
		var out []string
		if err := decode(req, r, &out); err != nil {
			return 0, err
		}
		return len(out), nil
	})
	if !errors.Is(err, identity.ErrUnavailable) || s.count() != maxPages+1 {
		t.Errorf("pages = %v after %d calls, want ErrUnavailable after %d", err, s.count(), maxPages+1)
	}
}

// TestPagesTakesExactlyTheBound: a listing of exactly maxPages full pages is
// whole; the page past the bound is given to the caller as a probe only.
func TestPagesTakesExactlyTheBound(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		if n, _ := strconv.Atoi(r.URL.Query().Get("page")); n > maxPages {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte("[" + strings.TrimSuffix(strings.Repeat(`"x",`, perPage), ",") + "]"))
	})
	var waits []time.Duration
	a := scriptedApp(t, s, &waits)
	pages, probes := 0, 0
	err := a.pages(context.Background(), "/x", nil, "", func(req request, r *response, probe bool) (int, error) {
		if probe {
			probes++
		} else {
			pages++
		}
		var out []string
		if err := decode(req, r, &out); err != nil {
			return 0, err
		}
		return len(out), nil
	})
	if err != nil || pages != maxPages || probes != 1 || s.count() != maxPages+1 {
		t.Errorf("pages = %v, %d pages and %d probes given, %d calls", err, pages, probes, s.count())
	}
}
