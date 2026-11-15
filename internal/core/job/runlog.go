package job

import (
	"strings"
	"sync"
)

// runLog is the log of a run on the runner side: what the runner and the
// handler write, the secrets it knows replaced by [redacted], cut in chunks
// numbered from 0 that are sent in order. A chunk, once taken, is frozen
// until the server took it: sent again, it is the same text under the same
// number. The log holds MaxLog bytes at most, redacted, then ends with
// Truncated.
type runLog struct {
	mu      sync.Mutex
	secrets []string
	// hold is how many bytes at the end of pending a secret could still be
	// starting in, kept back until more text or the end of the run.
	hold int
	// chunk is the chunk taken and not yet acknowledged, numbered seq;
	// pending is what was written after it, redacted.
	chunk   string
	pending string
	seq     int
	// sent counts the bytes acknowledged; with chunk and pending, the
	// redacted log so far.
	sent int
	cut  bool
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

// logLimit is the bound of the text before the marker: room is kept for a
// line break and the marker, as the server keeps it.
const logLimit = MaxLog - len(Truncated) - 1

// Write takes p, as valid UTF-8, past MaxLog dropped after the marker.
func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cut {
		return len(p), nil
	}
	l.pending += strings.ToValidUTF8(string(p), "�")
	for _, s := range l.secrets {
		l.pending = strings.ReplaceAll(l.pending, s, redacted)
	}
	// The bound counts the text as it is sent, redacted.
	if before := l.sent + len(l.chunk); before+len(l.pending) > logLimit {
		l.pending = l.pending[:cutAt(l.pending, max(logLimit-before, 0))]
		// No later write completes a secret now: the start of one at the
		// end goes too.
		l.pending = l.pending[:len(l.pending)-l.secretStart(l.pending)]
		last := l.pending
		if last == "" {
			last = l.chunk
		}
		if last != "" && !strings.HasSuffix(last, "\n") {
			l.pending += "\n"
		}
		l.pending += Truncated
		l.cut = true
	}
	return len(p), nil
}

// take returns the next chunk to send and its number, the same until ack
// removes it. Unless final, the bytes a secret could still be starting in
// are kept back from a new chunk.
func (l *runLog) take(final bool) (int, string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.chunk != "" {
		return l.seq, l.chunk, true
	}
	if final && !l.cut {
		// No later write completes a secret: the start of one at the end
		// is redacted.
		if n := l.secretStart(l.pending); n > 0 {
			l.pending = l.pending[:len(l.pending)-n] + redacted
		}
	}
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
	l.chunk, l.pending = l.pending[:n], l.pending[n:]
	return l.seq, l.chunk, true
}

// secretStart returns the length of the longest end of text that starts a
// secret without holding it whole; the caller holds the lock.
func (l *runLog) secretStart(text string) int {
	longest := 0
	for _, s := range l.secrets {
		for n := min(len(s)-1, len(text)); n > longest; n-- {
			if strings.HasSuffix(text, s[:n]) {
				longest = n
				break
			}
		}
	}
	return longest
}

// ack removes the chunk numbered seq, which the server took.
func (l *runLog) ack(seq int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq != l.seq || l.chunk == "" {
		return
	}
	l.sent += len(l.chunk)
	l.chunk = ""
	l.seq++
}

// drop forgets what is not sent: the server cannot take it.
func (l *runLog) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunk, l.pending = "", ""
	l.cut = true
}
