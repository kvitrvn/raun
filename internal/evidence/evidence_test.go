package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/gittest"
	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
)

const invoice = `package billing

func (s *Service) Update(inv *Invoice) error {
	if inv.Status == StatusIssued {
		return ErrInvoiceLocked
	}
	return s.repo.Save(inv)
}

func (s *Service) Delete(inv *Invoice) error {
	if inv.Status == StatusIssued {
		return ErrInvoiceLocked
	}
	return s.repo.Delete(inv)
}
`

const guarded = "if inv.Status == StatusIssued {\n\treturn ErrInvoiceLocked\n}"

type fixture struct {
	repo   *gitx.Repo
	c1, c2 string
}

// newFixture commits invoice.go (c1), then shifts it by two lines and drops
// docs/old.md (c2).
func newFixture(t *testing.T) fixture {
	t.Helper()
	fx := gittest.New(t)
	fx.Write("billing/invoice.go", invoice)
	fx.Write("billing/crlf.go", strings.ReplaceAll(invoice, "\n", "\r\n"))
	fx.Write("docs/old.md", "Invoices are immutable.\n")
	fx.Write("logo.png", "\x89PNG\x00\x00binary")
	c1 := fx.Commit("one")
	fx.Write("billing/invoice.go", "// Copyright\n\n"+invoice)
	fx.Remove("docs/old.md")
	c2 := fx.Commit("two")

	repo, err := gitx.Open(context.Background(), fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{repo: repo, c1: c1, c2: c2}
}

func ev(commit, path string, start, end int, excerpt string) knowledge.Evidence {
	return knowledge.Evidence{ID: "e1", Path: path, Commit: commit, StartLine: start, EndLine: end, Excerpt: excerpt}
}

func TestVerifyIntegrity(t *testing.T) {
	fx := newFixture(t)
	c1 := fx.c1

	tests := []struct {
		name       string
		ev         knowledge.Evidence
		want       knowledge.EvidenceState
		start, end int
		reason     string
	}{
		{"exact", ev(c1, "billing/invoice.go", 4, 6, guarded), knowledge.EvidenceVerified, 4, 6, ""},
		{"spaces instead of tabs", ev(c1, "billing/invoice.go", 4, 6, "  if inv.Status ==  StatusIssued {\n    return ErrInvoiceLocked\n  }"), knowledge.EvidenceVerified, 4, 6, ""},
		{"blank edges ignored", ev(c1, "billing/invoice.go", 1, 2, "\npackage billing\n\n"), knowledge.EvidenceVerified, 1, 2, ""},
		{"crlf file", ev(c1, "billing/crlf.go", 4, 6, guarded), knowledge.EvidenceVerified, 4, 6, ""},
		{"wrong lines, nearest occurrence wins", ev(c1, "billing/invoice.go", 9, 11, guarded), knowledge.EvidenceRelocated, 11, 13, "lines 11-13"},
		{"range past end of file", ev(c1, "billing/invoice.go", 90, 92, guarded), knowledge.EvidenceRelocated, 11, 13, ""},
		{"fragment of a line", ev(c1, "billing/invoice.go", 5, 5, "ErrInvoiceLocked"), knowledge.EvidenceInvalid, 0, 0, "not found"},
		{"invented excerpt", ev(c1, "billing/invoice.go", 4, 6, "if inv.Paid {\n\treturn nil\n}"), knowledge.EvidenceInvalid, 0, 0, "not found"},
		{"blank excerpt", ev(c1, "billing/invoice.go", 2, 2, " \n\t"), knowledge.EvidenceInvalid, 0, 0, "blank"},
		{"range too long", ev(c1, "billing/invoice.go", 1, 6, guarded), knowledge.EvidenceInvalid, 0, 0, "cites 6 lines, the limit is 5"},
		{"excerpt too long", ev(c1, "billing/invoice.go", 1, 1, invoice), knowledge.EvidenceInvalid, 0, 0, "the limit is 5"},
		{"missing file", ev(c1, "billing/nope.go", 1, 1, "x"), knowledge.EvidenceInvalid, 0, 0, "does not exist"},
		{"directory", ev(c1, "billing", 1, 1, "x"), knowledge.EvidenceInvalid, 0, 0, "not a regular file"},
		{"binary file", ev(c1, "logo.png", 1, 1, "PNG"), knowledge.EvidenceInvalid, 0, 0, "binary"},
	}
	v := NewVerifier(fx.repo, 5)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Verify(context.Background(), tt.ev, tt.ev.Commit)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got.State != tt.want || got.StartLine != tt.start || got.EndLine != tt.end {
				t.Errorf("Verify() = %+v, want %s %d-%d", got, tt.want, tt.start, tt.end)
			}
			if !strings.Contains(got.Reason, tt.reason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestVerifyReturnsFileText(t *testing.T) {
	fx := newFixture(t)
	v := NewVerifier(fx.repo, 40)
	for _, path := range []string{"billing/invoice.go", "billing/crlf.go"} {
		got, err := v.Verify(context.Background(), ev(fx.c1, path, 9, 11, "  if inv.Status == StatusIssued {\n    return ErrInvoiceLocked\n  }"), fx.c1)
		if err != nil {
			t.Fatal(err)
		}
		if want := "\tif inv.Status == StatusIssued {\n\t\treturn ErrInvoiceLocked\n\t}"; got.Excerpt != want {
			t.Errorf("%s: Excerpt = %q, want the file's own text %q", path, got.Excerpt, want)
		}
	}
}

func TestVerifyFreshness(t *testing.T) {
	fx := newFixture(t)

	tests := []struct {
		name       string
		ev         knowledge.Evidence
		want       knowledge.EvidenceState
		start, end int
	}{
		{"moved by the new header", ev(fx.c1, "billing/invoice.go", 4, 6, guarded), knowledge.EvidenceRelocated, 6, 8},
		{"unchanged file", ev(fx.c1, "billing/crlf.go", 4, 6, guarded), knowledge.EvidenceVerified, 4, 6},
		{"deleted file", ev(fx.c1, "docs/old.md", 1, 1, "Invoices are immutable."), knowledge.EvidenceStale, 0, 0},
		{"excerpt gone", ev(fx.c1, "billing/invoice.go", 1, 1, "package invoices"), knowledge.EvidenceStale, 0, 0},
	}
	v := NewVerifier(fx.repo, 40)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Verify(context.Background(), tt.ev, fx.c2)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got.State != tt.want || got.StartLine != tt.start || got.EndLine != tt.end {
				t.Errorf("Verify() = %+v, want %s %d-%d", got, tt.want, tt.start, tt.end)
			}
		})
	}
}

func TestVerifyUnknownCommitIsAnError(t *testing.T) {
	fx := newFixture(t)
	missing := strings.Repeat("ab", 20)
	_, err := NewVerifier(fx.repo, 40).Verify(context.Background(), ev(missing, "billing/invoice.go", 4, 6, guarded), missing)
	if !errors.Is(err, gitx.ErrUnknownCommit) {
		t.Errorf("error = %v, want ErrUnknownCommit", err)
	}
}

type countingSource struct {
	calls int
	data  string
}

func (s *countingSource) ReadFile(context.Context, string, string) ([]byte, error) {
	s.calls++
	return []byte(s.data), nil
}

func TestVerifyCachesFiles(t *testing.T) {
	src := &countingSource{data: invoice}
	v := NewVerifier(src, 40)
	for range 3 {
		if _, err := v.Verify(context.Background(), ev("c", "billing/invoice.go", 4, 6, guarded), "c"); err != nil {
			t.Fatal(err)
		}
	}
	if src.calls != 1 {
		t.Errorf("source read %d times, want 1", src.calls)
	}
}
