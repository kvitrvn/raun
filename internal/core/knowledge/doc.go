// Package knowledge is the knowledge core of Raun: the model of a knowledge
// item, its validation, its identifiers and its status transitions.
//
// The model is specified in docs/format.md, and the choices behind it in
// docs/implementation/02-knowledge-core.md. This package implements both and
// nothing else: it performs no I/O, reads no clock and holds no state.
//
// Every operation (New, AppendFromRun, Decide, Reopen, ResolveOpenPoint,
// MarkNeedsReview) takes the time and the actor as arguments, works on a copy
// of the item, validates the result and returns it. On error the input is left
// as it was. No operation changes the revision, which the store increments
// (#14), and none rewrites an existing support entry, evidence or history
// event.
//
// What this package does not do:
//   - verify a citation against the code (evidence verification, #13);
//   - store items or check that the personas a requirement names exist (#14);
//   - authenticate the author of a decision: By is an opaque identifier until
//     accounts exist (epic 4).
//
// It imports the standard library only; make architecture checks it.
package knowledge
