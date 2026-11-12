// Package ui holds the templates of the web interface: the components
// adapted from shadcn-templ (button, badge, input, table, dialog, dropdown
// menu), the icons, the layout, the page states and the pages.
//
// Templates are written in templ (*.templ) and compiled to *_templ.go by
// scripts/generate-web.sh, which also compiles styles.css into
// ../assets/app.css with Tailwind. The generated files are committed, and the
// tests of this package fail when they are stale.
//
// What this package does not do:
//   - render text unescaped: templ.Raw is refused everywhere by a test, and
//     icons are written as markup;
//   - put a style attribute, an inline script or an event handler attribute
//     in a template: the Content Security Policy refuses them, and behavior
//     lives in ../assets/app.js;
//   - generate identifiers: every id comes from the caller;
//   - hold state: a template renders what it is given.
//
// The components keep the data-slot structure and the cn-* classes of
// shadcn-templ v2.0.0-beta.16 (commit 88e25341331df34fcfe031f51416b132be30c2c6,
// MIT, ../licenses/shadcn-templ.txt); their look is defined in styles.css.
package ui
