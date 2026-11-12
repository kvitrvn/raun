package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// The schema, one numbered file per version, applied in order.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationName is the name of a migration file: its version on four digits
// and a slug.
var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

var (
	// ErrSchemaTooOld: the database is older than the binary and migrations
	// are not enabled.
	ErrSchemaTooOld = errors.New("database schema is older than this binary")
	// ErrSchemaTooNew: the database was migrated by a newer binary, which
	// this one never writes to.
	ErrSchemaTooNew = errors.New("database schema is newer than this binary")
)

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate brings the schema of the database to the version of the binary.
// The server calls it once at start, never on the first use of a
// connection. enabled is the administrator's setting
// (RAUN_DATABASE_MIGRATE=true, epic 3):
//   - a schema newer than the binary gives ErrSchemaTooNew, enabled or not;
//   - an older one gives ErrSchemaTooOld unless enabled, and is migrated
//     otherwise, each migration in its own transaction with its row in
//     schema_migrations;
//   - an up-to-date one is left as it is.
func (s *Store) Migrate(ctx context.Context, enabled bool) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	return migrate(ctx, s, migrations, enabled)
}

// loadMigrations reads the migrations of the migrations directory of fsys,
// which must be numbered from 1 without a gap.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migrations []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			return nil, fmt.Errorf("read migrations: %q is not named NNNN_<slug>.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: e.Name(), sql: string(body)})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return a.version - b.version })
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("read migrations: %s: expected version %d", m.name, i+1)
		}
	}
	if len(migrations) == 0 {
		return nil, errors.New("read migrations: none found")
	}
	return migrations, nil
}

func migrate(ctx context.Context, s *Store, migrations []migration, enabled bool) error {
	latest := migrations[len(migrations)-1].version
	current, err := schemaVersion(ctx, s.writer)
	if err != nil {
		return err
	}
	switch {
	case current > latest:
		return fmt.Errorf("%w: database at version %d, binary at %d", ErrSchemaTooNew, current, latest)
	case current == latest:
		return nil
	case !enabled:
		return fmt.Errorf("%w: database at version %d, binary at %d; set RAUN_DATABASE_MIGRATE=true to migrate it at start", ErrSchemaTooOld, current, latest)
	}
	for _, m := range migrations[current:] {
		err := s.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
				version    INTEGER PRIMARY KEY,
				applied_at TEXT NOT NULL
			) STRICT`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				m.version, formatTime(time.Now().UTC()))
			return err
		})
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
	}
	return nil
}

// schemaVersion returns the version of the last migration applied, 0 for a
// database that has none.
func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var tables int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if tables == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(version.Int64), nil
}
