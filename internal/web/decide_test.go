package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

func decisionForm(t *testing.T, h http.Handler, id string) url.Values {
	t.Helper()
	w := request(h, "GET", itemURL(id), nil)
	if w.Code != 200 {
		t.Fatalf("get form: %d %s", w.Code, w.Body.String())
	}
	v := url.Values{"author": {"Web reviewer"}, "reason": {"Reviewed against project evidence."}}
	for _, field := range []string{"csrf", "revision"} {
		match := regexp.MustCompile(`name="` + field + `" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(match) != 2 {
			t.Fatalf("no %s in form", field)
		}
		v.Set(field, match[1])
	}
	return v
}

func postDecision(h http.Handler, path string, v url.Values, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDecisionSuccess(t *testing.T) {
	for _, name := range []string{"persona", "requirement"} {
		for _, status := range []knowledge.Status{knowledge.StatusProposed, knowledge.StatusContested, knowledge.StatusNeedsReview} {
			for _, verb := range []string{"accept", "reject"} {
				for _, hx := range []bool{false, true} {
					t.Run(name+"/"+string(status)+"/"+verb+"/"+map[bool]string{false: "html", true: "htmx"}[hx], func(t *testing.T) {
						_, store, h := setup(t, false)
						it := fixture(t, name)
						// Retain questions/uncertainties: neither blocks a validation.
						it.OpenPoints = []knowledge.OpenPoint{{ID: "p1", Kind: knowledge.PointQuestion, Summary: "Question"}, {ID: "p2", Kind: knowledge.PointUncertainty, Summary: "Uncertainty"}}
						it.Status = status
						it.History[len(it.History)-1].To = status
						if err := store.Save(it); err != nil {
							t.Fatal(err)
						}
						before, _ := knowledge.Encode(it)
						v := decisionForm(t, h, it.ID)
						headers := map[string]string{"Origin": "http://127.0.0.1:8080"}
						want := 303
						if hx {
							headers["HX-Request"] = "true"
							want = 200
						}
						w := postDecision(h, itemURL(it.ID)+"/"+verb, v, headers)
						if w.Code != want {
							t.Fatalf("post: %d %s", w.Code, w.Body.String())
						}
						redirect := w.Header().Get("Location")
						if hx {
							redirect = w.Header().Get("HX-Redirect")
						}
						if redirect != itemURL(it.ID) {
							t.Fatalf("redirect=%q", redirect)
						}
						got, err := store.Get(it.ID)
						if err != nil {
							t.Fatal(err)
						}
						target := knowledge.StatusValidated
						if verb == "reject" {
							target = knowledge.StatusRejected
						}
						if got.Status != target || len(got.History) != len(it.History)+1 {
							t.Fatalf("decision: %+v", got)
						}
						last := got.History[len(got.History)-1]
						if last.Actor != knowledge.ActorHuman || last.By != v.Get("author") || last.Reason != v.Get("reason") {
							t.Fatalf("event: %+v", last)
						}
						got.Status, got.History = it.Status, it.History
						after, _ := knowledge.Encode(got)
						if !bytes.Equal(before, after) {
							t.Fatal("decision changed content")
						}
						body := request(h, "GET", redirect, nil).Body.String()
						assertContains(t, body, string(target), v.Get("reason"))
						if strings.Contains(body, `name="reason"`) {
							t.Fatal("decided item still offers actions")
						}
						w = postDecision(h, itemURL(it.ID)+"/"+verb, v, headers)
						if w.Code != 409 {
							t.Fatalf("double submission: %d", w.Code)
						}
						assertContains(t, w.Body.String(), v.Get("author"), v.Get("reason"), "Reload and review", `id="error-summary"`, `autofocus`)
					})
				}
			}
		}
	}
}

func TestDecisionValidationAndSecurity(t *testing.T) {
	for _, hx := range []bool{false, true} {
		for _, tt := range []struct {
			name    string
			change  func(url.Values)
			headers map[string]string
			status  int
			message string
		}{
			{"author", func(v url.Values) { v.Set("author", " ") }, nil, 422, "author: required"},
			{"reason", func(v url.Values) { v.Set("reason", " ") }, nil, 422, "reason: required"},
			{"csrf missing", func(v url.Values) { v.Del("csrf") }, nil, 403, "csrf:"},
			{"csrf forged", func(v url.Values) { v.Set("csrf", "forged") }, nil, 403, "csrf:"},
			{"revision missing", func(v url.Values) { v.Del("revision") }, nil, 422, "revision:"},
			{"revision forged", func(v url.Values) { v.Set("revision", strings.Repeat("0", 64)) }, nil, 409, "changed"},
			{"unknown", func(v url.Values) { v.Set("status", "validated") }, nil, 422, "form.status: unknown"},
			{"actor", func(v url.Values) { v.Set("actor", "human") }, nil, 422, "form.actor: unknown"},
			{"duplicate", func(v url.Values) { v.Add("reason", "second") }, nil, 422, "form.reason: duplicate"},
			{"duplicate token", func(v url.Values) { v.Add("csrf", v.Get("csrf")) }, nil, 422, "form.csrf: duplicate"},
			{"origin", nil, map[string]string{"Origin": "https://evil.example"}, 403, "cross-origin"},
			{"port origin", nil, map[string]string{"Origin": "http://127.0.0.1:8081"}, 403, "cross-origin"},
			{"fetch metadata", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403, "Local access only"},
			{"content type", nil, map[string]string{"Content-Type": "application/json"}, 422, "form: expected"},
			{"too large", func(v url.Values) { v.Set("reason", strings.Repeat("a", 64<<10)) }, nil, 422, "maximum 64 KiB"},
		} {
			t.Run(tt.name+map[bool]string{true: "/htmx", false: "/html"}[hx], func(t *testing.T) {
				_, store, h := setup(t, true)
				id := "persona-k3x9q2"
				before, err := os.ReadFile(store.Path(knowledge.TypePersona, id))
				if err != nil {
					t.Fatal(err)
				}
				v := decisionForm(t, h, id)
				if tt.change != nil {
					tt.change(v)
				}
				headers := map[string]string{}
				for k, v := range tt.headers {
					headers[k] = v
				}
				if hx {
					headers["HX-Request"] = "true"
				}
				w := postDecision(h, itemURL(id)+"/accept", v, headers)
				if w.Code != tt.status {
					t.Fatalf("status=%d: %s", w.Code, w.Body.String())
				}
				assertContains(t, w.Body.String(), tt.message)
				after, err := os.ReadFile(store.Path(knowledge.TypePersona, id))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("invalid submission wrote file")
				}
				if tt.name == "unknown" || tt.name == "duplicate" {
					assertContains(t, w.Body.String(), v.Get("author"), v.Get("reason"), `hx-push-url="false"`, `id="error-summary"`, `tabindex="-1"`)
				}
			})
		}
	}
}

func TestDecisionDisagreementsAndTerminalStatuses(t *testing.T) {
	_, store, h := setup(t, true)
	it := fixture(t, "requirement")
	body := request(h, "GET", itemURL(it.ID), nil).Body.String()
	assertContains(t, body, "raun resolve "+it.ID+" p1", `disabled`, "Rejection remains available")
	v := decisionForm(t, h, it.ID)
	w := postDecision(h, itemURL(it.ID)+"/accept", v, nil)
	if w.Code != 422 {
		t.Fatalf("disagreement accepted: %d", w.Code)
	}
	assertContains(t, w.Body.String(), "resolve the open disagreements first", v.Get("reason"))
	w = postDecision(h, itemURL(it.ID)+"/reject", v, nil)
	if w.Code != 303 {
		t.Fatalf("disagreement rejection: %d %s", w.Code, w.Body.String())
	}
	for _, status := range []knowledge.Status{knowledge.StatusValidated, knowledge.StatusRejected} {
		it := fixture(t, "persona")
		it.Status = status
		it.History[0].To = status
		if err := store.Save(it); err != nil {
			t.Fatal(err)
		}
		revision, err := knowledge.Revision(it)
		if err != nil {
			t.Fatal(err)
		}
		v.Set("revision", revision)
		for _, verb := range []string{"accept", "reject"} {
			w := postDecision(h, itemURL(it.ID)+"/"+verb, v, nil)
			if w.Code != 422 {
				t.Fatalf("terminal status %s %s: %d", status, verb, w.Code)
			}
		}
	}
}

func TestDecisionReadOnlyAndServerToken(t *testing.T) {
	root, store, h := setup(t, true)
	id := "persona-k3x9q2"
	v := decisionForm(t, h, id)
	for _, readOnly := range []bool{false, true} {
		other, err := NewHandler(root, Options{ReadOnly: readOnly})
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range []string{"accept", "reject"} {
			w := postDecision(other, itemURL(id)+"/"+verb, v, nil)
			if w.Code != 403 {
				t.Fatalf("readOnly=%v status=%d", readOnly, w.Code)
			}
		}
		if readOnly {
			body := request(other, "GET", itemURL(id), nil).Body.String()
			assertContains(t, body, "Read-only consultation")
			if strings.Contains(body, `name="csrf"`) {
				t.Fatal("read-only form exposed")
			}
		}
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != knowledge.StatusProposed {
		t.Fatal("forbidden request wrote status")
	}
}

func TestDecisionDiskErrors(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "busy", "unwritable"} {
		t.Run(kind, func(t *testing.T) {
			_, store, h := setup(t, true)
			id := "persona-k3x9q2"
			v := decisionForm(t, h, id)
			p := store.Path(knowledge.TypePersona, id)
			status := 500
			switch kind {
			case "missing":
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				status = 404
			case "invalid":
				if err := os.WriteFile(p, []byte("unknown: invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "busy":
				if err := os.WriteFile(p+".lock", nil, 0o600); err != nil {
					t.Fatal(err)
				}
				status = 409
			case "unwritable":
				if os.Geteuid() == 0 {
					t.Skip("permissions do not restrict root")
				}
				if err := os.Chmod(filepath.Dir(p), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(p), 0o700) })
			}
			w := postDecision(h, itemURL(id)+"/accept", v, map[string]string{"HX-Request": "true"})
			if w.Code != status {
				t.Fatalf("status=%d %s", w.Code, w.Body.String())
			}
			assertContains(t, w.Body.String(), v.Get("author"), v.Get("reason"), "Unable to complete decision")
			if kind != "missing" {
				assertContains(t, w.Body.String(), p)
			}
			if kind == "busy" {
				if _, err := os.Stat(p + ".lock"); err != nil {
					t.Fatal("removed another writer's lock")
				}
			}
		})
	}
}

func TestDecisionAuthor(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := gittest.New(t)
	if _, err := workspace.Init(repo.Dir); err != nil {
		t.Fatal(err)
	}
	store := knowledge.NewStore(workspace.KnowledgeDir(repo.Dir))
	it := fixture(t, "persona")
	if err := store.Save(it); err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(repo.Dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, request(h, "GET", itemURL(it.ID), nil).Body.String(), `name="author" value=""`)
	repo.Git("config", "user.name", "Git reviewer")
	assertContains(t, request(h, "GET", itemURL(it.ID), nil).Body.String(), `name="author" value="Git reviewer"`)
	v := decisionForm(t, h, it.ID)
	v.Set("author", "Different reviewer")
	if w := postDecision(h, itemURL(it.ID)+"/accept", v, nil); w.Code != 303 {
		t.Fatalf("editable author: %d", w.Code)
	}
}

func TestDecisionCLIStyleUpdateAfterDisplay(t *testing.T) {
	_, store, h := setup(t, true)
	id := "persona-k3x9q2"
	v := decisionForm(t, h, id)
	if err := store.Update(id, v.Get("revision"), func(it *knowledge.Item) error {
		return it.Resolve("p1", "CLI reviewer", time.Now(), nil, "Confirmed employee")
	}); err != nil {
		t.Fatal(err)
	}
	w := postDecision(h, itemURL(id)+"/accept", v, nil)
	if w.Code != 409 {
		t.Fatalf("outdated form accepted: %d", w.Code)
	}
	assertContains(t, w.Body.String(), "Confirmed employee", v.Get("author"), v.Get("reason"), `disabled`)
	// A reload is required to obtain a current revision.
	fresh := decisionForm(t, h, id)
	if fresh.Get("revision") == v.Get("revision") {
		t.Fatal("revision unchanged")
	}
	if w := postDecision(h, itemURL(id)+"/accept", fresh, nil); w.Code != 303 {
		t.Fatalf("fresh decision failed: %d", w.Code)
	}
}

func TestDecisionConcurrentRequests(t *testing.T) {
	_, store, h := setup(t, true)
	id := "persona-k3x9q2"
	v := decisionForm(t, h, id)
	const clients = 8
	start := make(chan struct{})
	results := make(chan int, clients)
	for i := range clients {
		go func() {
			<-start
			verb := "accept"
			if i%2 == 1 {
				verb = "reject"
			}
			results <- postDecision(h, itemURL(id)+"/"+verb, v, nil).Code
		}()
	}
	close(start)
	successes := 0
	for range clients {
		switch status := <-results; status {
		case 303:
			successes++
		case 409:
		default:
			t.Errorf("concurrent status=%d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful requests=%d", successes)
	}
	it, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.History) != 2 {
		t.Fatalf("history=%+v", it.History)
	}
}

func TestDecisionRejectsQueryAndEscapesRetainedFields(t *testing.T) {
	_, _, h := setup(t, true)
	id := "persona-k3x9q2"
	v := decisionForm(t, h, id)
	payload := `</textarea><script>alert("x")</script>`
	v.Set("reason", payload)
	w := postDecision(h, itemURL(id)+"/accept?actor=human", v, nil)
	if w.Code != 422 {
		t.Fatalf("query status=%d", w.Code)
	}
	assertContains(t, w.Body.String(), "query fields are not accepted", "&lt;/textarea&gt;&lt;script&gt;")
	if strings.Contains(w.Body.String(), payload) {
		t.Fatal("unescaped form input")
	}
}
