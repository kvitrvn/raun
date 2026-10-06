package report

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/knowledge"
)

var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T, name string) *knowledge.Item {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "knowledge", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	it, err := knowledge.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func fixtures(t *testing.T) []*knowledge.Item {
	t.Helper()
	persona := load(t, "persona.yaml")
	req := load(t, "requirement.yaml")
	at := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	first := 0
	if err := req.Resolve("p1", "PO", at, &first, "The code is authoritative; the docs are outdated."); err != nil {
		t.Fatal(err)
	}
	if err := req.Transition(knowledge.StatusValidated, knowledge.Actor{Kind: knowledge.ActorHuman, Name: "PO"}, at, "Confirmed with accounting."); err != nil {
		t.Fatal(err)
	}
	rejected := load(t, "persona.yaml")
	rejected.ID, rejected.Title = "persona-rrrrrr", "Rejected persona"
	if err := rejected.Transition(knowledge.StatusRejected, knowledge.Actor{Kind: knowledge.ActorHuman, Name: "PO"}, at, "Not a user."); err != nil {
		t.Fatal(err)
	}
	return []*knowledge.Item{persona, rejected, req}
}

func TestRenderGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, fixtures(t), Order[:4]); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join("testdata", "report.md")
	if *update {
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("Render() differs from %s (run with -update to accept):\n%s", p, buf.String())
	}
}

func TestRenderFilters(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, fixtures(t), []knowledge.Status{knowledge.StatusRejected}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 item(s): 1 rejected.") || !strings.Contains(out, "Rejected persona") || strings.Contains(out, "Requirements") {
		t.Errorf("filtered report:\n%s", out)
	}
}

func TestFenceFor(t *testing.T) {
	for in, want := range map[string]string{"plain": "```", "a ``` b": "````", "`x`": "```"} {
		if got := fenceFor(in); got != want {
			t.Errorf("fenceFor(%q) = %q, want %q", in, got, want)
		}
	}
}
