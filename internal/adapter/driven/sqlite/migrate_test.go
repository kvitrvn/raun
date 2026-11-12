package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// synthetic returns migrations that create one table each, t1 to tn.
func synthetic(t *testing.T, n int) []migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for i := 1; i <= n; i++ {
		name := "migrations/000" + string(rune('0'+i)) + "_step.sql"
		fsys[name] = &fstest.MapFile{Data: []byte("CREATE TABLE t" + string(rune('0'+i)) + " (x INTEGER);")}
	}
	m, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func freshStore(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "raun.db"))
}

func tables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.reader.QueryContext(context.Background(), `SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func version(t *testing.T, s *Store) int {
	t.Helper()
	v, err := schemaVersion(context.Background(), s.writer)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		applied int // migrations applied before the test, of the same set
		total   int
		enabled bool
		want    error
		after   int
	}{
		{"empty, enabled", 0, 2, true, nil, 2},
		{"empty, not enabled", 0, 2, false, ErrSchemaTooOld, 0},
		{"up to date, enabled", 2, 2, true, nil, 2},
		{"up to date, not enabled", 2, 2, false, nil, 2},
		{"older, enabled", 1, 2, true, nil, 2},
		{"older, not enabled", 1, 2, false, ErrSchemaTooOld, 1},
		{"newer, enabled", 3, 2, true, ErrSchemaTooNew, 3},
		{"newer, not enabled", 3, 2, false, ErrSchemaTooNew, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := freshStore(t)
			if tt.applied > 0 {
				if err := migrate(ctx, s, synthetic(t, tt.applied), true); err != nil {
					t.Fatal(err)
				}
			}
			err := migrate(ctx, s, synthetic(t, tt.total), tt.enabled)
			if !errors.Is(err, tt.want) {
				t.Fatalf("migrate = %v, want %v", err, tt.want)
			}
			if got := version(t, s); got != tt.after {
				t.Errorf("version = %d, want %d", got, tt.after)
			}
			if tt.want == nil && strings.Join(tables(t, s), " ") != "schema_migrations t1 t2" {
				t.Errorf("tables = %v", tables(t, s))
			}
			if errors.Is(tt.want, ErrSchemaTooOld) && !strings.Contains(err.Error(), "RAUN_DATABASE_MIGRATE=true") {
				t.Errorf("the refusal does not name the setting: %v", err)
			}
		})
	}
}

func TestMigrateTheEmbeddedSchema(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx, false); !errors.Is(err, ErrSchemaTooOld) {
		t.Errorf("Migrate(false) on an empty database = %v, want ErrSchemaTooOld", err)
	}
	if got := tables(t, s); len(got) != 0 {
		t.Errorf("a refused migration created %v", got)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tables(t, s), " "); got != "knowledge_history knowledge_items schema_migrations" {
		t.Errorf("tables = %s", got)
	}
	if err := s.Migrate(ctx, false); err != nil {
		t.Errorf("Migrate(false) on an up-to-date database = %v", err)
	}
}

// TestConcurrentMigrations runs Migrate on one file from two stores at once,
// as two processes starting together would: both succeed.
func TestConcurrentMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raun.db")
	a, b := openAt(t, path), openAt(t, path)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, s := range []*Store{a, b} {
		go func() {
			<-start
			errs <- s.Migrate(context.Background(), true)
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("Migrate: %v", err)
		}
	}
	if v := version(t, a); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
}

// TestNewerSchemaSeenUnderTheLock covers a binary that read an older version,
// then waited for the lock while a newer binary migrated past it.
func TestNewerSchemaSeenUnderTheLock(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	if err := migrate(ctx, s, synthetic(t, 2), true); err != nil {
		t.Fatal(err)
	}
	old := synthetic(t, 1)
	if err := apply(ctx, s, old[0], 1); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("apply over a newer schema = %v, want ErrSchemaTooNew", err)
	}
	if v := version(t, s); v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
}

func TestFailingMigrationLeavesNothing(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	fsys := fstest.MapFS{
		"migrations/0001_good.sql": {Data: []byte("CREATE TABLE good (x INTEGER);")},
		"migrations/0002_bad.sql":  {Data: []byte("CREATE TABLE half (x INTEGER); CREATE TABLE half (y INTEGER);")},
	}
	m, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s, m, true); err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("migrate = %v, want a failure naming 0002_bad.sql", err)
	}
	if got := strings.Join(tables(t, s), " "); got != "good schema_migrations" {
		t.Errorf("tables after the failure = %s, want the first migration only", got)
	}
	if v := version(t, s); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
}

func TestLoadMigrationsRefusesBadSets(t *testing.T) {
	sets := map[string]fstest.MapFS{
		"bad name":  {"migrations/1_init.sql": {}},
		"uppercase": {"migrations/0001_Init.sql": {}},
		"gap":       {"migrations/0001_a.sql": {}, "migrations/0003_c.sql": {}},
		"duplicate": {"migrations/0001_a.sql": {}, "migrations/0001_b.sql": {}},
		"not first": {"migrations/0002_b.sql": {}},
		"empty":     {"migrations/.keep": {}},
		"directory": {"migrations/0001_a.sql/x": {}},
	}
	for name, fsys := range sets {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := loadMigrations(migrationFiles); err != nil {
		t.Errorf("the embedded migrations: %v", err)
	}
}
