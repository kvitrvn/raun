// Package git implements the commit reader of the knowledge core
// (knowledge.CommitReader) on the git binary.
//
// A Reader is bound to one repository, the top of a working tree or a bare
// repository, and reads it at a commit through plumbing commands only:
// rev-parse to resolve a revision, ls-tree to list a tree, and one cat-file
// --batch to read many files. Reading the files of one commit starts three
// processes whatever their number.
//
// Git runs with an environment built from nothing and the user and system
// configuration ignored. Options before every subcommand disable hooks, the
// file system monitor, the pager, lazy fetching of missing objects and
// replacement objects, and an empty GIT_ALLOW_PROTOCOL denies every transport
// over any protocol.<name>.allow, so neither the caller's environment nor the
// configuration of the analyzed repository can make Git run a command, reach
// the network or show other content than the commit's. A partial clone is
// refused by Open: Git before 2.44 would fetch its missing objects whatever
// the environment says.
//
// A path never reaches Git: ReadFiles checks each one with
// knowledge.ValidatePath, then looks it up in the tree listing and hands Git
// the object name only. A file whose name starts with a dash is listed by
// ListTree but can never be read. A revision that starts with a dash is
// refused, and the others are passed after --end-of-options.
//
// ReadFiles holds one file at a time. ListTree returns the whole tree, sorted,
// so it holds every entry at once: about a hundred bytes each, some hundred
// megabytes at the default bound.
//
// What this package does not do:
//   - write to the repository: no checkout, no index, no lock, no reference;
//   - fetch, clone or reach the network;
//   - follow a link or enter a submodule: both are reported by kind only;
//   - relax safe.directory: a repository owned by another user is refused, as
//     Git refuses it;
//   - read a file above the size bound (8 MiB by default), reported as
//     TooLarge, or list a tree above the entry bound (one million by
//     default), refused with knowledge.ErrTreeTooLarge.
//
// It needs Git 2.32 or later, checked by Open.
package git
