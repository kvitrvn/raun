# Threat model

> Status: written in epic 1, before any feature exists. Each threat names the epic that answers it; that epic updates this page and links the test that proves the answer. **Implemented so far:** the Git part of T16 (epic 2, #11) and the verification of T5 (epic 2, #13). No other control is implemented yet.

## Scope

One Raun instance serves one organization: a server, one or more runners, and the forges it is connected to. Everyone who can push to an activated repository, or open a pull request against it, can influence what Raun reads. The design starts from that: **the analyzed repository is untrusted**, and so is everything an agent produces from it.

## Assets

| Asset | Where it lives | Why it matters |
| --- | --- | --- |
| Forge credentials: App private key, installation and user tokens, webhook secrets | Server | They give access to every repository the instance reaches |
| LLM credentials | Runners only | They cost money and can leak data to a third party |
| Analyzed code | Forge; runner checkouts for the duration of a job | It may be private |
| Raw prompts and agent outputs | Run artifacts | They contain code of the repository |
| Knowledge base and its history | Server database | Its value rests on every claim being backed by verified evidence |
| Authorship of decisions | Server database | Only an identified human may validate or reject |
| Sessions and runner credentials | Server database, browsers, runners | They act on behalf of a person or a runner |

## Actors

| Actor | Can | Trust |
| --- | --- | --- |
| Visitor without an account | Reach the public endpoints: login, webhooks, runner API | None |
| Member with read access on a repository | View its knowledge, runs and checks | View only, for as long as the forge grants it |
| Member with write access | Activate the repository, trigger analysis, decide | Decide, for as long as the forge grants it |
| Person who lost access on the forge | Still hold a session or a cached permission | None once the cache expires |
| Anyone who can push to an activated repository | Change the code, the context files and `.raun.yaml` | Untrusted input |
| Author of a pull request from a fork | Same as above, on the fork's branch | Untrusted input, deterministic check only |
| Agent | Read its snapshot, print a report, reach the hosts allowed to it | Untrusted, assumed hostile after prompt injection |
| Runner | Claim jobs, receive a short-lived clone credential, report results | One job at a time; a compromised runner is considered |
| Instance administrator | Configure the instance, register runners | Trusted for operation, not for repository knowledge |

## Trust boundaries

1. **Browser and server**: sessions, cross-site requests, rendering of repository content.
2. **Forge and server**: webhooks in, API calls out, tokens.
3. **Server and runner**: the runner API, job payloads, results.
4. **Runner and agent**: the sandbox: filesystem, environment, network, resources.
5. **Repository and Raun**: configuration, context files, code and Git metadata read by the server, the runner and the agents.
6. **LLM output and Raun**: reports, citations, plans and matches returned by agents.

## Threats and answers

| # | Threat | Answer | Epic |
| --- | --- | --- | --- |
| T1 | `.raun.yaml` makes the runner execute a command, pull an image or read a secret | The repository configuration has no field for a command, an image, an environment variable or a secret, and is decoded strictly. Agent definitions and secrets belong to the runner. | 8 |
| T2 | Prompt injection in the analyzed code turns an agent against Raun | Agents run in ephemeral containers that see one snapshot, with an empty environment and only their declared secrets. They cannot reach the runner, the server, the forge or the local network. | 9 |
| T3 | An agent exfiltrates code or secrets over the network | Egress is denied by default and limited to an allowlist set on the runner, never by the repository. No forge credential enters a container. | 9 |
| T4 | Secrets inherited from the runner or server environment reach an agent | Nothing is inherited. Declared secrets are injected by name and redacted from logs and artifacts. | 9 |
| T5 | An agent invents or alters a citation to support a claim | Raun verifies every citation against the checkout of the analyzed commit, never against a snapshot an agent could change. Unverified evidence supports nothing. Epic 2: the verifier reads the commit itself through the commit reader, never the agent's output, and gives a state and a reason by deterministic rules; an excerpt that matches several places is never moved to one of them (`internal/core/knowledge/verify_test.go`: `TestIntegrity`, `TestFreshness`; `internal/adapter/driven/git/verify_test.go`: `TestVerifyOnARepository`). Epic 10 runs it on every agent report. | 2, 10 |
| T6 | An agent report breaks the parser, or a plan drops or forges elements | Agent output is size-bounded and decoded strictly, element by element. A lead plan or reconciler answer that refers to unknown elements, or omits any, is rejected. | 8, 10 |
| T7 | A run validates, rejects or rewrites knowledge | Only an authenticated human decision moves an item to `validated` or `rejected`. A run appends; it never rewrites validated content. | 2, 10, 11 |
| T8 | A pull request from a fork reaches secrets or LLM analysis | A single policy point decides which jobs an event may create, deny by default. Forks get the deterministic check only, with no secret. The fork's `.raun.yaml` changes nothing. | 7 |
| T9 | Forged or replayed webhooks trigger work or change state | Signatures are verified before parsing, duplicates have no further effect, and events of repositories that are not activated are dropped. | 5 |
| T10 | Someone removed from the forge keeps access to knowledge | Permissions come from the forge, with a short cache, and an unknown permission means refusal. Workspace membership and the administrator role grant nothing on repository data. | 4, 11 |
| T11 | Cross-site requests make a logged-in user decide or activate | Sessions with strict cookie attributes, CSRF tokens, origin, Host and `Sec-Fetch` checks, and a strict Content Security Policy. Webhooks and the runner API opt out explicitly and have their own authentication. Decided by epic 3, not implemented yet ([record](implementation/03-server-foundation.md#request-protections)): server-side sessions with a hashed identifier and a `__Host-` cookie, `SameSite=Lax`; `http.CrossOriginProtection`; a per-session token on every non-safe request; a Host check against `RAUN_PUBLIC_URL`; no proxy header trusted; a policy without inline script or style. #20 links the tests. | 3 |
| T12 | Login is hijacked: forged OAuth state, replayed callback, open redirect | State bound to the browser, single-use callbacks, redirects limited to the instance. | 4 |
| T13 | Repository content injects markup into the web interface or a forge comment | Repository text, agent output and logs are rendered and posted as escaped text. Epic 3 decides templ, which escapes every value, and a check of `make verify` that refuses `templ.Raw` ([record](implementation/03-server-foundation.md#request-protections)); not implemented yet. | 3, 5, 11 |
| T14 | Raw artifacts of one repository are read through another, or kept forever | Artifacts are stored per repository, readable only by people who can view it, and purged by a retention policy. | 10, 13 |
| T15 | A compromised runner reads other repositories or tampers with knowledge | A runner receives one repository, one commit and a short-lived scoped credential per job. Its credential reaches the runner API only. Its results are validated by the server and can never decide. Runners can be scoped to a workspace and revoked. | 6 |
| T16 | Repository content attacks the Git process: hooks, option injection, paths outside the tree | Git runs with a controlled environment and no hooks. Paths from agents or configuration are never passed as options and must stay in the tree. Snapshots do not follow links that leave them. Epic 2: the commit reader builds the environment of Git from nothing, ignores the user and system configuration, overrides hooks, the file system monitor, the pager, lazy fetching and replacement objects, denies every transport with an empty protocol allowlist, refuses partial clones, never passes a path to Git and refuses paths that start with a dash or leave the tree; it never follows a link (`internal/adapter/driven/git/git_test.go`: `TestIgnoresHostileConfiguration`, `TestReadFilesRefusesInvalidPaths`, `TestReadsDoNotWrite`, `TestOpenRefusesAPartialClone`, `TestNeverFetchesAMissingObject`, and `TestListTree` and `TestReadFiles` for links; `guard_test.go`: `TestEnvironmentDeniesEveryTransport` and `TestEnvironmentBlocksLazyFetching` prove against a control that the empty allowlist holds on its own on every supported Git, and `GIT_NO_LAZY_FETCH` on its own from Git 2.44, the first to obey it; before, the allowlist and the refusal of partial clones at `Open` hold). | 2, 6, 9 |
| T17 | Forge or LLM credentials leak through logs, errors or the database | Secrets are encrypted at rest, never logged, never rendered back, and never sent to a runner beyond a scoped clone credential. Epic 3 decides the fields never logged and a secret type that logs as `[redacted]` ([record](implementation/03-server-foundation.md#logging)), and the encryption scheme (AES-256-GCM, a versioned envelope, a key from `RAUN_SECRET_KEY` with a previous key for rotation), which epic 4 implements with the first stored forge tokens ([record](implementation/03-server-foundation.md#secrets-at-rest)); not implemented yet. | 3, 4, 13 |
| T18 | A large repository, a huge output or a flood of events exhausts the instance | Sizes are bounded everywhere: webhook bodies, configuration, agent output, artifacts. Pending analyses are coalesced, and per-repository limits hold runs back. | 5, 9, 10 |

## Not decided yet

- The container runtime and how egress filtering is implemented (epic 9 review).
- The retention durations of artifacts, logs and events (epic 13 review).
- How the runner itself is isolated from the host when it drives the container runtime (epic 9 review).
- Tenancy: one organization per instance is the model; mutually untrusted tenants on one instance are not in scope.
