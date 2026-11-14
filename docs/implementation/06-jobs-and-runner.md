# Epic 6: jobs and runner

Epic: #35. Review: #36. Decided by the owner on 2026-11-14.

## Scope

Turn events into jobs that separate `raun runner` processes pull and execute: the job and its runs, the queue stored in the database with leases, how a runner is registered, trusted, routed and revoked, the protocol between runner and server, the short-lived credential that lets a runner fetch one commit, and the log of a run. What a job does is out of scope: the evidence check is epic 7, the analysis epics 8 to 10, the container sandbox epic 9.

Sources consulted: issues #35 to #42, the [epic 2](02-knowledge-core.md) (commit reader, controlled Git, SQLite writer), [epic 3](03-server-foundation.md) (bypass of the protections, body bounds, `RAUN_RUNNER_*` variables, logging, secrets), [epic 4](04-forge-port-and-identity.md) (capabilities, installation tokens, administrators) and [epic 5](05-repositories-and-events.md) (deliveries, event log, activation preconditions, deactivation, server workers) records, [docs/forge.md](../forge.md), [docs/architecture.md](../architecture.md), the [threat model](../threat-model.md) (T8, T15 to T18), the SQLite adapter (`internal/adapter/driven/sqlite/sqlite.go`: one writer, `BEGIN IMMEDIATE`, a busy timeout), the GitHub adapter (`repositoryScope` in `files.go`), and the Drone, Gitea Actions and GitLab runner models.

The protocol and the operation of a runner are described in [docs/runner.md](../runner.md). This page records why.

## Choices

### Job model

- **A job and its runs.** A job is the unit of the queue: what must be done, on which commit. A run is one attempt of a job by one runner, with its lease, its log and its outcome. A run counts once its runner has been given its job (one the claim created but never handed out is removed, see Clone credential): a job has at most 3 runs, the lost ones retried twice at most. The identifier of a run is the one epic 10 writes into the knowledge as the run that found or supported an item (`by` of an event, `run` of a support entry), so a knowledge item points to the attempt that produced it and to its log.
- **A job** holds its kind, its workspace and repository (forge and stable identifier), its commit and ref, the delivery that caused it (forge and delivery identifier, none for a job asked by hand), its priority, its state, its coalescing key when it has one, and its times: created, not before, finished.
- **Kinds.** `noop` in epic 6, which does nothing and serves the tests; `check` comes with epic 7, `analysis` with epic 10. Each kind declares the capability a runner needs to take it, its maximum duration (`noop`: 5 minutes; epics 7 and 10 set theirs), the bound and the validation of its result, and its priority. A kind is added by the epic that needs it, never ahead.
- **States of a job.** `queued` → `running` → `succeeded` or `failed`; `queued` or `running` → `cancelled`. A job whose run is lost goes back to `queued`. `succeeded`, `failed` and `cancelled` are final.
- **Outcomes of a run.** `succeeded`, `failed`, `lost` (its lease expired, or its runner was revoked) and `cancelled`; while it runs it has none.
- **Priority.** A check before an analysis, so a pull request gets its answer while an analysis waits; within a priority, the oldest first, by creation time then identifier.
- **Event, job and run.** A job names the delivery that caused it; the row of the event log of that delivery shows the jobs it created; a run belongs to one job. A job never holds the payload of a delivery: it holds what its kind needs, read from the normalized event when it is created.

### Queue semantics

- **Stored in the database**, no broker: jobs, runs and runners are tables of the server database, behind ports of the core, with a contract suite run on a memory store and on SQLite.
- **A claim is one short transaction** on the writer of SQLite, which begins `IMMEDIATE` and so serializes claims: it selects the first `queued` job the runner may take (its scope and capabilities, `not before` passed), by priority then age, creates its run and sets the job `running`, and commits. No call to the forge and no wait happen inside it. Two claims never get the same job, since a second writer waits for the first.
- **The lease** lasts 60 seconds and is renewed by a heartbeat that the runner sends every 15 seconds: three heartbeats can be lost before the lease expires, and 60 seconds stays far above the busy timeout of the writer. Each run gets a lease token, random, given once in the answer to the claim and stored hashed; every heartbeat, log and result carries the run and its token, and is refused with `409` when the run is not the current one of its job or is no longer running.
- **Expiry.** A sweeper of the server, every 15 seconds, ends each run whose lease has expired as `lost`; its job goes back to `queued` at once while it has had fewer than 3 runs, and fails otherwise. A claim never waits for the sweeper: the transaction of a claim ends the expired runs it meets first.
- **Retry.** Only a lost run is retried. A failure the runner reports is final: a deterministic check that fails would fail again, and an analysis that fails is retried by its own rules (epic 9 retries a transient agent failure within the run).
- **Maximum duration.** A run that has not reported when its kind's maximum duration has passed is ended as `failed` (timeout), and its job fails without retry; the runner stops its job at the same deadline, which the claim gives it.
- **Expiry in the queue.** A job still `queued` 24 hours after its creation fails as expired. A job sent back to the queue keeps its creation time, so a job that keeps losing its runs, or that no runner can take, ends within a day.
- **Cancellation.** A queued job is cancelled at once. A running job is cancelled at once too: its run ends as `cancelled`, the next heartbeat answers that it is cancelled, the runner stops, and any later result is refused. Deactivating a repository, or losing it to the App, cancels its queued and running jobs (the deferral of the epic 5 record). A person who can decide on the repository can cancel by hand (#41).
- **The guarantee.** At most one result is accepted per job, from its current run, while the run holds its lease. Two executions can overlap: a runner cut from the server keeps running until its next heartbeat fails, then stops; the queue fences what it accepts, not what a lost runner does.
- **Idempotency.** A result sent again for the same run, with the same content, is answered as the first time; a different one is refused. A log chunk sent again is ignored (Logs).

### Enqueue and coalescing

- **One transaction per delivery.** The keys of a delivery, its row of the event log and the jobs it creates are written in one transaction: after a crash, a delivery is recorded with its jobs or not at all, and GitHub delivers it again by hand. The repository context declares the requests for jobs it records with a delivery, in its own type, and the SQLite adapter writes them as jobs of the job context; `cmd/raun` composes the two, so no core context imports the other.
- **The policy point.** Which jobs an event creates is one function, wired in `cmd/raun`, deny by default (T8). In production it creates nothing until epic 7, which decides it; the end-to-end test wires a policy that creates a `noop` job for a push. A delivery that is not recorded, a duplicate or an event of a repository that is not active, creates nothing.
- **The coalescing hook.** An enqueue may carry a key. In the transaction that inserts the job, the `queued` jobs of the same workspace with the same key are cancelled as superseded; a `running` job is never touched. Epic 10 builds the key (such as the repository, the kind and the branch) and the policy that uses it; epic 6 provides the operation and its tests.
- **The workers of the server stay in the server.** The refresh of the configuration (#33) and the sync of the reach (#32) call the forge with the credentials of the App, which no runner ever holds; they are not jobs.

### Protocol

- **HTTP, started by the runner.** The server never connects to a runner, so a runner can sit behind a NAT on the infrastructure of its owner.
- **The routes** are under `/api/runner/v1/` on the root mux, like `POST /hooks/github` (epic 5): no session, no CSRF token, no origin check, none of the protections of the application, the Host check, the access log and the security headers kept; `api` is already reserved. Each route authenticates the runner itself (Runner trust). This settles the deferral of the epic 3 record: the runner API needs no bypass pattern.
- **Long polling.** `POST /api/runner/v1/claim` waits up to 30 seconds for a job the runner may take, woken in memory whenever a job becomes claimable (an enqueue, a job sent back to the queue by the sweeper or a revocation), and answers `200` with the job or `204` with none. The wait holds no transaction: each wake-up runs the short claim again. A job whose `not before` passes wakes nobody: the next poll takes it, within 30 seconds. A short poll would add up to its interval to every job; 30 seconds stays below the usual timeouts of a proxy.
- **The other routes**: `POST /api/runner/v1/runs/{run}/heartbeat`, `.../log` and `.../result`, each bound to the lease of the run.
- **Payloads** are JSON, decoded strictly: unknown fields refused with `400`, a body past the bound of its route with `413` as the epic 3 record answers it, the decoded text of a log chunk bounded on its own (docs/runner.md), and a result validated by its kind before anything is stored (#39). A result that fails validation fails its job and stores nothing else.
- **Versioning.** The protocol has a version, 1, in the path and in the header `Raun-Runner-Protocol` of every request; the runner also sends its release in `User-Agent`. A server answers a protocol it does not speak with `400` and a body that names the versions it speaks, in the header or in the path: every path under `/api/runner/` is its own, so `/api/runner/v2/claim` gets that answer rather than a page that does not exist; the runner logs it and stops with a non-zero exit, rather than polling a server it cannot work with. A runner and a server of different releases work together as long as they share the protocol; a change that breaks it is a new version, recorded on this page, and the server keeps speaking the previous one for at least one release (epic 13 settles release versioning).

### Runner trust

- **Instance administrators create runners**, on an Instance screen (`RAUN_ADMINS`, epic 4): a name, a scope and capabilities. Nobody else does: a workspace owner who could create a runner for the workspace would receive the code of every repository of it, those the forge does not let them read included, which the forge permissions forbid (AGENTS.md).
- **A token per runner, given directly.** Creating a runner, or regenerating its token, shows a token of 32 random bytes once; the server stores its SHA-256 only. The runner reads it from `RAUN_RUNNER_TOKEN` or `RAUN_RUNNER_TOKEN_FILE` and sends it as a bearer token on every request. No registration token is exchanged: the exchange would add a state file on the runner and a step, for a secret that is shown once either way.
- **What the token opens**: the routes of `/api/runner/v1/` only, never the web interface, the knowledge or another route of the server. A request without a known token, or with the token of a revoked runner, is refused with `401`.
- **Rotation** replaces the token: the old one is refused at once. A runner still using it stops at its next request, and its runs are lost when their leases expire; the administrator rotates a token while the runner is stopped, or accepts that.
- **Revocation** refuses every later request of the runner and, in the same transaction, ends its runs as `lost`, so their jobs go back to the queue (within the 3 runs).
- **Scope.** A runner serves the instance, or one workspace. A runner of a workspace takes only the jobs of that workspace; a runner of the instance takes the jobs of every workspace.
- **What a compromised runner can do**: read the code of the repositories of the jobs it takes, the clone credential of each (one repository, read, until the run ends and one hour at most), and what a job of its kind carries (from epic 7, the knowledge of the repository the check verifies); send logs and results for its own runs. Results are validated and can never decide on knowledge (T7, T15). It cannot reach the jobs outside its scope or its capabilities, the web interface, another repository, or a credential of the App.
- **The server address.** The runner refuses a server address in plain HTTP outside the loopback interface, so its token never travels in clear.

### Routing

- **A job goes to a runner by scope and capability.** The scope of the runner covers the workspace of the job, and the runner has the capability the kind of the job needs. Capabilities: `noop` (epic 6), `check` (epic 7), `agents` (epics 9 and 10).
- **The administrator sets the capabilities** when creating the runner, and changes them later; a runner never declares its own, so a runner taken over cannot widen what it receives.
- **Labels are deferred** until a job needs one (epics 8 to 10): no job of epic 6 or 7 chooses among runners of the same capability.
- **No runner can take it.** A job stays `queued`, and its page says that no runner can take it: no runner of its scope with its capability, or none seen in the last two minutes. It fails as expired after 24 hours in the queue (Queue semantics).
- **Concurrency.** A runner runs `RAUN_RUNNER_CAPACITY` jobs at a time, 1 by default, and claims only when a slot is free.

### Clone credential

- **A new optional capability of the forge**, Clone: a credential that fetches one repository, short-lived. Its contract is in the contract suite of `internal/forgetest`, on the fake forge and on GitHub; the job context declares the port it consumes. A repository of a forge without it cannot be activated from epic 6 on (the deferral of the epic 5 record).
- **On GitHub**, an installation token for that repository alone, with Contents read, which GitHub makes last one hour; it reuses the scope of the token that reads files (#33).
- **Issued at claim, held in memory only.** The server mints it after the transaction of the claim, never inside it, and gives it in the answer to the claim only: it is never written to the database, a file or a log, on the server or on the runner. It is minted for the run, apart from the tokens the server caches for its own reads, and the server revokes it when the run ends, where the forge allows it (GitHub does, `DELETE /installation/token`), so it rarely lives its full hour; a server that restarts before the run ends has lost it and cannot revoke it, and it then expires on its own within the hour. When minting fails, the runner is not given the job: a second transaction removes the run the claim created, which no one has seen, and puts the job back to `queued` with `not before` 30 seconds later, so a forge that is down is not hammered and no run is counted; the expiry of the queue bounds the whole. The claim goes on waiting for another job.
- **Used once.** The runner fetches the one commit of the job (`git fetch --depth 1` of its identifier) with the credential in an HTTP header that the environment of that Git process alone gives, never on the command line, in a file or in the configuration of the checkout, then forgets it before the handler of the job starts. A fetch that fails three times fails the job without retry: the credential, the commit or the forge is at fault, not the runner. It is not renewed: no planned job fetches again after its start, and one hour is longer than the maximum durations decided so far.
- **It covers the repository, not the commit**: whoever holds it can read the whole repository until it is revoked or expires, which the threat model counts (Runner trust).
- **A pull request from a fork**: its head commit is fetched by its identifier from the base repository, which holds it on GitHub; the credential never covers the fork.

### Logs

- **Content.** Lines of text written by the runner: its steps (fetch, handler, outcome) and what the handler of the job writes. Never a credential, a URL holding one, the environment, a declared secret, or the content of the repository. The runner redacts the clone credential and the secrets it knows; the server does not count on it, and shows a log as untrusted text.
- **Transport.** Numbered chunks of 64 KiB at most, appended in order: a chunk sent again is ignored, one that skips a number is refused with `409` and sent again by the runner. A run keeps 1 MiB of log; past it, the log ends with `(truncated)` and the rest is dropped.
- **Shown** on the page of the run, as escaped text, to whoever the forge lets read the repository (T13), and followed live by an HTMX poll every 2 seconds from the last offset shown, which needs no script from another origin and no new header.
- **Retention.** 30 days, purged with the events, until the epic 13 review decides the retention of logs; jobs and runs are kept, since knowledge names its runs.

### Runner and Git

- **The runner owns its checkouts.** Each run gets a new directory below `RAUN_RUNNER_WORKDIR`, created empty, removed whatever the outcome, and on start for any left by a runner that was killed. It reads the commit through the commit reader of epic 2, which already runs Git with a controlled environment and no hooks (T16).
- **Git 2.32 or later**, the minimum of the commit reader; the runner checks it at start and stops otherwise. Epic 13 decides how a runner is shipped.
- **Shutdown.** On `SIGINT` or `SIGTERM` the runner stops claiming and finishes its current jobs, each within its deadline; a second signal abandons them, and their leases expire.

### Packages

| Package | Content |
| --- | --- |
| `internal/core/job` | Jobs, runs, runners and their rules: kinds, states, priority, leases, expiry, retry, cancellation, coalescing, routing; the ports of the stores, of the clone credential and of the policy point; the loop of `raun runner` and its ports (the server, the checkout, the handler of a kind). |
| `internal/core/job/jobtest` | The contract suite of the stores, and an in-memory store that passes it. |
| `internal/core/repository` | Requests for jobs recorded with a delivery, and the cancellation of the jobs of a deactivated repository, in its own types. |
| `internal/adapter/driven/sqlite` | Jobs, runs, runners and logs, in new numbered migrations; the requests of a delivery written as jobs in its transaction. |
| `internal/adapter/driven/github` | The Clone capability. |
| `internal/forgetest` | The fake forge and the contract suite gain the Clone capability. |
| `internal/adapter/runnerapi` | The messages of the protocol and its version, shared by the server and the runner, standard library only. |
| `internal/adapter/driving/web` | The runner API under `/api/runner/v1/`, the Runners screen of the instance, the Runs list and the page of a run. |
| `internal/adapter/driven/runnerclient` | The client of the runner API that `raun runner` uses. |
| `cmd/raun` | `raun runner`, its `RAUN_RUNNER_*` variables, and the policy point wired in the server. |

## Deferred

| Topic | Where |
| --- | --- |
| Which jobs an event creates, and what forks may trigger (T8) | Epic 7 |
| The `check` kind, its maximum duration and its result | Epic 7 |
| Runner labels | Epics 8 to 10, when a job needs one |
| How the runner is isolated from the host when it drives containers | Epic 9 |
| The `analysis` kind, the coalescing key and its policy | Epic 10 |
| The clone credential of Gitea and Forgejo | Epic 12 |
| The retention of logs, and how a runner is shipped and upgraded | Epic 13 |
| Runners created by workspace owners | Not planned; a later review if needed |

## Sub-issues

| Issue | Change |
| --- | --- |
| #37 | The job and run model, kinds with `noop`, states, priority; the queue on SQLite: claim in one `IMMEDIATE` transaction, lease of 60 seconds and its token, heartbeat, result, failure, cancellation, the sweeper, at most 3 runs per job with only lost runs retried, the run removed and the job delayed 30 seconds when its credential cannot be minted, the maximum duration, expiry after 24 hours in the queue, `not before`; the coalescing hook; the requests of a delivery written with it in one transaction and the policy point, empty in production; the cancellation of the jobs of a deactivated repository; the contract suite on memory and SQLite. |
| #38 | Runners created by instance administrators, with a name, a scope (instance or one workspace) and capabilities; a token shown once and stored hashed; the authentication of `/api/runner/v1/` by bearer token, apart from sessions; rotation, and revocation that loses the runs of the runner at once; the Instance screen that creates, rotates and revokes. No registration token, no labels. |
| #39 | The routes under `/api/runner/v1/` on the root mux; the long-polling claim of 30 seconds with routing by scope and capability; heartbeat with cancellation, log chunks, result validated by its kind; the protocol version and the `400` of an unknown one; the bounds; the Clone capability on the fake forge and GitHub with its contract, minted per run after the claim, held in memory only, revoked when the run ends, and added to the activation preconditions; every path under `/api/runner/` answered by the runner API; the protocol part of `docs/runner.md`. |
| #40 | `raun runner`: `RAUN_RUNNER_SERVER`, `RAUN_RUNNER_TOKEN` and its `_FILE` form, `RAUN_RUNNER_CAPACITY`, `RAUN_RUNNER_WORKDIR`; HTTPS outside loopback; Git 2.32 checked at start; the loop with heartbeats, the stop on a lost lease or a cancellation, the deadline; the fetch of one commit with the credential in the environment of Git alone; the work directory removed whatever the outcome; the handler of a kind with `noop`; the shutdown; the operation part of `docs/runner.md`. No labels. |
| #41 | The Runs list of a repository and the page of a run with its log polled every 2 seconds, escaped, to those the forge lets read the repository; the manual cancellation by a person who decides on it; the reason a job waits; the Runners screen gains the last contact, the release and the current runs. |
| #42 | The end-to-end test with a policy that creates a `noop` job for a push, lease durations shortened for the test, and a runner killed mid-job whose lease expires and whose job is taken again. |

## Exit criteria

- [x] This page records the choices.
- [ ] The end-to-end test starts a server, the fake forge and a runner, sends a simulated webhook, and sees a job claimed, executed and finished (#42).
- [ ] A test kills a runner in the middle of a job and sees the lease expire (#37, #42).
- [ ] Concurrent claims never return the same job, on SQLite (#37).
- [ ] A revoked runner is refused, and a runner of a workspace never claims a job of another (#38).
- [ ] An expired or foreign lease is refused, and a result that fails validation stores nothing (#39).
- [ ] The clone credential is absent from the checkout, the environment of the handler and the logs (#40).
- [ ] `make verify` passes (checked at the end of the epic).
