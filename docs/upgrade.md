# Hosted Raun: scope and roadmap

> **Status:** step 0 (reconciliation) is implemented; the rest is planned. This page records the decisions taken on 2026-10-29 and the order of the work.

Raun is a local CLI today. The knowledge base is plain YAML under `.raun/`, versioned in Git, and `raun serve` is a local browser for human decisions.

The goal is a hosted server, in the spirit of Drone CI:

- user accounts;
- workspaces, with a default workspace named after each user;
- linked forge accounts: GitHub first, then GitLab, Gitea, … behind one interface;
- runs triggered by events of a repository that contains `.raun.yaml`;
- human review in the web interface, by the people who have access to the repository.

The local CLI (`raun init`, `run`, `serve`, …) stays fully functional throughout. There is one product with a shared core, not two.

## Decisions

| Topic | Decision |
|---|---|
| Target | **Self-hosted**: one instance per organization, configured by an administrator. The data model stays ready for several tenants, but there is no billing and no public sign-up. |
| Source of truth | **The server database.** It keeps the same item model and the same revision-checked updates. YAML and Markdown become exports. Raun never writes to the analyzed repository. |
| Execution | **Separate runners.** A `raun runner` process pulls jobs. It can run on the user's own infrastructure with the user's own LLM credentials. The server never holds LLM credentials. |
| Triggers | **Two tiers.** Every push or pull request triggers a deterministic evidence check, with no LLM, and a pull request check that reports knowledge made stale. LLM analysis runs only on the default branch, with coalescing (only the latest pending push is analyzed), or on demand. |
| Identity | **Forge OAuth only in v1.** Signing up means logging in with a forge for the first time. Linking another forge later adds an identity to the same account. The Raun username is chosen at sign-up. There are no local passwords. |
| Permissions | **Taken from the forge.** Read access to the repository means viewing; write or maintain access means deciding. Permissions are checked and cached briefly. A workspace groups repositories, agents and secrets, and never grants more than the forge does. The author of a decision is the authenticated user. |
| Order | **Reconciliation first**, locally, before any server work. |

## Why these decisions

- **Reruns are a prerequisite.** A run triggered by an event is always a rerun on an existing knowledge base. Without reconciliation, every push would duplicate every item, and nothing would move validated knowledge to `needs-review`.
- **LLM runs are expensive.** A Drone build is deterministic and cheap. A Raun run is not: it involves several agents and a lead, each with long timeouts and retries, and its output is not deterministic. Running it on every event would cost a lot and flood reviewers with proposals. Deterministic verification, on the other hand, is free and gives the most valuable signal on pull requests.
- **The repository is untrusted.** Anyone who can push to the repository, or open a pull request from a fork, controls `.raun.yaml` and the analyzed code. The configuration must not be able to run commands, and the agents must be isolated from server secrets.
- **One source of permissions.** If workspace membership granted access to a private repository's knowledge, someone removed from the forge organization would keep that access. Forge permissions avoid this leak.
- **Accounts without passwords.** Nothing in hosted Raun is useful without a linked forge. Forge OAuth avoids password storage, email verification, password reset and second-factor authentication.

## Security constraints

1. **Split configuration.** The repository's `.raun.yaml` holds no command. It contains only:
   - analysis parameters: types, sources, language, quorum, branch and path filters;
   - agent references by name.

   Agent definitions (command or image, timeouts, secrets) belong to the runner or the workspace. This is a new configuration format version, decided when its step starts.
2. **Empty agent environment.** Runners start agents with an empty environment and only explicitly declared secrets. Agents never inherit server secrets.
3. **Sandboxed agents.** Agents run in ephemeral containers with restricted network access. The analyzed code may contain prompt injection, so an agent must not be able to reach anything it could leak secrets to. The technology is chosen in the runner step. Today's disposable copy (`internal/snapshot`) isolates nothing.
4. **Pull requests from forks** get the deterministic check only: no secrets, no LLM.
5. **Raw artifacts** (prompts and outputs contain customer code) are isolated per repository and follow a retention policy.
6. **CSRF, origin checks and sessions** build on the existing protections in `internal/web` (`decide.go`, `server.go`), adapted to authenticated users.

## Roadmap

One step at a time, each reviewed by the owner before the next starts. Each step settles its own open decisions before any code is written.

0. **Reconciliation (local). Done.** Rerun on an existing knowledge base:
   - attach new interpretations to existing items;
   - move `validated` items to `needs-review` when their ground changes;
   - never rewrite validated content.

   A `reconciler` agent matches the run's items to existing ones (`internal/reconcile`, contract in `docs/agents.md`).
1. **Storage abstraction.**
   - Extract an interface from `internal/knowledge/store.go` (`List`, `Get`, `Save`, `Update(id, revision, mutate)`, `NewID`). The current file backend stays the local implementation.
   - Add a database backend. The choice between pure-Go SQLite and Postgres, and the justification for the new dependency, are made in this step.
   - Do the same for run manifests.
2. **Configuration split.** `.raun.yaml` for the repository, agent definitions for the runner or workspace. It is a new format version, so it needs the owner's decision and updates to `docs/agents.md` and `docs/format.md`.
3. **Server foundation.** It covers:
   - `raun server` itself, with sessions and CSRF protection;
   - accounts created through GitHub OAuth;
   - a default workspace named after the user;
   - one namespace shared by users and workspaces;
   - administrator configuration.
4. **Forge interface and GitHub App.** The interface describes capabilities, not the GitHub App model, because GitLab and Gitea rely on OAuth, per-repository webhooks and tokens. Woodpecker's `forge.Forge` interface is a useful reference. It covers:
   - login;
   - repository listing;
   - a user's permission on a repository;
   - reading a file at a commit (`.raun.yaml`, context files);
   - clone credentials;
   - webhook activation and deactivation;
   - parsing a webhook into a normalized event;
   - commit status or check;
   - pull request comment.

   This step also adds a fake forge for tests (`internal/forgetest`) and repository activation in a workspace. For GitHub, a GitHub App is preferred over an OAuth App: short-lived installation tokens, managed webhooks, the Checks API, fine-grained permissions, and no user token stored for cloning.
5. **Job queue and `raun runner`.** The runner registers with the server, pulls jobs, clones and starts a sandbox. The first job is the deterministic check on every push, with the pull request check. It reuses `internal/evidence`, `internal/gitx` and `internal/snapshot`.
6. **LLM analysis through the runner.** It reuses `internal/run`, `internal/consolidate`, `internal/agent` and reconciliation, on the default branch, with coalescing and manual triggers.
7. **Multi-repository web interface.** It reuses `internal/web`: templ templates, `decide.go`, revision checks. Permissions come from the forge, and the decision author is the authenticated user instead of a free-text field.
8. **Second forge (Gitea)**, to validate the forge abstraction.

## Verification

Every step passes the commands in `AGENTS.md` (`go build`, `go test`, `go vet`, `golangci-lint`, `gofmt`). Tests never use the network or a real LLM. They use the fake agent from `internal/agenttest`, the fake forge, and fixture repositories from `internal/gittest`.

From step 3 on, an end-to-end test runs locally with a server, the fake forge (simulated webhook), a runner and the fake agent. It checks three things:

- the pull request check appears;
- knowledge items appear;
- a decision is signed by the authenticated user.

`README.md`, `AGENTS.md`, `CLAUDE.md` and `docs/` are kept in sync at each step.
