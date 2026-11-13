package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// tokenPath is the OAuth endpoint that gives and refreshes user tokens.
const tokenPath = "/login/oauth/access_token"

// AuthorizeURL implements identity.Forge: the authorization page of the App,
// with the state, the S256 challenge and the callback of the server.
func (a *App) AuthorizeURL(state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", a.clientID)
	q.Set("redirect_uri", a.callback)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return a.web + "/login/oauth/authorize?" + q.Encode()
}

// Exchange implements identity.Forge. GitHub refuses an unknown, used or
// expired code, or one that does not match the verifier, with
// bad_verification_code: ErrInvalidCode. The exchange is never sent twice:
// the code serves once.
func (a *App) Exchange(ctx context.Context, code, verifier string) (identity.Identity, identity.Tokens, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", a.callback)
	tokens, err := a.userTokens(ctx, form, map[string]error{
		"bad_verification_code": identity.ErrInvalidCode,
		"invalid_grant":         identity.ErrInvalidCode,
	})
	if err != nil {
		return identity.Identity{}, identity.Tokens{}, err
	}
	id, err := a.user(ctx, tokens)
	if err != nil {
		return identity.Identity{}, identity.Tokens{}, err
	}
	return id, tokens, nil
}

// Refresh implements identity.Forge. GitHub refuses an expired, used or
// revoked refresh token with bad_refresh_token: ErrRevoked. A refresh is
// never sent twice: the refresh token serves once.
func (a *App) Refresh(ctx context.Context, t identity.Tokens) (identity.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh.Reveal())
	return a.userTokens(ctx, form, map[string]error{
		"bad_refresh_token": identity.ErrRevoked,
		"invalid_grant":     identity.ErrRevoked,
	})
}

// userTokens posts form to the token endpoint, authenticated as the App, and
// returns the tokens GitHub gives. refused maps the OAuth errors that refuse
// the code or the refresh token to the error of the port; any other OAuth
// error is ErrUnavailable.
func (a *App) userTokens(ctx context.Context, form url.Values, refused map[string]error) (identity.Tokens, error) {
	form.Set("client_id", a.clientID)
	form.Set("client_secret", a.clientSecret.Reveal())
	req := request{method: http.MethodPost, url: a.web + tokenPath, path: tokenPath, form: form, oauth: true}
	// The expiries count from before the request, so they are never late.
	now := a.now()
	r, err := a.do(ctx, req)
	if err != nil {
		return identity.Tokens{}, err
	}
	if r.status != http.StatusOK {
		return identity.Tokens{}, fail(req, identity.ErrUnavailable, "unexpected status "+strconv.Itoa(r.status))
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshToken     string `json:"refresh_token"`
		RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
		Error            string `json:"error"`
	}
	if err := decode(req, r, &out); err != nil {
		return identity.Tokens{}, err
	}
	if out.Error != "" {
		if sentinel, ok := refused[out.Error]; ok {
			return identity.Tokens{}, fail(req, sentinel, out.Error)
		}
		return identity.Tokens{}, fail(req, identity.ErrUnavailable, "OAuth error "+oauthError(out.Error))
	}
	switch {
	case out.AccessToken == "":
		return identity.Tokens{}, fail(req, identity.ErrUnavailable, "malformed response")
	case out.RefreshToken == "" && out.ExpiresIn == 0:
		// What GitHub gives when the App opts out of expiring user tokens.
		return identity.Tokens{}, fail(req, identity.ErrUnavailable, "the user token does not expire: enable the expiration of user authorization tokens in the settings of the App")
	case out.RefreshToken == "" || out.ExpiresIn <= 0 || out.RefreshExpiresIn <= 0:
		return identity.Tokens{}, fail(req, identity.ErrUnavailable, "malformed response")
	}
	return identity.Tokens{
		Access:        identity.Secret(out.AccessToken),
		AccessExpiry:  now.Add(time.Duration(out.ExpiresIn) * time.Second),
		Refresh:       identity.Secret(out.RefreshToken),
		RefreshExpiry: now.Add(time.Duration(out.RefreshExpiresIn) * time.Second),
	}, nil
}

// oauthError returns code when it is an OAuth error code, letters and
// underscores, so that an error never carries text GitHub did not mean as a
// code.
func oauthError(code string) string {
	if len(code) > 64 {
		return "(invalid)"
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && c != '_' {
			return "(invalid)"
		}
	}
	return code
}

// user returns the identity of the person of t.
func (a *App) user(ctx context.Context, t identity.Tokens) (identity.Identity, error) {
	req := a.apiGet("/user", nil, bearer(t.Access))
	r, err := a.do(ctx, req)
	if err != nil {
		return identity.Identity{}, err
	}
	if r.status != http.StatusOK {
		return identity.Identity{}, status(req, r)
	}
	var out struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := decode(req, r, &out); err != nil {
		return identity.Identity{}, err
	}
	if out.ID <= 0 || out.Login == "" {
		return identity.Identity{}, fail(req, identity.ErrUnavailable, "malformed response")
	}
	return identity.Identity{Forge: Name, ID: strconv.FormatInt(out.ID, 10), Login: out.Login, Name: out.Name}, nil
}
