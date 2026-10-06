# AGENTS.md

Raun is a Go CLI that builds an evidence-backed knowledge base of a software project (personas, requirements, …) from independent LLM agent analyses, a lead consolidation, and human validation.
The design reference is `docs/design.md`. Read it before changing behavior; update it when a decision changes.

## Commands

```sh
go build ./...                   # build
go build -o bin/raun ./cmd/raun  # binary
go test ./...                    # all tests (no network, no LLM)
go vet ./...
golangci-lint run ./...          # config: .golangci.yml
gofmt -l .                       # must print nothing
```

Run all of them before declaring a task done.

## Layout

```
cmd/raun/            CLI entry point; run() is testable (args, stdout, stderr) -> exit code
internal/config      .raun/config.yaml: strict decoding, defaults, validation with field paths
internal/workspace   .raun/ layout; `raun init` templates embedded from templates/
internal/knowledge   knowledge model, validation, status transitions, canonical YAML store
internal/gitx        git through the `git` binary: resolve commits, read files at a commit
internal/evidence    deterministic evidence verification (integrity, freshness, relocation)
internal/gittest     fixture repositories for tests (ignores user Git config)
docs/design.md       concepts, lifecycle, architecture, step-by-step roadmap
docs/format.md       knowledge file format reference
```

Planned packages (see `docs/design.md` → Architecture): `agent`, `prompt`, `run`, `consolidate`, `reconcile`. Don't create them before their roadmap step.

## Domain invariants

These invariants are the core of the product. Never weaken them:

- No claim without evidence. Evidence = path + line range + excerpt at a commit, verified deterministically by Raun (never by an LLM).
- An LLM proposes; it never establishes. Only a human command moves knowledge to `validated` or `rejected`. Human-validated content is never rewritten automatically.
- Disagreements between agents are stored as-is, never averaged or dropped.
- Agents analyze independently: they never see the knowledge base or each other's output.
- Provider neutrality: no LLM provider, model or vendor API is named or imported in code. Agents are reached through the agent contract (`runner.kind`). Concrete commands belong in docs and examples only.
- On-disk formats (config, knowledge files, agent contract) carry a `version` field. Changing one is a design decision: update `docs/design.md`.

## Go conventions

- Standard library first. A new dependency needs a stated reason. Current non-stdlib dependency: `go.yaml.in/yaml/v3`.
- Wrap errors with context: `fmt.Errorf("read config: %w", err)`. User-facing validation errors name the field path (`analysis.agents[1].runner.argv: ...`).
- Decode external input strictly (unknown fields rejected).
- Deterministic output: stable ordering, stable serialization (knowledge files are reviewed in Git diffs).
- No package-level mutable state. Read-only tables, compiled regexps and embedded files are fine.

## Testing

- Table-driven tests; `t.TempDir()` for filesystem work.
- Golden files live in `testdata/`; regenerate with `go test ./internal/knowledge -update` and review the diff.
- Never call a real LLM or the network in tests. Use fake agents, i.e. test helper processes that emit fixture JSON.
- Git-dependent tests build their own fixture repository with `internal/gittest`.
- CLI behavior is tested through `run()` in `cmd/raun`.

## Language

- All files (code, comments, docs, CLI messages, prompts) are written in English.
- Talk to the project owner in French.

## Workflow and boundaries

- Work one roadmap step at a time (`docs/design.md` → Implementation plan). Stop at the end of each step, summarize, and wait for the owner's review.
- Never commit, push, tag or rewrite Git history. The owner does it.
- At the end of each step, propose a commit title following Conventional Commits as enforced by commitlint (`@commitlint/config-conventional`): `type(scope)?: subject`, lowercase type, lowercase subject start, no trailing period, header ≤ 100 chars.
- Keep `README.md`, `AGENTS.md`, `CLAUDE.md` and `docs/design.md` in sync with what is actually implemented.
- Don't implement features outside the current step, and don't settle open decisions listed in `docs/design.md` without asking.
