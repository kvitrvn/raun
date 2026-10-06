package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestInitThenCheck(t *testing.T) {
	dir := t.TempDir()

	code, out, errOut := runCLI(t, "init", "-dir", dir)
	if code != 0 {
		t.Fatalf("init exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "created .raun/config.yaml") {
		t.Errorf("init output = %q", out)
	}

	code, out, _ = runCLI(t, "init", "-dir", dir)
	if code != 0 || strings.Contains(out, "created") || !strings.Contains(out, "left unchanged") {
		t.Errorf("second init: exit = %d, output = %q", code, out)
	}

	code, out, errOut = runCLI(t, "check", "-dir", dir)
	if code != 0 {
		t.Fatalf("check exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "is valid: 2 agent(s), quorum 2") {
		t.Errorf("check output = %q", out)
	}
}

func TestCheckReportsFieldPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".raun"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "version: 1\ntypes: [persona]\nanalysis:\n  agents:\n    - id: a\n      runner: { argv: [] }\n"
	if err := os.WriteFile(filepath.Join(dir, ".raun", "config.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runCLI(t, "check", "-dir", dir)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "analysis.agents[0].runner.argv: must contain at least the command to run") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestCheckWithoutConfig(t *testing.T) {
	code, _, errOut := runCLI(t, "check", "-dir", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "read config") {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{args: nil, wantCode: 2, wantErr: "missing command"},
		{args: []string{"frobnicate"}, wantCode: 2, wantErr: `unknown command "frobnicate"`},
		{args: []string{"diff"}, wantCode: 1, wantErr: "diff: not implemented yet"},
		{args: []string{"init", "extra"}, wantCode: 2, wantErr: `unexpected argument "extra"`},
		{args: []string{"init", "-nope"}, wantCode: 2, wantErr: "flag provided but not defined"},
		{args: []string{"help"}, wantCode: 0, wantOut: "Usage: raun"},
		{args: []string{"version"}, wantCode: 0, wantOut: "raun "},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, out, errOut := runCLI(t, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d (stderr = %q)", code, tt.wantCode, errOut)
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out, tt.wantOut)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut, tt.wantErr)
			}
		})
	}
}
