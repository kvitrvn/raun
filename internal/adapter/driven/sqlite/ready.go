package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// CheckReady reports whether the database answers and its schema is the one
// of the binary, for the readiness endpoint of the server. It reads only,
// through the read pool: it never migrates and creates no table. A schema
// older than the binary gives ErrSchemaTooOld, a newer one ErrSchemaTooNew,
// both wrapped; a database that does not answer gives the error of the query.
func (s *Store) CheckReady(ctx context.Context) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	latest := migrations[len(migrations)-1].version
	return s.read(ctx, func(tx *sql.Tx) error {
		current, err := schemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case current < latest:
			return fmt.Errorf("%w: database at version %d, binary at %d", ErrSchemaTooOld, current, latest)
		case current > latest:
			return fmt.Errorf("%w: database at version %d, binary at %d", ErrSchemaTooNew, current, latest)
		}
		return nil
	})
}
