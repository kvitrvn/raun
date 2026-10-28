package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ErrNoJSON reports an output that contains no JSON object.
var ErrNoJSON = errors.New("no JSON object found in agent output")

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
//   - Without such an object, the last candidate is returned, so that the
//     contract violation can be reported.
//   - With salvage, set for an agent stopped before it finished, an answer
//     cut while it was being printed is closed after its last complete
//     value, when no complete answer exists.
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
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case !inString:
			inString = c == '"'
			out.WriteByte(c)
		case c == '"':
			inString = false
			out.WriteByte(c)
		case c == '\\':
			if i+1 < len(b) && validEscape(b[i+1:]) {
				out.WriteByte(c)
				out.WriteByte(b[i+1])
				i++
			} else {
				out.WriteString(`\\`)
			}
		case c < 0x20:
			switch c {
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				fmt.Fprintf(&out, `\u%04x`, c)
			}
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
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
	start := -1
	for i := bytes.LastIndexByte(out, '{'); i >= 0; i = bytes.LastIndexByte(out[:i], '{') {
		if rest := bytes.TrimLeft(out[i+1:], " \t\r\n"); bytes.HasPrefix(rest, []byte(`"version"`)) {
			start = i
			break
		}
	}
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
