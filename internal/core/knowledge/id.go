package knowledge

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// idAlphabet leaves out characters that read alike: 0, 1, i, l and o.
const idAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// idLength is the number of random characters after the type and the dash.
const idLength = 6

// maxRandomReads bounds the reads of NewID, so that a source that only yields
// rejected bytes fails instead of looping forever.
const maxRandomReads = 64

// NewID draws an item identifier for the type t from the random source r:
// the type, a dash and six characters of the alphabet of docs/format.md.
//
// Production callers pass crypto/rand.Reader; tests pass a seeded source.
// NewID does not check that the identifier is free: the store refuses one that
// is taken in the repository, and the caller draws another.
func NewID(t Type, r io.Reader) (string, error) {
	if !t.Valid() {
		return "", fmt.Errorf("new id: unknown type %q", t)
	}
	if r == nil {
		return "", errors.New("new id: no random source")
	}

	// Rejection sampling: a byte at or above limit would make the modulo
	// favour the first characters of the alphabet.
	n := len(idAlphabet)
	limit := 256 - 256%n

	var b strings.Builder
	b.WriteString(string(t))
	b.WriteByte('-')
	buf := make([]byte, 16)
	drawn := 0
	for reads := 0; drawn < idLength; reads++ {
		if reads == maxRandomReads {
			return "", errors.New("new id: random source yields no usable byte")
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("new id: read random source: %w", err)
		}
		for _, c := range buf {
			if int(c) >= limit {
				continue
			}
			b.WriteByte(idAlphabet[int(c)%n])
			drawn++
			if drawn == idLength {
				break
			}
		}
	}
	return b.String(), nil
}

// idType returns the type of a well-formed item identifier.
func idType(id string) (Type, bool) {
	// Split on the last dash, so that a type holding a dash stays possible.
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return "", false
	}
	prefix, suffix := id[:i], id[i+1:]
	t := Type(prefix)
	if !t.Valid() || len(suffix) != idLength {
		return "", false
	}
	for _, c := range []byte(suffix) {
		if strings.IndexByte(idAlphabet, c) < 0 {
			return "", false
		}
	}
	return t, true
}

// localID reports whether s is a local identifier with the given prefix and a
// positive number without leading zero, such as e1 or p12.
func localID(s, prefix string) (int, bool) {
	digits, ok := strings.CutPrefix(s, prefix)
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	for _, c := range []byte(digits) {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return n, true
}

// FreeEvidenceIDs returns n evidence identifiers that the item does not use
// yet, in order, for evidence a caller is about to append.
func FreeEvidenceIDs(it Item, n int) []string {
	used := make([]string, len(it.Evidence))
	for i, e := range it.Evidence {
		used[i] = e.ID
	}
	return freeLocalIDs(used, "e", n)
}

// FreeOpenPointIDs returns n open point identifiers that the item does not use
// yet, in order, for open points a caller is about to append.
func FreeOpenPointIDs(it Item, n int) []string {
	used := make([]string, len(it.OpenPoints))
	for i, p := range it.OpenPoints {
		used[i] = p.ID
	}
	return freeLocalIDs(used, "p", n)
}

func freeLocalIDs(used []string, prefix string, n int) []string {
	highest := 0
	for _, id := range used {
		if k, ok := localID(id, prefix); ok && k > highest {
			highest = k
		}
	}
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strconv.Itoa(highest+1+i)
	}
	return out
}
