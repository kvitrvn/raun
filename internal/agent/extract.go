package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// ErrNoJSON reports an output that contains no JSON object.
var ErrNoJSON = errors.New("no JSON object found in agent output")

// ErrInvalidJSON reports an answer, an object that opens with a "version"
// key, that is not valid JSON even once repaired.
var ErrInvalidJSON = errors.New("the answer is not valid JSON")

// maxEnvelopeDepth bounds how deep Extract looks for a report carried as
// text inside other JSON objects.
const maxEnvelopeDepth = 4

// ExtractJSON is Extract for the output of an agent that finished.
func ExtractJSON(out []byte) ([]byte, error) {
	return Extract(out, false)
}

// Extract returns the JSON object an agent answered with. Agents may print
// logs, Markdown fences, or wrap their answer in tool-specific JSON, so:
//
//   - Every top-level JSON object of out is a candidate. One that does not
//     parse is repaired: backslashes that start no valid escape are doubled
//     and raw control characters inside strings are escaped.
//   - Starting from the last candidate, the first one with a top-level
//     "version" key is the answer. A candidate without one may carry the
//     answer as text in one of its string values (an envelope, or one event
//     of a JSON lines stream); those are searched the same way.
//   - With salvage, set for an agent stopped before it finished, an answer
//     cut while it was being printed is closed after its last complete
//     value, when no complete answer exists.
//   - Without such an object, an answer that does not parse even once
//     repaired fails with ErrInvalidJSON and the place of its first syntax
//     error, rather than yielding an object nested in it.
//   - Otherwise the last candidate is returned, so that the contract
//     violation can be reported.
//
// Nothing here knows any agent tool. The result is strictly decoded and
// validated by the caller.
func Extract(out []byte, salvage bool) ([]byte, error) {
	objs := topLevelObjects(out)
	for _, obj := range slices.Backward(objs) {
		if found := findVersioned(obj, 0); found != nil {
			return found, nil
		}
	}
	if salvage {
		if closed := closeTruncated(out); closed != nil {
			return closed, nil
		}
	}
	if err := invalidAnswer(out); err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, ErrNoJSON
	}
	return objs[len(objs)-1], nil
}

// topLevelObjects returns the JSON objects of out in order, repaired when
// needed. An object nested in another one is not listed on its own.
func topLevelObjects(out []byte) [][]byte {
	var objs [][]byte
	for i := 0; i < len(out); {
		j := bytes.IndexByte(out[i:], '{')
		if j < 0 {
			break
		}
		start := i + j
		dec := json.NewDecoder(bytes.NewReader(out[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			end := start + int(dec.InputOffset())
			objs = append(objs, out[start:end])
			i = end
			continue
		}
		// Repair only what looks like an object with keys, so that braces
		// in prose do not cost a scan to the end of the output.
		if looksLikeObject(out[start:]) {
			if end, ok := objectEnd(out[start:]); ok {
				if fixed := repair(out[start : start+end]); json.Valid(fixed) {
					objs = append(objs, fixed)
					i = start + end
					continue
				}
			}
		}
		i = start + 1
	}
	return objs
}

func looksLikeObject(b []byte) bool {
	rest := bytes.TrimLeft(b[1:], " \t\r\n")
	return len(rest) > 0 && (rest[0] == '"' || rest[0] == '}')
}

// objectEnd returns the length of the balanced object that b starts with,
// reading strings leniently (any escaped byte is skipped).
func objectEnd(b []byte) (int, bool) {
	depth, inString := 0, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inString && c == '\\':
			i++
		case inString:
			inString = c != '"'
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// repair fixes, inside strings, the mistakes agents make when they write
// JSON by hand: a backslash that starts no valid escape (a regexp's \d, a
// Windows path) and raw control characters (a newline in an excerpt).
func repair(b []byte) []byte {
	var out bytes.Buffer
	repairEach(b, func(_ int, s []byte) { out.Write(s) })
	return out.Bytes()
}

// repairEach calls emit, in order, with what repair writes for the byte of
// b at offset at (two bytes for a valid escape).
func repairEach(b []byte, emit func(at int, s []byte)) {
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case !inString:
			inString = c == '"'
			emit(i, b[i:i+1])
		case c == '"':
			inString = false
			emit(i, b[i:i+1])
		case c == '\\':
			if i+1 < len(b) && validEscape(b[i+1:]) {
				emit(i, b[i:i+2])
				i++
			} else {
				emit(i, []byte(`\\`))
			}
		case c < 0x20:
			switch c {
			case '\n':
				emit(i, []byte(`\n`))
			case '\r':
				emit(i, []byte(`\r`))
			case '\t':
				emit(i, []byte(`\t`))
			default:
				emit(i, fmt.Appendf(nil, `\u%04x`, c))
			}
		default:
			emit(i, b[i:i+1])
		}
	}
}

// invalidAnswer returns an ErrInvalidJSON error that locates the first
// syntax error of the last answer of out once repaired, or nil when out
// holds no answer or the answer parses. The place is given in out, with
// the text before it, since an agent asked again sees its output's tail.
func invalidAnswer(out []byte) error {
	start := answerStart(out)
	if start < 0 {
		return nil
	}
	b := out[start:]
	fixed := repair(b)
	var raw json.RawMessage
	err := json.NewDecoder(bytes.NewReader(fixed)).Decode(&raw)
	if err == nil {
		return nil
	}
	at := len(fixed) // where the error is, in fixed
	reason := "unexpected end of the output"
	if syn, ok := errors.AsType[*json.SyntaxError](err); ok {
		at, reason = int(syn.Offset)-1, syn.Error()
	}
	// Back to the byte of out that repair turned into fixed[at].
	pos, written := len(out), 0
	repairEach(b, func(i int, s []byte) {
		if written <= at && at < written+len(s) {
			pos = start + i
		}
		written += len(s)
	})

	lineStart := bytes.LastIndexByte(out[:pos], '\n') + 1
	line := bytes.Count(out[:pos], []byte{'\n'}) + 1
	column := utf8.RuneCount(out[lineStart:pos]) + 1
	from := max(0, pos-nearBytes)
	for from < pos && !utf8.RuneStart(out[from]) {
		from++
	}
	to := pos
	if to < len(out) {
		_, size := utf8.DecodeRune(out[to:])
		to += size
	}
	return fmt.Errorf("%w: line %d, column %d: %s, at the end of %q", ErrInvalidJSON, line, column, reason, out[from:to])
}

// nearBytes is how much of the output before a syntax error its message
// quotes.
const nearBytes = 48

// answerStart returns the offset of the last `{` of out that opens a
// "version" key, or -1.
func answerStart(out []byte) int {
	for i := bytes.LastIndexByte(out, '{'); i >= 0; i = bytes.LastIndexByte(out[:i], '{') {
		if rest := bytes.TrimLeft(out[i+1:], " \t\r\n"); bytes.HasPrefix(rest, []byte(`"version"`)) {
			return i
		}
	}
	return -1
}

// validEscape reports whether b (what follows a backslash) is a JSON escape.
func validEscape(b []byte) bool {
	switch b[0] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if len(b) < 5 {
			return false
		}
		for _, h := range b[1:5] {
			if !isHex(h) {
				return false
			}
		}
		return true
	}
	return false
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// findVersioned returns obj if it has a top-level "version" key, or else
// the last such object found in its string values, searched recursively.
func findVersioned(obj []byte, depth int) []byte {
	var keys map[string]json.RawMessage
	if json.Unmarshal(obj, &keys) != nil {
		return nil
	}
	if _, ok := keys["version"]; ok {
		return obj
	}
	if depth >= maxEnvelopeDepth {
		return nil
	}
	for _, s := range slices.Backward(stringValues(obj)) {
		for _, inner := range slices.Backward(topLevelObjects([]byte(s))) {
			if found := findVersioned(inner, depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

// stringValues lists the string values of a JSON document in order, keys
// left out.
func stringValues(doc []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(doc))
	type frame struct{ object, key bool }
	var stack []frame
	var values []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return values
		}
		top := len(stack) - 1
		switch t := tok.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				if top >= 0 && stack[top].object {
					stack[top].key = true
				}
				stack = append(stack, frame{object: t == '{', key: true})
			} else {
				stack = stack[:top]
			}
			continue
		case string:
			if top >= 0 && stack[top].object && stack[top].key {
				stack[top].key = false
				continue
			}
			values = append(values, t)
		}
		if top >= 0 && stack[top].object {
			stack[top].key = true
		}
	}
}

// closeTruncated returns the last answer of out that was cut while it was
// being printed, closed after its last complete value, or nil. The answer
// starts at the last `{` opening a "version" key.
func closeTruncated(out []byte) []byte {
	start := answerStart(out)
	if start < 0 {
		return nil
	}
	b := out[start:]

	// A cut is safe after an opening bracket, after a value string, after
	// a closed container, and before a comma: everything before it is
	// complete. Only cuts between the elements of an array, or between
	// the members of the answer itself, are kept: an element cut in its
	// middle would lack required fields. Remember the open containers at
	// the last safe cut.
	var stack, safeStack []byte
	safe := -1
	mark := func(at int) {
		if len(stack) == 1 || stack[len(stack)-1] == '[' {
			safe = at
			safeStack = append(safeStack[:0], stack...)
		}
	}
	key := false // the next string in the current object is a key
	for i := 0; i < len(b); i++ {
		switch c := b[i]; c {
		case '"':
			end := stringEnd(b[i:])
			if end < 0 {
				i = len(b)
				break
			}
			i += end - 1
			if len(stack) > 0 && stack[len(stack)-1] == '{' && key {
				key = false
			} else {
				mark(i + 1)
			}
		case '{', '[':
			stack = append(stack, c)
			key = c == '{'
			mark(i + 1)
		case '}', ']':
			if len(stack) == 0 {
				return nil
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return nil // complete: not a truncated answer
			}
			key = false
			mark(i + 1)
		case ',':
			mark(i)
			key = stack[len(stack)-1] == '{'
		case ':':
			key = false
		}
	}
	if safe < 0 {
		return nil
	}
	closed := append([]byte(nil), b[:safe]...)
	for _, c := range slices.Backward(safeStack) {
		if c == '{' {
			closed = append(closed, '}')
		} else {
			closed = append(closed, ']')
		}
	}
	if !json.Valid(closed) {
		closed = repair(closed)
	}
	if !json.Valid(closed) || findVersioned(closed, maxEnvelopeDepth) == nil {
		return nil
	}
	return closed
}

// stringEnd returns the length of the JSON string b starts with, or -1 if
// it is not terminated.
func stringEnd(b []byte) int {
	for i := 1; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}
