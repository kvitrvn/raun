package job

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// The bounds of the log of a run (epic 6 record, Logs).
const (
	// MaxLogChunk bounds the decoded text of one chunk, in bytes.
	MaxLogChunk = 64 << 10
	// MaxLog bounds the log a run keeps, in bytes, the marker of a
	// truncation included.
	MaxLog = 1 << 20
	// LogRetention is how long the log of a run is kept after the run
	// ended, until the epic 13 review decides it.
	LogRetention = 30 * 24 * time.Hour
)

// Truncated ends a log cut at MaxLog: what came after it was dropped.
const Truncated = "(truncated)\n"

var (
	// ErrLogSequence: a chunk skips a number; a SequenceError says which
	// one is expected.
	ErrLogSequence = errors.New("a chunk of the log skips a number")
	// ErrInvalidLog: a chunk is not one a run may send; the error says
	// why.
	ErrInvalidLog = errors.New("invalid chunk of log")
)

// SequenceError refuses a chunk that skips a number: the runner sends the
// chunks again from Expected.
type SequenceError struct {
	Expected int
}

func (e *SequenceError) Error() string {
	return fmt.Sprintf("%v: chunk %d is expected", ErrLogSequence, e.Expected)
}

func (e *SequenceError) Unwrap() error { return ErrLogSequence }

// Log is the log of a run, as a store keeps it.
type Log struct {
	// Text is what the run sent, MaxLog bytes at most, ending with
	// Truncated when it was cut.
	Text string
	// Next is the number of the chunk expected next: how many were taken.
	Next int
	// Cut reports whether the log was cut at MaxLog.
	Cut bool
}

// CheckChunk reports whether a run may send the chunk numbered seq holding
// text; its errors wrap ErrInvalidLog.
func CheckChunk(seq int, text string) error {
	switch {
	case seq < 0:
		return fmt.Errorf("%w: chunk %d: a number starts at 0", ErrInvalidLog, seq)
	case len(text) > MaxLogChunk:
		return fmt.Errorf("%w: the text holds %d bytes, past %d", ErrInvalidLog, len(text), MaxLogChunk)
	case !utf8.ValidString(text):
		return fmt.Errorf("%w: the text is not UTF-8", ErrInvalidLog)
	}
	return nil
}

// Takes reports whether a log whose next chunk is l.Next takes the chunk
// numbered seq: not one sent again, below it, which changes nothing; one
// past it is a SequenceError. A store that does not hold the text of l
// decides by it as Append does.
func (l Log) Takes(seq int) (bool, error) {
	switch {
	case seq < l.Next:
		return false, nil
	case seq > l.Next:
		return false, &SequenceError{Expected: l.Next}
	}
	return true, nil
}

// Append returns l with the chunk numbered seq holding text, by the rules
// every store applies: a chunk sent again, below Next, changes nothing; one
// past Next is a SequenceError; past MaxLog the text is cut, at a character,
// and ends with Truncated, after which the text of later chunks is dropped
// while their numbers are still taken. The chunk is checked by CheckChunk
// first.
func (l Log) Append(seq int, text string) (Log, error) {
	if err := CheckChunk(seq, text); err != nil {
		return l, err
	}
	if take, err := l.Takes(seq); !take {
		return l, err
	}
	l.Next++
	if l.Cut {
		return l, nil
	}
	if len(l.Text)+len(text) <= MaxLog {
		l.Text += text
		return l, nil
	}
	// Room is kept for a line break and the marker, which starts a line of
	// its own.
	room := MaxLog - len(Truncated) - 1
	all := l.Text + text
	l.Text = all[:cutAt(all, room)]
	if l.Text != "" && l.Text[len(l.Text)-1] != '\n' {
		l.Text += "\n"
	}
	l.Text += Truncated
	l.Cut = true
	return l, nil
}

// cutAt returns the length of the longest prefix of s, a valid UTF-8 text,
// that holds n bytes at most and ends at a character.
func cutAt(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}
