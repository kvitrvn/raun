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
	Blocked                                   bool
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
		d := &decisionModel{ID: r.PathValue("id"), CSRF: h.csrf}
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
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", itemURL(d.ID))
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, itemURL(d.ID), http.StatusSeeOther)
	}
}

func (h *handler) parseDecision(w http.ResponseWriter, r *http.Request, d *decisionModel) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return http.StatusUnprocessableEntity, errors.New("form: expected application/x-www-form-urlencoded")
	}
	err = r.ParseForm()
	d.Author, d.Reason, d.Revision = r.PostForm.Get("author"), r.PostForm.Get("reason"), r.PostForm.Get("revision")
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
		case "csrf", "revision", "author", "reason":
		default:
			return http.StatusUnprocessableEntity, fmt.Errorf("form.%s: unknown field", field)
		}
		if len(r.PostForm[field]) != 1 {
			return http.StatusUnprocessableEntity, fmt.Errorf("form.%s: duplicate field", field)
		}
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
	m := pageModel{Title: "Cannot record decision", Decision: d}
	items, loadErr := h.load()
	if loadErr == nil {
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
