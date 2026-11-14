package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// MaxHookBytes bounds the body of a webhook delivery (epic 5 record): GitHub
// caps one at 25 MB, and this holds ordinary pushes and pull requests in
// memory.
const MaxHookBytes = 5 << 20

// HookReceiver verifies, parses and records a delivery of the forge.
type HookReceiver interface {
	Receive(ctx context.Context, header func(string) string, body []byte) (repository.Outcome, error)
}

// forgeName is the rule of the name of a forge in the path of its hook: a
// literal segment, never a wildcard (epic 5 record).
var forgeName = regexp.MustCompile(`^[a-z]{1,32}$`)

// hookRoute registers the hook of the forge on the root mux, before the
// application: like the assets, it passes the Host check, the access log
// and the security headers, and none of the protections of the
// application, the body bound included. It authenticates by its signature
// alone, and reads its body up to MaxHookBytes.
func (s *Server) hookRoute(root *http.ServeMux) {
	s.route(root, "POST /hooks/"+s.forgeName, s.checkHost(http.HandlerFunc(s.hook)))
}

// hook answers a delivery: 413 for a body too large, before any signature;
// 401 for a refused signature, the same whatever the reason; 400 for a
// signed delivery that is not one of the forge; 200 for a duplicate or a
// replay; 202 once recorded; 500 when recording fails. Nothing of the
// payload is logged.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(connWriter(w), r.Body, MaxHookBytes))
	switch {
	case IsBodyTooLarge(err):
		s.warn(r, "webhook refused", "body larger than the bound")
		writeText(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	case err != nil:
		s.warn(r, "webhook refused", "the body could not be read")
		writeText(w, http.StatusBadRequest, "unreadable body")
		return
	}
	outcome, err := s.hooks.Receive(r.Context(), r.Header.Get, body)
	switch {
	case errors.Is(err, repository.ErrSignature):
		s.warn(r, "webhook refused", "signature refused")
		writeText(w, http.StatusUnauthorized, "signature refused")
		return
	case errors.Is(err, repository.ErrMalformed):
		// Its detail may quote the payload: it is not logged.
		s.warn(r, "webhook refused", "malformed delivery")
		writeText(w, http.StatusBadRequest, "malformed delivery")
		return
	case err != nil:
		s.fail(w, r, "record a webhook delivery", err)
		return
	}
	s.logger.LogAttrs(r.Context(), slog.LevelInfo, "webhook received",
		slog.String("request_id", RequestID(r.Context())),
		slog.String("outcome", string(outcome)))
	if outcome == repository.OutcomeDuplicate {
		writeText(w, http.StatusOK, "already received")
		return
	}
	writeText(w, http.StatusAccepted, "accepted")
}
