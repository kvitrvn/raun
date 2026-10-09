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

- The home page lists knowledge, with workspace-wide totals for every status.
- Search matches IDs, titles and business content without case sensitivity:
  persona descriptions, goals and capabilities; requirement statements, kinds,
  rationales and persona references. Agent metadata and evidence are not searched.
- Type and status filters combine with search. Rejected items are hidden by
  default. Choose `rejected` or `All statuses` to include them.
- Pages contain 50 items. Ordering is persona then requirement; within each type,
  validated, needs-review, contested, proposed, rejected; then ID alphabetically.
  A page beyond the last page shows the last page. Invalid filter values return 400.
- Each detail page presents business content, related items, interpretations,
  numbered evidence excerpts, every position of each disagreement, resolutions
  and status history. Missing persona references are explicitly marked.
- Related items include rejected items, with their status visible. Evidence
  citations link to anchors within the same detail page.

The browser enables human decisions by default. Start it with `-read-only` to
hide decision forms and refuse decision POSTs. Viewing pages never changes files.
The server never runs agents or verifies evidence. Evidence states are **recorded
states**, not a fresh verification result; use `raun verify` to check them.

The complete knowledge base is read once per GET page request through
`knowledge.Store`. Invalid files fail the page visibly and identify the file;
partial results are not presented as a complete base. Empty bases, empty search
results and missing items have distinct messages. Removing `.raun/` while the
server is running produces a visible error.


## Human decisions

The **Human decision** link in a persona or requirement header leads to a form
after open points and before history. It is available for `proposed`, `contested`
and `needs-review` items. `validated` and `rejected` items have no decision actions.

- **Author** is required and editable, initially taken from `git config user.name`.
  Without a Git name it is blank; an explicit author also works outside Git.
- **Reason** is required for both **Validate proposal** and **Reject proposal**.
  Submitting is the confirmation; there is no additional dialog.
- Unresolved disagreements disable validation and show the corresponding
  `raun resolve <id> <point> -note "..." [-position N]` commands. Rejection remains
  possible. Questions and uncertainties do not block validation.
- Success returns to the refreshed detail page, showing the new status and one
  human history event. Business content, evidence, interpretations and open points
  remain unchanged. On failure, bounded form values are retained, the error
  summary receives focus. Rejected submissions leave the item unchanged.

Resolution of open points and reopening remain CLI operations; bulk decisions
are not available in the browser.

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

`/` redirects to `/knowledge`. The list accepts `q`, `type`, `status` and `page`.
Details live at `/knowledge/{id}`; citations use `#evidence-{id}`.

Every page and form works without JavaScript. HTMX enhances links and forms
by replacing the main region and updating browser history. History snapshots
are disabled, and back/forward navigation requests current data. Reload or
navigate after changing files with the CLI; there is no polling or file watcher.
An error response also replaces the main region so disk errors remain visible.
Decision forms disable HTMX history updates during POSTs; successful HTML
submissions return `303` to the detail GET, while HTMX receives `200` and
[`HX-Redirect`](https://htmx.org/headers/hx-redirect/). Only the GET detail URL is
added to history after a decision.

CSS and JavaScript are embedded and served under `/assets/`. No CDN, external
font or internet access is required at runtime. No route exposes repository
files or raw `.raun/` files. File content is escaped by templ. GET and HEAD are
read-only; the only write routes are `POST /knowledge/{id}/accept` and
`POST /knowledge/{id}/reject`. They use a random server-specific CSRF token and
Go's [CrossOriginProtection](https://pkg.go.dev/net/http#CrossOriginProtection),
in addition to refusing non-loopback Host and cross-site fetch metadata.

POST forms accept only `csrf`, `revision`, `author` and `reason`, once each, as
URL-encoded body fields (maximum 64 KiB). Unknown, duplicate and query fields are
refused. Browser-supplied target statuses or actor types are never accepted.
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

`internal/web/ui/primitives.templ` retains the button, badge, card, input and
pagination structures from shadcn-templ, with application-specific APIs and CSS.
Unused variants, random IDs, class-merging utilities and icons are removed.
Pagination retains native link semantics and visible text on small screens.
These are maintained source adaptations, not generated installations of the
upstream component registry. Their MIT license is in `internal/web/licenses/`.

HTMX is the unchanged upstream `dist/htmx.min.js` from tag `v2.0.11`, SHA-256
`d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`.
Its license and Tailwind's license are retained alongside the component license.
`assets/app.js` handles focus, announcements and browser cache restoration.

## Verification

Go tests cover filtering, pagination, HTML and fragments, links, all recorded
statuses and source kinds, disagreement positions, human decisions, escaping,
read errors, required decision fields, immutable business content, terminal statuses,
CSRF/origin checks, read-only access, revision conflicts, CLI options, occupied
ports and graceful shutdown. Storage tests cover simultaneous updates in goroutines
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
6. Keyboard access, visible focus, announced navigation and a 375px viewport.
7. Validation and rejection with and without JavaScript; required fields,
   unresolved disagreements, error focus and retained input.
8. Two tabs with the same item, then a CLI update between display and submission;
   conflicts require review, never a silent retry.
9. Reload and back/forward after decisions, with the new status and one human
   history event; `-read-only` hides forms and refuses manual POSTs.
