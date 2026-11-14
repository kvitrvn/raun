package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The status of Raun on a commit (epic 5 record): a commit status, not a
// check run, so the App needs no permission beyond Commit statuses.
const (
	statusContext  = "raun"
	maxDescription = 140
)

// statusStates are the states GitHub gives a commit status.
var statusStates = []string{"pending", "success", "failure", "error"}

// writeID parses the stable identifier of a repository to write to.
func writeID(repo string) (int64, error) {
	n, ok := parseRepositoryID(repo)
	if !ok {
		return 0, fmt.Errorf("github: %q is not the identifier of a GitHub repository", repo)
	}
	return n, nil
}

// checkStatus refuses what GitHub would refuse or what Raun never sends.
func checkStatus(commit, state, description, target string) error {
	if !isCommit(commit) {
		return errors.New("github: status: not a commit")
	}
	if !slices.Contains(statusStates, state) {
		return fmt.Errorf("github: status: %q is not a state", state)
	}
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > maxDescription || strings.ContainsAny(description, "\r\n") {
		return fmt.Errorf("github: status: a description is one line of %d characters at most", maxDescription)
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return errors.New("github: status: the target is not an absolute http or https address")
	}
	return nil
}

// PublishStatus sets the commit status of context raun on commit of the
// repository with this stable identifier, with a token of that repository
// alone that writes statuses only. The request is sent once: GitHub keeps
// the last status of a context, and a repeat is harmless but not needed.
func (a *App) PublishStatus(ctx context.Context, repo, commit, state, description, target string) error {
	id, err := writeID(repo)
	if err != nil {
		return err
	}
	if err := checkStatus(commit, state, description, target); err != nil {
		return err
	}
	s, err := a.repositoryScope(ctx, id, map[string]string{"statuses": "write"})
	if err != nil {
		return err
	}
	path := "/repositories/" + strconv.FormatInt(id, 10) + "/statuses/" + commit
	var req request
	r, err := a.asInstallation(ctx, s, func(auth string) request {
		req = request{
			method: http.MethodPost, url: a.api + path, path: path, auth: auth,
			body: struct {
				State       string `json:"state"`
				Context     string `json:"context"`
				Description string `json:"description"`
				Target      string `json:"target_url"`
			}{state, statusContext, description, target},
		}
		return req
	})
	if err != nil {
		return err
	}
	if r.status != http.StatusCreated {
		return status(req, r)
	}
	return nil
}
