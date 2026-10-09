package web

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/gitx"
	"github.com/kvitrvn/raun/internal/knowledge"
)

type decisionModel struct {
	ID, CSRF, Revision, Author, Reason, Error string
	// Next is the address shown after a successful decision.
	Next    string
	Blocked bool
	// next and filters are the validated Next.
	next    string
	filters filters
}

func canDecide(status knowledge.Status) bool {
	return status == knowledge.StatusProposed || status == knowledge.StatusContested || status == knowledge.StatusNeedsReview
}

func (h *handler) defaultAuthor(ctx context.Context) string {
	repo, err := gitx.Open(ctx, h.root)
	if err != nil {
		return ""
	}
	name, err := repo.ConfigValue(ctx, "user.name")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

func (h *handler) decide(to knowledge.Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.readOnly {
			h.render(w, r, http.StatusForbidden, pageModel{Title: "Read-only consultation", Error: "Human decisions are disabled on this server."})
			return
		}
		d := &decisionModel{ID: r.PathValue("id"), CSRF: h.csrf, filters: filters{Status: statusPending, Page: 1}}
		status, err := h.parseDecision(w, r, d)
		if err != nil {
			h.decisionError(w, r, d, status, err)
			return
		}
		err = h.store.Update(d.ID, d.Revision, func(it *knowledge.Item) error {
			if !canDecide(it.Status) {
				return fmt.Errorf("%w: decisions are unavailable for %s items", knowledge.ErrTransition, it.Status)
			}
			return it.Transition(to, knowledge.Actor{Kind: knowledge.ActorHuman, Name: strings.TrimSpace(d.Author)}, time.Now(), d.Reason)
		})
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, knowledge.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, knowledge.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, knowledge.ErrTransition):
				status = http.StatusUnprocessableEntity
			}
			h.decisionError(w, r, d, status, err)
			return
		}
		target := itemURL(d.ID)
		if d.next != "" {
			target = d.filters.url(d.next)
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func (h *handler) parseDecision(w http.ResponseWriter, r *http.Request, d *decisionModel) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return http.StatusUnprocessableEntity, errors.New("form: expected application/x-www-form-urlencoded")
	}
	err = r.ParseForm()
	d.Author, d.Reason, d.Revision, d.Next = r.PostForm.Get("author"), r.PostForm.Get("reason"), r.PostForm.Get("revision"), r.PostForm.Get("next")
	if err != nil {
		d.Blocked = true
		return http.StatusUnprocessableEntity, fmt.Errorf("form: cannot read submission (maximum 64 KiB): %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(h.csrf)) != 1 {
		return http.StatusForbidden, errors.New("csrf: invalid or expired form; reload the item before deciding")
	}
	if r.URL.RawQuery != "" {
		return http.StatusUnprocessableEntity, errors.New("form: query fields are not accepted")
	}
	for _, field := range slices.Sorted(maps.Keys(r.PostForm)) {
		switch field {
		case "csrf", "revision", "author", "reason", "next":
		default:
			return http.StatusUnprocessableEntity, fmt.Errorf("form.%s: unknown field", field)
		}
		if len(r.PostForm[field]) != 1 {
			return http.StatusUnprocessableEntity, fmt.Errorf("form.%s: duplicate field", field)
		}
	}
	if d.Next != "" {
		next, f, err := parseNext(d.Next)
		if err != nil {
			return http.StatusUnprocessableEntity, err
		}
		d.next, d.filters = next, f
	}
	revision, err := hex.DecodeString(d.Revision)
	if err != nil || len(revision) != 32 {
		d.Blocked = true
		return http.StatusUnprocessableEntity, errors.New("revision: expected a SHA-256 fingerprint; reload the item")
	}
	if strings.TrimSpace(d.Author) == "" {
		return http.StatusUnprocessableEntity, errors.New("author: required for a human decision")
	}
	if strings.TrimSpace(d.Reason) == "" {
		return http.StatusUnprocessableEntity, errors.New("reason: required for a human decision")
	}
	return http.StatusOK, nil
}

func (h *handler) decisionError(w http.ResponseWriter, r *http.Request, d *decisionModel, status int, err error) {
	d.Error = err.Error()
	d.Blocked = d.Blocked || status != http.StatusUnprocessableEntity
	m := pageModel{Title: "Cannot record decision", Decision: d, Filters: d.filters}
	items, loadErr := h.load()
	if loadErr == nil {
		m = listModel(items, d.filters)
		m.Title, m.Decision, m.Explicit = "Cannot record decision", d, true
		for _, it := range items {
			if it.ID == d.ID {
				m.Title, m.Item, m.Links = it.Title, it, relatedItems(it, items)
				d.Blocked = d.Blocked || !canDecide(it.Status)
				break
			}
		}
	} else {
		d.Error += "\n" + loadErr.Error()
		d.Blocked = true
	}
	if m.Item == nil {
		d.Blocked = true
	}
	// Errors must not replace the GET address with the action URL in HTMX history.
	w.Header().Set("HX-Push-Url", "false")
	h.render(w, r, status, m)
}
