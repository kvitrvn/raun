package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
)

var _ repository.Files = (*App)(nil)

// repositoryID is the stable identifier id as GitHub numbers it.
func repositoryID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return 0, fmt.Errorf("github: %q is not the identifier of a GitHub repository: %w", id, repository.ErrNoFile)
	}
	return n, nil
}

// readScope finds the installation of the App that reaches the repository
// id, with the JWT of the App unless an earlier read found it, and returns
// the narrowest scope that reads it: that repository alone, contents read.
func (a *App) readScope(ctx context.Context, id int64) (scope, error) {
	if installation, ok := a.installs.get(id); ok {
		return readScopeOf(installation, id), nil
	}
	jwt, err := a.jwt()
	if err != nil {
		return scope{}, fmt.Errorf("github: installation of a repository: %w: %w", identity.ErrUnavailable, err)
	}
	path := "/repositories/" + strconv.FormatInt(id, 10) + "/installation"
	req := a.apiGet(path, nil, "Bearer "+jwt)
	r, err := a.do(ctx, req)
	if err != nil {
		return scope{}, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return scope{}, fail(req, identity.ErrUnavailable, "the App does not reach the repository")
	case http.StatusUnauthorized:
		return scope{}, fail(req, identity.ErrUnavailable, "GitHub refuses the JWT of the App: check RAUN_GITHUB_APP_ID, the private key and the clock")
	default:
		return scope{}, status(req, r)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := decode(req, r, &out); err != nil {
		return scope{}, err
	}
	if out.ID <= 0 {
		return scope{}, fail(req, identity.ErrUnavailable, "malformed response")
	}
	a.installs.put(id, out.ID)
	return readScopeOf(out.ID, id), nil
}

// readScopeOf is the scope that reads the repository id in installation.
func readScopeOf(installation, id int64) scope {
	return scope{installation: installation, repositories: []int64{id}, permissions: map[string]string{"contents": "read"}}
}

// asReader sends the GET of path with a token of the narrowest scope that
// reads the repository id.
func (a *App) asReader(ctx context.Context, id int64, path string, query url.Values) (request, *response, error) {
	s, err := a.readScope(ctx, id)
	if err != nil {
		return request{}, nil, err
	}
	var req request
	r, err := a.asInstallation(ctx, s, func(auth string) request {
		req = a.apiGet(path, query, auth)
		return req
	})
	return req, r, err
}

// Head implements repository.Files: the commit at the head of branch.
func (a *App) Head(ctx context.Context, repo, branch string) (string, error) {
	id, err := repositoryID(repo)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", repository.ErrNoFile
	}
	path := "/repositories/" + strconv.FormatInt(id, 10) + "/commits/" + url.PathEscape(branch)
	req, r, err := a.asReader(ctx, id, path, nil)
	if err != nil {
		return "", err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict:
		// No such branch, or a repository without a commit.
		return "", fmt.Errorf("github: %s %s: %w", req.method, req.path, repository.ErrNoFile)
	default:
		return "", status(req, r)
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := decode(req, r, &out); err != nil {
		return "", err
	}
	if !isCommit(out.SHA) {
		return "", fail(req, identity.ErrUnavailable, "malformed response")
	}
	return out.SHA, nil
}

// Read implements repository.Files: the file at path at commit, through the
// contents API, limit bytes at most. Its size is checked before its content
// is decoded. A directory, a submodule or a link GitHub does not resolve is
// not a file; GitHub resolves a link to a file of the same repository and
// gives that file.
func (a *App) Read(ctx context.Context, repo, commit, path string, limit int64) ([]byte, error) {
	id, err := repositoryID(repo)
	if err != nil {
		return nil, err
	}
	if !isCommit(commit) {
		return nil, fmt.Errorf("github: %q is not a commit: %w", commit, repository.ErrNoFile)
	}
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("github: %q is not a path from the root: %w", path, repository.ErrNoFile)
		}
		segments[i] = url.PathEscape(s)
	}
	endpoint := "/repositories/" + strconv.FormatInt(id, 10) + "/contents/" + strings.Join(segments, "/")
	req, r, err := a.asReader(ctx, id, endpoint, url.Values{"ref": {commit}})
	if err != nil {
		return nil, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("github: %s %s: %w", req.method, req.path, repository.ErrNoFile)
	default:
		return nil, status(req, r)
	}
	// A directory is listed as an array.
	if body := strings.TrimSpace(string(r.body)); strings.HasPrefix(body, "[") {
		return nil, fmt.Errorf("github: %s %s: a directory: %w", req.method, req.path, repository.ErrNoFile)
	}
	var out struct {
		Type     string `json:"type"`
		Size     *int64 `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := decode(req, r, &out); err != nil {
		return nil, err
	}
	switch out.Type {
	case "file":
	case "dir", "symlink", "submodule":
		return nil, fmt.Errorf("github: %s %s: a %s: %w", req.method, req.path, out.Type, repository.ErrNoFile)
	default:
		// An answer of another shape says nothing of the file.
		return nil, fail(req, identity.ErrUnavailable, "malformed response")
	}
	if out.Size == nil || *out.Size < 0 {
		return nil, fail(req, identity.ErrUnavailable, "malformed response")
	}
	if *out.Size > limit {
		return nil, fmt.Errorf("github: %s %s: %w", req.method, req.path, repository.ErrFileTooLarge)
	}
	if out.Encoding != "base64" {
		return nil, fail(req, identity.ErrUnavailable, "malformed response")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil || int64(len(content)) != *out.Size {
		return nil, fail(req, identity.ErrUnavailable, "malformed response")
	}
	return content, nil
}
