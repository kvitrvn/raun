// Package gittest builds Git repositories for the tests of packages outside
// the core, such as the Git adapter.
//
// A fixture is a real repository in a temporary directory, built with the git
// binary. Its commits have a fixed author, committer and date, and git runs
// with an environment built from nothing, the user and system configuration
// ignored, so two runs of the same fixture give the same commit identifiers on
// any machine.
//
// What this package does not do:
//   - read or touch any repository other than the fixture;
//   - clone, fetch or reach the network: a submodule is recorded as a bare
//     reference to a commit, never populated;
//   - serve core tests: a core package builds its inputs in memory, and make
//     architecture refuses an import of this package from the core.
//
// Every failure fails the test at once; nothing returns an error.
package gittest
