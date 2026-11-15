package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driving/web/ui"
	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// RunsBoard reads the jobs and runs of a repository, and cancels a job by
// hand: job.Board. It answers job.ErrNotFound for a job or a run of another
// repository; the handlers check the permission of the forge first.
type RunsBoard interface {
	Jobs(ctx context.Context, forge, repository string) ([]job.JobView, error)
	Deliveries(ctx context.Context, forge, repository string) (int, map[string]int, error)
	Job(ctx context.Context, forge, repository, id string) (job.JobView, error)
	Log(ctx context.Context, forge, repository, jobID, run string, offset int, shownCut bool) (job.LogPart, error)
	Cancel(ctx context.Context, forge, repository, id string) (job.Job, error)
}

// timeLayout is how the pages write a time.
const timeLayout = "2006-01-02 15:04:05 UTC"

// logID is the identifier of the log block of the page of a job.
const logID = "log"

// runRoutes registers the Runs list of a repository, the page of a job and
// its log, and the cancellation by hand, all below the page of the
// repository.
func (s *Server) runRoutes() {
	s.route(s.app, "GET /{name}/{owner}/{repo}/runs", http.HandlerFunc(s.runsPage))
	s.route(s.app, "GET /{name}/{owner}/{repo}/runs/{job}", http.HandlerFunc(s.jobPage))
	s.route(s.app, "GET /{name}/{owner}/{repo}/runs/{job}/log", http.HandlerFunc(s.jobLog))
	s.route(s.app, "POST /{name}/{owner}/{repo}/runs/{job}/cancel", http.HandlerFunc(s.jobCancel))
}

// withRepository opens the repository of the path for the account, as its
// page does, and calls next with it: a page that does not exist for anyone
// the forge does not let read it, a login for a visitor.
func (s *Server) withRepository(next func(w http.ResponseWriter, r *http.Request, v repository.View)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workspace, fullName, ok := repositoryTarget(r)
		if !ok || !isJobID(r.PathValue("job")) && r.PathValue("job") != "" {
			s.notFound(w, r)
			return
		}
		s.requireAccount(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, err := s.repositories.Open(r.Context(), account(r), workspace, fullName)
			switch {
			case errors.Is(err, repository.ErrNotFound):
				s.notFound(w, r)
			case s.forgeFailed(w, r, err):
			case err != nil:
				s.fail(w, r, "open a repository", err)
			default:
				next(w, r, v)
			}
		})).ServeHTTP(w, r)
	})
}

// isJobID reports whether s is the identifier of a job or a run: 32
// lowercase hexadecimal digits.
func isJobID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// runsPath is the Runs list of a repository.
func runsPath(rp repository.Repository) string {
	return repositoryPath(rp.Workspace.Name, rp.FullName) + "/runs"
}

func (s *Server) runsPage(w http.ResponseWriter, r *http.Request) {
	s.withRepository(func(w http.ResponseWriter, r *http.Request, v repository.View) {
		rp := v.Repository
		views, err := s.runs.Jobs(r.Context(), rp.Forge, rp.ID)
		if err != nil {
			s.fail(w, r, "list the jobs", err)
			return
		}
		now := s.now()
		props := ui.RunsProps{FullName: rp.FullName, RepoHref: repositoryPath(rp.Workspace.Name, rp.FullName), Limit: job.MaxListed}
		for _, jv := range views {
			j := jv.Job
			props.Rows = append(props.Rows, ui.JobRow{
				Href: runsPath(rp) + "/" + j.ID, Created: j.CreatedAt.UTC().Format(timeLayout), Kind: string(j.Kind),
				Trigger: trigger(j), Commit: j.Commit, State: string(j.State), Reason: reasonText(j.Reason),
				Runs: len(jv.Runs), Duration: jobDuration(j, now), Wait: waitText(j, jv.Wait),
			})
		}
		s.render(w, r, http.StatusOK, ui.RunsPage(s.page(r, "Runs · "+rp.FullName, ""), props))
	}).ServeHTTP(w, r)
}

func (s *Server) jobPage(w http.ResponseWriter, r *http.Request) {
	s.withRepository(func(w http.ResponseWriter, r *http.Request, v repository.View) {
		rp := v.Repository
		jv, err := s.runs.Job(r.Context(), rp.Forge, rp.ID, r.PathValue("job"))
		if errors.Is(err, job.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.fail(w, r, "read a job", err)
			return
		}
		j := jv.Job
		base := runsPath(rp) + "/" + j.ID
		// The run shown is the one asked for, the latest otherwise.
		selected := r.URL.Query().Get("run")
		if selected == "" && len(jv.Runs) > 0 {
			selected = jv.Runs[len(jv.Runs)-1].ID
		}
		now := s.now()
		props := ui.JobProps{
			FullName: rp.FullName, RepoHref: repositoryPath(rp.Workspace.Name, rp.FullName), RunsHref: runsPath(rp),
			Kind: string(j.Kind), Trigger: trigger(j), Commit: j.Commit, State: string(j.State), Reason: reasonText(j.Reason),
			Wait: waitText(j, jv.Wait), Created: j.CreatedAt.UTC().Format(timeLayout), Duration: jobDuration(j, now),
			CanCancel: v.Permission == repository.PermissionDecide && !j.State.Final(), CancelURL: base + "/cancel",
		}
		found := false
		for _, run := range jv.Runs {
			row := ui.RunRow{
				Href: base + "?run=" + run.ID, Selected: run.ID == selected, Runner: shortID(run.Runner),
				Started: run.StartedAt.UTC().Format(timeLayout), Outcome: string(run.Outcome), Duration: runDuration(run, now),
			}
			if run.Result != nil {
				row.Summary = run.Result.Summary
			}
			found = found || row.Selected
			props.Runs = append(props.Runs, row)
		}
		if selected != "" && !found {
			s.notFound(w, r)
			return
		}
		if selected != "" {
			part, err := s.runs.Log(r.Context(), rp.Forge, rp.ID, j.ID, selected, 0, false)
			if err != nil {
				s.fail(w, r, "read a log", err)
				return
			}
			props.HasLog = true
			props.Log = ui.LogView{ID: logID, Text: part.Text, PollURL: pollURL(base, selected, part)}
		}
		s.render(w, r, http.StatusOK, ui.JobPage(s.page(r, string(j.Kind)+" · "+rp.FullName, ""), props))
	}).ServeHTTP(w, r)
}

// pollURL is the address the next part of a log is polled from, empty once
// its run has ended.
func pollURL(base, run string, part job.LogPart) string {
	if part.Ended {
		return ""
	}
	cut := "0"
	if part.Cut {
		cut = "1"
	}
	q := url.Values{"run": {run}, "offset": {strconv.Itoa(part.Next)}, "cut": {cut}}
	return base + "/log?" + q.Encode()
}

// jobLog answers the poll of a log: the text past the offset shown, and the
// next poll while the run lasts; the whole block, out of band, when the log
// was cut since.
func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	s.withRepository(func(w http.ResponseWriter, r *http.Request, v repository.View) {
		rp := v.Repository
		q := r.URL.Query()
		run := q.Get("run")
		offset, err := strconv.Atoi(q.Get("offset"))
		cut := q.Get("cut")
		if !isJobID(run) || err != nil || offset < 0 || offset > job.MaxLog || (cut != "0" && cut != "1") {
			writeText(w, http.StatusBadRequest, "not a part of a log")
			return
		}
		jobID := r.PathValue("job")
		part, err := s.runs.Log(r.Context(), rp.Forge, rp.ID, jobID, run, offset, cut == "1")
		switch {
		case errors.Is(err, job.ErrNotFound):
			s.notFound(w, r)
			return
		case errors.Is(err, job.ErrBadOffset):
			writeText(w, http.StatusBadRequest, "not a part of a log")
			return
		case err != nil:
			s.fail(w, r, "read a log", err)
			return
		}
		poll := pollURL(runsPath(rp)+"/"+jobID, run, part)
		if part.Reset {
			s.render(w, r, http.StatusOK, ui.LogBlock(ui.LogView{ID: logID, Text: part.Text, PollURL: poll}, true))
			return
		}
		s.render(w, r, http.StatusOK, ui.LogTail(part.Text, poll))
	}).ServeHTTP(w, r)
}

// jobCancel cancels a job by hand, for a person the forge lets decide on
// the repository.
func (s *Server) jobCancel(w http.ResponseWriter, r *http.Request) {
	s.withRepository(func(w http.ResponseWriter, r *http.Request, v repository.View) {
		if _, ok := formValues(w, r); !ok {
			return
		}
		rp := v.Repository
		jobID := r.PathValue("job")
		back := runsPath(rp) + "/" + jobID
		if v.Permission != repository.PermissionDecide {
			s.warn(r, "cancellation refused", "the forge does not let the account decide")
			s.render(w, r, http.StatusForbidden, ui.ErrorPage(s.page(r, "Forbidden", ""), ui.ErrorProps{
				Status: http.StatusForbidden, Title: "Only a person who decides cancels",
				Message:  "A person with write access to the repository on the forge cancels its jobs.",
				BackHref: back, BackLabel: "Back to the job",
			}))
			return
		}
		j, err := s.runs.Cancel(r.Context(), rp.Forge, rp.ID, jobID)
		switch {
		case errors.Is(err, job.ErrNotFound):
			s.notFound(w, r)
			return
		case errors.Is(err, job.ErrFinal):
			// Ended meanwhile: the page says how.
		case err != nil:
			s.fail(w, r, "cancel a job", err)
			return
		default:
			s.logger.LogAttrs(r.Context(), slog.LevelInfo, "job cancelled",
				slog.String("request_id", RequestID(r.Context())), slog.String("account", account(r)),
				slog.String("job", j.ID), slog.String("repository", rp.ID))
		}
		redirect(w, back)
	}).ServeHTTP(w, r)
}

// trigger is what caused a job: the ref of its event, or a hand.
func trigger(j job.Job) string {
	switch {
	case j.Ref != "":
		return j.Ref
	case j.Delivery == "":
		return "by hand"
	}
	return "delivery " + shortID(j.Delivery)
}

// shortID is the start of an identifier, as a table shows it.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// jobDuration is how long a job has lasted: from its creation to its end,
// or to now while it is not over.
func jobDuration(j job.Job, now time.Time) string {
	end := j.FinishedAt
	if end.IsZero() {
		end = now
	}
	return duration(end.Sub(j.CreatedAt))
}

// runDuration is how long a run has lasted.
func runDuration(r job.Run, now time.Time) string {
	end := r.EndedAt
	if end.IsZero() {
		end = now
	}
	return duration(end.Sub(r.StartedAt))
}

func duration(d time.Duration) string {
	return max(d, 0).Round(time.Second).String()
}

// reasonText says why a job ended as it did, when its runner did not.
func reasonText(r job.Reason) string {
	switch r {
	case job.ReasonSuperseded:
		return "Superseded by a newer job."
	case job.ReasonCancelled:
		return "Cancelled by hand."
	case job.ReasonDeactivated:
		return "Cancelled: the repository was deactivated."
	case job.ReasonExpired:
		return "No runner took it within a day."
	case job.ReasonTimeout:
		return "Its run passed the maximum duration of its kind."
	case job.ReasonLost:
		return "Its runs were lost three times."
	case job.ReasonInvalidResult:
		return "Its runner reported a result its kind refuses."
	}
	return ""
}

// waitText says why a queued job is not taken yet.
func waitText(j job.Job, w job.Wait) string {
	switch w {
	case job.WaitDelayed:
		return "Waits until " + j.NotBefore.UTC().Format(timeLayout) + ": the forge could not give the credential of its run."
	case job.WaitNoRunner:
		capability := ""
		if spec, err := job.SpecOf(j.Kind); err == nil {
			capability = " " + string(spec.Capability)
		}
		return "No runner of its workspace has the capability" + capability + ": an administrator creates one on the Runners screen."
	case job.WaitNotSeen:
		return "No runner that may take it was seen in the last two minutes."
	}
	return ""
}
