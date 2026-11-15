package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
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
	if got := asRunner(t, s, token); got != http.StatusNotFound {
		t.Errorf("the runner on the API = %d, want through to a route that does not exist yet", got)
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
	if runner.LastContact.IsZero() {
		t.Error("the request of the runner left no last contact")
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
	if got := asRunner(t, s, fresh); got != http.StatusNotFound {
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
