package main

import (
	"github.com/kvitrvn/raun/internal/adapter/driven/github"
)

// newForge builds the GitHub App of cfg, or returns nil when cfg has none.
// It reaches no network: an invalid App stops the server at start, before
// anything listens.
func newForge(cfg config) (*github.App, error) {
	if cfg.github.appID == 0 {
		return nil, nil
	}
	return github.New(github.Options{
		WebURL:       cfg.github.webURL,
		AppID:        cfg.github.appID,
		ClientID:     cfg.github.clientID,
		ClientSecret: cfg.github.clientSecret,
		PrivateKey:   cfg.github.privateKey,
		CallbackURL:  cfg.publicURL.JoinPath("/login/callback").String(),
	})
}
