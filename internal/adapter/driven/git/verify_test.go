package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/git"
	"github.com/kvitrvn/raun/internal/core/knowledge"
	"github.com/kvitrvn/raun/internal/gittest"
)

const billing = "package billing\r\n" +
	"\r\n" +
	"func Update(inv Invoice) error {\r\n" +
	"\tif inv.Status == StatusIssued {\r\n" +
	"\t\treturn ErrInvoiceLocked\r\n" +
	"\t}\r\n" +
	"\treturn nil\r\n" +
	"}\r\n"

const locked = "if inv.Status == StatusIssued {\n    return ErrInvoiceLocked\n}"

func citation(id, path, commit string, start, end int, excerpt string) knowledge.Evidence {
	return knowledge.Evidence{
		ID: id, Source: knowledge.SourceCode, Path: path, Commit: commit,
		StartLine: start, EndLine: end, Excerpt: excerpt, SHA256: knowledge.ExcerptSHA256(excerpt),
	}
}

// TestVerifyOnARepository verifies, relocates and invalidates citations
// through the Git adapter, then follows them across later commits.
func TestVerifyOnARepository(t *testing.T) {
	repo := gittest.New(t)
	repo.WriteFile("billing/invoice.go", []byte(billing))
	repo.WriteFile("billing/rules.md", []byte("Retry once.\n\nRetry once.\n"))
	repo.WriteFile("assets/logo.png", []byte("PNG\x00\x01"))
	repo.WriteFile("data/dump.sql", []byte(strings.Repeat("INSERT INTO t VALUES (1);\n", 100)))
	repo.Symlink("current", "billing/invoice.go")
	first := repo.Commit("first")

	r, err := git.Open(context.Background(), repo.Dir(), git.Options{MaxFileSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	v, err := knowledge.NewVerifier(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	cited := []knowledge.Evidence{
		citation("e1", "billing/invoice.go", first, 4, 6, locked),
		citation("e2", "billing/invoice.go", first, 1, 2, "return nil"),
		citation("e3", "billing/invoice.go", first, 4, 4, "if inv.Status == StatusDraft {"),
		citation("e4", "billing/rules.md", first, 2, 2, "Retry once."),
		citation("e5", "current", first, 1, 1, "billing/invoice.go"),
		citation("e6", "assets/logo.png", first, 1, 1, "PNG"),
		citation("e7", "data/dump.sql", first, 1, 1, "INSERT INTO t VALUES (1);"),
		citation("e8", "billing/gone.go", first, 1, 1, "x"),
		citation("e9", "billing/invoice.go", first, 7, 9, "return nil"),
	}
	want := []struct {
		state      knowledge.EvidenceState
		reason     knowledge.Reason
		start, end int
	}{
		{knowledge.EvidenceVerified, "", 4, 6},
		{knowledge.EvidenceRelocated, "", 7, 7},
		{knowledge.EvidenceInvalid, knowledge.ReasonNotFound, 4, 4},
		{knowledge.EvidenceInvalid, knowledge.ReasonAmbiguous, 2, 2},
		{knowledge.EvidenceInvalid, knowledge.ReasonNotRegular, 1, 1},
		{knowledge.EvidenceInvalid, knowledge.ReasonBinary, 1, 1},
		{knowledge.EvidenceInvalid, knowledge.ReasonTooLarge, 1, 1},
		{knowledge.EvidenceInvalid, knowledge.ReasonMissingFile, 1, 1},
		{knowledge.EvidenceInvalid, knowledge.ReasonBadRange, 7, 9},
	}
	got, err := v.Integrity(context.Background(), cited, now)
	if err != nil {
		t.Fatalf("Integrity: %v", err)
	}
	for i, w := range want {
		e := got[i]
		if e.State != w.state || e.Reason != w.reason || e.StartLine != w.start || e.EndLine != w.end || e.CheckedCommit != first {
			t.Errorf("%s: %s/%q at %d-%d on %s, want %s/%q at %d-%d", e.ID, e.State, e.Reason, e.StartLine, e.EndLine, e.CheckedCommit, w.state, w.reason, w.start, w.end)
		}
	}

	// A change in the working tree that is not committed changes nothing.
	if err := os.WriteFile(filepath.Join(repo.Dir(), "billing", "invoice.go"), []byte("rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := v.Integrity(context.Background(), cited[:1], now)
	if err != nil || again[0].State != knowledge.EvidenceVerified {
		t.Errorf("after an uncommitted change: %+v, %v", again, err)
	}

	// Later commits: the excerpt moves, then the file goes, then comes back.
	repo.WriteFile("billing/invoice.go", []byte("// Package billing.\n"+billing))
	moved := repo.Commit("moved")
	repo.Remove("billing/invoice.go")
	removed := repo.Commit("removed")
	repo.WriteFile("billing/invoice.go", []byte(billing))
	restored := repo.Commit("restored")

	held := got[:2] // e1 verified, e2 relocated to line 7
	steps := []struct {
		commit string
		state  knowledge.EvidenceState
		reason knowledge.Reason
	}{
		{moved, knowledge.EvidenceVerified, ""},
		{removed, knowledge.EvidenceStale, knowledge.ReasonMissingFile},
		{restored, knowledge.EvidenceVerified, ""},
	}
	for _, step := range steps {
		fresh, err := v.Freshness(context.Background(), held, step.commit, now)
		if err != nil {
			t.Fatalf("Freshness at %s: %v", step.commit, err)
		}
		for i, e := range fresh {
			if e.State != step.state || e.Reason != step.reason || e.CheckedCommit != step.commit {
				t.Errorf("%s at %s: %s/%q, want %s/%q", e.ID, step.commit, e.State, e.Reason, step.state, step.reason)
			}
			if e.Commit != first || e.StartLine != held[i].StartLine || e.EndLine != held[i].EndLine {
				t.Errorf("%s at %s: the stored citation moved to %s %d-%d", e.ID, step.commit, e.Commit, e.StartLine, e.EndLine)
			}
		}
		held = fresh
	}

	// Freshness leaves invalid evidence as it is.
	fresh, err := v.Freshness(context.Background(), got[2:], restored, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range fresh {
		if e != got[2+i] {
			t.Errorf("freshness changed the invalid %s: %+v", e.ID, e)
		}
	}
}

func TestVerifyAtAnUnknownCommit(t *testing.T) {
	repo := gittest.New(t)
	repo.WriteFile("a.txt", []byte("a\n"))
	repo.Commit("first")
	r, err := git.Open(context.Background(), repo.Dir(), git.Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := knowledge.NewVerifier(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := citation("e1", "a.txt", strings.Repeat("0", 40), 1, 1, "a")
	if _, err := v.Integrity(context.Background(), []knowledge.Evidence{e}, time.Now().UTC()); !errors.Is(err, knowledge.ErrUnknownRevision) {
		t.Errorf("Integrity at an unknown commit = %v, want ErrUnknownRevision", err)
	}
}
