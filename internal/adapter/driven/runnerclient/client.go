// Package runnerclient is the client of the runner API that raun runner
// uses (job.Remote): the protocol of docs/runner.md over HTTP, with the
// messages of package runnerapi.
//
// Every request carries the token of the runner, the version of the
// protocol and the release of the runner. Redirects are never followed, so
// the token never reaches another address; an answer is read up to a bound
// and decoded strictly. No error holds the token, a lease token or a body
// the client sent.
package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/runnerapi"
	"github.com/kvitrvn/raun/internal/core/job"
)

// Timeouts of the requests: a claim waits up to job.ClaimWait on the
// server, and its mint 15 seconds more.
const (
	claimTimeout = 60 * time.Second
	callTimeout  = 30 * time.Second
	// maxAnswer bounds the body of an answer.
	maxAnswer = 64 << 10
)

// Options configure a Client.
type Options struct {
	// Server is the public address of the server: https, or http on a
	// loopback address, which the caller checked.
	Server string
	Token  job.Secret
	// Release is the release of the runner, as job.ValidRelease accepts it.
	Release string
	// HTTP sends the requests; nil means a client of its own. Its
	// redirects are refused either way.
	HTTP *http.Client
}

// Client is the runner API as a runner reaches it.
type Client struct {
	base    string
	token   job.Secret
	agent   string
	http    *http.Client
	version string
}

var _ job.Remote = (*Client)(nil)

// New returns the client of opts.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.Server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, errors.New("runnerclient: the address of the server is not an http or https URL")
	}
	if opts.Token.Reveal() == "" {
		return nil, errors.New("runnerclient: no token")
	}
	if !job.ValidRelease(opts.Release) {
		return nil, fmt.Errorf("runnerclient: %q is not a release", opts.Release)
	}
	c := &http.Client{}
	if opts.HTTP != nil {
		copied := *opts.HTTP
		c = &copied
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		base:  strings.TrimSuffix(u.String(), "/") + runnerapi.Prefix + "v" + strconv.Itoa(runnerapi.Version) + "/",
		token: opts.Token, agent: runnerapi.UserAgentPrefix + opts.Release, http: c,
		version: strconv.Itoa(runnerapi.Version),
	}, nil
}

// answer is a response of the server, read.
type answer struct {
	status int
	body   []byte
}

// send posts body to path, with the lease token when it is not empty.
func (c *Client) send(ctx context.Context, timeout time.Duration, path string, lease job.Secret, body any) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return answer{}, fmt.Errorf("runnerclient: %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return answer{}, fmt.Errorf("runnerclient: %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token.Reveal())
	req.Header.Set(runnerapi.HeaderProtocol, c.version)
	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Content-Type", "application/json")
	if lease.Reveal() != "" {
		req.Header.Set(runnerapi.HeaderLease, lease.Reveal())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var u *url.Error
		if errors.As(err, &u) {
			err = u.Err
		}
		return answer{}, fmt.Errorf("runnerclient: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return answer{}, fmt.Errorf("runnerclient: %s: read the answer: %w", path, err)
	}
	if len(b) > maxAnswer {
		return answer{}, fmt.Errorf("runnerclient: %s: an answer larger than %d bytes", path, maxAnswer)
	}
	return answer{status: resp.StatusCode, body: b}, nil
}

// refusal is the error of an answer the protocol names, or a transient
// failure for any other.
func refusal(path string, a answer) error {
	var e runnerapi.Error
	_ = json.Unmarshal(a.body, &e)
	switch a.status {
	case http.StatusUnauthorized:
		return fmt.Errorf("runnerclient: %s: %w", path, job.ErrUnauthenticated)
	case http.StatusBadRequest:
		return fmt.Errorf("runnerclient: %s: %w: %s", path, job.ErrProtocol, bounded(e.Error))
	case http.StatusConflict:
		switch {
		case e.Expected != nil && *e.Expected >= 0:
			return &job.SequenceError{Expected: *e.Expected}
		case e.Error == job.ErrCancelled.Error():
			return fmt.Errorf("runnerclient: %s: %w", path, job.ErrCancelled)
		case e.Error == job.ErrReported.Error():
			return fmt.Errorf("runnerclient: %s: %w", path, job.ErrReported)
		}
		return fmt.Errorf("runnerclient: %s: %w", path, job.ErrStaleLease)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("runnerclient: %s: %w: %s", path, job.ErrInvalidResult, bounded(e.Error))
	}
	return fmt.Errorf("runnerclient: %s: the server answered %d", path, a.status)
}

// bounded is a message of the server, cut to one short line.
func bounded(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Claim implements job.Remote.
func (c *Client) Claim(ctx context.Context) (job.Work, error) {
	const path = "claim"
	a, err := c.send(ctx, claimTimeout, path, "", runnerapi.Empty{})
	if err != nil {
		return job.Work{}, err
	}
	switch a.status {
	case http.StatusNoContent:
		return job.Work{}, job.ErrNoJob
	case http.StatusOK:
	default:
		return job.Work{}, refusal(path, a)
	}
	var m runnerapi.Assignment
	if err := runnerapi.Decode(a.body, &m); err != nil {
		return job.Work{}, fmt.Errorf("runnerclient: %s: %w", path, err)
	}
	return work(m)
}

// work checks the fields of m the runner reads, and returns them.
func work(m runnerapi.Assignment) (job.Work, error) {
	switch {
	case !isID(m.Run):
		return job.Work{}, errors.New("runnerclient: claim: an answer without the identifier of its run")
	case m.Lease.Token == "" || len(m.Lease.Token) > runnerapi.MaxLeaseToken || strings.ContainsAny(m.Lease.Token, " \t\r\n"):
		return job.Work{}, errors.New("runnerclient: claim: an answer without a lease token")
	case m.Lease.Expires.IsZero() || m.Deadline.IsZero():
		return job.Work{}, errors.New("runnerclient: claim: an answer without the end of its lease or its deadline")
	case m.Job.Kind == "" || m.Job.Commit == "" || m.Job.Repository.ID == "":
		return job.Work{}, errors.New("runnerclient: claim: an answer without its job")
	case m.Clone.URL == "" || m.Clone.Password == "":
		return job.Work{}, errors.New("runnerclient: claim: an answer without a clone credential")
	}
	return job.Work{
		Run: m.Run, Lease: job.Secret(m.Lease.Token), LeaseExpires: m.Lease.Expires, Deadline: m.Deadline,
		Kind: job.Kind(m.Job.Kind), Forge: m.Job.Repository.Forge, Repository: m.Job.Repository.ID,
		FullName: m.Job.Repository.FullName, Commit: m.Job.Commit, Ref: m.Job.Ref,
		Clone: job.Credential{URL: m.Clone.URL, FullName: m.Job.Repository.FullName, Username: m.Clone.Username,
			Password: job.Secret(m.Clone.Password), Expires: m.Clone.Expires},
	}, nil
}

func isID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// runPath is the route of a run: runs/<run>/<what>.
func runPath(run, what string) (string, error) {
	if !isID(run) {
		return "", fmt.Errorf("runnerclient: %q is not the identifier of a run", run)
	}
	return "runs/" + run + "/" + what, nil
}

// Heartbeat implements job.Remote.
func (c *Client) Heartbeat(ctx context.Context, run string, lease job.Secret) (job.Renewal, error) {
	path, err := runPath(run, "heartbeat")
	if err != nil {
		return job.Renewal{}, err
	}
	a, err := c.send(ctx, callTimeout, path, lease, runnerapi.Empty{})
	if err != nil {
		return job.Renewal{}, err
	}
	if a.status != http.StatusOK {
		return job.Renewal{}, refusal(path, a)
	}
	var m runnerapi.Renewal
	if err := runnerapi.Decode(a.body, &m); err != nil {
		return job.Renewal{}, fmt.Errorf("runnerclient: %s: %w", path, err)
	}
	if !m.Cancel && m.Expires.IsZero() {
		return job.Renewal{}, fmt.Errorf("runnerclient: %s: an answer without the end of the lease", path)
	}
	return job.Renewal{Expires: m.Expires, Cancel: m.Cancel}, nil
}

// AppendLog implements job.Remote.
func (c *Client) AppendLog(ctx context.Context, run string, lease job.Secret, seq int, text string) error {
	path, err := runPath(run, "log")
	if err != nil {
		return err
	}
	a, err := c.send(ctx, callTimeout, path, lease, runnerapi.Chunk{Seq: &seq, Text: &text})
	if err != nil {
		return err
	}
	if a.status != http.StatusNoContent {
		return refusal(path, a)
	}
	return nil
}

// Report implements job.Remote. The same result is the same bytes, so a
// result sent again after a lost answer is accepted again.
func (c *Client) Report(ctx context.Context, run string, lease job.Secret, r job.Result) error {
	path, err := runPath(run, "result")
	if err != nil {
		return err
	}
	a, err := c.send(ctx, callTimeout, path, lease, runnerapi.Result{Outcome: string(r.Outcome), Summary: r.Summary, Data: r.Data})
	if err != nil {
		return err
	}
	if a.status != http.StatusNoContent {
		return refusal(path, a)
	}
	return nil
}
