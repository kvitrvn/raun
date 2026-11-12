# Raun

Raun is Old Norse for "trial, test, experience". Í raun means "in reality".

Raun builds an evidence-backed knowledge base of a software project, such as its personas and business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

Raun is self-hosted and connected to your forge, in the spirit of Drone CI: a server receives the forge events and hosts the review interface, and separate runners check the code and run the agents with your own LLM credentials.

## Status

Early development: `raun server` starts, opens its database, serves health endpoints and a first page built with the web shell (no repository can be activated yet); `raun runner` is a stub. The knowledge model (`internal/core/knowledge`) exists with source classification, a reader of a commit on the `git` binary, deterministic evidence verification and its storage on SQLite. The plan is in the [issues](https://github.com/kvitrvn/raun/issues) and the [implementation records](docs/implementation/README.md).

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

The first start on an empty database needs `RAUN_DATABASE_MIGRATE=true`. A schema newer than the binary always stops it.

- **Public address.** Every request except the health endpoints must carry the host of `RAUN_PUBLIC_URL` in its `Host` header, or it is refused with `421`. No header from a proxy (`X-Forwarded-*`, `Forwarded`) is trusted. `RAUN_PUBLIC_URL` does not follow `RAUN_HTTP_ADDR`: change both together.
- **Health.** `GET /healthz` answers `200 ok` while the process serves; `GET /readyz` answers `200 ok` when the database answers at the schema of the binary, `503 unavailable` otherwise and while stopping. Neither needs the public host.
- **Signals.** `SIGINT` and `SIGTERM` stop the server: it stops accepting connections and waits for in-flight requests up to `RAUN_SHUTDOWN_TIMEOUT`, then exits with 1 if it had to drop some. `SIGHUP` reloads the TLS pair; a pair that fails to load is logged and the previous one is kept.
- **Limits.** Headers 5 s to arrive and 32 KiB at most (`431`), a request 30 s to be read, a response 60 s to be written, an idle connection 120 s, a request body 64 KiB by default (`413`). They are constants.
- **Logs.** One record per line on standard error. Each request gets a random identifier, returned in `X-Request-Id`, and one access record: method, route pattern, status, size, duration and peer address. Headers, cookies, queries, bodies and raw paths are never logged. The effective configuration is logged once at startup.

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
