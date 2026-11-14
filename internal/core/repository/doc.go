// Package repository is the context of the repositories Raun takes part in:
// their stable identity on a forge, their activation in one workspace by a
// person the forge lets decide on them, their deactivation, and whether the
// forge App still reaches them. The rules are those of the epic 5 record,
// docs/implementation/05-repositories-and-events.md.
//
// It imports only the standard library: the forge, the permission of a
// person, the workspaces and the store are ports declared here, which
// cmd/raun fills.
package repository
