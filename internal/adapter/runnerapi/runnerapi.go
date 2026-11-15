// Package runnerapi holds the protocol between raun runner and the server
// (docs/runner.md, Protocol): its versions, headers, paths, bounds and
// messages, shared by the runner API of the server and the client of the
// runner. It imports the standard library only.
package runnerapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Version is the protocol this release speaks; a change that breaks it is
// a new version (epic 6 record, Protocol).
const Version = 1

// spoken are the versions the server speaks, oldest first.
var spoken = [...]int{Version}

// Spoken returns the versions the server speaks, oldest first.
func Spoken() []int { return slices.Clone(spoken[:]) }

// Speaks reports whether the server speaks version v.
func Speaks(v int) bool { return slices.Contains(spoken[:], v) }

// SpokenText names the versions the server speaks, for an error.
func SpokenText() string {
	names := make([]string, len(spoken))
	for i, v := range spoken {
		names[i] = strconv.Itoa(v)
	}
	return strings.Join(names, ", ")
}

// Headers of the protocol.
const (
	// HeaderProtocol carries the version of every request.
	HeaderProtocol = "Raun-Runner-Protocol"
	// HeaderLease carries the lease token on the routes of a run.
	HeaderLease = "Raun-Lease"
	// UserAgentPrefix starts the User-Agent of a runner, followed by its
	// release.
	UserAgentPrefix = "raun-runner/"
)

// Prefix is the path of the runner API: every path below it is the API's.
const Prefix = "/api/runner/"

// Bounds of the bodies, by route (docs/runner.md). The body of a result is
// bounded by the data bound of its kind, plus ResultEnvelope.
const (
	MaxClaimBody     = 1 << 10
	MaxHeartbeatBody = 1 << 10
	MaxLogBody       = 512 << 10
	// ResultEnvelope is what a result body may hold besides its data: the
	// outcome and a summary of 1 KiB, escaped.
	ResultEnvelope = 8 << 10
)

// MaxLeaseToken bounds the Raun-Lease header, in bytes.
const MaxLeaseToken = 128

// ParsePath splits a path below Prefix into its version and the rest:
// "/api/runner/v1/claim" is 1 and "claim". ok is false for a path whose
// first segment is not v and a version number.
func ParsePath(path string) (version int, rest string, ok bool) {
	tail, found := strings.CutPrefix(path, Prefix)
	if !found {
		return 0, "", false
	}
	segment, rest, _ := strings.Cut(tail, "/")
	digits, found := strings.CutPrefix(segment, "v")
	if !found || digits == "" || len(digits) > 4 || strings.TrimLeft(digits, "0123456789") != "" || (digits[0] == '0' && digits != "0") {
		return 0, rest, false
	}
	v, err := strconv.Atoi(digits)
	return v, rest, err == nil
}

// ParseVersion reads the value of HeaderProtocol: a decimal version.
func ParseVersion(value string) (int, bool) {
	if value == "" || len(value) > 4 || strings.TrimLeft(value, "0123456789") != "" || (value[0] == '0' && value != "0") {
		return 0, false
	}
	v, err := strconv.Atoi(value)
	return v, err == nil
}

// Release reads the release of a runner from its User-Agent:
// raun-runner/<release>; empty when the header is not of that form.
func Release(userAgent string) string {
	r, ok := strings.CutPrefix(userAgent, UserAgentPrefix)
	if !ok {
		return ""
	}
	return r
}

// Empty is the body of a claim and of a heartbeat: {}.
type Empty struct{}

// Assignment answers a claim with a job.
type Assignment struct {
	Run      string    `json:"run"`
	Lease    Lease     `json:"lease"`
	Deadline time.Time `json:"deadline"`
	Job      Job       `json:"job"`
	Clone    Clone     `json:"clone"`
}

// Lease is the lease of a run: its token, given once, and its end.
type Lease struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

// Job is what a run does.
type Job struct {
	Kind       string     `json:"kind"`
	Repository Repository `json:"repository"`
	Commit     string     `json:"commit"`
	Ref        string     `json:"ref"`
	// Params are what the kind reads; an empty object for noop.
	Params json.RawMessage `json:"params"`
}

// Repository is the repository of a job, on its forge.
type Repository struct {
	Forge    string `json:"forge"`
	ID       string `json:"id"`
	FullName string `json:"full_name"`
}

// Clone is the clone credential of a run.
type Clone struct {
	URL      string    `json:"url"`
	Username string    `json:"username"`
	Password string    `json:"password"`
	Expires  time.Time `json:"expires"`
}

// Renewal answers a heartbeat: the new end of the lease, absent when the
// job is cancelled, and whether it is.
type Renewal struct {
	Expires time.Time `json:"expires,omitzero"`
	Cancel  bool      `json:"cancel"`
}

// Chunk is a numbered piece of the log of a run; both fields are required.
type Chunk struct {
	Seq  *int    `json:"seq"`
	Text *string `json:"text"`
}

// Result is what a run reports.
type Result struct {
	Outcome string          `json:"outcome"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error is the body of every refusal; it never holds a token. Expected is
// the number of the chunk the server expects, on a log refused for a
// skipped number.
type Error struct {
	Error    string `json:"error"`
	Expected *int   `json:"expected,omitempty"`
}

// ErrMalformed: a body is not one JSON object of the message, alone.
var ErrMalformed = errors.New("malformed body")

// Decode reads into v the one JSON object body holds, strictly: an unknown
// field, another value than an object, or anything after it is
// ErrMalformed. The caller bounds body.
func Decode(body []byte, v any) error {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: not a JSON object", ErrMalformed)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: something follows the object", ErrMalformed)
	}
	return nil
}
