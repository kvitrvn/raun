# Raun

*Raun* is Old Norse for "trial, test, experience". *Í raun* means "in reality".

Raun builds an evidence-backed knowledge base of a software project, such as its personas and business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

> **Status:** early development. `init`, `check`, `list`, `show` and `version` work today; nothing produces knowledge yet. Other commands are planned (see [docs/design.md](docs/design.md)).

## Install

Requires Go 1.27+ and `git`.

```sh
go install github.com/kvitrvn/raun/cmd/raun@latest
```

From source:

```sh
go build -o bin/raun ./cmd/raun
```

## Get started

In the repository you want to analyze:

```sh
raun init     # creates .raun/config.yaml, .raun/context.md, .raun/.gitignore
```

1. Edit `.raun/config.yaml`: set each agent's `runner.argv` to a command that reads a prompt on stdin and prints one JSON object on stdout. Use different models for different agents.
2. Write what the team knows in `.raun/context.md`. Agents can cite it, but it stays tagged as human input, separate from facts found in code.
3. Validate the configuration:

```sh
raun check
```

Browse the knowledge base:

```sh
raun list -status proposed   # filters: -type, -status
raun show <id>               # support, evidence, open points, history
```

Then (planned):

```sh
raun run                  # independent analyses, consolidation, evidence check
raun review               # items waiting for a human decision
raun accept <id> --reason "..."
raun reject <id> --reason "..."
```

The knowledge base is made of plain YAML files under `.raun/`. You review and version them with Git, like code.

## Docs

- [docs/design.md](docs/design.md): concepts, analysis lifecycle, architecture, roadmap.
- [docs/format.md](docs/format.md): knowledge file format and status rules.
- [AGENTS.md](AGENTS.md): conventions for contributors and coding agents.
