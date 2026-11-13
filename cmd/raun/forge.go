package main

import (
	"errors"
	"fmt"

	"github.com/kvitrvn/raun/internal/adapter/driven/github"
	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/core/identity"
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

// buildForge returns the forge of the server: the one of deps in tests, else
// the GitHub App of cfg, or nil without one. A forge that cannot tell the
// members of an organization while RAUN_SIGNUP_ORGS names one of its
// organizations stops the server.
func buildForge(cfg config, deps serverDeps) (identity.Forge, error) {
	var forge identity.Forge
	if deps.forge != nil {
		forge = deps.forge
	} else {
		app, err := newForge(cfg)
		if err != nil {
			return nil, err
		}
		if app == nil {
			return nil, nil
		}
		forge = app
	}
	if _, ok := identity.MembershipsOf(forge); !ok {
		for _, org := range cfg.signup.Orgs {
			if org.Forge == forge.Name() {
				return nil, fmt.Errorf("RAUN_SIGNUP_ORGS: %s names an organization of %s, which cannot tell the members of one", org, forge.Name())
			}
		}
	}
	return forge, nil
}

// keyring is the keyring of the secret keys of cfg, or nil without one.
func keyring(cfg config) (*sqlite.Keyring, error) {
	if cfg.secretKey == nil {
		if cfg.previousKey != nil {
			return nil, errors.New("RAUN_SECRET_KEY_PREVIOUS: set without RAUN_SECRET_KEY")
		}
		return nil, nil
	}
	return sqlite.NewKeyring(cfg.secretKey, cfg.previousKey)
}
