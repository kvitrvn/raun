package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// unreservedSegments returns the first segments of patterns that a name of
// the namespace could take: an account or a workspace of that name would be
// shadowed by the route, or shadow it. Wildcards and the root are skipped;
// a segment no name can be (uppercase, a dot) cannot collide.
func unreservedSegments(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		if _, rest, ok := strings.Cut(p, " "); ok {
			p = rest
		}
		if i := strings.Index(p, "/"); i > 0 {
			p = p[i:] // a host
		}
		seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
		if seg == "" || strings.HasPrefix(seg, "{") {
			continue
		}
		if identity.CheckName(seg) == nil {
			out = append(out, seg)
		}
	}
	return out
}

func TestUnreservedSegments(t *testing.T) {
	got := unreservedSegments([]string{
		"GET /{$}", "GET /{name}", "POST /{name}/members", "GET /login/x", "/healthz",
		"GET /assets/{file...}", "raun.example/login", "GET /favicon.ico", "GET /Upper",
		"GET /dashboard", "POST /reports/{id}", "example.com/acme",
	})
	if strings.Join(got, " ") != "dashboard reports acme" {
		t.Errorf("unreservedSegments = %v, want dashboard reports acme", got)
	}
}

// TestEveryRouteIsReserved: the first segment of every route of the server
// is a name no account or workspace can take. A route added without
// reserving it in internal/core/identity fails here.
func TestEveryRouteIsReserved(t *testing.T) {
	l := newLoginServer(t)
	if len(l.patterns) < 10 {
		t.Fatalf("%d routes recorded: the login and workspace routes are missing", len(l.patterns))
	}
	if got := unreservedSegments(l.patterns); len(got) > 0 {
		t.Errorf("routes whose first segment is not reserved: %v; add them to the reserved names of internal/core/identity", got)
	}
}

// TestRoutesGoThroughRoute: a route registered on a mux directly would
// escape TestEveryRouteIsReserved. The only direct registrations are route
// itself, handle, the two catch-alls of New, and registerBypass, whose mux
// only matches the patterns that skip the protections and serves nothing.
func TestRoutesGoThroughRoute(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var called string
				switch f := call.Fun.(type) {
				case *ast.Ident:
					called = f.Name
				case *ast.SelectorExpr:
					called = f.Sel.Name
				}
				switch {
				case called == "handle" && fn.Name.Name != "route":
					t.Errorf("%s: %s calls handle: register the route with s.route", fset.Position(call.Pos()), fn.Name.Name)
				case (called == "Handle" || called == "HandleFunc") && fn.Name.Name != "handle" && fn.Name.Name != "registerBypass":
					if lit, ok := call.Args[0].(*ast.BasicLit); !ok || fn.Name.Name != "newServer" || (lit.Value != `"GET /"` && lit.Value != `"/"`) {
						t.Errorf("%s: %s registers a route on a mux directly: use s.route", fset.Position(call.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
}
