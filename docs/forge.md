# Forges

> Status: decided by the [epic 4 review](implementation/04-forge-port-and-identity.md) (#23). The port of epic 4 exists in `internal/core/identity`: the types, `Forge` (login, repositories, permission) and the optional `Memberships` (#24); the fake forge and the contract suite are in `internal/forgetest`. The GitHub adapter is in `internal/adapter/driven/github` (#25), built at start when an App is configured; the login that uses it comes with #26. The capabilities of epic 5 (files, webhooks, events, commit status, comment) are decided by the [epic 5 review](implementation/05-repositories-and-events.md) (#30), not implemented yet; the clone credential comes with epic 6.

Raun reaches a forge through one port: a small set of operations every adapter implements, and optional capabilities an adapter implements when its forge offers them. A caller detects a capability by type assertion and handles its absence; a missing capability is never an error.

## Capabilities

| Capability | Required | Operations | Consumer | When absent |
| --- | --- | --- | --- | --- |
| Login | Yes | Authorization URL with state and PKCE; exchange of a code for the identity and the tokens of a person; refresh of a token | Epic 4 (#26) | — |
| Repositories | Yes | The repositories a person can reach through Raun | Epic 4, epic 5 (#31) | — |
| Permission | Yes | The permission of a person on a repository | Epic 4 (#28) | — |
| Membership | No | Whether a person belongs to an organization | Epic 4 (#26), for `RAUN_SIGNUP_ORGS` | An entry of `RAUN_SIGNUP_ORGS` naming an organization of that forge stops the server at start |
| Files | No | Read files of a repository at a commit | Epic 5 (#33) | The repository cannot be activated |
| Clone credential | No | A short-lived credential to clone one repository | Epic 6 | The repository cannot be activated, from epic 6 on |
| Webhooks | No | Activate and deactivate the events of a repository: on GitHub, the webhook of the App, one per instance | Epic 5 (#31, #32) | The repository cannot be activated |
| Events | No | Verify and parse a webhook into a normalized event | Epic 5 (#32) | The repository cannot be activated |
| Commit status | No | Publish the status of the check on a commit | Built by epic 5 (#34), used by epic 7 | Nothing is published; the repository page says so |
| Comment | No | Publish or update the comment of a pull request | Built by epic 5 (#34), used by epic 7 | Nothing is published; the repository page says so |

Each core context declares the operations it consumes, since a core context imports only its own package; an adapter implements all of them. No type of a forge library appears in a port.

## The port of epic 4

In `internal/core/identity`:

| Operation | Contract |
| --- | --- |
| `Name()` | The name the server gives the forge, the prefix of the identities of the configuration: `github` |
| `AuthorizeURL(state, challenge)` | Where the person approves Raun, carrying the state and the S256 PKCE challenge |
| `Exchange(ctx, code, verifier)` | The identity and the tokens of the person; `ErrInvalidCode` if the code is unknown, used, expired or does not match the verifier |
| `Refresh(ctx, tokens)` | New tokens, the refresh token included, which the caller keeps: the old refresh token is spent, and a forge that refuses it gives `ErrRevoked`; `ErrRevoked` too if the forge refuses for good, `ErrUnavailable` if it cannot answer now |
| `Repositories(ctx, tokens)` | The repositories the person reaches through Raun, sorted by full name |
| `Permission(ctx, tokens, repo)` | `none`, `view` or `decide`, already mapped, for a repository of this forge (the caller routes by `repo.Forge`); a repository the person cannot reach through Raun is `none`, not an error |
| `IsMember(ctx, tokens, org)` | Optional (`Memberships`, detected with `MembershipsOf`): whether the person belongs to the organization with this stable identifier |

Every call that uses an access token gives `ErrUnauthorized` when the forge refuses it, after which the caller refreshes, and `ErrUnavailable` for a transient failure; `Exchange` gives `ErrUnavailable` too. An adapter wraps its causes in these errors, so a caller decides without knowing the forge, and no error holds a token, a code or a verifier. Tokens are of type `Secret`, which prints as `[redacted]` in every format, log value and text encoding. `NewVerifier` and `Challenge` make the PKCE pair (RFC 7636).

## The fake forge

`internal/forgetest` is test code, never linked into the binary (`make architecture` refuses any package named `golden` or ending in `test` in the dependencies of `cmd/raun`).

- `New` gives a forge in memory with scripted people, repositories (installed or not), permissions and organizations; `Approve` plays a person approving Raun; `Revoke` withdraws a person's tokens; `SetDown` makes every call unavailable; `Advance` moves its clock forward. It runs PKCE, single-use codes of 10 minutes, access tokens of 8 hours, refresh tokens of 6 months that serve once, on the clock it is given.
- `Bare` gives the same forge without its optional capabilities.
- `Scenario` builds the fixture of the contract suite; `Suite` is the contract every adapter passes: an authorization URL that carries the state and the S256 challenge, login, codes and verifiers, an expired code, repositories, permissions, an expired access token refused by every call then refreshed, a refresh that rotates the refresh token, an expired refresh token refused for good, a revocation refused for good by every call, a transient failure of every call that takes nothing away, the refresh token included, a renamed login that keeps its identifier, no token, code or verifier in an error, and memberships when the forge has them. The fake passes it, with and without its optional capabilities.

Each later epic adds its capabilities to the fake and to the suite.

## Webhooks and events

Decided by the [epic 5 review](implementation/05-repositories-and-events.md#webhook-trust), not implemented yet (#32).

- **Endpoint**: one literal route per forge, `POST /hooks/github` for GitHub, never a `{forge}` wildcard, which would conflict with the routes of a workspace; outside the session and CSRF protections, authenticated by its signature alone.
- **Signature first.** A body past 5 MiB is refused with `413`. Within it, `X-Hub-Signature-256`, an HMAC-SHA256 of the body under the webhook secret, is checked in constant time before anything parses it; a refused signature is the same whatever the reason and logs no payload.
- **Duplicates and replays.** The delivery identifier and the SHA-256 of the raw body are both kept, 30 days, for every signed delivery: a delivery that repeats either one has no further effect. They are written with the row of the event log in one transaction, so a failure to record leaves none behind. A replayed body is the same byte for byte; two genuine deliveries differ.
- **Answers.** `202` once recorded; `200` for a duplicate or a replay; `401` for a refused signature; `413` past 5 MiB; `202` for an installation delivery, which makes Raun check the installation with the forge, and for a repository that is not active, dropped; `500` when recording fails. GitHub never delivers a refused delivery again on its own; it can be delivered again by hand.
- **Kept events.** `push` of a branch or a tag; `pull_request` `opened`, `reopened`, `synchronize`, `ready_for_review` and `closed`. `installation` and `installation_repositories` only make Raun check with the forge which repositories the App reaches, and are never logged in the event log of a repository. Any other signed delivery is recorded as ignored.

The normalized events:

| Event | Fields |
| --- | --- |
| Push | Repository (forge, stable identifier), delivery, ref and whether it is a branch or a tag, commits before and after, forced, deleted, head commit, sender |
| Pull request | Repository, delivery, number, action, head and base refs and commits, head and base repositories, draft, sender; from a fork when the head repository is not the base repository |

Each delivery of an active repository is kept in its event log, shown on its page, for 30 days until the epic 13 review decides the retention.

## Statuses and comments

Decided by the [epic 5 review](implementation/05-repositories-and-events.md#statuses-and-comments), built by #34, used by epic 7.

- **Commit status** with the context `raun`, the states pending, success, failure and error, a short description and a link back to Raun. Not a check run: the App asks for Commit statuses, and a new permission would make every installation approve it again.
- **One comment per pull request**, updated in place, found by the hidden marker `<!-- raun -->` on its first line among the comments the App wrote; text from the repository is escaped.

## Types

| Type | Content |
| --- | --- |
| Identity | The forge, the stable identifier the forge gives the person, the login and the display name at the last login |
| Tokens | The access token and its expiry, the refresh token and its expiry; never logged or rendered, stored encrypted |
| Repository | The forge, its stable identifier, its full name (`owner/name`), its default branch, whether it is private |
| Permission | `none`, `view` or `decide` |

## Permission mapping

| GitHub role | Raun right |
| --- | --- |
| `admin`, `maintain`, `write` | decide |
| `triage`, `read` | view |
| none | nothing |

The permission service of `internal/core/identity` is the one place that decides: it asks the forge with the account's own tokens, keeps the answer 5 minutes per account and repository, and drops it at logout, on a refusal of the forge and when the account's tokens are lost. A permission that cannot be established is a refusal, and a kept answer that expires while the forge cannot be reached is too.

## GitHub

One GitHub App per Raun server, on github.com or on GitHub Enterprise Server, through the adapter in `internal/adapter/driven/github`, written on the standard library.

### Create the App

On github.com, in the settings of the organization or the account that owns the App, open **Developer settings**, **GitHub Apps**, **New GitHub App**; on GitHub Enterprise Server, the same page under `<RAUN_GITHUB_URL>/settings/apps/new`.

- **Callback URL**: `<RAUN_PUBLIC_URL>/login/callback`, such as `https://raun.example.com/login/callback`.
- **Expire user authorization tokens**: on. Raun refuses an App whose user tokens do not expire, and says so at the first login.
- **Request user authorization (OAuth) during installation**: off; people log in from Raun.
- **Webhook**: inactive until #32. Then active, with the URL `<RAUN_PUBLIC_URL>/hooks/github` and a secret that `RAUN_GITHUB_WEBHOOK_SECRET` gives Raun too.
- **Repository permissions**: Metadata read, Contents read (epic 5, to read files; epic 6, to clone), Commit statuses read and write and Pull requests read and write (built by epic 5, #34, used by epic 7).
- **Organization permissions**: Members read, for `RAUN_SIGNUP_ORGS` (#26).
- **Events**, subscribed once the webhook exists (#32): `push`, `pull_request`, `installation_repositories`; `installation` comes to every App.
- **Where can this GitHub App be installed**: only on this account, unless people of other organizations log in to the same Raun.

All the permissions are requested from the start: changing the permissions of an App makes every installation approve them again.

Then, on the page of the App:

1. Note the **App ID** and the **Client ID**.
2. **Generate a new client secret** and keep it: GitHub shows it once.
3. **Generate a private key**: GitHub downloads a PEM file.
4. **Install App** on the organizations or accounts whose repositories Raun should reach, on all their repositories or a selection.

The App is installed on the organizations or accounts whose repositories Raun should reach. An installation is not tied to a workspace: a repository is activated in a workspace by someone with write access to it.

### Configure Raun

| Variable | Default | Content |
| --- | --- | --- |
| `RAUN_GITHUB_URL` | `https://github.com` | The address of GitHub: `https://` and a host, no path. The API is `https://api.github.com` for github.com, `<url>/api/v3` otherwise |
| `RAUN_GITHUB_APP_ID` | empty | The App ID; empty means no forge, and nobody can log in |
| `RAUN_GITHUB_CLIENT_ID` | empty | The Client ID |
| `RAUN_GITHUB_CLIENT_SECRET` | empty | Secret: the client secret |
| `RAUN_GITHUB_PRIVATE_KEY` | empty | Secret: the PEM private key, PKCS #1 as GitHub gives it or PKCS #8, RSA of 2048 bits at least |

A secret can be given as a file instead, `RAUN_GITHUB_CLIENT_SECRET_FILE` and `RAUN_GITHUB_PRIVATE_KEY_FILE`, which suits a key of several lines and container secrets; setting both forms of one variable is an error. The file holds the value, one trailing newline aside, 64 KiB at most.

```sh
RAUN_GITHUB_APP_ID=123456
RAUN_GITHUB_CLIENT_ID=Iv23liExample
RAUN_GITHUB_CLIENT_SECRET_FILE=/run/secrets/github-client-secret
RAUN_GITHUB_PRIVATE_KEY_FILE=/run/secrets/github-app.pem
```

The App is configured whole or not at all: with `RAUN_GITHUB_APP_ID`, the client ID, the client secret and the private key are required; without it, none of the other variables may be set, `RAUN_GITHUB_URL` included. A partial set, an invalid key or a malformed value stops the server at start, naming the variable and never showing a secret. The server reaches no GitHub at start: it builds the adapter, and logs `forge` with its name and address.

### How Raun uses the App

- **Login** is the App's user authorization, with PKCE (S256); user tokens last 8 hours and are refreshed, their refresh token lasts 6 months and serves once. GitHub Enterprise Server releases that predate PKCE ignore it; the login state of Raun still binds the login to the browser that started it.
- **Repositories and permissions** are resolved with the person's own token, through the installations of the App the person can reach: GitHub answers with the repositories both the person and the App reach, and with the person's own rights on each, which the table above maps. A repository is found by its stable identifier, so a rename changes nothing. A repository without a commit has no default branch: the listing leaves it out, since it holds nothing to activate, and its permission still resolves. The App's own permissions never stand for a person's.
- **Organization membership** is an active membership, found by the identifier of the organization: a pending invitation does not count. GitHub shows a user token the organizations where the App is installed only, so an organization of `RAUN_SIGNUP_ORGS` lets people in once the App is installed on it.
- **Installation tokens** are minted from the private key when a call needs one, with the repositories and permissions of that call and no more, and kept in memory until five minutes before they expire, then dropped. They serve the capabilities of epics 5 to 7.
- **Rate limits and failures.** A request that consumes nothing (a read, the mint of an installation token) is sent again after a rate limit or a transient failure: it waits what GitHub asks, `Retry-After` or the reset of the quota, or 0.5 then 1 second after a server or network error, 3 attempts and 15 seconds of waiting at most, 10 seconds for one wait. A rate limit that resets later fails the call at once. The exchange of a code and a refresh are sent once, since a replay would spend a single-use value. A call that still fails is a transient failure of the port.
- **Bounds.** Redirects are never followed, a response is read up to 4 MiB, and a listing up to 100 pages of 100 items; a longer listing is an error, never a shorter list.

## Identities in the configuration

`RAUN_ADMINS`, `RAUN_SIGNUP_USERS` and `RAUN_SIGNUP_ORGS` list entries of the form `<forge>:<identifier>`, such as `github:583231`: the stable identifier, never the login, which can be renamed and taken by someone else. On GitHub:

```sh
gh api users/<login> -q .id
gh api orgs/<org> -q .id
```

An entry of a forge the server does not configure stops it at start, and so does a GitHub entry that is not a number, and so does an entry of `RAUN_SIGNUP_ORGS` on a forge that cannot tell the members of an organization. The lists decide who may sign up and log in, checked at every login: someone who leaves a listed organization can no longer log in. GitHub shows a user token the organizations where the App is installed only, so an organization of `RAUN_SIGNUP_ORGS` lets its members in once the App is installed on it.

## Login

`GET /login?return=<path>` starts the authorization of the forge; the forge sends the browser back to `/login/callback`. At the first login, the person chooses the name of their account on `/login/signup`, proposed from their forge login; the name cannot change. `POST /logout` ends the session of the browser, and the other sessions of the account stay. The rules, from the state of a login to the return path, are those of the [epic 4 record](implementation/04-forge-port-and-identity.md#accounts-and-login).
