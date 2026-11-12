// Package golden compares the output of a test with a file committed next to
// it, for the tests of packages outside the core.
//
//	golden.Equal(t, "testdata/listing.golden", got)
//
// Equal compares byte for byte and fails the test on a difference, naming the
// first line that differs. Running the tests of one package with -update,
// such as go test ./internal/somepkg -update, rewrites its golden files
// instead; review the diff before committing it. Do not pass -update to
// go test ./...: every test binary that does not import this package refuses
// the flag.
//
// The package owns the -update flag of the test binary. It registers the flag
// unless another package already has, and then accepts that one only if it is
// a boolean that defaults to false; Equal fails the test otherwise. A package
// that defines -update must therefore initialize first: the flag package
// panics at init when it registers the name after this package, the signal
// that two packages claim the flag.
//
// What this package does not do:
//   - normalize anything: line endings, ordering, paths, times and the umask
//     are the caller's to make deterministic. The .gitattributes of the
//     repository marks testdata as -text, so Git never converts line endings
//     on checkout;
//   - write without -update, write outside a testdata directory below the
//     package directory, or follow a symbolic link: a golden path is clean
//     and relative, goes through no link, and names a regular file, in both
//     modes;
//   - leave a partial file: an update writes a temporary file and renames it;
//   - share a file between tests: a golden path belongs to one test, so tests
//     that run in parallel use distinct paths;
//   - serve core tests: a core package builds its inputs in memory, and make
//     architecture refuses an import of this package from the core.
//
// Every failure fails the test; nothing returns an error.
package golden
