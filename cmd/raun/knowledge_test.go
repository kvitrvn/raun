package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedKnowledge initializes a workspace and copies the knowledge package's
// golden items into it.
func seedKnowledge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if code, _, errOut := runCLI(t, "init", "-dir", dir); code != 0 {
		t.Fatalf("init: %s", errOut)
	}
	for _, f := range []struct{ golden, typ, id string }{
		{"persona.yaml", "persona", "persona-k3x9q2"},
		{"requirement.yaml", "requirement", "requirement-8fz2mc"},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "knowledge", "testdata", f.golden))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, ".raun", "knowledge", f.typ, f.id+".yaml")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestList(t *testing.T) {
	dir := seedKnowledge(t)

	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"all", nil, []string{"ID", "persona-k3x9q2", "requirement-8fz2mc  contested  1       2         1     Issued invoices are immutable"}, nil},
		{"by type", []string{"-type", "persona"}, []string{"persona-k3x9q2"}, []string{"requirement-8fz2mc"}},
		{"by status", []string{"-status", "contested"}, []string{"requirement-8fz2mc"}, []string{"persona-k3x9q2"}},
		{"no match", []string{"-status", "validated"}, []string{"No knowledge items."}, []string{"persona-k3x9q2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runCLI(t, append([]string{"list", "-dir", dir}, tt.args...)...)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, errOut)
			}
			for _, s := range tt.want {
				if !strings.Contains(out, s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			for _, s := range tt.notWant {
				if strings.Contains(out, s) {
					t.Errorf("output contains %q:\n%s", s, out)
				}
			}
		})
	}

	if code, _, errOut := runCLI(t, "list", "-dir", dir, "-status", "done"); code != 2 || !strings.Contains(errOut, `unknown status "done"`) {
		t.Errorf("bad status: exit = %d, stderr = %q", code, errOut)
	}
}

func TestListEmpty(t *testing.T) {
	code, out, _ := runCLI(t, "list", "-dir", t.TempDir())
	if code != 0 || !strings.Contains(out, "No knowledge items.") {
		t.Errorf("exit = %d, output = %q", code, out)
	}
}

func TestShow(t *testing.T) {
	dir := seedKnowledge(t)

	// Flags after the positional argument are accepted.
	code, out, errOut := runCLI(t, "show", "requirement-8fz2mc", "-dir", dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	for _, s := range []string{
		"requirement-8fz2mc  Issued invoices are immutable",
		"Status:  contested",
		"Personas:  persona-k3x9q2",
		"[supported] Issued invoices are read-only.  (agent alpha, run run-20261007-a1, i2)",
		"e1  billing/invoice.go:40-42 @ 3f1c2a9b8e7d  (code, verified)",
		"      | \treturn ErrInvoiceLocked",
		"p1  disagreement: Code forbids edits",
		"2. Admins can edit within 24 hours.  (beta)  [e2]",
		"unresolved",
		"run run-20261007-a1: proposed -> contested",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}

	tests := []struct {
		args     []string
		wantCode int
		wantErr  string
	}{
		{[]string{"show", "-dir", dir}, 2, "missing argument <id>"},
		{[]string{"show", "-dir", dir, "a", "b"}, 2, `unexpected argument "b"`},
		{[]string{"show", "-dir", dir, "persona-zzzzzz"}, 1, "knowledge item not found"},
	}
	for _, tt := range tests {
		code, _, errOut := runCLI(t, tt.args...)
		if code != tt.wantCode || !strings.Contains(errOut, tt.wantErr) {
			t.Errorf("%v: exit = %d, stderr = %q", tt.args, code, errOut)
		}
	}
}

func TestCheckValidatesKnowledge(t *testing.T) {
	dir := seedKnowledge(t)
	code, out, errOut := runCLI(t, "check", "-dir", dir)
	if code != 0 || !strings.Contains(out, "valid: 2 item(s)") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}

	// Removing the persona leaves the requirement with a dangling link.
	if err := os.Remove(filepath.Join(dir, ".raun", "knowledge", "persona", "persona-k3x9q2.yaml")); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI(t, "check", "-dir", dir)
	if code != 1 || !strings.Contains(errOut, `references unknown persona "persona-k3x9q2"`) {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
}
