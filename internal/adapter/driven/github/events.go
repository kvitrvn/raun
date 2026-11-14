package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/core/repository"
)

// Headers of a webhook delivery of GitHub.
const (
	headerSignature = "X-Hub-Signature-256"
	headerEvent     = "X-GitHub-Event"
	headerDelivery  = "X-GitHub-Delivery"
)

// Bounds of the headers of a delivery: GitHub sends a GUID and an event name
// of a few words.
const (
	maxDeliveryID = 128
	maxEventName  = 64
)

var _ repository.Events = (*App)(nil)

// Parse implements repository.Events: it checks the HMAC-SHA256 of body
// under the webhook secret of the App against X-Hub-Signature-256, in
// constant time, before anything reads body, then parses the delivery. Every
// refused signature is repository.ErrSignature, without a detail: no
// signature, a malformed one, a wrong one, or no secret. A signed delivery
// whose headers or body are not those of GitHub is repository.ErrMalformed.
func (a *App) Parse(header func(string) string, body []byte) (repository.Delivery, error) {
	if !a.signed(header(headerSignature), body) {
		return repository.Delivery{}, repository.ErrSignature
	}
	id, event := header(headerDelivery), header(headerEvent)
	if !token(id, maxDeliveryID, func(r rune) bool { return isAlnum(r) || r == '-' }) {
		return repository.Delivery{}, fmt.Errorf("%w: the header %s", repository.ErrMalformed, headerDelivery)
	}
	if !token(event, maxEventName, func(r rune) bool { return r >= 'a' && r <= 'z' || r == '_' }) {
		return repository.Delivery{}, fmt.Errorf("%w: the header %s", repository.ErrMalformed, headerEvent)
	}
	d, err := parseDelivery(event, body)
	if err != nil {
		return repository.Delivery{}, fmt.Errorf("%w: %s event: %v", repository.ErrMalformed, event, err)
	}
	d.Forge, d.ID = Name, id
	return d, nil
}

// signed reports whether signature is sha256=<the HMAC-SHA256 of body
// under the webhook secret>, in hexadecimal.
func (a *App) signed(signature string, body []byte) bool {
	if a.webhookSecret.Reveal() == "" {
		return false
	}
	got, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	sum, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.webhookSecret.Reveal()))
	mac.Write(body)
	return hmac.Equal(sum, mac.Sum(nil))
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// token reports whether s has 1 to max characters, each allowed.
func token(s string, max int, allowed func(rune) bool) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if !allowed(r) {
			return false
		}
	}
	return true
}

// isCommit reports whether s is the name of a commit: 40 hexadecimal digits,
// or 64 for a repository in SHA-256.
func isCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// The parts of a payload Raun reads: the decoding ignores the others, as for
// an answer of the API (epic 4 record).
type (
	hookRepository struct {
		ID int64 `json:"id"`
	}
	hookUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	hookSide struct {
		Ref  string          `json:"ref"`
		SHA  string          `json:"sha"`
		Repo *hookRepository `json:"repo"`
	}
	hookPayload struct {
		Action     string          `json:"action"`
		Repository *hookRepository `json:"repository"`
		Sender     *hookUser       `json:"sender"`
		// A push.
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Forced     bool   `json:"forced"`
		Deleted    bool   `json:"deleted"`
		HeadCommit *struct {
			ID string `json:"id"`
		} `json:"head_commit"`
		// A pull request.
		Number      int64 `json:"number"`
		PullRequest *struct {
			Draft bool     `json:"draft"`
			Head  hookSide `json:"head"`
			Base  hookSide `json:"base"`
		} `json:"pull_request"`
	}
)

// keptActions are the actions of a pull request Raun keeps.
var keptActions = map[string]bool{"opened": true, "reopened": true, "synchronize": true, "ready_for_review": true, "closed": true}

func ghID(n int64) string { return strconv.FormatInt(n, 10) }

func sender(u *hookUser) repository.Sender {
	if u == nil || u.ID <= 0 {
		return repository.Sender{}
	}
	return repository.Sender{ID: ghID(u.ID), Login: u.Login}
}

// parseDelivery parses the body of a delivery of event.
func parseDelivery(event string, body []byte) (repository.Delivery, error) {
	var p hookPayload
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&p); err != nil {
		return repository.Delivery{}, fmt.Errorf("not a JSON object: %v", err)
	}
	if dec.More() {
		return repository.Delivery{}, fmt.Errorf("more than one JSON value")
	}
	d := repository.Delivery{Event: event, Action: p.Action, Kind: repository.KindIgnored}
	if p.Repository != nil {
		if p.Repository.ID <= 0 {
			return repository.Delivery{}, fmt.Errorf("repository without its identifier")
		}
		d.Repository = ghID(p.Repository.ID)
	}
	switch event {
	case "installation", "installation_repositories":
		d.Kind = repository.KindInstallation
	case "push":
		tag := strings.HasPrefix(p.Ref, "refs/tags/")
		if !tag && !strings.HasPrefix(p.Ref, "refs/heads/") {
			// A push of another ref, such as a note, is ignored.
			return d, nil
		}
		if d.Repository == "" || !isCommit(p.Before) || !isCommit(p.After) {
			return repository.Delivery{}, fmt.Errorf("push without its repository or its commits")
		}
		push := &repository.Push{Ref: p.Ref, Tag: tag, Before: p.Before, After: p.After, Forced: p.Forced, Deleted: p.Deleted, Sender: sender(p.Sender)}
		if p.HeadCommit != nil {
			if !isCommit(p.HeadCommit.ID) {
				return repository.Delivery{}, fmt.Errorf("push with a malformed head commit")
			}
			push.Head = p.HeadCommit.ID
		}
		d.Kind, d.Push = repository.KindPush, push
	case "pull_request":
		if !keptActions[p.Action] {
			return d, nil
		}
		pr := p.PullRequest
		if d.Repository == "" || p.Number <= 0 || pr == nil || pr.Base.Repo == nil || pr.Base.Repo.ID <= 0 ||
			pr.Head.Ref == "" || pr.Base.Ref == "" || !isCommit(pr.Head.SHA) || !isCommit(pr.Base.SHA) {
			return repository.Delivery{}, fmt.Errorf("pull request without its number, refs, commits or base repository")
		}
		out := &repository.PullRequest{
			Number: p.Number, Action: p.Action,
			HeadRef: pr.Head.Ref, HeadCommit: pr.Head.SHA, BaseRef: pr.Base.Ref, BaseCommit: pr.Base.SHA,
			BaseRepository: ghID(pr.Base.Repo.ID), Draft: pr.Draft, Sender: sender(p.Sender),
		}
		// The head repository of a fork deleted since is null.
		if pr.Head.Repo != nil && pr.Head.Repo.ID > 0 {
			out.HeadRepository = ghID(pr.Head.Repo.ID)
		}
		d.Kind, d.PullRequest = repository.KindPullRequest, out
	}
	return d, nil
}
