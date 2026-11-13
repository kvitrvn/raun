package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// IsMember implements identity.Memberships: whether the person of t is an
// active member of the organization whose stable identifier is org. A
// pending invitation does not count, and neither does a login: only the
// identifier. GitHub shows a user token the organizations the App is
// installed on, so an organization without the App is never one the person
// belongs to.
func (a *App) IsMember(ctx context.Context, t identity.Tokens, org string) (bool, error) {
	id, err := strconv.ParseInt(org, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != org {
		return false, nil
	}
	member := false
	err = a.pages(ctx, "/user/memberships/orgs", url.Values{"state": {"active"}}, bearer(t.Access), func(req request, r *response) (int, error) {
		if member {
			return 0, nil
		}
		if r.status != http.StatusOK {
			return 0, status(req, r)
		}
		var out []struct {
			State        string `json:"state"`
			Organization struct {
				ID int64 `json:"id"`
			} `json:"organization"`
		}
		if err := decode(req, r, &out); err != nil {
			return 0, err
		}
		for _, m := range out {
			if m.Organization.ID == id && m.State == "active" {
				member = true
				return 0, nil
			}
		}
		return len(out), nil
	})
	if err != nil {
		return false, err
	}
	return member, nil
}
