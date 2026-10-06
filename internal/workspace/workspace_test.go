package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kvitrvn/raun/internal/config"
)

func TestInitCreatesValidScaffold(t *testing.T) {
	root := t.TempDir()

	res, err := Init(root)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	want := []string{".raun/config.yaml", ".raun/context.md", ".raun/.gitignore"}
	if !slices.Equal(res.Created, want) {
		t.Errorf("Created = %v, want %v", res.Created, want)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}

	// The generated configuration must be valid as-is.
	if _, err := config.Load(ConfigPath(root)); err != nil {
		t.Errorf("generated config is invalid: %v", err)
	}
}

func TestInitNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}

	custom := []byte("# edited by the team\n")
	ctx := filepath.Join(root, Dir, "context.md")
	if err := os.WriteFile(ctx, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, Dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}

	res, err := Init(root)
	if err != nil {
		t.Fatalf("second Init() error = %v", err)
	}
	if want := []string{".raun/.gitignore"}; !slices.Equal(res.Created, want) {
		t.Errorf("Created = %v, want %v", res.Created, want)
	}
	if want := []string{".raun/config.yaml", ".raun/context.md"}; !slices.Equal(res.Skipped, want) {
		t.Errorf("Skipped = %v, want %v", res.Skipped, want)
	}

	got, err := os.ReadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Errorf("context.md was overwritten: %q", got)
	}
}
