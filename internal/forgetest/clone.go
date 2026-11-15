package forgetest

import (
	"context"
	"fmt"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/job"
)

// CloneLifetime is how long a clone credential of the fake lasts, the
// longest GitHub gives an installation token.
const CloneLifetime = time.Hour

var _ job.Clone = (*Fake)(nil)

type fakeClone struct {
	repo    string
	expires time.Time
	revoked bool
}

// MintClone implements job.Clone: a credential of its own for each call,
// reading the one repository, for CloneLifetime.
func (f *Fake) MintClone(_ context.Context, repo string) (job.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readable(repo); err != nil {
		return job.Credential{}, err
	}
	if f.clones == nil {
		f.clones = map[string]*fakeClone{}
	}
	f.seq++
	secret := fmt.Sprintf("fake-clone-%d", f.seq)
	expires := f.clock().Add(CloneLifetime)
	f.clones[secret] = &fakeClone{repo: repo, expires: expires}
	full := f.repos[repo].repo.FullName
	u := "https://" + f.name + ".test/" + full + ".git"
	if f.cloneURL != nil {
		u = f.cloneURL(repo)
	}
	return job.Credential{
		URL: u, FullName: full,
		Username: "x-access-token", Password: job.Secret(secret), Expires: expires,
	}, nil
}

// RevokeClone implements job.Clone.
func (f *Fake) RevokeClone(_ context.Context, c job.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return identity.ErrUnavailable
	}
	if k, ok := f.clones[c.Password.Reveal()]; ok {
		k.revoked = true
	}
	return nil
}

// CloneReads reports whether the fake accepts c to read the repository
// with this identifier now: minted for it, neither revoked nor expired.
func (f *Fake) CloneReads(c job.Credential, repo string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.clones[c.Password.Reveal()]
	return ok && k.repo == repo && !k.revoked && f.clock().Before(k.expires)
}

// Clones returns how many clone credentials the fake minted, and how many
// of them it still accepts.
func (f *Fake) Clones() (minted, live int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.clones {
		minted++
		if !k.revoked && f.clock().Before(k.expires) {
			live++
		}
	}
	return minted, live
}

// SetCloneURL makes the clone credentials of the fake fetch the repository
// with this identifier from the address url gives, such as a fixture served
// on a loopback address, instead of https://<name>.test/<owner>/<name>.git.
func (f *Fake) SetCloneURL(url func(repo string) string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cloneURL = url
}

// ClonePassword reports whether password is the one of a clone credential
// the fake minted for the repository with this identifier and still
// accepts.
func (f *Fake) ClonePassword(password, repo string) bool {
	return f.CloneReads(job.Credential{Password: job.Secret(password)}, repo)
}
