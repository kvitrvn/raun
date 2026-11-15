package gittest

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
)

// Server serves a fixture over the smart HTTP protocol of Git, on a loopback
// address, for the tests of a fetch: git http-backend behind the CGI handler
// of the standard library.
type Server struct {
	// URL is the address of the repository.
	URL string

	srv  *httptest.Server
	mu   sync.Mutex
	gate Gate
	auth []string
}

// Gate sees each request before Git does, and reports whether it answered
// it.
type Gate func(w http.ResponseWriter, r *http.Request) bool

// Unless answers 401 to a request whose Authorization header is not
// authorization.
func Unless(authorization string) Gate {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == authorization {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="gittest"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
}

// Serve serves r until the test ends. Every request goes through gate when
// it is set.
func (r *Repo) Serve(gate Gate) *Server {
	r.t.Helper()
	root := filepath.Dir(r.dir)
	home := r.t.TempDir()
	backend := &cgi.Handler{
		Path: r.git,
		Args: []string{"http-backend"},
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"HOME=" + home,
		},
	}
	s := &Server{gate: gate}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, req.Header.Get("Authorization"))
		gate := s.gate
		s.mu.Unlock()
		if gate != nil && gate(w, req) {
			return
		}
		backend.ServeHTTP(w, req)
	}))
	r.t.Cleanup(s.srv.Close)
	s.URL = s.srv.URL + "/" + filepath.Base(r.dir) + "/.git"
	return s
}

// SetGate replaces the gate of s.
func (s *Server) SetGate(gate Gate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = gate
}

// Authorizations returns the Authorization header of every request s
// received, in order.
func (s *Server) Authorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}
