// Package identity holds who a person is to Raun and what the forge lets
// them do: forge identities and their tokens, and the port of the forge that
// logs a person in, lists the repositories they reach and gives their
// permission on one. Accounts, the namespace, workspaces and the permission
// service join it in epic 4 (#26 to #28); see
// docs/implementation/04-forge-port-and-identity.md.
//
// The forge port is a small required interface, Forge, and optional
// capabilities an adapter implements when its forge offers them, detected by
// type assertion: Memberships so far. A missing capability is a normal state
// the caller handles, never an error. The other capabilities of docs/forge.md
// (files, clone credentials, webhooks, events, statuses, comments) are
// declared by the contexts that consume them, in epics 5 to 7.
//
// What this package does not do:
//   - name a forge or import a forge library: adapters translate;
//   - map forge roles: an adapter gives the permission already mapped to view
//     or decide, by the table of docs/forge.md;
//   - print a token: Secret renders as [redacted] in every format.
package identity
