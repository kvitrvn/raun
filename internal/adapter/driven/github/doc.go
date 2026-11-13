// Package github implements the forge port of internal/core/identity for a
// GitHub App, on github.com or on GitHub Enterprise Server, with the standard
// library only: no SDK and no JWT library.
//
// The App logs people in through its user authorization, the OAuth web flow
// with PKCE, and gets user tokens that expire and are refreshed. Everything a
// person may see or decide is resolved with that person's own token, through
// the installations of the App they can reach: GitHub answers with the
// intersection of the person's rights and the App's, so the App's own rights
// never stand for the person's.
//
// The App authenticates as itself with an RS256 JWT signed by its private key,
// to mint installation tokens. They are scoped to the repositories and the
// permissions of the call, kept in memory until five minutes before they
// expire, and never stored.
//
// Every call goes through one HTTP helper: no redirect is followed, response
// bodies are bounded, and a failure maps to one of the errors of the port
// without a body, a header, a token, a code or a verifier in its text. Only
// requests that consume nothing are retried, after a rate limit or a
// transient failure, within a bounded wait; the exchange of a code and a
// refresh never are, since a replay would spend a single-use value.
package github
