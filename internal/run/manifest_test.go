package run

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/agenttest"
	"github.com/kvitrvn/raun/internal/workspace"
)

const v1Manifest = `version: 1
id: 20261007-080000-old1
status: failed
error: 'run failed: quorum not met: 0 valid report(s), 1 required'
started_at: 2026-10-07T08:00:00Z
finished_at: 2026-10-07T08:01:00Z
commit: 3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b
raun_version: old
config_sha256: abc
language: en
types: [persona, requirement]
quorum: 1
agents:
  - id: alpha
    role: analyst
    instructions: builtin:analyst
    instructions_sha256: def
    argv: [agent]
    prompt_sha256: ghi
    status: failed
    error: exited with code 2
    exit_code: 2
    duration: 1s
`

func TestReadManifests(t *testing.T) {
	fx := agenttest.NewTeamProject(t, reconciledConfig(agenttest.Valid, agenttest.Reconciler))
	at := func(hour int) func() time.Time {
		return func() time.Time { return time.Date(2026, 10, 7, hour, 0, 0, 0, time.UTC) }
	}
	first, err := Execute(context.Background(), Options{Dir: fx.Dir, Now: at(10), RaunVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Executed later but dated earlier: the start time orders runs.
	second, err := Execute(context.Background(), Options{Dir: fx.Dir, Now: at(9), RaunVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runs := workspace.RunsDir(fx.Dir)
	fx.Write(".raun/runs/20261007-080000-old1/manifest.yaml", v1Manifest)
	// A run stopped before writing its manifest, and a stray file.
	fx.Write(".raun/runs/20261007-070000-gone/raw/alpha/prompt.md", "prompt")
	fx.Write(".raun/runs/notes.txt", "not a run")

	got, err := ReadManifests(runs)
	if err != nil {
		t.Fatalf("ReadManifests() error = %v", err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if want := []string{"20261007-080000-old1", second.ID, first.ID}; !slices.Equal(ids, want) {
		t.Fatalf("runs = %v, want %v", ids, want)
	}
	if got[0].Status != StatusFailed || got[0].Agents[0].Error != "exited with code 2" {
		t.Errorf("version 1 manifest = %+v", got[0])
	}
	if got[1].Reconciliation == nil || got[1].Reconciler == nil || got[2].Reconciliation != nil || len(got[2].Items) == 0 {
		t.Errorf("reconciliation of the rerun = %+v, of the first run = %+v", got[1].Reconciliation, got[2].Reconciliation)
	}

	if got, err := ReadManifests(filepath.Join(t.TempDir(), "runs")); err != nil || got != nil {
		t.Errorf("missing directory: %v, %v; want no runs", got, err)
	}
}

func TestReadManifestsInvalid(t *testing.T) {
	tests := []struct{ name, manifest, want string }{
		{"unknown field", v1Manifest + "confidence: 0.9\n", "field confidence not found"},
		{"unknown version", strings.Replace(v1Manifest, "version: 1", "version: 4", 1), "version: must be between 1 and 3, got 4"},
		{"not YAML", "version: [1\n", "manifest.yaml: yaml:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := t.TempDir()
			path := filepath.Join(runs, "20261007-080000-old1", "manifest.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ReadManifests(runs)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ReadManifests() error = %v, want %q in it, naming %s", err, tt.want, path)
			}
		})
	}
}
