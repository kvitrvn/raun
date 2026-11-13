package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// adminSwitch is an administrator checker whose answer a test changes.
type adminSwitch struct {
	mu     sync.Mutex
	admins map[string]bool
	err    error
}

func (a *adminSwitch) IsAdministrator(_ context.Context, account string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admins[account], a.err
}

func (a *adminSwitch) set(account string, admin bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admins == nil {
		a.admins = map[string]bool{}
	}
	a.admins[account] = admin
	a.err = err
}

// newAdminServer is a login server with an Instance settings screen.
func newAdminServer(t *testing.T) (*loginServer, *adminSwitch) {
	t.Helper()
	l := newLoginServer(t)
	admins := &adminSwitch{}
	l.Server.administrators = admins
	l.Server.settings = []Setting{{Name: "RAUN_HTTP_ADDR", Value: ":8080"}, {Name: "RAUN_SECRET_KEY", Value: "[redacted]"}}
	l.Server.instanceRoutes()
	return l, admins
}

func TestInstanceSettings(t *testing.T) {
	l, admins := newAdminServer(t)
	visitor := l.browser(t)
	if got := location(t, visitor.do(http.MethodGet, "/instance", nil), http.StatusSeeOther); got != "/login?return=%2Finstance" {
		t.Errorf("a visitor went to %q", got)
	}
	alice := l.signUp(t, "1001", "alice")
	account := alice.session(l.sessions).Account

	resp := alice.do(http.MethodGet, "/instance", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "RAUN_HTTP_ADDR") || strings.Contains(body, `href="/instance"`) {
		t.Errorf("a non-administrator = %d %s", resp.StatusCode, body)
	}

	admins.set(account, true, nil)
	resp = alice.do(http.MethodGet, "/instance", nil)
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "RAUN_HTTP_ADDR") || !strings.Contains(body, "[redacted]") ||
		resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("an administrator = %d %s", resp.StatusCode, body)
	}
	if home := readBody(t, alice.do(http.MethodGet, "/", nil)); !strings.Contains(home, `href="/instance"`) {
		t.Error("the top bar of an administrator has no link to the settings")
	}

	// Removed from the list: the same session loses the screen and the
	// link at its next request.
	admins.set(account, false, nil)
	if resp := alice.do(http.MethodGet, "/instance", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a removed administrator = %d", resp.StatusCode)
	}
	if home := readBody(t, alice.do(http.MethodGet, "/", nil)); strings.Contains(home, `href="/instance"`) {
		t.Error("a removed administrator still has the link")
	}

	// A role that cannot be established is refused, and the pages render.
	admins.set(account, true, errors.New("database locked"))
	if resp := alice.do(http.MethodGet, "/instance", nil); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a failing check = %d", resp.StatusCode)
	}
	if resp := alice.do(http.MethodGet, "/", nil); resp.StatusCode != http.StatusOK || strings.Contains(readBody(t, resp), `href="/instance"`) {
		t.Error("a failing check broke the home page or showed the link")
	}

	admins.set(account, true, nil)
	if resp := alice.do(http.MethodHead, "/instance", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD /instance = %d", resp.StatusCode)
	}
	if resp := alice.post(l, "/instance", nil); resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /instance = %d, want it not served", resp.StatusCode)
	}
}

func TestNoInstanceScreenWithoutAdministrators(t *testing.T) {
	l := newLoginServer(t)
	alice := l.signUp(t, "1001", "alice")
	if resp := alice.do(http.MethodGet, "/instance", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/instance without administrators = %d", resp.StatusCode)
	}
}
