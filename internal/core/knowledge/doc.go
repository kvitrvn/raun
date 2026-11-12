// Package knowledge is the knowledge core of Raun: the model of a knowledge
// item, its validation, its identifiers and its status transitions, the
// classification of repository paths into sources, the port through which it
// reads a repository at a commit, and the verification of evidence against it.
//
// The model is specified in docs/format.md, and the choices behind it in
// docs/implementation/02-knowledge-core.md. This package implements both and
// nothing else: it performs no I/O, reads no clock and holds no state.
//
// CommitReader is an interface only: its implementation on the git binary is
// in internal/adapter/driven/git, and this package starts no process and
// opens no file. ValidatePath is the rule every path from outside Raun passes
// before anything reads it. A Classifier gives the source of a path from the
// sets it is built with: exact context paths, then exclusion and
// documentation patterns, whose only syntax beyond path.Match is a whole **
// segment. It works on paths alone and never asks whether a file exists.
//
// A Verifier checks evidence without any LLM: Integrity at the commit a
// citation was made at, Freshness at a later commit. It reads files through a
// CommitReader only, one call per commit, takes the time of the check from the
// caller, and never changes an excerpt, its hash or, at freshness, the stored
// commit and lines.
//
// Every operation (New, AppendFromRun, Decide, Reopen, ResolveOpenPoint,
// MarkNeedsReview) takes the time and the actor as arguments, works on a copy
// of the item, validates the result and returns it. On error the input is left
// as it was. No operation changes the revision, which the store increments
// (#14), and none rewrites an existing support entry, evidence or history
// event.
//
// What this package does not do:
//   - decide which commit to check, or record the result: the callers do,
//     on a push or a pull request (epic 7) and after an analysis (epic 10);
//   - read the repository configuration: the source sets are given by the
//     caller, and DefaultDocs stands in until epic 8;
//   - store items or check that the personas a requirement names exist (#14);
//   - authenticate the author of a decision: By is an opaque identifier until
//     accounts exist (epic 4).
//
// It imports the standard library only; make architecture checks it.
package knowledge
