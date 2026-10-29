# Local knowledge browser

Run the browser from a project containing `.raun/`:

```sh
raun serve
raun serve -dir /path/to/project -port 8081
raun serve -read-only
```

The default port is 8080. Open the printed `http://127.0.0.1:<port>/knowledge`
URL. The server accepts only local connections on IPv4 loopback; the address is
not configurable. Ports must be between 1 and 65535. An occupied port is an
error. Ctrl+C or SIGTERM stops the server gracefully.

The project name is the project directory's name. A `.raun/` directory is
required; a valid agent configuration, Git repository or agent installation is
not required for consultation.

## Consultation

The browser is a split view: a sidebar, the knowledge list and the detail of one
item. `/knowledge` shows the first listed item; `/knowledge/{id}` names the item.

- The sidebar filters by type (All, Personas, Requirements) and by review status
  (Awaiting decision, Contested, Needs review), with workspace-wide counts. Its
  footer shows the mode, the Git author (`Signed as`) and the local address.
- The list shows its view, the number of matching items, a progress bar of every
  status (`X of N decided`, items blocked by a disagreement), the search field and
  the Awaiting, Validated, Rejected and All tabs.
- **Awaiting decision** (`status=pending`: proposed, contested and needs-review) is
  the default view. `status=all` includes rejected items.
- Search matches IDs, titles and business content without case sensitivity:
  persona descriptions, goals and capabilities; requirement statements, kinds,
  rationales and persona references. Agent metadata and evidence are not searched.
- Filters combine with search. Changing a filter keeps the displayed item; list
  links keep the filters.
- Pages contain 50 items. Ordering is persona then requirement; within each type,
  validated, needs-review, contested, proposed, rejected; then ID alphabetically.
  A page beyond the last page shows the last page. Invalid filter values return 400.
- The detail presents business content, related items (a requirement's personas
  in their declared order, a persona's requirements by ID), interpretations
  grouped by run (newest run first), numbered evidence excerpts (stale evidence
  first, each with the run that cited it first), every position of each
  disagreement, and the item's Activity, newest first. Section tabs lead to each
  part. Missing persona references are explicitly marked.
- Related items include rejected items, with their status visible. Evidence
  citations link to anchors within the same detail page.
- Below 1180px wide, the sidebar opens as a sheet; below 1024px, `/knowledge`
  shows the list and `/knowledge/{id}` the detail, with a link back to the list.
  Only the sidebar, list and detail scroll; the window never does.

The browser enables human decisions by default. Start it with `-read-only` to
hide decision forms and refuse decision POSTs. Viewing pages never changes files.
The server never runs agents or verifies evidence. Evidence states are **recorded
states**, not a fresh verification result; use `raun verify` to check them.

The complete knowledge base is read once per GET page request through
`knowledge.Store`. Invalid files fail the page visibly and identify the file;
partial results are not presented as a complete base. Empty bases, empty search
results and missing items have distinct messages. A missing item keeps the list
and reports the error in the detail column. Removing `.raun/` while the
server is running produces a visible error. Run manifests are read with the
knowledge base, and an invalid manifest also fails the page and names its file.

## Runs

The browser reads the run manifests (`.raun/runs/<id>/manifest.yaml`), never
their raw artifacts. A run directory without a manifest is ignored.

- The sidebar lists the 8 most recent runs, newest first, with their start time
  (UTC), commit and outcome (`12 new · 33 updated`, or why the run failed). The
  command palette lists every run.
- A run's view (`run=<id>`, every status) lists the items it created, updated or
  left unchanged because they changed during the run. Its card shows the run ID,
  start time, duration, the commit compared with the previous succeeded run, each
  agent (failed agents and retries included), what the reconciler matched, how
  much evidence went stale, the items not updated with their reason, and the
  run's project-wide questions. Chips narrow the list to one kind of change
  (`change=`); kinds the run did not make are named instead.
- Rows and the detail mark what the **active run** did: the run of the view, or
  else the latest succeeded run. Rows show `New`, `+N support`, `Stale evidence`,
  `Conflict`, `Needs review`, `Found again` (a rejected item, which stays
  rejected) and `Not updated`, and count the runs backing the item. The detail
  shows the runs that saw the item, and a "Changed by the run" banner.
- Activity merges, newest first, what each run did to the item (created or
  interpretations and evidence added, stale evidence, status changes, conflicts),
  human decisions, and resolutions of open points. A run without a manifest shows
  its status changes only.

These changes are derived; no file records them per item. A run's interpretations,
evidence first cited by its interpretations or disagreement positions, status
changes and the manifest's lists (`items`, `updated`, `needs_review`,
`rejected_supported`, `skipped`) are exact. A conflict belongs to the run of its
last position. Stale evidence is only known to be stale now: the banner shows the
stale evidence of the items the run updated, and the manifest's count gives how
much evidence that run made stale. Uncertainties and questions are not attributed
to runs.


## Human decisions

A decision bar stays at the bottom of the detail of `proposed`, `contested` and
`needs-review` items. `validated` and `rejected` items show who decided and when,
with no decision actions; a rejected item offers its `raun reopen` command to copy.

- **Reason** is required for both **Reject** and **Validate**. Submitting is the
  confirmation; there is no additional dialog. Enter in the author field never
  submits.
- **Author** is required and editable, initially taken from `git config user.name`.
  Without a Git name it is blank; an explicit author also works outside Git.
- Unresolved disagreements disable validation (`Resolve p1 to validate`) and each
  open point shows its `raun resolve <id> <point> -note "..." [-position N]`
  command. Rejection remains possible. Questions and uncertainties do not block
  validation.
- Success leads to the next item awaiting a decision in the displayed list, after
  the decided one and wrapping around, or back to the decided item when none is
  left. The new status and one human history event are recorded. Business content,
  evidence, interpretations and open points remain unchanged. On failure, the
  decision bar keeps the bounded author and reason, its error summary receives
  focus, and submission is disabled when the item must be reviewed again.
  Rejected submissions leave the item unchanged.

Resolution of open points and reopening remain CLI operations; bulk decisions
are not available in the browser.

## Keyboard and enhancements

With JavaScript, shortcuts work outside text fields:

| Key | Action |
| --- | --- |
| `/` | Focus the search field |
| `J` / `↓`, `K` / `↑` | Next or previous listed item |
| `V`, `X` | Focus the reason, or validate / reject when a reason is written |
| `⌘↵` / `Ctrl+↵` in the reason | Validate; a blocked validation shows why and never rejects |
| `⌘K` / `Ctrl+K` | Jump to a view or an item (command palette) |
| `Esc` | Leave the field, close the palette or the sheet |

Shortcut hints show `⌘` on Apple platforms and `Ctrl+` elsewhere, and are hidden
without JavaScript and on touch screens. JavaScript also provides live search,
evidence previews when hovering a citation, section tracking while scrolling,
copy buttons (ID, file path, commit, commands), the sidebar toggle, client-side
checks of author and reason, a spinner while a decision is sent and a notification
after it. The palette lists every item from the page: it adds no route. Animations
respect `prefers-reduced-motion`.

## Concurrent updates and lock recovery

Each form carries a SHA-256 fingerprint of the canonical item it displayed. No
YAML field or format version is added. `Store.Update` exclusively creates the
adjacent `<id>.yaml.lock` file, reloads and checks the revision, applies the domain
transition, then atomically replaces the YAML file and removes the lock. CLI
`accept`, `reject`, `reopen` and `resolve` use this same operation; `Store.Save`
also observes the lock.

A busy lock or changed revision returns a conflict without a write or automatic
retry. The browser retains author and reason but disables submission: reload and
review the item before deciding again. This covers double submissions, separate
tabs and Raun processes. External editors do not participate in locking and must
not edit an item during a Raun write.

A crash can leave a lock behind. **Never remove a lock while its writer may still
be active.** Stop the Raun servers and commands using that workspace, confirm no
Raun process is still writing, inspect the named YAML file, then remove only its
adjacent `.yaml.lock` file. Reload and review before retrying. Raun never expires
or removes another process's lock automatically. Lock files contain no knowledge
and are not part of the versioned knowledge format.


## Navigation and local resources

`/` redirects to `/knowledge`. The list and details accept `q`, `type`, `status`
(`pending` by default, `all` or a knowledge status), `run` (a run ID), `change`
(`new`, `updated`, `stale`, `conflict`, `needs-review`, `rejected-again` or
`skipped`; it requires `run`) and `page`. An unknown run returns 400. Details live
at `/knowledge/{id}`; sections use `#sec-overview`, `#sec-interp`, `#sec-evidence`,
`#sec-points` and `#sec-history` (Activity); citations use `#evidence-{id}`.

Every page and form works without JavaScript. HTMX enhances links and forms
by replacing the application region and updating browser history; live search
replaces the current history entry. History snapshots
are disabled, and back/forward navigation requests current data. Reload or
navigate after changing files with the CLI; there is no polling or file watcher.
An error response also replaces the main region so disk errors remain visible.
Decision forms disable HTMX history updates during POSTs; successful HTML
submissions return `303` to the next item's GET, while HTMX receives `200` and
[`HX-Redirect`](https://htmx.org/headers/hx-redirect/). Only that GET URL is added
to history after a decision. The notification survives this navigation through
`sessionStorage`, only in the same tab, and is removed once shown.

CSS, JavaScript and fonts are embedded and served under `/assets/`. No CDN, external
font or internet access is required at runtime. The Content Security Policy allows
only same-origin scripts, stylesheets, images and fonts; inline styles and scripts
are refused. No route exposes repository
files or raw `.raun/` files. File content is escaped by templ. GET and HEAD are
read-only; the only write routes are `POST /knowledge/{id}/accept` and
`POST /knowledge/{id}/reject`. They use a random server-specific CSRF token and
Go's [CrossOriginProtection](https://pkg.go.dev/net/http#CrossOriginProtection),
in addition to refusing non-loopback Host and cross-site fetch metadata.

POST forms accept only `csrf`, `revision`, `author`, `reason` and the optional
`next`, once each, as URL-encoded body fields (maximum 64 KiB). Unknown, duplicate
and query fields are refused. `next` must be `/knowledge/{id}` with only `q`, `type`,
`status`, `run`, `change` and `page` parameters; anything else is refused before a write, so a form
cannot redirect outside the browser. Browser-supplied target statuses or actor types are never accepted.
Responses use `422` for invalid input or disallowed decisions, `409` for conflicts,
`404` for a missing item, `403` for forbidden access (including read-only mode),
and `500` for disk errors with the file path. Malformed or oversized bodies may
not be recoverable as form fields. The interface is intended for the local user's
browser and does not provide network sharing or authentication.

## Development and generation

Ordinary `go build`, `go install` and Go tests use committed generated files.
There is no Node.js requirement. After editing `.templ` or `styles.css`, run:

```sh
./scripts/generate-web.sh
```

This requires Go and curl, and network access on the first invocation. Tailwind's
standalone binary is downloaded to `${RAUN_WEB_TOOLS:-${TMPDIR:-/tmp}/raun-web-tools}`
and checked against a pinned SHA-256 digest. The script supports Linux glibc and
macOS on x64 and arm64. Commit the generated `*_templ.go` files and
`internal/web/assets/app.css`. Run generation twice and compare checksums to
check reproducibility. The generator scans only the application templates.

Pinned dependencies:

| Dependency | Version | Purpose |
| --- | --- | --- |
| [templ](https://templ.guide/quick-start/installation/) | v0.3.1070 | Typed, escaped HTML rendering; the only added Go runtime dependency |
| [Tailwind CSS](https://tailwindcss.com/docs/installation/tailwind-cli) | v4.3.3 | Standalone CSS generation |
| [HTMX](https://htmx.org/docs/) | v2.0.11 | Local HTML navigation enhancement |
| [shadcn-templ](https://github.com/axadrn/shadcn-templ/tree/d50c6a60c9373deb9ed94871e5eb5ac6b145128f) | v2.0.0-beta.13 | Owned, adapted UI primitives |

`internal/web/ui/` adapts these shadcn-templ components: Sidebar (with its sheet
below 1180px), Breadcrumb, Button (default, outline, ghost; default, sm, icon),
Badge, Card, Input and Input Group, Textarea, Tabs (segmented and line), Kbd, Item,
Empty, Separator, Hover Card, Command in a native `<dialog>`, Toast, Spinner,
Pagination and Icon. They keep the upstream `data-slot` structures and `cn-*`
classes, with application-specific APIs. The look reproduces the "Raun Knowledge"
design with shadcn semantic tokens (neutral base, Raun teal `#185f51` as primary)
and status and evidence tokens in `styles.css`. Unused variants, random IDs,
class-merging utilities, package state and the upstream JavaScript are removed:
`assets/app.js` provides the behavior. Tabs that navigate are links marked with
`aria-current`. Pagination retains native link semantics and visible text on small
screens. These are maintained source adaptations, not generated installations of
the upstream component registry. Their MIT license is in `internal/web/licenses/`.

Icons are the 27 Lucide 0.576.0 icons the browser uses, copied from the pinned
shadcn-templ registry into `ui/icon_data.go` (ISC license in
`internal/web/licenses/lucide.txt`). The Content Security Policy refuses inline
`style` attributes: progress segments use the generated `grow-1` to `grow-100`
classes.

HTMX is the unchanged upstream `dist/htmx.min.js` from tag `v2.0.11`, SHA-256
`d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`.
Its license and Tailwind's license are retained alongside the component license.
The Geist and Geist Mono fonts are the Geist 1.5.0 files shipped with the pinned
shadcn-templ assets: Geist's variable latin and latin-ext subsets, and the complete
variable Geist Mono (its subsets only provide weight 400). Their SIL Open Font
License is in `internal/web/licenses/geist.txt`.
`assets/app.js` handles focus, announcements, browser cache restoration and the
enhancements listed above.

## Verification

Go tests cover filtering (including the pending view and run views), the
attribution of changes to runs, Activity, run manifests that are missing or
invalid, counts and progress,
pagination, the split view and its links, HTML and fragments, missing items in the
shell, all recorded statuses and source kinds, disagreement positions, human
decisions and the next item, `next` validation, escaping, absence of inline styles,
read errors, required decision fields, immutable business content, terminal
statuses, CSRF/origin checks, read-only access, revision conflicts, CLI options,
occupied ports and graceful shutdown. Storage tests cover simultaneous updates in goroutines
and separate processes, canonical revisions, and lock cleanup after errors.
Tests do not use agents or the internet; server lifecycle tests use loopback
sockets and an isolated CLI subprocess.

For browser review, use a disposable project containing the knowledge fixtures
from `internal/knowledge/testdata/`. Check:

1. Direct list/detail URLs and evidence anchors, with and without JavaScript.
2. Search, combined filters, pagination and browser back/forward.
3. A CLI decision followed by navigation, back/forward and reload.
4. Empty, missing and invalid files, including errors reached through HTMX.
5. No requests outside loopback; no browser history snapshots in local storage.
6. Keyboard access and shortcuts, visible focus, announced navigation, the
   command palette, citation previews, section tracking, the sheet below 1180px
   and the stacked list and detail at 375px; the window itself never scrolls.
7. Validation and rejection with and without JavaScript; required fields,
   unresolved disagreements, error focus and retained input.
8. Two tabs with the same item, then a CLI update between display and submission;
   conflicts require review, never a silent retry.
9. On a knowledge base with two runs: the sidebar runs, the run card and its
   chips, row badges, the detail's banner, interpretations grouped by run and
   Activity; a failed run; a workspace without `.raun/runs`.
10. Reload and back/forward after decisions, with the new status, one human
   history event, the next pending item and its notification; `-read-only`
   hides forms and refuses manual POSTs.
