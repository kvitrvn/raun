package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
version: 1
sources:
  exclude: ["vendor/**"]
  context: [".raun/context.md"]
types: [persona, requirement]
analysis:
  agents:
    - id: alpha
      runner: { argv: ["agent-a"] }
    - id: beta
      instructions: prompts/beta.md
      runner: { kind: command, argv: ["agent-b", "--json"], timeout: 5m }
lead:
  runner: { argv: ["lead"] }
`

func TestParseValid(t *testing.T) {
	cfg, err := Parse(strings.NewReader(validConfig))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Analysis.Quorum != 2 {
		t.Errorf("default quorum = %d, want 2", cfg.Analysis.Quorum)
	}
	alpha := cfg.Analysis.Agents[0]
	if alpha.Instructions != BuiltinAnalyst {
		t.Errorf("default instructions = %q, want %q", alpha.Instructions, BuiltinAnalyst)
	}
	if alpha.Runner.Kind != RunnerCommand {
		t.Errorf("default runner kind = %q, want %q", alpha.Runner.Kind, RunnerCommand)
	}
	if got := time.Duration(alpha.Runner.Timeout); got != DefaultTimeout {
		t.Errorf("default timeout = %v, want %v", got, DefaultTimeout)
	}
	if got := time.Duration(cfg.Analysis.Agents[1].Runner.Timeout); got != 5*time.Minute {
		t.Errorf("timeout = %v, want 5m", got)
	}
	if cfg.Lead == nil || cfg.Lead.Instructions != BuiltinLead {
		t.Errorf("lead instructions not defaulted: %+v", cfg.Lead)
	}
	if cfg.Reconciler != nil {
		t.Errorf("reconciler = %+v, want nil when omitted", cfg.Reconciler)
	}
}

func TestParseSingleAgentDefaultsQuorumToOne(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
version: 1
types: [persona]
analysis:
  agents:
    - id: solo
      runner: { argv: ["agent"] }
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Analysis.Quorum != 1 {
		t.Errorf("quorum = %d, want 1", cfg.Analysis.Quorum)
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		// wantFields are field paths that must each be reported.
		wantFields []string
		// wantText is matched against the error message when the failure
		// happens before semantic validation (decoding errors).
		wantText string
	}{
		{
			name:     "empty",
			yaml:     "",
			wantText: "config is empty",
		},
		{
			name:     "unknown top-level field",
			yaml:     "version: 1\nagents: []\n",
			wantText: "field agents not found",
		},
		{
			name: "unknown nested field",
			yaml: `
version: 1
types: [persona]
analysis:
  agents:
    - id: a
      runer: { argv: ["x"] }
`,
			wantText: "field runer not found",
		},
		{
			name: "bad duration",
			yaml: `
version: 1
types: [persona]
analysis:
  agents:
    - id: a
      runner: { argv: ["x"], timeout: soon }
`,
			wantText: `invalid duration "soon"`,
		},
		{
			name:       "missing everything",
			yaml:       "sources: {}\n",
			wantFields: []string{"version", "types", "analysis.agents", "analysis.quorum"},
		},
		{
			name: "bad types",
			yaml: `
version: 1
types: [persona, epic, persona]
analysis:
  agents:
    - id: a
      runner: { argv: ["x"] }
`,
			wantFields: []string{"types[1]", "types[2]"},
		},
		{
			name: "bad agents",
			yaml: `
version: 1
types: [persona]
analysis:
  quorum: 5
  agents:
    - id: Alpha
      runner: { argv: ["x"] }
    - id: b
      runner: { kind: http, argv: [] }
    - id: b
      instructions: ../outside.md
      runner: { argv: ["x"], timeout: -1m }
`,
			wantFields: []string{
				"analysis.quorum",
				"analysis.agents[0].id",
				"analysis.agents[1].runner.kind",
				"analysis.agents[1].runner.argv",
				"analysis.agents[2].id",
				"analysis.agents[2].instructions",
				"analysis.agents[2].runner.timeout",
			},
		},
		{
			name: "bad sources and roles",
			yaml: `
version: 2
sources:
  exclude: ["[abc"]
  context: ["/etc/passwd", ""]
types: [requirement]
analysis:
  agents:
    - id: a
      instructions: "builtin:"
      runner: { argv: ["x"] }
lead:
  runner: { argv: [] }
reconciler:
  runner: { kind: llm, argv: ["x"] }
`,
			wantFields: []string{
				"version",
				"sources.exclude[0]",
				"sources.context[0]",
				"sources.context[1]",
				"analysis.agents[0].instructions",
				"lead.runner.argv",
				"reconciler.runner.kind",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatal("Parse() error = nil, want error")
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantText)
			}
			got := fieldsOf(err)
			for _, f := range tt.wantFields {
				if !got[f] {
					t.Errorf("missing error for field %q; got: %v", f, err)
				}
			}
			if tt.wantFields != nil && len(got) != len(tt.wantFields) {
				t.Errorf("got %d field errors, want %d: %v", len(got), len(tt.wantFields), err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, err := Load(filepath.Join(dir, "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(missing) error = %v, want os.ErrNotExist", err)
	}
}

// fieldsOf collects the field paths of all *FieldError joined in err.
func fieldsOf(err error) map[string]bool {
	fields := map[string]bool{}
	var walk func(error)
	walk = func(err error) {
		var fe *FieldError
		if errors.As(err, &fe) && err == error(fe) {
			fields[fe.Field] = true
			return
		}
		if j, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range j.Unwrap() {
				walk(e)
			}
		}
	}
	walk(err)
	return fields
}
