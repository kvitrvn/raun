// Package sqlite implements the knowledge store (knowledge.Store) on one
// SQLite database file, through the pure-Go driver modernc.org/sqlite.
//
// An item is one row of knowledge_items: the columns that queries filter or
// sort on, and a JSON document for the rest. Its history is knowledge_history,
// one row per event, which triggers keep append-only. Every row carries the
// workspace and the repository, and every query filters on both: an operation
// never sees another scope.
//
// The file is opened with two pools: one connection for every write, whose
// transactions begin IMMEDIATE so the write lock is taken before anything is
// read, and a pool of read connections, which cannot write. Every connection
// runs in WAL mode, with foreign keys on, synchronous=NORMAL and a busy
// timeout of 5 seconds.
//
// Migrations are numbered SQL files embedded in the binary. Migrate applies
// them, each in its own transaction, recorded in schema_migrations; the server
// calls it at start, only when the administrator enables it (epic 3).
//
// What this package does not do:
//   - migrate on Open or on the first use of a connection;
//   - decide what an item may become: the knowledge core checks every item
//     and every update before it is written (CheckCreate, CheckUpdate), and
//     this package checks only what needs the database, the persona
//     references;
//   - delete an item or an event, or offer a write without a revision check;
//   - open an in-memory database or a URI: each connection of an in-memory
//     database would be a database of its own.
package sqlite
