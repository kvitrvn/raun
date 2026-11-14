package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/forgetest"
)

// configurationOf is the configuration section of the page of acme/api.
func configurationOf(t *testing.T, v *visitor) string {
	t.Helper()
	_, _, page := v.send(http.MethodGet, "/alice/acme/api", nil)
	i := strings.Index(page, `data-slot="configuration"`)
	if i < 0 {
		t.Fatalf("no configuration on the page: %s", page)
	}
	section := page[i:]
	return section[:strings.Index(section, "</section>")]
}

// TestConfigurationThroughTheServer reads .raun.yaml of acme/api on the fake
// forge: at activation, at the head of the default branch after a push to
// it, never after a push to another branch, and again at start.
func TestConfigurationThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	fake, _ := forgetest.Scenario("github", false)
	env := append(app(keyFile), "RAUN_SIGNUP_USERS=github:1001")
	refreshed := make(chan string, 8)
	deps := serverDeps{forge: fake, refreshed: func(id string, err error) {
		if err != nil {
			t.Errorf("read of %s: %v", id, err)
		}
		refreshed <- id
	}}
	reads := func() string {
		t.Helper()
		select {
		case id := <-refreshed:
			return id
		case <-time.After(wait):
			t.Fatal("no read of the configuration")
			return ""
		}
	}
	s := start(t, dir, env, deps)
	alice := signedUp(t, s, fake, "1001", "alice")
	// The fake lacks nothing: the repository is activated, and read at
	// once.
	if status, _, body := alice.submit("/", "/repositories", url.Values{"workspace": {"alice"}, "repository": {"2001"}}); status != http.StatusSeeOther {
		t.Fatalf("activation = %d %s", status, body)
	}
	if c := configurationOf(t, alice); !strings.Contains(c, ">configured<") || !strings.Contains(c, forgetest.FilesHead[:12]) {
		t.Fatalf("after the activation: %s", c)
	}

	// A push to main whose head has no file: not configured, read at the
	// head the forge gives, not at the commit of the payload.
	head := strings.Repeat("d", 40)
	fake.SetHead("2001", "main", head)
	h, b := fake.Deliver("d1", push("2001", strings.Repeat("9", 40)))
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push = %d", got)
	}
	if id := reads(); id != "2001" {
		t.Errorf("read of %s", id)
	}
	if c := configurationOf(t, alice); !strings.Contains(c, ">not configured<") || !strings.Contains(c, head[:12]) {
		t.Errorf("after a push without the file: %s", c)
	}
	// A push to another branch reads nothing.
	other := push("2001", strings.Repeat("e", 40))
	other.Push.Ref = "refs/heads/feature"
	h, b = fake.Deliver("d2", other)
	if got := post(t, s, h, b); got != http.StatusAccepted {
		t.Fatalf("a push to a branch = %d", got)
	}
	select {
	case id := <-refreshed:
		t.Errorf("a push to another branch read %s", id)
	case <-time.After(100 * time.Millisecond):
	}
	// A file past the bound is unreadable, and refused whole.
	big := strings.Repeat("f", 40)
	fake.SetHead("2001", "main", big)
	fake.SetFile("2001", big, ".raun.yaml", strings.Repeat("x", repository.MaxConfigurationSize+1))
	h, b = fake.Deliver("d3", push("2001", big))
	post(t, s, h, b)
	reads()
	if c := configurationOf(t, alice); !strings.Contains(c, ">unreadable<") || !strings.Contains(c, "larger than 64 KiB") {
		t.Errorf("after a push of a file too large: %s", c)
	}
	s.stop(t)

	// At start, every active repository is read again.
	fake.SetFile("2001", big, ".raun.yaml", "version: 1\n")
	s = start(t, dir, env, deps)
	if id := reads(); id != "2001" {
		t.Errorf("read at start of %s", id)
	}
	// Her session outlives the restart.
	alice.rebind(s)
	if c := configurationOf(t, alice); !strings.Contains(c, ">configured<") {
		t.Errorf("after a restart: %s", c)
	}
}
