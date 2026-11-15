# Administration

> Status: the administrator role and the read-only Instance settings screen exist ([epic 4](implementation/04-forge-port-and-identity.md#administrators), #21), and the Runners screen ([epic 6](implementation/06-jobs-and-runner.md#runner-trust), #38). No setting is stored: the instance is configured by its `RAUN_*` variables only.

## Administrators

The administrators of an instance are forge accounts named in `RAUN_ADMINS`: a comma-separated list of `<forge>:<stable identifier>` entries, such as `github:583231`. The identifier is the number the forge gives the person, never the login, which can be renamed and then taken by someone else; [docs/forge.md](forge.md#identities-in-the-configuration) shows how to find it.

- **Empty by default.** An empty list is valid: the instance runs, and nobody reaches the administration screens.
- **No bootstrap.** There is no first-administrator step, no token and no default credential: the administrator is whoever the person who deploys the instance names.
- **Checked at each request, never stored.** An account is an administrator while one of its forge identities is listed. The list is read at start, so a change takes a restart; the sessions already open then follow the new list at their next request, without logging in again.
- **What the role grants.** Seeing the Instance settings screen, and creating, rotating and revoking runners. Nothing on repository data, nothing in a workspace: viewing and deciding on a repository come from the forge, through the permission service, for administrators as for anyone.
- **Logging in.** An administrator logs in like anyone, and `RAUN_ADMINS` admits to sign-up and login on its own: the account needs no entry in `RAUN_SIGNUP_USERS`.

## Instance settings

`GET /instance`, linked from the top bar for administrators, shows the effective configuration of the instance: every `RAUN_*` variable of the server, in the order of the [README](../README.md#server), with the value the server runs with, its default when the variable is unset.

- **Read-only.** Changing a value means changing the variable and restarting the server.
- **Secrets redacted.** Every secret shows as `[redacted]`, whether it is set or not, given directly or by its `_FILE` form, so the screen does not even tell whether a secret is set.
- **For administrators only.** Anyone else logged in gets 403; a visitor is sent to the login first.
- **No stored setting.** None exists until a feature needs a value an administrator changes while the instance runs; that feature adds it, stored with its author and time ([epic 3 record](implementation/03-server-foundation.md#instance-settings)).

## Runners

`GET /instance/runners`, linked from the Instance settings screen, lists the runners of the instance to its administrators, revoked ones included, with their scope, capabilities and last contact, and creates them. Anyone else logged in gets 403 on every route of the screen.

- **Creating a runner** takes a name, unique on the instance and kept by a revoked runner, a scope (the instance, whose jobs of every workspace it takes, or one workspace) and capabilities, among the kinds of job that exist: `noop` today. The answer shows the token of the runner once, to give it as `RAUN_RUNNER_TOKEN` or in the file `RAUN_RUNNER_TOKEN_FILE` names ([docs/runner.md](runner.md)); Raun keeps only its SHA-256, and no other page shows it.
- **A new token** replaces the old one, which is refused at once. A runner still using it stops at its next request, and the jobs it runs go back to the queue when their leases expire: give a runner a new token while it is stopped.
- **Revoking a runner** refuses it at once and sends the jobs it runs back to the queue, within the three runs of a job. A revoked runner cannot be given a token again.
- **The last contact** is written once a minute at most: the screen says a runner was seen recently when it was in the last two minutes.
- Each change is logged with the runner, the administrator and the scope, never with the token.
