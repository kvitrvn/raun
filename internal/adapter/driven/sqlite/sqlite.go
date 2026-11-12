package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kvitrvn/raun/internal/core/knowledge"

	// The driver registers itself as "sqlite": SQLite translated to Go, so
	// the binary builds without cgo.
	_ "modernc.org/sqlite"
)

// DefaultReadConns is the size of the read pool when Options sets none.
const DefaultReadConns = 4

// busyTimeout is how long, in milliseconds, a connection waits for a lock
// before it fails: a checkpoint or a backup does not fail a request at once.
const busyTimeout = 5000

// Options tunes a Store. A zero field takes its default.
type Options struct {
	// ReadConns is the number of connections reads run on, in parallel with
	// the one writer.
	ReadConns int
}

// Store is the knowledge store on one SQLite database file. It holds two
// pools on that file: one connection for every write, and a pool for reads.
type Store struct {
	writer *sql.DB
	reader *sql.DB
}

var _ knowledge.Store = (*Store)(nil)

// Open opens the database at path, creating the file if needed, with the
// settings of docs/implementation/02-knowledge-core.md on every connection.
// It applies no migration and creates no table: the server calls Migrate at
// start when the administrator enables it.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("open database: empty path")
	}
	// A plain file name ends at the first question mark, where the driver
	// reads its parameters; a path holding one, or a URI, would open
	// another file than the one asked for.
	if strings.ContainsAny(path, "?#") || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, fmt.Errorf("open database: path %q must be a plain file name", path)
	}
	readConns := opts.ReadConns
	if readConns < 0 {
		return nil, fmt.Errorf("open database: ReadConns must not be negative, got %d", readConns)
	}
	if readConns == 0 {
		readConns = DefaultReadConns
	}

	settings := url.Values{}
	settings.Set("_busy_timeout", fmt.Sprint(busyTimeout))
	settings.Set("_journal_mode", "WAL")
	settings.Set("_foreign_keys", "1")
	settings.Set("_synchronous", "NORMAL")
	writeSettings := url.Values{"_txlock": {"immediate"}}
	for k, v := range settings {
		writeSettings[k] = v
	}
	writeDSN := path + "?" + writeSettings.Encode()
	// A read connection cannot write, even by mistake.
	settings.Set("_query_only", "1")
	readDSN := path + "?" + settings.Encode()

	writer, err := openPool(ctx, writeDSN, 1)
	if err != nil {
		return nil, fmt.Errorf("open database %s: writer: %w", path, err)
	}
	reader, err := openPool(ctx, readDSN, readConns)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open database %s: readers: %w", path, err)
	}
	return &Store{writer: writer, reader: reader}, nil
}

func openPool(ctx context.Context, dsn string, conns int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.reader.Close(), s.writer.Close())
}

// write runs fn in a write transaction, which the writer begins IMMEDIATE:
// the write lock is taken before anything is read, so a revision read inside
// fn cannot change before the commit. An error from fn, or a panic, rolls the
// transaction back; the error comes back as it is.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// read runs fn in a read transaction on the read pool, so that every query of
// fn sees the same state of the database.
func (s *Store) read(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}
