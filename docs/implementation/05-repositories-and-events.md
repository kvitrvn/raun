# Epic 5: repositories and events

Epic: #29. Review: #30. Decided by the owner on 2026-11-13.

## Scope

Let a person activate a repository of the forge in a workspace, and turn what happens on the forge into normalized events Raun can trust, read from and answer to: the activation model and the identity of a repository, the trust of webhooks, the normalized event and its log, how Raun knows a repository takes part, and the commit statuses and pull request comments it publishes.

Sources consulted: issues #29 to #34, the [epic 2](02-knowledge-core.md) (scoping), [epic 3](03-server-foundation.md) (bypass patterns, body bounds) and [epic 4](04-forge-port-and-identity.md) (port, capabilities, GitHub App, permission service, namespace and routes) records, [docs/forge.md](../forge.md), the [threat model](../threat-model.md) (T8, T9, T13, T18), and the GitHub documentation of webhooks, deliveries, commit statuses and issue comments.

The capabilities and the webhook contract are described in [docs/forge.md](../forge.md). This page records why.

## Choices

### Activation

- **Who.** A person activates or deactivates a repository in a workspace they belong to when the forge lets them decide on it (`admin`, `maintain` or `write` on GitHub), as the permission service of epic 4 answers it. No role of the workspace or of the instance counts: an owner of the workspace without write access on the repository cannot activate it, and a member with it can.
- **One workspace.** A repository of the forge belongs to one workspace once activated there, active or deactivated: an activation elsewhere is refused while it belongs to the first. One repository has one knowledge base, so two copies never drift apart, and human-validated content never has to be brought back together.
- **Deactivation** stops Raun from reacting: its events are dropped, and the jobs it still waits on are cancelled once jobs exist (epic 6). Its knowledge, its runs and its event log are kept, read-only, shown to those the forge lets read the repository. Reactivating it in the same workspace resumes where it stopped. Moving a repository to another workspace is deferred; deleting its knowledge belongs to epic 13, with retention.
- **Lost by the App.** A repository the App no longer reaches, because its installation was removed or no longer covers it, is treated as deactivated, with the same effects; it can be activated again once the App reaches it. Whether the App reaches it is a state of the repository (#31). An `installation` or `installation_repositories` delivery does not set it from its payload: it makes Raun ask the forge which repositories the installation covers, and set the state from the answer, so a delivery replayed later cannot undo what happened since.
- **Preconditions.** Activation needs the capabilities that make the repository work in Raun: Files (#33) to read its configuration, and Webhooks and Events (#32) to hear from it. The clone credential arrives with epic 6, which adds it to the preconditions; until then a repository is activated without it. A forge without one of them shows on the activation screen why the repository cannot be activated.
- **Configuration does not gate activation.** A repository without `.raun.yaml` can be activated: it shows as not configured, and its events are logged but trigger nothing until the file exists (see Configuration presence).

### Repository identity

- **The forge and its stable identifier** identify a repository, as `identity.Repository` and the permission service already do: `github` and the number GitHub gives it. The full name (`owner/name`) and the default branch are snapshots, refreshed from each listing and each webhook. A rename or a transfer on the forge keeps the identifier, and with it the activation, the knowledge, the event log and the cached permission.
- **The page of a repository** is `/<workspace>/<owner>/<name>`, at its current full name: an old name is not found, as no name of Raun redirects (epic 4). Two repositories of one workspace cannot share a full name on one forge; epic 12, which brings a second forge, decides how the address tells them apart.
- **Assets move to `/-/assets/`, served apart.** The standard mux refuses two patterns of which neither is more specific, such as `GET /assets/{file...}` and `GET /{workspace}/{owner}/{name}`, which both match `/assets/a/b` (epic 4 record); moving the assets to `/-/assets/{file...}` on the same mux would conflict the same way. So `/-/` holds the paths of Raun that no name can take (a name never starts with a hyphen), and the root mux serves it, before the mux of the application, behind the Host check and the security headers: its patterns never meet those of `/<workspace>/...`, which is then free for every page of a workspace. #31, which adds the first such page, makes the move and updates [docs/web.md](../web.md).
- **Scoping.** The knowledge store keeps its `Scope{Workspace, Repository}` (epic 2): since a repository belongs to one workspace once activated, its identity is unique on the instance, and every query still filters on both. The table of repositories of #31 holds that uniqueness on every row, active or deactivated; #30 adds no migration.

### Webhook trust

- **One secret per instance on GitHub.** A GitHub App has one webhook secret: `RAUN_GITHUB_WEBHOOK_SECRET`, with its `_FILE` form, part of the GitHub App set of variables, added by #32. A forge that needs a webhook per repository (epic 12) gets its own rule then.
- **Rotation** is a change of the secret in the App and in Raun, then a restart: the deliveries signed with the old secret in between are refused, and GitHub can deliver them again by hand. No previous secret is accepted.
- **The endpoint** is `POST /hooks/github`, one literal route per forge, never a `{forge}` wildcard: `POST /hooks/{forge}` and `POST /{name}/members` both match `/hooks/members` and neither is more specific, so the mux would refuse them at start, as it refused the assets (Repository identity), while the literal `/hooks/github` meets no pattern of a workspace. It is a bypass pattern of the epic 3 protections: no session, no CSRF token, no origin check; it keeps the Host check, the access log and the security headers, and authenticates by its signature alone. `hooks` joins the reserved names (#32).
- **The signature first.** The body is read once, up to 5 MiB, and its HMAC-SHA256 under the secret is compared in constant time with `X-Hub-Signature-256` before anything parses it. A missing or wrong signature answers the same refusal whatever the reason, and logs no payload; a body that does not fit is refused on its own, with `413`, before any signature is computed.
- **Duplicates and replays.** Two keys, kept for every signed delivery, those of no repository or of a repository that is not active included, in a table of deliveries of their own: the delivery identifier (`X-GitHub-Delivery`), which catches a delivery GitHub sends again, and the SHA-256 of the raw body. The header is not signed, so a signed body sent again under a new identifier is a replay; a replayed body is the same byte for byte, while two genuine deliveries differ, if only by their times (a pull request closed and reopened at the same head, a push back to a commit already seen). The keys and the row of the event log, when there is one, are written in one transaction: a failure to record leaves no key behind, so the delivery GitHub sends again by hand is not taken for a duplicate. A delivery that repeats either key answers success and does nothing more, including when two arrive at once or after a restart, since both keys are unique in the database. The keys are kept 30 days, like the event log: past them a delivery counts as new, and a body that old can at most ask again for work on a commit Raun already knows.
- **Bounds.** 5 MiB a body, `413` beyond: GitHub caps a delivery at 25 MB, 5 MiB holds ordinary pushes and pull requests in memory, and a delivery that does not fit is lost, its refusal logged. No rate limit is added: GitHub never delivers a refused delivery again on its own, so a quota would lose legitimate events; the signature check, the bounded body, the quick answer and the timeouts and header bound of epic 3 bound the cost of a forged one.
- **Answers.** A delivery is recorded, then answered `202`; the work it causes runs after the answer. A duplicate or a replay answers `200`; a refused signature `401`; a body too large `413`. An installation delivery is answered `202` and makes Raun check the installation with the forge (Lost by the App); a delivery for a repository that is not active is answered `202` and dropped. Neither gets a row in the event log of a repository, but both keep their keys. A failure to record answers `500`, which GitHub shows as failed and can deliver again by hand.

### Event model

- **Kept events.** `push` of a branch (`refs/heads/*`) or of a tag (`refs/tags/*`), and `pull_request` with the actions `opened`, `reopened`, `synchronize`, `ready_for_review` and `closed`. `installation` and `installation_repositories` only keep track of what the App reaches (Lost by the App). A signed delivery of any other event or action is recorded as ignored and does nothing.
- **A push** is normalized to: the repository (forge and stable identifier), the delivery, the ref and whether it is a branch or a tag, the commits before and after, whether it was forced, whether it deleted the ref, the head commit, and the sender (forge identifier and login).
- **A pull request** is normalized to: the repository, the delivery, the number, the action, the head and base refs and commits, the head and base repositories (forge identifiers), whether it is a draft, and the sender.
- **The fork marker.** A pull request is from a fork when its head repository is not its base repository. Epic 7 reads it at the one policy point of T8: a fork gets the deterministic check only, with no secret.
- **Payload values are data**, never paths, commands or templates; a value used in a page or a comment is escaped (T13).

### Event log

- **Stored, one row per delivery** of an active repository: the delivery identifier, the repository, the event and its action, the ref, the commits, when it was received, and what came of it (recorded, ignored, and later the jobs it created). A delivery that belongs to no repository, or to one that is not active, is not logged; the keys of every delivery live in their own table (Webhook trust).
- **Shown on the page of the repository**, to whoever the forge lets read it, newest first.
- **Retention.** 30 days until the epic 13 review decides the retention of events; a row past it is purged. The window of duplicates and replays is the retention of the keys, purged on their own (Webhook trust), not that of the log.

### Configuration presence

- **The default branch decides.** `.raun.yaml` is read at the root of the default branch: at activation, and at its head on each push to it. A pull request, from a fork or not, is analyzed with the configuration of the default branch, never with the one of its head: a pull request cannot change how it is checked (T8). The analyzed commit and the trusted configuration are two different things.
- **States shown** on the page of the repository: configured, not configured (no file), unreadable (the forge could not give it, or it exceeds the bound of #33). From epic 8, which decodes the file, invalid, with the field named. Until then, a file present counts as configured.
- **Reading** goes through the Files capability of #33, without cloning, with a bound on the size and a short cache keyed by repository and commit; #33 sets the bound: 64 KiB.

### Statuses and comments

- **A commit status**, not a check run: the App already asks for Commit statuses, and the Checks permission would make every installation approve the App again. Its context is `raun`; its states pending, success, failure and error; its description a short sentence; its link points back to Raun.
- **One comment per pull request**, updated in place: Raun finds it by a hidden marker on its first line, `<!-- raun -->`, among the comments the App itself wrote, so a comment of someone else that copies the marker is ignored, and nothing is stored to find it. Text taken from the repository is escaped before it enters the comment (T13).
- **Built in epic 5, used in epic 7.** #34 delivers both capabilities on the GitHub adapter and the fake forge, proved by the contract suite of `internal/forgetest`, which holds their contract until a context consumes them; epic 7, whose context publishes, declares the port and decides what it reports and when. The epic 4 record and `docs/forge.md` are corrected accordingly.

### Packages

| Package | Content |
| --- | --- |
| `internal/core/repository` | Repositories, activation and its rules, the normalized events and the event log, the configuration presence, and the ports they consume: files, webhooks and events of the forge, and the stores. A context of a later epic that needs an event declares its own type, which `cmd/raun` fills; the reviews of epics 6 and 7 decide how. |
| `internal/forgetest` | The fake forge and the contract suite gain the capabilities of epic 5, statuses and comments included. |
| `internal/adapter/driven/github` | The Files, Webhooks, Events, Commit status and Comment capabilities. |
| `internal/adapter/driven/sqlite` | Repositories and their activation, the event log, in new numbered migrations. |
| `internal/adapter/driving/web` | The Repositories screen and the page of a repository, the webhook endpoint, the assets under `/-/assets/`. |

## Implementation of #31

Choices #31 made within the decisions above, for the owner to confirm in its review:

- **Routes.** The Repositories screen is `/`, the home page already titled so; an activation is `POST /repositories`, with the stable identifier and a workspace of the account, which reserves `repositories`; the page is `GET /<workspace>/<owner>/<name>` and a deactivation `POST /<workspace>/<owner>/<name>/deactivate`. Activating again goes through `POST /repositories`. The snapshots of an activation come from the listing of the forge, never from the form.
- **Who sees what.** The screen lists what the forge lists for the person; a repository Raun knows shows its state and its workspace, since the forge lets the person read it. The page shows to whoever the forge lets read the repository, member of its workspace or not; the page of a workspace lists only the repositories of the workspace the forge lists for the visitor.
- **Full names.** A full name is unique on its forge, as on the forge itself: a listing or an activation that gives a name to a repository takes it from any other one Raun knows, whose snapshot stays empty, and its page not found, until the forge lists it again. A page answers at the full name as the forge spells it: another casing is not found, as an old name is, since no name of Raun redirects.
- **Reach.** One optional capability of the forge, `Reach`, gives every repository the App reaches, across its installations, or an error, never a part of them; `SyncReach` makes each other repository Raun knows unreachable and not active, and one reached again reachable and still deactivated. #32 calls it on an installation delivery and adds it to the GitHub adapter; the fake forge has it.
- **Preconditions.** No forge offers files, webhooks or events before #32 and #33, which declare their operations in this context and detect them: until then the screen names what is missing and nothing can be activated, outside tests. #32 and #33 did: a repository can be activated since.

## Implementation of #32

Choices #32 made within the decisions above, for the owner to confirm in its review:

- **The secret is required with the App.** `RAUN_GITHUB_WEBHOOK_SECRET` joins the set of the App, all or none: no instance runs without it, and none ran before (raun-old never did).
- **The hook is on the root mux.** Like `/-/assets/`, `POST /hooks/github` is served before the application, behind the Host check, the access log and the security headers: no protection of the application applies to it, its body bound of 64 KiB included, so it needs no bypass pattern. Its own bound is 5 MiB.
- **A signed delivery that is not one of GitHub** (a missing or malformed delivery identifier or event name, a body that is not one JSON object, a kept event without its required fields) is answered `400` and keeps nothing. Payloads are decoded as the answers of the API are, with the exception of the epic 4 record: the fields Raun reads are checked, the others ignored.
- **Webhooks needs no operation on GitHub**: the webhook of the App covers every repository it reaches, so a forge with Events has Webhooks too. A forge with a webhook per repository (epic 12) declares its operations then.
- **The keys are unique per forge**, so a second forge never collides with the first. The log is written only while the repository is active, as the transaction that keeps the keys sees it, so a deactivation cannot fall in between.
- **The sync of the reach** runs in the server, one at a time, after the answer to an installation delivery and once at start, which recovers a sync a stop cut short; requests that come during one make one more. A sync that fails is tried again after 30 seconds, then a wait doubled after each failure in a row up to 30 minutes, until one succeeds. No request is stored: a sync reads the forge, never a payload, and the jobs of epic 6 can take it over.
- **The event log** shows the 50 newest rows, with the ref and the before and after commits (the base and head commits of a pull request), and nothing of the commits in between. A pull request whose head repository the forge no longer knows counts as from a fork, the stricter answer.

## Implementation of #33

Choices #33 made within the decisions above, for the owner to confirm in its review:

- **The bound is 64 KiB.** A configuration is short; a larger file is unreadable, refused whole, never truncated. The context files of epic 8 get their own bounds there.
- **The cache** keeps a file, its absence or that it is too large, by forge, repository, commit, path and bound, 5 minutes, 1024 entries at most, the oldest dropped first; a failure of the forge is never kept, nor the head of a branch, which moves.
- **The head is resolved when the file is read**, not taken from a push: a delivery late or out of order, or a force-push, never installs an older configuration. A push records its event and asks for a read; the read takes the head of the default branch the forge gives then.
- **The last read is stored**, state, commit, time, reason, and the file of a configured repository, untrusted, for the decoder of epic 8 (migration 7). A read that fails stores unreadable without content: an older file never stands for the current one. A deactivated repository keeps its last read.
- **When it is read.** At activation, at once, 10 seconds at most, without ever refusing the activation; after the answer to a push of a commit to the default branch of an active repository, one read at a time, a repository asked for twice read once; and at start, for every active repository, which recovers a read a stop cut short. A pull request, a tag, another branch or a deleted branch reads nothing.
- **Before a first read**, the page says not read yet.
- **On GitHub**, the installation of a repository comes from `GET /repositories/{id}/installation`, and the token is minted for that repository alone, with Contents read.

## Deferred

| Topic | Where |
| --- | --- |
| Moving a repository to another workspace | Not planned; a later epic if needed |
| Deleting the knowledge of a repository, and the retention of events | Epic 13 |
| The clone credential in the preconditions of activation | Epic 6 |
| Cancelling the jobs of a deactivated repository | Epic 6, with the jobs |
| The format and decoding of `.raun.yaml`, and its invalid state | Epic 8 |
| What a status and a comment report, and when | Epic 7 |
| The analysis a push triggers | Epic 10 |
| The address of repositories of a second forge, and per-repository webhooks | Epic 12 |

## Sub-issues

| Issue | Change |
| --- | --- |
| #31 | Repositories and their stable identity, activation in one workspace, which a repository belongs to once activated, by a person who decides on the forge; deactivation and its effects; the reachable state, which an installation delivery makes Raun check with the forge; the Repositories screen and the page `/<workspace>/<owner>/<name>`; the assets moved to `/-/assets/`, served by the root mux. |
| #32 | `POST /hooks/github` as a bypass pattern with `hooks` reserved, `RAUN_GITHUB_WEBHOOK_SECRET` and its `_FILE` form, the 5 MiB bound and its `413`, the signature before parsing, duplicates and replays by delivery identifier and body digest for every signed delivery, the answers, the kept events and their normalization with the fork marker, the event log with its 30-day retention and its view on the page of the repository. |
| #33 | The Files capability, `.raun.yaml` read on the default branch at activation and on each push to it, the states configured, not configured and unreadable, the size bound and the cache. |
| #34 | The commit status `raun` and the one updated comment with its marker, found among the comments of the App, on the GitHub adapter and the fake forge. |

## Exit criteria

- [x] This page records the choices.
- [x] A test activates a repository on the fake forge, sends a signed push webhook and gets one normalized event (#31, #32).
- [x] A test refuses an unsigned webhook, ignores a duplicate delivery and a replayed body, and keeps a pull request closed and reopened at the same head (#32).
- [x] A test refuses an activation by a person without write access, and keeps the knowledge of a renamed repository (#31).
- [ ] A status and a pull request comment are published on the fake forge, and the comment is updated in place (#34).
- [ ] `make verify` passes (checked at the end of the epic).
