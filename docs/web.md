# Web interface

The interface is rendered by the server: templ templates, HTMX for links and forms, Tailwind for the styles, and components adapted from shadcn-templ. Every page works without JavaScript, nothing is loaded from another origin, and a regular `go build` needs no frontend tool. The choices are recorded in the [epic 3 record](implementation/03-server-foundation.md#web-components).

## Files

All of it lives in `internal/adapter/driving/web`:

| Path | Content |
| --- | --- |
| `ui/*.templ` | The templates: components, icons, layout, page states, pages |
| `ui/*_templ.go` | Their generated Go code, committed |
| `ui/styles.css` | The Tailwind input: tokens, light and dark values, component classes |
| `assets/app.css` | The generated stylesheet, committed |
| `assets/app.js` | The behavior: theme toggle, dialog fallback, dropdown placement and keys |
| `assets/theme.js` | Sets the theme before the page paints |
| `assets/htmx.min.js` | HTMX, unchanged |
| `assets/favicon.svg` | The icon |
| `assets/fonts/*.woff2` | Onest and Spline Sans Mono, latin and latin-ext subsets |
| `licenses/` | The license of every third-party file |

## Pinned tools

Each tool is pinned to an exact version: the latest release that was at least two weeks old when it was pinned. A pin moves in a pull request that updates this page and the files it lists.

| Tool | Version | How it is pinned | Use |
| --- | --- | --- | --- |
| [templ](https://github.com/a-h/templ) | v0.3.1100 (`ed4e7663123ae9169ff5b378b055a99012ba5f5a`) | `tool` and `require` in `go.mod`, checked by `go.sum` | Generates typed Go from `.templ` files; its runtime package escapes every value |
| [Tailwind CSS](https://github.com/tailwindlabs/tailwindcss) | v4.3.3 | Standalone binary, SHA-256 below, in `scripts/generate-web.sh` | Generates the CSS from `ui/styles.css` and the templates |
| [HTMX](https://github.com/bigskysoftware/htmx) | v2.0.10 | `dist/htmx.min.js` of the tag, committed unchanged, SHA-256 below | Enhances links and forms |
| [shadcn-templ](https://github.com/axadrn/shadcn-templ) | v2.0.0-beta.16 (`88e25341331df34fcfe031f51416b132be30c2c6`) | Components copied from this commit and adapted | Source of the components |

Tailwind standalone binaries of v4.3.3, from the digests GitHub publishes for the release assets:

| Platform | Asset | SHA-256 |
| --- | --- | --- |
| Linux x64 (glibc) | `tailwindcss-linux-x64` | `dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a` |
| Linux arm64 (glibc) | `tailwindcss-linux-arm64` | `55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195` |
| macOS x64 | `tailwindcss-macos-x64` | `7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1` |
| macOS arm64 | `tailwindcss-macos-arm64` | `cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d` |

HTMX v2.0.10, `dist/htmx.min.js`: `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de`.

## Fonts

Onest (interface) and Spline Sans Mono (code, paths, identifiers), both under the SIL Open Font License 1.1. The files are the variable WOFF2 subsets Google Fonts serves for the latin and latin-ext ranges, downloaded once and committed; each name carries the first 8 hexadecimal digits of its SHA-256, which the server checks at start.

| File | Source | SHA-256 |
| --- | --- | --- |
| `onest-latin.052df745.woff2` | `fonts.gstatic.com/s/onest/v11/gNMKW3F-SZuj7xmf-HY.woff2` | `052df74533250c2e0ca0c4bdd32de594e107d99c1648e63820ef9c6142067828` |
| `onest-latin-ext.14e7d307.woff2` | `fonts.gstatic.com/s/onest/v11/gNMKW3F-SZuj7xmR-HY6EQ.woff2` | `14e7d3079b75860e2ab50efc6c318c398fdb46ede9b56e625f12ae70a3c5d9a4` |
| `spline-sans-mono-latin.46b7dcaf.woff2` | `fonts.gstatic.com/s/splinesansmono/v13/R70BjzAei_CDNLfgZxrW6wrZOF2WX5KZmA.woff2` | `46b7dcafe3e51dbe87be1bafaac3fed7646db1fe0a146647a6ffce3699f2752d` |
| `spline-sans-mono-latin-ext.0ca9a398.woff2` | `fonts.gstatic.com/s/splinesansmono/v13/R70BjzAei_CDNLfgZxrW6wrZOF2WX5yZmF2a.woff2` | `0ca9a3989044d1f9b4da1f303cea3c3091f5cb02fe54da04fd51fceaf676ecf9` |

## Design tokens

`ui/styles.css` defines the tokens under the shadcn names (`--background`, `--foreground`, `--primary`, `--muted`, `--border`, `--ring`, ...), plus Raun's: `--ground` behind the cards, the tones `--info`, `--warning`, `--success`, `--danger`, `--neutral` and their `-ink` text colours. Each value holds its light and its dark form through `light-dark()`: the system decides, and `data-theme` on `<html>` overrides it. `@theme` maps them to Tailwind utilities (`bg-card`, `text-muted-foreground`, `text-success`, ...).

Warm neutrals and a cobalt accent; radii of 4 px (tags), 6 px (controls) and 8 px (cards); soft shadows. A status is a filled disc with a white glyph and always a word next to it, never colour alone.

## Components

In `ui`, adapted from shadcn-templ at the pinned commit. They keep the upstream `data-slot` structure and `cn-*` classes, whose look is defined in `ui/styles.css`, and drop the class-merging helper, generated identifiers, package state and the upstream JavaScript. They are Raun code from then on: an upstream change is not followed automatically.

| Component | Templates | Notes |
| --- | --- | --- |
| Button | `Button`, `ButtonClasses` | Primary, success, outline, ghost, danger; small, medium, large, icon. A link stays a link; an icon-only button takes a `Label` |
| Badge | `Badge` | Default, primary, mono, outline, and the tones |
| Input | `Input`, `Field` | `Field` holds the label and the error, which `Input` names through `aria-describedby` when invalid |
| Table | `Table`, `TableCaption`, `TableHeader`, `TableBody`, `TableRow`, `TableHead`, `TableCell` | A head cell is a column header; the table scrolls inside its container |
| Dialog | `Dialog`, `DialogTrigger`, `DialogClose`, `DialogFooter` | The native `<dialog>`, opened by the platform's invoker commands (`commandfor`, `command`), and by `app.js` where a browser lacks them |
| Dropdown menu | `DropdownTrigger`, `DropdownContent`, `DropdownItem`, `DropdownSeparator`, `DropdownLabel`, `DropdownShortcut` | A native popover: it opens and closes without script, centred in the viewport; `app.js` places it under or above its trigger and moves focus with the arrow keys |

Raun's own: `Icon` (the icon set, drawn as markup), `Toast`, `Layout` (document, skip link, top bar with the workspace switcher and the account, flash messages, main region), `Empty`, `ErrorState`, `StateBadge` (the state of a repository: active, deactivated, unreachable), and the pages `HomePage`, `NotFoundPage`, `ErrorPage`, `LoginPage`, `SignupPage`, `WorkspacesPage`, `WorkspacePage`, `RepositoriesPage`, `RepositoryPage` and `InstancePage`. The workspace switcher is a dropdown of the workspaces of the account logged in, the current one marked, with a link to all of them; it follows links without JavaScript.

## Repositories

The Repositories screen is `/` for an account logged in: the repositories the forge lists for it through the App, searched by full name with `?q=`, each with a link to the forge and, once Raun knows it, its state and its workspace; one never activated has a form that activates it in a workspace of the account, `POST /repositories` with its stable identifier and the workspace, the full name and the branch coming from the forge. While the forge lacks files, webhooks or events, the screen says so and offers no activation. The page of a repository is `/<workspace>/<owner>/<name>`, at its current full name: it shows to whoever the forge lets read the repository, member of the workspace or not, and is not found otherwise, as an old name is. It shows the 50 newest rows of its event log, newest first, read-only once deactivated. A member the forge lets decide deactivates it there (`POST /<workspace>/<owner>/<name>/deactivate`) or activates it again. The page of a workspace lists its repositories the forge lets the visitor read. A forge that does not answer gives `503`, one that refuses the tokens `403`.

## Content Security Policy

The policy is in the [epic 3 record](implementation/03-server-foundation.md#request-protections); #20 sends it. The templates are written for it, and the tests of `ui` check every rendered page and component:

- no `style` attribute and no event handler attribute;
- no `<script>` element without `src`, and nothing loaded from another origin;
- HTMX is configured through `<meta name="htmx-config">` with `allowEval`, `allowScriptTags` and `includeIndicatorStyles` off, history snapshots off and requests to the same origin only;
- `templ.Raw` is used nowhere in the module, under any import name: a test parses every Go file, generated ones included.

`app.js` sets style properties from script to place a menu, which the policy allows; it never builds markup from strings.

## Assets

The server reads the embedded files at start and serves each under `/-/assets/` at a URL that carries the first 8 hexadecimal digits of its SHA-256 (`/-/assets/app.1a2b3c4d.css`), with `Cache-Control: public, max-age=31536000, immutable` and `X-Content-Type-Options: nosniff`. Fonts carry the hash in their file name, since the stylesheet names them; a font whose name does not match its content stops the server. Any other path below `/-/assets/`, a stale hash included, is not found. Pages are `Cache-Control: no-store`.

`POST /hooks/github`, the hook of the forge, is served by the root mux too, behind the same three: it authenticates by its signature alone and reads its body up to 5 MiB, past the bound of the application ([docs/forge.md](forge.md#webhooks-and-events)). `hooks` is a reserved name.

`/-/` holds the paths of Raun that no name can take, since a name never starts with a hyphen. The root mux serves `/-/assets/`, behind the Host check, the access log and the security headers, with no session, origin check or CSRF token; its pattern never meets those of the application, such as `GET /{name}/{owner}/{repo}` (epic 5 record).

## Generation

```sh
./scripts/generate-web.sh
```

It runs `go tool templ generate` on `ui`, after removing every generated file so that none is left behind, then the Tailwind binary on `ui/styles.css`, downloading the binary on first use into `${WEB_TOOLS_DIR:-${TMPDIR:-/tmp}/raun-web-tools}` and checking it against the table above on every run. It needs Go, `curl` and, the first time, the network. Commit the generated `ui/*_templ.go` files and `assets/app.css`. Two runs give the same bytes.

The first line of `assets/app.css` records a SHA-256 of what produced it: the Tailwind version, then the SHA-256 and the path of `ui/styles.css` and of each template. The tests of `ui`, run by `make verify`, fail without the network or Tailwind when:

- a generated template differs from what the pinned templ produces now, or one is missing or left over;
- the recorded hash differs from the current inputs, or the stylesheet was not produced by the pinned Tailwind version;
- `assets/htmx.min.js` differs from the pinned file.

The stylesheet check proves the inputs have not changed since the last generation, not that Tailwind produced exactly the committed bytes: review the stylesheet diff in pull requests.

## Licenses

The license of each third-party file is kept in `internal/adapter/driving/web/licenses/`.

| Source | Version | License | File |
| --- | --- | --- | --- |
| templ (runtime package, linked) | v0.3.1100 | MIT | `licenses/templ.txt` |
| Tailwind CSS (generated CSS) | v4.3.3 | MIT | `licenses/tailwindcss.txt` |
| HTMX (`htmx.min.js`) | v2.0.10 | Zero-Clause BSD | `licenses/htmx.txt` |
| shadcn-templ (adapted components) | v2.0.0-beta.16 | MIT | `licenses/shadcn-templ.txt` |
| Onest (font) | Google Fonts v11 | SIL Open Font License 1.1 | `licenses/onest.txt` |
| Spline Sans Mono (font) | Google Fonts v13 | SIL Open Font License 1.1 | `licenses/spline-sans-mono.txt` |

The icons are Raun's, drawn for it.
