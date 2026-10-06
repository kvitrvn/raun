package knowledge

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
)

// idAlphabet avoids look-alike characters (0/o, 1/l/i) since IDs are typed
// by humans in commands such as `raun accept <id>`.
const idAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

const idSuffixLen = 6

var idSuffixPattern = regexp.MustCompile(`^[a-z0-9]{6}$`)

// ValidID reports whether id is a well-formed ID for type t ("persona-k3x9q2").
func ValidID(t Type, id string) bool {
	suffix, ok := strings.CutPrefix(id, string(t)+"-")
	return ok && idSuffixPattern.MatchString(suffix)
}

// TypeOfID returns the type an ID belongs to.
func TypeOfID(id string) (Type, bool) {
	i := slices.IndexFunc(Types, func(t Type) bool { return ValidID(t, id) })
	if i < 0 {
		return "", false
	}
	return Types[i], true
}

// newID returns a random ID for type t. Random IDs stay stable when titles
// change and do not collide across Git branches.
func newID(t Type) string {
	b := make([]byte, idSuffixLen)
	for i := range b {
		b[i] = idAlphabet[rand.IntN(len(idAlphabet))]
	}
	return string(t) + "-" + string(b)
}
