package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCheckReady(t *testing.T) {
	ctx := context.Background()

	t.Run("an empty database is not ready and stays empty", func(t *testing.T) {
		s := freshStore(t)
		if err := s.CheckReady(ctx); !errors.Is(err, ErrSchemaTooOld) {
			t.Fatalf("CheckReady = %v, want ErrSchemaTooOld", err)
		}
		if names := tables(t, s); len(names) != 0 {
			t.Errorf("CheckReady created tables %v", names)
		}
	})

	t.Run("a migrated database is ready", func(t *testing.T) {
		s := freshStore(t)
		if err := s.Migrate(ctx, true); err != nil {
			t.Fatal(err)
		}
		if err := s.CheckReady(ctx); err != nil {
			t.Fatalf("CheckReady = %v, want nil", err)
		}
	})

	t.Run("a newer schema is not ready", func(t *testing.T) {
		s := freshStore(t)
		if err := s.Migrate(ctx, true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (9999, '2026-11-12T00:00:00.000000000Z')`); err != nil {
			t.Fatal(err)
		}
		if err := s.CheckReady(ctx); !errors.Is(err, ErrSchemaTooNew) {
			t.Fatalf("CheckReady = %v, want ErrSchemaTooNew", err)
		}
	})

	t.Run("a canceled context fails", func(t *testing.T) {
		s := freshStore(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.CheckReady(canceled); err == nil {
			t.Fatal("CheckReady with a canceled context = nil")
		}
	})

	t.Run("a closed store fails", func(t *testing.T) {
		s, err := Open(ctx, filepath.Join(t.TempDir(), "raun.db"), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx, true); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		err = s.CheckReady(ctx)
		if err == nil || errors.Is(err, ErrSchemaTooOld) || errors.Is(err, ErrSchemaTooNew) {
			t.Fatalf("CheckReady on a closed store = %v, want a database error", err)
		}
	})
}

func TestCheckPath(t *testing.T) {
	dir := t.TempDir()
	refused := []string{"", ":memory:", "file:" + filepath.Join(dir, "a.db"), filepath.Join(dir, "a.db?mode=ro"), filepath.Join(dir, "a#b.db")}
	for _, path := range refused {
		if err := CheckPath(path); err == nil {
			t.Errorf("CheckPath(%q) = nil", path)
		}
	}
	accepted := []string{"raun.db", filepath.Join(dir, "raun.db"), "./data/raun.db"}
	for _, path := range accepted {
		if err := CheckPath(path); err != nil {
			t.Errorf("CheckPath(%q) = %v", path, err)
		}
	}
}
