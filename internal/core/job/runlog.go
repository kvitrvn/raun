package job

import (
	"strings"
	"sync"
)

// runLog is the log of a run on the runner side: what the runner and the
// handler write, the secrets it knows replaced by [redacted], cut in chunks
// numbered from 0 that are sent in order, each kept until the server took
// it. It holds MaxLog bytes at most, then ends with Truncated.
type runLog struct {
	mu      sync.Mutex
	secrets []string
	// hold is how many bytes at the end of pending a secret could still be
	// starting in, kept back until more text or the end of the run.
	hold    int
	pending string
	// taken is the length of the chunk take gave last, which ack removes.
	taken int
	seq   int
	// written counts the bytes taken, the marker included; line reports
	// whether they end a line.
	written int
	line    bool
	cut     bool
}

func newRunLog(secrets ...string) *runLog {
	l := &runLog{}
	for _, s := range secrets {
		if s != "" {
			l.secrets = append(l.secrets, s)
			l.hold = max(l.hold, len(s)-1)
		}
	}
	return l
}

// Write takes p, as valid UTF-8, past MaxLog dropped after the marker.
func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cut {
		return len(p), nil
	}
	text := strings.ToValidUTF8(string(p), "\uFFFD")
	// Room is kept for a line break and the marker, as the server keeps it.
	room := MaxLog - len(Truncated) - 1 - l.written
	if len(text) > room {
		text = text[:cutAt(text, max(room, 0))]
		if (text != "" && !strings.HasSuffix(text, "\n")) || (text == "" && l.written > 0 && !l.line) {
			text += "\n"
		}
		text += Truncated
		l.cut = true
	}
	if text != "" {
		l.line = strings.HasSuffix(text, "\n")
	}
	l.written += len(text)
	l.pending += text
	l.redact()
	return len(p), nil
}

// redact replaces every secret in pending; the caller holds the lock.
func (l *runLog) redact() {
	for _, s := range l.secrets {
		l.pending = strings.ReplaceAll(l.pending, s, redacted)
	}
}

// take returns the next chunk to send and its number, without removing it:
// ack does once the server took it. Unless final, the bytes a secret could
// still be starting in are kept back.
func (l *runLog) take(final bool) (int, string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	avail := len(l.pending)
	if !final && !l.cut {
		avail -= l.hold
	}
	if avail <= 0 {
		return 0, "", false
	}
	n := cutAt(l.pending, min(avail, MaxLogChunk))
	if n == 0 {
		return 0, "", false
	}
	l.taken = n
	return l.seq, l.pending[:n], true
}

// ack removes the chunk numbered seq, which the server took: the one take
// gave last.
func (l *runLog) ack(seq int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq != l.seq || l.taken == 0 {
		return
	}
	l.pending = l.pending[l.taken:]
	l.taken = 0
	l.seq++
}

// drop forgets what is pending: the server cannot take it.
func (l *runLog) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending, l.taken = "", 0
	l.cut = true
}
