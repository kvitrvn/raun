# Web interface

> Status: decided by the [epic 3 review](implementation/03-server-foundation.md#web-components), not implemented yet. #19 adds the web shell, the generation script and the checks below, and turns this page into the description of what exists.

The interface is rendered by the server: templ templates, HTMX for links and forms, Tailwind for the styles, and components adapted from shadcn-templ. Every page works without JavaScript, nothing is loaded from another origin, and a regular `go build` needs no frontend tool.

## Pinned tools

Each tool is pinned to an exact version: the latest release that was at least two weeks old when it was pinned. A pin moves in a pull request that updates this page and the files it lists.

| Tool | Version | How it is pinned | Use |
| --- | --- | --- | --- |
| [templ](https://github.com/a-h/templ) | v0.3.1100 (`ed4e7663123ae9169ff5b378b055a99012ba5f5a`) | `tool` and `require` in `go.mod`, checked by `go.sum` | Generates typed Go from `.templ` files; its runtime package escapes every value |
| [Tailwind CSS](https://github.com/tailwindlabs/tailwindcss) | v4.3.3 | Standalone binary, SHA-256 below, in `scripts/generate-web.sh` | Generates the CSS from `styles.css` and the templates |
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

## Components

The first components, copied from shadcn-templ and adapted (#19): button, input, table, badge, dialog, dropdown. They keep the upstream structure and classes and drop what Raun does not use: random identifiers, the class-merging helper, package state and the upstream JavaScript. Their behavior, where they need one, is in `assets/app.js`.

They live in `internal/adapter/driving/web/ui` and are Raun code from then on: an upstream change is not followed automatically.

## Content Security Policy

The policy is in the [epic 3 record](implementation/03-server-foundation.md#request-protections). It refuses inline scripts, inline `style` attributes and `eval`, so:

- no `<script>` element other than those loading `/assets/` files, and no `style` attribute in a template;
- HTMX is configured through `<meta name="htmx-config">` with `allowEval`, `allowScriptTags` and `includeIndicatorStyles` off and history snapshots off;
- the CSRF header is set by `hx-headers` on `<body>`;
- repository content is never passed to `templ.Raw`.

## Generation

```sh
./scripts/generate-web.sh
```

It runs `go tool templ generate` on the templates and the Tailwind binary on `styles.css`, downloading the binary on first use into `${RAUN_WEB_TOOLS:-${TMPDIR:-/tmp}/raun-web-tools}` and checking it against the table above. It needs Go, `curl` and, the first time, the network. The generated `*_templ.go` files and the CSS are committed.

`make verify` fails when they are stale, without the network or Tailwind: it regenerates the templ output into a temporary directory and compares it, and checks that the CSS records the SHA-256 of its current inputs (the templates, `styles.css` and the Tailwind version).

## Licenses

The license of each third-party file is kept in `internal/adapter/driving/web/licenses/`.

| Source | Version | License | File |
| --- | --- | --- | --- |
| templ (runtime package, linked) | v0.3.1100 | MIT | `licenses/templ.txt` |
| Tailwind CSS (generated CSS) | v4.3.3 | MIT | `licenses/tailwindcss.txt` |
| HTMX (`htmx.min.js`) | v2.0.10 | Zero-Clause BSD | `licenses/htmx.txt` |
| shadcn-templ (adapted components) | v2.0.0-beta.16 | MIT | `licenses/shadcn-templ.txt` |

An icon set, if the components need one, is added with its license and a row here. No font is embedded: the interface uses the system font stack.
