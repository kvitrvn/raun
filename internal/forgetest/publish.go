package forgetest

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The publications of a forge, built by epic 5 (#34) and used by epic 7,
// whose context declares the port when it consumes them (epic 5 record).
// Until then these interfaces hold their contract: an adapter meets them
// with standard types alone, since it never imports this package.

// Statuses publishes the commit status of Raun.
type Statuses interface {
	// PublishStatus sets the status of context StatusContext on commit of
	// the repository with this stable identifier: state is one of the
	// four of StatusStates, description a sentence of MaxDescription
	// characters at most, target the absolute http or https address of
	// the page in Raun. A later status replaces an earlier one. Invalid
	// input is refused before anything is sent; a forge that cannot
	// answer gives identity.ErrUnavailable.
	PublishStatus(ctx context.Context, repository, commit, state, description, target string) error
}

// Comments publishes the one comment of Raun on a pull request.
type Comments interface {
	// PublishComment writes text on the pull request number of the
	// repository with this stable identifier, in the comment Raun wrote
	// earlier when there is one, found among the comments of Raun's own
	// identity by Marker on its first line, or in a new one. text is
	// plain: it is escaped, so it shows as written and holds no markup,
	// mention or marker of its own. It returns the identifier of the
	// comment. A forge that cannot answer gives identity.ErrUnavailable;
	// a listing that fails or is malformed writes nothing.
	PublishComment(ctx context.Context, repository string, number int64, text string) (string, error)
}

const (
	// StatusContext is the context of the status of Raun.
	StatusContext = "raun"
	// Marker is the first line of the comment of Raun, alone.
	Marker = "<!-- raun -->"
	// MaxDescription bounds the description of a status, in characters,
	// as GitHub does.
	MaxDescription = 140
	// MaxComment bounds the body of a comment, in bytes, under the 65536
	// characters GitHub takes; a text past it is cut and ends with
	// Truncated.
	MaxComment = 65000
	Truncated  = "\n\n(truncated)"
)

// StatusStates are the states of a status.
func StatusStates() []string { return []string{"pending", "success", "failure", "error"} }

// PublishedStatus is a status as the forge keeps it.
type PublishedStatus struct {
	Context     string
	State       string
	Description string
	Target      string
}

// PublishedComment is a comment of a pull request as the forge shows it:
// Raun reports whether Raun's own identity wrote it.
type PublishedComment struct {
	ID   string
	Raun bool
	Body string
}

// FakeBot is the login of the identity the fake publishes as, which no
// person of the fake has.
const FakeBot = "raun[bot]"

var (
	_ Statuses = (*Fake)(nil)
	_ Comments = (*Fake)(nil)
)

// CheckStatus refuses what no forge publishes: a state outside the four, a
// description past MaxDescription characters or not valid text, a target
// that is not an absolute http or https address.
func CheckStatus(state, description, target string) error {
	if !slices.Contains(StatusStates(), state) {
		return fmt.Errorf("status: %q is not a state", state)
	}
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > MaxDescription || strings.ContainsAny(description, "\r\n") {
		return fmt.Errorf("status: a description is one line of %d characters at most", MaxDescription)
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("status: the target is not an absolute http or https address")
	}
	return nil
}

// PublishStatus implements Statuses.
func (f *Fake) PublishStatus(_ context.Context, repo, commit, state, description, target string) error {
	if err := CheckStatus(state, description, target); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readable(repo); err != nil {
		return err
	}
	k := repo + "@" + commit
	if f.statuses[k] == nil {
		f.statuses[k] = map[string]PublishedStatus{}
	}
	f.statuses[k][StatusContext] = PublishedStatus{Context: StatusContext, State: state, Description: description, Target: target}
	return nil
}

// PublishComment implements Comments: the comment of FakeBot whose first
// line is Marker is updated, the oldest when there are several; any other
// is left alone.
func (f *Fake) PublishComment(_ context.Context, repo string, number int64, text string) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("comment: %d is not a pull request", number)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readable(repo); err != nil {
		return "", err
	}
	k := repo + "#" + strconv.FormatInt(number, 10)
	body := CommentBody(text)
	for i, c := range f.comments[k] {
		if c.Raun && Marked(c.Body) {
			f.comments[k][i].Body = body
			return c.ID, nil
		}
	}
	id := f.next("comment")
	f.comments[k] = append(f.comments[k], PublishedComment{ID: id, Raun: true, Body: body})
	return id, nil
}

// AddComment is someone else commenting body on the pull request number of
// the repository with this identifier.
func (f *Fake) AddComment(repo string, number int64, body string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := repo + "#" + strconv.FormatInt(number, 10)
	id := f.next("comment")
	f.comments[k] = append(f.comments[k], PublishedComment{ID: id, Body: body})
	return id
}

// Comments returns the comments of the pull request number of the
// repository with this identifier, oldest first.
func (f *Fake) Comments(repo string, number int64) []PublishedComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[repo+"#"+strconv.FormatInt(number, 10)])
}

// Statuses returns the statuses of commit of the repository with this
// identifier, by context.
func (f *Fake) Statuses(repo, commit string) []PublishedStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PublishedStatus
	for _, s := range f.statuses[repo+"@"+commit] {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b PublishedStatus) int { return strings.Compare(a.Context, b.Context) })
	return out
}

// Marked reports whether body is a comment of Raun's form: Marker alone on
// its first line.
func Marked(body string) bool {
	return body == Marker || strings.HasPrefix(body, Marker+"\n") || strings.HasPrefix(body, Marker+"\r\n")
}

// CommentBody is the body of the comment of Raun for text: Marker, then the
// text escaped, MaxComment bytes in all at most.
func CommentBody(text string) string {
	return Marker + "\n" + EscapeText(text, MaxComment-len(Marker)-1)
}

// EscapeText is plain text as Markdown that shows it as written, budget
// bytes at most: HTML is written as entities, so no tag nor comment, a
// marker included, survives; a mention or a reference to an issue is
// broken; the punctuation of Markdown, and math, is escaped; the spaces and
// tabs that start a line are written as non-breaking spaces, so no line
// becomes code, where nothing is escaped. Lines stay lines. A text that does
// not fit is cut between two characters and ends with Truncated. A forge
// adapter writes the same bytes; the contract suite checks it.
func EscapeText(text string, budget int) string {
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
		if b.Len()+len(piece) > budget-len(Truncated) {
			b.WriteString(Truncated)
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
