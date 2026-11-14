# Raun

Raun is Old Norse for "trial, test, experience". Í raun means "in reality".

Raun builds an evidence-backed knowledge base of a software project, such as its personas and business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

Raun is self-hosted and connected to your forge, in the spirit of Drone CI: a server receives the forge events and hosts the review interface, and separate runners check the code and run the agents with your own LLM credentials.

## Status

Early development: `raun server` starts, opens its database, serves health endpoints and the web interface; `raun runner` is a stub. The knowledge model (`internal/core/knowledge`) exists with source classification, a reader of a commit on the `git` binary, deterministic evidence verification and its storage on SQLite. People sign up and log in with their GitHub account through a GitHub App (`internal/core/identity`, `internal/adapter/driven/github`), and get a personal workspace; they create shared workspaces and add members. What a person may view or decide on a repository comes from the forge, through one permission service with a 5-minute cache. The Repositories screen lists the repositories a person reaches on the forge; activating one in a workspace needs the forge to offer files, webhooks and events, which arrive with the rest of epic 5, so no repository can be activated yet. The plan is in the [issues](https://github.com/kvitrvn/raun/issues) and the [implementation records](docs/implementation/README.md).

## Usage

```sh
raun server    # run the server, configured by the RAUN_* variables below
raun runner    # run a runner (not implemented yet)
raun version   # print the version
raun help      # print the help
```

## Server

`raun server` reads its configuration from `RAUN_*` environment variables only. An invalid value, or a `RAUN_*` variable the server does not know, stops it at startup with a message naming the variable; the variables of the runner, `RAUN_RUNNER_*`, are ignored.

| Variable | Default | Rule |
| --- | --- | --- |
| `RAUN_HTTP_ADDR` | `:8080` | Listen address, `host:port`, port 1 to 65535 |
| `RAUN_PUBLIC_URL` | `http://localhost:8080` | The address people and forges use: `https`, or `http` on a loopback address only; no path, query, fragment or user |
| `RAUN_TLS_CERT_FILE` | empty | PEM certificate; with `RAUN_TLS_KEY_FILE`, the server serves TLS 1.2 or later itself |
| `RAUN_TLS_KEY_FILE` | empty | PEM key; both TLS files are set, or neither |
| `RAUN_DATABASE_PATH` | `raun.db` | SQLite file; a plain path, not a URI or `:memory:` |
| `RAUN_DATABASE_MIGRATE` | `false` | `true` applies pending migrations at start; without it, an older schema stops the server |
| `RAUN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RAUN_LOG_FORMAT` | `json` | `json` or `text` |
| `RAUN_SHUTDOWN_TIMEOUT` | `10s` | How long a stop waits for in-flight requests, from `1s` to `5m` |
| `RAUN_GITHUB_URL` | `https://github.com` | The address of GitHub or of a GitHub Enterprise Server: `https`, a host, no path |
| `RAUN_GITHUB_APP_ID` | empty | The ID of the GitHub App; empty means no forge |
| `RAUN_GITHUB_CLIENT_ID` | empty | The client ID of the App |
| `RAUN_GITHUB_CLIENT_SECRET` | empty | Secret: the client secret of the App |
| `RAUN_GITHUB_PRIVATE_KEY` | empty | Secret: the PEM private key of the App, RSA |
| `RAUN_SECRET_KEY` | empty | Secret: 32 bytes in base64 (`openssl rand -base64 32`) that encrypt the tokens the server stores; required with a forge |
| `RAUN_SECRET_KEY_PREVIOUS` | empty | Secret: the key before the last rotation; the start encrypts every stored secret again under `RAUN_SECRET_KEY`, after which it can go |
| `RAUN_SIGNUP_USERS` | empty | Forge identities that may sign up and log in, comma-separated: `github:583231` |
| `RAUN_SIGNUP_ORGS` | empty | Organizations whose active members may sign up and log in: `github:9919` |
| `RAUN_ADMINS` | empty | Administrators of the instance, who may sign up and log in too: `github:583231`; see [docs/administration.md](docs/administration.md) |

The first start on an empty database needs `RAUN_DATABASE_MIGRATE=true`. A schema newer than the binary always stops it.

A secret can be given as a file instead, by the variable with `_FILE` added (`RAUN_GITHUB_PRIVATE_KEY_FILE`); setting both forms is an error. The GitHub App is configured whole or not at all: with `RAUN_GITHUB_APP_ID`, its client ID, client secret and private key are required, and without it none of its variables may be set. [docs/forge.md](docs/forge.md#github) explains how to create and configure the App.

- **Public address.** Every request except the health endpoints must carry the host of `RAUN_PUBLIC_URL` in its `Host` header, or it is refused with `421`. No header from a proxy (`X-Forwarded-*`, `Forwarded`) is trusted. `RAUN_PUBLIC_URL` does not follow `RAUN_HTTP_ADDR`: change both together.
- **Health.** `GET /healthz` answers `200 ok` while the process serves; `GET /readyz` answers `200 ok` when the database answers at the schema of the binary, `503 unavailable` otherwise and while stopping. Neither needs the public host.
- **Signals.** `SIGINT` and `SIGTERM` stop the server: it stops accepting connections and waits for in-flight requests up to `RAUN_SHUTDOWN_TIMEOUT`, then exits with 1 if it had to drop some. `SIGHUP` reloads the TLS pair; a pair that fails to load is logged and the previous one is kept.
- **Limits.** Headers 5 s to arrive and 32 KiB at most (`431`), a request 30 s to be read, a response 60 s to be written, an idle connection 120 s, a request body 64 KiB by default (`413`). They are constants.
- **Login.** `GET /login` sends a person to the forge; at their first login they choose the name of their Raun account, which cannot change. Only the identities of `RAUN_SIGNUP_USERS` and `RAUN_ADMINS`, and the members of `RAUN_SIGNUP_ORGS`, get in, checked at every login: the lists are empty by default, so a fresh instance lets nobody in until they are set. [docs/forge.md](docs/forge.md#identities-in-the-configuration) shows how to find the identifiers. Their tokens are stored encrypted under `RAUN_SECRET_KEY`; a stored secret under a key that is neither `RAUN_SECRET_KEY` nor `RAUN_SECRET_KEY_PREVIOUS` stops the server at start.
- **Administration.** The accounts named in `RAUN_ADMINS` see the effective configuration, secrets redacted, on `/instance`; the role is checked at each request and grants nothing on repositories ([docs/administration.md](docs/administration.md)).
- **Workspaces.** Every account has a personal workspace named after it, at `/<name>`; `/workspaces` lists the workspaces of the account and creates shared ones, whose owners add members. Accounts and workspaces share one namespace of permanent names. A workspace never grants access to a repository: the forge does.
- **Repositories.** `/` lists the repositories a person reaches on the forge through the App. A member of a workspace with write access on the forge activates one there, and it belongs to that workspace for good, active or deactivated; its page is `/<workspace>/<owner>/<name>`, shown to whoever the forge lets read it ([docs/web.md](docs/web.md#repositories)).
- **Protections.** Every response carries a strict Content Security Policy and the other security headers. A state-changing request needs a session and its CSRF token (header `X-CSRF-Token` or form field `csrf`), and a browser request from another site is refused. Sessions live 7 days without use and 30 days at most; expired ones are deleted at start and every hour.
- **Logs.** One record per line on standard error. Each request gets a random identifier, returned in `X-Request-Id`, and one access record: method, route pattern, status, size, duration and peer address. Headers, cookies, queries, bodies and raw paths are never logged. The effective configuration is logged once at startup, every secret as `[redacted]`.

## Development

Requirements: Go 1.27.3 and GNU Make. The linters are pinned in `go.mod` and run with `go tool`; nothing else is installed.

```sh
make build     # bin/raun
make verify    # the gate run before every change and by CI
./scripts/generate-web.sh   # after changing a template or ui/styles.css (docs/web.md)
```

Read [AGENTS.md](AGENTS.md) for the conventions and [docs/](docs/README.md) for the product, the architecture and the threat model.

## License

[MIT](LICENSE)
