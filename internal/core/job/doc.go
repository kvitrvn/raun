// Package job is the context of the work Raun hands to runners: jobs, the
// runs that attempt them, and the queue that leases a job to one runner at
// a time. The rules are those of the epic 6 record,
// docs/implementation/06-jobs-and-runner.md.
//
// A store applies the rules of a queue operation atomically; the rules that
// need no store, such as the kinds, the lease tokens and the checks of a
// result, are here, and every store passes the contract suite of package
// jobtest.
//
// It imports only the standard library: the stores are ports declared here,
// which cmd/raun fills.
package job
