package agent

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/knowledge"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"bare", `{"a":1}`, `{"a":1}`},
		{"logs around", "thinking...\n{\"a\":1}\ndone {not json\n", `{"a":1}`},
		{"markdown fence", "Here:\n```json\n{\"a\": {\"b\": [1, {\"c\": 2}]}}\n```\n", `{"a": {"b": [1, {"c": 2}]}}`},
		{"last top-level object wins", `{"a":1} then {"a":2}`, `{"a":2}`},
		{"braces in strings", `{"s":"} {"}`, `{"s":"} {"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractJSON([]byte(tt.out))
			if err != nil || string(got) != tt.want {
				t.Errorf("ExtractJSON() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, out := range []string{"", "no json here", "{broken", "[1,2]"} {
		if _, err := ExtractJSON([]byte(out)); !errors.Is(err, ErrNoJSON) {
			t.Errorf("ExtractJSON(%q) error = %v, want ErrNoJSON", out, err)
		}
	}
}

const validReport = `{
  "version": 1,
  "observations": [
    {"id": "o1", "statement": "Admins can delete accounts.",
     "evidence": [{"path": "auth/roles.go", "start_line": 3, "end_line": 4, "excerpt": "case Admin:\n\treturn All"}]}
  ],
  "interpretations": [
    {"id": "i1", "type": "persona", "title": "Administrator", "statement": "Admins run the platform.",
     "nature": "supported", "observations": ["o1"],
     "persona": {"description": "Runs the platform.", "goals": ["Keep accounts clean"], "capabilities": ["Delete accounts"]}},
    {"id": "i2", "type": "requirement", "title": "Only admins delete", "statement": "Deletion is restricted.",
     "nature": "hypothesis", "observations": [],
     "requirement": {"statement": "Only administrators can delete accounts.", "kind": "business-rule", "rationale": "", "personas": ["i1"]}}
  ],
  "questions": [{"id": "q1", "question": "Can admins restore accounts?", "about": ["i1"]}]
}`

var allTypes = []knowledge.Type{knowledge.TypePersona, knowledge.TypeRequirement}

func TestParseReportValid(t *testing.T) {
	r, raw, err := ParseReport([]byte("log line\n"+validReport+"\n"), allTypes)
	if err != nil {
		t.Fatalf("ParseReport() error = %v", err)
	}
	if !strings.HasPrefix(string(raw), "{") || len(r.Interpretations) != 2 || r.Interpretations[1].Requirement.Personas[0] != "i1" {
		t.Errorf("unexpected report: %+v", r)
	}
}

func TestParseReportInvalid(t *testing.T) {
	tests := []struct {
		name    string
		replace [2]string
		types   []knowledge.Type
		want    string
	}{
		{"unknown field", [2]string{`"version": 1,`, `"version": 1, "confidence": 0.9,`}, allTypes, `unknown field "confidence"`},
		{"wrong version", [2]string{`"version": 1`, `"version": 2`}, allTypes, "version: must be 1"},
		{"type not configured", [2]string{"", ""}, []knowledge.Type{knowledge.TypePersona}, "interpretations[1].type"},
		{"duplicate id", [2]string{`"id": "q1"`, `"id": "o1"`}, allTypes, `questions[0].id: duplicate id "o1"`},
		{"dangling observation", [2]string{`"observations": ["o1"]`, `"observations": ["o9"]`}, allTypes, `interpretations[0].observations[0]: unknown observation "o9"`},
		{"supported without observation", [2]string{`"observations": ["o1"]`, `"observations": []`}, allTypes, "interpretations[0].observations: a supported interpretation"},
		{"bad nature", [2]string{`"nature": "hypothesis"`, `"nature": "certain"`}, allTypes, "interpretations[1].nature"},
		{"persona link to non-persona", [2]string{`"personas": ["i1"]`, `"personas": ["i2"]`}, allTypes, `interpretations[1].requirement.personas[0]: unknown persona interpretation "i2"`},
		{"bad kind", [2]string{`"business-rule"`, `"must-have"`}, allTypes, "interpretations[1].requirement.kind"},
		{"content block mismatch", [2]string{`"type": "persona"`, `"type": "requirement"`}, allTypes, "interpretations[0].requirement: must be set"},
		{"path escaping repo", [2]string{`"auth/roles.go"`, `"../secrets"`}, allTypes, "observations[0].evidence[0].path"},
		{"bad lines", [2]string{`"start_line": 3`, `"start_line": 9`}, allTypes, "observations[0].evidence[0].start_line"},
		{"question about unknown", [2]string{`"about": ["i1"]`, `"about": ["o1"]`}, allTypes, `questions[0].about[0]: unknown interpretation "o1"`},
		{"not an object", [2]string{validReport, "[]"}, allTypes, "no JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := strings.Replace(validReport, tt.replace[0], tt.replace[1], 1)
			_, _, err := ParseReport([]byte(report), tt.types)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParseReport() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestCommandRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	ctx := context.Background()
	dir := t.TempDir()

	out, err := CommandRunner{Argv: []string{"sh", "-c", `cat; echo "$RAUN_RUN_ID $RAUN_ROLE $(pwd)"; echo oops >&2; exit 3`}}.
		Run(ctx, Request{RunID: "r1", Role: "analyst", Prompt: "hello\n", Dir: dir})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.HasPrefix(string(out.Stdout), "hello\nr1 analyst ") || !strings.Contains(string(out.Stdout), dir) {
		t.Errorf("stdout = %q", out.Stdout)
	}
	if string(out.Stderr) != "oops\n" || out.ExitCode != 3 {
		t.Errorf("stderr = %q, exit = %d", out.Stderr, out.ExitCode)
	}

	start := time.Now()
	_, err = CommandRunner{Argv: []string{"sh", "-c", "sleep 30 & sleep 30"}}.
		Run(ctx, Request{Dir: dir, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took %s: child processes were not killed", elapsed)
	}

	if _, err := (CommandRunner{Argv: []string{"raun-no-such-command"}}).Run(ctx, Request{Dir: dir}); err == nil {
		t.Error("missing command: Run() error = nil")
	}
}
