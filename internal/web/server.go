// Package web provides a local knowledge browser with human decisions.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kvitrvn/raun/internal/knowledge"
	"github.com/kvitrvn/raun/internal/workspace"
)

//go:embed assets/*
var assets embed.FS

type handler struct {
	root, project string
	store         *knowledge.Store
	readOnly      bool
	csrf          string
}

// Options configures the local browser. Decisions are enabled by default.
type Options struct {
	ReadOnly bool
}

// NewHandler requires an initialized workspace, but never loads agent configuration.
func NewHandler(root string, options Options) (http.Handler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project: %w", err)
	}
	if err = checkWorkspace(abs); err != nil {
		return nil, err
	}
	h := &handler{root: abs, project: filepath.Base(abs), store: knowledge.NewStore(workspace.KnowledgeDir(abs)), readOnly: options.ReadOnly, csrf: rand.Text()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/knowledge", http.StatusSeeOther) })
	mux.HandleFunc("GET /knowledge", h.list)
	mux.HandleFunc("GET /knowledge/{id}", h.detail)
	mux.HandleFunc("POST /knowledge/{id}/accept", h.decide(knowledge.StatusValidated))
	mux.HandleFunc("POST /knowledge/{id}/reject", h.decide(knowledge.StatusRejected))
	mux.HandleFunc("GET /assets/{name}", serveAsset)
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		// Refuse DNS rebinding and requests initiated by another website.
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		if host != "127.0.0.1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Local access only", http.StatusForbidden)
			return
		}
		protected.ServeHTTP(w, r)
	}), nil
}

func checkWorkspace(root string) error {
	p := filepath.Join(root, workspace.Dir)
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("open workspace %s: %w", p, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("open workspace %s: not a directory", p)
	}
	return nil
}

func (h *handler) load() ([]*knowledge.Item, error) {
	if err := checkWorkspace(h.root); err != nil {
		return nil, err
	}
	return h.store.List()
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilters(r.URL.Query())
	if err != nil {
		h.render(w, r, http.StatusBadRequest, pageModel{Title: "Invalid filters", Error: err.Error()})
		return
	}
	items, err := h.load()
	if err != nil {
		h.render(w, r, http.StatusInternalServerError, pageModel{Title: "Cannot read knowledge", Error: err.Error()})
		return
	}
	h.render(w, r, http.StatusOK, listModel(items, f))
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	items, err := h.load()
	if err != nil {
		h.render(w, r, http.StatusInternalServerError, pageModel{Title: "Cannot read knowledge", Error: err.Error()})
		return
	}
	for _, it := range items {
		if it.ID == r.PathValue("id") {
			m := pageModel{Title: it.Title, Item: it, Links: relatedItems(it, items)}
			if !h.readOnly && canDecide(it.Status) {
				revision, err := knowledge.Revision(it)
				if err != nil {
					h.render(w, r, http.StatusInternalServerError, pageModel{Title: "Cannot read knowledge", Error: err.Error()})
					return
				}
				m.Decision = &decisionModel{ID: it.ID, CSRF: h.csrf, Revision: revision, Author: h.defaultAuthor(r.Context())}
			}
			h.render(w, r, http.StatusOK, m)
			return
		}
	}
	h.render(w, r, http.StatusNotFound, pageModel{Title: "Knowledge item not found", Error: fmt.Sprintf("No knowledge item with ID %q.", r.PathValue("id"))})
}

func (h *handler) render(w http.ResponseWriter, r *http.Request, status int, m pageModel) {
	m.Project = h.project
	m.ReadOnly = h.readOnly
	var b bytes.Buffer
	component := document(m)
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		component = fragment(m)
	}
	if err := component.Render(r.Context(), &b); err != nil {
		http.Error(w, "Cannot render page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "HX-Request, HX-History-Restore-Request")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Bytes())
	}
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var contentType string
	switch name {
	case "favicon.svg":
		contentType = "image/svg+xml"
	case "app.css":
		contentType = "text/css; charset=utf-8"
	case "htmx.min.js", "app.js":
		contentType = "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// Serve binds only the IPv4 loopback interface and blocks until cancellation.
// Port zero is reserved for callers that need an automatically assigned port.
func Serve(ctx context.Context, root string, port int, stdout io.Writer, options Options) error {
	h, err := NewHandler(root, options)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen on 127.0.0.1:%d: %w", port, err)
	}
	defer func() { _ = listener.Close() }()
	fmt.Fprintf(stdout, "Raun knowledge browser: http://%s/knowledge\nPress Ctrl+C to stop.\n", listener.Addr())
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	stopped := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return fmt.Errorf("serve knowledge: %w", err)
}
