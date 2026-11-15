package job

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// drain takes and acknowledges every chunk of l, final.
func drain(l *runLog) (string, int) {
	var out strings.Builder
	n, next := 0, -1
	for {
		seq, text, ok := l.take(true)
		if !ok {
			return out.String(), n
		}
		if next >= 0 && seq != next {
			panic("chunks out of order")
		}
		next = seq + 1
		out.WriteString(text)
		l.ack(seq)
		n++
	}
}

func TestRunLogRedactsSecrets(t *testing.T) {
	l := newRunLog("s3cret-token", "")
	_, _ = l.Write([]byte("fetching with s3cret-token\n"))
	// A secret split across two writes is redacted too.
	_, _ = l.Write([]byte("then s3cret-"))
	if _, text, ok := l.take(false); !ok || strings.Contains(text, "s3cret") {
		t.Fatalf("take before the end = %q, %v", text, ok)
	}
	_, _ = l.Write([]byte("token again\n"))
	got, _ := drain(l)
	if strings.Contains(got, "s3cret") || strings.Count(got, "[redacted]") != 2 {
		t.Errorf("the log = %q", got)
	}
}

func TestRunLogChunks(t *testing.T) {
	l := newRunLog()
	if _, _, ok := l.take(true); ok {
		t.Fatal("an empty log gave a chunk")
	}
	text := strings.Repeat("é", MaxLogChunk+1) // 2 bytes each: three chunks
	_, _ = l.Write([]byte(text))
	seq, first, ok := l.take(false)
	if !ok || seq != 0 || len(first) > MaxLogChunk || !utf8.ValidString(first) {
		t.Fatalf("first chunk: %d, %d bytes, %v", seq, len(first), ok)
	}
	// Not acknowledged: the same chunk again.
	if seq2, again, _ := l.take(false); seq2 != 0 || again != first {
		t.Error("a chunk not acknowledged changed")
	}
	l.ack(5)
	if seq2, _, _ := l.take(false); seq2 != 0 {
		t.Error("the acknowledgement of another chunk moved the log")
	}
	l.ack(0)
	got, n := drain(l)
	if first+got != text || n != 2 {
		t.Errorf("the chunks rebuild %d bytes in %d more chunks, want %d", len(first+got), n, len(text))
	}
	_, _ = l.Write([]byte("\xff\xfe"))
	if _, text, _ := l.take(true); !utf8.ValidString(text) {
		t.Errorf("a chunk not UTF-8: %q", text)
	}
}

func TestRunLogIsBounded(t *testing.T) {
	l := newRunLog()
	line := strings.Repeat("x", 1000) + "\n"
	for range MaxLog/len(line) + 10 {
		_, _ = l.Write([]byte(line))
	}
	got, _ := drain(l)
	if len(got) > MaxLog || !strings.HasSuffix(got, "\n"+Truncated) || strings.Count(got, Truncated) != 1 {
		t.Errorf("a log past its bound: %d bytes, ends %q", len(got), got[max(len(got)-20, 0):])
	}
	_, _ = l.Write([]byte("more"))
	if _, _, ok := l.take(true); ok {
		t.Error("text was taken after the cut")
	}
}

// TestARunLogChunkIsFrozenUntilTaken: a chunk whose answer was lost is sent
// again the same, under the same number, whatever was written since; what
// was written goes to the next chunk.
func TestARunLogChunkIsFrozenUntilTaken(t *testing.T) {
	l := newRunLog()
	_, _ = l.Write([]byte("fetching\n"))
	seq, first, ok := l.take(false)
	if !ok || seq != 0 || first != "fetching\n" {
		t.Fatalf("take = %d %q %v", seq, first, ok)
	}
	// The answer is lost; the handler writes more.
	_, _ = l.Write([]byte("running noop\n"))
	if seq, again, _ := l.take(false); seq != 0 || again != first {
		t.Fatalf("the chunk sent again = %d %q, want 0 %q", seq, again, first)
	}
	l.ack(0)
	if seq, next, _ := l.take(true); seq != 1 || next != "running noop\n" {
		t.Errorf("the next chunk = %d %q", seq, next)
	}
}

// TestRunLogBoundCountsRedactedText: a secret shorter than [redacted] does
// not make the log pass its bound.
func TestRunLogBoundCountsRedactedText(t *testing.T) {
	l := newRunLog("abc")
	line := strings.Repeat("abc ", 250) + "\n"
	for range MaxLog/len(line) + 10 {
		_, _ = l.Write([]byte(line))
	}
	got, _ := drain(l)
	if len(got) > MaxLog || strings.Count(got, Truncated) != 1 || strings.Contains(got, "abc") {
		t.Errorf("a redacted log past its bound: %d bytes, %d markers", len(got), strings.Count(got, Truncated))
	}
}
