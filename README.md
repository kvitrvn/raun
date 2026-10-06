# Raun

*Raun* is Old Norse for "trial, test, experience". *Í raun* means "in reality".

Raun builds an evidence-backed knowledge base of a software project, such as its personas and business requirements. Several LLM agents analyze the repository independently. A lead agent consolidates their findings and keeps disagreements visible. Every claim points to exact lines at a given commit, and Raun checks each citation itself. Agents only propose knowledge; a human validates it.

> **Status:** early development. Everything below works today, except reruns on an existing knowledge base (planned).

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

1. Edit `.raun/config.yaml`: set each agent's `runner.argv`, and the lead's, to a command that reads a prompt on stdin and prints one JSON object on stdout (see [docs/agents.md](docs/agents.md)). Use different models for different agents. Set `language` for the knowledge you want to read.
2. Write what the team knows in `.raun/context.md` and commit it. Agents can cite it, but it stays tagged as human input, separate from facts found in code.
3. Validate the configuration, commit, and run an analysis:

```sh
raun check
git add .raun && git commit -m "chore: configure raun"
raun run                     # HEAD must be committed (or use -commit); -agent runs a single agent
```

Each analyst works on its own disposable copy of the commit, without Git history or the knowledge base. Raun verifies every citation. The lead then merges the analyses, seeing them anonymized, and keeps disagreements visible. Items are written as `proposed` (or `contested`) under `.raun/knowledge/`, with a manifest under `.raun/runs/`.

Browse the knowledge base:

```sh
raun list -status proposed   # filters: -type, -status
raun show <id>               # support, evidence, open points, history
raun verify                  # re-check every citation; exits 1 if some no longer hold
```

Decide. Only humans validate, and every decision is signed (`git config user.name`, or `-author`) and justified:

```sh
raun review                                         # what awaits a decision, with open points
raun resolve <id> p1 -position 1 -note "..."        # settle a disagreement (or any open point)
raun accept <id> -reason "..."                      # validate
raun reject <id> -reason "..."                      # reject; raun reopen <id> -reason "..." undoes it
raun report -o KNOWLEDGE.md                         # Markdown view for the team
```

A disagreement blocks validation until it is resolved. Resolving records your choice but does not rewrite the item. If its content must change, edit the YAML file and run `raun check`.

The knowledge base is made of plain YAML files under `.raun/`. You review and version them with Git, like code.

## Docs

- [docs/agents.md](docs/agents.md): agent command protocol and report contract.
- [docs/format.md](docs/format.md): knowledge files, status rules, run manifests.
- [AGENTS.md](AGENTS.md): conventions for contributors and coding agents.
