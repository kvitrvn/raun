package sqlite

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/session"
	"github.com/kvitrvn/raun/internal/core/session/sessiontest"
)

func TestSessionsContract(t *testing.T) {
	sessiontest.Suite(t, func(t *testing.T) session.Store {
		s, _ := openMigrated(t)
		return s.Sessions()
	})
}

// TestSessionsStoreNoIdentifier proves that the table holds the hash of an
// identifier, never the identifier.
func TestSessionsStoreNoIdentifier(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	id, sess, err := session.New(rand.Reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	rows, err := s.reader.QueryContext(ctx, `SELECT hash, csrf, created_at, last_seen_at FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(a + b + c + d)
	}
	if strings.Contains(dump.String(), id) {
		t.Fatal("the sessions table holds an identifier")
	}
}

func TestSessionsRefuseAMalformedRow(t *testing.T) {
	ctx := context.Background()
	s, _ := openMigrated(t)
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO sessions (hash, csrf, created_at, last_seen_at) VALUES ('short', 'x', 'a', 'b')`); err == nil {
		t.Error("a hash of the wrong length was stored")
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO sessions (hash, csrf, created_at, last_seen_at) VALUES (?, '', 'a', 'b')`, strings.Repeat("a", 64)); err == nil {
		t.Error("an empty CSRF token was stored")
	}
}
