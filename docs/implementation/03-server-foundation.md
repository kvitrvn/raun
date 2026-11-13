# Epic 3: server foundation

Epic: #16. Review: #17. Decided by the owner on 2026-11-12.

## Scope

Turn `raun server` into a long-lived process that later epics can expose safely: its configuration, its database and migrations, structured logs, health endpoints, the web shell (layout, components, embedded assets) and the request protections (sessions, CSRF, origin and Host checks, security headers). Identity, forges and jobs come next (epics 4 to 6) on this base.

The review also settles the administrator, the instance settings and secrets at rest. None of them has a consumer before accounts exist, so their code moves to epic 4: see [Administrator](#administrator) and [Sub-issues](#sub-issues).

Sources consulted: `kvitrvn/raun-old` at `330e634` (`docs/web.md`, `internal/web/server.go`, `scripts/generate-web.sh`), as a reference only: nothing is imported. Issues #16 and #18 to #21, the [threat model](../threat-model.md) (T10, T11, T13, T17, T18), and the records of [epic 1](01-project-foundation.md#server-configuration) and [epic 2](02-knowledge-core.md#migrations).

The pinned frontend tools, their checksums and their licenses are in [docs/web.md](../web.md). This page records why.

## Choices

### HTTP stack

The standard library: `net/http` and its `ServeMux` with method and wildcard patterns. No router or middleware dependency: the routes of epics 3 to 6 are a few dozen fixed patterns, which `ServeMux` covers, and every middleware below is a few lines over `http.Handler`.

The server code lives in `internal/adapter/driving/web`, created by #18; `cmd/raun` reads the configuration, builds the adapters and the `http.Server`, and runs it.

Middleware, from the outside in:

1. Request identifier and access log.
2. Panic recovery: a `500` with the request identifier, the stack in the log.
3. Security headers, on every response.
4. Host check (see [Public address](#public-address)).
5. Body limit.
6. Cross-origin protection.
7. Session lookup, on the routes that use one.
8. CSRF token check, on the non-safe methods of those routes.
9. The handler.

The health endpoints are mounted outside steps 4 to 8: a probe reaches the server by an address that is not the public one, and the endpoints read nothing a visitor could not.

Limits are constants of the server, written in its code and in `README.md`. Only the shutdown deadline is a setting, since it depends on what runs the process.

| Limit | Value | When exceeded |
| --- | --- | --- |
| `ReadHeaderTimeout` | 5 s | The connection is closed |
| `ReadTimeout` | 30 s | The connection is closed |
| `WriteTimeout` | 60 s | The response is cut |
| `IdleTimeout` | 120 s | The idle connection is closed |
| `MaxHeaderBytes` | 32 KiB | `431` |
| Request body | 64 KiB by default, through `http.MaxBytesReader`; a route states a larger bound explicitly (webhooks, the runner API) | The read fails with `*http.MaxBytesError`, which the form decoder answers with `413`; `net/http` writes no status itself |
| Shutdown | `RAUN_SHUTDOWN_TIMEOUT`, 10 s by default | In-flight requests are dropped and the server exits with an error |

A form is decoded strictly, as raun-old did: URL-encoded body fields only, each expected field once, unknown, duplicate and query fields refused with `400`.

### Configuration

The variables of epic 3, read by `raun server` only. Each has a default and a rule; an invalid value stops the server at startup with a message naming the variable (epic 1).

| Variable | Default | Rule |
| --- | --- | --- |
| `RAUN_HTTP_ADDR` | `:8080` | `host:port` as `net.Listen` accepts it, port 1 to 65535 |
| `RAUN_PUBLIC_URL` | `http://localhost:8080` | See [Public address](#public-address) |
| `RAUN_TLS_CERT_FILE` | empty | Both set or both empty; readable PEM files forming a valid pair |
| `RAUN_TLS_KEY_FILE` | empty | As above |
| `RAUN_DATABASE_PATH` | `raun.db` | A file path, refused by the SQLite adapter if it holds `?` or `#`, is a `file:` URI or `:memory:` (epic 2) |
| `RAUN_DATABASE_MIGRATE` | `false` | `true` or `false` exactly |
| `RAUN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RAUN_LOG_FORMAT` | `json` | `json` or `text` |
| `RAUN_SHUTDOWN_TIMEOUT` | `10s` | A Go duration from `1s` to `5m` |

Rules that hold for every variable, of this epic and the next:

- **The catalogue grows by epic.** Epic 4 adds the forge credentials of the instance (#25, named by its review #23), `RAUN_SECRET_KEY` and `RAUN_SECRET_KEY_PREVIOUS` with #26, which stores the first secrets, `RAUN_SIGNUP_USERS` and `RAUN_SIGNUP_ORGS` with #26, and `RAUN_ADMINS` with #21, each secret with its `_FILE` form; until then they are unknown, like any variable a later epic defines.
- **Unknown variables stop the server.** A variable whose name starts with `RAUN_` and that the server does not know stops it at startup, so a typo is never silently ignored (#18). The variables of the runner start with `RAUN_RUNNER_` and are ignored by the server, so one environment can hold both. A tool of the repository, such as the generation script of #19, reads no `RAUN_*` variable.
- **Secrets from files.** A secret variable `RAUN_<NAME>` can also be given as a file, `RAUN_<NAME>_FILE`; setting both is an error (epic 1). Epic 3 defines no secret variable; the first ones are the forge credentials of epic 4.
- **The effective configuration** is logged once at startup, each secret replaced by `[redacted]` whether set or not.

### Startup and migrations

Order: read and validate the whole configuration, build the logger, open the database, call `(*sqlite.Store).Migrate(ctx, enabled)` with `RAUN_DATABASE_MIGRATE`, open the listener, serve. A failure at any step stops the process with a non-zero exit code before the listener opens.

Migrations stay as the epic 2 record decided: at start, only when `RAUN_DATABASE_MIGRATE=true`, never on first query; a newer schema always stops the server. #18 said "an explicit command or flag"; the variable is that switch, and there is no separate command.

On `SIGINT` or `SIGTERM` the server reports itself not ready, stops accepting connections, waits for in-flight requests up to `RAUN_SHUTDOWN_TIMEOUT`, then closes the database. On `SIGHUP` it reloads the TLS certificate and key; a pair that fails to load is logged and the previous one is kept.

### Health endpoints

- `GET /healthz`: `200 ok` while the process serves. It touches nothing else.
- `GET /readyz`: `200 ok` when the database answers and its schema is the one of the binary; `503 unavailable` otherwise and during shutdown.

Both are unauthenticated, outside the Host check and the session, answer `text/plain` and give no version, error or path: a failure is in the log, not in the body.

### Public address

`RAUN_PUBLIC_URL` is the address people and forges use: it builds absolute links, OAuth callbacks (epic 4) and webhook URLs (epic 5). It is validated at startup:

- an absolute URL with scheme `https`, or `http` only when its host is a loopback address (`localhost`, `127.0.0.0/8`, `::1`), for development;
- no user information, query or fragment, and no path other than empty or `/`: the instance is served at the root of its host, since its links, OAuth callbacks and webhook URLs are built from this address, and its `Path=/` session cookie would reach anything else served on that host. A deployment under a prefix is not supported.

**No header from a proxy is trusted.** `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Host` and `Forwarded` are ignored. The scheme and the host come from `RAUN_PUBLIC_URL`, and the access log records the address of the TCP peer. A client can therefore not change what the server thinks its own address is.

**Host check.** Except on the health endpoints, a request whose `Host` header differs from the host and port of `RAUN_PUBLIC_URL` (the default port of the scheme omitted) is refused with `421` before any handler runs. A proxy forwards the original `Host`, or the administrator sets `RAUN_PUBLIC_URL` to the address the proxy sends. `RAUN_PUBLIC_URL` does not follow `RAUN_HTTP_ADDR`: changing the port of the listener without the public URL leaves only the health endpoints reachable, so the refusal is logged with the host received and the host expected.

**TLS.** The server speaks plain HTTP by default, for a reverse proxy that terminates TLS. With `RAUN_TLS_CERT_FILE` and `RAUN_TLS_KEY_FILE`, it serves TLS itself, TLS 1.2 at least, so a minimal deployment needs no proxy. It does not obtain certificates; an ACME client renews the files and sends `SIGHUP`.

### Sessions

Server-side, in the database: a session is a row, and the browser holds only its identifier.

- **Identifier.** 32 bytes from `crypto/rand`, sent base64url-encoded in the cookie. The database stores its SHA-256 only, so a copy of the database file opens no session (#20). Never derived from user data.
- **Lifetimes.** A session expires after 7 days without a request or 30 days after creation, whichever comes first. Both are constants. The time of last use is written at most once an hour per session, so reading pages does not write on every request.
- **Renewal.** A new identifier, and a new CSRF token, replace the old ones at login and at any change of privilege (epic 4); the old row is deleted in the same transaction. The absolute lifetime is not extended.
- **Revocation.** Deleting the row: on logout and when epic 4 removes an account's sessions. Expired rows are deleted at start and then every hour.
- **Creation.** A session is created only by a route that needs one, the login of epic 4 first. A page served to a visitor without a session sets no cookie, and every state-changing route requires a session.
- **Identity.** A session holds no account until epic 4 adds the column and attaches it (#20).
- **Cookie.** `__Host-raun-session`, `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`, and `Max-Age` set to what remains of the absolute lifetime: the server decides expiry, and the browser keeps the cookie no longer than the session can live. `Lax` and not `Strict`: the forge sends the browser back to the OAuth callback with a top-level GET from another site, which a `Strict` cookie would not follow. On an `http` loopback address the cookie is `raun-session` without `Secure`, since browsers do not all treat `http://localhost` as secure; the other attributes stay.

The rules (identifier, hash, lifetimes, expiry) are pure and live in a core context, `internal/core/session`, which declares the store port; the table is added by a new numbered migration of the SQLite adapter (`0002_sessions.sql`), never by changing `0001_knowledge.sql`. The middleware lives in the web adapter.

### Request protections

These answer T11. They apply to every route by default; a route opts out only by being named in the server's list of bypass patterns, each with its own authentication (webhooks in epic 5, the runner API in epic 6). Epic 3 bypasses none.

- **Cross-origin protection.** `http.CrossOriginProtection` from the standard library, on every route: a non-safe request whose `Sec-Fetch-Site` is `cross-site` or `same-site` is refused with `403`, and one with `same-origin` or `none` is accepted without reading `Origin`. Only when `Sec-Fetch-Site` is absent, an `Origin` whose host differs from `Host` is refused. A request with neither header is allowed, as a non-browser client or a browser too old to send them; the CSRF token covers it.
- **CSRF token.** Each session has a token of 32 random bytes, distinct from its identifier, stored in clear in its row and replaced together with the identifier. Every non-safe request on a route that uses a session must carry it, as the form field `csrf` or the header `X-CSRF-Token`; it is compared in constant time, and a missing or wrong token is refused with `403`. HTMX sends the header on every request through `hx-headers` on `<body>`, an attribute the Content Security Policy allows; forms also carry the hidden field, so they work without JavaScript.
- **Safe methods change nothing.** `GET`, `HEAD` and `OPTIONS` never change state, since every protection above lets them through. The one exception is the login of epic 4, whose flow the forge drives with top-level `GET` requests: `GET /login` gives a browser without an account a session and a login state, and `GET /login/callback` renews that session and attaches the account, only with the single-use state issued to that same session. `GET /login` never replaces or downgrades a session that already has an account ([epic 4 record](04-forge-port-and-identity.md#accounts-and-login)).
- **Rendering.** Repository text, agent output and logs are rendered as escaped text by templ (T13). `templ.Raw` is refused everywhere by a test of `make verify`, whatever name templ is imported under; icons are written as markup, so there is no exception.

Security headers on every response, pages, fragments and errors alike:

| Header | Value |
| --- | --- |
| `Content-Security-Policy` | `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `no-referrer` |
| `Cross-Origin-Opener-Policy` | `same-origin` |
| `Strict-Transport-Security` | `max-age=31536000`, only when `RAUN_PUBLIC_URL` is `https` |
| `Cache-Control` | `no-store` on pages and fragments; assets as in [Web components](#web-components) |

The policy refuses inline scripts, inline `style` attributes, `eval` and every other origin. HTMX is configured for it through `<meta name="htmx-config">`: `allowEval` and `allowScriptTags` off, `includeIndicatorStyles` off (it would inject a `<style>` element), history snapshots off. Components keep their behavior in `assets/app.js` and use classes, never `style` attributes.

### Web components

As #16 proposed: templ for typed and escaped templates, HTMX to enhance links and forms, Tailwind for the styles, and components from shadcn-templ.

- **Copied and adapted, not imported.** shadcn-templ distributes its components to be copied into the project. They are copied from a pinned commit into `internal/adapter/driving/web/ui`, keep their structure and classes, and are trimmed to what Raun uses: no random identifiers, no class-merging helper, no package state, no upstream JavaScript. They are maintained as Raun code from then on.
- **Pinned versions.** templ, Tailwind and HTMX are pinned to exact versions in [docs/web.md](../web.md): the latest release at least two weeks old, as for Go modules (epic 2). HTMX stays on the 2.x line, which raun-old ran under the same policy; the 4.x line is two months old and still changing. A pin moves in a pull request that updates `docs/web.md`.
- **templ is a tool and a library.** The generator is a `tool` in `go.mod`, run with `go tool templ`, so its version is pinned and checked by `go.sum`; its runtime package is linked into the binary. That makes `github.com/a-h/templ` a production dependency, added to the table of `AGENTS.md` by #19 with this reason: typed templates that escape every value.
- **Tailwind is a standalone binary**, downloaded by the generation script and checked against the SHA-256 of `docs/web.md`. No Node.js.
- **HTMX is one file**, the unchanged upstream `dist/htmx.min.js`, committed with its SHA-256, which a test checks.
- **Fonts.** Onest for the interface and Spline Sans Mono for code, both under the SIL Open Font License, embedded as WOFF2 latin and latin-ext subsets with their licenses. The review first chose the system font stack; the owner changed it on 2026-11-12 with the design of the interface (#19), whose warm, readable look the system stacks do not give everywhere.
- **Generated files are committed.** `scripts/generate-web.sh` regenerates the templ output and the CSS; a regular `go build` needs neither tool. `make verify` fails when they are stale, without the network: the templ output is regenerated with `go tool templ` into a temporary directory and compared, and the CSS carries the SHA-256 of its inputs (the templates, `styles.css` and the Tailwind version), which a check recomputes.
- **Assets** are embedded with `go:embed` and served under `/assets/` with a content hash in their name and `Cache-Control: public, max-age=31536000, immutable`.
- **Licenses.** The license of every copied or embedded third-party file is kept next to it, in `internal/adapter/driving/web/licenses/`, and listed in `docs/web.md` (epic 1).

### Web shell (#19)

The owner approved a design of the interface on 2026-11-12: a top bar with the workspace, the navigation and the account, a repository bar with its tabs, and content in cards on a warm ground; warm neutrals and a cobalt accent, radii of 4 to 8 px, statuses as filled discs with a white glyph and a word. #19 builds its base; [docs/web.md](../web.md) lists the files.

Rules made explicit while implementing it (#19):

- **One package of templates.** Every template, components, layout, states and pages, is in `internal/adapter/driving/web/ui`, so one directory is generated and checked; `web` stays the HTTP layer that renders them.
- **Six components, then the domain.** #19 adapts button, badge, input with its field, table, dialog and dropdown menu, and adds the icons, the toast, the layout and the empty and error states. The domain components of the design (queue row, citation, decision bar, position, run steps and console) come with the epics that show them, so no package holds a component nothing uses.
- **Native elements first.** The dialog is a `<dialog>` opened by invoker commands, the dropdown menu a popover: both work without script and keep the focus behavior of the platform; `app.js` only fills in for browsers without invoker commands, places menus and moves focus with the arrow keys.
- **The checks are tests.** The freshness of the templ output, the hash of the stylesheet inputs, the HTMX checksum and the refusal of `templ.Raw` are tests of `ui`, so `go test ./...` and `make verify` run them without a new target. The templ check generates into a temporary copy and compares every file, so a missing or left-over generated file fails too.
- **Theme.** `theme.js`, the only script in the head without `defer`, sets `data-theme` from the person's choice or the system before the page paints; the toggle shows only once it has run.
- **First page.** `/` lists the repositories, empty until epic 5 activates one; any other path of the application answers a not-found page. Pages set `Cache-Control: no-store` now; #20 adds the other headers.

### Sessions and protections (#20)

Rules made explicit while implementing them (#20):

- **The core holds the rules.** `internal/core/session` draws the identifier and the CSRF token from a reader it is given, hashes the identifier, computes the ends of a session, when to touch it and the `Max-Age` of its cookie, and compares tokens in constant time. It reads no clock and no cookie. Renewal keeps the creation time. An identifier that is not 32 bytes in canonical unpadded base64url is malformed and never reaches the store as a key.
- **The port.** `session.Store` has `Create`, `Get`, `Touch`, `Replace` (delete the old row and insert the new one in one transaction: a renewal never leaves both or neither), `Delete` (a revocation succeeds whether the row exists or not) and `DeleteExpired`. Expiry is the core's rule: `Get` returns an expired session, and the middleware deletes it. `sessiontest.Suite` holds the contract; `sessiontest.Memory` and the SQLite adapter pass it.
- **SQLite.** `0002_sessions.sql` adds the `sessions` table, `STRICT`, a 64-character hash as key, and indexes on both times for the purge. The store is `(*sqlite.Store).Sessions()`, a type of its own on the same pools, since both ports name their operations alike.
- **Purge.** `raun server` deletes expired sessions after migrating, and stops if it cannot, then every hour; a failure of the hourly purge is logged and retried at the next tick.
- **The chain.** Security headers wrap every response, health endpoints and refusals included; then, for the application only, the Host check, the body limit, `http.CrossOriginProtection`, the session lookup and the CSRF check. A request without a session that changes state is refused with `403` before routing, so another method on a page answers `403`, not `405`: no route is revealed without a session.
- **The token.** It comes from the `X-CSRF-Token` header, else from exactly one `csrf` field of a URL-encoded body, never from the query; a body over the limit answers `413`. The check reads a URL-encoded body to find the field, so a handler reads its form from `r.PostForm`, never from `r.Body`; a multipart body is not parsed, and such a request sends the header. `ui.Page` carries it, the layout puts it in `hx-headers` on `<body>`, and `ui.CSRFField` puts it in a form. A page without a session sets no cookie and carries no token.
- **For later epics.** `requireSession` answers `401` without a session; `startSession`, `renewSession` and `endSession` create, renew and revoke the session of a request and set or clear its cookie. A route that must take a request without a session, such as the first request of a login, is listed in the bypass patterns of the server, which skip the cross-origin check, the session requirement and the CSRF check, never the Host check. One mux decides for all three, and a path that is not clean matches no bypass.

### Logging

`log/slog`, one record per line on standard error, JSON by default and text with `RAUN_LOG_FORMAT=text` for development. Standard output stays for the output of commands.

- **Request identifier.** 16 random bytes in hexadecimal per request, put in the context, every record of the request and the `X-Request-Id` response header. An incoming `X-Request-Id` is ignored, as every header from a client.
- **Access log fields.** `time`, `level`, `msg`, `request_id`, `method`, `route` (the matched `ServeMux` pattern, never the raw path or query, which may hold search text), `status`, `bytes`, `duration`, `remote` (the TCP peer).
- **Never logged.** Cookies, the `Authorization` header, session identifiers and their hashes, CSRF tokens, any secret value (in a configuration dump too), forge tokens and keys, request and response bodies, repository content. A secret is held in a type whose `String`, `GoString` and `LogValue` give `[redacted]`, so a log call cannot print it by mistake.

### Administrator

There are no passwords and no local accounts (#16): the administrators are forge accounts, listed in the configuration.

- **`RAUN_ADMINS`**, a comma-separated list of forge identities, `<forge>:<stable identifier>` such as `github:583231` (epic 4), empty by default. A listed account is an administrator; the role is evaluated at each login and each request, never stored, so removing an account from the list removes the role at its next request. An empty list is valid: the instance runs, and nobody reaches the administration screens.
- **No bootstrap.** There is no first-administrator step, token or first-login rule, so there is no second bootstrap to refuse and no default credential. The administrator is whoever the person who deploys the instance names.
- **What the role grants.** Configuring the instance and, later, registering runners. Nothing on repository data: viewing and deciding come from the forge (T10).
- **The form of an entry**, decided by the epic 4 review: the forge and the stable identifier it gives the person, never a login, which can be renamed and then taken by someone else ([record](04-forge-port-and-identity.md#accounts-and-login)).

### Instance settings

The instance is configured by its `RAUN_*` variables. Epic 3 needs no stored setting, and none is added before a feature needs one: a table of settings without a setting would be an empty package.

When a stored setting appears, the rule is: a value that an administrator changes while the instance runs, and that must not need a restart, is stored, with its author and time; everything else, and every secret given by the person who deploys, is configuration. The Instance settings screen arrives with accounts (epic 4): first a read-only view of the effective configuration, secrets redacted.

### Secrets at rest

Epic 3 stores no secret: sessions and CSRF tokens are random values, the first stored hashed. The forge credentials of the instance (a GitHub App private key, its webhook secret) are configuration, given by file with the `_FILE` form of epic 1; epic 4 names their variables. The first secrets the server stores are the tokens it obtains from a forge at runtime (epic 4). They follow this scheme, which epic 4 implements with them:

- **Cipher.** AES-256-GCM from the standard library, a random 96-bit nonce per encryption, and as associated data the table, column and key of the row, so a ciphertext copied to another row does not decrypt.
- **Envelope.** `v1`, the identifier of the key, the nonce and the ciphertext, in one stored value. The `v1` is the version of the envelope, recorded with the other formats.
- **Key.** 32 random bytes, base64-encoded, from `RAUN_SECRET_KEY` or `RAUN_SECRET_KEY_FILE`; its identifier is the first 8 bytes of its SHA-256. It is never stored in the database.
- **Rotation.** The administrator puts the new key in `RAUN_SECRET_KEY` and the old one in `RAUN_SECRET_KEY_PREVIOUS`. At start, the server re-encrypts every secret under the new key, in one transaction; the previous key can then be removed.
- **Failure.** A stored secret whose key identifier matches neither key stops the server at startup, naming the variable. A secret that fails to decrypt is an error of the operation that reads it, never a value.
- **Never rendered.** A stored secret is written, replaced or deleted; no screen or API returns it (#21).

### Server (#18)

Rules made explicit while implementing the server (#18):

- **Where the configuration lives.** `cmd/raun` reads it, as this page says: `config.go` holds the catalogue, one read-only table of names, defaults and rules, so adding a variable is one row. A shared package waits for a second consumer. `loadConfig` takes the environment as `os.Environ` gives it, so tests never touch the process environment; the first value of a duplicated variable wins, as for `os.Getenv`.
- **Every error at once.** All the errors of a configuration are reported together, sorted, each starting with its variable. The value of an unknown variable is never shown, since it may be a misspelled secret. `RAUN_DATABASE_PATH` is checked by the rule of the SQLite adapter, `sqlite.CheckPath`, so the two never disagree.
- **Secrets wait for the first one.** No variable of epic 3 is secret, so the `_FILE` form and the secret type come with the first secret variable (epic 4). Until then a row of the catalogue marks a variable secret, and the startup log shows `[redacted]` for it whether set or not; a test proves it with a secret row of its own. #25 brought both with the variables of the GitHub App. The TLS files are paths, not secrets, and are logged.
- **Health.** Only `GET` and `HEAD` reach `/healthz` and `/readyz`; any other method goes to the application. `/readyz` asks the database at each probe through `(*sqlite.Store).CheckReady`, which reads the schema version on the read pool and never migrates, so a schema migrated by another binary after start is seen. A probe gets 2 seconds, since the route is reachable without the Host check, and the version of the binary is computed once, when the store opens.
- **Logs.** The `route` of the access log is the pattern the server matched, empty when none did. The host of a `421` is logged bounded to 256 bytes, since it comes from the client. A panic logs its type and the stack; its value only when it is a runtime error, whose text the program wrote, since any other value could hold request data. A panic after the response has started aborts the connection, so a client never takes a truncated body for a complete one. `net/http`'s own messages go through the same logger, at `warn`.
- **Host.** The Host check drops one final dot of a DNS name, so `raun.example.` is `raun.example`; an IP address is compared as it is.
- **Exit codes.** `raun server` takes no argument (exit 2). A failure before the listener opens, an error of the listener, or in-flight requests dropped at the deadline exit 1; a clean stop exits 0.

## Deferred

| Topic | Where |
| --- | --- |
| Accounts, login, the account of a session, logout | Done by epic 4 (#26) |
| The form of an entry of `RAUN_ADMINS` | Decided by the epic 4 review: `github:<stable identifier>` ([record](04-forge-port-and-identity.md#accounts-and-login)) |
| The `RAUN_ADMINS` check and the Instance settings screen | Done by epic 4 (#21, [docs/administration.md](../administration.md)) |
| The encryption of stored secrets | Done by epic 4 (#26, with the first stored tokens) |
| The first stored instance setting | The epic whose feature needs one |
| The `_FILE` form of a variable and the secret type of the configuration | Done by epic 4 (#25), with the first secret variables, those of the GitHub App ([record](04-forge-port-and-identity.md#github-adapter-25)) |
| The bypass and authentication of webhooks | Epic 5 review (#30) |
| The bypass and authentication of the runner API | Epic 6 |
| How an upgrade is run, and the retention of logs | Epic 13 |

## Sub-issues

| Issue | Change |
| --- | --- |
| #18 | The variables, startup order, `RAUN_DATABASE_MIGRATE` instead of a command or flag, limits, shutdown and `SIGHUP`, logs and their forbidden fields, `/healthz` and `/readyz`, the Host check and TLS. |
| #19 | `docs/web.md` pins, copied components, templ as a tool and a dependency, the stale check without network, content-hashed assets, licenses, embedded fonts, the `templ.Raw` check. |
| #20 | `internal/core/session`, the `0002_sessions.sql` migration, lifetimes, renewal, revocation, cookie attributes, `http.CrossOriginProtection`, the token field and header, the security headers. |
| #21 | Moved to epic 4 and rescoped: the `RAUN_ADMINS` check once accounts exist, the read-only Instance settings screen. No bootstrap. The encryption of stored secrets moved to #26, which stores the first ones (epic 4 review). |
| #16 | Scope and exit criteria without the administrator and the instance settings. |

## Exit criteria

- [x] This page records the choices.
- [x] `raun server` starts on an empty database, applies migrations and serves a page built with the web shell (#18, #19): `TestServerServes` in `cmd/raun/server_test.go`.
- [x] Tests prove that a cross-site state-changing request is refused (#20): `TestCrossOriginRequestsAreRefused` and `TestCSRFToken` in `internal/adapter/driving/web/protect_test.go`.
- [x] `make verify` passes, including the check that generated files are up to date (#19: `TestGeneratedTemplatesAreCurrent` and `TestStylesheetIsCurrent` in `internal/adapter/driving/web/ui`).
