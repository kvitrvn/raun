// Adapted from shadcn-templ v2.0.0-beta.13 (MIT): components/icon.
// See ../licenses/shadcn-templ.txt and ../licenses/lucide.txt.

package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"
)

// IconProps configures an icon. Size is lucide's size prop and defaults to 16.
type IconProps struct {
	Size  int
	Class string
	// StrokeWidth defaults to lucide's 2.
	StrokeWidth string
}

// Icon renders a decorative Lucide icon with the attributes lucide renders.
// Unlike upstream, generated markup is not cached in package state.
func Icon(name string, props ...IconProps) templ.Component {
	var p IconProps
	if len(props) > 0 {
		p = props[0]
	}
	if p.Size == 0 {
		p.Size = 16
	}
	if p.StrokeWidth == "" {
		p.StrokeWidth = "2"
	}
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		content, ok := icons[name]
		if !ok {
			return fmt.Errorf("icon %q: unknown", name)
		}
		class := strings.TrimSpace("lucide lucide-" + name + " " + p.Class)
		_, err := fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round" class="%s" aria-hidden="true">%s</svg>`,
			p.Size, p.Size, templ.EscapeString(p.StrokeWidth), templ.EscapeString(class), content)
		return err
	})
}

// cn joins non-empty class lists. Unlike upstream, conflicting utilities are not
// merged: callers must not pass classes that override each other.
func cn(classes ...string) string {
	var parts []string
	for _, c := range classes {
		if c = strings.TrimSpace(c); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, " ")
}
