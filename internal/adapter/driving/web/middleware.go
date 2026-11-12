package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
	"unicode/utf8"
)

// requestIDKey and routeKey are the context keys of the request identifier
// and of the slot the matched route pattern is written to.
type (
	requestIDKey struct{}
	routeKey     struct{}
)

// maxLoggedHost bounds a Host header copied to the log: it comes from the
// client.
const maxLoggedHost = 256

// RequestID returns the identifier of the request ctx belongs to, or "" if
// it has none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID returns 16 random bytes in hexadecimal.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusWriter records the status and the size of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// logRequests gives each request a new identifier, in its context and in the
// X-Request-Id response header, and writes one access record when it ends.
// The record holds the matched route pattern, never the path or the query,
// and no header, cookie or body.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		var route string
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = context.WithValue(ctx, routeKey{}, &route)
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			s.logger.LogAttrs(ctx, slog.LevelInfo, "request",
				slog.String("request_id", id),
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", status),
				slog.Int64("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", r.RemoteAddr))
		}()
		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}

// recoverPanics turns a panic of a handler into a 500 that carries the
// request identifier, and logs the stack. Once the response has started, it
// can no longer change: the connection is aborted instead, so the client
// never takes a truncated body for a complete one. The panic value is logged only
// when it is a runtime error, whose text the program wrote; any other value
// could hold request data, so only its type is logged.
// http.ErrAbortHandler is passed on, as net/http expects.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, _ := w.(*statusWriter)
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			attrs := []slog.Attr{
				slog.String("request_id", RequestID(r.Context())),
				slog.String("type", fmt.Sprintf("%T", v)),
				slog.String("stack", string(debug.Stack())),
			}
			var rerr runtime.Error
			if err, ok := v.(error); ok && errors.As(err, &rerr) {
				attrs = append(attrs, slog.String("error", rerr.Error()))
			}
			s.logger.LogAttrs(r.Context(), slog.LevelError, "panic", attrs...)
			if sw != nil && sw.status != 0 {
				// net/http aborts the connection, or the HTTP/2 stream,
				// on this value and logs nothing more.
				panic(http.ErrAbortHandler)
			}
			writeText(w, http.StatusInternalServerError, "internal error, request "+RequestID(r.Context()))
		}()
		next.ServeHTTP(w, r)
	})
}

// checkHost refuses with 421 a request whose Host is not the host of the
// public address, before any handler runs. No proxy header is read: the
// public address comes from the configuration only.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := CanonicalHost(r.Host, s.scheme)
		if err != nil || got != s.publicHost {
			s.logger.LogAttrs(r.Context(), slog.LevelWarn, "host refused",
				slog.String("request_id", RequestID(r.Context())),
				slog.String("host", truncate(r.Host, maxLoggedHost)),
				slog.String("expected", s.publicHost))
			writeText(w, http.StatusMisdirectedRequest, "misdirected request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody bounds the body of every request to MaxBodyBytes. A read past
// the bound fails with *http.MaxBytesError, which the decoder of the route
// answers with 413 through IsBodyTooLarge.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// IsBodyTooLarge reports whether err comes from reading past the bound of a
// request body. net/http writes no status for it: the route answers 413.
func IsBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

// truncate bounds s to n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
