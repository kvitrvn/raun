package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testHost = "raun.example"

// newTestServer returns a server for https://raun.example, whose logs go to
// the returned buffer as JSON, and whose readiness is ready.
func newTestServer(t *testing.T, ready func(context.Context) error) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	if ready == nil {
		ready = func(context.Context) error { return nil }
	}
	s, err := New(Options{
		Logger:     slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PublicHost: testHost,
		Scheme:     "https",
		Ready:      ready,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, &logs
}

// serve sends one request to s and returns the response.
func serve(s http.Handler, r *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec.Result()
}

func request(method, target, host string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = host
	return r
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// records decodes the JSON log lines of logs.
func records(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// find returns the log records whose msg is msg.
func find(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func TestNewRefusesBadOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	ready := func(context.Context) error { return nil }
	tests := []struct {
		name string
		opts Options
	}{
		{"no logger", Options{PublicHost: testHost, Scheme: "https", Ready: ready}},
		{"no readiness", Options{Logger: logger, PublicHost: testHost, Scheme: "https"}},
		{"no host", Options{Logger: logger, Scheme: "https", Ready: ready}},
		{"bad scheme", Options{Logger: logger, PublicHost: testHost, Scheme: "ftp", Ready: ready}},
		{"host not canonical", Options{Logger: logger, PublicHost: "Raun.Example:443", Scheme: "https", Ready: ready}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Fatal("New accepted the options")
			}
		})
	}
}

func TestHealth(t *testing.T) {
	notReady := errors.New("database at version 0 /secret/path")
	tests := []struct {
		name       string
		target     string
		ready      error
		stopping   bool
		wantStatus int
		wantBody   string
		wantCalled bool
	}{
		{"liveness", "/healthz", nil, false, http.StatusOK, "ok\n", false},
		{"liveness while not ready", "/healthz", notReady, false, http.StatusOK, "ok\n", false},
		{"liveness while stopping", "/healthz", nil, true, http.StatusOK, "ok\n", false},
		{"ready", "/readyz", nil, false, http.StatusOK, "ok\n", true},
		{"not ready", "/readyz", notReady, false, http.StatusServiceUnavailable, "unavailable\n", true},
		{"stopping", "/readyz", nil, true, http.StatusServiceUnavailable, "unavailable\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			s, logs := newTestServer(t, func(context.Context) error {
				called = true
				return tt.ready
			})
			if tt.stopping {
				s.BeginShutdown()
			}
			// A probe reaches the server by an address that is not the
			// public one: the health endpoints skip the Host check.
			for _, host := range []string{testHost, "10.0.0.7:8080"} {
				called = false
				resp := serve(s, request(http.MethodGet, tt.target, host, nil))
				if resp.StatusCode != tt.wantStatus {
					t.Errorf("Host %s: status = %d, want %d", host, resp.StatusCode, tt.wantStatus)
				}
				if body := readBody(t, resp); body != tt.wantBody {
					t.Errorf("Host %s: body = %q, want %q", host, body, tt.wantBody)
				}
				if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
					t.Errorf("Content-Type = %q", ct)
				}
				if called != tt.wantCalled {
					t.Errorf("Host %s: readiness called = %v, want %v", host, called, tt.wantCalled)
				}
			}
			// The cause of a failure is in the log, never in the body.
			if tt.ready != nil && tt.wantCalled {
				got := find(records(t, logs), "not ready")
				if len(got) != 2 || got[0]["error"] != notReady.Error() {
					t.Errorf("not ready records = %v", got)
				}
			}
		})
	}
}

func TestHealthRefusesOtherMethods(t *testing.T) {
	s, _ := newTestServer(t, nil)
	// Only GET and HEAD reach the health endpoints; any other method goes to
	// the application, which has no such route.
	resp := serve(s, request(http.MethodPost, "/healthz", testHost, nil))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /healthz = %d, want 404", resp.StatusCode)
	}
	resp = serve(s, request(http.MethodHead, "/readyz", testHost, nil))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD /readyz = %d, want 200", resp.StatusCode)
	}
}

func TestHostCheck(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		headers map[string]string
		allowed bool
	}{
		{"public host", "raun.example", nil, true},
		{"public host with its default port", "raun.example:443", nil, true},
		{"case differs", "RAUN.example", nil, true},
		{"fully qualified name", "raun.example.", nil, true},
		{"foreign host", "evil.example", nil, false},
		{"public host on another port", "raun.example:8443", nil, false},
		{"empty host", "", nil, false},
		{"malformed host", "raun.example:", nil, false},
		{"proxy headers are ignored", "evil.example", map[string]string{
			"X-Forwarded-Host":  "raun.example",
			"X-Forwarded-Proto": "https",
			"Forwarded":         "host=raun.example;proto=https",
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, logs := newTestServer(t, nil)
			called := false
			handle(s.app, "GET /page", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := request(http.MethodGet, "/page", tt.host, nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			resp := serve(s, r)
			if tt.allowed {
				if resp.StatusCode != http.StatusNoContent || !called {
					t.Fatalf("status = %d, called = %v; want 204 and the handler called", resp.StatusCode, called)
				}
				return
			}
			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Fatalf("status = %d, want 421", resp.StatusCode)
			}
			if called {
				t.Fatal("the handler ran for a refused Host")
			}
			refused := find(records(t, logs), "host refused")
			if len(refused) != 1 || refused[0]["host"] != tt.host || refused[0]["expected"] != testHost {
				t.Fatalf("host refused records = %v", refused)
			}
		})
	}
}

func TestHostCheckBoundsTheLoggedHost(t *testing.T) {
	s, logs := newTestServer(t, nil)
	serve(s, request(http.MethodGet, "/", strings.Repeat("é", 200), nil))
	refused := find(records(t, logs), "host refused")
	if len(refused) != 1 {
		t.Fatalf("host refused records = %v", refused)
	}
	host := refused[0]["host"].(string)
	if len(host) > maxLoggedHost+3 || !strings.HasSuffix(host, "...") || !strings.HasPrefix(host, "é") {
		t.Errorf("logged host = %q (%d bytes)", host, len(host))
	}
}

func TestBodyLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		chunked bool
		want    int
	}{
		{"at the bound", MaxBodyBytes, false, http.StatusNoContent},
		{"one byte over", MaxBodyBytes + 1, false, http.StatusRequestEntityTooLarge},
		{"chunked at the bound", MaxBodyBytes, true, http.StatusNoContent},
		{"chunked one byte over", MaxBodyBytes + 1, true, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t, nil)
			handle(s.app, "POST /upload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					if IsBodyTooLarge(err) {
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						return
					}
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			var body io.Reader = bytes.NewReader(make([]byte, tt.size))
			if tt.chunked {
				// A reader of unknown size: no Content-Length.
				body = io.MultiReader(body)
			}
			r := request(http.MethodPost, "/upload", testHost, body)
			if tt.chunked {
				r.ContentLength = -1
			}
			if resp := serve(s, r); resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestIsBodyTooLarge(t *testing.T) {
	if IsBodyTooLarge(errors.New("read")) || IsBodyTooLarge(nil) {
		t.Error("an unrelated error is reported as too large")
	}
	if !IsBodyTooLarge(&http.MaxBytesError{Limit: 1}) {
		t.Error("a MaxBytesError is not reported as too large")
	}
}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestAccessLog(t *testing.T) {
	s, logs := newTestServer(t, nil)
	handle(s.app, "GET /items/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("the handler sees no request identifier")
		}
		_, _ = io.WriteString(w, "hello")
	}))

	forged := "0123456789abcdef0123456789abcdef"
	r := request(http.MethodGet, "/items/req-ab12cd?q=private-search", testHost, nil)
	r.Header.Set("X-Request-Id", forged)
	r.RemoteAddr = "192.0.2.10:51000"
	first := serve(s, r)
	second := serve(s, request(http.MethodGet, "/missing", testHost, nil))

	id := first.Header.Get("X-Request-Id")
	if !hexID.MatchString(id) || id == forged {
		t.Fatalf("X-Request-Id = %q, want 32 new hex characters", id)
	}
	if other := second.Header.Get("X-Request-Id"); other == id || !hexID.MatchString(other) {
		t.Fatalf("second X-Request-Id = %q, first %q", other, id)
	}

	got := find(records(t, logs), "request")
	if len(got) != 2 {
		t.Fatalf("access records = %v", got)
	}
	want := map[string]any{
		"request_id": id,
		"method":     "GET",
		"route":      "GET /items/{id}",
		"status":     float64(200),
		"bytes":      float64(5),
		"remote":     "192.0.2.10:51000",
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("access record %s = %v, want %v", k, got[0][k], v)
		}
	}
	if _, ok := got[0]["duration"]; !ok {
		t.Error("access record has no duration")
	}
	if got[1]["route"] != "" || got[1]["status"] != float64(http.StatusNotFound) {
		t.Errorf("unmatched request record = %v", got[1])
	}
	for _, leak := range []string{"req-ab12cd", "private-search", forged} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log holds %q", leak)
		}
	}
}

func TestLogsNoRequestData(t *testing.T) {
	s, logs := newTestServer(t, nil)
	handle(s.app, "POST /form", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	r := request(http.MethodPost, "/form?token=query-sentinel", testHost, strings.NewReader("field=body-sentinel"))
	r.Header.Set("Authorization", "Bearer authorization-sentinel")
	r.Header.Set("Cookie", "__Host-raun-session=cookie-sentinel")
	r.Header.Set("User-Agent", "agent-sentinel")
	serve(s, r)
	for _, leak := range []string{"query-sentinel", "body-sentinel", "authorization-sentinel", "cookie-sentinel", "agent-sentinel"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log holds %q", leak)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	s, logs := newTestServer(t, nil)
	handle(s.app, "GET /value", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("panic-value-sentinel")
	}))
	handle(s.app, "GET /runtime", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var empty []int
		_ = empty[len(r.URL.Path)]
	}))

	resp := serve(s, request(http.MethodGet, "/value", testHost, nil))
	id := resp.Header.Get("X-Request-Id")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, id) || strings.Contains(body, "sentinel") {
		t.Errorf("body = %q, want the request identifier %s and no panic value", body, id)
	}
	serve(s, request(http.MethodGet, "/runtime", testHost, nil))

	recs := records(t, logs)
	panics := find(recs, "panic")
	if len(panics) != 2 {
		t.Fatalf("panic records = %v", panics)
	}
	if panics[0]["request_id"] != id || panics[0]["type"] != "string" || !strings.Contains(panics[0]["stack"].(string), "recoverPanics") {
		t.Errorf("panic record = %v", panics[0])
	}
	if err, _ := panics[1]["error"].(string); !strings.Contains(err, "index out of range") {
		t.Errorf("runtime panic record = %v, want its error", panics[1])
	}
	if strings.Contains(logs.String(), "panic-value-sentinel") {
		t.Error("the log holds the panic value")
	}
	access := find(recs, "request")
	if len(access) != 2 || access[0]["status"] != float64(500) || access[0]["route"] != "GET /value" {
		t.Errorf("access records = %v", access)
	}
}

func TestPanicAfterTheResponseStartedAbortsIt(t *testing.T) {
	s, logs := newTestServer(t, nil)
	handle(s.app, "GET /partial", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		panic("late")
	}))
	srv := httptest.NewUnstartedServer(s)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/partial", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testHost
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the 200 already sent", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("the client read %q as a complete body", body)
	}
	if string(body) != "partial" {
		t.Errorf("body read before the abort = %q", body)
	}
	// The panic and the request are both logged. Close waits for the
	// handler, whose deferred calls write them.
	srv.Close()
	recs := records(t, logs)
	if len(find(recs, "panic")) != 1 || len(find(recs, "request")) != 1 {
		t.Errorf("records = %v", recs)
	}
}

func TestReadinessHasADeadline(t *testing.T) {
	var left time.Duration
	s, _ := newTestServer(t, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("no deadline")
		}
		left = time.Until(deadline)
		return nil
	})
	if resp := serve(s, request(http.MethodGet, "/readyz", testHost, nil)); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if left <= 0 || left > ReadyTimeout {
		t.Errorf("readiness ran with %s left, want at most %s", left, ReadyTimeout)
	}
}

func TestAbortHandlerIsPassedOn(t *testing.T) {
	s, _ := newTestServer(t, nil)
	handle(s.app, "GET /abort", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	serve(s, request(http.MethodGet, "/abort", testHost, nil))
	t.Fatal("http.ErrAbortHandler was swallowed")
}

func TestCanonicalHost(t *testing.T) {
	tests := []struct {
		authority, scheme, want string
		wantErr                 bool
	}{
		{"raun.example", "https", "raun.example", false},
		{"raun.example:443", "https", "raun.example", false},
		{"raun.example:80", "http", "raun.example", false},
		{"raun.example:80", "https", "raun.example:80", false},
		{"Raun.Example:8443", "https", "raun.example:8443", false},
		{"localhost:8080", "http", "localhost:8080", false},
		{"127.0.0.1:8080", "http", "127.0.0.1:8080", false},
		{"[::1]:8080", "http", "[::1]:8080", false},
		{"[::1]", "http", "[::1]", false},
		{"[::1]:80", "http", "[::1]", false},
		{"raun.example.", "https", "raun.example", false},
		{"Raun.Example.:8443", "https", "raun.example:8443", false},
		{".", "https", "", true},
		{"raun.example..", "https", "", true},
		{"[2001:DB8::1]:443", "https", "[2001:db8::1]", false},
		{"", "https", "", true},
		{":8080", "http", "", true},
		{"raun.example:", "https", "", true},
		{"raun.example:0", "https", "", true},
		{"raun.example:65536", "https", "", true},
		{"raun.example:0443", "https", "", true},
		{"raun.example:http", "https", "", true},
		{"raun.example", "ftp", "", true},
		{"[not-an-ip]:80", "http", "", true},
		{"[127.0.0.1]:80", "http", "", true},
		{"[::1", "http", "", true},
		{"a b", "http", "", true},
	}
	for _, tt := range tests {
		got, err := CanonicalHost(tt.authority, tt.scheme)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("CanonicalHost(%q, %q) = %q, %v; want %q, error %v", tt.authority, tt.scheme, got, err, tt.want, tt.wantErr)
		}
	}
}
