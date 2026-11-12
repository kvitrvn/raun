package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/knowledge"
	"github.com/kvitrvn/raun/internal/core/knowledge/knowledgetest"
)

// openMigrated opens a migrated database in a temporary directory, closed at
// the end of the test.
func openMigrated(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raun.db")
	s := openAt(t, path)
	if err := s.Migrate(context.Background(), true); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s, path
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestContract(t *testing.T) {
	knowledgetest.Suite(t, func(t *testing.T) knowledge.Store {
		s, _ := openMigrated(t)
		return s
	})
}

func testScope() knowledge.Scope { return knowledge.Scope{Workspace: "acme", Repository: "billing"} }

func TestHistoryIsAppendOnly(t *testing.T) {
	s, _ := openMigrated(t)
	ctx := context.Background()
	if err := s.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2")); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE knowledge_history SET reason = 'rewritten'`,
		`UPDATE knowledge_history SET to_status = 'validated' WHERE position = 0`,
		`DELETE FROM knowledge_history`,
		`DELETE FROM knowledge_history WHERE workspace = 'acme'`,
	} {
		_, err := s.writer.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "knowledge_history is append-only") {
			t.Errorf("%s: %v, want the trigger to refuse it", stmt, err)
		}
	}
	it, err := s.Get(ctx, testScope(), "persona-k3x9q2")
	if err != nil || len(it.History) != 1 || it.History[0].Reason != "" {
		t.Errorf("the history changed: %+v, %v", it.History, err)
	}
}

func TestHistoryNeedsItsItem(t *testing.T) {
	s, _ := openMigrated(t)
	_, err := s.writer.ExecContext(context.Background(), `INSERT INTO knowledge_history
		(workspace, repository, item, position, at, actor, actor_id, from_status, to_status, reason)
		VALUES ('acme', 'billing', 'persona-k3x9q2', 0, '2026-11-12T09:00:00.000000000Z', 'run', 'r', '', 'proposed', '')`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("an event without its item: %v, want a foreign key failure", err)
	}
}

func TestStatusColumnFollowsHistory(t *testing.T) {
	s, _ := openMigrated(t)
	ctx := context.Background()
	if err := s.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(ctx, testScope(), "persona-k3x9q2", 1, func(it knowledge.Item) (knowledge.Item, error) {
		return knowledge.Decide(it, knowledge.StatusRejected, "account-42", "not a role", it.UpdatedAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	var status, last string
	if err := s.reader.QueryRowContext(ctx, `SELECT status FROM knowledge_items`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := s.reader.QueryRowContext(ctx, `SELECT to_status FROM knowledge_history ORDER BY position DESC LIMIT 1`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" || last != "rejected" {
		t.Errorf("status column %q, last event %q, want rejected for both", status, last)
	}
}

func TestDataSurvivesAReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "raun.db")
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, true); err != nil {
		t.Fatal(err)
	}
	p := knowledgetest.Persona(t, "persona-k3x9q2")
	if err := s.Create(ctx, testScope(), p); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again := openAt(t, path)
	if err := again.Migrate(ctx, false); err != nil {
		t.Fatalf("an up-to-date database needs no migration: %v", err)
	}
	got, err := again.Get(ctx, testScope(), p.ID)
	if err != nil || got.Title != p.Title || got.Revision != 1 {
		t.Errorf("after a reopen: %+v, %v", got, err)
	}
}

// TestTwoStoresOnOneFile shows that the revision check holds across
// processes, not only within the one writer connection of a Store.
func TestTwoStoresOnOneFile(t *testing.T) {
	ctx := context.Background()
	a, path := openMigrated(t)
	b := openAt(t, path)
	if err := a.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2")); err != nil {
		t.Fatal(err)
	}
	decide := func(it knowledge.Item) (knowledge.Item, error) {
		return knowledge.Decide(it, knowledge.StatusValidated, "account-42", "checked", it.UpdatedAt)
	}
	if _, err := a.Update(ctx, testScope(), "persona-k3x9q2", 1, decide); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Update(ctx, testScope(), "persona-k3x9q2", 1, decide); !errors.Is(err, knowledge.ErrStaleRevision) {
		t.Errorf("the second store: %v, want ErrStaleRevision", err)
	}
	got, err := b.Get(ctx, testScope(), "persona-k3x9q2")
	if err != nil || got.Revision != 2 {
		t.Errorf("the second store reads %+v, %v", got.Revision, err)
	}
}

// TestWriterRecovers shows that the one write connection is usable after
// every way an update or a creation can fail.
func TestWriterRecovers(t *testing.T) {
	s, _ := openMigrated(t)
	ctx := context.Background()
	if err := s.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2")); err != nil {
		t.Fatal(err)
	}
	failures := map[string]func() error{
		"mutation error": func() error {
			_, err := s.Update(ctx, testScope(), "persona-k3x9q2", 1, func(knowledge.Item) (knowledge.Item, error) {
				return knowledge.Item{}, errors.New("no")
			})
			return err
		},
		"unknown reference": func() error {
			return s.Create(ctx, testScope(), knowledgetest.Requirement(t, "requirement-8fz2mc", "persona-m7h4wd"))
		},
		"taken identifier": func() error {
			return s.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2"))
		},
		"canceled context": func() error {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err := s.Update(canceled, testScope(), "persona-k3x9q2", 1, func(it knowledge.Item) (knowledge.Item, error) { return it, nil })
			return err
		},
		"panicking mutation": func() (err error) {
			defer func() {
				if recover() == nil {
					err = nil
				} else {
					err = errors.New("panicked")
				}
			}()
			_, err = s.Update(ctx, testScope(), "persona-k3x9q2", 1, func(knowledge.Item) (knowledge.Item, error) { panic("boom") })
			return err
		},
	}
	for name, fail := range failures {
		if err := fail(); err == nil {
			t.Errorf("%s: no error", name)
		}
		it, err := s.Get(ctx, testScope(), "persona-k3x9q2")
		if err != nil || it.Revision != 1 {
			t.Fatalf("after %s: %+v, %v", name, it.Revision, err)
		}
	}
	if _, err := s.Update(ctx, testScope(), "persona-k3x9q2", 1, func(it knowledge.Item) (knowledge.Item, error) { return it, nil }); err != nil {
		t.Errorf("an update after every failure: %v", err)
	}
}

// TestReadersSeeOneState reads while a write transaction is open on the
// writer: the readers see the state before it, and are not blocked.
func TestReadersSeeOneState(t *testing.T) {
	s, _ := openMigrated(t)
	ctx := context.Background()
	if err := s.Create(ctx, testScope(), knowledgetest.Persona(t, "persona-k3x9q2")); err != nil {
		t.Fatal(err)
	}
	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.Update(ctx, testScope(), "persona-k3x9q2", 1, func(it knowledge.Item) (knowledge.Item, error) {
			close(inside)
			<-release
			return knowledge.Decide(it, knowledge.StatusValidated, "account-42", "checked", it.UpdatedAt)
		})
		done <- err
	}()
	<-inside
	it, err := s.Get(ctx, testScope(), "persona-k3x9q2")
	if err != nil || it.Revision != 1 || len(it.History) != 1 {
		t.Errorf("read during the write: revision %d, %d events, %v", it.Revision, len(it.History), err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	it, err = s.Get(ctx, testScope(), "persona-k3x9q2")
	if err != nil || it.Revision != 2 || len(it.History) != 2 {
		t.Errorf("read after the write: revision %d, %d events, %v", it.Revision, len(it.History), err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := openMigrated(t)
	ctx := context.Background()
	want := map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "1"}

	check := func(name string, conn *sql.Conn) {
		t.Helper()
		for pragma, value := range want {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("%s: PRAGMA %s: %v", name, pragma, err)
			}
			if got != value {
				t.Errorf("%s: %s = %s, want %s", name, pragma, got, value)
			}
		}
	}
	w, err := s.writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("writer", w)
	// Hold every reader connection at once, so each is checked.
	var readers []*sql.Conn
	for range DefaultReadConns {
		r, err := s.reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, r)
		check("reader", r)
		if _, err := r.ExecContext(ctx, `CREATE TABLE intruder (x)`); err == nil {
			t.Error("a reader connection wrote")
		}
	}
	for _, r := range readers {
		_ = r.Close()
	}
	_ = w.Close()
	if n := s.writer.Stats().MaxOpenConnections; n != 1 {
		t.Errorf("the writer pool holds up to %d connections, want 1", n)
	}
}

// TestWriterTakesTheLockFirst shows that a write transaction holds the write
// lock from its beginning: another connection cannot begin one meanwhile.
func TestWriterTakesTheLockFirst(t *testing.T) {
	_, path := openMigrated(t)
	ctx := context.Background()
	other, err := sql.Open("sqlite", path+"?_busy_timeout=0&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()

	s := openAt(t, path)
	err = s.write(ctx, func(*sql.Tx) error {
		tx, err := other.BeginTx(ctx, nil)
		if err == nil {
			_ = tx.Rollback()
			return errors.New("another connection began a write transaction")
		}
		if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "busy") {
			return err
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, path := range []string{"", ":memory:", "file:" + filepath.Join(dir, "a.db"), filepath.Join(dir, "a.db?mode=ro"), filepath.Join(dir, "a#b.db")} {
		if s, err := Open(ctx, path, Options{}); err == nil {
			_ = s.Close()
			t.Errorf("Open(%q) accepted", path)
		}
	}
	if _, err := Open(ctx, filepath.Join(dir, "a.db"), Options{ReadConns: -1}); err == nil {
		t.Error("a negative ReadConns accepted")
	}
	if _, err := Open(ctx, filepath.Join(dir, "missing", "a.db"), Options{}); err == nil {
		t.Error("a database in a missing directory opened")
	}

	// Open creates no table: the schema comes from Migrate only.
	s := openAt(t, filepath.Join(dir, "fresh.db"))
	var tables int
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("Open created %d tables", tables)
	}
}

func TestDocumentRefusesUnknownFields(t *testing.T) {
	var it knowledge.Item
	if err := decodeDocument(`{"persona":{"description":"x"},"extra":1}`, &it); err == nil {
		t.Error("a document with an unknown field was read")
	}
	if err := decodeDocument(`{"persona":{"description":"x"}} {}`, &it); err == nil {
		t.Error("a document with trailing data was read")
	}
	if err := decodeDocument(`{"persona":{"description":"x"}}`, &it); err != nil || it.Persona.Description != "x" {
		t.Errorf("a valid document: %+v, %v", it.Persona, err)
	}
}

func TestTimesKeepTheirOrderAsText(t *testing.T) {
	a := knowledgetest.Persona(t, "persona-k3x9q2").CreatedAt
	b := a.Add(1) // one nanosecond later
	c := a.Add(1_000_000_000)
	if !(formatTime(a) < formatTime(b) && formatTime(b) < formatTime(c)) {
		t.Errorf("text order differs: %s %s %s", formatTime(a), formatTime(b), formatTime(c))
	}
	back, err := parseTime(formatTime(b))
	if err != nil || !back.Equal(b) || back.Location().String() != "UTC" {
		t.Errorf("round trip: %v, %v", back, err)
	}
}
