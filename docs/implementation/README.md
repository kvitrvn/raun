# Implementation records

Raun is built epic by epic. Each epic starts with a review issue, labelled `decision`, that settles its open topics before any code is written. The review records its choices here, in one page per epic: `NN-<slug>.md`.

Until a review is closed, the tasks of its epic are provisional, and the orientations written in the epic issue are not decisions.

## Rules

- A page states choices that were made. A topic still open stays in its review issue, or appears under "Deferred" with the epic that owns it.
- A choice that changes later is changed on its page, in the same pull request as the code, and the page says why.
- A dependency added by an epic is justified on its page.
- The page is written in English, like the rest of the repository.

## Template

```markdown
# Epic N: <title>

Epic: #N. Review: #M. Decided by <who> on <date>.

## Scope

What the epic covers, in a few lines, and the sources consulted.

## Choices

### <Topic>

The choice, why it was made, and what it implies for later epics.

## Deferred

| Topic | Where |
| --- | --- |

## Exit criteria

- [ ] Each exit criterion of the epic, checked against a file, a command or a CI run.
```

## Epics

| # | Epic | Milestone | Record |
| --- | --- | --- | --- |
| 1 | Project foundation (#1) | v0.1 Foundation | [01-project-foundation.md](01-project-foundation.md) |
| 2 | Knowledge core (#8) | v0.1 Foundation | [02-knowledge-core.md](02-knowledge-core.md) |
| 3 | Server foundation (#16) | v0.2 Server and forge | [03-server-foundation.md](03-server-foundation.md) |
| 4 | Forge port and identity (#22) | v0.2 Server and forge | |
| 5 | Repositories and events (#29) | v0.2 Server and forge | |
| 6 | Jobs and runner (#35) | v0.3 Deterministic checks | |
| 7 | Evidence checks on push and pull request (#43) | v0.3 Deterministic checks | |
| 8 | Configuration and agent contract (#50) | v0.4 LLM analysis | |
| 9 | Sandboxed agent execution (#57) | v0.4 LLM analysis | |
| 10 | Analysis pipeline (#65) | v0.4 LLM analysis | |
| 11 | Knowledge review (#73) | v0.5 Human review | |
| 12 | Gitea and Forgejo (#80) | v1.0 Self-hosting ready | |
| 13 | Operations and release (#86) | v1.0 Self-hosting ready | |
| 14 | PostgreSQL backend (#93) | post-v1 | |
| 15 | GitLab (#98) | post-v1 | |
| 16 | Incremental analysis (#102) | post-v1 | |
