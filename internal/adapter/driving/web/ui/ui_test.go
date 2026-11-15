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
			CSS:     "/-/assets/app.0123abcd.css",
			ThemeJS: "/-/assets/theme.0123abcd.js",
			AppJS:   "/-/assets/app.0123abcd.js",
			HTMX:    "/-/assets/htmx.min.0123abcd.js",
			Icon:    "/-/assets/favicon.0123abcd.svg",
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
		{"csrf-field", CSRFField(`tok"en<`)},
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
	withSession := testPage()
	withSession.CSRF = `tok"en<`
	loggedIn := withSession
	loggedIn.Account = hostile
	loggedIn.Login = true
	canLogin := testPage()
	canLogin.Login = true
	adminPage := loggedIn
	adminPage.Nav = []NavItem{{Label: "Repositories", Href: "/"}, {Label: "Instance settings", Href: "/instance", Current: true}}
	switcherPage := loggedIn
	switcherPage.Account = "alice"
	switcherPage.Workspaces = []WorkspaceItem{
		{Name: "acme", Href: "/acme", Current: true},
		{Name: hostile, Href: "/" + hostile},
		{Name: "alice", Href: "/alice", Personal: true},
	}
	tests := []struct {
		name string
		c    templ.Component
	}{
		{"home", HomePage(testPage())},
		{"home-flash", HomePage(flash)},
		{"not-found", NotFoundPage(hostileTitle)},
		{"home-session", HomePage(withSession)},
		{"home-account", HomePage(loggedIn)},
		{"home-login", HomePage(canLogin)},
		{"login", LoginPage(canLogin, LoginProps{Title: "You cannot sign up here", Message: hostile, Retry: true})},
		{"login-unavailable", LoginPage(testPage(), LoginProps{Title: "Login is not available", Message: "No forge is configured."})},
		{"signup", SignupPage(withSession, SignupProps{Forge: "github", Login: hostile, Name: hostile, Error: hostile})},
		{"home-switcher", HomePage(switcherPage)},
		{"workspaces", WorkspacesPage(switcherPage, WorkspacesProps{
			Rows:  []WorkspaceRow{{Name: "acme", Href: "/acme", Role: "owner"}, {Name: hostile, Href: "/" + hostile, Role: "member"}, {Name: "alice", Href: "/alice", Role: "owner", Personal: true}},
			Name:  hostile,
			Error: hostile,
		})},
		{"workspace-owner", WorkspacePage(switcherPage, WorkspaceProps{
			Name: "acme", Role: "owner", Owner: true, Account: hostile, MemberRole: "owner", Error: hostile,
			Members: []MemberRow{{Name: "alice", Role: "owner"}, {Name: hostile, Role: "member"}},
		})},
		{"workspace-member", WorkspacePage(switcherPage, WorkspaceProps{Name: "acme", Role: "member", Members: []MemberRow{{Name: "alice", Role: "owner"}}})},
		{"workspace-personal", WorkspacePage(switcherPage, WorkspaceProps{Name: "alice", Personal: true, Role: "owner", Owner: true, Members: []MemberRow{{Name: "alice", Role: "owner"}}})},
		{"workspace-repositories", WorkspacePage(switcherPage, WorkspaceProps{
			Name: "acme", Role: "member", Members: []MemberRow{{Name: "alice", Role: "owner"}},
			Repositories: []RepositoryLink{{FullName: "acme/api", Href: "/acme/acme/api", State: StateActive}, {FullName: hostile, Href: "/acme/" + hostile, State: StateDeactivated}},
		})},
		{"workspace-repositories-unshown", WorkspacePage(switcherPage, WorkspaceProps{Name: "acme", Role: "member", RepositoriesError: hostile, Members: []MemberRow{{Name: "alice", Role: "owner"}}})},
		{"repositories", RepositoriesPage(switcherPage, RepositoriesProps{
			Query:      hostile,
			Workspaces: []string{"acme", "alice"},
			Error:      hostile,
			Rows: []RepositoryRow{
				{ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", Private: true, State: StateActive, Workspace: "acme", Href: "/acme/acme/api"},
				{ID: "2002", FullName: "acme/lost", ForgeHref: "https://github.com/acme/lost", State: StateUnreachable, Workspace: "beta", Href: "/beta/acme/lost"},
				{ID: hostile, FullName: hostile, ForgeHref: "https://github.com/" + hostile},
			},
		})},
		{"repositories-missing", RepositoriesPage(switcherPage, RepositoriesProps{
			Workspaces: []string{"alice"},
			Missing:    []string{"files", "webhooks", "events"},
			Rows:       []RepositoryRow{{ID: "2003", FullName: "acme/web", ForgeHref: "https://github.com/acme/web"}},
		})},
		{"repositories-empty", RepositoriesPage(switcherPage, RepositoriesProps{})},
		{"repositories-no-match", RepositoriesPage(switcherPage, RepositoriesProps{Query: "nothing"})},
		{"repository-active", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: hostile, Private: true, State: StateActive, CanDecide: true,
			Configuration: ConfigurationView{State: "configured", Commit: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", ReadAt: "2026-11-14 10:00:00 UTC"},
		})},
		{"repository-not-configured", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateActive,
			Configuration: ConfigurationView{State: "not_configured", Commit: hostile, ReadAt: "2026-11-14 10:00:00 UTC"},
		})},
		{"repository-unreadable", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateActive,
			Configuration: ConfigurationView{State: "unreadable", Reason: "too_large", ReadAt: "2026-11-14 10:00:00 UTC"},
		})},
		{"repository-events", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateActive,
			Events: []EventRow{
				{ReceivedAt: "2026-11-14 10:00:02 UTC", Event: "pull_request", Action: "opened", Ref: hostile, Before: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", After: "9c48853fa3dc5c1c3d6f1f1cd1f2743e72652840", Result: "recorded"},
				{ReceivedAt: "2026-11-14 10:00:01 UTC", Event: "issues", Action: hostile, Result: "ignored"},
			},
		})},
		{"repository-active-viewer", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateActive,
		})},
		{"repository-deactivated", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateDeactivated, CanDecide: true,
		})},
		{"repository-deactivated-missing", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateDeactivated, CanDecide: true, Missing: []string{"files"},
		})},
		{"repository-unreachable", RepositoryPage(switcherPage, RepositoryProps{
			Workspace: "acme", ID: "2001", FullName: "acme/api", ForgeHref: "https://github.com/acme/api", DefaultBranch: "main", State: StateUnreachable, CanDecide: true,
		})},
		{"error", ErrorPage(withSession, ErrorProps{Status: 403, Title: hostile, Message: hostile, BackHref: "/acme", BackLabel: "Back"})},
		{"instance", InstancePage(adminPage, []InstanceSetting{
			{Name: "RAUN_HTTP_ADDR", Value: ":8080"},
			{Name: "RAUN_TLS_CERT_FILE", Value: ""},
			{Name: "RAUN_SECRET_KEY", Value: "[redacted]"},
			{Name: "RAUN_ADMINS", Value: hostile},
		}, true)},
		{"runners", RunnersPage(adminPage, RunnersProps{
			Rows: []RunnerRow{
				{ID: "0123456789abcdef0123456789abcdef", Name: hostile, Scope: "workspace acme", Capabilities: []string{"noop"}, LastContact: "2026-11-14 15:01 UTC", Seen: true, Release: hostile, Running: 2},
				{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "idle", Scope: "instance", Capabilities: []string{"noop"}, LastContact: "never seen"},
				{ID: "fedcba9876543210fedcba9876543210", Name: "old", Scope: "instance", Capabilities: []string{"noop"}, Revoked: true},
			},
			Form: RunnerForm{Name: hostile, Workspace: "acme", Capabilities: []string{"noop"}, Workspaces: []string{"acme", "beta"}, Known: []string{"noop"}, Error: hostile},
		})},
		{"runners-empty", RunnersPage(adminPage, RunnersProps{Form: RunnerForm{Known: []string{"noop"}}})},
		{"runs-empty", RunsPage(loggedIn, RunsProps{FullName: "acme/api", RepoHref: "/acme/acme/api", Limit: 50})},
		{"runs", RunsPage(loggedIn, RunsProps{FullName: hostile, RepoHref: "/acme/acme/api", Limit: 50, Rows: []JobRow{
			{Href: "/acme/acme/api/runs/0123", Created: "2026-11-14 15:00:00 UTC", Kind: "noop", Trigger: hostile, Commit: strings.Repeat("c", 40), State: JobQueued, Runs: 0, Duration: "1m0s", Wait: hostile},
			{Href: "/acme/acme/api/runs/4567", Created: "2026-11-14 14:00:00 UTC", Kind: "noop", Trigger: "refs/heads/main", Commit: strings.Repeat("d", 40), State: JobSucceeded, Runs: 2, Duration: "2m3s"},
			{Href: "/acme/acme/api/runs/89ab", Created: "2026-11-14 13:00:00 UTC", Kind: "noop", Trigger: "by hand", Commit: strings.Repeat("e", 40), State: JobCancelled, Reason: "Cancelled by hand.", Runs: 1, Duration: "5s"},
		}})},
		{"job-running", JobPage(withSession, JobProps{FullName: "acme/api", RepoHref: "/acme/acme/api", RunsHref: "/acme/acme/api/runs",
			Kind: "noop", Trigger: "refs/heads/main", Commit: strings.Repeat("c", 40), State: JobRunning, Created: "2026-11-14 15:00:00 UTC", Duration: "12s",
			Runs: []RunRow{
				{Href: "/acme/acme/api/runs/0123?run=a", Runner: "r1", Started: "2026-11-14 15:00:01 UTC", Outcome: RunLost, Duration: "1m0s"},
				{Href: "/acme/acme/api/runs/0123?run=b", Selected: true, Runner: hostile, Started: "2026-11-14 15:01:01 UTC", Duration: "11s"},
			},
			HasLog: true, Log: LogView{ID: "log", Text: "fetching\n" + hostile + "\n&amp; done", PollURL: "/acme/acme/api/runs/0123/log?cut=0&offset=9&run=b"},
			CanCancel: true, CancelURL: "/acme/acme/api/runs/0123/cancel"})},
		{"job-ended", JobPage(withSession, JobProps{FullName: "acme/api", RepoHref: "/acme/acme/api", RunsHref: "/acme/acme/api/runs",
			Kind: "noop", Trigger: "refs/heads/main", Commit: strings.Repeat("c", 40), State: JobFailed, Reason: hostile, Created: "2026-11-14 15:00:00 UTC", Duration: "12s",
			Runs:   []RunRow{{Href: "/acme/acme/api/runs/0123?run=b", Selected: true, Runner: "r1", Started: "2026-11-14 15:01:01 UTC", Outcome: JobFailed, Summary: hostile, Duration: "11s"}},
			HasLog: true, Log: LogView{ID: "log", Text: "done\n"}})},
		{"job-queued", JobPage(withSession, JobProps{FullName: "acme/api", RepoHref: "/acme/acme/api", RunsHref: "/acme/acme/api/runs",
			Kind: "noop", Trigger: "by hand", Commit: strings.Repeat("c", 40), State: JobQueued, Wait: hostile, Created: "2026-11-14 15:00:00 UTC", Duration: "3s"})},
		{"runner-token", RunnerTokenPage(adminPage, RunnerTokenProps{Name: hostile, Token: "token-sentinel", Created: true})},
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
	// A link to a page of the forge navigates, and is the only absolute
	// address allowed: a source, a stylesheet, a base or the target of a
	// form never points to another origin.
	foreignOrigin = regexp.MustCompile(`(?i)(\s(src|action|formaction)="(https?:)?//|<(link|base|area)\s[^>]*href="(https?:)?//)`)
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

// TestButtonsKeepTheFocusRing guards the focus indicator of the base layer:
// an outline utility on a button would win over it.
func TestButtonsKeepTheFocusRing(t *testing.T) {
	for _, p := range []ButtonProps{{}, {Variant: ButtonGhost, Size: ButtonIcon}, {Href: "/"}} {
		if c := ButtonClasses(p); strings.Contains(c, "outline-") {
			t.Errorf("button classes %q remove the focus outline", c)
		}
	}
}

// TestLogFragments: the parts of a log a poll answers hold its text as
// text, and the next poll while the run lasts; the block of a reset is
// marked to replace the one shown.
func TestLogFragments(t *testing.T) {
	for _, tt := range []struct {
		name string
		c    templ.Component
	}{
		{"log-tail", LogTail(hostile+"\n", "/acme/acme/api/runs/0123/log?cut=0&offset=40&run=b")},
		{"log-tail-ended", LogTail("done\n", "")},
		{"log-reset", LogBlock(LogView{ID: "log", Text: "x\n(truncated)\n", PollURL: "/acme/acme/api/runs/0123/log?cut=1&offset=14&run=b"}, true)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.c)
			if strings.Contains(got, "<script>") {
				t.Errorf("markup came out as markup: %s", got)
			}
			golden.Equal(t, "testdata/fragment-"+tt.name+".golden", []byte(got))
			checkMarkup(t, got)
		})
	}
}
