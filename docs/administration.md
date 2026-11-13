# Administration

> Status: the administrator role and the read-only Instance settings screen exist ([epic 4](implementation/04-forge-port-and-identity.md#administrators), #21). No setting is stored: the instance is configured by its `RAUN_*` variables only.

## Administrators

The administrators of an instance are forge accounts named in `RAUN_ADMINS`: a comma-separated list of `<forge>:<stable identifier>` entries, such as `github:583231`. The identifier is the number the forge gives the person, never the login, which can be renamed and then taken by someone else; [docs/forge.md](forge.md#identities-in-the-configuration) shows how to find it.

- **Empty by default.** An empty list is valid: the instance runs, and nobody reaches the administration screens.
- **No bootstrap.** There is no first-administrator step, no token and no default credential: the administrator is whoever the person who deploys the instance names.
- **Checked at each request, never stored.** An account is an administrator while one of its forge identities is listed. The list is read at start, so a change takes a restart; the sessions already open then follow the new list at their next request, without logging in again.
- **What the role grants.** Seeing the Instance settings screen. Nothing on repository data, nothing in a workspace: viewing and deciding on a repository come from the forge, through the permission service, for administrators as for anyone.
- **Logging in.** An administrator logs in like anyone, and `RAUN_ADMINS` admits to sign-up and login on its own: the account needs no entry in `RAUN_SIGNUP_USERS`.

## Instance settings

`GET /instance`, linked from the top bar for administrators, shows the effective configuration of the instance: every `RAUN_*` variable of the server, in the order of the [README](../README.md#server), with the value the server runs with, its default when the variable is unset.

- **Read-only.** Changing a value means changing the variable and restarting the server.
- **Secrets redacted.** Every secret shows as `[redacted]`, whether it is set or not, given directly or by its `_FILE` form, so the screen does not even tell whether a secret is set.
- **For administrators only.** Anyone else logged in gets 403; a visitor is sent to the login first.
- **No stored setting.** None exists until a feature needs a value an administrator changes while the instance runs; that feature adds it, stored with its author and time ([epic 3 record](implementation/03-server-foundation.md#instance-settings)).
