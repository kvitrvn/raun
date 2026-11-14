package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// stubHooks answers as told and keeps the bodies it received.
type stubHooks struct {
	mu      sync.Mutex
	outcome repository.Outcome
	err     error
	bodies  [][]byte
	headers []string
}

func (h *stubHooks) Receive(_ context.Context, header func(string) string, body []byte) (repository.Outcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bodies = append(h.bodies, body)
	h.headers = append(h.headers, header("X-Hub-Signature-256"))
	if h.outcome == "" && h.err == nil {
		return repository.OutcomeRecorded, nil
	}
	return h.outcome, h.err
}

func (h *stubHooks) received() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

// deliver posts body to the hook as GitHub does: no cookie, no CSRF token,
// no origin of the instance.
func deliver(l *loginServer, path, host string, body []byte) *http.Response {
	r := request(http.MethodPost, path, host, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Hub-Signature-256", "sha256=signature-sentinel")
	r.Header.Set("Origin", "https://github.com")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	return serve(l, r)
}

func TestHookSkipsTheProtectionsOfTheApplication(t *testing.T) {
	l := newLoginServer(t)
	resp := deliver(l, "/hooks/github", testHost, []byte(`{"payload-sentinel":1}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a delivery = %d %s", resp.StatusCode, readBody(t, resp))
	}
	if l.hooks.received() != 1 || l.hooks.headers[0] != "sha256=signature-sentinel" {
		t.Errorf("the receiver got %d deliveries, %v", l.hooks.received(), l.hooks.headers)
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Request-Id") == "" {
		t.Errorf("headers = %v", resp.Header)
	}
	// The Host check stays.
	if resp := deliver(l, "/hooks/github", "evil.example", []byte(`{}`)); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("a foreign Host = %d", resp.StatusCode)
	}
	// Neighbours of the hook are the application, with its protections.
	for _, path := range []string{"/hooks/gitlab", "/hooks/github/x", "/hooks"} {
		if resp := deliver(l, path, testHost, []byte(`{}`)); resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403 from the protections", path, resp.StatusCode)
		}
	}
	if resp := serve(l, request(http.MethodGet, "/hooks/github", testHost, nil)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /hooks/github = %d, want 404", resp.StatusCode)
	}
	if l.hooks.received() != 1 {
		t.Errorf("the receiver got %d deliveries", l.hooks.received())
	}
	logs := l.logs.String()
	if strings.Contains(logs, "payload-sentinel") || strings.Contains(logs, "signature-sentinel") {
		t.Error("the log holds the payload or the signature")
	}
	if !strings.Contains(logs, `"route":"POST /hooks/github"`) || !strings.Contains(logs, `"outcome":"recorded"`) {
		t.Errorf("the log does not name the route and the outcome: %s", logs)
	}
}

func TestHookBoundsItsBody(t *testing.T) {
	l := newLoginServer(t)
	// Past the bound of the application, within that of the hook.
	big := bytes.Repeat([]byte("a"), MaxBodyBytes+1)
	if resp := deliver(l, "/hooks/github", testHost, big); resp.StatusCode != http.StatusAccepted {
		t.Errorf("a delivery of %d bytes = %d", len(big), resp.StatusCode)
	}
	exact := bytes.Repeat([]byte("a"), MaxHookBytes)
	if resp := deliver(l, "/hooks/github", testHost, exact); resp.StatusCode != http.StatusAccepted {
		t.Errorf("a delivery of exactly the bound = %d", resp.StatusCode)
	}
	if n := l.hooks.received(); n != 2 || len(l.hooks.bodies[1]) != MaxHookBytes {
		t.Fatalf("%d deliveries received", n)
	}
	// One byte more is refused before anything reads its signature.
	if resp := deliver(l, "/hooks/github", testHost, append(exact, 'a')); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a delivery past the bound = %d, want 413", resp.StatusCode)
	}
	if l.hooks.received() != 2 {
		t.Error("a delivery past the bound reached the receiver")
	}
}

func TestHookAnswers(t *testing.T) {
	for _, tc := range []struct {
		outcome repository.Outcome
		err     error
		status  int
	}{
		{repository.OutcomeRecorded, nil, http.StatusAccepted},
		{repository.OutcomeIgnored, nil, http.StatusAccepted},
		{repository.OutcomeDropped, nil, http.StatusAccepted},
		{repository.OutcomeInstallation, nil, http.StatusAccepted},
		{repository.OutcomeDuplicate, nil, http.StatusOK},
		{"", repository.ErrSignature, http.StatusUnauthorized},
		{"", repository.ErrMalformed, http.StatusBadRequest},
		{"", errors.New("disk full"), http.StatusInternalServerError},
	} {
		l := newLoginServer(t)
		l.hooks.outcome, l.hooks.err = tc.outcome, tc.err
		if resp := deliver(l, "/hooks/github", testHost, []byte(`{}`)); resp.StatusCode != tc.status {
			t.Errorf("%v %v = %d, want %d", tc.outcome, tc.err, resp.StatusCode, tc.status)
		}
	}
	// Every refused signature is answered alike.
	var bodies []string
	for range 2 {
		l := newLoginServer(t)
		l.hooks.err = repository.ErrSignature
		bodies = append(bodies, readBody(t, deliver(l, "/hooks/github", testHost, []byte(`{}`))))
	}
	if bodies[0] != bodies[1] || bodies[0] != "signature refused\n" {
		t.Errorf("refusals = %q", bodies)
	}
	// A malformed delivery logs no detail, which may quote the payload.
	l := newLoginServer(t)
	l.hooks.err = errors.Join(repository.ErrMalformed, errors.New("payload-sentinel"))
	deliver(l, "/hooks/github", testHost, []byte(`{}`))
	if strings.Contains(l.logs.String(), "payload-sentinel") {
		t.Error("the log holds the detail of a malformed delivery")
	}
}

func TestNoHookWithoutAReceiver(t *testing.T) {
	s, _ := newTestServer(t, nil)
	r := request(http.MethodPost, "/hooks/github", testHost, strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://github.com")
	if resp := serve(s, r); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /hooks/github without a receiver = %d, want 403", resp.StatusCode)
	}
	if _, err := New(Options{Logger: s.logger, PublicHost: testHost, Scheme: "https", Ready: s.ready, Sessions: s.sessions, Hooks: &stubHooks{}, ForgeName: "{forge}"}); err == nil {
		t.Error("a hook with a wildcard for the forge")
	}
}
