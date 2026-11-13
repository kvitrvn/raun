# Epic 4: forge port and identity

Epic: #22. Review: #23. Decided by the owner on 2026-11-12.

## Scope

Connect Raun to a forge behind one port and make the forge the only source of identity and permissions, with GitHub as the first adapter: accounts created by logging in with the forge, workspaces in one namespace shared with users, the permission of a user on a repository resolved from the forge and cached briefly, and the administrators and stored secrets that epic 3 handed over (#21).

Sources consulted: `kvitrvn/raun-old` at `330e634` (`docs/upgrade.md`), `internal/forge/forge.go` in `Bornholm/conclave` v0.23.0 (a small core interface and optional capabilities detected by type assertion) and Woodpecker's `forge.Forge`, the GitHub App documentation, issues #22 and #24 to #28 and #21, the [threat model](../threat-model.md) (T10, T12, T15, T17), and the [epic 3 record](03-server-foundation.md) (sessions, bypass patterns, administrators, secrets at rest).

The port, its capabilities and the GitHub App are described in [docs/forge.md](../forge.md). This page records why.

## Choices

### Port and capabilities

The port is a small required core and optional capabilities, as in Conclave: an adapter implements what its forge offers, and a caller detects a capability by type assertion.

- **Required of every adapter**: logging a person in (the authorization URL, the exchange of the code for the person's identity and tokens, the refresh of a token), the repositories a person can reach through Raun, and the permission of a person on one repository. Raun is useless without them, and every forge in the plan (GitHub, Gitea and Forgejo, GitLab) has them.
- **Optional**: membership of an organization (for the sign-up rule), reading a file at a commit, a clone credential, activating and deactivating webhooks, parsing a webhook into a normalized event, publishing a commit status, publishing a pull request comment. A missing capability is a normal state with a defined consequence, never an error: without a clone credential or webhooks, a repository cannot be activated, and the activation screen says why; without a commit status or a comment, nothing is published and the repository page says so; without organization membership, organizations of that forge cannot be listed in `RAUN_SIGNUP_ORGS`, and an entry naming one stops the server at start.
- **Where it lives.** A core context imports only the standard library and itself, so each consuming context declares the operations it needs: `internal/core/identity` (epic 4) declares login, listing, permission and membership; the context of epic 5 declares files, webhooks and events; epic 7 statuses and comments. One adapter per forge implements all of them. `docs/forge.md` lists the whole set and which epic consumes each.
- **No SDK.** No type of a forge library appears in a port, and the GitHub adapter is written on `net/http` and `crypto` from the standard library: the App JWT is RS256, which the standard library signs. No dependency is added.
- **The fake forge** is `internal/forgetest`, outside the core like `internal/gittest`: scripted people, organizations, repositories, permissions and failures, and every capability, with a variant that lacks the optional ones. It holds the contract suite every adapter passes; the GitHub adapter passes it on recorded HTTP responses.

### GitHub

- **A GitHub App**, not an OAuth App: short-lived installation tokens, webhooks managed by the App, fine-grained permissions, and no user token needed to clone. The same App logs people in through its user authorization (OAuth web flow with PKCE), with user tokens that expire after 8 hours and a refresh token.
- **Permissions requested.** Repository: Metadata read, Contents read, Commit statuses read and write, Pull requests read and write. Organization: Members read, for the sign-up rule. Events: `push`, `pull_request`, `installation`, `installation_repositories`. The permissions of epics 5 and 7 are requested from the start, since changing the permissions of an App makes every installation approve them again; the documentation says which epic uses each.
- **Installations and workspaces are not linked.** An installation only makes repositories reachable. A repository is activated in a workspace by a person who has write access to it on the forge (epic 5), and Raun finds the installation that covers it. One organization can feed several workspaces, and a workspace can hold repositories of several installations.
- **GitHub Enterprise Server** is supported from the start: `RAUN_GITHUB_URL`, `https://github.com` by default, gives the web address, from which the API address follows (`https://api.github.com` for github.com, `<url>/api/v3` otherwise). One GitHub per Raun server.
- **Configuration**, added to the catalogue by #25: `RAUN_GITHUB_URL`, `RAUN_GITHUB_APP_ID`, `RAUN_GITHUB_CLIENT_ID`, and the secrets `RAUN_GITHUB_CLIENT_SECRET` and `RAUN_GITHUB_PRIVATE_KEY`, each with its `_FILE` form. The webhook secret is added by epic 5 (#32). Without `RAUN_GITHUB_APP_ID` the server has no forge and nobody can log in; a partial set stops it at start.

### Tokens

- **User tokens are stored**, encrypted with the scheme of the [epic 3 record](03-server-foundation.md#secrets-at-rest) (AES-256-GCM, versioned envelope, `RAUN_SECRET_KEY` and `RAUN_SECRET_KEY_PREVIOUS`): the access token, its expiry, the refresh token and its expiry, one set per forge identity. They are needed to list repositories and to resolve permissions for that person.
- **Installation tokens are never stored.** They are minted from the App's private key when needed, kept in memory until five minutes before they expire (they live one hour), and scoped to the repositories and permissions of the call.
- **Refresh.** An access token is refreshed when it is within five minutes of its expiry or when the forge refuses it. A refresh the forge refuses for good, because the refresh token expired or the person revoked the App, deletes the tokens of that identity and ends every session of the account: Raun can no longer check that person's permissions, so the person logs in again. A transient failure (the network, a server error, a rate limit) keeps the tokens; the request is refused for lack of an established permission, as when the forge is down, and the next request tries again.
- **Nothing reaches a runner** but the scoped clone credential of epic 6.

### Accounts and login

- **An account** has a name in the shared namespace and one or more forge identities. An identity is a forge and the stable identifier the forge gives the person (a number on GitHub), with the login kept as a snapshot updated at each login; an identity belongs to one account only.
- **Login** is the authorization flow of the forge, driven by top-level `GET` requests: the one exception to the epic 3 rule that safe methods change nothing, recorded there. `GET /login` from a session that already has an account goes straight to the return path. Otherwise it uses the session of the browser, or starts one, draws a state and a PKCE verifier, and redirects to the forge; a third-party page that sends a browser there gains nothing, since only the forge's answer to that state completes the login, in that browser. The state is 32 random bytes, stored hashed with the verifier, the session it belongs to and the return path; it is single-use and expires after 10 minutes. `GET /login/callback` accepts only a state issued for the session that comes back with it, consumes it, exchanges the code, then renews the session (new identifier and token) and attaches the account. The return path is a path of the instance: it starts with `/`, its second character is neither `/` nor `\`, it holds no backslash and no control character, and parsed as a URL it has neither scheme nor host; anything else returns to `/`.
- **Sign-up** is the first login of an identity. It is allowed when the identity is listed in `RAUN_SIGNUP_USERS` or `RAUN_ADMINS`, or is a member of an organization listed in `RAUN_SIGNUP_ORGS`. All three are empty by default, so a fresh instance lets only its administrators in, and nothing opens sign-up to everyone silently. The rule is checked at every login, not only the first: someone who leaves the organization can no longer log in. A refusal says which rule applies, without listing the allowed organizations.
- **The username** is chosen on the sign-up screen, proposed from the forge login, lowercased, and checked against the namespace rules below; a name that is taken or reserved is refused and another is asked for.
- **Logout** ends the session (epic 3). The account's other sessions stay, except when its tokens are lost (above).
- **Entries** of `RAUN_ADMINS`, `RAUN_SIGNUP_USERS` and `RAUN_SIGNUP_ORGS` are a forge and a stable identifier: `github:583231`. A login can be renamed and then taken by someone else; an identifier cannot. The documentation shows how to find it (`gh api users/<login> -q .id`, `gh api orgs/<org> -q .id`). An entry of another form, or of a forge the server does not know, stops the server at start.

### Namespace

- **One namespace** holds the names of accounts and of shared workspaces, used in URLs (`/<name>/...`). The default workspace of an account is its personal workspace and carries the account's name: it is that name's workspace, not a second entry.
- **A name** is 1 to 39 characters among lowercase letters, digits and hyphens, neither starting nor ending with a hyphen and without two hyphens in a row, the rule GitHub applies to logins, so that most logins can be kept. Comparison is exact, since names are lowercase.
- **Reserved names** are the first path segment of every route of the application (`login`, `logout`, `assets`, `healthz`, `readyz`, `instance`, `workspaces`, `new`, ...) and a fixed list (`admin`, `api`, `raun`, `settings`, `help`, `about`, the names of the forges). A test fails when a route is added without reserving its first segment.
- **No renaming** in epic 4: names are permanent, so URLs stay stable and no redirection or reservation is needed. Renaming is deferred.

### Workspaces

- **A workspace** groups repositories (epic 5), and later the agents and secrets of those repositories. It never grants access to a repository: viewing and deciding come from the forge.
- **Personal and shared.** Every account has its personal workspace, created at sign-up, which only that account belongs to. Any account can create a shared workspace and becomes its owner.
- **Two roles** in a shared workspace: an owner manages its members and its settings; a member sees it in the switcher and can activate in it a repository on which the forge gives them write access. A workspace keeps at least one owner. Members are added by an owner from existing accounts.
- **Instance administrators** get no role in a workspace and no access to repository data by being administrators (T10).

### Permissions

- **Mapping.** A forge role gives one of two rights: view or decide. On GitHub, `admin`, `maintain` and `write` decide; `triage` and `read` view; no access gives nothing. Each adapter documents its mapping.
- **One service decides.** Every screen and action on repository data asks the permission service of `internal/core/identity`; nothing else decides access.
- **Cache.** A permission is cached for 5 minutes per account and repository, in memory. It is dropped at logout, when the forge refuses a call for that repository, and when the account's tokens are lost.
- **Failure.** When the forge cannot be reached, a cached permission still valid is used; once it expires, access is refused. A permission that cannot be established is a refusal, never a grant (T10).

### Administrators

`RAUN_ADMINS` lists forge identities in the form above. An account is an administrator while one of its identities is listed, checked at each request and never stored (epic 3 record). The Instance settings screen of #21 is for administrators only.

### Several forges

The model already holds them: an account has identities, each of one forge, and the forge prefix of every entry names the forge. In epic 12, a person logged in links another forge by going through that forge's authorization from their account page; the new identity joins the account unless it already belongs to another one, which is refused. A person can then log in with any of their identities. The forge name of an entry is the name the server gives each configured forge (`github`, then `gitea` or `forgejo`, `gitlab`).

### Packages

| Package | Content |
| --- | --- |
| `internal/core/identity` | Accounts, identities, the namespace rules, workspaces and roles, the sign-up and administrator rules, the permission mapping and the permission service with its cache, and the ports they consume: the forge (login, listing, permission, membership) and the stores. |
| `internal/forgetest` | The fake forge and the contract suite of the forge adapters. |
| `internal/adapter/driven/github` | The GitHub App adapter. |
| `internal/adapter/driven/sqlite` | Accounts, identities, names, workspaces, members, login states and encrypted tokens, in new numbered migrations; the `account` column of `sessions`. |
| `internal/adapter/driving/web` | Login, callback, sign-up, logout, the workspace screens and switcher, the account in the top bar. |

### Port (#24)

Rules made explicit while implementing the port (#24):

- **Four errors.** `ErrUnauthorized` (the access token is refused: refresh and try again), `ErrRevoked` (refused for good: log in again), `ErrUnavailable` (transient), `ErrInvalidCode` (the code of a login) and none else: an adapter wraps its causes in them, so the rules of the tokens and of the permission cache are written once, against the port.
- **No access is not an error.** A repository the person cannot reach through Raun, because the forge gives no access or the App is not installed on it, has the permission `none`; the forge's own refusal of the token stays `ErrUnauthorized`.
- **Repositories** come sorted by full name, so screens and tests are deterministic whatever the forge returns.
- **Tokens are secrets.** `identity.Secret` prints as `[redacted]` in every `fmt` verb, `slog` value and text encoding, without an error; only `Reveal` gives the value. The store of #26 therefore encrypts `Reveal()`, never a JSON or text encoding of `Tokens`, which would persist `[redacted]`; a test that stores and reads back tokens catches it.
- **Refresh rotates the refresh token.** `Refresh` gives a new refresh token, which works; the caller keeps it and treats the old one as spent. GitHub and GitLab refuse a used refresh token, Gitea only when `INVALIDATE_REFRESH_TOKENS` is set, so the suite requires the new token and `ErrRevoked` when the forge refuses the old one, not the refusal itself. Since a refused refresh ends the sessions of the account (above), the code that refreshes never refreshes the same tokens twice: two requests of one identity that both find the token expired refresh it once.
- **No secret in an error.** No error of an adapter holds a token, a code or a verifier; the suite checks the error of every call it expects to fail.
- **PKCE in the core.** `NewVerifier` draws 32 bytes from the reader it is given, `Challenge` is S256; the adapter only carries them.
- **The fake has what exists.** It implements the port of epic 4 and `Memberships`; the capabilities of epics 5 to 7 are declared by the contexts that consume them, and each such epic adds them to the fake and to the suite. `make architecture` refuses a test helper linked into `cmd/raun`: a package of the module with a path element named `golden` or ending in `test`, which covers `internal/forgetest`, `internal/gittest`, `internal/golden` and the `*test` packages of the core.

### GitHub adapter (#25)

Rules made explicit while implementing the adapter (#25), in `internal/adapter/driven/github`:

- **The person's own view.** `Repositories` and `Permission` use the person's token on `GET /user/installations` and `GET /user/installations/{id}/repositories`, which give the repositories both the person and the App reach, with the person's own rights (`admin`, `maintain`, `push`, `triage`, `pull`) mapped by the table of `docs/forge.md`. `Permission` finds the repository by its stable identifier and stops at it; a repository that is not there, or of another forge, is `none`. A repository without a commit comes with no default branch: `Repositories` leaves it out, since the contract gives every listed repository its branch and there is nothing to activate, and `Permission` still resolves it. An installation token never decides what a person may do: it would give the App's rights, and it needs the login, which changes.
- **Membership** is `GET /user/memberships/orgs?state=active`, matched on the identifier of the organization, active only. A user token of an App reaches only what both the person and the App reach, so an organization of `RAUN_SIGNUP_ORGS` works once the App is installed on it; #26 checks it on a real organization and documents it with the sign-up rule.
- **PKCE on GitHub Enterprise Server.** The authorization always carries the S256 challenge and the exchange the verifier. A release that predates PKCE ignores both; the login state of #26 still binds the login to its browser. No fallback without PKCE exists.
- **Expiring user tokens are required.** An App that opts out of them gives a token without a refresh token, which `Exchange` refuses with a message that says which setting to change.
- **Retries.** Only a request that consumes nothing is sent again: a read, the mint of an installation token. It waits what GitHub asks (`Retry-After`, or the reset of a spent quota), or 0.5 then 1 second after a server or network error; 3 attempts, 10 seconds for one wait and 15 seconds for all of them, after which the call fails with `ErrUnavailable`, at once when the limit resets later. The exchange of a code and a refresh are sent once: a replay would spend the code or the refresh token, and with it the sessions of the account.
- **Decoding of GitHub responses** is bounded and checked, not strict: a response is read up to 4 MiB, a malformed, truncated or doubled document is an error, and so is a listing without its array, the fields Raun uses are required, and identifiers are read as integers, never through a float. Unknown fields are ignored, an exception to the strict decoding of AGENTS.md: GitHub adds fields to its responses without a new API version, and refusing them would break every adapter call on the day it does. The configuration and every input Raun defines stay strict.
- **Bounds.** No redirect is followed, so no header or form leaves for another address. A listing walks pages of 100 by number, up to 100 pages, and asks for one more after 100 full ones, under the rules of any page (a refused token, a removed installation, a malformed document): empty, the listing is whole; else it is longer, which is an error, never a shorter list. An installation removed during the walk is skipped. `RAUN_GITHUB_URL` on github.com takes no port but 443.
- **Errors** name the method and the endpoint path, never the query, a header, the body of GitHub, a token, a code or a verifier; OAuth error codes are kept only when they are codes (letters and underscores).
- **Installation tokens** are a primitive of the adapter with no caller before epic 5: a mint names its repositories and permissions every time, the cache is keyed on the installation and the sorted scope, concurrent callers of one scope share one mint, a failed mint is never cached, a token past its margin is dropped, a late refusal of an old token never drops its replacement, an installation GitHub refuses (removed, suspended) drops its tokens, and a token refused before its expiry is minted again once, never with a wider scope.
- **The App at start.** `cmd/raun` builds the adapter after the configuration, without reaching GitHub, and logs `forge`; it reaches no screen until #26. A secret variable takes a `_FILE` form, read up to 64 KiB without its trailing newline, and the values of the App are `identity.Secret`. The App is set whole or not at all: `RAUN_GITHUB_URL` alone is a partial set.
- **The contract suite** runs on the adapter against a scripted GitHub over TLS, laid out as a GitHub Enterprise Server, sharing its clock with the adapter.

### Accounts and login (#26)

Rules made explicit while implementing the login (#26):

- **A sign-up waits for its name.** The callback takes the state, exchanges the code and applies the sign-up rule; a new identity gets a pending sign-up of its session, with its tokens sealed, for 10 minutes, and the person chooses the name on `GET /login/signup`. `POST /login/signup`, guarded by the CSRF token like any state change, creates the account, its identity and its tokens and removes the sign-up in one transaction, then renews the session logged in to the account. The callback stays single-use, and the page can be reloaded.
- **Reserved names from now on.** Names are permanent, so the sign-up refuses now the first segments of the routes (`assets`, `healthz`, `instance`, `login`, `logout`, `new`, `readyz`, `workspaces`), the kept names (`about`, `admin`, `api`, `help`, `raun`, `settings`) and the names of the forges. #27 extends the list with its routes and adds the test that keeps it in step with them.
- **A refusal for the person.** A refused sign-up or login says which rule refused (not listed, not a member) without naming the allowed organizations, with status 403. A membership the forge could not check refuses the login too, answered as a failure of the forge to try again (503), never as an acceptance.
- **Entries are checked against their forge.** On GitHub an entry is a number; a login in its place (`github:alice`) would never match and the instance would refuse everyone, so it stops the server at start, naming the variable and how to find the identifier.
- **The account of a session** is a column of `sessions`, an identifier opaque to the session context; a renewal keeps it, and a login sets it in the same replacement. The identity context ends the sessions of an account by deleting the rows of that account, in the transaction that deletes its tokens.
- **`RAUN_SECRET_KEY` is required with a forge**, which gives tokens to store; without a forge it is optional, but a database that holds sealed secrets stops a start without it. The key is 32 bytes in standard base64. `RAUN_SECRET_KEY_PREVIOUS` needs `RAUN_SECRET_KEY` and differs from it.
- **The sealed columns** are `tokens.sealed` and `signups.sealed`. A sealed value is a version byte, the identifier of the key, a nonce of 12 bytes and the AES-256-GCM ciphertext, with the table, the column and the key of its row as additional data, so a value moved to another row does not open. Its plaintext is written from `Reveal()` of each token, never from an encoding of `Tokens`. A plaintext that would not open again (64 KiB sealed at most) is refused when written, so nothing unreadable is stored. At start, the purge of expired login states and sign-ups runs first, then the rotation goes through both columns in one transaction, before anything reads a secret.
- **The PKCE verifier stays in clear** in `login_states`: it serves once, within 10 minutes, and only with a code the forge gives that browser; the state is stored hashed. A take of the state removes it whatever its answer, a callback of another session included, which cancels that login: the person starts again.
- **A login starts a session.** A browser without a cookie gets one at `GET /login`, as the login state is bound to it; the session lives by the rules of epic 3 and is purged once expired. Limiting how often a login starts is not done here.
- **Refreshes are serialized in memory.** The token service holds one lock per identity while a caller holds or waits for it, and reads the tokens again under it, so two requests that find a token expired refresh it once. One server process serves a database: SQLite on a local file already requires it. The service has no caller before the permission service of #28, which also shares that lock with the tokens a login stores, so a refresh in flight never overwrites or revokes the tokens of a login that just completed.
- **`RAUN_ADMINS` admits to sign-up.** It is read here for the sign-up rule only; the administrator role and its screen stay with #21.
- **`GET /login` is the one GET that changes state**, as the epic 3 record allows; a `HEAD` of `/login` or `/login/callback` changes nothing.

## Deferred

| Topic | Where |
| --- | --- |
| Renaming an account or a workspace | Not planned; a later epic if needed |
| Deleting a workspace or an account | Epic 13, with retention |
| Linking a second forge identity | Epic 12 |
| Files, webhooks, events, clone credentials | Epics 5 and 6 |
| Commit statuses and pull request comments | Epic 7 |
| The webhook secret variable | Epic 5 (#32) |

## Sub-issues

| Issue | Change |
| --- | --- |
| #24 | The port in `internal/core/identity`: login, listing, permission, membership; the fake forge and the contract suite in `internal/forgetest`; `docs/forge.md`. |
| #25 | GitHub App: JWT, installation tokens in memory, user authorization with PKCE and refresh, listing through the installations of the user, permission and membership, GHES through `RAUN_GITHUB_URL`, the configuration variables and, as they are the first secret variables, the `_FILE` form and the secret type of the configuration; no SDK. |
| #26 | Login with state and PKCE, sign-up rule (`RAUN_SIGNUP_USERS`, `RAUN_SIGNUP_ORGS`), username choice, logout, the account of a session; it stores the first secrets, so it adds their encryption (moved from #21). |
| #27 | Namespace rules and reserved names, personal and shared workspaces, owner and member, switcher; no renaming. |
| #28 | Mapping, permission service, 5-minute cache, invalidation, refusal when unknown. |
| #21 | `RAUN_ADMINS` in the stable form, the Instance settings screen; the encryption of stored secrets moves to #26. |

## Exit criteria

- [x] This page records the choices.
- [ ] A user logs in through the fake forge in a test and gets an account and a default workspace (#26, #27): the account is done by #26 (`cmd/raun/login_test.go`: `TestLoginThroughTheServer`), the workspace comes with #27.
- [ ] A test proves that a permission removed on the forge is refused after the cache expires (#28).
- [ ] `make verify` passes (checked at the end of the epic).
