# Epic 1: project foundation

Epic: #1. Review: #2. Decided by the owner on 2026-11-11.

## Scope

Set up the repository so that every later epic starts from a base that builds, is verified the same way locally and in CI, and has its product rules written down before any feature code. No product feature is in scope.

Functional specification consulted: `kvitrvn/raun-old` at `330e634` (2026-10-29), mainly `AGENTS.md`, `docs/upgrade.md`, `docs/format.md` and `docs/agents.md`.

## Choices

### Product shape

Raun is a self-hosted server with separate runners, in the spirit of Drone CI. One instance serves one organization and is configured by an administrator. The data model stays ready for several tenants, but there is no public sign-up and no billing.

- The server owns the database, the web interface, forge integration and the job queue. It never holds LLM credentials.
- A runner pulls jobs from the server, checks a commit out and runs the deterministic checks and the agents. It may run on the user's own infrastructure, with the user's own LLM credentials.
- The forge is the only source of identity and permissions.

See [the architecture](../architecture.md) and [the threat model](../threat-model.md).

### Local mode

Dropped. There is no file-based `.raun/` workspace and no local `init`, `run` or `serve` command.

- The server database is the source of truth. YAML and Markdown are exports (epic 11).
- A knowledge base written by raun-old under `.raun/knowledge/` can be imported (#15).

Why: one product with one storage model. Keeping the file backend would double every storage change and every test of the knowledge core for a mode the hosted product does not need.

### Code base

Full rewrite. raun-old is the functional specification, not a code base to port: its behavior, formats and invariants are carried over, its code is not. A package that ends up close to its raun-old counterpart is written again and tested here.

Conclave (`Bornholm/conclave`) is a source of ideas only. It is not a dependency: everything in it is internal and it has no stable API.

### Binary and command line

One `raun` binary with the subcommands `server`, `runner`, `version` and `help`. The command line is parsed with the standard library (`flag` per subcommand once options exist); no command-line framework.

Exit codes:

| Case | Code |
| --- | --- |
| Success, `help`, `version` | 0 |
| Failure, including a subcommand that is not implemented yet | 1 |
| Usage error: no command, unknown command | 2 |

`main` only calls `run(args, stdout, stderr) int`, which tests drive directly.

`raun version` prints the module version stamped by the Go toolchain: a tag, or a pseudo-version made from the Git revision. A build without one prints `dev`, followed by the revision when the toolchain recorded it. Release versioning is settled in epic 13.

### Package layout

```
cmd/raun/                  wiring: configuration, adapters, subcommands
internal/core/<context>/   domain logic, one directory per bounded context
internal/adapter/driven/   implementations of the ports: database, forge, sandbox, Git
internal/adapter/driving/  entry points: HTTP server, webhooks, runner API
```

- A core context, its tests included, imports the standard library and its own context only.
- A port is an interface declared in the core context that consumes it, never next to its adapter and never in a shared `ports` package.
- Adapters import the core; the core never imports an adapter.
- `cmd/raun` is the only place where adapters are built and handed to the core.

The rule is enforced by `make architecture` (`scripts/check-architecture.sh`), part of `make verify`. It passes trivially until epic 2 adds the first core context.

Packages are created by the epic that first needs them. This epic creates none besides `cmd/raun`: an empty package would fix a shape before the knowledge model of epic 2 exists. #3 is adjusted accordingly.

### Dependency policy

Standard library first. A dependency is added only with a stated reason, recorded in the implementation record of the epic that adds it, and listed in `AGENTS.md`.

- Production dependencies (linked into the binary): none in this epic.
- Tool dependencies (declared with `tool` in `go.mod`, never linked into the binary): `honnef.co/go/tools/cmd/staticcheck` v0.8.2 and `golang.org/x/vuln/cmd/govulncheck` v1.9.0. Reason: pinned linters that a fresh clone runs with `go tool`, without installing anything.

Dependabot proposes weekly updates of Go modules and GitHub Actions.

### License

MIT, `Copyright (c) 2026 Benjamin Gaudé`, as for the owner's other projects. The license of each embedded third-party asset is kept next to it once there is one (epic 3).

### Verification

`make verify` is the single gate, run before a change is declared done and run by CI unchanged:

| Target | Check |
| --- | --- |
| `fmt-check` | `gofmt` reports no file; nothing is rewritten |
| `tidy-check` | `go mod tidy` would change nothing |
| `vet` | `go vet ./...` |
| `lint` | `staticcheck ./...` |
| `architecture` | dependency direction of the core |
| `test` | `go test ./...`, without network or LLM |
| `build` | `CGO_ENABLED=0` build of `bin/raun` |

Outside `verify`:

- `make vuln` runs `govulncheck`. It needs the network for the vulnerability database, so CI runs it as a warning that never blocks a pull request.
- `make commits` checks commit messages (see below). CI runs it on pull requests.

Go version: 1.27.3, from `go.mod`. CI reads it with `go-version-file`. GitHub Actions are pinned by commit SHA, with the version in a comment.

### Conventions

- Commits follow Conventional Commits: `type(scope)?: subject`, lowercase type, subject starting lowercase, no trailing period, header of 100 characters at most.
- Commits, pull requests, issues and comments carry no attribution trailer or tool mention (`Co-authored-by`, "Generated with", and the like). `make commits` rejects them.
- Work happens on a branch named `<type>/<short-description>`, such as `feat/epic-1-project-foundation`, and reaches `main` through a pull request that links the issues it closes.
- The owner merges.
- Everything is written in English: code, comments, documentation, CLI messages, issues, pull requests and commits.

### Server configuration

`raun server` and `raun runner` read their configuration from `RAUN_*` environment variables only, as Drone does.

- Every setting has a default and an explicit validation rule; an invalid value stops the process at startup with the name of the variable.
- A secret can also be read from a file named by the same variable with a `_FILE` suffix (`RAUN_GITHUB_PRIVATE_KEY_FILE`), for container secrets. Setting both is an error.
- The variables themselves are defined by the epics that need them, starting with epic 3.

## Deferred

| Topic | Where |
| --- | --- |
| A check that no LLM provider, model or vendor API is named in code | Epic 8, once agent code exists |
| Link checking of the documentation | When the documentation grows past the skeleton |
| Secret scanning in CI | Epic 13 |
| Release versioning, packaging and signing | Epic 13 |
| Storage engine (SQLite first, PostgreSQL later is the orientation) | Epic 2 review (#9) |
| Tenancy beyond one organization per instance | Not planned; the data model stays ready for it |

## Exit criteria

- [x] This page records the choices.
- [x] `make verify` passes locally and in CI on a buildable binary whose `server` and `runner` subcommands are stubs.
- [x] `AGENTS.md`, `README.md` and `docs/` describe the product, the invariants and the conventions.
- [x] The threat model is written and referenced from `AGENTS.md`.
