package web

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

func fixture(t *testing.T, name string) *knowledge.Item {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "knowledge", "testdata", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	it, err := knowledge.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return it
}
func setup(t *testing.T, populated bool) (string, *knowledge.Store, http.Handler) {
	t.Helper()
	root := t.TempDir()
	if _, err := workspace.Init(root); err != nil {
		t.Fatal(err)
	}
	// Serving must not parse this file or execute anything from it.
	if err := os.WriteFile(workspace.ConfigPath(root), []byte("not valid configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := knowledge.NewStore(workspace.KnowledgeDir(root))
	if populated {
		for _, name := range []string{"persona", "requirement"} {
			if err := store.Save(fixture(t, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	h, err := NewHandler(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return root, store, h
}
func request(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func assertContains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(body, part) {
			t.Errorf("response lacks %q", part)
		}
	}
}

func TestHTTP(t *testing.T) {
	_, _, h := setup(t, true)
	for _, tt := range []struct {
		name, method, path string
		headers            map[string]string
		status             int
		contains           string
		full               bool
	}{
		{"root", "GET", "/", nil, 303, "/knowledge", false},
		{"list", "GET", "/knowledge", nil, 200, "Platform administrator", true},
		{"detail", "GET", "/knowledge/requirement-8fz2mc", nil, 200, "Issued invoices are never editable.", true},
		{"fragment", "GET", "/knowledge", map[string]string{"HX-Request": "true"}, 200, "Platform administrator", false},
		{"restore", "GET", "/knowledge", map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"}, 200, "Platform administrator", true},
		{"unknown", "GET", "/knowledge/persona-zzzzzz", nil, 404, "Knowledge item not found", true},
		{"unknown fragment", "GET", "/knowledge/persona-zzzzzz", map[string]string{"HX-Request": "true"}, 404, "Knowledge item not found", false},
		{"bad filters", "GET", "/knowledge?page=-1", nil, 400, "page: must be a positive integer", true},
		{"no results", "GET", "/knowledge?q=nonexistent", nil, 200, "No matching knowledge", true},
		{"head", "HEAD", "/knowledge", nil, 200, "", false},
		{"mutation", "POST", "/knowledge", nil, 405, "Method Not Allowed", false},
		{"detail mutation", "DELETE", "/knowledge/persona-k3x9q2", nil, 405, "Method Not Allowed", false},
		{"raw files", "GET", "/.raun/config.yaml", nil, 404, "404", false},
		{"asset listing", "GET", "/assets/", nil, 404, "404", false},
		{"unknown asset", "GET", "/assets/server.go", nil, 404, "404", false},
		{"css", "GET", "/assets/app.css", nil, 200, ".cn-card", false},
		{"js", "GET", "/assets/htmx.min.js", nil, 200, "htmx", false},
		{"cross site", "GET", "/knowledge", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403, "Local access only", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := request(h, tt.method, tt.path, tt.headers)
			if w.Code != tt.status {
				t.Fatalf("status = %d; body %s", w.Code, w.Body.String())
			}
			assertContains(t, w.Body.String(), tt.contains)
			if strings.Contains(w.Body.String(), "<!doctype html>") != tt.full {
				t.Errorf("full document = %v", !tt.full)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("page can be cached")
			}
			if tt.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD has body")
			}
		})
	}
	r := httptest.NewRequest("GET", "http://attacker.example/knowledge", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("nonlocal host accepted: %d", w.Code)
	}
}

func TestDetailContentAndLinks(t *testing.T) {
	_, store, h := setup(t, true)
	body := request(h, "GET", "/knowledge/requirement-8fz2mc", nil).Body.String()
	assertContains(t, body, "An invoice cannot be edited once issued.", "Legal immutability", `href="/knowledge/persona-k3x9q2"`, "Position 1", "Position 2", "Issued invoices are never editable.", "Admins can edit within 24 hours.", `href="#evidence-e1"`, `id="evidence-e1"`, "Recorded state:", "verified", "billing/invoice.go", "3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b", `aria-hidden="true">40</span>`, "Unresolved")
	body = request(h, "GET", "/knowledge/persona-k3x9q2", nil).Body.String()
	assertContains(t, body, "Hypothesis", "Administrators are internal support staff.", `href="/knowledge/requirement-8fz2mc"`, "Goals", "Capabilities")
	it := fixture(t, "requirement")
	it.Requirement.Personas = append(it.Requirement.Personas, "persona-zzzzzz")
	pos := 1
	it.OpenPoints[0].Resolution = &knowledge.Resolution{At: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC), By: "Reviewer", Position: &pos, Note: "Keep the correction window."}
	it.Status = knowledge.StatusValidated
	it.History = append(it.History, knowledge.Event{At: it.OpenPoints[0].Resolution.At, Actor: knowledge.ActorHuman, By: "Reviewer", From: knowledge.StatusContested, To: knowledge.StatusValidated, Reason: "Confirmed by the owner."})
	if err := store.Save(it); err != nil {
		t.Fatal(err)
	}
	body = request(h, "GET", itemURL(it.ID), nil).Body.String()
	assertContains(t, body, "Missing reference: persona-zzzzzz", "Human resolution", "Keep the correction window.", "Kept position 2", "Confirmed by the owner.", "human · Reviewer", "Issued invoices are never editable.", "Admins can edit within 24 hours.")
}

func TestRecordedStatusesAndSources(t *testing.T) {
	_, store, h := setup(t, false)
	for _, status := range knowledge.Statuses {
		for _, state := range []knowledge.EvidenceState{knowledge.EvidenceVerified, knowledge.EvidenceRelocated, knowledge.EvidenceInvalid, knowledge.EvidenceStale} {
			for _, source := range []knowledge.SourceKind{knowledge.SourceCode, knowledge.SourceDoc, knowledge.SourceHumanContext} {
				it := fixture(t, "persona")
				it.Status = status
				it.History[0].To = status
				it.Evidence[0].State = state
				it.Evidence[0].Source = source
				if err := store.Save(it); err != nil {
					t.Fatal(err)
				}
				body := request(h, "GET", itemURL(it.ID), nil).Body.String()
				assertContains(t, body, string(status), string(state), "Source: "+string(source), "does not verify evidence")
			}
		}
	}
}

func TestReadOnlyRefreshAndErrors(t *testing.T) {
	root, store, h := setup(t, true)
	before := tree(t, root)
	for _, path := range []string{"/knowledge", "/knowledge?status=all", "/knowledge/persona-k3x9q2", "/knowledge/requirement-8fz2mc", "/assets/app.css", "/assets/htmx.min.js"} {
		request(h, "GET", path, nil)
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("consultation changed workspace files")
	}
	it := fixture(t, "persona")
	it.Title = "Updated from disk"
	if err := store.Save(it); err != nil {
		t.Fatal(err)
	}
	assertContains(t, request(h, "GET", "/knowledge", map[string]string{"HX-Request": "true"}).Body.String(), it.Title)
	p := store.Path(it.Type, it.ID)
	if err := os.WriteFile(p, []byte("version: 1\nunknown: invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/knowledge", "/knowledge/requirement-8fz2mc"} {
		w := request(h, "GET", path, map[string]string{"HX-Request": "true"})
		if w.Code != 500 {
			t.Fatalf("status=%d", w.Code)
		}
		assertContains(t, w.Body.String(), p, "Cannot read knowledge")
	}
	if err := os.RemoveAll(filepath.Join(root, ".raun")); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/knowledge", nil); w.Code != 500 {
		t.Fatalf("removed workspace: %d", w.Code)
	}
}

func TestEmptyAndEscaping(t *testing.T) {
	_, store, h := setup(t, false)
	assertContains(t, request(h, "GET", "/knowledge", nil).Body.String(), "No knowledge yet")
	it := fixture(t, "persona")
	payload := `<script>alert("x")</script>`
	it.Title = payload
	it.Persona.Description = payload
	it.Support[0].Statement = payload
	it.Evidence[0].Excerpt = payload
	it.Evidence[0].EndLine = it.Evidence[0].StartLine
	it.Evidence[0].SHA256 = knowledge.HashExcerpt(payload)
	if err := store.Save(it); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/knowledge", itemURL(it.ID)} {
		body := request(h, "GET", path, nil).Body.String()
		if strings.Contains(body, payload) {
			t.Fatal("unescaped file content")
		}
		assertContains(t, body, "&lt;script&gt;")
	}
	if _, err := NewHandler(t.TempDir(), Options{}); err == nil {
		t.Fatal("accepted missing workspace")
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files[path] = fmt.Sprintf("%s|%s|%s", info.Mode(), info.ModTime(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestServeCancelled(t *testing.T) {
	root, _, _ := setup(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := Serve(ctx, root, 0, &out, Options{}); err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), "http://127.0.0.1:", "Press Ctrl+C")
}
