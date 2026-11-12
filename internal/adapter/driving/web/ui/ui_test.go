package ui

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/kvitrvn/raun/internal/golden"
)

// hostile is text an analyzed repository could hold: it must come out as
// text, never as markup or as an attribute of its own.
const hostile = `<script>alert(1)</script>" onmouseover="x`

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// withText gives a component the escaped text child text.
func withText(c templ.Component, text string) templ.Component {
	child := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, templ.EscapeString(text))
		return err
	})
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return c.Render(templ.WithChildren(ctx, child), w)
	})
}

func testPage() Page {
	return Page{
		Title:    "Repositories",
		Instance: "raun.acme.dev",
		Assets: Assets{
			CSS:     "/assets/app.0123abcd.css",
			ThemeJS: "/assets/theme.0123abcd.js",
			AppJS:   "/assets/app.0123abcd.js",
			HTMX:    "/assets/htmx.min.0123abcd.js",
			Icon:    "/assets/favicon.0123abcd.svg",
		},
		Nav: []NavItem{{Label: "Repositories", Href: "/", Current: true}},
	}
}

func TestComponents(t *testing.T) {
	tests := []struct {
		name string
		c    templ.Component
	}{
		{"button", Button()},
		{"button-variants", templ.Join(
			Button(ButtonProps{Variant: ButtonSuccess, Type: TypeSubmit, Name: "decision", Value: "validate"}),
			Button(ButtonProps{Variant: ButtonOutline, Size: ButtonSmall, Form: "decide"}),
			Button(ButtonProps{Variant: ButtonGhost, Size: ButtonIcon, Label: "More actions"}),
			Button(ButtonProps{Variant: ButtonDanger, Size: ButtonLarge, Disabled: true}),
		)},
		{"button-link", Button(ButtonProps{Href: "/repositories", Variant: ButtonOutline, ID: "back"})},
		{"button-disabled-link", Button(ButtonProps{Href: "/repositories", Disabled: true})},
		{"button-unsafe-link", Button(ButtonProps{Href: "javascript:alert(1)"})},
		{"badge", templ.Join(
			Badge(),
			Badge(BadgeProps{Variant: BadgePrimary}),
			Badge(BadgeProps{Variant: BadgeMono, ID: "commit"}),
			Badge(BadgeProps{Variant: BadgeSuccess, Attributes: templ.Attributes{"title": "Validated"}}),
		)},
		{"input", Input(InputProps{ID: "name", Name: "name", Value: hostile, Placeholder: "Workspace", Required: true})},
		{"field-error", Field(FieldProps{For: "reason", Label: "Reason", Error: "A decision needs a reason."})},
		{"input-invalid", Input(InputProps{ID: "reason", Name: "reason", Invalid: true})},
		{"table", Table(TableProps{Label: "Repositories"})},
		{"table-parts", templ.Join(TableHeader(), TableBody(), TableRow(), TableHead(), TableCell(), TableCaption())},
		{"dialog", Dialog(DialogProps{ID: "activate", Title: "Activate a repository"})},
		{"dialog-trigger", DialogTrigger(DialogTriggerProps{For: "activate", Button: ButtonProps{Attributes: templ.Attributes{"data-x": "1"}}})},
		{"dialog-footer", DialogFooter()},
		{"dropdown-trigger", DropdownTrigger(DropdownTriggerProps{For: "item-actions", Button: ButtonProps{Variant: ButtonGhost, Size: ButtonIcon, Label: "Item actions"}})},
		{"dropdown-content", DropdownContent(DropdownContentProps{ID: "item-actions", Label: "Item actions"})},
		{"dropdown-items", templ.Join(
			DropdownItem(DropdownItemProps{Href: "/items/requirement-8fz2mc"}),
			DropdownItem(DropdownItemProps{Form: "reject", Variant: DropdownItemDestructive}),
			DropdownItem(DropdownItemProps{Disabled: true, Href: "/never"}),
			DropdownSeparator(), DropdownLabel(), DropdownShortcut(),
		)},
		{"toasts", templ.Join(
			Toast(Flash{Tone: ToneSuccess, Message: "Validated"}),
			Toast(Flash{Tone: ToneDanger, Message: hostile}),
			Toast(Flash{Message: "Saved"}),
		)},
		{"empty", Empty(EmptyProps{Title: "No repository yet", Icon: IconFolder})},
		{"error", ErrorState(ErrorProps{Status: 403, Title: hostile, Message: "No access.", BackHref: "/", BackLabel: "Back"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, withText(tt.c, "Label"))
			golden.Equal(t, "testdata/"+tt.name+".golden", []byte(got))
			checkMarkup(t, got)
		})
	}
}

func TestPages(t *testing.T) {
	flash := testPage()
	flash.Flash = []Flash{{Tone: ToneDanger, Message: hostile}}
	hostileTitle := testPage()
	hostileTitle.Title = hostile
	tests := []struct {
		name string
		c    templ.Component
	}{
		{"home", HomePage(testPage())},
		{"home-flash", HomePage(flash)},
		{"not-found", NotFoundPage(hostileTitle)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.c)
			golden.Equal(t, "testdata/page-"+tt.name+".golden", []byte(got))
			checkMarkup(t, got)
			for _, want := range []string{`<html lang="en">`, `<main id="main"`, `href="#main"`, `<nav aria-label="Main"`, `name="htmx-config"`} {
				if !strings.Contains(got, want) {
					t.Errorf("page lacks %s", want)
				}
			}
		})
	}
}

func TestIcons(t *testing.T) {
	names := []IconName{IconRune, IconCheck, IconX, IconPlus, IconSearch, IconMore, IconChevronDown, IconArrowLeft, IconCopy, IconExternal, IconFolder, IconSun, IconMoon, IconDiscCheck, IconDiscDot, IconDiscSwap, IconDiscAlert, IconDiscCross}
	for _, n := range names {
		got := render(t, Icon(n))
		if !strings.Contains(got, `aria-hidden="true"`) || !regexp.MustCompile(`<(path|circle|rect) `).MatchString(got) {
			t.Errorf("icon %s = %s", n, got)
		}
		checkMarkup(t, got)
	}
	labelled := render(t, Icon(IconDiscCheck, IconProps{Label: "Validated", Class: "text-success"}))
	if !strings.Contains(labelled, `role="img"`) || !strings.Contains(labelled, `aria-label="Validated"`) || strings.Contains(labelled, "aria-hidden") {
		t.Errorf("labelled icon = %s", labelled)
	}
}

var (
	styleAttribute   = regexp.MustCompile(`(?i)\sstyle=["']`)
	handlerAttribute = regexp.MustCompile(`(?i)\son[a-z]+=["']`)
	inlineScript     = regexp.MustCompile(`(?i)<script(\s[^>]*)?>`)
	foreignOrigin    = regexp.MustCompile(`(?i)(src|href)="(https?:)?//`)
)

// checkMarkup refuses what the Content Security Policy forbids, and checks
// that hostile text was escaped.
func checkMarkup(t *testing.T, html string) {
	t.Helper()
	if styleAttribute.MatchString(html) {
		t.Error("markup has a style attribute")
	}
	if handlerAttribute.MatchString(html) {
		t.Error("markup has an event handler attribute")
	}
	for _, m := range inlineScript.FindAllString(html, -1) {
		if !strings.Contains(m, " src=") {
			t.Errorf("markup has an inline script: %s", m)
		}
	}
	if foreignOrigin.MatchString(html) {
		t.Error("markup loads from another origin")
	}
	if strings.Contains(html, "<script>alert") || strings.Contains(html, `" onmouseover="x`) {
		t.Error("hostile text was not escaped")
	}
}
