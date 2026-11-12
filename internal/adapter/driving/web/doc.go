// Package web is the HTTP entry point of the Raun server: its handler, its
// middleware and its health endpoints. cmd/raun builds the http.Server with
// the limits of this package and serves the handler New returns.
//
// Every request goes through, from the outside in: the request identifier and
// the access log, panic recovery, then, except on /healthz and /readyz, the
// Host check against the public address and the body limit, before the
// application routes. The security headers, the cross-origin protection,
// sessions and the CSRF check join that chain with #20, at the places
// docs/implementation/03-server-foundation.md gives them.
//
// What this package does not do:
//   - trust a header from a proxy: X-Forwarded-For, X-Forwarded-Proto,
//     X-Forwarded-Host, Forwarded and an incoming X-Request-Id are never read;
//   - log a header, a cookie, a query, a body or a raw path: the access log
//     records the matched route pattern;
//   - open the database: readiness is a function given by the caller.
package web
