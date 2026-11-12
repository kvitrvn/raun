# Forges

> Status: decided by the [epic 4 review](implementation/04-forge-port-and-identity.md) (#23), not implemented yet. #24 adds the port and the fake forge, #25 the GitHub adapter; each turns its part of this page into the description of what exists.

Raun reaches a forge through one port: a small set of operations every adapter implements, and optional capabilities an adapter implements when its forge offers them. A caller detects a capability by type assertion and handles its absence; a missing capability is never an error.

## Capabilities

| Capability | Required | Operations | Consumer | When absent |
| --- | --- | --- | --- | --- |
| Login | Yes | Authorization URL with state and PKCE; exchange of a code for the identity and the tokens of a person; refresh of a token | Epic 4 (#26) | — |
| Repositories | Yes | The repositories a person can reach through Raun | Epic 4, epic 5 (#31) | — |
| Permission | Yes | The permission of a person on a repository | Epic 4 (#28) | — |
| Membership | No | Whether a person belongs to an organization | Epic 4 (#26), for `RAUN_SIGNUP_ORGS` | An entry of `RAUN_SIGNUP_ORGS` naming an organization of that forge stops the server at start |
| Files | No | Read files of a repository at a commit | Epic 5 (#33) | The repository cannot be activated |
| Clone credential | No | A short-lived credential to clone one repository | Epic 6 | The repository cannot be activated |
| Webhooks | No | Activate and deactivate the events of a repository | Epic 5 (#31) | The repository cannot be activated |
| Events | No | Verify and parse a webhook into a normalized event | Epic 5 (#32) | The repository cannot be activated |
| Commit status | No | Publish the status of the check on a commit | Epic 7 | Nothing is published; the repository page says so |
| Comment | No | Publish or update the comment of a pull request | Epic 7 | Nothing is published; the repository page says so |

Each core context declares the operations it consumes, since a core context imports only its own package; an adapter implements all of them. No type of a forge library appears in a port.

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

A permission is cached 5 minutes per account and repository. A permission that cannot be established is a refusal.

## GitHub

One GitHub App per Raun server, on github.com or on GitHub Enterprise Server.

- **Address**: `RAUN_GITHUB_URL`, `https://github.com` by default; the API is `https://api.github.com`, or `<url>/api/v3` on Enterprise Server.
- **Configuration**: `RAUN_GITHUB_APP_ID`, `RAUN_GITHUB_CLIENT_ID`, and the secrets `RAUN_GITHUB_CLIENT_SECRET` and `RAUN_GITHUB_PRIVATE_KEY`, each also as `_FILE`.
- **Permissions**: repository Metadata read, Contents read, Commit statuses read and write, Pull requests read and write; organization Members read.
- **Events**: `push`, `pull_request`, `installation`, `installation_repositories`.
- **Login**: the App's user authorization, with PKCE; user tokens expire after 8 hours and are refreshed.
- **Installation tokens**: minted from the private key when a call needs one, scoped to its repositories and permissions, kept in memory only.
- **Callback URL** of the App: `<RAUN_PUBLIC_URL>/login/callback`.

The App is installed on the organizations or accounts whose repositories Raun should reach. An installation is not tied to a workspace: a repository is activated in a workspace by someone with write access to it.

## Identities in the configuration

`RAUN_ADMINS`, `RAUN_SIGNUP_USERS` and `RAUN_SIGNUP_ORGS` list entries of the form `<forge>:<identifier>`, such as `github:583231`: the stable identifier, never the login, which can be renamed and taken by someone else. On GitHub:

```sh
gh api users/<login> -q .id
gh api orgs/<org> -q .id
```
