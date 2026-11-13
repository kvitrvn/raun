// Package identity holds who a person is to Raun and what the forge lets
// them do: forge identities and their tokens, and the port of the forge that
// logs a person in, lists the repositories they reach and gives their
// permission on one; accounts and the rule of their names, the sign-up rule,
// the login with its states and sign-ups, and the token service that
// refreshes tokens once and revokes them for good; and the port of the store
// that keeps them, with its contract suite in identitytest; workspaces,
// personal and shared, their owners and members, in the one namespace of
// accounts and workspaces; and the permission service, the one place that
// decides what an account may do on a repository, from the forge, with a
// short cache. See docs/implementation/04-forge-port-and-identity.md.
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
//   - print a token: Secret renders as [redacted] in every format;
//   - encrypt anything: the store seals the tokens it keeps;
//   - read a request or a cookie: the web adapter does, with these rules.
package identity
