package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadAssets(t *testing.T) {
	a, err := loadAssets(embeddedAssets)
	if err != nil {
		t.Fatal(err)
	}
	served := regexp.MustCompile(`^/assets/(fonts/)?[a-z0-9.-]+\.[0-9a-f]{8}\.[a-z0-9]+$`)
	for _, name := range a.names() {
		url := a.url(name)
		if !served.MatchString(url) {
			t.Errorf("%s is served at %s", name, url)
		}
		body := a.byURL[url].body
		sum := sha256.Sum256(body)
		if !strings.Contains(url, "."+hex.EncodeToString(sum[:4])+".") {
			t.Errorf("%s: URL %s does not carry the hash of its content", name, url)
		}
	}
	for _, name := range []string{"app.css", "app.js", "theme.js", "htmx.min.js", "favicon.svg"} {
		if a.url(name) == "" {
			t.Errorf("no URL for %s", name)
		}
	}
	if got := a.url("htmx.min.js"); !strings.HasPrefix(got, "/assets/htmx.min.") {
		t.Errorf("htmx.min.js is served at %s", got)
	}
}

func TestLoadAssetsRefuses(t *testing.T) {
	base := fstest.MapFS{
		"assets/app.css":     {Data: []byte("a")},
		"assets/app.js":      {Data: []byte("b")},
		"assets/theme.js":    {Data: []byte("c")},
		"assets/htmx.min.js": {Data: []byte("d")},
		"assets/favicon.svg": {Data: []byte("e")},
	}
	if _, err := loadAssets(base); err != nil {
		t.Fatalf("a complete set is refused: %v", err)
	}
	tests := []struct {
		name string
		fsys fstest.MapFS
	}{
		{"unknown kind", with(base, "assets/notes.txt", "x")},
		{"font without its hash", with(base, "assets/fonts/a.woff2", "x")},
		{"font with a wrong hash", with(base, "assets/fonts/a.00000000.woff2", "x")},
		{"missing stylesheet", without(base, "assets/app.css")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadAssets(tt.fsys); err == nil {
				t.Fatal("loadAssets accepted the files")
			}
		})
	}
	sum := sha256.Sum256([]byte("x"))
	good := "assets/fonts/a." + hex.EncodeToString(sum[:4]) + ".woff2"
	if _, err := loadAssets(with(base, good, "x")); err != nil {
		t.Errorf("a font named after its hash is refused: %v", err)
	}
}

func with(fsys fstest.MapFS, name, data string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range fsys {
		out[k] = v
	}
	out[name] = &fstest.MapFile{Data: []byte(data)}
	return out
}

func without(fsys fstest.MapFS, name string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range fsys {
		if k != name {
			out[k] = v
		}
	}
	return out
}

func TestServeAssets(t *testing.T) {
	s, logs := newTestServer(t, nil)
	css := s.assets.url("app.css")
	tests := []struct {
		name, method, target, host string
		status                     int
		contentType                string
	}{
		{"stylesheet", http.MethodGet, css, testHost, http.StatusOK, "text/css; charset=utf-8"},
		{"head", http.MethodHead, css, testHost, http.StatusOK, "text/css; charset=utf-8"},
		{"script", http.MethodGet, s.assets.url("app.js"), testHost, http.StatusOK, "text/javascript; charset=utf-8"},
		{"font", http.MethodGet, s.assets.url("fonts/onest-latin.052df745.woff2"), testHost, http.StatusOK, "font/woff2"},
		{"icon", http.MethodGet, s.assets.url("favicon.svg"), testHost, http.StatusOK, "image/svg+xml"},
		{"logical name", http.MethodGet, "/assets/app.css", testHost, http.StatusNotFound, ""},
		{"stale hash", http.MethodGet, "/assets/app.00000000.css", testHost, http.StatusNotFound, ""},
		{"directory", http.MethodGet, "/assets/fonts/", testHost, http.StatusNotFound, ""},
		// ServeMux redirects a path that is not clean; the target is not an asset.
		{"traversal", http.MethodGet, "/assets/../server.go", testHost, http.StatusTemporaryRedirect, ""},
		{"foreign host", http.MethodGet, css, "evil.example", http.StatusMisdirectedRequest, ""},
		{"post", http.MethodPost, css, testHost, http.StatusForbidden, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := serve(s, request(tt.method, tt.target, tt.host, nil))
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			if tt.status != http.StatusOK {
				return
			}
			h := resp.Header
			if h.Get("Content-Type") != tt.contentType || h.Get("Cache-Control") != assetCache || h.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("headers = %v", h)
			}
			body, _ := io.ReadAll(resp.Body)
			if tt.method == http.MethodGet && len(body) == 0 {
				t.Error("empty body")
			}
		})
	}
	if !strings.Contains(logs.String(), `"route":"GET /assets/{file...}"`) {
		t.Error("the access log does not name the asset route")
	}
}

// TestStylesheetNamesServedFonts checks that every font the stylesheet loads
// is served at that exact URL.
func TestStylesheetNamesServedFonts(t *testing.T) {
	s, _ := newTestServer(t, nil)
	css := string(s.assets.byURL[s.assets.url("app.css")].body)
	urls := regexp.MustCompile(`url\(["']?(/assets/[^"')]+)`).FindAllStringSubmatch(css, -1)
	if len(urls) == 0 {
		t.Fatal("the stylesheet loads no font")
	}
	for _, m := range urls {
		if _, ok := s.assets.byURL[m[1]]; !ok {
			t.Errorf("the stylesheet loads %s, which is not served", m[1])
		}
	}
}

// TestLicensesAreKept checks that docs/web.md lists a license file for each
// third-party source and that the file exists.
func TestLicensesAreKept(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "web.md"))
	if err != nil {
		t.Fatal(err)
	}
	listed := regexp.MustCompile("`licenses/([a-z0-9-]+\\.txt)`").FindAllStringSubmatch(string(doc), -1)
	if len(listed) < 6 {
		t.Fatalf("docs/web.md lists %d license files", len(listed))
	}
	for _, m := range listed {
		if _, err := os.Stat(filepath.Join("licenses", m[1])); err != nil {
			t.Errorf("license %s: %v", m[1], err)
		}
	}
}
