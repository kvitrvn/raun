package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/runnerapi"
	"github.com/kvitrvn/raun/internal/core/job"
)

// RunnerDispatcher hands jobs to runners and runs the operations of a run,
// each for the runner that holds it: job.Dispatcher.
type RunnerDispatcher interface {
	Claim(ctx context.Context, runner string, stop <-chan struct{}) (job.Assignment, error)
	Heartbeat(ctx context.Context, runner, run, token string) (time.Time, error)
	AppendLog(ctx context.Context, runner, run, token string, seq int, text string) error
	Kind(ctx context.Context, runner, run string) (job.Kind, error)
	Report(ctx context.Context, runner, run, token string, res job.Result) error
}

// noParams are the parameters of a kind that reads none.
var noParams = json.RawMessage(`{}`)

// runnerAPIRoutes registers the routes of protocol 1 on mux, the routes of
// a run bound to its lease.
func (s *Server) runnerAPIRoutes(mux *http.ServeMux) {
	v1 := runnerapi.Prefix + "v1/"
	s.route(mux, v1+"claim", post(http.HandlerFunc(s.runnerClaim)))
	s.route(mux, v1+"runs/{run}/heartbeat", post(http.HandlerFunc(s.runnerHeartbeat)))
	s.route(mux, v1+"runs/{run}/log", post(http.HandlerFunc(s.runnerLog)))
	s.route(mux, v1+"runs/{run}/result", post(http.HandlerFunc(s.runnerResult)))
	s.route(mux, v1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "no such route of the runner API")
	}))
}

// post lets POST through and answers any other method 405, in JSON as every
// answer of the runner API.
func post(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAPIError(w, http.StatusMethodNotAllowed, "the runner API takes POST only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runnerProtocol answers every path below /api/runner/ that does not speak a
// protocol of the server, in the path or in the header, with 400 naming the
// versions the server speaks, and a User-Agent that is not one of a runner
// with 400; it passes the others to next.
func (s *Server) runnerProtocol(next http.Handler) http.Handler {
	spoken := "this server speaks " + runnerapi.SpokenText()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version, _, ok := runnerapi.ParsePath(r.URL.Path)
		if !ok {
			s.warn(r, "runner refused", "no protocol in the path")
			writeAPIError(w, http.StatusBadRequest, "the path names no protocol; "+spoken)
			return
		}
		if !runnerapi.Speaks(version) {
			s.warn(r, "runner refused", fmt.Sprintf("protocol %d in the path", version))
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("protocol %d is not spoken here; %s", version, spoken))
			return
		}
		values := r.Header.Values(runnerapi.HeaderProtocol)
		if len(values) != 1 {
			s.warn(r, "runner refused", "no protocol header")
			writeAPIError(w, http.StatusBadRequest, "one "+runnerapi.HeaderProtocol+" header is required; "+spoken)
			return
		}
		header, ok := runnerapi.ParseVersion(values[0])
		switch {
		case !ok:
			s.warn(r, "runner refused", "malformed protocol header")
			writeAPIError(w, http.StatusBadRequest, runnerapi.HeaderProtocol+" is not a version; "+spoken)
			return
		case !runnerapi.Speaks(header):
			s.warn(r, "runner refused", fmt.Sprintf("protocol %d in the header", header))
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("protocol %d is not spoken here; %s", header, spoken))
			return
		case header != version:
			s.warn(r, "runner refused", "the path and the header name different protocols")
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("the path speaks protocol %d and %s %d", version, runnerapi.HeaderProtocol, header))
			return
		}
		if !job.ValidRelease(runnerapi.Release(r.UserAgent())) {
			s.warn(r, "runner refused", "not the User-Agent of a runner")
			writeAPIError(w, http.StatusBadRequest, "the User-Agent is not "+runnerapi.UserAgentPrefix+"<release>")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeAPIError answers status with the error body of the protocol, which
// never holds a token.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeAPI(w, status, runnerapi.Error{Error: message})
}

// writeAPI answers status with v in JSON.
func writeAPI(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// apiFail logs err and answers 500, without err, as fail does for pages.
func (s *Server) apiFail(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.LogAttrs(r.Context(), slog.LevelError, what,
		slog.String("request_id", RequestID(r.Context())),
		slog.String("error", err.Error()))
	writeAPIError(w, http.StatusInternalServerError, "internal error, request "+RequestID(r.Context()))
}

// readAPIBody reads the body of r up to limit bytes and decodes it into v,
// strictly; it answers 413 past the bound and 400 for a malformed body, and
// reports whether it went on.
func (s *Server) readAPIBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(connWriter(w), r.Body, limit))
	switch {
	case IsBodyTooLarge(err):
		s.warn(r, "runner request refused", "body larger than the bound")
		writeAPIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d bytes", limit))
		return false
	case err != nil:
		s.warn(r, "runner request refused", "the body could not be read")
		writeAPIError(w, http.StatusBadRequest, "unreadable body")
		return false
	}
	if err := runnerapi.Decode(body, v); err != nil {
		s.warn(r, "runner request refused", "malformed body")
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// lease returns the lease token of the one Raun-Lease header of r, or
// answers 400.
func lease(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values(runnerapi.HeaderLease)
	if len(values) != 1 || values[0] == "" || len(values[0]) > runnerapi.MaxLeaseToken || strings.ContainsAny(values[0], " \t") {
		writeAPIError(w, http.StatusBadRequest, "one "+runnerapi.HeaderLease+" header with the lease token of the run is required")
		return "", false
	}
	return values[0], true
}

// runError answers an error of an operation of a run: the answer the
// protocol gives it, or 500.
func (s *Server) runError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var seq *job.SequenceError
	switch {
	case errors.As(err, &seq):
		expected := seq.Expected
		writeAPI(w, http.StatusConflict, runnerapi.Error{Error: fmt.Sprintf("chunk %d is expected", expected), Expected: &expected})
	case errors.Is(err, job.ErrStaleLease):
		s.warn(r, what+" refused", "the run is no longer current")
		writeAPIError(w, http.StatusConflict, job.ErrStaleLease.Error())
	case errors.Is(err, job.ErrCancelled):
		writeAPIError(w, http.StatusConflict, job.ErrCancelled.Error())
	case errors.Is(err, job.ErrReported):
		writeAPIError(w, http.StatusConflict, job.ErrReported.Error())
	case errors.Is(err, job.ErrInvalidLog):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, job.ErrInvalidResult):
		s.warn(r, what+" refused", "a result its kind refuses; the job failed")
		writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, job.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", `Bearer realm="raun"`)
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
	default:
		s.apiFail(w, r, what, err)
	}
}

func (s *Server) runnerClaim(w http.ResponseWriter, r *http.Request) {
	runner, _ := runnerFrom(r.Context())
	if !s.readAPIBody(w, r, runnerapi.MaxClaimBody, &runnerapi.Empty{}) {
		return
	}
	a, err := s.dispatcher.Claim(r.Context(), runner.ID, s.stop)
	switch {
	case errors.Is(err, job.ErrNoJob):
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		s.runError(w, r, "claim", err)
		return
	}
	s.logger.LogAttrs(r.Context(), slog.LevelInfo, "job claimed",
		slog.String("request_id", RequestID(r.Context())), slog.String("runner", runner.ID),
		slog.String("job", a.Job.ID), slog.String("run", a.Run.ID), slog.String("kind", string(a.Job.Kind)))
	writeAPI(w, http.StatusOK, runnerapi.Assignment{
		Run:      a.Run.ID,
		Lease:    runnerapi.Lease{Token: a.Token, Expires: a.Run.LeaseExpires.UTC()},
		Deadline: a.Run.Deadline.UTC(),
		Job: runnerapi.Job{
			Kind:       string(a.Job.Kind),
			Repository: runnerapi.Repository{Forge: a.Job.Forge, ID: a.Job.Repository, FullName: a.Clone.FullName},
			Commit:     a.Job.Commit, Ref: a.Job.Ref, Params: noParams,
		},
		Clone: runnerapi.Clone{URL: a.Clone.URL, Username: a.Clone.Username, Password: a.Clone.Password.Reveal(), Expires: a.Clone.Expires.UTC()},
	})
}

func (s *Server) runnerHeartbeat(w http.ResponseWriter, r *http.Request) {
	runner, _ := runnerFrom(r.Context())
	token, ok := lease(w, r)
	if !ok || !s.readAPIBody(w, r, runnerapi.MaxHeartbeatBody, &runnerapi.Empty{}) {
		return
	}
	expires, err := s.dispatcher.Heartbeat(r.Context(), runner.ID, r.PathValue("run"), token)
	switch {
	case errors.Is(err, job.ErrCancelled):
		writeAPI(w, http.StatusOK, runnerapi.Renewal{Cancel: true})
	case err != nil:
		s.runError(w, r, "heartbeat", err)
	default:
		writeAPI(w, http.StatusOK, runnerapi.Renewal{Expires: expires.UTC()})
	}
}

func (s *Server) runnerLog(w http.ResponseWriter, r *http.Request) {
	runner, _ := runnerFrom(r.Context())
	token, ok := lease(w, r)
	var c runnerapi.Chunk
	if !ok || !s.readAPIBody(w, r, runnerapi.MaxLogBody, &c) {
		return
	}
	switch {
	case c.Seq == nil || c.Text == nil:
		writeAPIError(w, http.StatusBadRequest, "a chunk needs seq and text")
		return
	case len(*c.Text) > job.MaxLogChunk:
		s.warn(r, "log refused", "text larger than the bound")
		writeAPIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the text of a chunk is larger than %d bytes", job.MaxLogChunk))
		return
	}
	if err := s.dispatcher.AppendLog(r.Context(), runner.ID, r.PathValue("run"), token, *c.Seq, *c.Text); err != nil {
		s.runError(w, r, "log", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runnerResult(w http.ResponseWriter, r *http.Request) {
	runner, _ := runnerFrom(r.Context())
	token, ok := lease(w, r)
	if !ok {
		return
	}
	run := r.PathValue("run")
	// The kind bounds the body, so it is read first.
	kind, err := s.dispatcher.Kind(r.Context(), runner.ID, run)
	if err != nil {
		s.runError(w, r, "result", err)
		return
	}
	spec, err := job.SpecOf(kind)
	if err != nil {
		s.apiFail(w, r, "result", err)
		return
	}
	var res runnerapi.Result
	if !s.readAPIBody(w, r, int64(spec.MaxResult+runnerapi.ResultEnvelope), &res) {
		return
	}
	err = s.dispatcher.Report(r.Context(), runner.ID, run, token, job.Result{
		Outcome: job.Outcome(res.Outcome), Summary: res.Summary, Data: res.Data,
	})
	if err != nil {
		s.runError(w, r, "result", err)
		return
	}
	s.logger.LogAttrs(r.Context(), slog.LevelInfo, "run reported",
		slog.String("request_id", RequestID(r.Context())), slog.String("runner", runner.ID),
		slog.String("run", run), slog.String("outcome", res.Outcome))
	w.WriteHeader(http.StatusNoContent)
}
