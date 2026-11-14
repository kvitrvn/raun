package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// embeddedAssets are the files served under /-/assets/: the generated
// stylesheet, the scripts, HTMX, the icon and the fonts.
//
//go:embed assets
var embeddedAssets embed.FS

// assetPrefix is where the assets are served: below /-/, which no name of the
// namespace can take, on the root mux (epic 5 record).
const assetPrefix = "/-/assets/"

// assetCache is the Cache-Control of an asset: its URL changes with its
// content, so a browser keeps it as long as it likes.
const assetCache = "public, max-age=31536000, immutable"

// assetTypes are the media types of the files that may be served; a file
// of another kind stops the server from starting.
var assetTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
}

// hashedName is a file name that carries the first 8 hexadecimal digits of
// the SHA-256 of its content before its extension, as the fonts do: the
// stylesheet names them, so their URL cannot be computed at start.
var hashedName = regexp.MustCompile(`\.([0-9a-f]{8})\.[a-z0-9]+$`)

// asset is one servable file.
type asset struct {
	body        []byte
	contentType string
}

// assets maps the URL of each asset to its content, and the logical name of
// each file (its path below assets/) to its URL. It is built once, from the
// embedded files, and only read afterwards.
type assets struct {
	byURL  map[string]asset
	byName map[string]string
}

// loadAssets reads every file below assets/ in fsys. A file of the
// assets/fonts directory must already carry the hash of its content in its
// name; the others get it inserted before their extension.
func loadAssets(fsys fs.FS) (*assets, error) {
	a := &assets{byURL: map[string]asset{}, byName: map[string]string{}}
	err := fs.WalkDir(fsys, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(p, "assets/")
		ext := path.Ext(name)
		contentType, ok := assetTypes[ext]
		if !ok {
			return fmt.Errorf("asset %s: no media type for %q", name, ext)
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("asset %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:4])
		served := name
		if strings.HasPrefix(name, "fonts/") {
			m := hashedName.FindStringSubmatch(name)
			if m == nil || m[1] != digest {
				return fmt.Errorf("asset %s: the name must carry %s, the start of the SHA-256 of its content", name, digest)
			}
		} else {
			served = strings.TrimSuffix(name, ext) + "." + digest + ext
		}
		url := assetPrefix + served
		a.byURL[url] = asset{body: body, contentType: contentType}
		a.byName[name] = url
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"app.css", "app.js", "theme.js", "htmx.min.js", "favicon.svg"} {
		if _, ok := a.byName[required]; !ok {
			return nil, fmt.Errorf("asset %s is missing", required)
		}
	}
	return a, nil
}

// url returns the URL of the asset with this logical name.
func (a *assets) url(name string) string {
	return a.byName[name]
}

// names returns the logical names, sorted.
func (a *assets) names() []string {
	names := make([]string, 0, len(a.byName))
	for n := range a.byName {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// serve answers GET and HEAD of an asset by its exact URL; any other path
// below /-/assets/, a stale hash included, is not found. Nothing is listed.
func (a *assets) serve(w http.ResponseWriter, r *http.Request) {
	f, ok := a.byURL[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Content-Length", strconv.Itoa(len(f.body)))
	h.Set("Cache-Control", assetCache)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.body)
}
