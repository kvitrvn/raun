package github

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// Name is the name the server gives GitHub, the prefix of the identities of
// the configuration: github:583231.
const Name = "github"

// DefaultWebURL is the address of github.com.
const DefaultWebURL = "https://github.com"

// requestTimeout bounds one HTTP request of the default client.
const requestTimeout = 10 * time.Second

// Options configure an App.
type Options struct {
	// WebURL is the address of GitHub: DefaultWebURL when empty, or the
	// address of a GitHub Enterprise Server. CheckWebURL gives its rule.
	WebURL string
	// AppID is the identifier of the GitHub App.
	AppID int64
	// ClientID and ClientSecret authenticate the App's user authorization.
	ClientID     string
	ClientSecret identity.Secret
	// PrivateKey signs the JWT of the App; ParsePrivateKey reads it.
	PrivateKey *rsa.PrivateKey
	// WebhookSecret is the secret of the webhook of the App, one per
	// instance, which signs every delivery.
	WebhookSecret identity.Secret
	// CallbackURL is where GitHub sends a person back after they approve
	// Raun: <RAUN_PUBLIC_URL>/login/callback.
	CallbackURL string
	// HTTPClient sends the requests; nil is a client with a timeout of 10
	// seconds. Its redirects are never followed.
	HTTPClient *http.Client
	// Now is the clock of the expiries; nil is time.Now.
	Now func() time.Time
	// Wait waits d before a retry, or until ctx ends; nil sleeps.
	Wait func(ctx context.Context, d time.Duration) error
}

// App is a GitHub App, the forge adapter of GitHub. It is safe for
// concurrent use.
type App struct {
	appID        int64
	clientID     string
	clientSecret identity.Secret
	key          *rsa.PrivateKey
	// webhookSecret signs the deliveries of the webhook of the App.
	webhookSecret identity.Secret
	callback      string
	web           string // without a trailing slash
	api           string // without a trailing slash
	client        *http.Client
	now           func() time.Time
	wait          func(ctx context.Context, d time.Duration) error
	tokens        *tokenCache
}

var (
	_ identity.Forge       = (*App)(nil)
	_ identity.Memberships = (*App)(nil)
	_ repository.Reach     = (*App)(nil)
)

// CheckWebURL checks the address of a GitHub: an absolute https URL with a
// host, and no user, path, query or fragment. The configuration and New
// apply the same rule.
func CheckWebURL(raw string) error {
	if strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("%q has a query or a fragment", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("%q is not an https URL with a host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("%q holds a user", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%q has a path: GitHub is served at the root of its host", raw)
	}
	if strings.EqualFold(u.Hostname(), "github.com") && u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("%q: github.com is served on port 443 only", raw)
	}
	return nil
}

// CheckIdentifier checks that id is a stable identifier of GitHub, of a user
// or an organization: a positive number, never a login, which can be renamed
// and taken by someone else.
func CheckIdentifier(id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return fmt.Errorf("%q is not the stable identifier of a GitHub user or organization, a number such as 583231", id)
	}
	return nil
}

// New returns the App of opts. It checks opts and reaches no network.
func New(opts Options) (*App, error) {
	web := opts.WebURL
	if web == "" {
		web = DefaultWebURL
	}
	var errs []error
	if err := CheckWebURL(web); err != nil {
		errs = append(errs, fmt.Errorf("web URL: %w", err))
	}
	if opts.AppID <= 0 {
		errs = append(errs, errors.New("app ID: must be a positive number"))
	}
	if opts.ClientID == "" {
		errs = append(errs, errors.New("client ID: empty"))
	}
	if opts.ClientSecret.Reveal() == "" {
		errs = append(errs, errors.New("client secret: empty"))
	}
	if opts.PrivateKey == nil {
		errs = append(errs, errors.New("private key: missing"))
	}
	if opts.WebhookSecret.Reveal() == "" {
		errs = append(errs, errors.New("webhook secret: empty"))
	}
	if u, err := url.Parse(opts.CallbackURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("callback URL: %q is not an http or https URL", opts.CallbackURL))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("github: %w", errors.Join(errs...))
	}

	web = strings.TrimSuffix(web, "/")
	api := apiOf(web)
	client := &http.Client{Timeout: requestTimeout}
	if opts.HTTPClient != nil {
		c := *opts.HTTPClient
		client = &c
	}
	// A redirect is an answer like another: following it would send the
	// Authorization header or a form elsewhere.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	wait := opts.Wait
	if wait == nil {
		wait = sleep
	}
	return &App{
		appID:         opts.AppID,
		clientID:      opts.ClientID,
		clientSecret:  opts.ClientSecret,
		key:           opts.PrivateKey,
		webhookSecret: opts.WebhookSecret,
		callback:      opts.CallbackURL,
		web:           web,
		api:           api,
		client:        client,
		now:           now,
		wait:          wait,
		tokens:        newTokenCache(),
	}, nil
}

// apiOf returns the API address of the GitHub at web:
// https://api.github.com for github.com, <web>/api/v3 for a GitHub
// Enterprise Server.
func apiOf(web string) string {
	if u, err := url.Parse(web); err == nil && strings.EqualFold(u.Hostname(), "github.com") {
		return "https://api.github.com"
	}
	return web + "/api/v3"
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Name implements identity.Forge.
func (a *App) Name() string { return Name }
