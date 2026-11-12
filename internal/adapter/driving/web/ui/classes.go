package ui

import (
	"maps"
	"strings"

	"github.com/a-h/templ"
)

// classes joins the non-empty class lists. It replaces the class-merging
// helper of shadcn-templ: a caller's class is appended, never merged.
func classes(lists ...string) string {
	var parts []string
	for _, l := range lists {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// with returns a copy of attrs with the given attributes added; attrs is
// never changed, so a caller can reuse its map.
func with(attrs templ.Attributes, add templ.Attributes) templ.Attributes {
	out := make(templ.Attributes, len(attrs)+len(add))
	maps.Copy(out, attrs)
	maps.Copy(out, add)
	return out
}
