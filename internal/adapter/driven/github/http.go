package github

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

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Bounds of the HTTP helper.
const (
	// maxBody bounds the body of one response.
	maxBody = 4 << 20
	// maxAttempts bounds the attempts of a request that may be retried.
	maxAttempts = 3
	// maxWait bounds one wait before a retry: a rate limit that resets
	// later fails the call instead.
	maxWait = 10 * time.Second
	// waitBudget bounds the waits of one call.
	waitBudget = 15 * time.Second
	// backoff is the first wait after a transient failure, doubled after
	// each one.
	backoff = 500 * time.Millisecond
	// secondaryWait is the wait GitHub asks for after a secondary rate
	// limit without a Retry-After header: more than the budget, so the call
	// fails.
	secondaryWait = time.Minute
	// perPage and maxPages bound a listing: 100 items a page, the most
	// GitHub gives, and 100 pages.
	perPage  = 100
	maxPages = 100
)

// apiVersion is the version of the REST API the adapter is written against.
const apiVersion = "2022-11-28"

// userAgent identifies Raun to GitHub, which requires the header.
const userAgent = "raun"

// request is one HTTP request of the adapter.
type request struct {
	method string
	// url is the whole address; path is the endpoint without its query,
	// the only part of the address errors name.
	url  string
	path string
	// auth is the value of the Authorization header; it never reaches an
	// error.
	auth string
	// form or body is the payload: a form for the OAuth endpoints, JSON for
	// the API.
	form url.Values
	body any
	// oauth requests JSON from the OAuth endpoints instead of the API media
	// type.
	oauth bool
	// retry marks a request that consumes nothing, so that it may be sent
	// again after a rate limit or a transient failure.
	retry bool
}

// response is an answer of GitHub: its status, headers and bounded body.
type response struct {
	status int
	header http.Header
	body   []byte
}

// fail returns the error of req: the method and the path, the error of the
// port it wraps, and a detail that holds no secret.
func fail(req request, sentinel error, detail string) error {
	return fmt.Errorf("github: %s %s: %w: %s", req.method, req.path, sentinel, detail)
}

// do sends req and returns the answer of GitHub, whatever its status, except
// a rate limit, a server error or a network failure, which are retried when
// req allows it and give ErrUnavailable once the attempts or the wait budget
// are spent.
func (a *App) do(ctx context.Context, req request) (*response, error) {
	var waited time.Duration
	delay := backoff
	for attempt := 1; ; attempt++ {
		r, err := a.send(ctx, req)
		var wait time.Duration
		var cause string
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, fmt.Errorf("github: %s %s: %w: %w", req.method, req.path, identity.ErrUnavailable, ctx.Err())
			}
			var big *tooLarge
			if errors.As(err, &big) {
				return nil, fail(req, identity.ErrUnavailable, err.Error())
			}
			wait, cause = delay, err.Error()
		case rateLimited(r):
			wait, cause = a.rateLimitWait(r), "rate limited"
		case r.status >= 500:
			wait, cause = delay, "server error "+strconv.Itoa(r.status)
			if d, ok := retryAfter(r); ok {
				wait = d
			}
		default:
			return r, nil
		}
		if !req.retry || attempt == maxAttempts || wait > maxWait || waited+wait > waitBudget {
			return nil, fail(req, identity.ErrUnavailable, cause)
		}
		if err := a.wait(ctx, wait); err != nil {
			return nil, fmt.Errorf("github: %s %s: %w: %w", req.method, req.path, identity.ErrUnavailable, err)
		}
		waited += wait
		delay *= 2
	}
}

// tooLarge is a response body over maxBody.
type tooLarge struct{}

func (*tooLarge) Error() string { return fmt.Sprintf("response larger than %d bytes", maxBody) }

// send sends req once.
func (a *App) send(ctx context.Context, req request) (*response, error) {
	var payload io.Reader
	contentType := ""
	switch {
	case req.form != nil:
		payload = strings.NewReader(req.form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case req.body != nil:
		b, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("encode the request: %w", err)
		}
		payload = bytes.NewReader(b)
		contentType = "application/json"
	}
	hr, err := http.NewRequestWithContext(ctx, req.method, req.url, payload)
	if err != nil {
		return nil, errors.New("build the request")
	}
	if contentType != "" {
		hr.Header.Set("Content-Type", contentType)
	}
	if req.oauth {
		hr.Header.Set("Accept", "application/json")
	} else {
		hr.Header.Set("Accept", "application/vnd.github+json")
		hr.Header.Set("X-GitHub-Api-Version", apiVersion)
	}
	hr.Header.Set("User-Agent", userAgent)
	if req.auth != "" {
		hr.Header.Set("Authorization", req.auth)
	}
	resp, err := a.client.Do(hr)
	if err != nil {
		// The error of the transport names the address, never the body or
		// the headers; its cause alone is kept.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read the response: %w", err)
	}
	if len(body) > maxBody {
		return nil, &tooLarge{}
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// rateLimited reports whether r is a rate limit of GitHub: a 429, or a 403
// when the quota is spent or GitHub asks to wait.
func rateLimited(r *response) bool {
	if r.status == http.StatusTooManyRequests {
		return true
	}
	return r.status == http.StatusForbidden &&
		(r.header.Get("X-RateLimit-Remaining") == "0" || r.header.Get("Retry-After") != "")
}

// rateLimitWait is how long GitHub asks to wait after the rate limit r:
// Retry-After, else until X-RateLimit-Reset when the quota is spent, else the
// minute GitHub asks for after a secondary limit.
func (a *App) rateLimitWait(r *response) time.Duration {
	if d, ok := retryAfter(r); ok {
		return d
	}
	if r.header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(r.header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			d := time.Unix(reset, 0).Sub(a.now())
			return max(d, 0) + time.Second
		}
	}
	return secondaryWait
}

// retryAfter is the Retry-After header of r, in seconds.
func retryAfter(r *response) (time.Duration, bool) {
	s, err := strconv.ParseInt(r.header.Get("Retry-After"), 10, 64)
	if err != nil || s < 0 || s > int64(time.Hour/time.Second) {
		return 0, false
	}
	return time.Duration(s) * time.Second, true
}

// decode reads the JSON document of r into v. Fields GitHub adds are
// ignored; a malformed or truncated document, or a second one after it, is
// an error. The callers check the fields they require.
func decode(req request, r *response, v any) error {
	dec := json.NewDecoder(bytes.NewReader(r.body))
	if err := dec.Decode(v); err != nil {
		return fail(req, identity.ErrUnavailable, "malformed response")
	}
	if _, err := dec.Token(); err != io.EOF {
		return fail(req, identity.ErrUnavailable, "malformed response")
	}
	return nil
}

// status maps a status a caller did not expect to the error of the port: a
// refused token for a 401, a transient failure otherwise.
func status(req request, r *response) error {
	if r.status == http.StatusUnauthorized {
		return fail(req, identity.ErrUnauthorized, "status 401")
	}
	return fail(req, identity.ErrUnavailable, "unexpected status "+strconv.Itoa(r.status))
}

// apiGet is a GET of the API at path, with query, authorized by auth.
func (a *App) apiGet(path string, query url.Values, auth string) request {
	u := a.api + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return request{method: http.MethodGet, url: u, path: path, auth: auth, retry: true}
}

// pages walks the pages of the listing at path: each page is given to page,
// which checks its status and its document and returns the number of items
// it held, until a page that is not full. After maxPages full pages, one more
// is given to page as a probe, whose items page counts but never takes:
// empty, the listing held exactly maxPages pages; else it is longer, which is
// an error, never a list cut short.
func (a *App) pages(ctx context.Context, path string, query url.Values, auth string, page func(req request, r *response, probe bool) (int, error)) error {
	for n := 1; n <= maxPages+1; n++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(n))
		req := a.apiGet(path, q, auth)
		r, err := a.do(ctx, req)
		if err != nil {
			return err
		}
		probe := n == maxPages+1
		count, err := page(req, r, probe)
		if err != nil {
			return err
		}
		if probe {
			if count == 0 {
				return nil
			}
			break
		}
		if count < perPage {
			return nil
		}
	}
	return fail(request{method: http.MethodGet, path: path}, identity.ErrUnavailable, fmt.Sprintf("more than %d pages", maxPages))
}

// bearer is the Authorization header of a token.
func bearer(token identity.Secret) string {
	return "Bearer " + token.Reveal()
}
