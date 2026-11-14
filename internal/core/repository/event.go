package repository

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrSignature: the delivery is not signed by the forge, whatever the
	// reason: no signature, a malformed one, or one that does not match.
	ErrSignature = errors.New("delivery not signed by the forge")
	// ErrMalformed: the delivery is signed, but its headers or its body
	// are not those of a delivery of the forge.
	ErrMalformed = errors.New("malformed delivery")
	// ErrDuplicate: the store knows the identifier or the body of the
	// delivery already.
	ErrDuplicate = errors.New("delivery already received")
	// ErrNoEvents: the forge cannot verify and parse deliveries.
	ErrNoEvents = errors.New("the forge cannot receive events")
)

// EventRetention is how long the keys of a delivery and the rows of the
// event log are kept, until the epic 13 review decides the retention of
// events.
const EventRetention = 30 * 24 * time.Hour

// LogLimit is how many rows of its event log the page of a repository
// shows, newest first.
const LogLimit = 50

// Kind is what a delivery is, once the forge parsed it.
type Kind string

const (
	// KindPush: a push of a branch or a tag.
	KindPush Kind = "push"
	// KindPullRequest: a pull request opened, reopened, synchronized,
	// ready for review or closed.
	KindPullRequest Kind = "pull_request"
	// KindInstallation: the App was installed, removed or changed: Raun
	// asks the forge what it reaches, never the payload.
	KindInstallation Kind = "installation"
	// KindIgnored: any other event or action.
	KindIgnored Kind = "ignored"
)

// Sender is the person of the forge who caused an event: the stable
// identifier the forge gives them, and their login at the time.
type Sender struct {
	ID    string
	Login string
}

// Push is a push of a branch or a tag.
type Push struct {
	// Ref is the full ref: refs/heads/main or refs/tags/v1.
	Ref string
	// Tag: the ref is a tag; otherwise a branch.
	Tag bool
	// Before and After are the commits the ref pointed to before and
	// after; a deleted ref has no After that exists.
	Before, After string
	Forced        bool
	Deleted       bool
	// Head is the head commit of the push; empty for a deleted ref.
	Head   string
	Sender Sender
}

// PullRequest is an event of a pull request.
type PullRequest struct {
	Number int64
	// Action is opened, reopened, synchronize, ready_for_review or closed.
	Action     string
	HeadRef    string
	HeadCommit string
	BaseRef    string
	BaseCommit string
	// HeadRepository and BaseRepository are the stable identifiers of the
	// repositories of the head and the base; HeadRepository is empty when
	// the forge no longer knows the head repository.
	HeadRepository string
	BaseRepository string
	Draft          bool
	Sender         Sender
}

// Fork reports whether the pull request comes from a fork: its head
// repository is not its base repository. A head repository the forge no
// longer knows counts as a fork, the stricter answer.
func (p PullRequest) Fork() bool {
	return p.HeadRepository == "" || p.HeadRepository != p.BaseRepository
}

// Delivery is a delivery of the forge the forge signed, parsed: its
// identifier, the event and action the forge named, the stable identifier
// of its repository when it has one, and its normalized content.
type Delivery struct {
	Forge string
	// ID is the identifier the forge gives the delivery; the forge sends
	// it again with a delivery sent again.
	ID     string
	Event  string
	Action string
	Kind   Kind
	// Repository is the stable identifier of the repository of the
	// event, empty for an event of no repository.
	Repository  string
	Push        *Push
	PullRequest *PullRequest
}

// Events is the optional capability of a forge that verifies and parses its
// deliveries. On GitHub it holds the webhook of the App, one per instance,
// which covers every repository the App reaches: no repository needs a
// webhook of its own, so it stands for the Webhooks capability too.
type Events interface {
	// Parse verifies that the forge signed body, with header giving the
	// headers of the request, and parses it. ErrSignature for a delivery
	// it did not sign, the same error whatever the reason, checked before
	// anything parses the body; ErrMalformed for a signed delivery that is
	// not one of the forge.
	Parse(header func(name string) string, body []byte) (Delivery, error)
}

// Result is what came of a delivery in the event log.
type Result string

const (
	// ResultRecorded: a kept event of an active repository.
	ResultRecorded Result = "recorded"
	// ResultIgnored: an event or an action Raun does not react to.
	ResultIgnored Result = "ignored"
)

// LoggedEvent is a row of the event log of a repository: one delivery of
// it while it was active.
type LoggedEvent struct {
	Forge      string
	Repository string
	Delivery   string
	Event      string
	Action     string
	// Ref is the ref of a push, the head ref of a pull request.
	Ref string
	// Before and After are the commits of a push, or the base and head
	// commits of a pull request.
	Before, After string
	ReceivedAt    time.Time
	Result        Result
}

// Key is what makes a delivery the same as another one: its identifier, and
// the SHA-256 of its raw body in hexadecimal, since the identifier is not
// signed and a body sent again under a new one is a replay.
type Key struct {
	Forge      string
	Delivery   string
	Digest     string
	ReceivedAt time.Time
}

// EventStore keeps the keys of every delivery and the event log of the
// repositories. Every store passes the contract suite of package
// repositorytest.
type EventStore interface {
	// Record keeps k and, when row is not nil, row in the event log when
	// the repository of row is active then, in one operation: it reports
	// whether the row was logged. ErrDuplicate when the forge of k has a
	// delivery with its identifier or its digest already; nothing changes
	// then, nor on any other error.
	Record(ctx context.Context, k Key, row *LoggedEvent) (bool, error)
	// Log returns the newest rows of the event log of the repository of
	// forge with this identifier, newest first, limit at most.
	Log(ctx context.Context, forge, repository string, limit int) ([]LoggedEvent, error)
	// PurgeEvents deletes the keys and the rows received before before,
	// and returns how many keys and rows it deleted.
	PurgeEvents(ctx context.Context, before time.Time) (int, error)
}

// Outcome is what came of a delivery Raun received.
type Outcome string

const (
	// OutcomeRecorded: a kept event of an active repository, logged.
	OutcomeRecorded Outcome = "recorded"
	// OutcomeIgnored: an event or an action Raun does not react to,
	// logged for an active repository.
	OutcomeIgnored Outcome = "ignored"
	// OutcomeDropped: an event of no repository, or of one that is not
	// active: only its keys are kept.
	OutcomeDropped Outcome = "dropped"
	// OutcomeInstallation: the App changed; the caller asks the forge
	// what it reaches, after the answer.
	OutcomeInstallation Outcome = "installation"
	// OutcomeDuplicate: a delivery sent again, or a body replayed: nothing
	// more happens.
	OutcomeDuplicate Outcome = "duplicate"
)
