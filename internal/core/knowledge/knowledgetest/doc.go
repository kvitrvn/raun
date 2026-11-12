// Package knowledgetest holds what every implementation of knowledge.Store is
// tested with: Suite, the contract of the port, and Memory, a store held in
// memory that passes it.
//
// Suite is the one place where the rules of the port are tested: identifiers,
// revisions, the scope of every operation, references to personas, the
// append-only history, copies and concurrent writers. A store implementation
// runs it on fresh stores and adds only the tests of its own mechanics.
//
// Memory is for tests and for the suite itself. It keeps nothing beyond the
// process, and one mutex serializes every operation.
//
// What this package does not do:
//   - import anything but the standard library and the knowledge core, so the
//     architecture rule holds and every backend runs the same suite;
//   - test a storage format, a migration or a database: the adapters do.
package knowledgetest
