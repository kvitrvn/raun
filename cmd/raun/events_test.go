package main

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// post sends a delivery of the fake forge to the hook of s, as the forge
// does: no cookie, no CSRF token.
func post(t *testing.T, s *server, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+s.addr+"/hooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func push(repo, after string) repository.Delivery {
	return repository.Delivery{Event: "push", Kind: repository.KindPush, Repository: repo,
		Push: &repository.Push{Ref: "refs/heads/main", Before: strings.Repeat("a", 40), After: after, Head: after, Sender: repository.Sender{ID: "1001", Login: "alice"}}}
}

func pullRequest(action string) repository.Delivery {
	return repository.Delivery{Event: "pull_request", Action: action, Kind: repository.KindPullRequest, Repository: "2001",
		PullRequest: &repository.PullRequest{Number: 7, Action: action, HeadRef: "feature", HeadCommit: strings.Repeat("c", 40),
			BaseRef: "main", BaseCommit: strings.Repeat("a", 40), HeadRepository: "2001", BaseRepository: "2001"}}
}

var installation = repository.Delivery{Event: "installation_repositories", Action: "removed", Kind: repository.KindInstallation}

// eventRows counts the rows of the event log on the page of acme/api.
func eventRows(t *testing.T, v *visitor) int {
	t.Helper()
	_, _, page := v.send(http.MethodGet, "/alice/acme/api", nil)
	i := strings.Index(page, `data-slot="events"`)
	if i < 0 {
		t.Fatalf("no event log on the page: %s", page)
	}
	section := page[i:]
	section = section[:strings.Index(section, "</section>")]
	return strings.Count(section, `<tr data-slot="table-row"`) - strings.Count(section, "<thead")
}

// TestWebhooksThroughTheServer receives the deliveries of the fake forge
// through the server, on SQLite: a valid delivery, a refused signature,
// duplicates sequential and concurrent, a replayed body, a pull request
// closed and reopened at the same head and a push back to a commit, an
// oversized body, unknown and inactive repositories, an ignored event, an
// installation delivery and its replay, and the purge.
func TestWebhooksThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	synced := make(chan []string, 4)
	ticks := make(chan time.Time)
	s := start(t, dir, append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001"), serverDeps{
		forge: fake, missing: allCapabilities, purge: ticks,
		synced: func(lost []string, err error) {
			if err != nil {
				t.Errorf("sync: %v", err)
			}
			synced <- lost
		},
	})
	syncs := func() []string {
		t.Helper()
		select {
		case lost := <-synced:
			return lost
		case <-time.After(wait):
			t.Fatal("no sync of the reach of the App")
			return nil
		}
	}
	syncs() // the one at start
	alice := signedUp(t, s, fake, "1001", "alice")
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d %s", status, body)
	}

	h, b := fake.Deliver("d1", push("2001", strings.Repeat("b", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	if n := eventRows(t, alice); n != 1 {
		t.Fatalf("%d rows after a push", n)
	}
	// Refused: a wrong signature, and a body past the bound, before any
	// signature.
	if got := post(t, s, map[string]string{forgetest.HeaderSignature: strings.Repeat("0", 64), forgetest.HeaderDelivery: "d2"}, b); got != http.StatusUnauthorized {
		t.Errorf("a wrong signature = %d", got)
	}
	huge := bytes.Repeat([]byte("a"), 5<<20+1)
	if got := post(t, s, map[string]string{forgetest.HeaderSignature: forgetest.Sign(huge), forgetest.HeaderDelivery: "d3"}, huge); got != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body = %d", got)
	}
	// Sent again, then replayed under a new identifier: nothing more.
	if got := post(t, s, h, b); got != http.StatusOK {
		t.Errorf("a delivery sent again = %d", got)
	}
	replay := map[string]string{forgetest.HeaderSignature: h[forgetest.HeaderSignature], forgetest.HeaderDelivery: "d4"}
	if got := post(t, s, replay, b); got != http.StatusOK {
		t.Errorf("a body replayed under a new identifier = %d", got)
	}
	// The same delivery at once: one is recorded.
	h, b = fake.Deliver("d5", push("2001", strings.Repeat("c", 40)))
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = post(t, s, h, b)
		}()
	}
	wg.Wait()
	accepted := 0
	for _, c := range codes {
		switch c {
		case http.StatusAccepted:
			accepted++
		case http.StatusOK:
		default:
			t.Errorf("a concurrent duplicate = %d", c)
		}
	}
	if accepted != 1 {
		t.Errorf("%d concurrent duplicates recorded, want 1", accepted)
	}
	// Genuine deliveries that look alike are all kept: a pull request
	// closed and reopened at the same head, a push back to a commit.
	for i, d := range []repository.Delivery{pullRequest("closed"), pullRequest("reopened"), push("2001", strings.Repeat("b", 40))} {
		h, b := fake.Deliver("d"+string(rune('6'+i)), d)
		if got := post(t, s, h, b); got != http.StatusAccepted {
			t.Errorf("delivery %d = %d", i, got)
		}
	}
	if n := eventRows(t, alice); n != 5 {
		t.Errorf("%d rows, want 5", n)
	}
	// Unknown, never activated: dropped. Another event: logged as ignored.
	for i, d := range []repository.Delivery{push("9999", strings.Repeat("b", 40)), push("2002", strings.Repeat("b", 40))} {
		h, b := fake.Deliver("drop"+string(rune('a'+i)), d)
		if got := post(t, s, h, b); got != http.StatusAccepted {
			t.Errorf("a delivery of a repository that is not active = %d", got)
		}
	}
	h, b = fake.Deliver("d9", repository.Delivery{Event: "issues", Action: "opened", Kind: repository.KindIgnored, Repository: "2001"})
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Errorf("an ignored event = %d", got)
	}
	if _, _, page := alice.send(http.MethodGet, "/alice/acme/api", nil); eventRows(t, alice) != 6 || !strings.Contains(page, ">ignored<") {
		t.Errorf("the log after an ignored event: %d rows", eventRows(t, alice))
	}

	// The App loses acme/api: an installation delivery makes Raun ask the
	// forge, which deactivates it.
	fake.SetInstalled("2001", false)
	h, b = fake.Deliver("i1", installation)
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("an installation delivery = %d", got)
	}
	if lost := syncs(); len(lost) != 1 || lost[0] != "2001" {
		t.Fatalf("the sync lost %v, want 2001", lost)
	}
	if _, _, page := alice.send(http.MethodGet, "/alice/acme/api", nil); !strings.Contains(page, ">unreachable<") {
		t.Errorf("the page once the App lost it: %s", page)
	}
	// Installed again, the old delivery replayed changes nothing: no sync.
	fake.SetInstalled("2001", true)
	if got := post(t, s, map[string]string{forgetest.HeaderSignature: h[forgetest.HeaderSignature], forgetest.HeaderDelivery: "i2"}, b); got != http.StatusOK {
		t.Errorf("a replayed installation delivery = %d", got)
	}
	select {
	case lost := <-synced:
		t.Errorf("a replayed installation delivery made a sync: %v", lost)
	case <-time.After(100 * time.Millisecond):
	}
	// A deactivated repository logs nothing more.
	h, b = fake.Deliver("d10", push("2001", strings.Repeat("d", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted || eventRows(t, alice) != 6 {
		t.Errorf("a push to an unreachable repository = %d, %d rows", got, eventRows(t, alice))
	}

	// 31 days later, the log and the keys are gone: d1 counts as new.
	ticks <- time.Now().Add(31 * 24 * time.Hour)
	deadline := time.Now().Add(wait)
	for !strings.Contains(s.stderr.String(), "expired deliveries and events deleted") {
		if time.Now().After(deadline) {
			t.Fatal("the purge did nothing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Her session expired with them.
	alice.logIn(fake, "1001")
	if n := eventRows(t, alice); n != 0 {
		t.Errorf("%d rows after the purge", n)
	}
	h, b = fake.Deliver("d1", push("2001", strings.Repeat("b", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Errorf("a delivery past the window = %d", got)
	}
}
