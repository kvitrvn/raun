package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// commentMarker is the first line of the comment of Raun, alone: Raun finds
// its comment by it, among the comments of its own identity, and stores
// nothing to find it (epic 5 record).
const commentMarker = "<!-- raun -->"

// maxComment bounds the body of a comment, under the 65536 characters
// GitHub takes.
const maxComment = 65000

// truncated ends a text cut to fit a comment.
const truncated = "\n\n(truncated)"

// escapeText is plain text as Markdown that shows it as written, budget
// bytes at most: HTML is written as entities, so no tag nor comment, the
// marker included, survives; a mention or a reference to an issue is
// broken; the punctuation of Markdown, and math, is escaped; the spaces and
// tabs that start a line are written as non-breaking spaces, so no line
// becomes code, where nothing is escaped. Lines stay lines. A text that
// does not fit is cut between two characters and says so. The fake forge
// of internal/forgetest writes the same bytes, which the contract suite
// checks.
func escapeText(text string, budget int) string {
	var b strings.Builder
	start := true
	for _, r := range strings.ToValidUTF8(text, "\uFFFD") {
		var piece string
		switch {
		case start && r == ' ':
			piece = "\u00a0"
		case start && r == '\t':
			piece = "\u00a0\u00a0\u00a0\u00a0"
		default:
			piece = escapeRune(r)
		}
		if r != '\r' {
			start = r == '\n' || (start && (r == ' ' || r == '\t'))
		}
		if b.Len()+len(piece) > budget-len(truncated) {
			b.WriteString(truncated)
			return b.String()
		}
		b.WriteString(piece)
	}
	return b.String()
}

// escapeRune is r as Markdown that shows it as written.
func escapeRune(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '@':
		return "&#64;"
	case '\\', '`', '*', '_', '[', ']', '(', ')', '!', '#', '|', '~', '{', '}', '+', '-', '.', '=', ':', '$':
		return `\` + string(r)
	case '\r':
		return ""
	}
	return string(r)
}

// commentBody is the body of the comment of Raun for text.
func commentBody(text string) string {
	return commentMarker + "\n" + escapeText(text, maxComment-len(commentMarker)-1)
}

// marked reports whether body starts with the marker, alone on its line.
func marked(body string) bool {
	return body == commentMarker || strings.HasPrefix(body, commentMarker+"\n") || strings.HasPrefix(body, commentMarker+"\r\n")
}

// botLogin returns the login of the bot of the App, <slug>[bot], from GET
// /app under its JWT, kept once found. The lock is never held across the
// request: two first callers may both ask, and agree.
func (a *App) botLogin(ctx context.Context) (string, error) {
	a.bot.mu.Lock()
	login := a.bot.login
	a.bot.mu.Unlock()
	if login != "" {
		return login, nil
	}
	jwt, err := a.jwt()
	if err != nil {
		return "", fmt.Errorf("github: the App: %w: %w", identity.ErrUnavailable, err)
	}
	req := a.apiGet("/app", nil, "Bearer "+jwt)
	r, err := a.do(ctx, req)
	if err != nil {
		return "", err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return "", fail(req, identity.ErrUnavailable, "GitHub refuses the JWT of the App: check RAUN_GITHUB_APP_ID, the private key and the clock")
	default:
		return "", status(req, r)
	}
	var out struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := decode(req, r, &out); err != nil {
		return "", err
	}
	if out.ID != a.appID || !token(out.Slug, 64, func(r rune) bool { return isAlnum(r) || r == '-' }) {
		return "", fail(req, identity.ErrUnavailable, "malformed response")
	}
	login = out.Slug + "[bot]"
	a.bot.mu.Lock()
	a.bot.login = login
	a.bot.mu.Unlock()
	return login, nil
}

// botIdentity is the identity of the App as GitHub shows it on what it
// writes, found once.
type botIdentity struct {
	mu    sync.Mutex
	login string
}

// issueComment is a comment of a pull request as GitHub lists it.
type issueComment struct {
	ID   int64   `json:"id"`
	Body *string `json:"body"`
	User *struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	App *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
}

// ours reports whether c is a comment of Raun: written by the bot of the
// App, with the marker on its first line. The login <slug>[bot] is the
// App's alone: a person's login holds no bracket, and a slug names one App.
// A comment that names another App as its author is not Raun's either, nor
// a person writing through the authorization of Raun, who is not the bot.
func (a *App) ours(c issueComment, login string) bool {
	return c.User != nil && c.User.Type == "Bot" && c.User.Login == login &&
		(c.App == nil || c.App.ID == a.appID) && c.Body != nil && marked(*c.Body)
}

// PublishComment writes text, escaped, in the comment of Raun on the pull
// request number of the repository with this stable identifier: the oldest
// comment of Raun there when there are several, or a new one. It lists the
// comments first, all of them, and a listing that fails or is malformed
// writes nothing; the writes are sent once. Calls for one pull request run
// one at a time in this App, the adapter of one server.
func (a *App) PublishComment(ctx context.Context, repo string, number int64, text string) (string, error) {
	id, err := writeID(repo)
	if err != nil {
		return "", err
	}
	if number <= 0 {
		return "", fmt.Errorf("github: comment: %d is not a pull request", number)
	}
	unlock := a.commenting.lock(repo + "#" + strconv.FormatInt(number, 10))
	defer unlock()
	login, err := a.botLogin(ctx)
	if err != nil {
		return "", err
	}
	s, err := a.repositoryScope(ctx, id, map[string]string{"pull_requests": "write"})
	if err != nil {
		return "", err
	}
	token, err := a.installationToken(ctx, s)
	if err != nil {
		return "", err
	}
	base := "/repositories/" + strconv.FormatInt(id, 10) + "/issues/"
	listing := base + strconv.FormatInt(number, 10) + "/comments"
	var found int64
	err = a.pages(ctx, listing, nil, bearer(token), func(req request, r *response, probe bool) (int, error) {
		switch r.status {
		case http.StatusOK:
		case http.StatusNotFound:
			return 0, fail(req, identity.ErrUnavailable, "no such pull request")
		default:
			return 0, status(req, r)
		}
		var out *[]issueComment
		if err := decode(req, r, &out); err != nil {
			return 0, err
		}
		if out == nil {
			return 0, fail(req, identity.ErrUnavailable, "malformed response")
		}
		for _, c := range *out {
			// An entry without its author or its body could be Raun's:
			// deciding without it could write a second comment.
			if c.ID <= 0 || c.Body == nil || c.User == nil || c.User.Login == "" || c.User.Type == "" {
				return 0, fail(req, identity.ErrUnavailable, "malformed response")
			}
			if !probe && a.ours(c, login) && (found == 0 || c.ID < found) {
				found = c.ID
			}
		}
		return len(*out), nil
	})
	if err != nil {
		return "", err
	}
	body := struct {
		Body string `json:"body"`
	}{commentBody(text)}
	method, path, want := http.MethodPost, listing, http.StatusCreated
	if found != 0 {
		method, path, want = http.MethodPatch, base+"comments/"+strconv.FormatInt(found, 10), http.StatusOK
	}
	var req request
	r, err := a.asInstallation(ctx, s, func(auth string) request {
		req = request{method: method, url: a.api + path, path: path, auth: auth, body: body}
		return req
	})
	if err != nil {
		return "", err
	}
	if r.status != want {
		return "", status(req, r)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := decode(req, r, &out); err != nil {
		return "", err
	}
	if out.ID <= 0 || (found != 0 && out.ID != found) {
		return "", fail(req, identity.ErrUnavailable, "malformed response")
	}
	return strconv.FormatInt(out.ID, 10), nil
}

// keyedMutex holds one lock per key while it is held or waited on. Its
// zero value is ready.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*countedMutex
}

type countedMutex struct {
	sync.Mutex
	users int
}

// lock takes the lock of key and returns what releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*countedMutex{}
	}
	l, ok := k.locks[key]
	if !ok {
		l = &countedMutex{}
		k.locks[key] = l
	}
	l.users++
	k.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		l.users--
		if l.users == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
