# Architecture

> Status: the shape below is decided ([epic 1](implementation/01-project-foundation.md)), and the storage engine by [epic 2](implementation/02-knowledge-core.md#sqlite). What exists so far: `cmd/raun`; the knowledge model, the source classification, the commit reader port, evidence verification and the store port in `internal/core/knowledge`, with its contract suite in `knowledgetest`; the store on SQLite in `internal/adapter/driven/sqlite`; the commit reader on the `git` binary in `internal/adapter/driven/git`; the test helpers `internal/gittest` (fixture repositories) and `internal/golden` (golden files); and `raun server` with its configuration, its lifecycle and the HTTP entry point in `internal/adapter/driving/web` (middleware, request protections, sessions, health endpoints, embedded assets and the first page), the session rules and store port in `internal/core/session` with their contract suite in `sessiontest`; the forge port in `internal/core/identity`, with the fake forge and its contract suite in `internal/forgetest` ([epic 4](implementation/04-forge-port-and-identity.md)), with its templates in `internal/adapter/driving/web/ui` ([epic 3](implementation/03-server-foundation.md), [docs/web.md](web.md)). Each epic fills in its part and updates this page.

## Components

```
                 webhooks, OAuth, statuses, comments
   forge  <------------------------------------------------>  raun server
 (GitHub, Gitea,                                              |  database (SQLite)
  Forgejo, GitLab)                                            |  web interface
      ^                                                       |  forge adapters
      |  clone with a short-lived credential                  |  job queue, runner API
      |                                                       |
      +--------------------------  raun runner  <-------------+  jobs pulled over HTTP
                                    |  checkout of one commit
                                    |  deterministic evidence check
                                    |  agents in ephemeral containers
                                    v
                                  LLM endpoints (from the runner only)
```

| Component | Role | Holds |
| --- | --- | --- |
| Forge | Identity, permissions, code, events | The repositories |
| `raun server` | Web interface, accounts, workspaces, repository activation, webhooks, job queue, knowledge store | Forge tokens, the knowledge base, run records |
| `raun runner` | Pulls jobs, checks out a commit, runs the evidence check and the agents, reports results | LLM credentials, agent definitions |
| Agent | A command that reads a prompt and prints one JSON report, in a sandbox | Nothing beyond its job |

Both the server and the runner are subcommands of the one `raun` binary. Both read their configuration from `RAUN_*` environment variables.

## Flows

1. **Activation.** A user with write access on the forge activates a repository in a workspace. The server subscribes to its events.
2. **Push or pull request.** The forge sends a webhook. The server verifies it, normalizes it and enqueues an evidence check. A runner claims the job, checks the commit out, verifies every citation of the stored knowledge, and reports. The server publishes a commit status and, on a pull request, one comment listing the knowledge the change makes stale.
3. **Analysis.** A push to the default branch, or a manual trigger, enqueues an analysis, coalesced so that only the latest pending commit runs. The runner starts the analyst agents in sandboxes, verifies their citations, and consolidates and reconciles their findings with the existing knowledge. The server stores the result as proposed knowledge.
4. **Review.** People with access to the repository read the knowledge in the web interface and decide. A decision is signed by the authenticated user.

## Code layout

```
cmd/raun/                  wiring: configuration, adapters, subcommands
internal/core/<context>/   domain logic, one directory per bounded context
internal/adapter/driven/   implementations of the ports: database, forge, sandbox, Git
internal/adapter/driving/  entry points: HTTP server, webhooks, runner API
```

- A core context, its tests included, imports the standard library and its own context only. `make architecture` enforces it.
- A port is an interface declared in the core context that consumes it. Its adapter lives under `internal/adapter`.
- Only `cmd/raun` builds adapters and hands them to the core.
- A package is created by the epic that first needs it.

## Trust boundaries

The server, the runner, the agents, the analyzed repository and the browser trust each other only as far as the [threat model](threat-model.md) says. In short:

- the repository and its configuration are untrusted input;
- an agent is untrusted: it runs in a sandbox with an empty environment and restricted network;
- a runner is trusted with one job at a time, never with knowledge decisions;
- the server trusts the forge for identity and permissions, and nothing else.
