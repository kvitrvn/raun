# AGENTS.md

Raun builds an evidence-backed knowledge base of a software project (personas, requirements) from independent LLM agent analyses, a lead consolidation and human validation. It is a self-hosted server with separate runners, connected to forges in the spirit of Drone CI. Read [docs/product-vision.md](docs/product-vision.md) before adding product behavior, and [docs/threat-model.md](docs/threat-model.md) before touching a trust boundary.

## Commands

```sh
make verify       # the gate: format, tidy, vet, staticcheck, architecture, tests, build
make test         # go test ./... (no network, no LLM)
make build        # bin/raun
make fmt          # rewrite Go files with gofmt
make vuln         # govulncheck (needs the network; a warning in CI)
make commits      # commit messages since origin/main
```

Run `make verify` before declaring a task done. CI runs the same target.

## Layout

```
cmd/raun/                  wiring and subcommands; run() is testable (args, stdout, stderr) -> exit code
internal/core/<context>/   domain logic: standard library and its own context only
internal/adapter/driven/   implementations of the ports: database, forge, sandbox, Git
internal/adapter/driving/  entry points: HTTP server, webhooks, runner API
internal/gittest/          test helper outside the core: fixture Git repositories
internal/golden/           test helper outside the core: golden files, rewritten with -update
internal/forgetest/        test helper outside the core: the fake forge and the contract suite of forge adapters
scripts/                   repository checks called by the Makefile, and generate-web.sh (docs/web.md)
docs/                      product, architecture, threat model, implementation records
```

- Declare a port in the core context that consumes it; put its implementation under `internal/adapter`. `make architecture` enforces the dependency direction of the core.
- Build adapters in `cmd/raun` only.
- Create a package in the epic that first needs it; never add an empty one.

## Domain invariants

Never weaken these. A change that needs to is a product decision for the owner.

- No claim without evidence: a path, a line range and an excerpt at a commit, verified deterministically by Raun, never by an LLM.
- An LLM proposes; it never establishes. Only a human decision moves knowledge to `validated` or `rejected`, and human-validated content is never rewritten automatically.
- Disagreements between agents are stored as they are, never averaged or dropped.
- Agents analyze independently: they never see the knowledge base, the configuration or each other's output.
- The lead groups and arbitrates on anonymized reports. It never adds evidence and never discards an interpretation; Raun derives positions, agents and evidence itself.
- The reconciler only matches a run's items to existing items, seeing their content and never agents, runs or statuses. A rerun appends; it never rewrites an item's content or support.
- Provider neutrality: no LLM provider, model or vendor API is named or imported in code. Concrete agent commands belong in documentation and examples.
- Formats (configuration, agent contract, exports, run manifests) carry a `version` field. Changing one is a decision recorded in an implementation record.

## Hosted invariants

- Raun never writes to the analyzed repository; it only publishes statuses and comments.
- The server never holds LLM credentials; they stay on the runners.
- The repository configuration holds no command, image, environment variable or secret.
- Pull requests from forks get the deterministic check only: no secret, no LLM.
- Agents run sandboxed, with an empty environment, declared secrets only and restricted network.
- Permissions come from the forge and are never exceeded: read access means viewing, write access means deciding. The author of a decision is the authenticated user.
- Raw artifacts contain customer code: they stay scoped to their repository and follow a retention policy.

## Go conventions

- Standard library first. A dependency needs a reason, recorded in the implementation record of its epic and listed below.
- Wrap errors with context: `fmt.Errorf("read config: %w", err)`. A validation error names the field or the variable.
- Decode external input strictly: reject unknown fields, bound sizes.
- Propagate `context.Context`; define narrow interfaces at the consumer.
- No package-level mutable state. Constants, read-only tables, compiled regular expressions and embedded files are fine.
- Deterministic output: stable ordering and stable serialization.
- Read configuration only from `RAUN_*` environment variables. Give each one a default and a validation rule; a secret may come from the file named by `RAUN_<NAME>_FILE`.

Dependencies:

| Module | Kind | Reason |
| --- | --- | --- |
| `honnef.co/go/tools/cmd/staticcheck` | tool | Pinned linter, run with `go tool` |
| `golang.org/x/vuln/cmd/govulncheck` | tool | Pinned vulnerability scanner, run with `go tool` |
| `modernc.org/sqlite` | library | Pure-Go SQLite driver of the knowledge store: the binary keeps building with `CGO_ENABLED=0` (epic 2 record) |
| `github.com/a-h/templ` | tool and library | Typed templates that escape every value; the generator runs with `go tool templ`, the runtime package is linked (epic 3 record) |

## Testing

- Table-driven tests; `t.TempDir()` for filesystem work.
- Never call a real LLM, a real forge or the network in tests.
- Build the Git repositories a test needs with `internal/gittest`; core tests build their inputs in memory instead.
- Compare long outputs with golden files under `testdata/` through `internal/golden`. Refresh them one package at a time with `go test ./<pkg> -update`, never `./...`, and review the diff before committing. `.gitattributes` keeps every `testdata` directory byte for byte.
- Test the CLI through `run()` in `cmd/raun`.
- Cover error paths and boundaries, not only the happy path.

## Language

- Everything in the repository and on GitHub is written in English: code, comments, documentation, CLI messages, issues, pull requests, commits.
- Talk to the project owner in French.

## Workflow

- Work one epic at a time. Its review issue (label `decision`) settles open topics first and records them in `docs/implementation/NN-<slug>.md`. Do not settle an open decision without the owner.
- Branch from `main` as `<type>/<short-description>` and open a pull request that links the issues it closes. The owner merges.
- Commit messages follow Conventional Commits: `type(scope)?: subject`, lowercase type, subject starting lowercase, no trailing period, header of 100 characters at most.
- Commits, pull requests, issues and comments carry no attribution trailer or tool mention, such as `Co-authored-by` or "Generated with". `make commits` rejects them.
- Keep `README.md`, `AGENTS.md` and `docs/` in sync with what is implemented, in the same change.
