package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/core/session"
)

// wait bounds every wait of these tests, so that a regression fails rather
// than hangs.
const wait = 10 * time.Second

// syncBuffer is a buffer the server's goroutines can write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// messages returns the msg of every JSON record in b.
func (b *syncBuffer) messages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		var rec struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec.Msg)
	}
	return out
}

// server is a raun server running in a goroutine of the test.
type server struct {
	addr   string
	scheme string
	stderr *syncBuffer
	cancel context.CancelFunc
	code   chan int
}

// start runs runServer on a loopback listener with a database in dir,
// migrated at start, and waits until it serves. env is added to that
// configuration and wins over it.
func start(t *testing.T, dir string, env []string, deps serverDeps) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	scheme := "http"
	for _, kv := range env {
		if strings.HasPrefix(kv, "RAUN_TLS_CERT_FILE=") {
			scheme = "https"
		}
	}
	environ := slices.Concat(env, []string{
		"RAUN_PUBLIC_URL=" + scheme + "://" + ln.Addr().String(),
		"RAUN_DATABASE_PATH=" + filepath.Join(dir, "raun.db"),
		"RAUN_DATABASE_MIGRATE=true",
	})
	ctx, cancel := context.WithCancel(context.Background())
	s := &server{addr: ln.Addr().String(), scheme: scheme, stderr: &syncBuffer{}, cancel: cancel, code: make(chan int, 1)}
	started := make(chan struct{})
	deps.listener = ln
	deps.started = func(net.Addr) { close(started) }
	go func() { s.code <- runServer(ctx, environ, s.stderr, deps) }()
	select {
	case <-started:
	case code := <-s.code:
		t.Fatalf("the server exited with %d before serving:\n%s", code, s.stderr)
	case <-time.After(wait):
		t.Fatal("the server did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.code:
		case <-time.After(wait):
			t.Error("the server did not stop")
		}
	})
	return s
}

// stop cancels the server's context and returns its exit code.
func (s *server) stop(t *testing.T) int {
	t.Helper()
	s.cancel()
	select {
	case code := <-s.code:
		s.code <- code // for the cleanup
		return code
	case <-time.After(wait):
		t.Fatal("the server did not stop")
		return -1
	}
}

// get sends a GET to path with the Host header host ("" for the public one).
func (s *server) get(t *testing.T, path, host string, header http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.scheme+"://"+s.addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{Timeout: wait, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestServerServes(t *testing.T) {
	s := start(t, t.TempDir(), nil, serverDeps{})

	if code, body := s.get(t, "/healthz", "", nil); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/healthz = %d %q", code, body)
	}
	if code, body := s.get(t, "/readyz", "", nil); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/readyz = %d %q", code, body)
	}
	if code, _ := s.get(t, "/", "evil.example", nil); code != http.StatusMisdirectedRequest {
		t.Errorf("foreign Host = %d, want 421", code)
	}
	code, page := s.get(t, "/", "", nil)
	if code != http.StatusOK || !strings.Contains(page, "<title>Repositories · Raun</title>") {
		t.Errorf("/ = %d %q", code, page)
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(assets) < 5 {
		t.Errorf("the page names %d assets", len(assets))
	}
	for _, m := range assets {
		if code, _ := s.get(t, m[1], "", nil); code != http.StatusOK {
			t.Errorf("%s = %d", m[1], code)
		}
	}
	if code, _ := s.get(t, "/readyz", "evil.example", nil); code != http.StatusOK {
		t.Errorf("/readyz with a foreign Host = %d, want 200", code)
	}
	sentinels := http.Header{
		"Authorization": {"Bearer authorization-sentinel"},
		"Cookie":        {"__Host-raun-session=cookie-sentinel"},
	}
	s.get(t, "/?q=query-sentinel", "", sentinels)

	if code := s.stop(t); code != exitOK {
		t.Fatalf("exit code = %d, want 0:\n%s", code, s.stderr)
	}
	logs := s.stderr.String()
	for _, leak := range []string{"authorization-sentinel", "cookie-sentinel", "query-sentinel"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log holds %q", leak)
		}
	}
	msgs := strings.Join(s.stderr.messages(t), ",")
	if !strings.HasPrefix(msgs, "configuration,serving,") || !strings.HasSuffix(msgs, "shutting down,stopped") {
		t.Errorf("log messages = %s", msgs)
	}
}

func TestServerRestartsOnAMigratedDatabase(t *testing.T) {
	dir := t.TempDir()
	if code := start(t, dir, nil, serverDeps{}).stop(t); code != exitOK {
		t.Fatalf("first run exit code = %d", code)
	}
	// The env given to start wins: the first value of a variable is kept.
	s := start(t, dir, []string{"RAUN_DATABASE_MIGRATE=false"}, serverDeps{})
	if code, _ := s.get(t, "/readyz", "", nil); code != http.StatusOK {
		t.Errorf("/readyz after a restart = %d", code)
	}
}

func TestServerStopsBeforeListening(t *testing.T) {
	tests := []struct {
		name     string
		env      func(dir string) []string
		want     string
		database bool
	}{
		{
			name:     "invalid configuration",
			env:      func(dir string) []string { return []string{"RAUN_LOG_LEVEL=verbose", "RAUN_FOO=1"} },
			want:     "RAUN_FOO: unknown variable\nRAUN_LOG_LEVEL:",
			database: false,
		},
		{
			name:     "schema older than the binary",
			env:      func(dir string) []string { return []string{"RAUN_DATABASE_MIGRATE=false"} },
			want:     "set RAUN_DATABASE_MIGRATE=true",
			database: true,
		},
		{
			name: "database in a missing directory",
			env: func(dir string) []string {
				return []string{"RAUN_DATABASE_PATH=" + filepath.Join(dir, "missing", "raun.db")}
			},
			want:     "RAUN_DATABASE_PATH",
			database: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			environ := append(tt.env(dir), "RAUN_DATABASE_PATH="+filepath.Join(dir, "raun.db"))
			var stderr syncBuffer
			started := false
			deps := serverDeps{started: func(net.Addr) { started = true }}
			code := runServer(context.Background(), environ, &stderr, deps)
			if code != exitFailure {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if started {
				t.Fatal("the server started listening")
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr = %s\nwant it to contain %q", stderr.String(), tt.want)
			}
			_, err := os.Stat(filepath.Join(dir, "raun.db"))
			if exists := err == nil; exists != tt.database {
				t.Errorf("database file exists = %v, want %v", exists, tt.database)
			}
		})
	}
}

// blocking returns a wrapper that holds requests to /block until release is
// closed, and signals entered when one arrives.
func blocking(entered chan<- struct{}, release <-chan struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/block" {
				next.ServeHTTP(w, r)
				return
			}
			entered <- struct{}{}
			<-release
			_, _ = io.WriteString(w, "released")
		})
	}
}

func TestShutdownWaitsForInFlightRequests(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	s := start(t, t.TempDir(), nil, serverDeps{wrap: blocking(entered, release)})

	type result struct {
		code int
		body string
		err  error
	}
	answered := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + s.addr + "/block")
		if err != nil {
			answered <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		answered <- result{resp.StatusCode, string(body), err}
	}()
	select {
	case <-entered:
	case <-time.After(wait):
		t.Fatal("the request did not arrive")
	}

	s.cancel()
	select {
	case code := <-s.code:
		t.Fatalf("the server exited with %d while a request was in flight", code)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case r := <-answered:
		if r.err != nil || r.code != http.StatusOK || r.body != "released" {
			t.Errorf("in-flight request = %d %q, %v", r.code, r.body, r.err)
		}
	case <-time.After(wait):
		t.Fatal("the in-flight request got no answer")
	}
	if code := s.stop(t); code != exitOK {
		t.Fatalf("exit code = %d, want 0:\n%s", code, s.stderr)
	}
}

func TestShutdownDeadline(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	s := start(t, t.TempDir(), []string{"RAUN_SHUTDOWN_TIMEOUT=1s"}, serverDeps{wrap: blocking(entered, release)})

	failed := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + s.addr + "/block")
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		failed <- err
	}()
	select {
	case <-entered:
	case <-time.After(wait):
		t.Fatal("the request did not arrive")
	}

	begin := time.Now()
	if code := s.stop(t); code != exitFailure {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if elapsed := time.Since(begin); elapsed < time.Second {
		t.Errorf("the server stopped after %s, before the deadline", elapsed)
	}
	select {
	case err := <-failed:
		if err == nil {
			t.Error("the dropped request got a complete answer")
		}
	case <-time.After(wait):
		t.Fatal("the dropped request did not fail")
	}
	if !strings.Contains(s.stderr.String(), "RAUN_SHUTDOWN_TIMEOUT") {
		t.Errorf("the log does not name the deadline:\n%s", s.stderr)
	}
}

func TestServerTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSerialPair(t, dir, 1)
	reload := make(chan os.Signal, 1)
	s := start(t, dir, []string{"RAUN_TLS_CERT_FILE=" + certFile, "RAUN_TLS_KEY_FILE=" + keyFile}, serverDeps{reload: reload})

	serial := func() int64 {
		conn, err := tls.Dial("tcp", s.addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		state := conn.ConnectionState()
		if state.Version < tls.VersionTLS12 {
			t.Errorf("TLS version %x", state.Version)
		}
		return state.PeerCertificates[0].SerialNumber.Int64()
	}
	if code, _ := s.get(t, "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("/healthz over TLS = %d", code)
	}
	if got := serial(); got != 1 {
		t.Fatalf("serial = %d, want 1", got)
	}
	if _, err := tls.Dial("tcp", s.addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		t.Error("a TLS 1.1 handshake succeeded")
	}

	writeSerialPair(t, dir, 2)
	reload <- syscall.SIGHUP
	deadline := time.Now().Add(wait)
	for serial() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("the reloaded pair is not served")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	reload <- syscall.SIGHUP
	for !strings.Contains(s.stderr.String(), "keeping the previous one") {
		if time.Now().After(deadline) {
			t.Fatal("the failed reload is not logged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := serial(); got != 2 {
		t.Fatalf("serial after a failed reload = %d, want 2", got)
	}
}

func TestServerBoundsHeaders(t *testing.T) {
	// MaxHeaderBytes is set on the http.Server: past 32 KiB, net/http answers
	// 431 before any handler.
	s := start(t, t.TempDir(), nil, serverDeps{})
	req, err := http.NewRequest(http.MethodGet, "http://"+s.addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Large", strings.Repeat("a", 64<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("64 KiB of headers = %d, want 431", resp.StatusCode)
	}
}

// TestServerPurgesExpiredSessions proves that expired sessions are deleted
// at start and on each tick of the purge, and live ones kept.
func TestServerPurgesExpiredSessions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hash := func(c string) string { return strings.Repeat(c, 64) }
	add := func(h string, created, seen time.Time) {
		t.Helper()
		if err := store.Sessions().Create(ctx, session.Session{Hash: h, CSRF: "t", CreatedAt: created, LastSeenAt: seen}); err != nil {
			t.Fatal(err)
		}
	}
	add(hash("a"), now.Add(-time.Hour), now.Add(-time.Minute))
	add(hash("b"), now.Add(-10*24*time.Hour), now.Add(-8*24*time.Hour))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	ticks := make(chan time.Time)
	s := start(t, dir, nil, serverDeps{purge: ticks})
	if !strings.Contains(s.stderr.String(), `"msg":"expired sessions deleted","count":1`) {
		t.Errorf("the purge at start is not logged:\n%s", s.stderr)
	}
	// The next tick, 31 days later, ends the live one too.
	ticks <- now.Add(31 * 24 * time.Hour)
	deadline := time.Now().Add(wait)
	for strings.Count(s.stderr.String(), "expired sessions deleted") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the purge tick did nothing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := s.stop(t); code != exitOK {
		t.Fatalf("exit code = %d", code)
	}

	store, err = sqlite.Open(ctx, filepath.Join(dir, "raun.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, h := range []string{hash("a"), hash("b")} {
		if _, err := store.Sessions().Get(ctx, h); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("session %s… kept: %v", h[:4], err)
		}
	}
}
